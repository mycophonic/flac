package flac_test

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/mycophonic/flac/frame"
)

// Random damage against the reference decoder. Streams from a corpus are
// damaged in the ways files get damaged (bits flipped, spans truncated,
// dropped, doubled, overwritten, zeroed, the metadata and the frame headers
// hit on purpose) and decoded by this decoder and by the reference.
// Whatever happens, this decoder must neither panic nor hang; where both
// decode, at the depths the reference writes raw, the samples must be the
// same; where one decodes and the other refuses, the case is logged for the
// record. DAMAGE_CASES sets the number of cases (default 200); DAMAGE_CORPUS
// adds directories (a path list) of .flac files to damage on top of the
// encoder's own stream and the small IETF subset files.

// frameOffsets finds the byte offsets of frame sync words.
func frameOffsets(data []byte) []int {
	var offsets []int

	for i := 0; i+1 < len(data); i++ {
		if data[i] == 0xFF && data[i+1]&0xFE == 0xF8 {
			offsets = append(offsets, i)
		}
	}

	return offsets
}

// damage returns a damaged copy of data and the name of what was done.
func damage(rng *rand.Rand, data []byte) ([]byte, string) {
	out := bytes.Clone(data)
	if len(out) < 8 {
		return out, "untouched"
	}

	span := func() (int, int) {
		start := rng.IntN(len(out))
		length := 1 + rng.IntN(min(len(out)-start, 1+rng.IntN(4096)))

		return start, length
	}

	switch rng.IntN(8) {
	case 0:
		n := 1 + rng.IntN(8)
		for range n {
			out[rng.IntN(len(out))] ^= byte(1 << rng.IntN(8))
		}

		return out, fmt.Sprintf("%d bits flipped", n)
	case 1:
		at := rng.IntN(len(out))

		return out[:at], fmt.Sprintf("truncated at %d of %d", at, len(out))
	case 2:
		start, length := span()

		return append(out[:start], out[start+length:]...), fmt.Sprintf("%d bytes dropped at %d", length, start)
	case 3:
		start, length := span()
		dup := slices.Clone(out[start : start+length])

		return slices.Insert(out, start+length, dup...), fmt.Sprintf("%d bytes doubled at %d", length, start)
	case 4:
		start, length := span()
		for i := start; i < start+length; i++ {
			out[i] = byte(rng.IntN(256))
		}

		return out, fmt.Sprintf("%d bytes overwritten at %d", length, start)
	case 5:
		start, length := span()
		for i := start; i < start+length; i++ {
			out[i] = 0
		}

		return out, fmt.Sprintf("%d bytes zeroed at %d", length, start)
	case 6:
		// The metadata: the first kilobyte, or less.
		at := rng.IntN(min(len(out), 1024))
		out[at] ^= byte(1 << rng.IntN(8))

		return out, fmt.Sprintf("a metadata bit flipped at %d", at)
	default:
		// A frame header: the bytes after a sync word.
		offsets := frameOffsets(out)
		if len(offsets) == 0 {
			return out, "untouched"
		}

		at := offsets[rng.IntN(len(offsets))] + rng.IntN(8)
		if at >= len(out) {
			return out, "untouched"
		}

		out[at] ^= byte(1 << rng.IntN(8))

		return out, fmt.Sprintf("a frame header bit flipped at %d", at)
	}
}

