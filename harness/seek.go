package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"math/rand"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SeekRecord is one seek of one decoder: Seek(target), then the window
// [target, target+count) decoded from there against flac -d --skip=target
// --until=target+count. Targets are visited in a shuffled order on one
// stream, so a seek also follows the ones before it.
//
// Verdicts: match, mismatch (samples differ, or the frame found starts after
// the target), error (Seek or the decode after it failed), panic,
// libflac_error; for a target at or past the end, refused or accepted.
type SeekRecord struct {
	Corpus     string `json:"corpus"`
	File       string `json:"file"`
	Decoder    string `json:"decoder"`
	NSamples   uint64 `json:"total_samples"`
	BlockSize  uint16 `json:"block_size_max"`
	Variable   bool   `json:"variable_block_size"`
	Target     uint64 `json:"target"`
	Count      int    `json:"count"`
	FrameStart uint64 `json:"frame_start"`
	Verdict    string `json:"verdict"`
	Error      string `json:"error,omitempty"`
	Panic      string `json:"panic,omitempty"`
	FirstDiff  *Diff  `json:"first_diff,omitempty"`
}

const seekWindow = 1000

func cmdSeek(args []string) error {
	fl := flag.NewFlagSet("seek", flag.ExitOnError)

	var corpora corpusFlags
	fl.Var(&corpora, "corpus", "name=dir: seek in every .flac under dir (repeatable)")
	out := fl.String("out", "-", "output, JSON lines")
	random := fl.Int("random", 4, "seeded random targets per file, beside the fixed ones")
	match := fl.String("match", "", "seek only in files whose names match one of these globs, comma separated")
	_ = fl.Parse(args)

	all, err := listFLAC(corpora)
	if err != nil {
		return err
	}

	var files []inputFile

	for _, f := range all {
		keep := *match == ""
		for _, g := range strings.Split(*match, ",") {
			if ok, _ := filepath.Match(g, filepath.Base(f.rel)); ok && g != "" {
				keep = true
			}
		}

		if keep {
			files = append(files, f)
		}
	}

	w, err := createOut(*out)
	if err != nil {
		return err
	}
	defer w.Close()

	jl := newJSONLines(w)

	lib := newLibFLAC()
	for _, f := range files {
		for _, r := range seekFile(lib, f, *random) {
			if err := jl.write(r); err != nil {
				return err
			}
		}
	}

	return nil
}

// frameStarts decodes the whole stream once (fork first, upstream if the
// fork refuses it) for the frame boundaries the targets are chosen around.
func frameStarts(data []byte) (starts []uint64, total uint64, info StreamInfo, ok bool) {
	for _, dec := range decoderNames {
		starts, total = nil, 0

		func() {
			defer func() { _ = recover() }()

			d, err := openDecoder(dec, bytes.NewReader(data))
			if err != nil {
				return
			}

			info = d.Info()
			for {
				fr, err := d.Next()
				if errors.Is(err, io.EOF) {
					ok = true
					return
				}

				if err != nil {
					return
				}

				starts = append(starts, total)
				total += uint64(fr.BlockSize)
			}
		}()

		if ok && total > 0 {
			return starts, total, info, true
		}

		ok = false
	}

	return nil, 0, info, false
}

func seekTargets(starts []uint64, total uint64, name string, random int) []uint64 {
	set := map[uint64]bool{}
	add := func(t int64) {
		if t >= 0 && uint64(t) < total {
			set[uint64(t)] = true
		}
	}
	n := int64(total)

	add(0)
	add(1)

	if len(starts) > 1 {
		b := int64(starts[1])
		add(b - 1)
		add(b)
		add(b + 1)
	}

	add(n / 2)

	last := int64(starts[len(starts)-1])
	add(last - 1)
	add(last)
	add(last + 1)
	add(n - 2)
	add(n - 1)

	h := fnv.New64a()
	h.Write([]byte(name))

	rng := rand.New(rand.NewSource(int64(h.Sum64())))
	for k := 0; k < random; k++ {
		add(rng.Int63n(n))
	}

	ts := make([]uint64, 0, len(set))
	for t := range set {
		ts = append(ts, t)
	}

	sort.Slice(ts, func(i, j int) bool { return ts[i] < ts[j] })
	rng.Shuffle(len(ts), func(i, j int) { ts[i], ts[j] = ts[j], ts[i] })

	return ts
}

