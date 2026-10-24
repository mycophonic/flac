package flac_test

import (
	"bytes"
	"errors"
	"testing"

	"github.com/mycophonic/flac"
)

// A first block that is not a STREAMINFO is refused on its header's type:
// parsing it first let a PICTURE's MIME length of 0x30303030 size an
// allocation of 808 MB from a 16-byte file.
//
//nolint:paralleltest // the allocation is measured process-wide, as in FuzzMetadata
func TestFirstBlockTypeIsCheckedBeforeParsing(t *testing.T) {
	data := append([]byte("fLaC\x86"), bytes.Repeat([]byte("0"), 11)...)

	var err error

	allocated := allocatedDuring(func() {
		_, err = flac.New(bytes.NewReader(data))
	})

	if !errors.Is(err, flac.ErrFirstBlockNotStreamInfo) {
		t.Fatalf("a stream whose first block is a PICTURE: %v, want ErrFirstBlockNotStreamInfo", err)
	}

	if allocated > 4<<20 {
		t.Fatalf("New allocated %d bytes for a 16-byte file: %v", allocated, err)
	}

	t.Logf("refused: %v (%d bytes allocated)", err, allocated)
}
