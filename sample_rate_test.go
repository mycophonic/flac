package flac_test

import (
	"bytes"
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// A frame whose sample rate differs from STREAMINFO's is refused by Next and
// ParseNext, as a differing channel count or bit depth is: the reference
// decoder refuses the file (IETF uncommon/01), and a caller sized by
// STREAMINFO would play it at the wrong rate. The frame is this encoder's,
// which writes a frame's own rate code.
func TestSampleRateIsHeldToStreamInfo(t *testing.T) {
	t.Parallel()

	info := &meta.StreamInfo{
		BlockSizeMin: 16, BlockSizeMax: 16,
		SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 16,
	}

	var out bytes.Buffer

	enc, err := flac.NewEncoder(&out, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 16, SampleRate: 32000,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredVerbatim},
			NSamples:  16,
			Samples:   make([]int32, 16),
		}},
	}

	if err := enc.WriteFrame(f); err != nil {
		t.Fatalf("WriteFrame: %v", err)
	}

	if err := enc.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	for _, c := range []struct {
		name string
		next func(*flac.Stream) error
	}{
		{"ParseNext", func(s *flac.Stream) error { _, err := s.ParseNext(); return err }},
		{"Next", func(s *flac.Stream) error { _, err := s.Next(); return err }},
	} {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			stream, err := flac.New(bytes.NewReader(out.Bytes()))
			if err != nil {
				t.Fatalf("New: %v", err)
			}

			err = c.next(stream)
			if err == nil || errors.Is(err, io.EOF) {
				t.Fatalf("a 32 kHz frame under a 44.1 kHz STREAMINFO: %v, want a refusal", err)
			}
		})
	}
}
