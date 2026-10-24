package bits_test

import (
	"bytes"
	"errors"
	"io"
	"math/rand/v2"
	"testing"

	"github.com/mycophonic/flac/internal/bits"
)

// A plain bit reader as the oracle: one bit at a time from a byte slice.
type plainBits struct {
	data []byte
	pos  int // in bits
}

func (p *plainBits) bit() (uint64, bool) {
	if p.pos >= len(p.data)*8 {
		return 0, false
	}

	b := uint64(p.data[p.pos/8]>>(7-p.pos%8)) & 1
	p.pos++

	return b, true
}

func (p *plainBits) read(n uint) (uint64, bool) {
	var x uint64

	for range n {
		b, ok := p.bit()
		if !ok {
			return 0, false
		}

		x = x<<1 | b
	}

	return x, true
}

func (p *plainBits) unary() (uint64, bool) {
	var n uint64

	for {
		b, ok := p.bit()
		if !ok {
			return 0, false
		}

		if b == 1 {
			return n, true
		}

		n++
	}
}

func (p *plainBits) rice(k uint) (int32, bool) {
	high, ok := p.unary()
	if !ok {
		return 0, false
	}

	low, ok := p.read(k)
	if !ok {
		return 0, false
	}

	// The folded value is 32 bits wide, as libFLAC holds it; even is
	// positive, odd negative.
	u := uint32(high<<k | low)

	v := int32(u >> 1)
	if u&1 == 1 {
		v = ^v
	}

	return v, true
}

// Random sequences of Read, ReadUnary and ReadRice over random bytes give
// the oracle's values and fail where the oracle runs out, never later.
func TestReaderMatchesAPlainBitReader(t *testing.T) {
	t.Parallel()

	for seed := range 200 {
		rng := rand.New(rand.NewPCG(uint64(seed), 1))
		data := make([]byte, 1+rng.IntN(300))

		for i := range data {
			switch rng.IntN(4) {
			case 0:
				data[i] = 0
			case 1:
				data[i] = 0xFF
			default:
				data[i] = byte(rng.IntN(256))
			}
		}

		br := bits.NewReader(bytes.NewReader(data))
		oracle := &plainBits{data: data}

		for step := 0; ; step++ {
			var (
				got, want uint64
				err       error
				ok        bool
				what      string
			)

			switch rng.IntN(3) {
			case 0:
				n := uint(1 + rng.IntN(64))
				what = "Read"
				want, ok = oracle.read(n)
				got, err = br.Read(n)
			case 1:
				what = "ReadUnary"
				want, ok = oracle.unary()
				got, err = br.ReadUnary()
			default:
				k := uint(rng.IntN(32))
				what = "ReadRice"

				var v int32

				v, ok = oracle.rice(k)
				want = uint64(uint32(v))

				var g int32

				g, err = br.ReadRice(k)
				got = uint64(uint32(g))
			}

			if !ok {
				if err == nil {
					t.Fatalf("seed %d step %d: %s read past the %d bytes (got %d)", seed, step, what, len(data), got)
				}

				if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
					t.Fatalf("seed %d step %d: %s at the end: %v", seed, step, what, err)
				}

				break
			}

			if err != nil {
				t.Fatalf(
					"seed %d step %d: %s failed with %d bits left: %v",
					seed,
					step,
					what,
					len(data)*8-oracle.pos,
					err,
				)
			}

			if got != want {
				t.Fatalf("seed %d step %d: %s = %d, the oracle says %d", seed, step, what, got, want)
			}
		}
	}
}
