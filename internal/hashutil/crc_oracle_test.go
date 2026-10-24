package hashutil_test

import (
	"math/rand/v2"
	"testing"

	"github.com/mycophonic/flac/internal/hashutil/crc16"
	"github.com/mycophonic/flac/internal/hashutil/crc8"
)

// The table-driven checksums against bit-by-bit polynomial division, the
// definition: CRC-8 with polynomial 0x07 and CRC-16 with 0x8005, both from
// zero, as the FLAC frame header and frame footer use them, over random
// data of every length from 0 to 300 bytes, and the published check value
// of "123456789".
func plainCRC8(data []byte) uint8 {
	var crc uint8

	for _, b := range data {
		crc ^= b
		for range 8 {
			if crc&0x80 != 0 {
				crc = crc<<1 ^ 0x07
			} else {
				crc <<= 1
			}
		}
	}

	return crc
}

func plainCRC16(data []byte) uint16 {
	var crc uint16

	for _, b := range data {
		crc ^= uint16(b) << 8
		for range 8 {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x8005
			} else {
				crc <<= 1
			}
		}
	}

	return crc
}

func TestChecksumsMatchPolynomialDivision(t *testing.T) {
	t.Parallel()

	check := []byte("123456789")
	if got := crc8.ChecksumATM(check); got != 0xF4 {
		t.Fatalf("CRC-8 of the check string: %#x, want 0xF4", got)
	}

	if got := crc16.ChecksumIBM(check); got != 0xFEE8 {
		t.Fatalf("CRC-16 of the check string: %#x, want 0xFEE8", got)
	}

	rng := rand.New(rand.NewPCG(3, 4))

	for n := range 301 {
		data := make([]byte, n)
		for i := range data {
			data[i] = byte(rng.IntN(256))
		}

		if got, want := crc8.ChecksumATM(data), plainCRC8(data); got != want {
			t.Fatalf("CRC-8 of %d bytes: %#x, the division says %#x", n, got, want)
		}

		if got, want := crc16.ChecksumIBM(data), plainCRC16(data); got != want {
			t.Fatalf("CRC-16 of %d bytes: %#x, the division says %#x", n, got, want)
		}
	}
}
