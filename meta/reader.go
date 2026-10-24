package meta

import (
	"bytes"
	"errors"
	"io"
)

// stackBufSize is the string length read through a stack buffer; longer
// strings are read into a buffer that grows as bytes arrive.
const stackBufSize = 4096

// readString reads a string of n bytes. A length beyond the stack buffer is
// not allocated up front: the buffer grows with the bytes read, so a stream
// that ends before the string does costs what it held, not what n claimed.
func readString(r io.Reader, n int) (string, error) {
	if n <= stackBufSize {
		var backingArray [stackBufSize]byte // hopefully allocated on stack.

		readBuf := backingArray[:n]

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
