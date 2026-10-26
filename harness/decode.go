package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"
)

// Decode verdicts, each decoder against libFLAC:
//
//	match         both accept the stream and every sample is equal
//	mismatch      a sample differs, a sample is outside the depth, a frame
//	              is malformed, or the decoder ends with a different count
//	panic         the decoder panicked
//	timeout       the decoder gave no result in time
//	extra_error   the decoder refuses a stream libFLAC accepts
//	missed_error  the decoder accepts a stream libFLAC refuses
//	md5_only      libFLAC refuses for the STREAMINFO MD5 alone; every sample
//	              is equal (the Go decoders do not check the MD5)
//	both_refuse   both refuse; the samples before the refusals agree
const maxFrames = 1 << 26

// DecodeRecord is one file's line of decode.jsonl.
type DecodeRecord struct {
	Corpus         string      `json:"corpus"`
	File           string      `json:"file"`
	Bytes          int64       `json:"bytes"`
	SHA256         string      `json:"sha256"`
	Info           *StreamInfo `json:"streaminfo,omitempty"`
	LibFLAC        LibResult   `json:"libflac"`
	Fork           DecResult   `json:"fork"`
	Upstream       DecResult   `json:"upstream"`
	ForkVsUpstream string      `json:"fork_vs_upstream"`
}

// DecResult is one decoder's result on one file.
type DecResult struct {
	Verdict      string           `json:"verdict"`
	Error        string           `json:"error,omitempty"`
	EOFNote      string           `json:"eof_note,omitempty"`
	Panic        string           `json:"panic,omitempty"`
	Stack        string           `json:"stack,omitempty"`
	Frames       int64            `json:"frames"`
	SampleFrames int64            `json:"sample_frames"`
	PCMBytes     int64            `json:"pcm_bytes"`
	ParseError   string           `json:"parse_error,omitempty"`   // flac.Parse: every metadata block parsed
	NewSeekError string           `json:"newseek_error,omitempty"` // flac.NewSeek: likewise, through its seek buffer
	PCMSHA256    string           `json:"pcm_sha256,omitempty"`
	MD5Status    string           `json:"md5_status,omitempty"`
	DiffFrames   int64            `json:"diff_frames,omitempty"`
	DiffSamples  int64            `json:"diff_samples,omitempty"`
	OutOfRange   int64            `json:"out_of_range,omitempty"`
	BadShape     int64            `json:"bad_shape,omitempty"`
	BeyondLib    int64            `json:"bytes_after_libflac_ended,omitempty"`
	LibBeyond    int64            `json:"libflac_bytes_after_decoder_ended,omitempty"`
	FirstDiff    *Diff            `json:"first_diff,omitempty"`
	Assignments  map[string]int64 `json:"assignments,omitempty"`
	Seconds      float64          `json:"seconds"`
	info         *StreamInfo
}

// Diff locates the first difference from libFLAC.
type Diff struct {
	Kind        string `json:"kind"` // sample, range, shape or length
	Frame       int64  `json:"frame"`
	FrameNumber uint64 `json:"frame_number"`
	FirstSample int64  `json:"frame_first_sample"`
	Index       int    `json:"index"`
	Channel     int    `json:"channel"`
	Got         int64  `json:"got"`
	Want        *int64 `json:"want,omitempty"`
	Note        string `json:"note,omitempty"`
}

type corpusFlags []struct{ name, dir string }

func (c *corpusFlags) String() string { return fmt.Sprint(*c) }
func (c *corpusFlags) Set(v string) error {
	name, dir, ok := strings.Cut(v, "=")
	if !ok || name == "" || dir == "" {
		return fmt.Errorf("want name=dir, got %q", v)
	}
	*c = append(*c, struct{ name, dir string }{name, dir})
	return nil
}

type inputFile struct {
	corpus, root, rel string
}

func (f inputFile) path() string { return filepath.Join(f.root, filepath.FromSlash(f.rel)) }

