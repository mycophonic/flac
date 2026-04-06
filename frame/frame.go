// Package frame implements access to FLAC audio frames.
//
// A brief introduction of the FLAC audio format [1] follows. FLAC encoders
// divide the audio stream into blocks through a process called blocking [2]. A
// block contains the unencoded audio samples from all channels during a short
// period of time. Each audio block is divided into subblocks, one per channel.
//
// There is often a correlation between the left and right channel of stereo
// audio. Using inter-channel decorrelation [3] it is possible to store only one
// of the channels and the difference between the channels, or store the average
// of the channels and their difference. An encoder decorrelates audio samples
// as follows:
//
//	mid = (left + right)/2 // average of the channels
//	side = left - right    // difference between the channels
//
// The blocks are encoded using a variety of prediction methods [4][5] and
// stored in frames. Blocks and subblocks contains unencoded audio samples while
// frames and subframes contain encoded audio samples. A FLAC stream contains
// one or more audio frames.
//
//	[1]: https://www.xiph.org/flac/format.html#architecture
//	[2]: https://www.xiph.org/flac/format.html#blocking
//	[3]: https://www.xiph.org/flac/format.html#interchannel
//	[4]: https://www.xiph.org/flac/format.html#prediction
//	[5]: https://godoc.org/github.com/mewkiz/flac/frame#Pred
package frame

import (
	"errors"
	"fmt"
	"hash"
	"io"
	"log"

	"github.com/mycophonic/flac/internal/bits"
	"github.com/mycophonic/flac/internal/utf8"
)

// A Frame contains the header and subframes of an audio frame. It holds the
// encoded samples from a block (a part) of the audio stream. Each subframe
// holding the samples from one of its channel.
//
// ref: https://www.xiph.org/flac/format.html#frame
type Frame struct {
	// Audio frame header.
	Header
	// One subframe per channel, containing encoded audio samples.
	Subframes []*Subframe
	// A bit reader with internal 4KB buffer and inline CRC computation.
	br *bits.Reader
	// Contiguous sample buffer shared by all subframes. Each subframe's
	// Samples slice points into this buffer. Allocated once per frame to
	// avoid per-subframe allocations.
	samplesBuf []int32
}

// New creates a new Frame for accessing the audio samples via the provided bit
// reader. It reads and parses an audio frame header, resetting CRC state for
// the new frame. It returns io.EOF to signal a graceful end of FLAC stream.
//
// The bit reader must persist across frames (owned by the Stream) so that its
// internal read-ahead buffer is not lost between frames.
//
// Call Frame.Parse to parse the audio samples of its subframes.
func New(br *bits.Reader) (frame *Frame, err error) {
	// Reset CRC for the new frame.
	br.EnableCRC16()

	// Parse frame header.
	frame = &Frame{br: br}
	err = frame.parseHeader()
	return frame, err
}

// Parse reads and parses the header, and the audio samples from each subframe
// of a frame via the provided bit reader. If the samples are inter-channel
// decorrelated between the subframes, it correlates them. It returns io.EOF to
// signal a graceful end of FLAC stream.
//
// ref: https://www.xiph.org/flac/format.html#interchannel
func Parse(br *bits.Reader) (frame *Frame, err error) {
	// Parse frame header.
	frame, err = New(br)
	if err != nil {
		return frame, err
	}

	// Parse subframes.
	err = frame.Parse()
	return frame, err
}

// ParseInto is like Parse but reuses pre-allocated buffers to avoid per-frame
// heap allocations. samplesBuf must have capacity for at least
// nChannels*blockSize int32 values. subframes must have length >= nChannels,
// with each element pointing to a valid Subframe struct that will be reset and
// reused.
func ParseInto(br *bits.Reader, samplesBuf []int32, subframes []*Subframe) (*Frame, error) {
	frame, err := New(br)
	if err != nil {
		return frame, err
	}
	err = frame.parseInto(samplesBuf, subframes)
	return frame, err
}

// Parse reads and parses the audio samples from each subframe of the frame. If
// the samples are inter-channel decorrelated between the subframes, it
// correlates them.
//
// ref: https://www.xiph.org/flac/format.html#interchannel
func (frame *Frame) Parse() error {
	// Allocate fresh buffers.
	nChannels := frame.Channels.Count()
	blockSize := int(frame.BlockSize)
	frame.samplesBuf = make([]int32, nChannels*blockSize)
	frame.Subframes = make([]*Subframe, nChannels)
	for i := range frame.Subframes {
		frame.Subframes[i] = new(Subframe)
	}
	return frame.parseSubframes()
}

