package flac_test

import (
	"bytes"
	"crypto/md5"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// encodeMatrixCase is one encode: a stream of n samples at a depth, with an
// assignment, cut into blocks of blockSize (the last one shorter).
type encodeMatrixCase struct {
	n, bps, channels, blockSize int
	assignment                  frame.Channels
	analysis                    bool
}

func (c encodeMatrixCase) name() string {
	return fmt.Sprintf("%dbit/%dsamples/ch%d-%d/block%d/analysis=%v",
		c.bps, c.n, c.channels, c.assignment, c.blockSize, c.analysis)
}

// writeMatrixStream writes the case's stream to path and returns the samples
// written, per block, and the frame count.
func writeMatrixStream(t *testing.T, path string, c encodeMatrixCase) (blocks [][][]int32, frames int) {
	t.Helper()

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	info := &meta.StreamInfo{
		BlockSizeMin: uint16(c.blockSize), BlockSizeMax: uint16(c.blockSize),
		SampleRate: 48000, NChannels: uint8(c.channels), BitsPerSample: uint8(c.bps),
	}

	enc, err := flac.NewEncoder(file, info)
	if err != nil {
		t.Fatal(err)
	}

	enc.EnablePredictionAnalysis(c.analysis)

	for off := 0; off < c.n; off += c.blockSize {
		k := min(c.blockSize, c.n-off)
		block := signal(c.bps, c.channels, k, off)
		blocks = append(blocks, block)

		subframes := make([]*frame.Subframe, c.channels)
		for ch := range subframes {
			subframes[ch] = &frame.Subframe{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
				Samples:   append([]int32(nil), block[ch]...),
				NSamples:  k,
			}
		}

		f := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true, BlockSize: uint16(k), SampleRate: 48000,
				Channels: c.assignment, BitsPerSample: uint8(c.bps),
			},
			Subframes: subframes,
		}

		if writeErr := enc.WriteFrame(f); writeErr != nil {
			t.Fatalf("WriteFrame at %d: %v", off, writeErr)
		}

		frames++
	}

	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	return blocks, frames
}

// checkMatrixStreamInfo holds the rewritten STREAMINFO to the stream: the
// sample count, the MD5 of the samples, block sizes of 16 or more with the
// minimum excluding the last block, and frame sizes that are measured.
func checkMatrixStreamInfo(t *testing.T, info *meta.StreamInfo, c encodeMatrixCase, blocks [][][]int32, frames int) {
	t.Helper()

	if info.NSamples != uint64(c.n) {
		t.Fatalf("STREAMINFO counts %d samples, want %d", info.NSamples, c.n)
	}

	if sum := md5.Sum(interleaved(blocks, c.bps)); sum != info.MD5sum {
		t.Fatalf("STREAMINFO MD5 %x, the samples hash to %x", info.MD5sum, sum)
	}

	if info.BlockSizeMin < 16 || info.BlockSizeMax < 16 || info.BlockSizeMin > info.BlockSizeMax {
		t.Fatalf("STREAMINFO block sizes %d..%d", info.BlockSizeMin, info.BlockSizeMax)
	}

	if frames > 1 && (int(info.BlockSizeMin) != c.blockSize || int(info.BlockSizeMax) != c.blockSize) {
		t.Fatalf(
			"STREAMINFO block sizes %d..%d, want %d..%d",
			info.BlockSizeMin,
			info.BlockSizeMax,
			c.blockSize,
			c.blockSize,
		)
	}

	if info.FrameSizeMin == 0 || info.FrameSizeMax < info.FrameSizeMin {
		t.Fatalf("STREAMINFO frame sizes %d..%d", info.FrameSizeMin, info.FrameSizeMax)
	}
}

