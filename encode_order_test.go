package flac_test

import (
	"io"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
	"github.com/mycophonic/flac/meta"
)

// WriteFrame refuses a subframe whose predictor order exceeds the block size
// with an error; it indexed past the block and panicked.
func TestWriteFrame_RefusesOrderAboveBlockSize(t *testing.T) {
	t.Parallel()

	info := &meta.StreamInfo{
		BlockSizeMin: 2, BlockSizeMax: 2,
		SampleRate: 44100, NChannels: 1, BitsPerSample: 16, NSamples: 2,
	}

	enc, err := flac.NewEncoder(io.Discard, info)
	if err != nil {
		t.Fatalf("NewEncoder: %v", err)
	}

	enc.EnablePredictionAnalysis(false)

	f := &frame.Frame{
		Header: frame.Header{
			HasFixedBlockSize: true, BlockSize: 2, SampleRate: 44100,
			Channels: frame.ChannelsMono, BitsPerSample: 16,
		},
		Subframes: []*frame.Subframe{{
			SubHeader: frame.SubHeader{Pred: frame.PredFixed, Order: 4},
			NSamples:  2,
			Samples:   []int32{1, 2},
		}},
	}

	if err := enc.WriteFrame(f); err == nil {
		t.Fatal("WriteFrame accepted a fixed order of 4 on a 2-sample block")
	}

	// The boundary: an order equal to the block size is refused too, as the
	// reference decoder refuses the frame it would write.
	f.Subframes[0].Order = 2

	if err := enc.WriteFrame(f); err == nil {
		t.Fatal("WriteFrame accepted a fixed order of 2 on a 2-sample block")
	}
}
