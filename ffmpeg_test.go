package flac_test

import (
	"bytes"
	"errors"
	"fmt"
	"os/exec"
	"testing"
)

var errNoRawDepth = errors.New("ffmpeg writes no raw PCM at this depth")

// ffmpegFormat is ffmpeg's raw PCM format and codec for a depth it writes
// whole bytes for.
func ffmpegFormat(bps int) (format, codec string, ok bool) {
	switch bps {
	case 8:
		return "s8", "pcm_s8", true
	case 16:
		return "s16le", "pcm_s16le", true
	case 24:
		return "s24le", "pcm_s24le", true
	case 32:
		return "s32le", "pcm_s32le", true
	}

	return "", "", false
}

// ffmpegRaw decodes path through ffmpeg to signed little-endian PCM of the
// depth, as flac -d --force-raw-format writes it.
func ffmpegRaw(t *testing.T, ffmpegBin, path string, bps int) ([]byte, error) {
	t.Helper()

	format, codec, ok := ffmpegFormat(bps)
	if !ok {
		return nil, fmt.Errorf("%w: %d bits", errNoRawDepth, bps)
	}

	return exec.CommandContext(t.Context(), ffmpegBin,
		"-nostdin", "-v", "error", "-i", path, "-f", format, "-acodec", codec, "-").Output()
}

// holdToFFmpeg compares want, the samples the reference and this decoder
// agree on, with ffmpeg's decode of path: the fourth implementation. A
// difference fails on mono and stereo; on three channels or more ffmpeg
// reorders channels to its own layouts for some depths, so it is logged.
func holdToFFmpeg(t *testing.T, ffmpegBin, path string, want []byte, bps, channels int) {
	t.Helper()

	if ffmpegBin == "" {
		t.Log("no ffmpeg on the PATH: the fourth implementation is not compared")

		return
	}

	if _, _, ok := ffmpegFormat(bps); !ok {
		return
	}

	got, err := ffmpegRaw(t, ffmpegBin, path, bps)
	if err != nil {
		t.Fatalf("ffmpeg refuses a stream the reference and this decoder read: %v", err)
	}

	if bytes.Equal(got, want) {
		return
	}

	if channels > 2 {
		t.Logf("ffmpeg decodes %d bytes differing from the reference's %d on %d channels (its channel layout)",
			len(got), len(want), channels)

		return
	}

	t.Fatalf("ffmpeg decodes %d bytes differing from the reference's %d", len(got), len(want))
}

// ffmpegOrEmpty is ffmpeg's path, or empty when absent: the comparison is
// then skipped, as the reference tests skip without flac.
func ffmpegOrEmpty() string {
	p, err := exec.LookPath("ffmpeg")
	if err != nil {
		return ""
	}

	return p
}