// The encoder matrix the harness ran: stream lengths that are not a multiple
// of the block size (1, 15 and 17 samples; one block and some; many), three
// depths, mono and the four stereo assignments, three block sizes, analysis
// on and off. Each stream is accepted by flac -t, decodes to the input through
// flac -d and through this decoder, and carries a STREAMINFO that matches.
// The lengths below 16 are #40; the 32-bit decorrelated assignments are #11.
func TestEncoderMatrixAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	var cases []encodeMatrixCase

	for _, n := range []int{1, 15, 17, 4097, 12345} {
		for _, bps := range []int{16, 24, 32} {
			for _, blockSize := range []int{4096, 192, 16} {
				for _, analysis := range []bool{false, true} {
					cases = append(cases, encodeMatrixCase{n, bps, 1, blockSize, frame.ChannelsMono, analysis})

					for _, a := range []frame.Channels{
						frame.ChannelsLR, frame.ChannelsLeftSide, frame.ChannelsSideRight, frame.ChannelsMidSide,
					} {
						cases = append(cases, encodeMatrixCase{n, bps, 2, blockSize, a, analysis})
					}
				}
			}
		}
	}

	for _, c := range cases {
		t.Run(c.name(), func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "matrix.flac")
			blocks, frames := writeMatrixStream(t, path, c)
			want := interleaved(blocks, c.bps)

			if out, refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).
				CombinedOutput(); refErr != nil {
				t.Fatalf("flac -t refuses the stream: %v\n%s", refErr, out)
			}

			got, refErr := referenceRaw(t, flacBin, path)
			if refErr != nil {
				t.Fatalf("flac -d: %v", refErr)
			}

			if !bytes.Equal(got, want) {
				t.Fatalf("flac -d decodes %d bytes differing from the %d written", len(got), len(want))
			}

			ours := decodeWhole(path)
			if ours.err != nil {
				t.Fatalf("this decoder: %v", ours.err)
			}

			if !bytes.Equal(ours.pcm, want) {
				t.Fatalf("this decoder decodes %d bytes differing from the %d written", len(ours.pcm), len(want))
			}

			again, openErr := flac.Open(path)
			if openErr != nil {
				t.Fatalf("Open: %v", openErr)
			}

			info := *again.Info

			_ = again.Close()

			checkMatrixStreamInfo(t, &info, c, blocks, frames)
		})
	}
}

// writeWAV writes blocks as a RIFF WAVE file of the depth, little-endian
// signed PCM, so flac can encode them.
func writeWAV(t *testing.T, path string, blocks [][][]int32, bps, channels, rate int) {
	t.Helper()

	pcm := interleaved(blocks, bps)
	bytesPer := bps / 8

	var b bytes.Buffer

	put32 := func(v uint32) { b.Write([]byte{byte(v), byte(v >> 8), byte(v >> 16), byte(v >> 24)}) }
	put16 := func(v uint16) { b.Write([]byte{byte(v), byte(v >> 8)}) }

	b.WriteString("RIFF")
	put32(uint32(36 + len(pcm)))
	b.WriteString("WAVEfmt ")
	put32(16)
	put16(1)
	put16(uint16(channels))
	put32(uint32(rate))
	put32(uint32(rate * channels * bytesPer))
	put16(uint16(channels * bytesPer))
	put16(uint16(bps))
	b.WriteString("data")
	put32(uint32(len(pcm)))
	b.Write(pcm)

	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// The 32-bit decode matrix the harness ran: 32-bit stereo signals encoded by
// the reference under its own settings (compression levels, mid-side and
// adaptive mid-side, exhaustive model search, fixed and LPC orders at the
// lax limits, block sizes, Rice partition orders, independent channels), each
// decoded here to the samples flac -d gives. #10 was wrong samples without
// an error on this path.
func TestDecoderMatrixAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	settings := map[string][]string{
		"m8":    {"-8", "-m"},
		"M5":    {"-5", "-M"},
		"fx":    {"-l", "0", "-m", "--lax"},
		"e8":    {"-8", "-e", "-p", "-m"},
		"l32":   {"-l", "32", "-m", "--lax"},
		"b16":   {"-8", "-m", "--blocksize=16"},
		"b192":  {"-8", "-m", "--blocksize=192"},
		"b4608": {"-8", "-m", "--blocksize=4608"},
		"r8":    {"-8", "-m", "-r", "8"},
		"ind":   {"-8", "--no-mid-side"},
	}

	for _, n := range []int{17, 4097, 20000} {
		for name, args := range settings {
			t.Run(fmt.Sprintf("%dsamples/%s", n, name), func(t *testing.T) {
				t.Parallel()

				dir := t.TempDir()
				wav := filepath.Join(dir, "in.wav")
				out := filepath.Join(dir, "out.flac")
				blocks := [][][]int32{signal(32, 2, n, 7)}

				writeWAV(t, wav, blocks, 32, 2, 48000)

				encArgs := append(append([]string{"-s", "-f"}, args...), "-o", out, wav)
				if msg, encErr := exec.CommandContext(t.Context(), flacBin, encArgs...).
					CombinedOutput(); encErr != nil {
					t.Skipf("the reference refuses to encode with %v: %v\n%s", args, encErr, msg)
				}

				want, refErr := referenceRaw(t, flacBin, out)
				if refErr != nil {
					t.Fatalf("flac -d: %v", refErr)
				}

				ours := decodeWhole(out)
				if ours.err != nil && !errors.Is(ours.err, io.EOF) {
					t.Fatalf("this decoder: %v", ours.err)
				}

				if !bytes.Equal(ours.pcm, want) {
					t.Fatalf(
						"this decoder decodes %d bytes differing from the reference's %d",
						len(ours.pcm),
						len(want),
					)
				}
			})
		}
	}
}
