package frame_test

import (
	"encoding/binary"
	"io"
	"os"
	"testing"

	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/internal/bits"
)

// A frame from New alone, with no stream to tell it the block sizes, numbers
// its samples by its own block size: the second frame of a 4096-sample
// stream starts at 4096.
func TestSampleNumber_WithoutStreamBlockSizes(t *testing.T) {
	t.Parallel()

	file, err := os.Open("../testdata/flac-test-files/subset/01 - blocksize 4096.flac")
	if err != nil {
		t.Skipf("no IETF test files: %v", err)
	}
	defer func() { _ = file.Close() }()

	// Skip "fLaC" and the metadata blocks to the first frame.
	var header [4]byte
	if _, err := io.ReadFull(file, header[:]); err != nil || string(header[:]) != "fLaC" {
		t.Fatalf("not a FLAC file: %v", err)
	}

	for {
		if _, err := io.ReadFull(file, header[:]); err != nil {
			t.Fatalf("metadata block header: %v", err)
		}

		length := binary.BigEndian.Uint32(header[:]) & 0xFFFFFF
		if _, err := file.Seek(int64(length), io.SeekCurrent); err != nil {
			t.Fatalf("skip block: %v", err)
		}

		if header[0]&0x80 != 0 {
			break // the last metadata block
		}
	}

	br := bits.NewReader(file)

	for want := range uint64(2) {
		f, err := frame.New(br)
		if err != nil {
			t.Fatalf("frame %d: New: %v", want, err)
		}

		if err := f.Parse(); err != nil {
			t.Fatalf("frame %d: Parse: %v", want, err)
		}

		if f.Num != want || f.BlockSize != 4096 {
			t.Fatalf("frame %d: number %d of %d samples", want, f.Num, f.BlockSize)
		}

		if got := f.SampleNumber(); got != want*4096 {
			t.Fatalf("frame %d starts at %d, want %d", want, got, want*4096)
		}
	}
}
