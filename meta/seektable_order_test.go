package meta_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/mycophonic/flac/meta"
)

// seekTableBlock is a last SEEKTABLE block of the points given, each a
// sample number with offset and length of zero.
func seekTableBlock(samples ...uint64) []byte {
	body := make([]byte, 0, 18*len(samples))
	for _, s := range samples {
		body = binary.BigEndian.AppendUint64(body, s)
		body = binary.BigEndian.AppendUint64(body, 0)
		body = binary.BigEndian.AppendUint16(body, 0)
	}

	n := len(body)

	return append([]byte{0x80 | 3, byte(n >> 16), byte(n >> 8), byte(n)}, body...)
}

// Placeholders may only end a seek table (RFC 9639, section 8.5): one before
// a real point is refused by name, where it used to be refused as a
// "duplicate" of sample 0, the previous sample's initial value. Real points
// stay sorted and unique, and a real first point at sample 0 is no duplicate.
func TestSeekTablePlaceholdersEndTheTable(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name    string
		samples []uint64
		want    error
	}{
		{"real points then placeholders", []uint64{0, 16, meta.PlaceholderPoint, meta.PlaceholderPoint}, nil},
		{"a first point at sample 0", []uint64{0, 4096}, nil},
		{"a placeholder before real points", []uint64{meta.PlaceholderPoint, 0, 16}, meta.ErrPlaceholderNotLast},
		{"a placeholder between real points", []uint64{0, meta.PlaceholderPoint, 16}, meta.ErrPlaceholderNotLast},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			block, err := meta.New(bytes.NewReader(seekTableBlock(c.samples...)))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = block.Parse()
			if !errors.Is(err, c.want) {
				t.Fatalf("Parse: %v, want %v", err, c.want)
			}
		})
	}
}

// A duplicate real point is still a duplicate, and an unsorted one still
// unsorted, after the placeholder rule.
func TestSeekTableRealPointsStaySortedAndUnique(t *testing.T) {
	t.Parallel()

	for _, samples := range [][]uint64{{0, 16, 16}, {16, 0}} {
		block, err := meta.New(bytes.NewReader(seekTableBlock(samples...)))
		if err != nil {
			t.Fatalf("New: %v", err)
		}

		if err := block.Parse(); err == nil {
			t.Fatalf("table %v accepted", samples)
		}
	}
}
