package flac_test

// The MD5 as the oracle for every sample. Each IETF subset file carries
// the MD5 of its unencoded samples; decoding it to the end and hashing the
// frames with Frame.Hash must give that signature. A wrong sample anywhere,
// at any depth from 8 to 32 bits and any channel count, shows here.

import (
	"bytes"
	"crypto/md5"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
)

func TestDecodedSamplesMatchTheStreamMD5(t *testing.T) {
	t.Parallel()

	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	if len(files) == 0 {
		t.Skip("no IETF test files under testdata/flac-test-files")
	}

	var zero [16]byte

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			stream, err := flac.ParseFile(path)
			if err != nil {
				t.Fatal(err)
			}

			defer func() { _ = stream.Close() }()

			if stream.Info.MD5sum == zero {
				t.Skip("no MD5 in StreamInfo")
			}

			sum := md5.New()

			for {
				f, err := stream.ParseNext()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatal(err)
				}

				f.Hash(sum)
			}

			if got := sum.Sum(nil); !bytes.Equal(got, stream.Info.MD5sum[:]) {
				t.Fatalf("the decoded samples hash to %x, StreamInfo says %x", got, stream.Info.MD5sum)
			}
		})
	}
}