// listFLAC lists every .flac file under each corpus, sorted.
func listFLAC(corpora corpusFlags) ([]inputFile, error) {
	var files []inputFile
	for _, c := range corpora {
		var here []inputFile
		err := filepath.WalkDir(c.dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".flac") {
				rel, err := filepath.Rel(c.dir, p)
				if err != nil {
					return err
				}
				here = append(here, inputFile{c.name, c.dir, filepath.ToSlash(rel)})
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		sort.Slice(here, func(i, j int) bool { return here[i].rel < here[j].rel })
		files = append(files, here...)
	}
	return files, nil
}

func cmdDecode(args []string) error {
	fl := flag.NewFlagSet("decode", flag.ExitOnError)
	var corpora corpusFlags
	fl.Var(&corpora, "corpus", "name=dir: decode every .flac under dir, labelled name (repeatable)")
	out := fl.String("out", "-", "output, JSON lines")
	jobs := fl.Int("j", 2, "files decoded at once")
	timeout := fl.Duration("timeout", 2*time.Minute, "deadline per decoder and file, plus one second per MB")
	_ = fl.Parse(args)
	if len(corpora) == 0 {
		return errors.New("decode: no -corpus given")
	}
	files, err := listFLAC(corpora)
	if err != nil {
		return err
	}
	w, err := createOut(*out)
	if err != nil {
		return err
	}
	defer w.Close()
	lib := newLibFLAC()
	results := make([]DecodeRecord, len(files))
	work := make(chan int)
	var wg sync.WaitGroup
	var mu sync.Mutex
	done := 0
	for k := 0; k < max(1, *jobs); k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range work {
				results[i] = decodeOne(lib, files[i], *timeout)
				mu.Lock()
				done++
				if done%50 == 0 || done == len(files) {
					fmt.Fprintf(os.Stderr, "decode: %d/%d files\n", done, len(files))
				}
				mu.Unlock()
			}
		}()
	}
	for i := range files {
		work <- i
	}
	close(work)
	wg.Wait()
	jl := newJSONLines(w)
	for _, r := range results {
		if err := jl.write(r); err != nil {
			return err
		}
	}
	return nil
}

func decodeOne(lib libFLAC, f inputFile, timeout time.Duration) DecodeRecord {
	rec := DecodeRecord{Corpus: f.corpus, File: f.rel}
	p := f.path()
	if fh, err := os.Open(p); err == nil {
		h := sha256.New()
		n, _ := io.Copy(h, fh)
		fh.Close()
		rec.Bytes, rec.SHA256 = n, hex.EncodeToString(h.Sum(nil))
	}
	limit := timeout + time.Duration(rec.Bytes/1e6)*time.Second
	var libs [2]LibResult
	for k, name := range decoderNames {
		r, lr := compareFile(lib, p, name, limit)
		r.ParseError, r.NewSeekError = metadataErrors(name, p)
		libs[k] = lr
		if rec.Info == nil && r.info != nil {
			rec.Info = r.info
		}
		if name == "fork" {
			rec.Fork = r
		} else {
			rec.Upstream = r
		}
	}
	rec.LibFLAC = libs[0]
	if libs[0].OK != libs[1].OK || libs[0].Bytes != libs[1].Bytes {
		rec.LibFLAC.Error += " (libFLAC differed between its two runs)"
	}
	rec.ForkVsUpstream = relation(rec.Fork, rec.Upstream)
	return rec
}

func relation(a, b DecResult) string {
	switch {
	case a.PCMSHA256 == b.PCMSHA256 && (a.Error == "") == (b.Error == "") && (a.Panic == "") == (b.Panic == ""):
		return "same"
	case a.PCMSHA256 == b.PCMSHA256:
		return "same_samples_different_end"
	}
	return "different"
}

