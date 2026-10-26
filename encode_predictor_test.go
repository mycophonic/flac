package flac_test

import (
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
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
		// A Rice parameter beyond its field, or an escaped partition's bit
		// depth beyond its 5 bits, was written masked: 16 in 4 bits as 0, 32
		// in 5 bits as 0, and the stream decoded to other residuals (#48).
		{
			"Rice parameter 16 in a 4-bit field",
			frame.SubHeader{
				Pred: frame.PredFixed, Order: 1,
				RiceSubframe: &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 16}}},
			},
		},
		{
			"Rice parameter 32 in a 5-bit field",
			frame.SubHeader{
				Pred: frame.PredFixed, Order: 1, ResidualCodingMethod: frame.ResidualCodingMethodRice2,
				RiceSubframe: &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 32}}},
			},
		},
		{
			"escaped partition of 32 bits per sample",
			frame.SubHeader{
				Pred: frame.PredFixed, Order: 1,
				RiceSubframe: &frame.RiceSubframe{
					Partitions: []frame.RicePartition{{Param: 15, EscapedBitsPerSample: 32}},
				},
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

// The same subframe with its fields in range is written; so is a partition
// coded with a parameter that carries a stray escaped depth, which only the
// escape code reads.
func TestWriteFrame_WritesAPredictorInRange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		sub  frame.SubHeader
	}{
		{
			"FIR in range",
			frame.SubHeader{
				Pred: frame.PredFIR, Order: 1, CoeffPrec: 12, CoeffShift: 2, Coeffs: []int32{1 << 2},
				RiceSubframe: &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 4}}},
			},
		},
		{
			"a coded partition with a stray escaped depth of 40",
			frame.SubHeader{
				Pred: frame.PredFixed, Order: 1,
				RiceSubframe: &frame.RiceSubframe{
					Partitions: []frame.RicePartition{{Param: 4, EscapedBitsPerSample: 40}},
				},
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if err := writeMono16(t, &frame.Subframe{SubHeader: c.sub, NSamples: 16, Samples: ramp16()}); err != nil {
				t.Fatalf("WriteFrame: %v", err)
			}
		})
	}
}

// An escaped partition, the parameter at the escape code with a depth in its
// 5 bits, is written as such: the frame comes back from a file with its
// samples, and the reference accepts it. The escape code is the field's top
// value, so a refusal one short would turn every escaped partition away.
func TestWriteFrame_WritesAnEscapedPartition(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "escaped.flac")

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	info := &meta.StreamInfo{
		BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 16,
	}

	enc, err := flac.NewEncoder(file, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	sub := frame.SubHeader{
		Pred: frame.PredFixed, Order: 1,
		RiceSubframe: &frame.RiceSubframe{
			Partitions: []frame.RicePartition{{Param: 15, EscapedBitsPerSample: 16}},
		},
	}

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{SubHeader: sub, NSamples: 16, Samples: ramp16()}},
	}

	if writeErr := enc.WriteFrame(f); writeErr != nil {
		t.Fatalf("WriteFrame: %v", writeErr)
	}

	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	again, err := flac.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = again.Close() }()

	got, err := again.ParseNext()
	if err != nil {
		t.Fatalf("ParseNext: %v", err)
	}

	if !slices.Equal(got.Subframes[0].Samples, ramp16()) {
		t.Fatalf("decoded samples %v, want the ramp", got.Subframes[0].Samples)
	}

	if flacBin, lookErr := exec.LookPath("flac"); lookErr == nil {
		out, testErr := exec.CommandContext(t.Context(), flacBin, "-s", "-t", path).CombinedOutput()
		if testErr != nil {
			t.Fatalf("flac -t: %v\n%s", testErr, out)
		}
	}
}
