package bufseekio_test

import (
	"bytes"
	"io"
	"testing"

	"github.com/mycophonic/flac/internal/bufseekio"
)

// A read larger than the buffer bypasses it and advances the position, and
// a seek back into the window that the position and the old buffer length
// describe must not serve the old buffer's bytes: after reading data[0:4096]
// through the buffer and data[4096:8192] directly, Seek(4096+k) is
// data[4096+k], not data[k].
// bufSize is bufseekio's default buffer size, which the test cannot import.
const bufSize = 4096

func TestSeekAfterADirectReadIsNotTheOldBuffer(t *testing.T) {
	t.Parallel()

	// Not periodic in the buffer size: a byte and the byte one buffer
	// later differ.
	data := make([]byte, 3*bufSize)
	for i := range data {
		data[i] = byte(i*7 + i/bufSize)
	}

	for _, k := range []int64{0, 1, 100, bufSize - 4} {
		rs := bufseekio.NewReadSeeker(bytes.NewReader(data))

		// Two reads smaller than the buffer: the buffer is filled and consumed.
		small := make([]byte, bufSize/2)
		for range 2 {
			if _, err := io.ReadFull(rs, small); err != nil {
				t.Fatal(err)
			}
		}

		// One read as large as the buffer: served from the source directly.
		large := make([]byte, bufSize)
		if _, err := io.ReadFull(rs, large); err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(large, data[bufSize:2*bufSize]) {
			t.Fatal("the direct read is not the data")
		}

		target := int64(bufSize) + k

		if got, err := rs.Seek(target, io.SeekStart); err != nil || got != target {
			t.Fatalf("Seek(%d): %d, %v", target, got, err)
		}

		p := make([]byte, 4)
		if _, err := io.ReadFull(rs, p); err != nil {
			t.Fatal(err)
		}

		if !bytes.Equal(p, data[target:target+4]) {
			t.Errorf("after Seek(%d), read % x, the data there is % x (the old buffer has % x at %d)",
				target, p, data[target:target+4], data[k:k+4], k)
		}

		if pos, _ := rs.Seek(0, io.SeekCurrent); pos != target+4 {
			t.Errorf("after Seek(%d) and four bytes, the position is %d", target, pos)
		}
	}
}
