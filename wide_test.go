package flac_test

import (
	"bytes"
	"crypto/md5"
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

// A 32-bit stereo stream with inter-channel decorrelation carries a 33-bit
// side channel. Issue 10: it decoded through int32 and came back wrong with no
// error. The mid/side files are the reference encoder's (flac 1.5.0), their
// side beyond 32 bits in every sample but fixed-midside's two middle frames:
// one per subframe type on the side channel, one with Rice partitions of
// order 2, one with wasted bits on both channels. Each file's STREAMINFO MD5
// is the oracle.
//
// The left/side and side/right files are this encoder's, whose side is
// computed in int32 and so wraps (issue 11): the file holds that wrapped side
// sign-extended to 33 bits, within int32 in every sample, and the reference
// decoder recovers the source from it because those two assignments
// correlate by one subtraction or addition, correct modulo 2^32 for any
// representation of the side. They cover correlateWide's left/side and
// side/right lines; they are not a side beyond 32 bits.
func TestDecode32BitSide(t *testing.T) {
	t.Parallel()

	files := []struct {
		path     string
		channels frame.Channels
	}{
		// The issue's file: a constant (2147483647, -2147483648).
		{"testdata/wide/constant-midside.flac", frame.ChannelsMidSide},
		{"testdata/wide/verbatim-midside.flac", frame.ChannelsMidSide},
		{"testdata/wide/fixed-midside.flac", frame.ChannelsMidSide},
		{"testdata/wide/lpc-midside.flac", frame.ChannelsMidSide},
		{"testdata/wide/lpc-partitions-midside.flac", frame.ChannelsMidSide}, // flac -r 2,8: partition order 2
		{"testdata/wide/wasted-midside.flac", frame.ChannelsMidSide},         // low bits zero: 7 and 8 wasted bits
		{"testdata/wide/verbatim-leftside.flac", frame.ChannelsLeftSide},
		{"testdata/wide/verbatim-sideright.flac", frame.ChannelsSideRight},
	}

	for _, file := range files {
		t.Run(file.path, func(t *testing.T) {
			t.Parallel()

			stream, err := flac.ParseFile(file.path)
			if err != nil {
				t.Fatalf("ParseFile: %v", err)
			}

			defer func() { _ = stream.Close() }()

			if stream.Info.BitsPerSample != 32 || stream.Info.NChannels != 2 {
				t.Fatalf("not a 32-bit stereo stream: %d bits, %d channels",
					stream.Info.BitsPerSample, stream.Info.NChannels)
			}

			hash := md5.New()
			frames := 0

			for {
				f, err := stream.ParseNext()
				if errors.Is(err, io.EOF) {
					break
				}

				if err != nil {
					t.Fatalf("ParseNext: %v", err)
				}

				if f.Channels != file.channels {
					t.Fatalf("frame %d: channel assignment %v, want %v", frames, f.Channels, file.channels)
				}

				f.Hash(hash)

				frames++
			}

			if frames == 0 {
				t.Fatal("no frames")
			}

			if got := hash.Sum(nil); !bytes.Equal(got, stream.Info.MD5sum[:]) {
				t.Fatalf("MD5 mismatch over %d frames: got %x, want %x", frames, got, stream.Info.MD5sum)
			}
		})
	}
}

// The issue's file, sample by sample: every frame is the constant pair at the
// extremes of int32, where the side is 2^32-1.
func TestDecode32BitSide_Samples(t *testing.T) {
	t.Parallel()

	stream, err := flac.ParseFile("testdata/wide/constant-midside.flac")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}

	defer func() { _ = stream.Close() }()

	f, err := stream.ParseNext()
	if err != nil {
		t.Fatalf("ParseNext: %v", err)
	}

	const (
		left  = 2147483647
		right = -2147483648
	)

	for i := range f.Subframes[0].Samples {
		if l, r := f.Subframes[0].Samples[i], f.Subframes[1].Samples[i]; l != left || r != right {
			t.Fatalf("sample %d: (%d, %d), want (%d, %d)", i, l, r, left, right)
		}
	}
}
