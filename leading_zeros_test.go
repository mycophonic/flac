package flac_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"slices"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

// streamInfoEnd is where the fLaC marker and the last STREAMINFO block end in
// a stream the hostile builder writes: 4 + 4 + 34 bytes.
const streamInfoEnd = 42

// twoFrames is a valid stream of two frames, numbered 0 and 1, STREAMINFO
// counting both; it returns the stream and the offset of the second frame.
func twoFrames() ([]byte, int) {
	first := validFrame()
	first.streamSamples = 32

	second := validFrame()
	second.number = []byte{1}

	stream := first.build()
	frameLen := len(stream) - streamInfoEnd

	return append(stream, second.build()[streamInfoEnd:]...), streamInfoEnd + frameLen
}

// decodeAll decodes a stream to its end and returns the frames read and the
// error that stopped it, nil at a clean end.
func decodeAll(data []byte) (int, error) {
	stream, err := flac.New(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}

	defer func() { _ = stream.Close() }()

	frames := 0

	for {
		if _, err := stream.ParseNext(); err != nil {
			if errors.Is(err, io.EOF) {
				return frames, nil
			}

			return frames, err
		}

		frames++
	}
}

// Zero bytes between the metadata and the first frame are undeclared padding
// some releases carry (HDtracks, 16 MB of it), which this decoder scans past
// where the reference refuses: a decision, recorded here with the reference's
// verdict. Zeros anywhere else are lost data: between two frames and after
// the last, the decoder refuses as the reference does, where it used to
// resync over them and report a clean end.
func TestZeroBytesAroundFrames(t *testing.T) {
	t.Parallel()

	flacBin, _ := exec.LookPath("flac")
	stream, second := twoFrames()

	if frames, err := decodeAll(stream); err != nil || frames != 2 {
		t.Fatalf("the two-frame stream: %d frames, %v", frames, err)
	}

	for _, n := range []int{1, 2, 3, 16, 4096} {
		zeros := make([]byte, n)

		t.Run(fmt.Sprintf("%d before the first frame", n), func(t *testing.T) {
			t.Parallel()

			padded := slices.Concat(stream[:streamInfoEnd], zeros, stream[streamInfoEnd:])

			frames, err := decodeAll(padded)
			if n == 1 {
				// One zero byte cannot be read as a 14-bit zero sync: it is an
				// invalid sync code, as it is to the reference.
				if !errors.Is(err, frame.ErrInvalidSync) {
					t.Fatalf("one zero byte before the first frame: %d frames, %v", frames, err)
				}
			} else if err != nil || frames != 2 {
				t.Fatalf("%d zero bytes before the first frame: %d frames, %v", n, frames, err)
			}

			if flacBin != "" && n > 1 && referenceAccepts(t, flacBin, padded) {
				t.Fatalf(
					"the reference accepts %d zero bytes before the first frame; the decision assumed it refuses",
					n,
				)
			}
		})

		t.Run(fmt.Sprintf("%d between the frames", n), func(t *testing.T) {
			t.Parallel()

			cut := slices.Concat(stream[:second], zeros, stream[second:])

			frames, err := decodeAll(cut)
			if !errors.Is(err, frame.ErrInvalidSync) || frames != 1 {
				t.Fatalf(
					"%d zero bytes between the frames: %d frames, %v; want one frame and the invalid sync",
					n,
					frames,
					err,
				)
			}

			if flacBin != "" && referenceAccepts(t, flacBin, cut) {
				t.Fatalf("the reference accepts %d zero bytes between the frames", n)
			}
		})

		t.Run(fmt.Sprintf("%d after the last frame", n), func(t *testing.T) {
			t.Parallel()

			trailing := slices.Concat(stream, zeros)

			// A single trailing zero cannot fill a sync code's 14 bits: the
			// stream ends inside it, which is the cut, not the sync.
			frames, err := decodeAll(trailing)
			if (!errors.Is(err, frame.ErrInvalidSync) && !errors.Is(err, io.ErrUnexpectedEOF)) || frames != 2 {
				t.Fatalf(
					"%d zero bytes after the last frame: %d frames, %v; want two frames and a refusal",
					n,
					frames,
					err,
				)
			}
		})
	}
}

