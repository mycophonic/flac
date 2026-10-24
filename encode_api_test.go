package flac_test

// The encoder's own edges. A frame the encoder cannot write is an error,
// not a panic and not a stream the reference refuses: a predictor order
// above the block, coefficients short of the order, a precision or shift
// the format cannot carry, a prediction method that does not exist, wasted
// bits at the depth, and samples beyond the depth, which no header can
// announce and the MD5 would expose.

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// writeOne writes one mono 16-bit frame of 16 samples shaped by change
// through a fresh encoder into path, returning WriteFrame's or Close's
// error, or errPanic.
func writeOne(path string, change func(f *frame.Frame)) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errPanic, r)
		}
	}()

	file, err := os.Create(path)
	if err != nil {
		return err
	}

	info := &meta.StreamInfo{BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 16}

	enc, err := flac.NewEncoder(file, info)
	if err != nil {
		return err
	}

	enc.EnablePredictionAnalysis(false)

	samples := make([]int32, 16)
	for i := range samples {
		samples[i] = int32(i*500 - 4000)
	}

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true,
			BlockSize:         16,
			SampleRate:        44100,
			Channels:          frame.ChannelsMono,
			BitsPerSample:     16,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
			Samples:   samples,
			NSamples:  16,
		}},
	}

	change(f)

	if err := enc.WriteFrame(f); err != nil {
		return err
	}

	return enc.Close()
}

func TestWriteFrameRefusesWhatItCannotWrite(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	// With analysis off the caller supplies the rice partitions too; one
	// partition of parameter 4 fits these samples.
	fir := func(order, coeffs int, prec uint, shift int32) func(f *frame.Frame) {
		return func(f *frame.Frame) {
			s := f.Subframes[0]
			s.Pred, s.Order, s.CoeffPrec, s.CoeffShift = frame.PredFIR, order, prec, shift

			s.Coeffs = make([]int32, coeffs)
			for i := range s.Coeffs {
				s.Coeffs[i] = 1
			}

			s.RiceSubframe = &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 4}}}
		}
	}

	cases := []struct {
		name   string
		change func(f *frame.Frame)
	}{
		{"valid verbatim", func(*frame.Frame) {}},
		{"valid LPC order 2", fir(2, 2, 12, 2)},
		{"LPC order 20 on 16 samples", fir(20, 20, 12, 2)},
		{"fixed order 4 on 2 samples", func(f *frame.Frame) {
			s := f.Subframes[0]
			s.Pred, s.Order = frame.PredFixed, 4
			s.Samples, s.NSamples, f.BlockSize = s.Samples[:2], 2, 2
		}},
		{"LPC coefficients short of the order", fir(4, 2, 12, 2)},
		{"LPC without rice partitions", func(f *frame.Frame) {
			fir(2, 2, 12, 2)(f)
			f.Subframes[0].RiceSubframe = nil
		}},
		{"LPC precision 0", fir(2, 2, 0, 2)},
		{"LPC precision 16", fir(2, 2, 16, 2)},
		{"LPC shift 16 (five bits hold -16..15)", fir(2, 2, 12, 16)},
		{"LPC shift -1", fir(2, 2, 12, -1)},
		{"prediction method 99", func(f *frame.Frame) { f.Subframes[0].Pred = frame.Pred(99) }},
		{"wasted bits at the depth", func(f *frame.Frame) { f.Subframes[0].Wasted = 16 }},
		{"a sample beyond 16 bits", func(f *frame.Frame) { f.Subframes[0].Samples[3] = 70000 }},
		{"a sample at -2^31 in a 16-bit frame", func(f *frame.Frame) { f.Subframes[0].Samples[3] = -1 << 31 }},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "frame.flac")

			err := writeOne(path, c.change)
			if errors.Is(err, errPanic) {
				t.Fatal(err)
			}

			if err != nil {
				t.Logf("refused: %v", err)

				return
			}

			out, refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).CombinedOutput()
			if refErr != nil {
				t.Fatalf("the encoder wrote a stream the reference refuses: %v\n%s", refErr, bytes.TrimSpace(out))
			}
		})
	}
}