// parseInto sets up the frame to use pre-allocated buffers and parses all
// subframes.
func (frame *Frame) parseInto(samplesBuf []int32, subframes []*Subframe) error {
	nChannels := frame.Channels.Count()
	blockSize := int(frame.BlockSize)
	required := nChannels * blockSize
	if required > len(samplesBuf) || nChannels > len(subframes) {
		return fmt.Errorf("frame.Frame.parseInto: frame requires %d channels × %d block size, but buffers have %d samples and %d subframes",
			nChannels, blockSize, len(samplesBuf), len(subframes))
	}
	frame.samplesBuf = samplesBuf[:required]
	frame.Subframes = subframes[:nChannels]
	return frame.parseSubframes()
}

// parseSubframes parses all subframe audio samples, correlates inter-channel
// samples, verifies padding alignment and the CRC-16 footer. The frame's
// Subframes slice and samplesBuf must already be set up before calling.
func (frame *Frame) parseSubframes() error {
	nChannels := frame.Channels.Count()
	blockSize := int(frame.BlockSize)
	for channel := range nChannels {
		// The side channel requires an extra bit per sample when using
		// inter-channel decorrelation.
		bps := uint(frame.BitsPerSample)
		switch frame.Channels {
		case ChannelsSideRight:
			// channel 0 is the side channel.
			if channel == 0 {
				bps++
			}
		case ChannelsLeftSide, ChannelsMidSide:
			// channel 1 is the side channel.
			if channel == 1 {
				bps++
			}
		}

		// Slice the contiguous sample buffer for this channel's subframe.
		off := channel * blockSize
		samples := frame.samplesBuf[off : off : off+blockSize]

		// Parse subframe into the pre-allocated struct.
		if err := frame.parseSubframeInto(frame.br, bps, samples, frame.Subframes[channel]); err != nil {
			return err
		}
	}

	// Inter-channel correlation of subframe samples.
	frame.Correlate()

	// Zero-padding to byte alignment.
	if !frame.br.IsAligned() {
		padding, err := frame.br.Read(frame.br.BitsBuffered())
		if err != nil {
			return unexpected(err)
		}
		if padding != 0 {
			return fmt.Errorf("frame.Frame.Parse: non-zero padding bits (%d)", padding)
		}
	}

	// 2 bytes: CRC-16 checksum.
	// Disable CRC-16 before reading the footer so the footer bytes are NOT
	// included in the CRC-16 computation.
	got := frame.br.CRC16()
	frame.br.DisableCRC16()
	crc16Val, err := frame.br.Read(16)
	if err != nil {
		return unexpected(err)
	}
	want := uint16(crc16Val)
	if got != want {
		return fmt.Errorf("frame.Frame.Parse: CRC-16 checksum mismatch; expected 0x%04X, got 0x%04X", want, got)
	}

	return nil
}

// Hash adds the decoded audio samples of the frame to a running MD5 hash. It
// can be used in conjunction with StreamInfo.MD5sum to verify the integrity of
// the decoded audio samples.
//
// Note: The audio samples of the frame must be decoded before calling Hash.
func (frame *Frame) Hash(md5sum hash.Hash) {
	// Write decoded samples to a running MD5 hash.
	bps := frame.BitsPerSample
	var buf [4]byte
	if len(frame.Subframes) == 0 {
		return
	}
	// Use the length of the first subframe's samples as they should all be the same length
	nsamples := len(frame.Subframes[0].Samples)
	for i := 0; i < nsamples; i++ {
		for _, subframe := range frame.Subframes {
			sample := subframe.Samples[i]
			switch {
			case 1 <= bps && bps <= 8:
				buf[0] = uint8(sample)
				md5sum.Write(buf[:1])
			case 9 <= bps && bps <= 16:
				buf[0] = uint8(sample)
				buf[1] = uint8(sample >> 8)
				md5sum.Write(buf[:2])
			case 17 <= bps && bps <= 24:
				buf[0] = uint8(sample)
				buf[1] = uint8(sample >> 8)
				buf[2] = uint8(sample >> 16)
				md5sum.Write(buf[:3])
			case 25 <= bps && bps <= 32:
				buf[0] = uint8(sample)
				buf[1] = uint8(sample >> 8)
				buf[2] = uint8(sample >> 16)
				buf[3] = uint8(sample >> 24)
				md5sum.Write(buf[:4])
			default:
				log.Printf("frame.Frame.Hash: support for %d-bit sample size not yet implemented", bps)
			}
		}
	}
}

