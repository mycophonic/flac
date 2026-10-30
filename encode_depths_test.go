package flac_test

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// The format allows every depth from 4 to 32 bits (RFC 9639, section 8.2)
// and codes six of them in the frame header; the rest the header defers to
// STREAMINFO, as 4-bit streams have been written since #24. Every such depth,
// mono and stereo, is written and accepted by flac -t, whose MD5 check holds
// the stream's hash to the samples at that width, and decodes here to the
// input. Before, the encoder refused them as "not yet implemented".
func TestEncoderWritesEveryUncodedDepth(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	const blockSize = 192

	for _, bps := range []int{5, 6, 7, 9, 10, 11, 13, 14, 15, 17, 18, 19, 21, 22, 23, 25, 26, 27, 28, 29, 30, 31} {
		for _, channels := range []int{1, 2} {
			t.Run(itoa(bps)+"bit/ch"+itoa(channels), func(t *testing.T) {
				t.Parallel()

				path := filepath.Join(t.TempDir(), "depth.flac")

				file, err := os.Create(path)
				if err != nil {
					t.Fatal(err)
				}

				info := &meta.StreamInfo{
					BlockSizeMin: blockSize, BlockSizeMax: blockSize,
					SampleRate: 48000, NChannels: uint8(channels), BitsPerSample: uint8(bps),
				}

				enc, err := flac.NewEncoder(file, info)
				if err != nil {
					t.Fatal(err)
				}

				var blocks [][][]int32

				for k := range 3 {
					block := signal(bps, channels, blockSize, k*blockSize)
					blocks = append(blocks, block)

					subframes := make([]*frame.Subframe, channels)
					for c := range subframes {
						subframes[c] = &frame.Subframe{
							SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
							Samples:   append([]int32(nil), block[c]...),
							NSamples:  blockSize,
						}
					}

					f := &frame.Frame{
						Header: frame.Header{
							HasFixedBlockSize: true, BlockSize: blockSize, SampleRate: 48000,
							Channels: channelsFor(channels), BitsPerSample: uint8(bps),
						},
						Subframes: subframes,
					}

					if writeErr := enc.WriteFrame(f); writeErr != nil {
						t.Fatalf("WriteFrame: %v", writeErr)
					}
				}

				if closeErr := enc.Close(); closeErr != nil {
					t.Fatalf("Close: %v", closeErr)
				}

				if out, refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).
					CombinedOutput(); refErr != nil {
					t.Fatalf("flac -t refuses the %d-bit stream: %v\n%s", bps, refErr, out)
				}

				again, err := flac.ParseFile(path)
				if err != nil {
					t.Fatalf("ParseFile: %v", err)
				}

				defer func() { _ = again.Close() }()

				if again.Info.BitsPerSample != uint8(bps) {
					t.Fatalf("STREAMINFO says %d bits, want %d", again.Info.BitsPerSample, bps)
				}

				for k := range 3 {
					f, parseErr := again.ParseNext()
					if parseErr != nil {
						t.Fatalf("frame %d: %v", k, parseErr)
					}

					if int(f.BitsPerSample) != bps {
						t.Fatalf("frame %d reads %d bits, want %d", k, f.BitsPerSample, bps)
					}

					for c := range channels {
						if !bytes.Equal(
							interleaved([][][]int32{{f.Subframes[c].Samples}}, 32),
							interleaved([][][]int32{{blocks[k][c]}}, 32),
						) {
							t.Fatalf("frame %d channel %d differs from the input", k, c)
						}
					}
				}
			})
		}
	}
}

// A size the format does not have, below 4 or above 32 bits, is refused by
// the encoder rather than written with a code that means something else.
func TestEncoderRefusesADepthOutsideTheFormat(t *testing.T) {
	t.Parallel()

	for _, bps := range []int{3, 33} {
		info := &meta.StreamInfo{
			BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 48000, NChannels: 1, BitsPerSample: uint8(bps),
		}

		enc, err := flac.NewEncoder(io.Discard, info)
		if err != nil {
			t.Fatalf("%d bits: NewEncoder: %v", bps, err)
		}

		f := &frame.Frame{
			Header: frame.Header{
				HasFixedBlockSize: true, BlockSize: 16, SampleRate: 48000,
				Channels: frame.ChannelsMono, BitsPerSample: uint8(bps),
			},
			Subframes: []*frame.Subframe{{
				SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: 16, Samples: make([]int32, 16),
			}},
		}

		if err := enc.WriteFrame(f); err == nil {
			t.Fatalf("%d bits: WriteFrame accepted a size the format does not have", bps)
		}
	}
}

func itoa(n int) string {
	return string(rune('0'+n/10%10)) + string(rune('0'+n%10))
}
