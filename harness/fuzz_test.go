package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	fork "github.com/mycophonic/flac"
	fmeta "github.com/mycophonic/flac/meta"
)

// The fuzz targets fail on a panic in the fork, an input that takes more
// than five seconds, or an allocation over the bound. A sample divergence
// from upstream on a frame both decoders accept is not a failure (either
// may be the wrong one); with FLACHARNESS_OBS set, the input is kept there
// for `flacharness triage`, which asks libFLAC.
//
// Seeds: the FLAC files under the directories in FLACHARNESS_SEEDS,
// separated as PATH is. run.sh points it at the generated corpora.
//
// FLACHARNESS_ALLOW_PANIC names functions, comma separated, whose panics are
// known: a panic raised in one of them is kept under FLACHARNESS_OBS/allowed
// and the run goes on, so a commit that still has a known panic is fuzzed
// past it. The match is on the function that panicked, not its callers.

const slowInput = 5 * time.Second

func streamAllocBound(n int) uint64 { return 16<<20 + 64*uint64(n) }

func seedFiles(tb testing.TB) []string {
	var files []string

	for _, dir := range filepath.SplitList(os.Getenv("FLACHARNESS_SEEDS")) {
		if dir == "" {
			continue
		}

		_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil && d.Type().IsRegular() && strings.EqualFold(filepath.Ext(p), ".flac") {
				files = append(files, p)
			}

			return nil
		})
	}

	if len(files) == 0 {
		tb.Logf("no seed files: set FLACHARNESS_SEEDS")
	}

	return files
}

func readSeed(p string, limit int) []byte {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}

	if len(b) > limit {
		b = b[:limit]
	}

	return b
}

// metadataPrefix is a stream's signature and metadata blocks, up to the
// first frame.
func metadataPrefix(b []byte) []byte {
	if len(b) < 4 || string(b[:4]) != "fLaC" {
		return nil
	}

	p := 4
	for p+4 <= len(b) {
		last := b[p]&0x80 != 0
		n := int(b[p+1])<<16 | int(b[p+2])<<8 | int(b[p+3])
		p = min(len(b), p+4+n)

		if last {
			break
		}
	}

	return b[:p]
}

func FuzzStream(f *testing.F) {
	for _, p := range seedFiles(f) {
		if b := readSeed(p, 64<<10); b != nil {
			f.Add(b)
		}
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}

		checkDecode(t, "FuzzStream", b, b)
	})
}

func FuzzFrame(f *testing.F) {
	for i := 0; i < 64; i++ {
		f.Add(seedBytes(i, 64+i*16))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 64<<10 {
			t.Skip()
		}

		checkDecode(t, "FuzzFrame", b, buildStream(b))
	})
}

func FuzzMeta(f *testing.F) {
	for _, p := range seedFiles(f) {
		if m := metadataPrefix(readSeed(p, 64<<10)); m != nil {
			f.Add(m)
		}
	}

	for _, p := range allocProbes() {
		f.Add(p.block)
		f.Add(cat([]byte("fLaC"), streamInfoBlock(), p.block))
	}

	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) > 1<<20 {
			t.Skip()
		}

		bound := metaBound(len(b))

		for _, e := range []struct {
			name string
			run  func() error
		}{
			{"flac.Parse", func() error { _, err := fork.Parse(bytes.NewReader(b)); return err }},
			{"flac.NewSeek", func() error { _, err := fork.NewSeek(bytes.NewReader(b)); return err }},
			{"meta.Parse", func() error { _, err := fmeta.Parse(bytes.NewReader(b)); return err }},
		} {
			start := time.Now()

			var a, z runtime.MemStats
			runtime.ReadMemStats(&a)

			var pn string

			func() {
				defer func() {
					if p := recover(); p != nil {
						pn = fmt.Sprintf("%v\n%s", p, trimStack(debug.Stack()))
					}
				}()

				_ = e.run()
			}()
			runtime.ReadMemStats(&z)

			if pn != "" {
				t.Fatalf("%s panicked: %s", e.name, pn)
			}

			if d := time.Since(start); d > slowInput {
				t.Fatalf("%s took %s", e.name, d)
			}

			if n := z.TotalAlloc - a.TotalAlloc; n > bound {
				t.Fatalf("%s allocated %d bytes for a %d-byte input (bound %d)", e.name, n, len(b), bound)
			}
		}
	})
}

// metaBound is FuzzMeta's bound: #33's, 4 MiB + 16 bytes per input byte.
// FLACHARNESS_META_PER_BYTE replaces the 16: 1040 tolerates a cost of 4 KiB
// per metadata string (a string takes four input bytes at least), so the
// fuzzer can look past that for allocations that follow declared lengths.
func metaBound(n int) uint64 {
	per := uint64(16)
	if v, err := strconv.Atoi(os.Getenv("FLACHARNESS_META_PER_BYTE")); err == nil && v > 0 {
		per = uint64(v)
	}

	return 4<<20 + per*uint64(n)
}

// seedBytes is a deterministic byte string for FuzzFrame's seeds.
func seedBytes(i, n int) []byte {
	b := make([]byte, n)

	x := uint32(2166136261) ^ uint32(i)*16777619
	for k := range b {
		x ^= x << 13
		x ^= x >> 17
		x ^= x << 5
		b[k] = byte(x)
	}

	return b
}