// seekTableLen is a SEEKTABLE block of one point: 4 header bytes, 18 of
// point.
const seekTableLen = 22

// withSeekPoint inserts a last SEEKTABLE block of one point after the
// STREAMINFO of a stream the hostile builder wrote. Offsets in the table
// count from the first frame, as the format has them, so the offset given is
// the one a caller measured in the stream before the block went in.
func withSeekPoint(stream []byte, sample, offset uint64) []byte {
	block := make([]byte, seekTableLen)
	block[0] = 0x83 // last block, SEEKTABLE
	block[3] = 18
	binary.BigEndian.PutUint64(block[4:], sample)
	binary.BigEndian.PutUint64(block[12:], offset-streamInfoEnd)
	binary.BigEndian.PutUint16(block[20:], 16)

	out := slices.Concat(stream[:streamInfoEnd], block, stream[streamInfoEnd:])
	out[4] = 0x00 // STREAMINFO is no longer the last block

	return out
}

// seekTo opens a stream, reads the frames asked for, then seeks to the sample
// and returns what the seek said.
func seekTo(data []byte, framesFirst int, sample uint64) (uint64, error) {
	stream, err := flac.NewSeek(bytes.NewReader(data))
	if err != nil {
		return 0, err
	}

	defer func() { _ = stream.Close() }()

	for range framesFirst {
		if _, err := stream.ParseNext(); err != nil {
			return 0, err
		}
	}

	return stream.Seek(sample)
}

// A seek point on zero bytes between two frames is the same lost data as a
// read reaching them: the seek refuses, whether it is the first thing the
// stream does or comes after a frame was read. The allowance for zeros
// before the first frame belongs to the start of the audio data, so a seek
// landing there, on a stream padded that way, still scans to its first
// frame.
func TestSeekOntoZeroBytes(t *testing.T) {
	t.Parallel()

	stream, second := twoFrames()
	zeros := make([]byte, 16)

	// The point for sample 16 names where the second frame stands, in both
	// streams; in the cut one the zeros stand there instead.
	clean := withSeekPoint(stream, 16, uint64(second))
	cut := withSeekPoint(
		slices.Concat(stream[:second], zeros, stream[second:]),
		16,
		uint64(second),
	)

	// The padded stream carries the zeros before its first frame; its point
	// for sample 16 is measured past them.
	padded := withSeekPoint(
		slices.Concat(stream[:streamInfoEnd], zeros, stream[streamInfoEnd:]),
		16,
		uint64(second+len(zeros)),
	)

	if got, err := seekTo(clean, 0, 16); err != nil || got != 16 {
		t.Fatalf("the clean stream, seek to 16 first: %d, %v", got, err)
	}

	for _, framesFirst := range []int{0, 1} {
		t.Run(fmt.Sprintf("after %d frames read", framesFirst), func(t *testing.T) {
			t.Parallel()

			got, err := seekTo(cut, framesFirst, 16)
			if !errors.Is(err, frame.ErrInvalidSync) {
				t.Fatalf("seek onto the zeros between the frames: %d, %v; want the invalid sync", got, err)
			}
		})
	}

	t.Run("padded stream, seek to its start", func(t *testing.T) {
		t.Parallel()

		if got, err := seekTo(padded, 0, 0); err != nil || got != 0 {
			t.Fatalf("seek to 0 on the padded stream: %d, %v", got, err)
		}

		if got, err := seekTo(padded, 1, 16); err != nil || got != 16 {
			t.Fatalf("seek to 16 on the padded stream after a frame: %d, %v", got, err)
		}
	})
}
