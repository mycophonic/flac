// Tests adapted from github.com/icza/bitio v1.1.0 bitio_test.go.
// Original copyright 2016 Andras Belicza, Apache License 2.0.
// Adapted to use the standard library testing package (mighty helpers removed).

package bitio

import (
	"bytes"
	"errors"
	"io"
	"testing"
)

// testWriter that does not implement io.ByteWriter so we can test the
// behaviour of Writer when it creates an internal bufio.Writer.
type testWriter struct {
	b *bytes.Buffer
}

func (w *testWriter) Write(p []byte) (n int, err error) {
	return w.b.Write(p)
}

func (w *testWriter) Bytes() []byte {
	return w.b.Bytes()
}

func TestWriter(t *testing.T) {
	for i := range 2 {
		// 2 rounds, first use something that implements io.ByteWriter (*bytes.Buffer),
		// next testWriter which does not.
		var b interface {
			io.Writer
			Bytes() []byte
		}
		{
			buf := &bytes.Buffer{}

			b = buf
			if i > 0 {
				b = &testWriter{b: buf}
			}
		}

		w := NewWriter(b)

		expected := []byte{0xc1, 0x7f, 0xac, 0x89, 0x24, 0x78, 0x01, 0x02, 0xf8, 0x08, 0xf0, 0xff, 0x80, 0x12, 0x34}

		check := func(err error) {
			t.Helper()

			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		}

		check(w.WriteByte(0xc1))
		check(w.WriteBool(false))
		check(w.WriteBits(0x3f, 6))
		check(w.WriteBool(true))
		check(w.WriteByte(0xac))
		check(w.WriteBits(0x01, 1))
		check(w.WriteBits(0x1248f, 20))

		skipped, err := w.Align()
		check(err)

		if skipped != 3 {
			t.Fatalf("Align: want skipped=3, got %d", skipped)
		}

		n, err := w.Write([]byte{0x01, 0x02})
		check(err)

		if n != 2 {
			t.Fatalf("Write: want n=2, got %d", n)
		}

		check(w.WriteBits(0x0f, 4))

		n, err = w.Write([]byte{0x80, 0x8f})
		check(err)

		if n != 2 {
			t.Fatalf("Write: want n=2, got %d", n)
		}

		skipped, err = w.Align()
		check(err)

		if skipped != 4 {
			t.Fatalf("Align: want skipped=4, got %d", skipped)
		}

		skipped, err = w.Align()
		check(err)

		if skipped != 0 {
			t.Fatalf("Align: want skipped=0, got %d", skipped)
		}

		check(w.WriteBits(0x01, 1))
		check(w.WriteByte(0xff))

		skipped, err = w.Align()
		check(err)

		if skipped != 7 {
			t.Fatalf("Align: want skipped=7, got %d", skipped)
		}

		check(w.WriteBits(0x1234, 16))

		check(w.Close())

		if !bytes.Equal(b.Bytes(), expected) {
			t.Fatalf("output mismatch:\n got:  % x\n want: % x", b.Bytes(), expected)
		}
	}
}

type nonByteReaderWriter struct {
	io.Reader
	io.Writer
}

func TestNonByteWriter(t *testing.T) {
	NewWriter(nonByteReaderWriter{})
}

type errWriter struct {
	limit int
}

func (e *errWriter) WriteByte(c byte) error {
	if e.limit == 0 {
		return errors.New("Can't write more")
	}

	e.limit--

	return nil
}

func (e *errWriter) Write(p []byte) (n int, err error) {
	for i, v := range p {
		if err := e.WriteByte(v); err != nil {
			return i, err
		}
	}

	return len(p), nil
}

func TestWriterError(t *testing.T) {
	eq := func(want, got any) {
		t.Helper()

		if want != got {
			t.Fatalf("want %v, got %v", want, got)
		}
	}
	neqNil := func(err error) {
		t.Helper()

		if err == nil {
			t.Fatalf("expected non-nil error")
		}
	}
	eqNil := func(err error) {
		t.Helper()

		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	w := NewWriter(&errWriter{1})
	eqNil(w.WriteBool(true))
	got, err := w.Write([]byte{0x01, 0x02})
	eq(1, got)
	neqNil(err)
	neqNil(w.Close())

	w = NewWriter(&errWriter{0})
	neqNil(w.WriteBits(0x00, 9))

	w = NewWriter(&errWriter{1})
	neqNil(w.WriteBits(0x00, 17))

	w = NewWriter(&errWriter{})
	eqNil(w.WriteBits(0x00, 7))
	neqNil(w.WriteBool(false))

	w = NewWriter(&errWriter{})
	eqNil(w.WriteBool(true))
	_, err = w.Align()
	neqNil(err)
}
