package flac_test

import (
	"errors"
	"io"
	"testing"

	"github.com/mycophonic/flac/frame"
)

// A sample the subframe's depth cannot carry is refused: it was written
// truncated and hashed as written, a stream the reference accepts whose
// content is not the caller's.
func TestWriteFrame_RefusesASampleBeyondTheDepth(t *testing.T) {
	t.Parallel()

	for _, bad := range []int32{70000, -32769, 32768, -2147483648} {
		samples := ramp16()
		samples[3] = bad

		sub := &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: 16, Samples: samples}

		err := writeMono16(t, sub)
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("WriteFrame accepted %d in a 16-bit subframe: %v", bad, err)
		}
	}

	// The extremes themselves are in range.
	samples := ramp16()
	samples[0], samples[1] = -32768, 32767

	sub := &frame.Subframe{SubHeader: frame.SubHeader{Pred: frame.PredVerbatim}, NSamples: 16, Samples: samples}
	if err := writeMono16(t, sub); err != nil {
		t.Fatalf("WriteFrame refused the 16-bit extremes: %v", err)
	}
}
