// TODO: Remove note about encoder API.

// Package flac provides access to FLAC (Free Lossless Audio Codec) streams.
//
// A brief introduction of the FLAC stream format [1] follows. Each FLAC stream
// starts with a 32-bit signature ("fLaC"), followed by one or more metadata
// blocks, and then one or more audio frames. The first metadata block
// (StreamInfo) describes the basic properties of the audio stream and it is the
// only mandatory metadata block. Subsequent metadata blocks may appear in an
// arbitrary order.
//
// Please refer to the documentation of the meta [2] and the frame [3] packages
// for a brief introduction of their respective formats.
//
//	[1]: https://www.xiph.org/flac/format.html#stream
//	[2]: https://godoc.org/github.com/mewkiz/flac/meta
//	[3]: https://godoc.org/github.com/mewkiz/flac/frame
//
// Note: the Encoder API is experimental until the 1.1.x release. As such, it's
// API is expected to change.
package flac

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"

	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/internal/bits"
	"github.com/mycophonic/flac/internal/bufseekio"
	"github.com/mycophonic/flac/meta"
)

// A Stream contains the metadata blocks and provides access to the audio frames
// of a FLAC stream.
//
// ref: https://www.xiph.org/flac/format.html#stream
type Stream struct {
	// The StreamInfo metadata block describes the basic properties of the FLAC
	// audio stream.
	Info *meta.StreamInfo
	// Zero or more metadata blocks.
	Blocks []*meta.Block

	// seekTable contains one or more pre-calculated audio frame seek points of
	// the stream; nil if uninitialized.
	seekTable *meta.SeekTable
	// seekTableSize determines how many seek points the seekTable should have if
	// the flac file does not include one in the metadata.
	seekTableSize int
	// dataStart is the offset of the first frame header since SeekPoint.Offset
	// is relative to this position.
	dataStart int64

	// samplesDecoded tracks the running total of inter-channel samples decoded
	// so far. Used to detect when frame data exceeds the total sample count
	// declared in StreamInfo.NSamples (which callers use for buffer allocation).
	samplesDecoded uint64

	// Underlying io.Reader, or io.ReadCloser.
	r io.Reader
	// Bit reader for frame parsing, persists across frames to preserve its
	// internal read-ahead buffer. Created after metadata parsing completes.
	br *bits.Reader

	// Reusable decode buffers, sized to StreamInfo.BlockSize * NChannels.
	// Allocated once after parsing StreamInfo, reused across ParseNext calls.
	samplesBuf  []int32
	subframeBuf []*frame.Subframe
}

// New creates a new Stream for accessing the audio samples of r. It reads and
// parses the FLAC signature and the StreamInfo metadata block, but skips all
// other metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
func New(r io.Reader) (stream *Stream, err error) {
	// Verify FLAC signature and parse the StreamInfo metadata block.
	stream = &Stream{r: r}

	block, err := stream.parseStreamInfo()
	if err != nil {
		return nil, err
	}

	// Skip the remaining metadata blocks.
	for !block.IsLast {
		block, err = meta.New(r)
		if err != nil && !errors.Is(err, meta.ErrReservedType) {
			return stream, err
		}

		if err = block.Skip(); err != nil {
			return stream, err
		}
	}

	// Create persistent bit reader for frame parsing.
	stream.br = bits.NewReader(r)
	stream.initDecodeBuffers()

	return stream, nil
}

// NewSeek returns a Stream that has seeking enabled. The incoming io.ReadSeeker
// will not be buffered, which might result in performance issues. Using an
// in-memory buffer like *bytes.Reader should work well.
func NewSeek(rs io.ReadSeeker) (stream *Stream, err error) {
	br := bufseekio.NewReadSeeker(rs)
	stream = &Stream{r: br, seekTableSize: defaultSeekTableSize}

	// Verify FLAC signature and parse the StreamInfo metadata block.
	block, err := stream.parseStreamInfo()
	if err != nil {
		return stream, err
	}

	for !block.IsLast {
		block, err = meta.Parse(stream.r)
		if err != nil {
			if !errors.Is(err, meta.ErrReservedType) {
				return stream, err
			}

			if err = block.Skip(); err != nil {
				return stream, err
			}
		}

		if block.Type == meta.TypeSeekTable {
			stream.seekTable = block.Body.(*meta.SeekTable)
		}
	}

	// Record file offset of the first frame header.
	stream.dataStart, err = br.Seek(0, io.SeekCurrent)
	if err != nil {
		return stream, err
	}

	// Create persistent bit reader for frame parsing.
	stream.br = bits.NewReader(br)
	stream.initDecodeBuffers()

	return stream, err
}

