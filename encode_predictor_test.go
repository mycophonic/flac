package flac_test

import (
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// writeMono16 writes one 16-bit mono frame of 16 samples with the given
// subframe through the encoder, analysis off, and returns WriteFrame's error.
func writeMono16(t *testing.T, sub *frame.Subframe) error {
	t.Helper()

	info := &meta.StreamInfo{
		BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 16,
	}

	enc, err := flac.NewEncoder(io.Discard, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{sub},
	}

	return enc.WriteFrame(f)
}

func ramp16() []int32 {
	s := make([]int32, 16)
	for i := range s {
		s[i] = int32(i * 100)
	}

	return s
}

// A predictor subframe whose fields the stream cannot carry, or without its
// Rice partitions, is refused with an error: each wrote a stream the
// reference refuses, or panicked on a nil RiceSubframe. A negative shift
// and a coefficient count other than the order were refused already, by
// getLPCResiduals.
func TestWriteFrame_RefusesPredictorFieldsItCannotWrite(t *testing.T) {
	t.Parallel()

	rice := &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 4}}}

	cases := []struct {
		name string
		sub  frame.SubHeader
	}{
		{
			"FIR without Rice partitions",
			frame.SubHeader{Pred: frame.PredFIR, Order: 1, CoeffPrec: 12, Coeffs: []int32{1}},
		},
		{"fixed without Rice partitions", frame.SubHeader{Pred: frame.PredFixed, Order: 1}},
		{
			"coefficient precision 16",
			frame.SubHeader{Pred: frame.PredFIR, Order: 1, CoeffPrec: 16, Coeffs: []int32{1}, RiceSubframe: rice},
		},
		{
			"coefficient precision 0",
			frame.SubHeader{Pred: frame.PredFIR, Order: 1, CoeffPrec: 0, Coeffs: []int32{1}, RiceSubframe: rice},
		},
		{
			"coefficient shift 16",
			frame.SubHeader{
				Pred: frame.PredFIR, Order: 1, CoeffPrec: 12, CoeffShift: 16,
				Coeffs: []int32{1}, RiceSubframe: rice,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			err := writeMono16(t, &frame.Subframe{SubHeader: c.sub, NSamples: 16, Samples: ramp16()})
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("WriteFrame accepted %s: %v", c.name, err)
			}
		})
	}
}

// The same subframe with its fields in range is written.
func TestWriteFrame_WritesAPredictorInRange(t *testing.T) {
	t.Parallel()

	sub := frame.SubHeader{
		Pred: frame.PredFIR, Order: 1, CoeffPrec: 12, CoeffShift: 2, Coeffs: []int32{1 << 2},
		RiceSubframe: &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 4}}},
	}

	if err := writeMono16(t, &frame.Subframe{SubHeader: sub, NSamples: 16, Samples: ramp16()}); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}
}
