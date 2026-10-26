package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// libFLAC runs the reference flac tool. FLAC names the binary; the default
// is flac on PATH.
type libFLAC struct {
	path string
}

func newLibFLAC() libFLAC {
	if p := os.Getenv("FLAC"); p != "" {
		return libFLAC{path: p}
	}

	return libFLAC{path: "flac"}
}

// version is the first line of flac --version.
func (l libFLAC) version() string {
	out, err := exec.Command(l.path, "--version").Output()
	if err != nil {
		return "unavailable: " + err.Error()
	}

	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0])
}

var rawArgs = []string{"-d", "-c", "-s", "--force-raw-format", "--endian=little", "--sign=signed"}

// decodeCmd decodes file to raw PCM on stdout; extra goes before the file.
func (l libFLAC) decodeCmd(ctx context.Context, file string, extra ...string) *exec.Cmd {
	args := append(append(append([]string{}, rawArgs...), extra...), "--", file)
	return exec.CommandContext(ctx, l.path, args...)
}

// decodeBytes decodes a stream held in memory, read from stdin.
func (l libFLAC) decodeBytes(ctx context.Context, data []byte, extra ...string) (pcm []byte, res LibResult) {
	args := append(append(append([]string{}, rawArgs...), extra...), "-")
	cmd := exec.CommandContext(ctx, l.path, args...)
	cmd.Stdin = bytes.NewReader(data)

	var out bytes.Buffer

	errBuf := &capBuffer{max: 8192}
	cmd.Stdout, cmd.Stderr = &out, errBuf
	err := cmd.Run()
	res = libResult(err, errBuf.String(), "-")
	res.Bytes = int64(out.Len())

	return out.Bytes(), res
}

// LibResult is libFLAC's verdict on one decode.
type LibResult struct {
	OK      bool   `json:"ok"`
	Exit    int    `json:"exit"`
	Error   string `json:"error,omitempty"`
	MD5Only bool   `json:"md5_only,omitempty"` // refused for an MD5 mismatch alone
	Bytes   int64  `json:"pcm_bytes"`
	SHA256  string `json:"pcm_sha256,omitempty"`
}

// libResult reads a finished command: its exit status and its first
// error line, with the file's directory taken out of the text.
func libResult(err error, stderr, file string) LibResult {
	r := LibResult{OK: err == nil}
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			r.Exit = ee.ExitCode()
		} else {
			r.Exit = -1
			r.Error = err.Error()
		}
	}

	text := stderr

	if file != "-" {
		dir := filepath.Dir(file) + string(filepath.Separator)
		text = strings.ReplaceAll(text, dir, "")
	}

	if strings.Contains(text, "MD5 signature mismatch") {
		r.MD5Only = !r.OK
	}

	if !r.OK && r.Error == "" {
		r.Error = firstError(text)
	}

	return r
}

// firstError picks the most telling line of flac's stderr.
func firstError(text string) string {
	var first string

	for _, l := range strings.Split(text, "\n") {
		l = strings.TrimSpace(l)
		if l == "" {
			continue
		}

		if first == "" {
			first = l
		}

		if strings.Contains(l, "ERROR") || strings.Contains(l, "error code") || strings.Contains(l, "MD5") {
			return clip(l, 300)
		}
	}

	return clip(first, 300)
}

func clip(s string, n int) string {
	if len(s) > n {
		return s[:n] + "..."
	}

	return s
}

// rawDepth reports whether flac's raw output takes a depth: it refuses any
// other ("bits per sample is 12, must be 8/16/24/32 for raw format output").
func rawDepth(bps int) bool {
	switch bps {
	case 8, 16, 24, 32:
		return true
	}

	return false
}

