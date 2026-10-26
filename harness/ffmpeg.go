package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ffmpegStderrCap bounds what is kept of ffmpeg's stderr.
const ffmpegStderrCap = 16384

// toolAbsent is the version of a tool that is not there.
const toolAbsent = "absent"

// errorClip bounds an outside implementation's error text kept in a record.
const errorClip = 200

// Relations of an outside implementation's verdict to libFLAC's.
const (
	relAgree          = "agree"
	relDiffer         = "differ"
	relFFmpegRefuses  = "ffmpeg_refuses"
	relLibFLACRefuses = "libflac_refuses"
	relBothRefuse     = "both_refuse"
	relNoRawDepth     = "no_raw_depth"
)

// ffmpegTool runs ffmpeg, the fourth implementation the decode lane holds
// the files to. FFMPEG names the binary; the default is ffmpeg on PATH, and
// an absent ffmpeg leaves the column empty.
type ffmpegTool struct {
	path    string
	present bool
}

func newFFmpeg() ffmpegTool {
	p := os.Getenv("FFMPEG")
	if p == "" {
		p = "ffmpeg"
	}

	resolved, err := exec.LookPath(p)

	return ffmpegTool{path: resolved, present: err == nil}
}

// version is the first line of ffmpeg -version, "ffmpeg version N ...".
func (f ffmpegTool) version() string {
	if !f.present {
		return toolAbsent
	}

	// #nosec G204 -- the tool named by FFMPEG or found on PATH
	cmd := exec.CommandContext(context.Background(), f.path, "-version")

	out, err := cmd.Output()
	if err != nil {
		return "unavailable: " + err.Error()
	}

	first := strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
	first = strings.TrimPrefix(first, "ffmpeg version ")

	// "9.0.2 Copyright (c) ...": the number alone.
	return strings.Fields(first + " ")[0]
}

// ffmpegRawFormat is ffmpeg's raw PCM format and codec at a depth it writes
// whole bytes for, as flac's raw output does.
func ffmpegRawFormat(bps int) (format, codec string, ok bool) {
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

// decode decodes file to raw PCM through ffmpeg and returns its verdict
// with the PCM's length and hash, comparable to libFLAC's.
func (f ffmpegTool) decode(ctx context.Context, file string, bps int) LibResult {
	format, codec, ok := ffmpegRawFormat(bps)
	if !ok {
		return LibResult{Exit: -1, Error: fmt.Sprintf("no raw output at %d bits", bps)}
	}

	cmd := exec.CommandContext(
		ctx,
		f.path, // #nosec G204 -- the tool named by FFMPEG or found on PATH, on a corpus file
		"-nostdin",
		"-v",
		"error",
		"-i",
		file,
		"-f",
		format,
		"-acodec",
		codec,
		"-",
	)
	errBuf := &capBuffer{max: ffmpegStderrCap}
	cmd.Stderr = errBuf

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return LibResult{Exit: -1, Error: err.Error()}
	}

	if err := cmd.Start(); err != nil {
		return LibResult{Exit: -1, Error: "start ffmpeg: " + err.Error()}
	}

	h := sha256.New()
	n, _ := io.Copy(h, stdout)
	r := libResult(cmd.Wait(), errBuf.String(), file)
	r.Bytes = n
	r.SHA256 = hex.EncodeToString(h.Sum(nil))

	return r
}

// ffmpegRelation is ffmpeg's verdict against libFLAC's on one file.
func ffmpegRelation(ffmpeg, lib LibResult) string {
	switch {
	case ffmpeg.Exit == -1 && strings.HasPrefix(ffmpeg.Error, "no raw output"):
		return relNoRawDepth
	case ffmpeg.OK && lib.OK:
		if ffmpeg.SHA256 == lib.SHA256 {
			return relAgree
		}

		return relDiffer
	case ffmpeg.OK:
		return relLibFLACRefuses
	case lib.OK:
		return relFFmpegRefuses
	default:
		return relBothRefuse
	}
}
