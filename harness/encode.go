//go:build !noencoder

package main

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	fork "github.com/mycophonic/flac"
	fframe "github.com/mycophonic/flac/frame"
	fmeta "github.com/mycophonic/flac/meta"
)

const encoderBuilt = true

// EncodeRecord is one encode of one WAV file by the fork's encoder, judged
// by libFLAC (flac -d: acceptance, samples and MD5), by the fork's own
// decoder, and against the STREAMINFO it should carry.
type EncodeRecord struct {
	WAV          string           `json:"wav"`
	Bits         int              `json:"bits"`
	Assignment   string           `json:"assignment"`
	BlockSize    int              `json:"block_size"`
	Analysis     bool             `json:"analysis"`
	Samples      int              `json:"samples"`
	Frames       int              `json:"frames"`
	Bytes        int              `json:"bytes"`
	Error        string           `json:"error,omitempty"`
	Panic        string           `json:"panic,omitempty"`
	LibFLAC      LibResult        `json:"libflac"`
	LibFLACExact bool             `json:"libflac_exact"`
	LibDiff      *Diff            `json:"libflac_first_diff,omitempty"`
	ForkExact    bool             `json:"fork_exact"`
	ForkError    string           `json:"fork_error,omitempty"`
	Written      map[string]int64 `json:"written_assignments,omitempty"`
	StreamInfo   SICheck          `json:"streaminfo"`
	Problems     []string         `json:"problems,omitempty"`
	Verdict      string           `json:"verdict"`
}

// SICheck compares the STREAMINFO written with what the stream holds.
type SICheck struct {
	MD5OK        bool   `json:"md5_ok"`
	NSamples     uint64 `json:"total_samples"`
	NSamplesOK   bool   `json:"total_samples_ok"`
	BlockMin     uint16 `json:"block_size_min"`
	BlockMax     uint16 `json:"block_size_max"`
	WantBlockMin uint16 `json:"want_block_size_min"`
	WantBlockMax uint16 `json:"want_block_size_max"`
	FrameMin     uint32 `json:"frame_size_min"`
	FrameMax     uint32 `json:"frame_size_max"`
	MeasFrameMin uint32 `json:"measured_frame_size_min"`
	MeasFrameMax uint32 `json:"measured_frame_size_max"`
}

var assignments = map[string]fframe.Channels{
	"independent": fframe.ChannelsLR,
	"left-side":   fframe.ChannelsLeftSide,
	"side-right":  fframe.ChannelsSideRight,
	"mid-side":    fframe.ChannelsMidSide,
}

type wavData struct {
	rate        uint32
	bits        int
	left, right []int32
	pcm         []byte // the samples as FLAC hashes them: little-endian, bits/8 bytes
}

// readWAV reads a stereo PCM WAV of 16, 24 or 32 bits.
func readWAV(path string) (*wavData, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return nil, errors.New("not a RIFF WAVE file")
	}
	var w wavData
	var data []byte
	var channels int
	for p := 12; p+8 <= len(b); {
		id, n := string(b[p:p+4]), int(binary.LittleEndian.Uint32(b[p+4:]))
		body := b[p+8 : min(len(b), p+8+n)]
		switch id {
		case "fmt ":
			if len(body) < 16 {
				return nil, errors.New("short fmt chunk")
			}
			format := binary.LittleEndian.Uint16(body[0:])
			if format != 1 && format != 0xFFFE {
				return nil, fmt.Errorf("WAV format %#x is not PCM", format)
			}
			channels = int(binary.LittleEndian.Uint16(body[2:]))
			w.rate = binary.LittleEndian.Uint32(body[4:])
			w.bits = int(binary.LittleEndian.Uint16(body[14:]))
		case "data":
			data = body
		}
		p += 8 + n + n%2
	}
	if channels != 2 || (w.bits != 16 && w.bits != 24 && w.bits != 32) {
		return nil, fmt.Errorf("want stereo 16, 24 or 32-bit PCM, got %d channels of %d bits", channels, w.bits)
	}
	width := w.bits / 8
	frames := len(data) / (2 * width)
	w.pcm = data[:frames*2*width]
	w.left, w.right = make([]int32, frames), make([]int32, frames)
	for i := 0; i < frames; i++ {
		w.left[i] = int32(unpack(data[(2*i)*width : (2*i+1)*width]))
		w.right[i] = int32(unpack(data[(2*i+1)*width : (2*i+2)*width]))
	}
	return &w, nil
}

