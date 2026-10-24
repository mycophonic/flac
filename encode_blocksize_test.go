package flac_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// reencodeToFile re-encodes a stream's frames with their own subframes into
// a file, the encoder given a copy of the StreamInfo, and returns the
// STREAMINFO the file carries after Close. A file, not a buffer: Close
// rewrites STREAMINFO only through an io.WriteSeeker.
func reencodeToFile(t *testing.T, stream *flac.Stream) *meta.StreamInfo {
	t.Helper()

	path := filepath.Join(t.TempDir(), "again.flac")

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}

	info := *stream.Info

	enc, err := flac.NewEncoder(file, &info, stream.Blocks...)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	for {
		f, parseErr := stream.ParseNext()
		if errors.Is(parseErr, io.EOF) {
			break
		}

		if parseErr != nil {
			t.Fatalf("ParseNext: %v", parseErr)
		}

		if writeErr := enc.WriteFrame(f); writeErr != nil {
			t.Fatalf("WriteFrame: %v", writeErr)
		}
	}

	// Close rewrites STREAMINFO through the file and closes it.
	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	again, err := flac.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = again.Close() }()

	return again.Info
}

// STREAMINFO's minimum block size excludes the last block (RFC 9639,
// section 8.2): subset/01, fixed 4096 with a last block of 1500, re-encodes
// with a minimum of 4096, not 1500, so its decoder reads it as a
// fixed-blocksize stream again.
func TestEncoder_MinimumBlockSizeExcludesTheLastBlock(t *testing.T) {
	t.Parallel()

	stream, err := flac.ParseFile("testdata/flac-test-files/subset/01 - blocksize 4096.flac")
	if err != nil {
		t.Skipf("no IETF test files: %v", err)
	}

	defer func() { _ = stream.Close() }()

	info := reencodeToFile(t, stream)
	if info.BlockSizeMin != 4096 || info.BlockSizeMax != 4096 {
		t.Fatalf("re-encoded block sizes %d..%d, want 4096..4096", info.BlockSizeMin, info.BlockSizeMax)
	}
}

// A stream of one block has no block before the last: its minimum is its
// maximum, as the reference encoder writes it, and a decoder accepts it.
func TestEncoder_OneBlockStreamHasItsMinimum(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "one.flac")

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

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: 16, Samples: make([]int32, 16),
		}},
	}

	if writeErr := enc.WriteFrame(f); writeErr != nil {
		t.Fatalf("WriteFrame: %v", writeErr)
	}

	// Close rewrites STREAMINFO through the file and closes it.
	if closeErr := enc.Close(); closeErr != nil {
		t.Fatalf("Close: %v", closeErr)
	}

	again, err := flac.Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	defer func() { _ = again.Close() }()

	if again.Info.BlockSizeMin != 16 || again.Info.BlockSizeMax != 16 {
		t.Fatalf("one-block stream's block sizes %d..%d, want 16..16", again.Info.BlockSizeMin, again.Info.BlockSizeMax)
	}
}
