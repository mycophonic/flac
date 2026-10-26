package flac_test

// The IETF uncommon and faulty sets against the reference decoder. Every
// file is decoded to the end by this decoder, under a deadline and with a
// panic a failure, and tested by `flac -t`; where both accept and the
// reference writes the depth raw, the samples are compared. The decisions
// themselves are logged: the sets are made of files a decoder may refuse,
// and which ones this decoder refuses is its documented contract.
// FLAC_TEST_FILES names the flac-test-files checkout; the default is
// testdata/flac-test-files.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

// errSetPanic marks a recovered panic, the one outcome never allowed.
var errSetPanic = errors.New("the decoder panicked")

type setOutcome struct {
	pcm      []byte
	frames   int
	bps      uint8
	channels int
	err      error
}

// decodeWhole decodes the file to its end and interleaves the samples as
// little-endian signed PCM of the stream's depth, when that depth is whole
// bytes.
func decodeWhole(path string) (out setOutcome) {
	defer func() {
		if r := recover(); r != nil {
			out.err = fmt.Errorf("%w: %v", errSetPanic, r)
		}
	}()

	stream, err := flac.ParseFile(path)
	if err != nil {
		return setOutcome{err: err}
	}

	defer func() { _ = stream.Close() }()

	out.bps = stream.Info.BitsPerSample
	out.channels = int(stream.Info.NChannels)
	bytesPer := int(out.bps) / 8

	for {
		f, err := stream.ParseNext()
		if errors.Is(err, io.EOF) {
			return out
		}

		if err != nil {
			out.err = err

			return out
		}

		out.frames++

		if out.bps%8 != 0 {
			continue
		}

		var b [4]byte

		for i := range int(f.BlockSize) {
			for _, sub := range f.Subframes {
				binary.LittleEndian.PutUint32(b[:], uint32(sub.Samples[i]))
				out.pcm = append(out.pcm, b[:bytesPer]...)
			}
		}
	}
}

func referenceRaw(t *testing.T, flacBin, path string) ([]byte, error) {
	t.Helper()

	return exec.CommandContext(t.Context(), flacBin,
		"-d", "-s", "-f", "--force-raw-format", "--sign=signed", "--endian=little", "-o", "-", path).Output()
}

func TestIETFSetsAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	ffmpegBin := ffmpegOrEmpty()

	root := os.Getenv("FLAC_TEST_FILES")
	if root == "" {
		root = filepath.Join("testdata", "flac-test-files")
	}

	var files []string

	for _, set := range []string{"subset", "uncommon", "faulty"} {
		matches, _ := filepath.Glob(filepath.Join(root, set, "*.flac"))
		files = append(files, matches...)
	}

	if len(files) == 0 {
		t.Skip("no uncommon or faulty files under " + root)
	}

	for _, path := range files {
		t.Run(filepath.Base(filepath.Dir(path))+"/"+filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			done := make(chan setOutcome, 1)

			go func() { done <- decodeWhole(path) }()

			var ours setOutcome

			select {
			case ours = <-done:
			case <-time.After(60 * time.Second):
				t.Fatal("the decoder did not finish in 60 s")
			}

			if errors.Is(ours.err, errSetPanic) {
				t.Fatal(ours.err)
			}

			reference := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).Run() == nil

			t.Logf(
				"%s: this decoder: %d frames, %v; the reference accepts: %v",
				filepath.Base(path),
				ours.frames,
				ours.err,
				reference,
			)

			if ours.err != nil || !reference || ours.bps%8 != 0 {
				return
			}

			want, err := referenceRaw(t, flacBin, path)
			if err != nil {
				t.Fatalf("flac -d: %v", err)
			}

			if !bytes.Equal(ours.pcm, want) {
				at := 0
				for at < len(ours.pcm) && at < len(want) && ours.pcm[at] == want[at] {
					at++
				}

				t.Fatalf(
					"both decode, the samples differ: %d bytes against the reference's %d, first difference at byte %d",
					len(ours.pcm),
					len(want),
					at,
				)
			}

			holdToFFmpeg(t, ffmpegBin, path, want, int(ours.bps), ours.channels)
		})
	}
}

var _ frame.Channels
