package utf8_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac/internal/utf8"
)

// Encode and Decode are inverses over the whole 36-bit range, at every
// boundary where the coding grows a byte, and Decode refuses what the
// coding forbids: an overlong form, a lone or missing continuation byte,
// the lead bytes 0xFE and 0xFF beyond the seven-byte form, and a sequence
// cut short.
func TestRoundTripAtTheBoundaries(t *testing.T) {
	t.Parallel()

	var values []uint64
	for _, bits := range []uint{7, 11, 16, 21, 26, 31, 36} {
		values = append(values, 1<<bits-2, 1<<bits-1, 1<<bits)
	}

	values = append(values, 0, 1, 42)

	for _, v := range values {
		if v >= 1<<36 {
			continue
		}

		var buf bytes.Buffer
		if err := utf8.Encode(&buf, v); err != nil {
			t.Fatalf("Encode(%d): %v", v, err)
		}

		got, err := utf8.Decode(bytes.NewReader(buf.Bytes()))
		if err != nil || got != v {
			t.Fatalf("Decode(Encode(%d)) = %d, %v (%d bytes: % x)", v, got, err, buf.Len(), buf.Bytes())
		}
	}

	if err := utf8.Encode(io.Discard, 1<<36); err == nil {
		t.Fatal("a 37-bit value was encoded")
	}
}

func TestDecodeRefusesWhatTheCodingForbids(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []byte
		cut  bool // the error must be io.ErrUnexpectedEOF
	}{
		{"empty", nil, false},
		{"lone continuation byte", []byte{0x80}, false},
		{"overlong two-byte zero", []byte{0xC0, 0x80}, false},
		{"overlong two-byte 127", []byte{0xC1, 0xBF}, false},
		{"overlong three-byte", []byte{0xE0, 0x80, 0x80}, false},
		{"overlong seven-byte", []byte{0xFE, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}, false},
		{"lead byte 0xFF", []byte{0xFF, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}, false},
		{"missing continuation", []byte{0xC2, 0x41}, false},
		{"second byte a lead byte", []byte{0xE2, 0xC2, 0x80}, false},
		{"cut after the lead byte", []byte{0xC2}, true},
		{"cut inside a four-byte form", []byte{0xF0, 0x90, 0x80}, true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			got, err := utf8.Decode(bytes.NewReader(c.in))
			if err == nil {
				t.Fatalf("decoded % x as %d", c.in, got)
			}

			if c.cut && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("a cut sequence: %v, want io.ErrUnexpectedEOF", err)
			}

			if !c.cut && len(c.in) > 0 && errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("a whole but invalid sequence reported as cut: %v", err)
			}
		})
	}
}
