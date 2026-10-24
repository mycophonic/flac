package flac_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// encodeStereo32 writes one 32-bit stereo frame of the given assignment and
// samples through the encoder, analysis off, and returns the stream.
func encodeStereo32(t *testing.T, channels frame.Channels, left, right []int32) []byte {
	t.Helper()

	n := len(left)
	info := &meta.StreamInfo{
		BlockSizeMin: uint16(n), BlockSizeMax: uint16(n),
		SampleRate: 44100, NChannels: 2, BitsPerSample: 32, NSamples: uint64(n),
	}

	var out bytes.Buffer

	enc, err := flac.NewEncoder(&out, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: uint16(n), SampleRate: 44100,
			Channels: channels, BitsPerSample: 32,
		},
		Subframes: []*frame.Subframe{
			{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: n, Samples: left},
			{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: n, Samples: right},
		},
	}

	if err := enc.WriteFrame(f); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	return out.Bytes()
}

// A 32-bit frame asked for with a decorrelated assignment is written with
// independent channels, since its 33-bit side would wrap: the stream decodes
// to the caller's samples, and the reference accepts it.
func TestWriteFrame_32BitDecorrelatedIsWrittenIndependent(t *testing.T) {
	t.Parallel()

	left := make([]int32, 16)
	right := make([]int32, 16)

	for i := range left {
		left[i], right[i] = 2147483647, -2147483648
		if i%2 == 1 {
			left[i], right[i] = right[i], left[i]
		}
	}

	for _, c := range []struct {
		name     string
		channels frame.Channels
	}{
		{"left/side", frame.ChannelsLeftSide},
		{"side/right", frame.ChannelsSideRight},
		{"mid/side", frame.ChannelsMidSide},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			data := encodeStereo32(t, c.channels, append([]int32{}, left...), append([]int32{}, right...))

			stream, err := flac.New(bytes.NewReader(data))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			f, err := stream.ParseNext()
			if err != nil {
				t.Fatalf("ParseNext: %v", err)
			}

			if f.Channels != frame.ChannelsLR {
				t.Fatalf("the frame was written as %v, want independent channels", f.Channels)
			}

			for i := range left {
				if l, r := f.Subframes[0].Samples[i], f.Subframes[1].Samples[i]; l != left[i] || r != right[i] {
					t.Fatalf("sample %d: (%d, %d), want (%d, %d)", i, l, r, left[i], right[i])
				}
			}

			if _, err := stream.ParseNext(); !errors.Is(err, io.EOF) {
				t.Fatalf("after the one frame: %v, want io.EOF", err)
			}

			if bin, err := exec.LookPath("flac"); err == nil {
				path := filepath.Join(t.TempDir(), "wide.flac")
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatal(err)
				}

				if out, err := exec.CommandContext(t.Context(), bin, "-t", "-s", path).CombinedOutput(); err != nil {
					t.Fatalf("the reference refuses the stream: %v\n%s", err, bytes.TrimSpace(out))
				}
			}
		})
	}
}
