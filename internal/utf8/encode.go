package utf8

import (
	"io"

	"github.com/mycophonic/flac/internal/ioutilx"
)

// Encode encodes x as a "UTF-8" coded number.
func Encode(w io.Writer, x uint64) error {
	// 1-byte, 7-bit sequence?
	if x <= rune1Max {
		if err := ioutilx.WriteByte(w, byte(x)); err != nil {
			return err
		}

		return nil
	}

	// get number of continuation bytes and store bits of c0.
	var (
		// number of continuation bytes.,
		l int
		// bits of c0.
		bits uint64
	)

	switch {
	case x <= rune2Max:
		// if c0 == 110xxxxx
		// total: 11 bits (5 + 6)
		l = 1
		bits = t2 | (x>>6)&mask2
	case x <= rune3Max:
		// if c0 == 1110xxxx
		// total: 16 bits (4 + 6 + 6)
		l = 2
		bits = t3 | (x>>(6*2))&mask3
	case x <= rune4Max:
		// if c0 == 11110xxx
		// total: 21 bits (3 + 6 + 6 + 6)
		l = 3
		bits = t4 | (x>>(6*3))&mask4
	case x <= rune5Max:
		// if c0 == 111110xx
		// total: 26 bits (2 + 6 + 6 + 6 + 6)
		l = 4
		bits = t5 | (x>>(6*4))&mask5
	case x <= rune6Max:
		// if c0 == 1111110x
		// total: 31 bits (1 + 6 + 6 + 6 + 6 + 6)
		l = 5
		bits = t6 | (x>>(6*5))&mask6
	case x <= rune7Max:
		// if c0 == 11111110
		// total: 36 bits (0 + 6 + 6 + 6 + 6 + 6 + 6)
		// The leading byte has no payload bits — all 8 bits are the prefix
		// marker (t7 = 0xFE). The full 36-bit value lives entirely in the
		// 6 continuation bytes.
		l = 6
		bits = t7
	}
	// Store bits of c0.
	if err := ioutilx.WriteByte(w, byte(bits)); err != nil {
		return err
	}

	// Store continuation bytes.
	for i := l - 1; i >= 0; i-- {
		bits := tx | (x>>uint(6*i))&maskx
		if err := ioutilx.WriteByte(w, byte(bits)); err != nil {
			return err
		}
	}

	return nil
}
