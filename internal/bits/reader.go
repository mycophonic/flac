// Package bits provides bit access operations and binary decoding algorithms.
package bits

import (
	"errors"
	"fmt"
	"io"

	"github.com/mycophonic/flac/internal/hashutil/crc16"
	"github.com/mycophonic/flac/internal/hashutil/crc8"
)

const readBufSize = 4096

// A Reader handles bit reading operations. It maintains an internal read-ahead
// buffer to minimize calls to the underlying reader, and computes CRC-16 and
// CRC-8 checksums on consumed bytes inline.
type Reader struct {
	// Underlying reader.
	r io.Reader
	// Read-ahead buffer.
	buf [readBufSize]byte
	// Read position and valid byte count in buf.
	pos int
	end int
	// Between 0 and 7 buffered bits since previous read operations.
	x uint8
	// The number of buffered bits in x.
	n uint
	// CRC state — computed on bytes as they are consumed, not as they enter
	// the buffer. This allows read-ahead buffering without corrupting CRC.
	crc16   uint16
	crc8    uint8
	doCRC16 bool
	doCRC8  bool
}

// NewReader returns a new Reader that reads bits from r.
func NewReader(r io.Reader) *Reader {
	return &Reader{r: r}
}

// fill reads more data from the underlying reader into the buffer.
// It preserves any unread bytes by shifting them to the front.
func (br *Reader) fill() error {
	// Shift unread bytes to front.
	if br.pos > 0 {
		n := copy(br.buf[:], br.buf[br.pos:br.end])
		br.pos = 0
		br.end = n
	}

	// Read into remaining space.
	n, err := br.r.Read(br.buf[br.end:])
	br.end += n

	if n > 0 {
		return nil
	}

	return err
}

// available returns the number of buffered bytes available for reading.
func (br *Reader) available() int {
	return br.end - br.pos
}

// needBytes ensures at least n bytes are available in the buffer.
func (br *Reader) needBytes(n int) error {
	for br.available() < n {
		if err := br.fill(); err != nil {
			if br.available() >= n {
				return nil
			}

			// Partial data available but not enough — unexpected EOF.
			if br.available() > 0 && errors.Is(err, io.EOF) {
				return io.ErrUnexpectedEOF
			}

			return err
		}
	}

	return nil
}

// consumeBytes updates CRC checksums for bytes consumed from the buffer
// in the range [from, to).
func (br *Reader) consumeBytes(from, to int) {
	if from >= to {
		return
	}

	data := br.buf[from:to]

	if br.doCRC16 {
		br.crc16 = crc16.Update(br.crc16, crc16.IBMTable, data)
	}

	if br.doCRC8 {
		br.crc8 = crc8.Update(br.crc8, crc8.ATMTable, data)
	}
}

// EnableCRC16 resets and enables CRC-16 accumulation.
func (br *Reader) EnableCRC16() {
	br.crc16 = 0
	br.doCRC16 = true
}

// DisableCRC16 stops CRC-16 accumulation.
func (br *Reader) DisableCRC16() {
	br.doCRC16 = false
}

// CRC16 returns the accumulated CRC-16 value.
func (br *Reader) CRC16() uint16 {
	return br.crc16
}

// EnableCRC8 resets and enables CRC-8 accumulation.
func (br *Reader) EnableCRC8() {
	br.crc8 = 0
	br.doCRC8 = true
}

// DisableCRC8 stops CRC-8 accumulation.
func (br *Reader) DisableCRC8() {
	br.doCRC8 = false
}

// CRC8 returns the accumulated CRC-8 value.
func (br *Reader) CRC8() uint8 {
	return br.crc8
}

// FeedCRC updates active CRC checksums with the provided bytes. Use this to
// include bytes that were identified outside the normal Read path (e.g. frame
// sync bytes found during a zero-padding scan).
func (br *Reader) FeedCRC(data []byte) {
	if br.doCRC16 {
		br.crc16 = crc16.Update(br.crc16, crc16.IBMTable, data)
	}

	if br.doCRC8 {
		br.crc8 = crc8.Update(br.crc8, crc8.ATMTable, data)
	}
}

// Reset discards all buffered data and CRC state. Call after seeking the
// underlying reader to invalidate the read-ahead buffer.
func (br *Reader) Reset() {
	br.pos = 0
	br.end = 0
	br.x = 0
	br.n = 0
	br.crc16 = 0
	br.crc8 = 0
	br.doCRC16 = false
	br.doCRC8 = false
}