// damageCorpus is the encoder's own stereo stream, the IETF subset files
// under 4 MB, and DAMAGE_CORPUS's files.
func damageCorpus(t *testing.T) map[string][]byte {
	t.Helper()

	corpus := map[string][]byte{}

	own := filepath.Join(t.TempDir(), "own.flac")
	c := encodeMatrixCase{
		n:          20000,
		bps:        16,
		channels:   2,
		blockSize:  1152,
		assignment: frame.ChannelsMidSide,
		analysis:   true,
	}
	writeMatrixStream(t, own, c)

	data, err := os.ReadFile(own)
	if err != nil {
		t.Fatal(err)
	}

	corpus["own stereo 16-bit"] = data

	dirs := []string{filepath.Join("testdata", "flac-test-files", "subset")}
	dirs = append(dirs, filepath.SplitList(os.Getenv("DAMAGE_CORPUS"))...)

	for _, dir := range dirs {
		files, _ := filepath.Glob(filepath.Join(dir, "*.flac"))
		for _, path := range files {
			data, err := os.ReadFile(path)
			if err != nil || len(data) > 4<<20 {
				continue
			}

			corpus[filepath.Base(dir)+"/"+filepath.Base(path)] = data
		}
	}

	return corpus
}

// decodeBytesWithDeadline decodes data from a file in a goroutine, so a hang
// is a failure and not a stuck test.
func decodeBytesWithDeadline(t *testing.T, dir string, data []byte, deadline time.Duration) (setOutcome, bool) {
	t.Helper()

	path := filepath.Join(dir, "case.flac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	done := make(chan setOutcome, 1)
	go func() { done <- decodeWhole(path) }()

	select {
	case out := <-done:
		return out, true
	case <-time.After(deadline):
		return setOutcome{}, false
	}
}

func saveCase(t *testing.T, i int, data []byte) string {
	t.Helper()

	dir := os.Getenv("DAMAGE_KEEP")
	if dir == "" {
		dir = t.TempDir()
	}

	path := filepath.Join(dir, fmt.Sprintf("damage-%03d.flac", i))
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return path
}

func TestRandomDamageAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	cases := 200
	if s := os.Getenv("DAMAGE_CASES"); s != "" {
		cases, _ = strconv.Atoi(s)
	}

	corpus := damageCorpus(t)
	names := make([]string, 0, len(corpus))

	for name := range corpus {
		names = append(names, name)
	}

	// Sorted for a reproducible walk under one seed.
	slices.Sort(names)

	rng := rand.New(rand.NewPCG(20261026, uint64(cases)))
	dir := t.TempDir()

	var bothOK, refOnly, oursOnly, bothRefuse int

	for i := range cases {
		name := names[rng.IntN(len(names))]
		data, what := damage(rng, corpus[name])

		ours, finished := decodeBytesWithDeadline(t, dir, data, 20*time.Second)
		if !finished {
			t.Fatalf(
				"case %d (%s, %s): the decoder did not finish in 20 s; input at %s",
				i,
				name,
				what,
				saveCase(t, i, data),
			)
		}

		if errors.Is(ours.err, errSetPanic) {
			t.Fatalf("case %d (%s, %s): %v; input at %s", i, name, what, ours.err, saveCase(t, i, data))
		}

		path := filepath.Join(dir, "case.flac")
		refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).Run()

		switch {
		case ours.err == nil && refErr == nil:
			bothOK++

			if ours.bps%8 != 0 {
				continue
			}

			reference, rawErr := referenceRaw(t, flacBin, path)
			if rawErr != nil {
				t.Fatalf(
					"case %d (%s, %s): the reference accepts the stream but will not write it raw: %v",
					i,
					name,
					what,
					rawErr,
				)
			}

			if !bytes.Equal(ours.pcm, reference) {
				t.Fatalf(
					"case %d (%s, %s): both decode, the samples differ (%d bytes against the reference's %d); input at %s",
					i,
					name,
					what,
					len(ours.pcm),
					len(reference),
					saveCase(t, i, data),
				)
			}
		case ours.err == nil:
			oursOnly++

			t.Logf("case %d (%s, %s): decoded %d frames where the reference refuses", i, name, what, ours.frames)
		case refErr == nil:
			refOnly++

			t.Logf("case %d (%s, %s): refused (%v) where the reference decodes", i, name, what, ours.err)
		default:
			bothRefuse++
		}
	}

	t.Logf("%d cases: both decode %d, only the reference %d, only this decoder %d, both refuse %d",
		cases, bothOK, refOnly, oursOnly, bothRefuse)
}