var (
	// flacSignature marks the beginning of a FLAC stream.
	flacSignature = []byte("fLaC")

	// id3Signature marks the beginning of an ID3 stream, used to skip over ID3
	// data.
	id3Signature = []byte("ID3")

	// ErrNoSeeker reports that flac.NewSeek was called with an io.Reader not
	// implementing io.Seeker, and thus does not allow for seeking.
	ErrNoSeeker = errors.New("stream.Seek: reader does not implement io.Seeker")

	// ErrNoSeektable reports that no seektable has been generated. Therefore,
	// it is not possible to seek in the stream.
	ErrNoSeektable = errors.New("stream.searchFromStart: no seektable exists")
)

const (
	defaultSeekTableSize = 100
)

// parseStreamInfo verifies the signature which marks the beginning of a FLAC
// stream, and parses the StreamInfo metadata block. It returns a boolean value
// which specifies if the StreamInfo block was the last metadata block of the
// FLAC stream.
func (stream *Stream) parseStreamInfo() (block *meta.Block, err error) {
	// Verify FLAC signature.
	r := stream.r

	var buf [4]byte
	if _, err = io.ReadFull(r, buf[:]); err != nil {
		return block, err
	}

	// Skip prepended ID3v2 data.
	if bytes.Equal(buf[:3], id3Signature) {
		if err := stream.skipID3v2(); err != nil {
			return block, err
		}

		// Second attempt at verifying signature.
		if _, err = io.ReadFull(r, buf[:]); err != nil {
			return block, err
		}
	}

	if !bytes.Equal(buf[:], flacSignature) {
		return block, fmt.Errorf(
			"flac.parseStreamInfo: invalid FLAC signature; expected %q, got %q",
			flacSignature,
			buf,
		)
	}

	// Parse StreamInfo metadata block.
	block, err = meta.Parse(r)
	if err != nil {
		return block, err
	}

	si, ok := block.Body.(*meta.StreamInfo)
	if !ok {
		return block, fmt.Errorf(
			"flac.parseStreamInfo: incorrect type of first metadata block; expected *meta.StreamInfo, got %T",
			block.Body,
		)
	}

	stream.Info = si

	return block, nil
}

// skipID3v2 skips ID3v2 data prepended to flac files.
func (stream *Stream) skipID3v2() error {
	r := stream.r

	// Discard 2 unnecessary bytes from the ID3v2 header (version + flags).
	var skip [2]byte
	if _, err := io.ReadFull(r, skip[:]); err != nil {
		return err
	}

	// Read the size from the ID3v2 header.
	var sizeBuf [4]byte
	if _, err := io.ReadFull(r, sizeBuf[:]); err != nil {
		return err
	}
	// The size is encoded as a synchsafe integer.
	size := int64(sizeBuf[0])<<21 | int64(sizeBuf[1])<<14 | int64(sizeBuf[2])<<7 | int64(sizeBuf[3])

	_, err := io.CopyN(io.Discard, r, size)

	return err
}

// Parse creates a new Stream for accessing the metadata blocks and audio
// samples of r. It reads and parses the FLAC signature and all metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
func Parse(r io.Reader) (stream *Stream, err error) {
	// Verify FLAC signature and parse the StreamInfo metadata block.
	stream = &Stream{r: r}

	block, err := stream.parseStreamInfo()
	if err != nil {
		return nil, err
	}

	// Parse the remaining metadata blocks.
	for !block.IsLast {
		block, err = meta.Parse(r)
		if err != nil {
			if !errors.Is(err, meta.ErrReservedType) {
				return stream, err
			}
			// Skip the body of unknown (reserved) metadata blocks, as stated by
			// the specification.
			//
			// ref: https://www.xiph.org/flac/format.html#format_overview
			if err = block.Skip(); err != nil {
				return stream, err
			}
		}

		stream.Blocks = append(stream.Blocks, block)
	}

	// Create persistent bit reader for frame parsing.
	stream.br = bits.NewReader(r)
	stream.initDecodeBuffers()

	return stream, nil
}

