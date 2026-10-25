package flac_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// writeOneBlock writes a mono 16-bit stream of one block of n samples through
// NewEncoder and Close, with the caller's STREAMINFO block sizes, and returns
// its path.
func writeOneBlock(t *testing.T, n, sizeMin, sizeMax uint16) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "one.flac")

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	info := &meta.StreamInfo{
		BlockSizeMin: sizeMin, BlockSizeMax: sizeMax, SampleRate: 44100, NChannels: 1, BitsPerSample: 16,
		NSamples: uint64(n),
	}

	enc, err := flac.NewEncoder(file, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: n, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: int(n), Samples: make([]int32, n),
		}},
	}

	if writeErr := enc.WriteFrame(f); writeErr != nil {
		t.Fatalf("WriteFrame: %v", writeErr)
	}

	// Close rewrites STREAMINFO through the file and closes it.
	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	return path
}

// A stream of one block shorter than 16 samples: STREAMINFO keeps the block
// sizes it was opened with, as the reference encoder writes its configured
// size, since RFC 9639 (section 8.2) has the minimum at 16 and the last block
// alone may be shorter. Written as 15..15 (#40), the decoder refused the
// stream at open. Opened with sizes below 16, or inverted, there is nothing
// to keep and 16..16 stands. At or above 16, the only block is the minimum
// and the maximum, as before.
func TestEncoder_OneBlockBelowSixteenKeepsTheStreamInfoSizes(t *testing.T) {
	t.Parallel()

	flacBin, _ := exec.LookPath("flac")

	cases := []struct {
		samples, sizeMin, sizeMax uint16
		wantMin, wantMax          uint16
	}{
		{1, 16, 16, 16, 16},
		{1, 4096, 4096, 4096, 4096},
		{15, 16, 16, 16, 16},
		{15, 4096, 4096, 4096, 4096},
		{15, 1152, 4096, 1152, 4096},
		{15, 16, 4096, 16, 4096},
		// Opened with sizes below 16, or inverted: nothing to keep, 16..16.
		{15, 8, 8, 16, 16},
		{1, 0, 0, 16, 16},
		{15, 4096, 16, 16, 16},
		{16, 4096, 4096, 16, 16},
		{17, 4096, 4096, 17, 17},
	}

	for _, c := range cases {
		path := writeOneBlock(t, c.samples, c.sizeMin, c.sizeMax)

		again, err := flac.Open(path)
		if err != nil {
			t.Fatalf("%d samples, opened with %d..%d: Open: %v", c.samples, c.sizeMin, c.sizeMax, err)
		}

		gotMin, gotMax, gotSamples := again.Info.BlockSizeMin, again.Info.BlockSizeMax, again.Info.NSamples

		_ = again.Close()

		if gotMin != c.wantMin || gotMax != c.wantMax || gotSamples != uint64(c.samples) {
			t.Fatalf("%d samples, opened with %d..%d: STREAMINFO %d..%d, %d samples; want %d..%d, %d samples",
				c.samples, c.sizeMin, c.sizeMax, gotMin, gotMax, gotSamples, c.wantMin, c.wantMax, c.samples)
		}

		if flacBin == "" {
			continue
		}

		out, testErr := exec.CommandContext(t.Context(), flacBin, "-s", "-t", path).CombinedOutput()
		if testErr != nil {
			t.Fatalf("%d samples, opened with %d..%d: flac -t: %v\n%s", c.samples, c.sizeMin, c.sizeMax, testErr, out)
		}
	}
}
