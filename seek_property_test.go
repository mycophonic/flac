package flac_test

// Seek against a sequential decode, over the IETF subset: for sample
// numbers at the stream's edges and inside it, Seek lands on the frame
// holding the sample (its first sample at or before it, its last at or
// after), and the frames read from there are the ones a sequential decode
// read at those positions, sample for sample; past the end is refused.

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

// sequential reads every frame of path, keyed by its first sample number.
func sequential(t *testing.T, path string) (frames map[uint64]*frame.Frame, total uint64) {
	t.Helper()

	stream, err := flac.ParseFile(path)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = stream.Close() }()

	frames = map[uint64]*frame.Frame{}

	for {
		f, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			return frames, total
		}

		if err != nil {
			t.Fatalf("sequential decode: %v", err)
		}

		// ParseNext reuses its buffers: keep a copy of the samples.
		kept := &frame.Frame{Header: f.Header, Subframes: make([]*frame.Subframe, len(f.Subframes))}
		for i, sub := range f.Subframes {
			kept.Subframes[i] = &frame.Subframe{Samples: append([]int32(nil), sub.Samples...), NSamples: sub.NSamples}
		}

		frames[f.SampleNumber()] = kept
		total += uint64(f.BlockSize)
	}
}

func sameSamples(a, b *frame.Frame) bool {
	if len(a.Subframes) != len(b.Subframes) || a.BlockSize != b.BlockSize {
		return false
	}

	for i := range a.Subframes {
		x, y := a.Subframes[i].Samples, b.Subframes[i].Samples
		if len(x) != len(y) {
			return false
		}

		for j := range x {
			if x[j] != y[j] {
				return false
			}
		}
	}

	return true
}

func TestSeekFindsTheFrameOfTheSample(t *testing.T) {
	t.Parallel()

	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	if len(files) == 0 {
		t.Skip("no IETF test files under testdata/flac-test-files")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			frames, total := sequential(t, path)
			if total == 0 {
				t.Skip("no frames")
			}

			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = file.Close() }()

			stream, err := flac.NewSeek(file)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = stream.Close() }()

			targets := []uint64{0, 1, total / 3, total / 2, total - 2, total - 1}

			for _, target := range targets {
				landed, err := stream.Seek(target)
				if err != nil {
					t.Fatalf("Seek(%d) of %d: %v", target, total, err)
				}

				want, ok := frames[landed]
				if !ok {
					t.Fatalf("Seek(%d) landed on %d, which starts no frame", target, landed)
				}

				if landed > target || landed+uint64(want.BlockSize) <= target {
					t.Fatalf("Seek(%d) landed on the frame %d..%d, which does not hold the sample",
						target, landed, landed+uint64(want.BlockSize)-1)
				}

				got, err := stream.ParseNext()
				if err != nil {
					t.Fatalf("after Seek(%d): %v", target, err)
				}

				if got.SampleNumber() != landed || !sameSamples(got, want) {
					t.Fatalf("after Seek(%d), the frame read starts at %d and differs from the sequential frame at %d",
						target, got.SampleNumber(), landed)
				}

				// The next frame too: the reader is positioned, not just the
				// one frame found.
				next, err := stream.ParseNext()
				if errors.Is(err, io.EOF) {
					continue
				}

				if err != nil {
					t.Fatalf("after Seek(%d), the second frame: %v", target, err)
				}

				following := landed + uint64(got.BlockSize)

				wantNext, known := frames[next.SampleNumber()]
				if !known || next.SampleNumber() != following || !sameSamples(next, wantNext) {
					t.Fatalf("after Seek(%d), the second frame starts at %d, want %d, or differs",
						target, next.SampleNumber(), following)
				}
			}

			for _, past := range []uint64{total, total + 1, 1 << 62} {
				if landed, err := stream.Seek(past); err == nil {
					t.Fatalf("Seek(%d) past the %d samples landed on %d", past, total, landed)
				}
			}
		})
	}
}
