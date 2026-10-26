package flac_test

import (
	"bytes"
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

// mono16Frame is a 16-bit mono frame of 16 samples with the given subframe.
func mono16Frame(sub frame.SubHeader, samples []int32) *frame.Frame {
	return &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{SubHeader: sub, NSamples: 16, Samples: samples}},
	}
}

// refusedFrames are frames WriteFrame refuses, one per check that runs after
// the frame header used to be written (#50).
func refusedFrames() []struct {
	name string
	f    *frame.Frame
} {
	rice := &frame.RiceSubframe{Partitions: []frame.RicePartition{{Param: 4}}}
	beyond := ramp16()
	beyond[3] = 70000

	return []struct {
		name string
		f    *frame.Frame
	}{
		{
			"predictor order at the block size",
			mono16Frame(frame.SubHeader{Pred: frame.PredFixed, Order: 16, RiceSubframe: rice}, ramp16()),
		},
		{"fixed without Rice partitions", mono16Frame(frame.SubHeader{Pred: frame.PredFixed, Order: 1}, ramp16())},
		{
			"coefficient precision 16",
			mono16Frame(
				frame.SubHeader{Pred: frame.PredFIR, Order: 1, CoeffPrec: 16, Coeffs: []int32{1}, RiceSubframe: rice},
				ramp16(),
			),
		},
		{"a sample beyond the depth", mono16Frame(frame.SubHeader{Pred: frame.PredVerbatim}, beyond)},
		{"wasted bits at the depth", mono16Frame(frame.SubHeader{Pred: frame.PredVerbatim, Wasted: 16}, ramp16())},
	}
}

// A refused frame leaves nothing in the stream: the frame header went out
// before any subframe was checked, so the stream was corrupt after an error
// (#50).
func TestWriteFrame_ARefusedFrameLeavesNothingInTheStream(t *testing.T) {
	t.Parallel()

	for _, c := range refusedFrames() {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			var out bytes.Buffer

			info := &meta.StreamInfo{
				BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 16,
			}

			enc, err := flac.NewEncoder(&out, info)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}

			enc.EnablePredictionAnalysis(false)

			before := out.Len()

			if writeErr := enc.WriteFrame(c.f); writeErr == nil || errors.Is(writeErr, io.EOF) {
				t.Fatalf("WriteFrame accepted %s: %v", c.name, writeErr)
			}

			if leaked := out.Len() - before; leaked != 0 {
				t.Fatalf("%s: %d bytes in the stream after the refusal", c.name, leaked)
			}
		})
	}
}

// After a refusal the encoder goes on as if the frame had not been offered:
// the frames around it are numbered 0 and 1, STREAMINFO counts their samples
// and hashes them alone, the decoder reads both, and the reference accepts
// the stream.
func TestWriteFrame_ARefusedFrameLeavesTheStreamIntact(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "intact.flac")

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	info := &meta.StreamInfo{
		BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 32,
	}

	enc, err := flac.NewEncoder(file, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	good := func() *frame.Frame { return mono16Frame(frame.SubHeader{Pred: frame.PredVerbatim}, ramp16()) }

	if writeErr := enc.WriteFrame(good()); writeErr != nil {
		t.Fatalf("WriteFrame: %v", writeErr)
	}

	for _, c := range refusedFrames() {
		if writeErr := enc.WriteFrame(c.f); writeErr == nil {
			t.Fatalf("WriteFrame accepted %s", c.name)
		}
	}

	if writeErr := enc.WriteFrame(good()); writeErr != nil {
		t.Fatalf("WriteFrame after the refusals: %v", writeErr)
	}

	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	again, err := flac.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = again.Close() }()

	if again.Info.NSamples != 32 {
		t.Fatalf("STREAMINFO counts %d samples, want 32", again.Info.NSamples)
	}

	for want := range uint64(2) {
		f, parseErr := again.ParseNext()
		if parseErr != nil {
			t.Fatalf("frame %d: %v", want, parseErr)
		}

		if f.Num != want || !slices.Equal(f.Subframes[0].Samples, ramp16()) {
			t.Fatalf("frame %d: number %d, samples %v", want, f.Num, f.Subframes[0].Samples)
		}
	}

	if _, parseErr := again.ParseNext(); !errors.Is(parseErr, io.EOF) {
		t.Fatalf("after two frames: %v, want io.EOF", parseErr)
	}

	if flacBin, lookErr := exec.LookPath("flac"); lookErr == nil {
		out, testErr := exec.CommandContext(t.Context(), flacBin, "-s", "-t", path).CombinedOutput()
		if testErr != nil {
			t.Fatalf("flac -t: %v\n%s", testErr, out)
		}
	}
}
