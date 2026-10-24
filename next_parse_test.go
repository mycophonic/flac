package flac_test

// Next then Parse is ParseNext. Over the IETF subset, a stream read with
// Next and Frame.Parse yields the same headers and samples, frame for
// frame, as one read with ParseNext, and both end at the same frame.

import (
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

func TestNextThenParseIsParseNext(t *testing.T) {
	t.Parallel()

	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	if len(files) == 0 {
		t.Skip("no IETF test files under testdata/flac-test-files")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			whole, err := flac.ParseFile(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = whole.Close() }()

			headers, err := flac.ParseFile(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = headers.Close() }()

			for i := 0; ; i++ {
				a, errA := whole.ParseNext()

				b, errB := headers.Next()
				if errB == nil {
					errB = b.Parse()
				}

				if errors.Is(errA, io.EOF) || errors.Is(errB, io.EOF) {
					if !errors.Is(errA, io.EOF) || !errors.Is(errB, io.EOF) {
						t.Fatalf("frame %d: ParseNext %v, Next then Parse %v", i, errA, errB)
					}

					return
				}

				if errA != nil || errB != nil {
					t.Fatalf("frame %d: ParseNext %v, Next then Parse %v", i, errA, errB)
				}

				if a.Header != b.Header {
					t.Fatalf("frame %d: headers differ: %+v against %+v", i, a.Header, b.Header)
				}

				if !sameSamplesOf(a.Subframes, b.Subframes) {
					t.Fatalf("frame %d: the samples differ", i)
				}
			}
		})
	}
}

func sameSamplesOf(a, b []*frame.Subframe) bool {
	if len(a) != len(b) {
		return false
	}

	for i := range a {
		if len(a[i].Samples) != len(b[i].Samples) {
			return false
		}

		for j := range a[i].Samples {
			if a[i].Samples[j] != b[i].Samples[j] {
				return false
			}
		}
	}

	return true
}
