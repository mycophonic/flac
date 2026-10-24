package bitio_test

import (
	"bytes"
	"math/rand/v2"
	"testing"

	"github.com/mycophonic/flac/internal/bitio"
	"github.com/mycophonic/flac/internal/bits"
)

// What the writer writes, the reader reads: random sequences of fields of
// 1 to 64 bits, with bools and bytes among them, written through
// bitio.Writer and aligned, come back from bits.Reader field for field, and
// the written length is the bit count rounded up to bytes.
func TestWriterAndReaderAreInverses(t *testing.T) {
	t.Parallel()

	type field struct {
		v uint64
		n uint8
	}

	for seed := range 300 {
		rng := rand.New(rand.NewPCG(uint64(seed), 2))

		var (
			fields []field
			total  uint64
			buf    bytes.Buffer
		)

		w := bitio.NewWriter(&buf)

		for range 1 + rng.IntN(200) {
			var f field

			switch rng.IntN(4) {
			case 0:
				f.n = 1
				f.v = uint64(rng.IntN(2))

				if err := w.WriteBool(f.v == 1); err != nil {
					t.Fatal(err)
				}
			case 1:
				f.n = 8
				f.v = uint64(rng.IntN(256))

				if err := w.WriteByte(byte(f.v)); err != nil {
					t.Fatal(err)
				}
			default:
				f.n = uint8(1 + rng.IntN(64))
				f.v = rng.Uint64()

				if f.n < 64 {
					f.v &= 1<<f.n - 1
				}

				if err := w.WriteBits(f.v, f.n); err != nil {
					t.Fatal(err)
				}
			}

			fields = append(fields, f)
			total += uint64(f.n)
		}

		if _, err := w.Align(); err != nil {
			t.Fatal(err)
		}

		if want := (total + 7) / 8; uint64(buf.Len()) != want {
			t.Fatalf("seed %d: %d bits written as %d bytes, want %d", seed, total, buf.Len(), want)
		}

		r := bits.NewReader(bytes.NewReader(buf.Bytes()))

		for i, f := range fields {
			got, err := r.Read(uint(f.n))
			if err != nil {
				t.Fatalf("seed %d field %d: %v", seed, i, err)
			}

			if got != f.v {
				t.Fatalf("seed %d field %d (%d bits): read %d, wrote %d", seed, i, f.n, got, f.v)
			}
		}
	}
}
