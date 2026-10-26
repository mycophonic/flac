package flac_test

import (
	"errors"
	"io"
	"math/rand/v2"
	"os"
	"testing"

	"github.com/mycophonic/flac"
)

// seekBenchFile is the largest IETF subset file, 87 MB of 16-bit stereo.
const seekBenchFile = "testdata/flac-test-files/subset/55 - file 48-53 combined.flac"

func openForSeek(b *testing.B) *flac.Stream {
	b.Helper()

	file, err := os.Open(seekBenchFile)
	if err != nil {
		b.Skipf("no IETF test files: %v", err)
	}

	stream, err := flac.NewSeek(file)
	if err != nil {
		b.Fatal(err)
	}

	b.Cleanup(func() { _ = stream.Close() })

	return stream
}

// NewSeek alone: the metadata parse, which on this file is a 16 MB seek
// table of 932,067 points read a field at a time through the buffer.
func BenchmarkNewSeek_Open(b *testing.B) {
	for range b.N {
		file, err := os.Open(seekBenchFile)
		if err != nil {
			b.Skipf("no IETF test files: %v", err)
		}

		stream, err := flac.NewSeek(file)
		if err != nil {
			b.Fatal(err)
		}

		_ = stream.Close()
	}
}

// A sequential decode of every frame, the stream opened outside the timed
// loop: frame throughput, read in 4 KiB fills.
func BenchmarkNewSeek_Decode(b *testing.B) {
	for range b.N {
		b.StopTimer()

		stream := openForSeek(b)

		b.StartTimer()

		for {
			_, err := stream.ParseNext()
			if errors.Is(err, io.EOF) {
				break
			}

			if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// Random seeks across the file, one frame parsed after each.
func BenchmarkNewSeek_RandomSeek(b *testing.B) {
	stream := openForSeek(b)
	rng := rand.New(rand.NewPCG(1, 2))
	nsamples := stream.Info.NSamples

	b.ResetTimer()

	for range b.N {
		if _, err := stream.Seek(rng.Uint64N(nsamples)); err != nil {
			b.Fatal(err)
		}

		if _, err := stream.ParseNext(); err != nil {
			b.Fatal(err)
		}
	}
}

// Short forward seeks, two blocks at a time with one frame parsed after each:
// the pattern of a scrub or a lookahead.
func BenchmarkNewSeek_ShortSeek(b *testing.B) {
	stream := openForSeek(b)
	step := uint64(stream.Info.BlockSizeMax) * 2
	nsamples := stream.Info.NSamples
	pos := uint64(0)

	b.ResetTimer()

	for range b.N {
		pos += step
		if pos >= nsamples {
			pos = 0
		}

		if _, err := stream.Seek(pos); err != nil {
			b.Fatal(err)
		}

		if _, err := stream.ParseNext(); err != nil {
			b.Fatal(err)
		}
	}
}