// A Header contains the basic properties of an audio frame, such as its sample
// rate and channel count. To facilitate random access decoding each frame
// header starts with a sync-code. This allows the decoder to synchronize and
// locate the start of a frame header.
//
// ref: https://www.xiph.org/flac/format.html#frame_header
type Header struct {
	// Specifies if the block size is fixed or variable.
	HasFixedBlockSize bool
	// Block size in inter-channel samples, i.e. the number of audio samples in
	// each subframe.
	BlockSize uint16
	// Sample rate in Hz; a 0 value implies unknown, get sample rate from
	// StreamInfo.
	SampleRate uint32
	// Specifies the number of channels (subframes) that exist in the frame,
	// their order and possible inter-channel decorrelation.
	Channels Channels
	// Sample size in bits-per-sample; a 0 value implies unknown, get sample size
	// from StreamInfo.
	BitsPerSample uint8
	// Specifies the frame number if the block size is fixed, and the first
	// sample number in the frame otherwise. When using fixed block size, the
	// first sample number in the frame can be derived by multiplying the frame
	// number with the block size (in samples).
	Num uint64
}

// Errors returned by Frame.parseHeader.
var (
	ErrInvalidSync = errors.New("frame.Frame.parseHeader: invalid sync-code")
)

// maxSyncScan is the maximum number of zero bytes to skip when scanning for a
// frame sync code through undeclared zero padding.
const maxSyncScan = 32 << 20 // 32 MB

// parseHeader reads and parses the header of an audio frame.
func (frame *Frame) parseHeader() error {
	// Enable CRC-8 accumulation for header verification.
	br := frame.br
	br.EnableCRC8()

	// 14 bits: sync-code (11111111111110)
	x, err := br.Read(14)
	if err != nil {
		// This is the only place an audio frame may return io.EOF, which signals
		// a graceful end of a FLAC stream.
		return err
	}
	if x == 0 {
		// All zeros: undeclared zero padding before the first audio frame
		// (e.g. HDtracks FLAC files with 16 MB of zeros after metadata).
		// Scan forward to the real sync code. On return, the reserved and
		// blocking strategy bits have been consumed.
		if err := frame.scanToSync(); err != nil {
			return err
		}
	} else if x == 0x3FFE {
		// 1 bit: reserved.
		x, err = br.Read(1)
		if err != nil {
			return unexpected(err)
		}
		if x != 0 {
			return errors.New("frame.Frame.parseHeader: non-zero reserved value")
		}

		// 1 bit: HasFixedBlockSize.
		x, err = br.Read(1)
		if err != nil {
			return unexpected(err)
		}
		if x == 0 {
			frame.HasFixedBlockSize = true
		}
	} else {
		return ErrInvalidSync
	}

	// 4 bits: BlockSize. The block size parsing is simplified by deferring it to
	// the end of the header.
	blockSize, err := br.Read(4)
	if err != nil {
		return unexpected(err)
	}

	// 4 bits: SampleRate. The sample rate parsing is simplified by deferring it
	// to the end of the header.
	sampleRate, err := br.Read(4)
	if err != nil {
		return unexpected(err)
	}

	// Parse channels.
	if err := frame.parseChannels(br); err != nil {
		return err
	}

	// Parse bits per sample.
	if err := frame.parseBitsPerSample(br); err != nil {
		return err
	}

	// 1 bit: reserved.
	x, err = br.Read(1)
	if err != nil {
		return unexpected(err)
	}
	if x != 0 {
		return errors.New("frame.Frame.parseHeader: non-zero reserved value")
	}

	// if (fixed block size)
	//    1-6 bytes: UTF-8 encoded frame number.
	// else
	//    1-7 bytes: UTF-8 encoded sample number.
	// Note: at this point exactly 32 bits (4 bytes) have been consumed, so the
	// stream is byte-aligned. Read through the bit reader's byte adapter so
	// bytes flow through the internal buffer and CRC computation.
	frame.Num, err = utf8.Decode(br.ByteReader())
	if err != nil {
		return unexpected(err)
	}

	// Parse block size.
	if err := frame.parseBlockSize(br, blockSize); err != nil {
		return err
	}

	// Parse sample rate.
	if err := frame.parseSampleRate(br, sampleRate); err != nil {
		return err
	}

	// 1 byte: CRC-8 checksum.
	// Disable CRC-8 before reading the checksum byte so it is not included
	// in the CRC-8 computation. The byte still flows through CRC-16.
	br.DisableCRC8()
	crc8Val, err := br.Read(8)
	if err != nil {
		return unexpected(err)
	}
	want := uint8(crc8Val)
	got := br.CRC8()
	if want != got {
		return fmt.Errorf("frame.Frame.parseHeader: CRC-8 checksum mismatch; expected 0x%02X, got 0x%02X", want, got)
	}

	return nil
}