// pcmCmd decodes file to PCM on stdout: raw where flac's raw output takes
// the depth, AIFF otherwise. Wrapping the command's stdout with wrap gives
// the raw bytes either way.
func (l libFLAC) pcmCmd(
	ctx context.Context,
	file string,
	bps int,
	extra ...string,
) (cmd *exec.Cmd, wrap func(io.Reader) io.Reader) {
	if bps == 0 || rawDepth(bps) {
		return l.decodeCmd(ctx, file, extra...), func(r io.Reader) io.Reader { return r }
	}

	args := append(append([]string{"-d", "-c", "-s", "--force-aiff-format"}, extra...), "--", file)

	return exec.CommandContext(ctx, l.path, args...), func(r io.Reader) io.Reader { return newAIFFPCM(r, bps) }
}

// aiffPCM turns flac's AIFF output into its raw output. AIFF holds each
// sample big-endian and, at a depth that is not whole bytes, shifted left to
// fill them (flac's decode.c shifts by 8 - bps%8 for every format but raw);
// raw holds it little-endian and unshifted.
type aiffPCM struct {
	r       *bufio.Reader
	width   int
	shift   uint
	started bool
	err     error
}

func newAIFFPCM(r io.Reader, bps int) *aiffPCM {
	w := sampleWidth(bps)
	return &aiffPCM{r: bufio.NewReaderSize(r, 1<<16), width: w, shift: uint(8*w - bps)}
}

func (a *aiffPCM) header() error {
	var h [12]byte
	if _, err := io.ReadFull(a.r, h[:]); err != nil {
		return err
	}

	if string(h[0:4]) != "FORM" || string(h[8:12]) != "AIFF" {
		return errors.New("flac's AIFF output has no FORM/AIFF header")
	}

	for {
		var c [8]byte
		if _, err := io.ReadFull(a.r, c[:]); err != nil {
			return err
		}

		n := int64(binary.BigEndian.Uint32(c[4:]))
		if string(c[0:4]) == "SSND" {
			var o [8]byte
			if _, err := io.ReadFull(a.r, o[:]); err != nil {
				return err
			}

			_, err := io.CopyN(io.Discard, a.r, int64(binary.BigEndian.Uint32(o[0:])))

			return err
		}

		if _, err := io.CopyN(io.Discard, a.r, n+n%2); err != nil {
			return err
		}
	}
}

func (a *aiffPCM) Read(p []byte) (int, error) {
	if a.err != nil {
		return 0, a.err
	}

	if !a.started {
		a.started = true
		if err := a.header(); err != nil {
			a.err = err
			if errors.Is(err, io.ErrUnexpectedEOF) {
				a.err = io.EOF
			}

			return 0, a.err
		}
	}

	k := len(p) / a.width * a.width
	if k == 0 {
		return 0, io.ErrShortBuffer
	}

	n, err := io.ReadFull(a.r, p[:k])
	n = n / a.width * a.width

	bits := 8 * uint(a.width)
	for i := 0; i < n; i += a.width {
		var u uint64
		for j := 0; j < a.width; j++ {
			u = u<<8 | uint64(p[i+j])
		}

		v := int64(u<<(64-bits)) >> (64 - bits) >> a.shift
		for j := 0; j < a.width; j++ {
			p[i+j] = byte(v >> (8 * uint(j)))
		}
	}

	switch {
	case err == nil:
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		a.err = io.EOF
		if n > 0 {
			err = nil
		} else {
			err = io.EOF
		}
	default:
		a.err = err
	}

	return n, err
}

// peekBitsPerSample reads a FLAC file's depth from its STREAMINFO, past an
// ID3v2 tag; 0 if it cannot.
func peekBitsPerSample(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()

	var b [64]byte

	n, _ := io.ReadFull(f, b[:])

	p := b[:n]
	if len(p) >= 10 && string(p[:3]) == "ID3" {
		size := int64(p[6])<<21 | int64(p[7])<<14 | int64(p[8])<<7 | int64(p[9])
		if _, err := f.Seek(10+size, io.SeekStart); err != nil {
			return 0
		}

		n, _ = io.ReadFull(f, b[:])
		p = b[:n]
	}

	if len(p) < 4+4+34 || string(p[:4]) != "fLaC" || p[4]&0x7F != 0 {
		return 0
	}

	si := p[8:]

	return int(si[12]&1)<<4 | int(si[13]>>4) + 1
}