// Open creates a new Stream for accessing the audio samples of path. It reads
// and parses the FLAC signature and the StreamInfo metadata block, but skips
// all other metadata blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
//
// Note: The Close method of the stream must be called when finished using it.
func Open(path string) (stream *Stream, err error) {
	f, err := os.Open(path) //nolint:gosec // caller-controlled path is the API contract
	if err != nil {
		return nil, err
	}

	stream, err = New(f)
	if err != nil {
		_ = f.Close()

		return nil, err
	}

	return stream, err
}

// ParseFile creates a new Stream for accessing the metadata blocks and audio
// samples of path. It reads and parses the FLAC signature and all metadata
// blocks.
//
// Call Stream.Next to parse the frame header of the next audio frame, and call
// Stream.ParseNext to parse the entire next frame including audio samples.
//
// Note: The Close method of the stream must be called when finished using it.
func ParseFile(path string) (stream *Stream, err error) {
	f, err := os.Open(path) //nolint:gosec // caller-controlled path is the API contract
	if err != nil {
		return nil, err
	}

	stream, err = Parse(f)
	if err != nil {
		_ = f.Close()

		return nil, err
	}

	return stream, err
}

// Close closes the stream gracefully if the underlying io.Reader also implements the io.Closer interface.
func (stream *Stream) Close() error {
	if closer, ok := stream.r.(io.Closer); ok {
		return closer.Close()
	}

	return nil
}

// initDecodeBuffers pre-allocates sample and subframe buffers sized to the
// stream's maximum block size and channel count. These buffers are reused
// across ParseNext calls to avoid per-frame heap allocations.
func (stream *Stream) initDecodeBuffers() {
	nChannels := int(stream.Info.NChannels)
	blockSize := int(stream.Info.BlockSizeMax)
	stream.samplesBuf = make([]int32, nChannels*blockSize)

	stream.subframeBuf = make([]*frame.Subframe, nChannels)
	for i := range stream.subframeBuf {
		stream.subframeBuf[i] = new(frame.Subframe)
	}
}

// Next parses the frame header of the next audio frame. It returns io.EOF to
// signal a graceful end of FLAC stream.
//
// Call Frame.Parse to parse the audio samples of its subframes.
func (stream *Stream) Next() (f *frame.Frame, err error) {
	f, err = frame.New(stream.br)
	if err != nil {
		return f, err
	}

	// Each frame header independently specifies its own channel assignment
	// (frame/frame.go parseChannels), which may differ from StreamInfo.NChannels
	// in malformed files (e.g. IETF faulty/04 "wrong number of channels") or in
	// uncommon files where the channel count changes mid-stream (e.g. IETF
	// uncommon/03 "decreasing number of channels").
	//
	// Callers (decoders) typically allocate buffers and interleave samples based
	// on StreamInfo.NChannels. A mismatch causes index-out-of-range panics in
	// interleave loops when the frame has fewer subframes than expected.
	//
	// Return a clear error instead of letting the caller panic.
	if got, want := f.Channels.Count(), int(stream.Info.NChannels); got != want {
		return nil, fmt.Errorf(
			"flac.Stream.Next: channel count mismatch; frame has %d channels, StreamInfo has %d",
			got,
			want,
		)
	}

	// Each frame header independently specifies its own bit depth
	// (frame/frame.go parseBitsPerSample). A value of 0 means "get from
	// StreamInfo" (valid per spec). When explicitly set, it must match
	// StreamInfo.BitsPerSample — a mismatch (e.g. IETF faulty/03 "wrong
	// bits per sample in frame header") means the frame was encoded at a
	// different resolution than the stream declares, which corrupts sample
	// decoding and buffer sizing.
	if f.BitsPerSample != 0 && f.BitsPerSample != stream.Info.BitsPerSample {
		return nil, fmt.Errorf(
			"flac.Stream.Next: bit depth mismatch; frame has %d bits, StreamInfo has %d",
			f.BitsPerSample,
			stream.Info.BitsPerSample,
		)
	}

	// Resolve BitsPerSample=0 from StreamInfo so that callers who subsequently
	// call f.Parse() get correct subframe decoding.
	if f.BitsPerSample == 0 {
		f.BitsPerSample = stream.Info.BitsPerSample
	}

	// Validate running sample count against StreamInfo.NSamples.
	// See ParseNext() for detailed rationale.
	stream.samplesDecoded += uint64(f.BlockSize)
	if err = stream.validateSampleCount(); err != nil {
		return nil, err
	}

	return f, nil
}