// scanToSync scans forward through undeclared zero padding to locate the next
// FLAC frame sync code.
//
// Some FLAC files (e.g. from HDtracks) contain large blocks of zero bytes
// between the last metadata block and the first audio frame that are not
// declared as FLAC Padding metadata. Standard decoders (libFLAC, ffmpeg) fail
// on these files because they expect audio frames immediately after metadata.
//
// Precondition: the caller's Read(14) returned all zeros, leaving 2 zero bits
// buffered. On return, the full 16-bit sync word (sync code + reserved +
// blocking strategy) has been consumed, HasFixedBlockSize is set, and
// CRC-8/CRC-16 have been reset and seeded with the two sync bytes.
func (frame *Frame) scanToSync() error {
	br := frame.br

	// The caller's Read(14) consumed 2 zero bytes with 2 bits still buffered.
	// Drain them before switching to byte-aligned scanning.
	if n := br.BitsBuffered(); n > 0 {
		pad, err := br.Read(n)
		if err != nil {
			return unexpected(err)
		}

		if pad != 0 {
			return ErrInvalidSync
		}
	}

	// Scan byte-by-byte through zeros for the first sync byte (0xFF).
	// Only zero bytes are tolerated; any non-zero byte that is not 0xFF
	// is treated as corruption.
	for skipped := 0; skipped < maxSyncScan; skipped++ {
		b, err := br.Read(8)
		if err != nil {
			if err == io.EOF {
				return io.ErrUnexpectedEOF
			}

			return err
		}

		if b == 0 {
			continue
		}

		if b != 0xFF {
			return ErrInvalidSync
		}

		// Found 0xFF. Read the second byte of the sync word.
		// Valid patterns:
		//   0xFFF8 = sync(0x3FFE) + reserved(0) + blocking(0) → fixed block size
		//   0xFFF9 = sync(0x3FFE) + reserved(0) + blocking(1) → variable block size
		next, err := br.Read(8)
		if err != nil {
			return unexpected(err)
		}

		switch next {
		case 0xF8:
			frame.HasFixedBlockSize = true
		case 0xF9:
			// Variable block size; HasFixedBlockSize stays false.
		default:
			return ErrInvalidSync
		}

		// Reset CRC checksums to start from this frame and seed with the
		// two sync bytes so the header CRC covers the complete frame header.
		syncBytes := [2]byte{0xFF, byte(next)}
		br.EnableCRC16()
		br.EnableCRC8()
		br.FeedCRC(syncBytes[:])

		return nil
	}

	return ErrInvalidSync
}

