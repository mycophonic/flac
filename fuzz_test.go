package flac_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
)

// FuzzStream feeds arbitrary bytes to New and ParseNext. A stream may be
// refused at any point; it may never panic or hang, and every frame it
// does yield is whole: one subframe per channel, each holding exactly the
// frame's block size of samples. The seeds are the small streams under
// testdata.
func FuzzStream(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.flac"))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || len(data) > 64<<10 {
			continue
		}

		f.Add(data)
	}

	f.Add([]byte("fLaC"))
	f.Add([]byte("ID3"))

	f.Fuzz(func(t *testing.T, data []byte) {
		stream, err := flac.New(bytes.NewReader(data))
		if err != nil {
			return
		}

		defer func() { _ = stream.Close() }()

		// A stream of many tiny frames is legal; bound the work per input.
		for range 4096 {
			frame, err := stream.ParseNext()
			if err != nil {
				// io.EOF or a refusal: either ends the stream.
				return
			}

			if got, want := len(frame.Subframes), frame.Channels.Count(); got != want {
				t.Fatalf("%d subframes for %d channels", got, want)
			}

			for i, sub := range frame.Subframes {
				if sub.NSamples != int(frame.BlockSize) || len(sub.Samples) != int(frame.BlockSize) {
					t.Fatalf(
						"subframe %d: %d samples (%d held) for a block of %d",
						i,
						sub.NSamples,
						len(sub.Samples),
						frame.BlockSize,
					)
				}
			}
		}
	})
}