// memWriteSeeker is an in-memory io.WriteSeeker, so Encoder.Close can
// rewrite STREAMINFO.
type memWriteSeeker struct {
	b   []byte
	pos int
}

func (m *memWriteSeeker) Write(p []byte) (int, error) {
	if end := m.pos + len(p); end > len(m.b) {
		m.b = append(m.b, make([]byte, end-len(m.b))...)
	}
	copy(m.b[m.pos:], p)
	m.pos += len(p)
	return len(p), nil
}

func (m *memWriteSeeker) Seek(off int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		off += int64(m.pos)
	case io.SeekEnd:
		off += int64(len(m.b))
	}
	if off < 0 {
		return 0, errors.New("negative position")
	}
	m.pos = int(off)
	return off, nil
}

type encodeConfig struct {
	wav        string
	assignment string
	blockSize  int
	analysis   bool
}

func cmdEncode(args []string) error {
	fl := flag.NewFlagSet("encode", flag.ExitOnError)
	var dirs multiFlag
	fl.Var(&dirs, "wav", "directory of stereo WAV files to encode (repeatable)")
	out := fl.String("out", "-", "output, JSON lines")
	blocks := fl.String("block", "4096,1152,192,16", "block sizes")
	assigns := fl.String("assign", "independent,left-side,side-right,mid-side", "channel assignments asked for")
	jobs := fl.Int("j", 2, "encodes at once")
	_ = fl.Parse(args)
	var wavs []string
	for _, d := range dirs {
		m, _ := filepath.Glob(filepath.Join(d, "*.wav"))
		sort.Strings(m)
		wavs = append(wavs, m...)
	}
	var configs []encodeConfig
	for _, w := range wavs {
		for _, a := range strings.Split(*assigns, ",") {
			if _, ok := assignments[a]; !ok {
				return fmt.Errorf("unknown assignment %q", a)
			}
			for _, bs := range strings.Split(*blocks, ",") {
				n, err := strconv.Atoi(bs)
				if err != nil || n < 1 || n > 65535 {
					return fmt.Errorf("bad block size %q", bs)
				}
				for _, an := range []bool{true, false} {
					configs = append(configs, encodeConfig{w, a, n, an})
				}
			}
		}
	}
	w, err := createOut(*out)
	if err != nil {
		return err
	}
	defer w.Close()
	lib := newLibFLAC()
	recs := make([]EncodeRecord, len(configs))
	cache := map[string]*wavData{}
	var mu sync.Mutex
	load := func(p string) (*wavData, error) {
		mu.Lock()
		defer mu.Unlock()
		if d, ok := cache[p]; ok {
			return d, nil
		}
		d, err := readWAV(p)
		if err == nil {
			cache[p] = d
		}
		return d, err
	}
	work := make(chan int)
	var wg sync.WaitGroup
	for k := 0; k < max(1, *jobs); k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				c := configs[i]
				rel := filepath.Base(filepath.Dir(c.wav)) + "/" + filepath.Base(c.wav)
				d, err := load(c.wav)
				if err != nil {
					recs[i] = EncodeRecord{WAV: rel, Verdict: "input_error", Error: err.Error()}
					continue
				}
				recs[i] = encodeOne(lib, d, rel, c)
			}
		}()
	}
	for i := range configs {
		work <- i
	}
	close(work)
	wg.Wait()
	jl := newJSONLines(w)
	for _, r := range recs {
		if err := jl.write(r); err != nil {
			return err
		}
	}
	return nil
}