func seekFile(lib libFLAC, f inputFile, random int) []SeekRecord {
	data, err := os.ReadFile(f.path())
	if err != nil {
		return []SeekRecord{{Corpus: f.corpus, File: f.rel, Verdict: "error", Error: err.Error()}}
	}

	starts, total, info, ok := frameStarts(data)
	if !ok {
		return []SeekRecord{
			{Corpus: f.corpus, File: f.rel, Verdict: "skipped", Error: "neither decoder reads the whole stream"},
		}
	}

	targets := seekTargets(starts, total, f.corpus+"/"+f.rel, random)
	width := sampleWidth(int(info.BitsPerSample))
	nch := int(info.NChannels)
	// The reference windows.
	type window struct {
		count int
		pcm   []byte
		err   string
	}

	ref := map[uint64]window{}

	for _, t := range targets {
		count := int(min(uint64(seekWindow), total-t))
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		cmd, wrap := lib.pcmCmd(
			ctx,
			f.path(),
			int(info.BitsPerSample),
			"--skip="+strconv.FormatUint(t, 10),
			"--until="+strconv.FormatUint(t+uint64(count), 10),
		)
		errBuf := &capBuffer{max: 4096}
		cmd.Stderr = errBuf

		var pcm []byte

		stdout, werr := cmd.StdoutPipe()
		if werr == nil {
			if werr = cmd.Start(); werr == nil {
				pcm, _ = io.ReadAll(wrap(stdout))
				werr = cmd.Wait()
			}
		}

		cancel()

		wd := window{count: count, pcm: pcm}
		if lr := libResult(werr, errBuf.String(), f.path()); !lr.OK {
			wd.err = lr.Error
		} else if len(wd.pcm) != count*nch*width {
			wd.err = fmt.Sprintf("libFLAC wrote %d bytes for %d samples", len(wd.pcm), count)
		}

		ref[t] = wd
	}

	var recs []SeekRecord

	base := SeekRecord{
		Corpus: f.corpus, File: f.rel, NSamples: total, BlockSize: info.BlockSizeMax,
		Variable: info.BlockSizeMin != info.BlockSizeMax,
	}

	for _, dec := range decoderNames {
		var s SeekDecoder

		reopen := func() error {
			var err error

			s, err = openSeekDecoder(dec, bytes.NewReader(data))

			return err
		}
		openErr := func() (err error) {
			defer func() {
				if p := recover(); p != nil {
					err = fmt.Errorf("panic: %v", p)
				}
			}()

			return reopen()
		}()

		for _, t := range targets {
			r := base

			r.Decoder, r.Target, r.Count = dec, t, ref[t].count
			if openErr != nil {
				r.Verdict, r.Error = "error", "NewSeek: "+clip(openErr.Error(), 200)
				recs = append(recs, r)

				continue
			}

			if ref[t].err != "" {
				r.Verdict, r.Error = "libflac_error", ref[t].err
				recs = append(recs, r)

				continue
			}

			panicked := seekOne(s, &r, ref[t].pcm, width, nch)
			recs = append(recs, r)

			if panicked {
				// The stream's state is unknown after a panic.
				openErr = func() (err error) {
					defer func() {
						if p := recover(); p != nil {
							err = fmt.Errorf("panic: %v", p)
						}
					}()

					return reopen()
				}()
			}
		}
		// Past the end, on a fresh stream: both must refuse.
		for _, t := range []uint64{total, total + 1000} {
			r := base
			r.Decoder, r.Target = dec, t

			func() {
				defer func() {
					if p := recover(); p != nil {
						r.Verdict, r.Panic = "panic", clip(fmt.Sprint(p), 300)+"\n"+trimStack(debug.Stack())
					}
				}()

				if err := reopen(); err != nil {
					r.Verdict, r.Error = "error", "NewSeek: "+clip(err.Error(), 200)
					return
				}

				pos, err := s.Seek(t)

				r.FrameStart = pos
				if err != nil {
					r.Verdict, r.Error = "refused", clip(err.Error(), 200)
				} else {
					r.Verdict = "accepted"
				}
			}()

			recs = append(recs, r)
		}
	}

	return recs
}

// seekOne seeks s to r.Target and compares the window that follows with the
// reference. It reports whether the decoder panicked.
func seekOne(s SeekDecoder, r *SeekRecord, want []byte, width, nch int) (panicked bool) {
	defer func() {
		if p := recover(); p != nil {
			r.Verdict, r.Panic = "panic", clip(fmt.Sprint(p), 300)+"\n"+trimStack(debug.Stack())
			panicked = true
		}
	}()

	pos, err := s.Seek(r.Target)

	r.FrameStart = pos
	if err != nil {
		r.Verdict, r.Error = "error", "Seek: "+clip(err.Error(), 200)
		return false
	}

	if pos > r.Target {
		r.Verdict, r.Error = "mismatch", fmt.Sprintf("Seek(%d) returned a frame starting at %d", r.Target, pos)
		return false
	}

	need := r.Count * nch * width

	var got, buf []byte

	at := pos

	for uint64(len(got)) < uint64(need) {
		fr, err := s.Next()
		if err != nil {
			r.Verdict, r.Error = "error", fmt.Sprintf(
				"decode after Seek(%d), at sample %d: %s",
				r.Target,
				at,
				clip(err.Error(), 200),
			)

			return false
		}

		if !frameShapeOK(fr, nch) {
			r.Verdict, r.Error = "mismatch", fmt.Sprintf("malformed frame at sample %d after Seek(%d)", at, r.Target)
			return false
		}

		buf = pack(buf, fr, width)

		end := at + uint64(fr.BlockSize)
		if end > r.Target {
			from := uint64(0)
			if r.Target > at {
				from = r.Target - at
			}

			got = append(got, buf[int(from)*nch*width:]...)
		}

		at = end
	}

	got = got[:need]
	if bytes.Equal(got, want) {
		r.Verdict = "match"
		return false
	}

	r.Verdict = "mismatch"

	for i := 0; i+width <= need; i += width {
		if !bytes.Equal(got[i:i+width], want[i:i+width]) {
			w := unpack(want[i : i+width])
			k := i / width
			r.FirstDiff = &Diff{
				Kind: "sample", FirstSample: int64(r.Target), Index: k / nch, Channel: k % nch,
				Got: unpack(got[i : i+width]), Want: &w,
				Note: fmt.Sprintf("sample %d of the window from %d (frame found at %d)", k/nch, r.Target, pos),
			}

			break
		}
	}

	return false
}