// compareFile decodes path with one Go decoder while libFLAC decodes it in
// a subprocess, comparing the two PCM streams as they arrive.
func compareFile(lib libFLAC, path, dec string, limit time.Duration) (DecResult, LibResult) {
	start := time.Now()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cmd, wrap := lib.pcmCmd(ctx, path, peekBitsPerSample(path))
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return DecResult{Verdict: "harness_error", Error: err.Error()}, LibResult{Exit: -1, Error: err.Error()}
	}
	errBuf := &capBuffer{max: 16384}
	cmd.Stderr = errBuf
	if err := cmd.Start(); err != nil {
		return DecResult{
			Verdict: "harness_error",
			Error:   "start flac: " + err.Error(),
		}, LibResult{
			Exit:  -1,
			Error: err.Error(),
		}
	}
	libR := bufio.NewReaderSize(wrap(stdout), 1<<20)
	libHash := sha256.New()
	var libBytes int64
	done := make(chan DecResult, 1)
	go func() { done <- decodeAgainst(path, dec, libR, libHash, &libBytes) }()
	timer := time.NewTimer(limit)
	var r DecResult
	select {
	case r = <-done:
		timer.Stop()
	case <-timer.C:
		// The decoder goroutine is abandoned; flac is killed.
		cancel()
		_ = cmd.Wait()
		return DecResult{
			Verdict: "timeout", Error: fmt.Sprintf("no result after %s", limit),
			Seconds: time.Since(start).Seconds(),
		}, LibResult{Exit: -1, Error: "killed after the decoder's timeout"}
	}
	extra, _ := io.Copy(libHash, libR)
	libBytes += extra
	werr := cmd.Wait()
	lr := libResult(werr, errBuf.String(), path)
	lr.Bytes = libBytes
	lr.SHA256 = hex.EncodeToString(libHash.Sum(nil))
	r.LibBeyond = extra
	r.Seconds = time.Since(start).Seconds()
	classify(&r, lr)
	return r, lr
}

func decodeAgainst(path, dec string, lib io.Reader, libHash hash.Hash, libBytes *int64) (r DecResult) {
	pcmHash := sha256.New()
	defer func() {
		if p := recover(); p != nil {
			r.Panic = clip(fmt.Sprint(p), 500)
			r.Stack = trimStack(debug.Stack())
		}
		r.PCMSHA256 = hex.EncodeToString(pcmHash.Sum(nil))
	}()
	f, err := os.Open(path)
	if err != nil {
		r.Error = err.Error()
		return r
	}
	defer f.Close()
	d, err := openDecoder(dec, bufio.NewReaderSize(f, 1<<20))
	if err != nil {
		r.Error = "open: " + clip(err.Error(), 300)
		return r
	}
	info := d.Info()
	r.info = &info
	md5h := md5.New()
	var buf []byte
	sink := func(got []byte) {
		md5h.Write(got)
		pcmHash.Write(got)
	}
	walkFrames(d, info, &r, sink, func(got []byte) []byte {
		if cap(buf) < len(got) {
			buf = make([]byte, len(got))
		}
		want := buf[:len(got)]
		n, _ := io.ReadFull(lib, want)
		libHash.Write(want[:n])
		*libBytes += int64(n)
		return want[:n]
	})
	if r.Error == "" {
		switch {
		case info.MD5 == strings.Repeat("0", 32):
			r.MD5Status = "absent"
		case hex.EncodeToString(md5h.Sum(nil)) == info.MD5:
			r.MD5Status = "ok"
		default:
			r.MD5Status = "mismatch"
		}
	} else {
		r.MD5Status = "incomplete"
	}
	return r
}