func encodeOne(lib libFLAC, d *wavData, rel string, c encodeConfig) (r EncodeRecord) {
	r = EncodeRecord{
		WAV: rel, Bits: d.bits, Assignment: c.assignment, BlockSize: c.blockSize, Analysis: c.analysis,
		Samples: len(d.left),
	}
	stream, frames, err := encodeStream(d, c)
	if err != nil {
		if strings.HasPrefix(err.Error(), "panic: ") {
			r.Panic = err.Error()
			r.Verdict = "panic"
		} else {
			r.Error = clip(err.Error(), 300)
			r.Verdict = "encode_error"
		}
		return r
	}
	r.Bytes, r.Frames = len(stream), len(frames)
	// libFLAC: acceptance, samples, MD5.
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	pcm, lr := lib.decodeBytes(ctx, stream)
	cancel()
	r.LibFLAC = lr
	r.LibFLACExact = lr.OK && bytes.Equal(pcm, d.pcm)
	if lr.OK && !r.LibFLACExact {
		r.LibDiff = firstPCMDiff(pcm, d.pcm, d.bits/8, 2)
	}
	if !lr.OK {
		r.Problems = append(r.Problems, "libflac_refuses")
	} else if !r.LibFLACExact {
		r.Problems = append(r.Problems, "libflac_samples_differ")
	}
	// The fork's own decoder.
	r.ForkExact, r.ForkError, r.Written = forkRoundTrip(stream, d)
	switch {
	case r.ForkExact:
	case strings.HasPrefix(r.ForkError, "open: "):
		r.Problems = append(r.Problems, "fork_decoder_refuses_stream")
	default:
		r.Problems = append(r.Problems, "fork_decode_differs")
	}
	// STREAMINFO against the stream.
	r.StreamInfo = checkStreamInfo(stream, d, frames)
	si := r.StreamInfo
	if !si.MD5OK {
		r.Problems = append(r.Problems, "streaminfo_md5")
	}
	if !si.NSamplesOK {
		r.Problems = append(r.Problems, "streaminfo_total_samples")
	}
	// RFC 9639 section 8.2: the minimum block size leaves out the last block,
	// and both sizes are 16 to 65535. A one-block stream has no block but the
	// last: its maximum holds the block, as libFLAC's nominal size does.
	if len(frames) > 1 && (si.BlockMin != si.WantBlockMin || si.BlockMax != si.WantBlockMax) ||
		len(frames) == 1 && (si.BlockMax < si.WantBlockMax || si.BlockMin > si.BlockMax) {
		r.Problems = append(r.Problems, "streaminfo_block_size")
	}
	if si.BlockMin < 16 || si.BlockMax < 16 {
		r.Problems = append(r.Problems, "streaminfo_block_size_below_16")
	}
	switch {
	case si.FrameMin == 0 && si.FrameMax == 0:
		r.Problems = append(r.Problems, "streaminfo_frame_size_unknown")
	case si.FrameMin != si.MeasFrameMin || si.FrameMax != si.MeasFrameMax:
		r.Problems = append(r.Problems, "streaminfo_frame_size_wrong")
	}
	r.Verdict = "ok"
	if len(r.Problems) > 0 {
		r.Verdict = "fail"
	}
	return r
}

// encodeStream encodes the WAV's samples in blocks of c.blockSize, the last
// block shorter, and returns the stream and each frame as written.
func encodeStream(d *wavData, c encodeConfig) (stream []byte, frames []frameStat, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v\n%s", p, trimStack(debug.Stack()))
		}
	}()
	ws := &memWriteSeeker{}
	bs := uint16(min(c.blockSize, max(1, len(d.left))))
	info := &fmeta.StreamInfo{
		BlockSizeMin: bs, BlockSizeMax: bs, SampleRate: d.rate, NChannels: 2,
		BitsPerSample: uint8(d.bits),
	}
	enc, err := fork.NewEncoder(ws, info)
	if err != nil {
		return nil, nil, fmt.Errorf("NewEncoder: %w", err)
	}
	enc.EnablePredictionAnalysis(c.analysis)
	for off := 0; off < len(d.left); off += c.blockSize {
		k := min(c.blockSize, len(d.left)-off)
		// WriteFrame decorrelates the samples in place and correlates them
		// back; hand it copies so the input stays what the WAV holds.
		l := append([]int32(nil), d.left[off:off+k]...)
		rr := append([]int32(nil), d.right[off:off+k]...)
		f := &fframe.Frame{
			Header: fframe.Header{
				HasFixedBlockSize: true, BlockSize: uint16(k), SampleRate: d.rate,
				Channels: assignments[c.assignment], BitsPerSample: uint8(d.bits),
			},
			Subframes: []*fframe.Subframe{
				{SubHeader: fframe.SubHeader{Pred: fframe.PredVerbatim}, Samples: l, NSamples: k},
				{SubHeader: fframe.SubHeader{Pred: fframe.PredVerbatim}, Samples: rr, NSamples: k},
			},
		}
		before := ws.pos
		if err := enc.WriteFrame(f); err != nil {
			return nil, nil, fmt.Errorf("WriteFrame at sample %d: %w", off, err)
		}
		frames = append(frames, frameStat{block: k, bytes: ws.pos - before})
	}
	if err := enc.Close(); err != nil {
		return nil, nil, fmt.Errorf("Close: %w", err)
	}
	return ws.b, frames, nil
}

