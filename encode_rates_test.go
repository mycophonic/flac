package flac_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/frame"
)

// The sample rate is a header field the decoder never computes with, but
// the frame header codes it four ways besides deferring to STREAMINFO: a
// table of eleven common rates, 8 bits of kHz, 16 bits of Hz, 16 bits of
// tens of Hz (RFC 9639, section 9.1.4). The rates below cover each path: a
// 16-bit stereo stream at each is accepted by flac -t, decodes to the input
// through flac -d, ffmpeg and this decoder, and carries the rate in
// STREAMINFO.
func TestEncoderSampleRatesAgainstTheReference(t *testing.T) {
	t.Parallel()

	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	ffmpegBin := ffmpegOrEmpty()

	rates := []int{
		8000, 16000, 22050, 24000, 32000, 44100, 48000, 88200, 96000, 176400, 192000, // the table
		37000,        // 8 bits of kHz
		11025, 44101, // 16 bits of Hz
		655350, // 16 bits of tens of Hz
	}

	for _, rate := range rates {
		t.Run(strconv.Itoa(rate), func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "rate.flac")
			c := encodeMatrixCase{n: 4097, bps: 16, channels: 2, blockSize: 1152, assignment: frame.ChannelsLR}
			blocks, _ := writeMatrixStreamAtRate(t, path, c, rate)
			want := interleaved(blocks, c.bps)

			if out, refErr := exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).
				CombinedOutput(); refErr != nil {
				t.Fatalf("flac -t refuses the stream: %v\n%s", refErr, out)
			}

			got, refErr := referenceRaw(t, flacBin, path)
			if refErr != nil {
				t.Fatalf("flac -d: %v", refErr)
			}

			if !bytes.Equal(got, want) {
				t.Fatalf("flac -d decodes %d bytes differing from the %d written", len(got), len(want))
			}

			holdToFFmpeg(t, ffmpegBin, path, want, c.bps, c.channels)

			ours := decodeWhole(path)
			if ours.err != nil || !bytes.Equal(ours.pcm, want) {
				t.Fatalf("this decoder: %v, %d bytes against the %d written", ours.err, len(ours.pcm), len(want))
			}

			again, openErr := flac.Open(path)
			if openErr != nil {
				t.Fatalf("Open: %v", openErr)
			}

			defer func() { _ = again.Close() }()

			if again.Info.SampleRate != uint32(rate) {
				t.Fatalf("STREAMINFO rate %d, want %d", again.Info.SampleRate, rate)
			}
		})
	}
}