// ParseNext parses the entire next frame including audio samples. It returns
// io.EOF to signal a graceful end of FLAC stream.
func (stream *Stream) ParseNext() (f *frame.Frame, err error) {
	// Parse header first so we can validate and resolve fields before
	// subframe decoding begins.
	f, err = frame.New(stream.br)
	if err != nil {
		return f, err
	}

	// See Next() for rationale on channel count validation.
	if got, want := f.Channels.Count(), int(stream.Info.NChannels); got != want {
		return nil, fmt.Errorf(
			"flac.Stream.ParseNext: channel count mismatch; frame has %d channels, StreamInfo has %d",
			got,
			want,
		)
	}

	// See Next() for rationale on bit depth validation.
	if f.BitsPerSample != 0 && f.BitsPerSample != stream.Info.BitsPerSample {
		return nil, fmt.Errorf(
			"flac.Stream.ParseNext: bit depth mismatch; frame has %d bits, StreamInfo has %d",
			f.BitsPerSample,
			stream.Info.BitsPerSample,
		)
	}

	// Resolve BitsPerSample=0 from StreamInfo before subframe parsing.
	if f.BitsPerSample == 0 {
		f.BitsPerSample = stream.Info.BitsPerSample
	}

	// Now parse subframes with the resolved header.
	if err = f.ParseReuse(stream.samplesBuf, stream.subframeBuf); err != nil {
		return f, err
	}

	// Track running sample count and validate against StreamInfo.NSamples.
	//
	// StreamInfo.NSamples declares the total number of inter-channel samples in
	// the stream. A value of 0 means "unknown" (valid per spec). When non-zero,
	// callers rely on it for buffer pre-allocation:
	//
	//   buf = make([]byte, NSamples * NChannels * bytesPerSample)
	//
	// If the actual frame data exceeds the declared count (e.g. IETF faulty/05
	// "wrong total number of samples"), the pre-allocated buffer is too small and
	// interleave writes panic with a slice-bounds-out-of-range error.
	//
	// Catch the mismatch here so callers get an error instead of a panic.
	stream.samplesDecoded += uint64(f.BlockSize)
	if err = stream.validateSampleCount(); err != nil {
		return nil, err
	}

	return f, nil
}

// validateSampleCount returns an error if the running total of decoded samples
// exceeds the total declared in StreamInfo.NSamples (when non-zero).
func (stream *Stream) validateSampleCount() error {
	nsamples := stream.Info.NSamples
	if nsamples == 0 {
		// NSamples of 0 means "unknown" per spec; nothing to validate.
		return nil
	}

	if stream.samplesDecoded > nsamples {
		return fmt.Errorf(
			"flac.Stream: decoded samples (%d) exceed StreamInfo.NSamples (%d)",
			stream.samplesDecoded, nsamples,
		)
	}

	return nil
}

