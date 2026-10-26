package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"fmt"
	"io"
	"os"

	upstream "github.com/mewkiz/flac"

	fork "github.com/mycophonic/flac"
)

// StreamInfo holds the STREAMINFO fields both libraries expose.
type StreamInfo struct {
	BlockSizeMin  uint16 `json:"block_size_min"`
	BlockSizeMax  uint16 `json:"block_size_max"`
	FrameSizeMin  uint32 `json:"frame_size_min"`
	FrameSizeMax  uint32 `json:"frame_size_max"`
	SampleRate    uint32 `json:"sample_rate"`
	NChannels     uint8  `json:"channels"`
	BitsPerSample uint8  `json:"bits_per_sample"`
	NSamples      uint64 `json:"total_samples"`
	MD5           string `json:"md5"`
}

// Frame is one decoded frame. Samples may be borrowed by the decoder and
// stay valid only until the next call to Next.
type Frame struct {
	Num        uint64
	BlockSize  int
	SampleRate uint32
	Bits       int
	Assignment string
	Samples    [][]int32
}

// Decoder is the part of both libraries' Stream the harness drives.
type Decoder interface {
	Info() StreamInfo
	Next() (Frame, error) // io.EOF at the end of the stream
}

// SeekDecoder can also seek to a sample.
type SeekDecoder interface {
	Decoder
	Seek(sample uint64) (uint64, error)
}

var decoderNames = []string{"fork", "upstream"}

func assignmentName(v uint8) string {
	switch {
	case v <= 7:
		return "independent"
	case v == 8:
		return "left-side"
	case v == 9:
		return "side-right"
	case v == 10:
		return "mid-side"
	}

	return fmt.Sprintf("reserved-%d", v)
}

func openDecoder(name string, r io.Reader) (Decoder, error) {
	switch name {
	case "fork":
		s, err := fork.New(r)
		if err != nil {
			return nil, err
		}

		return &forkDecoder{s: s}, nil
	case "upstream":
		s, err := upstream.New(r)
		if err != nil {
			return nil, err
		}

		return &upstreamDecoder{s: s}, nil
	}

	return nil, fmt.Errorf("unknown decoder %q", name)
}

func openSeekDecoder(name string, rs io.ReadSeeker) (SeekDecoder, error) {
	switch name {
	case "fork":
		s, err := fork.NewSeek(rs)
		if err != nil {
			return nil, err
		}

		return &forkDecoder{s: s}, nil
	case "upstream":
		s, err := upstream.NewSeek(rs)
		if err != nil {
			return nil, err
		}

		return &upstreamDecoder{s: s}, nil
	}

	return nil, fmt.Errorf("unknown decoder %q", name)
}

type forkDecoder struct {
	s   *fork.Stream
	buf [][]int32
}

func (d *forkDecoder) Info() StreamInfo {
	i := d.s.Info

	return StreamInfo{
		i.BlockSizeMin, i.BlockSizeMax, i.FrameSizeMin, i.FrameSizeMax, i.SampleRate,
		i.NChannels, i.BitsPerSample, i.NSamples, hex.EncodeToString(i.MD5sum[:]),
	}
}

func (d *forkDecoder) Next() (Frame, error) {
	f, err := d.s.ParseNext()
	if err != nil {
		return Frame{}, err
	}

	fr := Frame{
		Num: f.Num, BlockSize: int(f.BlockSize), SampleRate: f.SampleRate, Bits: int(f.BitsPerSample),
		Assignment: assignmentName(uint8(f.Channels)),
	}

	d.buf = d.buf[:0]
	for _, sf := range f.Subframes {
		d.buf = append(d.buf, sf.Samples)
	}

	fr.Samples = d.buf

	return fr, nil
}

func (d *forkDecoder) Seek(sample uint64) (uint64, error) { return d.s.Seek(sample) }

type upstreamDecoder struct {
	s   *upstream.Stream
	buf [][]int32
}

func (d *upstreamDecoder) Info() StreamInfo {
	i := d.s.Info

	return StreamInfo{
		i.BlockSizeMin, i.BlockSizeMax, i.FrameSizeMin, i.FrameSizeMax, i.SampleRate,
		i.NChannels, i.BitsPerSample, i.NSamples, hex.EncodeToString(i.MD5sum[:]),
	}
}

