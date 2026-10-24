package flac_test

import (
	"bytes"
	"errors"
	"os"
	"testing"

	"github.com/mycophonic/flac"
)

// streamInfoOf returns subset/01's STREAMINFO block (header and body), with
// the last-block flag as asked.
func streamInfoOf(t *testing.T, last bool) []byte {
	t.Helper()

	data, err := os.ReadFile("testdata/flac-test-files/subset/01 - blocksize 4096.flac")
	if err != nil {
		t.Skipf("no IETF test files: %v", err)
	}

	block := append([]byte{}, data[4:4+4+34]...)
	block[0] &= 0x7F

	if last {
		block[0] |= 0x80
	}

	return block
}

// A second STREAMINFO block is refused by New and NewSeek: the format has
// exactly one, first, and the reference decoder refuses a second.
func TestSecondStreamInfoIsRefused(t *testing.T) {
	t.Parallel()

	data := append([]byte("fLaC"), streamInfoOf(t, false)...)
	data = append(data, streamInfoOf(t, true)...)

	if _, err := flac.New(bytes.NewReader(data)); !errors.Is(err, flac.ErrSecondStreamInfo) {
		t.Fatalf("New: %v, want ErrSecondStreamInfo", err)
	}

	if _, err := flac.NewSeek(bytes.NewReader(data)); !errors.Is(err, flac.ErrSecondStreamInfo) {
		t.Fatalf("NewSeek: %v, want ErrSecondStreamInfo", err)
	}

	if _, err := flac.Parse(bytes.NewReader(data)); !errors.Is(err, flac.ErrSecondStreamInfo) {
		t.Fatalf("Parse: %v, want ErrSecondStreamInfo", err)
	}
}

// A seek table whose length is not a multiple of 18 is refused where it is
// parsed: the tail is not a point to ignore, and the reference refuses it.
func TestSeekTableOfAStrayLengthIsRefused(t *testing.T) {
	t.Parallel()

	data := append([]byte("fLaC"), streamInfoOf(t, false)...)
	data = append(data, 0x83, 0x00, 0x00, 19)
	data = append(data, make([]byte, 19)...)

	if _, err := flac.NewSeek(bytes.NewReader(data)); err == nil {
		t.Fatal("NewSeek accepted a seek table of 19 bytes")
	}
}