// Seek implements io.Seeker. It requires the underlying reader to also
// implement io.Seeker.
//
// For position queries (offset=0, whence=io.SeekCurrent), the returned
// position is the logical stream position: the underlying reader's position
// minus bytes buffered but not yet consumed. This accounts for read-ahead
// buffering transparently.
//
// For all other seeks, the internal buffer and bit state are reset before
// delegating to the underlying seeker. For relative seeks (SeekCurrent),
// the offset is adjusted to account for buffered data.
func (br *Reader) Seek(offset int64, whence int) (int64, error) {
	seeker, ok := br.r.(io.Seeker)
	if !ok {
		return 0, errors.New("bits.Reader.Seek: underlying reader does not implement io.Seeker")
	}

	// Position query: return logical position accounting for buffered data.
	if offset == 0 && whence == io.SeekCurrent {
		pos, err := seeker.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}

		return pos - int64(br.available()), nil
	}

	// For relative seeks, adjust offset to account for bytes that the
	// underlying reader has delivered but we haven't consumed yet.
	if whence == io.SeekCurrent {
		offset -= int64(br.available())
	}

	// Clear buffer and bit state before seeking.
	br.Reset()

	return seeker.Seek(offset, whence)
}

// IsAligned reports whether the reader is at a byte boundary (no buffered bits).
func (br *Reader) IsAligned() bool {
	return br.n == 0
}

// BitsBuffered returns the number of buffered bits remaining from the last
// partial byte read. This is between 0 and 7.
func (br *Reader) BitsBuffered() uint {
	return br.n
}

// ReadAligned reads exactly len(p) bytes into p. The reader must be
// byte-aligned (no buffered bits). This enables bulk reads for byte-aligned
// data like verbatim audio samples.
func (br *Reader) ReadAligned(p []byte) error {
	if br.n != 0 {
		return fmt.Errorf("bits.Reader.ReadAligned: reader not byte-aligned (%d buffered bits)", br.n)
	}

	// Drain from internal buffer first.
	avail := br.available()
	if avail > 0 {
		n := copy(p, br.buf[br.pos:br.end])
		br.consumeBytes(br.pos, br.pos+n)
		br.pos += n
		p = p[n:]
	}

	if len(p) == 0 {
		return nil
	}

	// Remaining data is larger than what we had buffered.
	// Read directly into the caller's buffer to avoid extra copies.
	_, err := io.ReadFull(br.r, p)
	if err != nil {
		return err
	}

	// CRC the directly-read bytes.
	if br.doCRC16 {
		br.crc16 = crc16.Update(br.crc16, crc16.IBMTable, p)
	}

	if br.doCRC8 {
		br.crc8 = crc8.Update(br.crc8, crc8.ATMTable, p)
	}

	return nil
}

// Read reads and returns the next n bits, at most 64. It buffers bits up to the
// next byte boundary.
func (br *Reader) Read(n uint) (x uint64, err error) {
	if n == 0 {
		return 0, nil
	}

	if n > 64 {
		return 0, fmt.Errorf("bit.Reader.Read: invalid number of bits; n (%d) exceeds 64", n)
	}

	// Read buffered bits.
	if br.n > 0 {
		switch {
		case br.n == n:
			br.n = 0

			return uint64(br.x), nil
		case br.n > n:
			br.n -= n
			mask := ^uint8(0) << br.n
			x = uint64(br.x&mask) >> br.n
			br.x &^= mask

			return x, nil
		}

		n -= br.n
		x = uint64(br.x)
		br.n = 0
	}

	// Calculate bytes needed.
	nBytes := n / 8
	nBits := n % 8

	if nBits > 0 {
		nBytes++
	}

	// Ensure enough bytes are buffered.
	if err = br.needBytes(int(nBytes)); err != nil {
		return 0, err
	}

	// Read bytes from the internal buffer, updating CRC on consumed bytes.
	oldPos := br.pos

	for range nBytes - 1 {
		x <<= 8
		x |= uint64(br.buf[br.pos])
		br.pos++
	}

	b := br.buf[br.pos]
	br.pos++

	// Update CRC for all consumed bytes.
	br.consumeBytes(oldPos, br.pos)

	if nBits > 0 {
		x <<= nBits
		br.n = 8 - nBits
		mask := ^uint8(0) << br.n
		x |= uint64(b&mask) >> br.n
		br.x = b & ^mask
	} else {
		x <<= 8
		x |= uint64(b)
	}

	return x, nil
}

// ByteReader returns an io.Reader that reads byte-aligned data through the bit
// reader. The bit reader must be byte-aligned when the returned reader's Read
// is called. This adapter allows passing the bit reader to functions that
// expect an io.Reader (e.g., UTF-8 decoding).
func (br *Reader) ByteReader() io.Reader {
	return &byteReaderAdapter{br: br}
}

type byteReaderAdapter struct {
	br *Reader
}

func (a *byteReaderAdapter) Read(p []byte) (int, error) {
	for i := range p {
		x, err := a.br.Read(8)
		if err != nil {
			return i, err
		}

		p[i] = byte(x) //nolint:gosec // value bounded by bit-field width just read from the stream
	}

	return len(p), nil
}
