package flac_test

import (
	"testing"

	"github.com/mycophonic/flac"
)

// STREAMINFO's minimum and maximum frame size are the smallest and largest
// frame written, header to CRC; they were written as 0, never measured.
// subset/01 re-encoded with its own subframes has the original's frame
// sizes, 2445..9278, which its own STREAMINFO carries.
func TestEncoder_WritesTheFrameSizes(t *testing.T) {
	t.Parallel()

	stream, err := flac.ParseFile("testdata/flac-test-files/subset/01 - blocksize 4096.flac")
	if err != nil {
		t.Skipf("no IETF test files: %v", err)
	}

	defer func() { _ = stream.Close() }()

	wantMin, wantMax := stream.Info.FrameSizeMin, stream.Info.FrameSizeMax
	if wantMin != 2445 || wantMax != 9278 {
		t.Fatalf("subset/01 carries frame sizes %d..%d, the test expects 2445..9278", wantMin, wantMax)
	}

	info := reencodeToFile(t, stream)
	if info.FrameSizeMin != wantMin || info.FrameSizeMax != wantMax {
		t.Fatalf("re-encoded frame sizes %d..%d, want %d..%d", info.FrameSizeMin, info.FrameSizeMax, wantMin, wantMax)
	}
}
