package flac_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"hash/fnv"
	"io"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"testing"

	"github.com/mycophonic/flac"
)

// seekWindow is how many samples a seek target is checked over.
const seekWindow = 2000

// frameStarts decodes the file once and returns each frame's first sample
// and the total, or false when this decoder does not read it whole.
func frameStarts(path string) (starts []uint64, total uint64, ok bool) {
	stream, err := flac.ParseFile(path)
	if err != nil {
		return nil, 0, false
	}

	defer func() { _ = stream.Close() }()

	for {
		f, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			return starts, total, total > 0
		}

		if err != nil {
			return nil, 0, false
		}

		starts = append(starts, total)
		total += uint64(f.BlockSize)
	}
}

// seekTargets are the harness's: the first samples, both sides of the second
// frame's start, the middle, both sides of the last frame's start, the last
// samples, and four seeded random ones.
func seekTargets(starts []uint64, total uint64, name string) []uint64 {
	set := map[uint64]bool{}

	add := func(t int64) {
		if t >= 0 && uint64(t) < total {
			set[uint64(t)] = true
		}
	}

	n := int64(total)
	last := int64(starts[len(starts)-1])

	for _, t := range []int64{0, 1, n / 2, last - 1, last, last + 1, n - 2, n - 1} {
		add(t)
	}

	if len(starts) > 1 {
		b := int64(starts[1])
		add(b - 1)
		add(b)
		add(b + 1)
	}

	h := fnv.New64a()
	_, _ = h.Write([]byte(name))
	rng := rand.New(rand.NewPCG(h.Sum64(), 0))

	for range 4 {
		add(rng.Int64N(n))
	}

	targets := make([]uint64, 0, len(set))
	for t := range set {
		targets = append(targets, t)
	}

	slices.Sort(targets)

	return targets
}

// seekAndDecode seeks to target and returns the next count samples from it,
// as little-endian PCM of bytesPer bytes.
func seekAndDecode(t *testing.T, stream *flac.Stream, target, count uint64, bytesPer int) []byte {
	t.Helper()

	start, err := stream.Seek(target)
	if err != nil {
		t.Fatalf("Seek(%d): %v", target, err)
	}

	if start > target {
		t.Fatalf("Seek(%d) landed on the frame starting at %d", target, start)
	}

	skip := target - start
	channels := int(stream.Info.NChannels)
	need := count * uint64(bytesPer) * uint64(channels)
	out := make([]byte, 0, need)

	var b [4]byte

	for uint64(len(out)) < need {
		f, parseErr := stream.ParseNext()
		if errors.Is(parseErr, io.EOF) {
			break
		}

		if parseErr != nil {
			t.Fatalf("ParseNext after Seek(%d): %v", target, parseErr)
		}

		for i := range int(f.BlockSize) {
			if skip > 0 {
				skip--

				continue
			}

			for _, sub := range f.Subframes {
				binary.LittleEndian.PutUint32(b[:], uint32(sub.Samples[i]))
				out = append(out, b[:bytesPer]...)
			}

			if uint64(len(out)) >= need {
				break
			}
		}
	}

	return out
}

// The seek matrix the harness ran: for every IETF subset file this decoder
// reads whole, a dozen targets around the frame boundaries and the ends plus
// four seeded random ones, each seek followed by two thousand samples that
// must be the ones flac -d --skip/--until gives. #13, #14 and #17 were seeks
// that landed on stale bytes or the wrong frame.
func TestSeekMatrixAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	if len(files) == 0 {
		t.Skip("no IETF test files")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			starts, total, ok := frameStarts(path)
			if !ok {
				t.Skip("this decoder does not read the file whole")
			}

			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = file.Close() }()

			stream, err := flac.NewSeek(file)
			if err != nil {
				t.Fatalf("NewSeek: %v", err)
			}

			if stream.Info.BitsPerSample%8 != 0 {
				t.Skip("raw comparison needs a whole-byte depth")
			}

			bytesPer := int(stream.Info.BitsPerSample) / 8

			for _, target := range seekTargets(starts, total, filepath.Base(path)) {
				count := min(uint64(seekWindow), total-target)

				want, refErr := exec.CommandContext(t.Context(), flacBin,
					"-d", "-s", "-f", "--force-raw-format", "--sign=signed", "--endian=little",
					"--skip="+strconv.FormatUint(target, 10),
					"--until="+strconv.FormatUint(target+count, 10),
					"-o", "-", path).Output()
				if refErr != nil {
					// No total sample count, or a target the reference will
					// not seek to: nothing to compare against.
					t.Logf("flac -d --skip=%d refuses: %v", target, refErr)

					continue
				}

				got := seekAndDecode(t, stream, target, count, bytesPer)
				if !bytes.Equal(got, want) {
					t.Fatalf("Seek(%d): %d bytes differing from the reference's %d", target, len(got), len(want))
				}
			}
		})
	}
}
