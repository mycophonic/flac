package flac_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// A 4-bit stream encodes: the sample size has no header code of its own, so
// the frame header defers to STREAMINFO, which the decoder resolves.
func TestEncoder_Writes4BitSamples(t *testing.T) {
	t.Parallel()

	samples := []int32{-8, -7, -4, -1, 0, 1, 3, 7, -8, 7, 0, 0, 2, -2, 5, -5}
	info := &meta.StreamInfo{
		BlockSizeMin: 16, BlockSizeMax: 16, SampleRate: 44100, NChannels: 1, BitsPerSample: 4, NSamples: 16,
	}

	var out bytes.Buffer

	enc, err := flac.NewEncoder(&out, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 4,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
			NSamples:  16,
			Samples:   append([]int32{}, samples...),
		}},
	}

	if writeErr := enc.WriteFrame(f); writeErr != nil {
		t.Fatalf("WriteFrame: %v", writeErr)
	}

	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	stream, err := flac.New(bytes.NewReader(out.Bytes()))
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	got, err := stream.ParseNext()
	if err != nil {
		t.Fatalf("ParseNext: %v", err)
	}

	if got.BitsPerSample != 4 {
		t.Fatalf("the frame resolves to %d bits, want 4", got.BitsPerSample)
	}

	for i, want := range samples {
		if got.Subframes[0].Samples[i] != want {
			t.Fatalf("sample %d: %d, want %d", i, got.Subframes[0].Samples[i], want)
		}
	}

	if bin, err := exec.LookPath("flac"); err == nil {
		path := filepath.Join(t.TempDir(), "four.flac")
		if writeErr := os.WriteFile(path, out.Bytes(), 0o600); writeErr != nil {
			t.Fatal(writeErr)
		}

		if msg, refErr := exec.CommandContext(t.Context(), bin, "-t", "-s", path).CombinedOutput(); refErr != nil {
			t.Fatalf("the reference refuses the stream: %v\n%s", refErr, bytes.TrimSpace(msg))
		}
	}
}
