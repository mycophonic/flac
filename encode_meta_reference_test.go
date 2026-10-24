package flac_test

// The metadata encoder against metaflac. Every IETF subset file is read
// with ParseFile and written again through NewEncoder with its StreamInfo
// and blocks and its frames as parsed; `flac -t` must accept the result,
// and `metaflac --list` must print the same for both files, block for
// block, field for field, but for what the encoder measures itself.

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/mycophonic/flac"
)

func metaflacListing(t *testing.T, metaflacBin, path string) string {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), metaflacBin, "--list", path).Output()
	if err != nil {
		t.Fatalf("metaflac --list %s: %v", filepath.Base(path), err)
	}

	// The first line names the file.
	_, rest, _ := strings.Cut(string(out), "\n")

	return rest
}

func TestEncodedMetadataMatchesMetaflac(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	metaflacBin, err := exec.LookPath("metaflac")
	if err != nil {
		t.Skip("no metaflac on the PATH")
	}

	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	if len(files) == 0 {
		t.Skip("no IETF test files under testdata/flac-test-files")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			stream, err := flac.ParseFile(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = stream.Close() }()

			out := filepath.Join(t.TempDir(), "again.flac")

			file, err := os.Create(out)
			if err != nil {
				t.Fatal(err)
			}

			enc, err := flac.NewEncoder(file, stream.Info, stream.Blocks...)
			if err != nil {
				t.Fatalf("NewEncoder: %v", err)
			}

			enc.EnablePredictionAnalysis(false)

			for {
				f, err := stream.ParseNext()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatal(err)
				}

				if err := enc.WriteFrame(f); err != nil {
					t.Fatalf("WriteFrame: %v", err)
				}
			}

			if err := enc.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			if msg, err := exec.CommandContext(t.Context(), flacBin, "-t", "-s", out).CombinedOutput(); err != nil {
				t.Fatalf("the reference refuses the re-encoded stream: %v\n%s", err, bytes.TrimSpace(msg))
			}

			sameListing(t, metaflacListing(t, metaflacBin, path), metaflacListing(t, metaflacBin, out))
		})
	}
}

// listedNumber is the number a listing line carries after its colon.
func listedNumber(line string) int64 {
	_, after, _ := strings.Cut(line, ":")
	fields := strings.Fields(after)

	if len(fields) == 0 {
		return -1
	}

	n, err := strconv.ParseInt(fields[0], 10, 64)
	if err != nil {
		return -1
	}

	return n
}

// sameListing compares metaflac's listings line by line. Three fields are
// the encoder's to measure and may differ from the original without being
// wrong: the frame size bounds, which the original may leave at 0 and which
// this encoder's shorter header coding can bring down by a byte; the total
// sample count, which the original may leave at 0; and the block size
// bounds of a variable-block-size original, where the original records the
// encoder's settings and this encoder the smallest block written before the
// last and the largest written at all. Everything else must match.
func sameListing(t *testing.T, want, got string) {
	t.Helper()

	wl, gl := strings.Split(want, "\n"), strings.Split(got, "\n")
	if len(wl) != len(gl) {
		t.Fatalf("metaflac lists %d lines for the original and %d for the re-encoded file", len(wl), len(gl))
	}

	var minBlock, maxBlock int64

	for _, line := range wl {
		switch field(line) {
		case "minimum blocksize":
			minBlock = listedNumber(line)
		case "maximum blocksize":
			maxBlock = listedNumber(line)
		}
	}

	variable := minBlock != maxBlock

	for i := range wl {
		w, g := wl[i], gl[i]
		if w == g {
			continue
		}

		wn, gn := listedNumber(w), listedNumber(g)
		ok := false

		switch field(w) {
		case "minimum framesize", "maximum framesize":
			ok = wn == 0 || (gn > 0 && gn <= wn)
		case "total samples":
			ok = wn == 0 && gn > 0
		case "minimum blocksize":
			ok = variable && gn >= wn
		case "maximum blocksize":
			ok = variable && gn <= wn
		}

		if !ok {
			t.Fatalf(
				"metaflac lists the re-encoded file differently at line %d:\n original: %.120s\n again:    %.120s",
				i+1,
				w,
				g,
			)
		}
	}
}

// field is the label of a listing line, trimmed.
func field(line string) string {
	label, _, _ := strings.Cut(line, ":")

	return strings.TrimSpace(label)
}