func forkRoundTrip(stream []byte, d *wavData) (exact bool, errText string, written map[string]int64) {
	var r DecResult
	func() {
		defer func() {
			if p := recover(); p != nil {
				r.Panic = fmt.Sprint(p)
			}
		}()
		dec, err := openDecoder("fork", bytes.NewReader(stream))
		if err != nil {
			r.Error = "open: " + err.Error()
			return
		}
		info := dec.Info()
		pos := 0
		walkFrames(dec, info, &r, nil, func(got []byte) []byte {
			n := min(len(got), len(d.pcm)-pos)
			want := d.pcm[pos : pos+n]
			pos += n
			return want
		})
		if r.Error == "" && pos != len(d.pcm) {
			r.Error = fmt.Sprintf("decoded %d of %d PCM bytes", pos, len(d.pcm))
		}
	}()
	switch {
	case r.Panic != "":
		errText = "panic: " + r.Panic
	case r.Error != "":
		errText = r.Error
	}
	exact = errText == "" && r.DiffSamples == 0 && r.OutOfRange == 0 && r.BadShape == 0 && r.BeyondLib == 0
	return exact, errText, r.Assignments
}

func checkStreamInfo(stream []byte, d *wavData, frames []frameStat) SICheck {
	var c SICheck
	if len(stream) < 42 || string(stream[:4]) != "fLaC" || stream[4]&0x7F != 0 {
		return c
	}
	b := stream[8:42]
	c.BlockMin = binary.BigEndian.Uint16(b[0:])
	c.BlockMax = binary.BigEndian.Uint16(b[2:])
	c.FrameMin = uint32(b[4])<<16 | uint32(b[5])<<8 | uint32(b[6])
	c.FrameMax = uint32(b[7])<<16 | uint32(b[8])<<8 | uint32(b[9])
	c.NSamples = binary.BigEndian.Uint64(b[10:]) & (1<<36 - 1)
	sum := md5.Sum(d.pcm)
	c.MD5OK = hex.EncodeToString(b[18:34]) == hex.EncodeToString(sum[:])
	c.NSamplesOK = c.NSamples == uint64(len(d.left))
	// RFC 9639 section 8.2: the minimum block size leaves out the last block;
	// a one-block stream has min = max.
	if len(frames) > 0 {
		c.WantBlockMin, c.WantBlockMax = ^uint16(0), 0
		for i, f := range frames {
			c.WantBlockMax = max(c.WantBlockMax, uint16(f.block))
			if i < len(frames)-1 || len(frames) == 1 {
				c.WantBlockMin = min(c.WantBlockMin, uint16(f.block))
			}
		}
		c.MeasFrameMin, c.MeasFrameMax = ^uint32(0), 0
		for _, f := range frames {
			c.MeasFrameMin = min(c.MeasFrameMin, uint32(f.bytes))
			c.MeasFrameMax = max(c.MeasFrameMax, uint32(f.bytes))
		}
	}
	return c
}

// frameStat is one frame as the encoder wrote it.
type frameStat struct{ block, bytes int }

type multiFlag []string

func (m *multiFlag) String() string     { return strings.Join(*m, ",") }
func (m *multiFlag) Set(v string) error { *m = append(*m, v); return nil }