// parseBitsPerSample parses the bits per sample of the header.
func (frame *Frame) parseBitsPerSample(br *bits.Reader) error {
	// 3 bits: BitsPerSample.
	x, err := br.Read(3)
	if err != nil {
		return unexpected(err)
	}

	// The 3 bits are used to specify the sample size as follows:
	//    000: unknown sample size; get from StreamInfo.
	//    001: 8 bits-per-sample.
	//    010: 12 bits-per-sample.
	//    011: reserved.
	//    100: 16 bits-per-sample.
	//    101: 20 bits-per-sample.
	//    110: 24 bits-per-sample.
	//    111: reserved.
	switch x {
	case 0x0:
		// 000: unknown bits-per-sample; get from StreamInfo.
	case 0x1:
		// 001: 8 bits-per-sample.
		frame.BitsPerSample = 8
	case 0x2:
		// 010: 12 bits-per-sample.
		frame.BitsPerSample = 12
	case 0x4:
		// 100: 16 bits-per-sample.
		frame.BitsPerSample = 16
	case 0x5:
		// 101: 20 bits-per-sample.
		frame.BitsPerSample = 20
	case 0x6:
		// 110: 24 bits-per-sample.
		frame.BitsPerSample = 24
	case 0x7:
		// 111: 32 bits-per-sample (RFC 9639).
		frame.BitsPerSample = 32
	default:
		// 011: reserved.
		return fmt.Errorf("frame.Frame.parseHeader: reserved sample size bit pattern (%03b)", x)
	}
	return nil
}

// parseChannels parses the channels of the header.
func (frame *Frame) parseChannels(br *bits.Reader) error {
	// 4 bits: Channels.
	//
	// The 4 bits are used to specify the channels as follows:
	//    0000: (1 channel) mono.
	//    0001: (2 channels) left, right.
	//    0010: (3 channels) left, right, center.
	//    0011: (4 channels) left, right, left surround, right surround.
	//    0100: (5 channels) left, right, center, left surround, right surround.
	//    0101: (6 channels) left, right, center, LFE, left surround, right surround.
	//    0110: (7 channels) left, right, center, LFE, center surround, side left, side right.
	//    0111: (8 channels) left, right, center, LFE, left surround, right surround, side left, side right.
	//    1000: (2 channels) left, side; using inter-channel decorrelation.
	//    1001: (2 channels) side, right; using inter-channel decorrelation.
	//    1010: (2 channels) mid, side; using inter-channel decorrelation.
	//    1011: reserved.
	//    1100: reserved.
	//    1101: reserved.
	//    1111: reserved.
	x, err := br.Read(4)
	if err != nil {
		return unexpected(err)
	}
	if x >= 0xB {
		return fmt.Errorf("frame.Frame.parseHeader: reserved channels bit pattern (%04b)", x)
	}
	frame.Channels = Channels(x)
	return nil
}

// parseBlockSize parses the block size of the header.
func (frame *Frame) parseBlockSize(br *bits.Reader, blockSize uint64) error {
	// The 4 bits of n are used to specify the block size as follows:
	//    0000: reserved.
	//    0001: 192 samples.
	//    0010-0101: 576 * 2^(n-2) samples.
	//    0110: get 8 bit (block size)-1 from the end of the header.
	//    0111: get 16 bit (block size)-1 from the end of the header.
	//    1000-1111: 256 * 2^(n-8) samples.
	n := blockSize
	switch {
	case n == 0x0:
		// 0000: reserved.
		return errors.New("frame.Frame.parseHeader: reserved block size bit pattern (0000)")
	case n == 0x1:
		// 0001: 192 samples.
		frame.BlockSize = 192
	case n >= 0x2 && n <= 0x5:
		// 0010-0101: 576 * 2^(n-2) samples.
		frame.BlockSize = 576 * (1 << (n - 2))
	case n == 0x6:
		// 0110: get 8 bit (block size)-1 from the end of the header.
		x, err := br.Read(8)
		if err != nil {
			return unexpected(err)
		}
		frame.BlockSize = uint16(x + 1)
	case n == 0x7:
		// 0111: get 16 bit (block size)-1 from the end of the header.
		x, err := br.Read(16)
		if err != nil {
			return unexpected(err)
		}
		frame.BlockSize = uint16(x + 1)
	default:
		//    1000-1111: 256 * 2^(n-8) samples.
		frame.BlockSize = 256 * (1 << (n - 8))
	}
	return nil
}