func (d *upstreamDecoder) Next() (Frame, error) {
	f, err := d.s.ParseNext()
	if err != nil {
		return Frame{}, err
	}

	fr := Frame{
		Num: f.Num, BlockSize: int(f.BlockSize), SampleRate: f.SampleRate, Bits: int(f.BitsPerSample),
		Assignment: assignmentName(uint8(f.Channels)),
	}

	d.buf = d.buf[:0]
	for _, sf := range f.Subframes {
		d.buf = append(d.buf, sf.Samples)
	}

	fr.Samples = d.buf

	return fr, nil
}

func (d *upstreamDecoder) Seek(sample uint64) (uint64, error) { return d.s.Seek(sample) }

// sampleWidth is libFLAC's raw output width for a depth: whole bytes.
func sampleWidth(bps int) int {
	w := (bps + 7) / 8
	if w < 1 {
		w = 1
	}

	if w > 4 {
		w = 4
	}

	return w
}

// frameShapeOK reports whether a frame holds one channel of BlockSize
// samples per subframe, as a caller interleaving it assumes.
func frameShapeOK(f Frame, channels int) bool {
	if f.BlockSize < 1 || len(f.Samples) == 0 || (channels > 0 && len(f.Samples) != channels) {
		return false
	}

	for _, ch := range f.Samples {
		if len(ch) != f.BlockSize {
			return false
		}
	}

	return true
}

// pack interleaves a frame as libFLAC's raw output does: signed,
// little-endian, width bytes per sample. A short channel packs as zeros
// (frameShapeOK reports it).
func pack(dst []byte, f Frame, width int) []byte {
	dst = dst[:0]

	for i := 0; i < f.BlockSize; i++ {
		for _, ch := range f.Samples {
			var v int32
			if i < len(ch) {
				v = ch[i]
			}

			u := uint32(v)
			for k := 0; k < width; k++ {
				dst = append(dst, byte(u))
				u >>= 8
			}
		}
	}

	return dst
}

// unpack reads one sample of width bytes, sign-extended.
func unpack(b []byte) int64 {
	var u uint64
	for k := len(b) - 1; k >= 0; k-- {
		u = u<<8 | uint64(b[k])
	}

	shift := 64 - 8*uint(len(b))

	return int64(u<<shift) >> shift
}

// inRange reports whether v fits a signed bps-bit sample.
func inRange(v int32, bps int) bool {
	if bps <= 0 || bps >= 32 {
		return true
	}

	lim := int32(1) << (bps - 1)

	return v >= -lim && v < lim
}

// firstPCMDiff locates the first sample where two interleaved PCM buffers
// differ; Index counts sample frames from the start of the stream.
func firstPCMDiff(got, want []byte, width, nch int) *Diff {
	n := min(len(got), len(want))
	for i := 0; i+width <= n; i += width {
		if !bytes.Equal(got[i:i+width], want[i:i+width]) {
			k := i / width
			w := unpack(want[i : i+width])

			return &Diff{Kind: "sample", Index: k / nch, Channel: k % nch, Got: unpack(got[i : i+width]), Want: &w}
		}
	}

	if len(got) != len(want) {
		return &Diff{Kind: "length", Note: fmt.Sprintf("%d PCM bytes, want %d", len(got), len(want))}
	}

	return nil
}

// metadataErrors opens path with the two constructors that parse every
// metadata block, Parse and NewSeek (New skips the bodies it does not need),
// and returns their errors.
func metadataErrors(name, path string) (parseErr, newSeekErr string) {
	f, err := os.Open(path)
	if err != nil {
		return err.Error(), err.Error()
	}
	defer f.Close()

	try := func(fn func() error) (s string) {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err.Error()
		}

		defer func() {
			if p := recover(); p != nil {
				s = clip(fmt.Sprintf("panic: %v", p), 300)
			}
		}()

		if err := fn(); err != nil {
			return clip(err.Error(), 300)
		}

		return ""
	}
	if name == "fork" {
		parseErr = try(func() error { _, err := fork.Parse(bufio.NewReaderSize(f, 64<<10)); return err })
		newSeekErr = try(func() error { _, err := fork.NewSeek(f); return err })
	} else {
		parseErr = try(func() error { _, err := upstream.Parse(bufio.NewReaderSize(f, 64<<10)); return err })
		newSeekErr = try(func() error { _, err := upstream.NewSeek(f); return err })
	}

	return parseErr, newSeekErr
}