// Seek seeks to the frame containing the given absolute sample number. The
// return value specifies the first sample number of the frame containing
// sampleNum.
func (stream *Stream) Seek(sampleNum uint64) (uint64, error) {
	if stream.seekTable == nil && stream.seekTableSize > 0 {
		if err := stream.makeSeekTable(); err != nil {
			return 0, err
		}
	}

	if stream.Info.NSamples != 0 && sampleNum >= stream.Info.NSamples {
		return 0, fmt.Errorf("unable to seek to sample number %d", sampleNum)
	}

	point, err := stream.searchFromStart(sampleNum)
	if err != nil {
		return 0, err
	}

	if _, err := stream.br.Seek(stream.dataStart+int64(point.Offset), io.SeekStart); err != nil { //nolint:gosec // value bounded by bit-field width just read from the stream
		return 0, err
	}

	// Reset the decoded sample counter to the seek point's starting sample.
	// The loop below calls ParseNext to scan forward from the seek point to
	// the frame containing sampleNum. These are internal scanning calls — not
	// caller-visible decoding — but ParseNext still accumulates the counter.
	// Starting from the seek point's sample number keeps the running total
	// consistent with the actual stream position, so validateSampleCount
	// works correctly during the scan.
	stream.samplesDecoded = point.SampleNum

	for {
		// Record seek offset to start of frame.
		offset, err := stream.br.Seek(0, io.SeekCurrent)
		if err != nil {
			return 0, err
		}

		f, err := stream.ParseNext()
		if err != nil {
			return 0, err
		}

		if f.SampleNumber()+uint64(f.BlockSize) > sampleNum {
			// Restore seek offset to the start of the frame containing the
			// specified sample number.
			//
			// Reset decoded sample counter to this frame's starting sample.
			// The reader is rewound to the frame's start, so the caller's
			// next ParseNext will re-decode this frame and re-add its
			// BlockSize to the counter.
			stream.samplesDecoded = f.SampleNumber()

			_, err := stream.br.Seek(offset, io.SeekStart)

			return f.SampleNumber(), err
		}
	}
}

// searchFromStart searches for the given sample number using binary search and
// returns the last seek point whose start sample is at or before sampleNum. If
// sampleNum is before the first seek point, a zero seek point (SampleNum: 0,
// Offset: 0) is returned so the caller scans from the start of audio data and
// finds the frame actually containing sampleNum.
func (stream *Stream) searchFromStart(sampleNum uint64) (meta.SeekPoint, error) {
	points := stream.seekTable.Points
	if len(points) == 0 {
		return meta.SeekPoint{}, ErrNoSeektable
	}
	// Find the first point where SampleNum > sampleNum.
	// The point before it is the last one starting at or before sampleNum.
	i := sort.Search(len(points), func(i int) bool {
		return points[i].SampleNum > sampleNum
	}) - 1
	if i < 0 {
		// Target precedes the first seek point; fall back to the start of
		// audio data so the scan loop can find the correct frame.
		return meta.SeekPoint{SampleNum: 0, Offset: 0}, nil
	}

	return points[i], nil
}

// makeSeekTable creates a seek table with seek points to each frame of the FLAC
// stream.
func (stream *Stream) makeSeekTable() (err error) {
	// Save current position to restore after scanning.
	pos, err := stream.br.Seek(0, io.SeekCurrent)
	if err != nil {
		return ErrNoSeeker
	}

	if _, err = stream.br.Seek(stream.dataStart, io.SeekStart); err != nil {
		return err
	}

	// Save and restore samplesDecoded around the scan. makeSeekTable is an
	// internal operation that parses every frame to build seek points — it
	// does not represent actual decoding progress for the caller. Without
	// this save/restore, the running counter would accumulate the entire
	// file's worth of samples, causing validateSampleCount to reject
	// subsequent legitimate ParseNext calls.
	savedSamples := stream.samplesDecoded
	stream.samplesDecoded = 0

	var (
		sampleNum uint64
		points    []meta.SeekPoint
	)

	for {
		// Record seek offset to start of frame.
		off, err := stream.br.Seek(0, io.SeekCurrent)
		if err != nil {
			return err
		}

		f, err := stream.ParseNext()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return err
		}

		points = append(points, meta.SeekPoint{
			SampleNum: sampleNum,
			Offset:    uint64(off - stream.dataStart), //nolint:gosec // value is non-negative by construction
			NSamples:  f.BlockSize,
		})

		sampleNum += uint64(f.BlockSize)
	}

	stream.seekTable = &meta.SeekTable{Points: points}
	stream.samplesDecoded = savedSamples

	// Restore original position.
	_, err = stream.br.Seek(pos, io.SeekStart)

	return err
}
