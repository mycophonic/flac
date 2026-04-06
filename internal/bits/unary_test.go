package bits_test

import (
	"bytes"
	"testing"

	"github.com/icza/bitio"

	"github.com/mycophonic/flac/internal/bits"
)

func TestUnary(t *testing.T) {
	buf := &bytes.Buffer{}
	bw := bitio.NewWriter(buf)

	for want := range uint64(1000) {
		// Write unary
		if err := bits.WriteUnary(bw, want); err != nil {
			t.Fatalf("unable to write unary; %v", err)
		}
		// Flush buffer
		if err := bw.Close(); err != nil {
			t.Fatalf("unable to close (flush) the bit buffer; %v", err)
		}

		// Read written unary
		r := bits.NewReader(buf)

		got, err := r.ReadUnary()
		if err != nil {
			t.Fatalf("unable to read unary; %v", err)
		}

		if want != got {
			t.Fatalf("mismatch between written and read unary value; expected: %d, got: %d", want, got)
		}
	}
}
