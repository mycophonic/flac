package flac_test

import (
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac"
)

// Next tells each frame the stream's block sizes as ParseNext does: walked
// with Next and Parse, subset/01's last frame, 1500 samples long, starts at
// frame 75 × 4096, not 75 × 1500.
func TestNext_LastFrameSampleNumber(t *testing.T) {
	t.Parallel()

	stream, err := flac.Open("testdata/flac-test-files/subset/01 - blocksize 4096.flac")
	if err != nil {
		t.Skipf("no IETF test files: %v", err)
	}
	defer func() { _ = stream.Close() }()

	var last *flacFrame

	for {
		f, err := stream.Next()
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			t.Fatalf("Next: %v", err)
		}

		if err := f.Parse(); err != nil {
			t.Fatalf("Parse: %v", err)
		}

		last = &flacFrame{num: f.Num, blockSize: f.BlockSize, sampleNumber: f.SampleNumber()}
	}

	if last == nil {
		t.Fatal("no frames")
	}

	if last.blockSize != 1500 || last.num != 75 {
		t.Fatalf("last frame: number %d of %d samples, want 75 of 1500", last.num, last.blockSize)
	}

	if want := uint64(75 * 4096); last.sampleNumber != want {
		t.Fatalf("last frame starts at %d, want %d", last.sampleNumber, want)
	}
}

type flacFrame struct {
	num          uint64
	blockSize    uint16
	sampleNumber uint64
}