// decoded is one decoder's frames, hashed.
type decoded struct {
	frames     []frameDigest
	err        string
	panic      string
	badShape   string
	bps        int
	outOfRange bool // a sample outside the stream's depth
}

type frameDigest struct {
	block, channels int
	pcm             [32]byte
}

func decodeAll(name string, stream []byte) (r decoded) {
	defer func() {
		if p := recover(); p != nil {
			r.panic = fmt.Sprintf("%v\n%s", p, trimStack(debug.Stack()))
		}
	}()

	d, err := openDecoder(name, bytes.NewReader(stream))
	if err != nil {
		r.err = err.Error()
		return r
	}

	info := d.Info()
	r.bps = int(info.BitsPerSample)
	width := sampleWidth(r.bps)

	var buf []byte

	for len(r.frames) < 1<<16 {
		fr, err := d.Next()
		if err != nil {
			if !errors.Is(err, io.EOF) {
				r.err = err.Error()
			}

			return r
		}

		if r.badShape == "" && !frameShapeOK(fr, int(info.NChannels)) {
			lens := make([]int, len(fr.Samples))
			for c, ch := range fr.Samples {
				lens[c] = len(ch)
			}

			r.badShape = fmt.Sprintf("frame %d: block size %d, channel lengths %v, STREAMINFO channels %d",
				len(r.frames), fr.BlockSize, lens, info.NChannels)
		}

		for _, ch := range fr.Samples {
			for _, v := range ch {
				if !inRange(v, r.bps) {
					r.outOfRange = true
				}
			}
		}

		buf = pack(buf, fr, width)
		r.frames = append(r.frames, frameDigest{fr.BlockSize, len(fr.Samples), sha256.Sum256(buf)})
	}

	return r
}

func checkDecode(t *testing.T, target string, input, stream []byte) {
	start := time.Now()

	var a, z runtime.MemStats
	runtime.ReadMemStats(&a)

	fr := decodeAll("fork", stream)

	runtime.ReadMemStats(&z)

	if fr.panic != "" {
		if allowedPanic(fr.panic) {
			keep("allowed", target, input, stream, fr.panic)
			return
		}

		t.Fatalf("fork panicked: %s", fr.panic)
	}

	if fr.badShape != "" {
		t.Fatalf("fork yielded a malformed frame: %s", fr.badShape)
	}

	if d := time.Since(start); d > slowInput {
		t.Fatalf("fork took %s on a %d-byte stream", d, len(stream))
	}

	if n := z.TotalAlloc - a.TotalAlloc; n > streamAllocBound(len(stream)) {
		t.Fatalf("fork allocated %d bytes for a %d-byte stream", n, len(stream))
	}
	// A divergence is only worth keeping on a stream both decoders read to
	// its end, at a depth upstream decodes (it refuses 32-bit frames and
	// holds the side channel of a 32-bit stream in 32 bits), whose samples
	// all fit the depth: an out-of-range prediction is an invalid stream, on
	// which any two decoders may wrap differently.
	if fr.err != "" || fr.outOfRange || fr.bps > 24 {
		return
	}

	ur := decodeAll("upstream", stream)
	if ur.err != "" || ur.panic != "" || len(ur.frames) != len(fr.frames) {
		return
	}

	for i := range fr.frames {
		a, b := fr.frames[i], ur.frames[i]
		if a.block == b.block && a.channels == b.channels && a.pcm != b.pcm {
			keep("observations", target, input, stream, fmt.Sprintf("frame %d: the decoders' samples differ", i))
			return
		}
	}
}

// allowedPanic reports whether a panic was raised in a function named in
// FLACHARNESS_ALLOW_PANIC.
func allowedPanic(p string) bool {
	site := panicSite(p)
	for _, name := range strings.Split(os.Getenv("FLACHARNESS_ALLOW_PANIC"), ",") {
		if name = strings.TrimSpace(name); name != "" && strings.Contains(site, name) {
			return true
		}
	}

	return false
}

// panicSite is the function frame that raised a panic: the first frame of
// the trimmed stack outside the runtime. The first line is the message.
func panicSite(p string) string {
	lines := strings.Split(p, "\n")
	for _, l := range lines[min(1, len(lines)):] {
		l = strings.TrimSpace(l)
		if l == "" || strings.HasPrefix(l, "goroutine ") || strings.HasPrefix(l, "/") ||
			strings.HasPrefix(l, "runtime.") || strings.HasPrefix(l, "panic(") || !strings.Contains(l, "(") {
			continue
		}

		return l
	}

	return ""
}

var kept struct {
	sync.Mutex
	n map[string]int
}

// keep writes an input under FLACHARNESS_OBS/<kind>/<target>, at most 200
// of a kind per process: the stream as .flac, the reason as .json.
func keep(kind, target string, input, stream []byte, why string) {
	dir := os.Getenv("FLACHARNESS_OBS")
	if dir == "" {
		return
	}

	kept.Lock()
	defer kept.Unlock()

	if kept.n == nil {
		kept.n = map[string]int{}
	}

	if kept.n[kind] >= 200 {
		return
	}

	kept.n[kind]++
	sum := sha256.Sum256(input)

	name := filepath.Join(dir, kind, target, hex.EncodeToString(sum[:8]))
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		return
	}

	_ = os.WriteFile(name+".flac", stream, 0o644)
	meta, _ := json.Marshal(map[string]string{"target": target, "why": why})
	_ = os.WriteFile(name+".json", meta, 0o644)
}