// walkFrames decodes every frame of d, checking its shape and range. sink,
// if set, sees every frame's packed PCM. ref, if set, returns the reference
// bytes for the frame: as many as the reference still has, fewer once it has
// ended, after which it is not called again.
func walkFrames(d Decoder, info StreamInfo, r *DecResult, sink func(got []byte), ref func(got []byte) []byte) {
	bps := int(info.BitsPerSample)
	width := sampleWidth(bps)
	var got []byte
	refDone := false
	for r.Frames < maxFrames {
		fr, err := d.Next()
		if err != nil {
			if errors.Is(err, io.EOF) {
				if err != io.EOF {
					r.EOFNote = clip(err.Error(), 300)
				}
				return
			}
			r.Error = clip(err.Error(), 300)
			return
		}
		if r.Assignments == nil {
			r.Assignments = map[string]int64{}
		}
		r.Assignments[fr.Assignment]++
		at := Diff{Frame: r.Frames, FrameNumber: fr.Num, FirstSample: r.SampleFrames}
		if !frameShapeOK(fr, int(info.NChannels)) {
			r.BadShape++
			if r.FirstDiff == nil {
				lens := make([]int, len(fr.Samples))
				for c, ch := range fr.Samples {
					lens[c] = len(ch)
				}
				d := at
				d.Kind, d.Note = "shape", fmt.Sprintf(
					"block size %d, channel lengths %v, STREAMINFO channels %d",
					fr.BlockSize,
					lens,
					info.NChannels,
				)
				r.FirstDiff = &d
			}
		}
		for c, ch := range fr.Samples {
			for i, v := range ch {
				if !inRange(v, bps) {
					r.OutOfRange++
					if r.FirstDiff == nil {
						d := at
						d.Kind, d.Index, d.Channel, d.Got = "range", i, c, int64(v)
						d.Note = fmt.Sprintf("outside a signed %d-bit sample", bps)
						r.FirstDiff = &d
					}
				}
			}
		}
		got = pack(got, fr, width)
		r.PCMBytes += int64(len(got))
		if sink != nil {
			sink(got)
		}
		if ref == nil {
			// Nothing to compare with.
		} else if refDone {
			r.BeyondLib += int64(len(got))
		} else {
			want := ref(got)
			n := len(want)
			if !bytes.Equal(got[:n], want) {
				r.DiffFrames++
				nch := len(fr.Samples)
				for i := 0; i < fr.BlockSize; i++ {
					for c := 0; c < nch; c++ {
						off := (i*nch + c) * width
						if off+width > n {
							break
						}
						if !bytes.Equal(got[off:off+width], want[off:off+width]) {
							r.DiffSamples++
							if r.FirstDiff == nil {
								d := at
								w := unpack(want[off : off+width])
								d.Kind, d.Index, d.Channel, d.Want = "sample", i, c, &w
								d.Got = unpack(got[off : off+width])
								if c < len(fr.Samples) && i < len(fr.Samples[c]) {
									d.Got = int64(fr.Samples[c][i])
								}
								r.FirstDiff = &d
							}
						}
					}
				}
			}
			if n < len(got) {
				refDone = true
				r.BeyondLib += int64(len(got) - n)
			}
		}
		r.Frames++
		r.SampleFrames += int64(fr.BlockSize)
	}
	r.Error = "harness frame limit reached"
}

func classify(r *DecResult, lib LibResult) {
	goOK := r.Error == "" && r.Panic == ""
	lengthDiff := r.LibBeyond > 0 || r.BeyondLib > 0
	switch {
	case r.Verdict != "":
	case r.Panic != "":
		r.Verdict = "panic"
	case r.DiffSamples > 0 || r.OutOfRange > 0 || r.BadShape > 0:
		r.Verdict = "mismatch"
	case goOK && lib.OK:
		if lengthDiff {
			r.Verdict = "mismatch"
			r.FirstDiff = &Diff{
				Kind: "length", Frame: r.Frames, FirstSample: r.SampleFrames,
				Note: fmt.Sprintf("decoder wrote %d PCM bytes, libFLAC %d", r.PCMBytes, lib.Bytes),
			}
		} else {
			r.Verdict = "match"
		}
	case !goOK && lib.OK:
		r.Verdict = "extra_error"
	case goOK && !lib.OK:
		if lib.MD5Only && !lengthDiff {
			r.Verdict = "md5_only"
		} else {
			r.Verdict = "missed_error"
		}
	default:
		r.Verdict = "both_refuse"
	}
}
