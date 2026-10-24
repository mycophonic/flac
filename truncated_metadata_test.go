package flac_test

// Hostile frames against the reference decoder. A one-frame stream is
// built bit by bit and sealed with its CRC-8 and CRC-16, then one field is
// given a value the format reserves, forbids or cannot hold, and both this
// decoder and `flac -t` decide. The decoder may never panic; where the
// table says so, its decision is the reference's.

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac"
)

// A stream cut inside its metadata is not a stream that ends cleanly:
// New and NewSeek must report the cut, not io.EOF on the first frame.
func TestTruncatedMetadataIsNotAnEnd(t *testing.T) {
	t.Parallel()

	whole := validFrame().build()

	// Make the STREAMINFO not last and append a PADDING block of 4096 bytes
	// of which only 10 are present.
	data := append([]byte{}, whole[:4]...)
	data = append(data, whole[4]&0x7F)
	data = append(data, whole[5:4+4+34]...)
	data = append(data, 0x81, 0x00, 0x10, 0x00) // last PADDING, 4096 bytes
	data = append(data, make([]byte, 10)...)

	for _, c := range []struct {
		name string
		open func(io.ReadSeeker) (*flac.Stream, error)
	}{
		{"New", func(r io.ReadSeeker) (*flac.Stream, error) { return flac.New(r) }},
		{"NewSeek", flac.NewSeek},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			stream, err := c.open(bytes.NewReader(data))
			if err == nil {
				defer func() { _ = stream.Close() }()

				_, err = stream.ParseNext()
			}

			if err == nil || errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("a stream cut inside its metadata ends with %v, want an error that is not a clean EOF", err)
			}
		})
	}
}
