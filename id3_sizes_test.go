package flac_test

// An ID3v2 tag before the stream is skipped by its sync-safe size, at
// every width of that size: one byte, two, three and four, with the
// largest value each width holds and the smallest the next needs. The
// stream behind it decodes, and the reference accepts the file as well.

import (
	"bytes"
	"fmt"
	"os/exec"
	"testing"

	"github.com/mycophonic/flac"
)

// id3v2 is a tag of n bytes of zero padding after its 10-byte header, the
// size written sync-safe, seven bits per byte.
func id3v2(n int) []byte {
	tag := []byte{
		'I', 'D', '3', 4, 0, 0,
		byte(n>>21) & 0x7F, byte(n>>14) & 0x7F, byte(n>>7) & 0x7F, byte(n) & 0x7F,
	}

	return append(tag, make([]byte, n)...)
}

func TestID3v2TagsOfEverySizeWidthAreSkipped(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	stream := validFrame().build()

	for _, n := range []int{0, 1, 127, 128, 16383, 16384, 2097151, 2097152} {
		t.Run(fmt.Sprintf("%d bytes", n), func(t *testing.T) {
			t.Parallel()

			data := append(id3v2(n), stream...)

			s, err := flac.New(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			defer func() { _ = s.Close() }()

			f, err := s.ParseNext()
			if err != nil {
				t.Fatalf("ParseNext: %v", err)
			}

			if f.BlockSize != 16 {
				t.Fatalf("the frame behind the tag has %d samples, want 16", f.BlockSize)
			}

			if !referenceAccepts(t, flacBin, data) {
				t.Fatal("the reference refuses the file")
			}
		})
	}
}
