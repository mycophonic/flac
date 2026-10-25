package meta

import (
	"bytes"
	"errors"
	"io"
)

// directReadSize is the longest string read into a buffer of its own
// length; longer strings are read into a buffer that grows as bytes arrive.
const directReadSize = 4096

// readString reads a string of n bytes. A short string costs its length: the
// buffer is sized to n, not a fixed array meant for the stack, which escaped
// to the heap through the reader and cost 4 KiB per string however short
// (#39). A length beyond directReadSize is not allocated up front: the buffer
// grows with the bytes read, so a stream that ends before the string does
// costs what it held, not what n claimed.
func readString(r io.Reader, n int) (string, error) {
	if n <= directReadSize {
		readBuf := make([]byte, n)

		if _, err := io.ReadFull(r, readBuf); err != nil {
			return "", err
		}

		return string(readBuf), nil
	}

	buf, err := readGrowing(r, int64(n))
	if err != nil {
		return "", err
	}

	return string(buf), nil
}

// readGrowing reads exactly n bytes into a buffer that grows as they arrive,
// returning io.ErrUnexpectedEOF when the reader ends first.
func readGrowing(r io.Reader, n int64) ([]byte, error) {
	var buf bytes.Buffer

	got, err := io.CopyN(&buf, r, n)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, io.ErrUnexpectedEOF
		}

		return nil, err
	}

	if got < n {
		return nil, io.ErrUnexpectedEOF
	}

	return buf.Bytes(), nil
}
