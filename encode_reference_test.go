package flac_test

// The encoder against the reference decoder. PCM at every depth the format
// has, every channel count, several block sizes, with and without the
// encoder's prediction analysis, and the stereo decorrelations on request,
// is written through the encoder; `flac -t` must accept the stream, which
// checks the MD5 the encoder wrote against what libFLAC decodes, and at
// the depths the reference writes raw the decoded PCM must be the source.

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// signal is one block of per-channel samples: a tone with the low bits busy,
// and in its second half the depth's extremes alternating, which is where
// predictors overflow if they can.
func signal(bps, channels, n, seed int) [][]int32 {
	maxV := int64(1)<<(bps-1) - 1
	minV := -maxV - 1
	out := make([][]int32, channels)

	for c := range out {
		out[c] = make([]int32, n)
		for i := range n {
			if i >= n/2 && n >= 32 {
				if (i+c)%2 == 0 {
					out[c][i] = int32(maxV)
				} else {
					out[c][i] = int32(minV)
				}

				continue
			}

			phase := 2 * math.Pi * float64(i+seed) * (0.013 * float64(c+1))
			v := int64(0.7*float64(maxV)*math.Sin(phase)) + int64((i*7919+c*31)%97) - 48
			out[c][i] = int32(max(minV, min(maxV, v)))
		}
	}

	return out
}

// interleaved is the samples as little-endian signed PCM of bps/8 bytes.
func interleaved(blocks [][][]int32, bps int) []byte {
	bytesPer := bps / 8

	var out []byte

	for _, block := range blocks {
		for i := range block[0] {
			for c := range block {
				var b [4]byte

				binary.LittleEndian.PutUint32(b[:], uint32(block[c][i]))
				out = append(out, b[:bytesPer]...)
			}
		}
	}

	return out
}

func channelsFor(channels int) frame.Channels {
	return frame.Channels(channels - 1)
}

func TestEncoderAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	type variant struct {
		name       string
		channels   int
		assignment frame.Channels
		analysis   bool
	}

	var variants []variant

	for channels := 1; channels <= 8; channels++ {
		for _, analysis := range []bool{false, true} {
			variants = append(
				variants,
				variant{fmt.Sprintf("%dch/analysis=%v", channels, analysis), channels, channelsFor(channels), analysis},
			)
		}
	}

	for _, a := range []frame.Channels{frame.ChannelsLeftSide, frame.ChannelsSideRight, frame.ChannelsMidSide} {
		for _, analysis := range []bool{false, true} {
			variants = append(variants, variant{fmt.Sprintf("stereo-%d/analysis=%v", a, analysis), 2, a, analysis})
		}
	}

	for _, bps := range []int{4, 8, 12, 16, 20, 24, 32} {
		for _, blockSize := range []int{16, 1152, 4096} {
			for _, v := range variants {
				t.Run(fmt.Sprintf("%dbit/block%d/%s", bps, blockSize, v.name), func(t *testing.T) {
					t.Parallel()

					path := filepath.Join(t.TempDir(), "encoded.flac")

					file, err := os.Create(path)
					if err != nil {
						t.Fatal(err)
					}

					info := &meta.StreamInfo{
						BlockSizeMin: uint16(blockSize), BlockSizeMax: uint16(blockSize),
						SampleRate: 48000, NChannels: uint8(v.channels), BitsPerSample: uint8(bps),
					}

					enc, err := flac.NewEncoder(file, info)
					if err != nil {
						t.Fatal(err)
					}

					enc.EnablePredictionAnalysis(v.analysis)

					var blocks [][][]int32

					for k := range 3 {
						block := signal(bps, v.channels, blockSize, k*blockSize)
						blocks = append(blocks, block)

						subframes := make([]*frame.Subframe, v.channels)
						for c := range subframes {
							subframes[c] = &frame.Subframe{
								SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
								Samples:   append([]int32(nil), block[c]...),
								NSamples:  blockSize,
							}
						}

						f := &frame.Frame{
							Header: frame.Header{
								HasFixedBlockSize: true, BlockSize: uint16(blockSize), SampleRate: 48000,
								Channels: v.assignment, BitsPerSample: uint8(bps),
							},
							Subframes: subframes,
						}

						if writeErr := enc.WriteFrame(f); writeErr != nil {
							t.Fatalf("WriteFrame %d: %v", k, writeErr)
						}
					}

					if closeErr := enc.Close(); closeErr != nil {
						t.Fatalf("Close: %v", closeErr)
					}

					if out, refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).
						CombinedOutput(); refErr != nil {
						t.Fatalf("the reference refuses the stream: %v\n%s", refErr, out)
					}

					if bps%8 != 0 {
						return
					}

					out, err := exec.CommandContext(
						t.Context(),
						flacBin,
						"-d",
						"-s",
						"-f",
						"--force-raw-format",
						"--sign=signed",
						"--endian=little",
						"-o",
						"-",
						path,
					).Output()
					if err != nil {
						t.Fatalf("flac -d: %v", err)
					}

					if want := interleaved(blocks, bps); !bytes.Equal(out, want) {
						at := 0
						for at < len(out) && at < len(want) && out[at] == want[at] {
							at++
						}

						t.Fatalf(
							"the reference decodes %d bytes, the source is %d; first difference at byte %d",
							len(out),
							len(want),
							at,
						)
					}
				})
			}
		}
	}
}