// parseSampleRate parses the sample rate of the header.
func (frame *Frame) parseSampleRate(br *bits.Reader, sampleRate uint64) error {
	// The 4 bits are used to specify the sample rate as follows:
	//    0000: unknown sample rate; get from StreamInfo.
	//    0001: 88.2 kHz.
	//    0010: 176.4 kHz.
	//    0011: 192 kHz.
	//    0100: 8 kHz.
	//    0101: 16 kHz.
	//    0110: 22.05 kHz.
	//    0111: 24 kHz.
	//    1000: 32 kHz.
	//    1001: 44.1 kHz.
	//    1010: 48 kHz.
	//    1011: 96 kHz.
	//    1100: get 8 bit sample rate (in kHz) from the end of the header.
	//    1101: get 16 bit sample rate (in Hz) from the end of the header.
	//    1110: get 16 bit sample rate (in daHz) from the end of the header.
	//    1111: invalid.
	switch sampleRate {
	case 0x0:
		// 0000: unknown sample rate; get from StreamInfo.
	case 0x1:
		// 0001: 88.2 kHz.
		frame.SampleRate = 88200
	case 0x2:
		// 0010: 176.4 kHz.
		frame.SampleRate = 176400
	case 0x3:
		// 0011: 192 kHz.
		frame.SampleRate = 192000
	case 0x4:
		// 0100: 8 kHz.
		frame.SampleRate = 8000
	case 0x5:
		// 0101: 16 kHz.
		frame.SampleRate = 16000
	case 0x6:
		// 0110: 22.05 kHz.
		frame.SampleRate = 22050
	case 0x7:
		// 0111: 24 kHz.
		frame.SampleRate = 24000
	case 0x8:
		// 1000: 32 kHz.
		frame.SampleRate = 32000
	case 0x9:
		// 1001: 44.1 kHz.
		frame.SampleRate = 44100
	case 0xA:
		// 1010: 48 kHz.
		frame.SampleRate = 48000
	case 0xB:
		// 1011: 96 kHz.
		frame.SampleRate = 96000
	case 0xC:
		// 1100: get 8 bit sample rate (in kHz) from the end of the header.
		x, err := br.Read(8)
		if err != nil {
			return unexpected(err)
		}
		frame.SampleRate = uint32(x * 1000)
	case 0xD:
		// 1101: get 16 bit sample rate (in Hz) from the end of the header.
		x, err := br.Read(16)
		if err != nil {
			return unexpected(err)
		}
		frame.SampleRate = uint32(x)
	case 0xE:
		// 1110: get 16 bit sample rate (in daHz) from the end of the header.
		x, err := br.Read(16)
		if err != nil {
			return unexpected(err)
		}
		frame.SampleRate = uint32(x * 10)
	default:
		// 1111: invalid.
		return errors.New("frame.Frame.parseHeader: invalid sample rate bit pattern (1111)")
	}
	return nil
}

// Channels specifies the number of channels (subframes) that exist in a frame,
// their order and possible inter-channel decorrelation.
type Channels uint8

// Channel assignments. The following abbreviations are used:
//
//	C:   center (directly in front)
//	R:   right (standard stereo)
//	Sr:  side right (directly to the right)
//	Rs:  right surround (back right)
//	Cs:  center surround (rear center)
//	Ls:  left surround (back left)
//	Sl:  side left (directly to the left)
//	L:   left (standard stereo)
//	Lfe: low-frequency effect (placed according to room acoustics)
//
// The first 6 channel constants follow the SMPTE/ITU-R channel order:
//
//	L R C Lfe Ls Rs
const (
	ChannelsMono           Channels = iota // 1 channel: mono.
	ChannelsLR                             // 2 channels: left, right.
	ChannelsLRC                            // 3 channels: left, right, center.
	ChannelsLRLsRs                         // 4 channels: left, right, left surround, right surround.
	ChannelsLRCLsRs                        // 5 channels: left, right, center, left surround, right surround.
	ChannelsLRCLfeLsRs                     // 6 channels: left, right, center, LFE, left surround, right surround.
	ChannelsLRCLfeCsSlSr                   // 7 channels: left, right, center, LFE, center surround, side left, side right.
	ChannelsLRCLfeLsRsSlSr                 // 8 channels: left, right, center, LFE, left surround, right surround, side left, side right.
	ChannelsLeftSide                       // 2 channels: left, side; using inter-channel decorrelation.
	ChannelsSideRight                      // 2 channels: side, right; using inter-channel decorrelation.
	ChannelsMidSide                        // 2 channels: mid, side; using inter-channel decorrelation.
)

