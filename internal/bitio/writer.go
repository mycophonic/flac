// Package bitio provides an optimized bit-level Writer.
//
// Trimmed and adapted from github.com/icza/bitio v1.1.0 (Apache 2.0,
// Copyright 2016 Andras Belicza). Only the Writer methods used by the
// flac encoder are retained.
package bitio

import (
	"bufio"
	"io"
)

// writerAndByteWriter is an io.Writer and io.ByteWriter at the same time.
type writerAndByteWriter interface {
	io.Writer
	io.ByteWriter
}

// Writer is the bit writer implementation.
//
// For convenience, it also implements io.WriteCloser and io.ByteWriter.
type Writer struct {
	out       writerAndByteWriter
	wrapperbw *bufio.Writer // wrapper bufio.Writer if the target does not implement io.ByteWriter
	cache     byte          // unwritten bits are stored here
	bits      byte          // number of unwritten bits in cache
}

// NewWriter returns a new Writer using the specified io.Writer as the output.
//
// Must be closed in order to flush cached data.
// If you can't or don't want to close it, flushing data can also be forced
// by calling Align().
func NewWriter(out io.Writer) *Writer {
	w := &Writer{}

	bw, ok := out.(writerAndByteWriter)
	if !ok {
		w.wrapperbw = bufio.NewWriter(out)
		bw = w.wrapperbw
	}

	w.out = bw

	return w
}

// Write writes len(p) bytes (8 * len(p) bits) to the underlying writer.
//
// Write implements io.Writer, and gives a byte-level interface to the bit stream.
// This will give best performance if the underlying io.Writer is aligned
// to a byte boundary (else all the individual bytes are spread to multiple bytes).
// Byte boundary can be ensured by calling Align().
func (w *Writer) Write(p []byte) (int, error) {
	// w.bits will be the same after writing 8 bits, so we don't need to update that.
	if w.bits == 0 {
		return w.out.Write(p)
	}

	for i, b := range p {
		if err := w.writeUnalignedByte(b); err != nil {
			return i, err
		}
	}

	return len(p), nil
}

// WriteBits writes out the n lowest bits of r.
// Bits of r in positions higher than n are ignored.
//
// For example:
//
//	err := w.WriteBits(0x1234, 8)
//
// is equivalent to:
//
//	err := w.WriteBits(0x34, 8)
//
// #nosec G115 -- bit-packing: byte(...) intentionally masks to low 8 bits per the algorithm
func (w *Writer) WriteBits(r uint64, n uint8) error {
	// Mask out bits at positions >= n so we never corrupt previously cached bits.
	r &= 1<<n - 1

	// Some optimization, frequent cases.
	newbits := w.bits + n
	if newbits < 8 {
		// r fits into cache, no write will occur to out.
		w.cache |= byte(r) << (8 - newbits)
		w.bits = newbits

		return nil
	}

	if newbits > 8 {
		// cache will be filled, and there will be more bits to write.
		// "Fill cache" and write it out.
		free := 8 - w.bits
		if err := w.out.WriteByte(w.cache | byte(r>>(n-free))); err != nil {
			return err
		}

		n -= free

		// Write out whole bytes.
		for n >= 8 {
			n -= 8
			// No need to mask r, converting to byte will mask out higher bits.
			if err := w.out.WriteByte(byte(r >> n)); err != nil {
				return err
			}
		}

		// Put remaining into cache.
		if n > 0 {
			// Note: n < 8 (in case of n=8, 1<<n would overflow byte).
			w.cache, w.bits = (byte(r)&((1<<n)-1))<<(8-n), n
		} else {
			w.cache, w.bits = 0, 0
		}

		return nil
	}

	// cache will be filled exactly with the bits to be written.
	bb := w.cache | byte(r)
	w.cache, w.bits = 0, 0

	return w.out.WriteByte(bb)
}

// WriteByte writes 8 bits.
//
// WriteByte implements io.ByteWriter.
func (w *Writer) WriteByte(b byte) error {
	// w.bits will be the same after writing 8 bits, so we don't need to update that.
	if w.bits == 0 {
		return w.out.WriteByte(b)
	}

	return w.writeUnalignedByte(b)
}

// WriteBool writes one bit: 1 if value is true, 0 otherwise.
//
//revive:disable-next-line:flag-parameter
func (w *Writer) WriteBool(value bool) error {
	if w.bits == 7 {
		out := w.cache
		if value {
			out |= 1
		}

		if err := w.out.WriteByte(out); err != nil {
			return err
		}

		w.cache, w.bits = 0, 0

		return nil
	}

	w.bits++
	if value {
		w.cache |= 1 << (8 - w.bits)
	}

	return nil
}

// Align aligns the bit stream to a byte boundary,
// so next write will start/go into a new byte.
// If there are cached bits, they are first written to the output.
// Returns the number of skipped (unset but still written) bits.
func (w *Writer) Align() (uint8, error) {
	var skipped uint8

	if w.bits > 0 {
		if err := w.out.WriteByte(w.cache); err != nil {
			return 0, err
		}

		skipped = 8 - w.bits
		w.cache, w.bits = 0, 0
	}

	if w.wrapperbw != nil {
		if err := w.wrapperbw.Flush(); err != nil {
			return skipped, err
		}
	}

	return skipped, nil
}

// Close closes the bit writer, writes out cached bits.
// It does not close the underlying io.Writer.
//
// Close implements io.Closer.
func (w *Writer) Close() error {
	// Make sure cached bits are flushed.
	if _, err := w.Align(); err != nil {
		return err
	}

	return nil
}

// writeUnalignedByte writes 8 bits which are (may be) unaligned.
func (w *Writer) writeUnalignedByte(b byte) error {
	bits := w.bits
	if err := w.out.WriteByte(w.cache | b>>bits); err != nil {
		return err
	}

	w.cache = (b & (1<<bits - 1)) << (8 - bits)

	return nil
}
