package flac_test

import (
	"bytes"
	"testing"

	"github.com/mycophonic/flac"
)

// A subframe may not waste more bits than the sample has (RFC 9639, section
// 9.2.2: the number of wasted bits is less than the bit depth). A 16-bit, 8-channel
// stream whose verbatim subframe declares more wasted bits than that is a
// refusal, not a panic. Found by fuzzing saprobe-flac's decoder.
func TestWastedBitsBeyondTheDepthAreRefused(t *testing.T) {
	t.Parallel()

	data := []byte("fLaC\x80\x00\x00\"000000000000.\xf000000000000000000000\xff\xf8ix\x00'\x8f\x03\x00\x00\x01")

	stream, err := flac.New(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the metadata is not the problem: %v", err)
	}

	defer func() { _ = stream.Close() }()

	if _, err := stream.ParseNext(); err == nil {
		t.Fatal("a subframe wasting more bits than its depth was decoded")
	}
}