// nChannels specifies the number of channels used by each channel assignment.
var nChannels = [...]int{
	ChannelsMono:           1,
	ChannelsLR:             2,
	ChannelsLRC:            3,
	ChannelsLRLsRs:         4,
	ChannelsLRCLsRs:        5,
	ChannelsLRCLfeLsRs:     6,
	ChannelsLRCLfeCsSlSr:   7,
	ChannelsLRCLfeLsRsSlSr: 8,
	ChannelsLeftSide:       2,
	ChannelsSideRight:      2,
	ChannelsMidSide:        2,
}

// Count returns the number of channels (subframes) used by the provided channel
// assignment.
func (channels Channels) Count() int {
	return nChannels[channels]
}

// Correlate reverts any inter-channel decorrelation between the samples of the
// subframes.
//
// An encoder decorrelates audio samples as follows:
//
//	mid = (left + right)/2
//	side = left - right
func (frame *Frame) Correlate() {
	switch frame.Channels {
	case ChannelsLeftSide:
		// 2 channels: left, side; using inter-channel decorrelation.
		left := frame.Subframes[0].Samples
		side := frame.Subframes[1].Samples
		for i := range side {
			// right = left - side
			side[i] = left[i] - side[i]
		}
	case ChannelsSideRight:
		// 2 channels: side, right; using inter-channel decorrelation.
		side := frame.Subframes[0].Samples
		right := frame.Subframes[1].Samples
		for i := range side {
			// left = right + side
			side[i] = right[i] + side[i]
		}
	case ChannelsMidSide:
		// 2 channels: mid, side; using inter-channel decorrelation.
		mid := frame.Subframes[0].Samples
		side := frame.Subframes[1].Samples
		for i := range side {
			// left = (2*mid + side)/2
			// right = (2*mid - side)/2
			//
			// Use int64 to avoid overflow: for 32bps audio, mid values can
			// reach 2^31-1 and m*2 overflows int32, producing wrong output.
			m := int64(mid[i])
			s := int64(side[i])
			m <<= 1
			// Notice that the integer division in mid = (left + right)/2 discards
			// the least significant bit. It can be reconstructed however, since a
			// sum A+B and a difference A-B has the same least significant bit.
			//
			// ref: Data Compression: The Complete Reference (ch. 7, Decorrelation)
			m |= s & 1
			mid[i] = int32((m + s) >> 1)
			side[i] = int32((m - s) >> 1)
		}
	}
}

// Decorrelate performs inter-channel decorrelation between the samples of the
// subframes.
//
// An encoder decorrelates audio samples as follows:
//
//	mid = (left + right)/2
//	side = left - right
func (frame *Frame) Decorrelate() {
	switch frame.Channels {
	case ChannelsLeftSide:
		// 2 channels: left, side; using inter-channel decorrelation.
		left := frame.Subframes[0].Samples  // already left; no change after inter-channel decorrelation.
		right := frame.Subframes[1].Samples // set to side after inter-channel decorrelation.
		for i := range left {
			l := left[i]
			r := right[i]
			// inter-channel decorrelation:
			//	side = left - right
			side := l - r
			right[i] = side
		}
	case ChannelsSideRight:
		// 2 channels: side, right; using inter-channel decorrelation.
		left := frame.Subframes[0].Samples  // set to side after inter-channel decorrelation.
		right := frame.Subframes[1].Samples // already right; no change after inter-channel decorrelation.
		for i := range left {
			l := left[i]
			r := right[i]
			// inter-channel decorrelation:
			//	side = left - right
			side := l - r
			left[i] = side
		}
	case ChannelsMidSide:
		// 2 channels: mid, side; using inter-channel decorrelation.
		left := frame.Subframes[0].Samples  // set to mid after inter-channel decorrelation.
		right := frame.Subframes[1].Samples // set to side after inter-channel decorrelation.
		for i := range left {
			// inter-channel decorrelation:
			//	mid = (left + right)/2
			//	side = left - right
			l := left[i]
			r := right[i]
			mid := int32((int64(l) + int64(r)) >> 1) // NOTE: using `(left + right) >> 1`, not the same as `(left + right) / 2`.
			side := l - r
			left[i] = mid
			right[i] = side
		}
	}
}

// SampleNumber returns the first sample number contained within the frame.
func (frame *Frame) SampleNumber() uint64 {
	if frame.HasFixedBlockSize {
		return frame.Num * uint64(frame.BlockSize)
	}
	return frame.Num
}

// unexpected returns io.ErrUnexpectedEOF if err is io.EOF, and returns err
// otherwise.
func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
