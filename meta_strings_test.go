package flac_test

import (
	"bytes"
	"testing"

	"github.com/mycophonic/flac"
)

// shortTags is a VORBIS_COMMENT body: an empty vendor string, then count
// three-byte tags.
func shortTags(count int) []byte {
	body := le32(nil, 0)
	body = le32(body, uint32(count))

	for range count {
		body = le32(body, 3)
		body = append(body, "a=b"...)
	}

	return body
}

// A short metadata string costs about its length: 2000 three-byte tags, 14 KB
// of input, parse within a small fixed cost plus 128 bytes per tag. Each tag
// cost 4 KiB when a buffer meant for the stack escaped to the heap (#39).
//
//nolint:paralleltest // the allocation is measured process-wide, as in FuzzMetadata
func TestShortMetadataStringsCostTheirLength(t *testing.T) {
	const count = 2000

	body := shortTags(count)
	stream := streamWith(append(lastBlock(4, len(body)), body...))

	bound := uint64(64<<10) + count*128
	if raceDetector {
		bound *= 2
	}

	open := []struct {
		name string
		fn   func() error
	}{
		{"Parse", func() error {
			s, err := flac.Parse(bytes.NewReader(stream))
			if err == nil {
				_ = s.Close()
			}

			return err
		}},
		{"NewSeek", func() error {
			s, err := flac.NewSeek(bytes.NewReader(stream))
			if err == nil {
				_ = s.Close()
			}

			return err
		}},
	}

	for _, entry := range open {
		var err error

		got := allocated(func() { err = entry.fn() })

		if err != nil {
			t.Fatalf("%s: %v", entry.name, err)
		}

		if got > bound {
			t.Fatalf("%s allocated %d bytes for %d tags of 3 bytes: %d per tag, bound %d in all",
				entry.name, got, count, got/count, bound)
		}
	}
}
