package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"runtime"
	"runtime/debug"

	upstream "github.com/mewkiz/flac"
	umeta "github.com/mewkiz/flac/meta"

	fork "github.com/mycophonic/flac"
	fmeta "github.com/mycophonic/flac/meta"
)

// AllocRecord is one line of alloc.jsonl: the bytes one parse of one hostile
// input allocates, measured as runtime.MemStats.TotalAlloc around the call
// (the minimum of three runs).
type AllocRecord struct {
	Probe      string `json:"probe"`
	Issue      string `json:"issue"`
	Entry      string `json:"entry"`
	Decoder    string `json:"decoder"`
	InputBytes int    `json:"input_bytes"`
	Declared   int64  `json:"declared"`
	AllocBytes uint64 `json:"alloc_bytes"`
	Mallocs    uint64 `json:"mallocs"`
	Bound      uint64 `json:"bound"`
	Bounded    bool   `json:"bounded"`
	Error      string `json:"error,omitempty"`
	Panic      string `json:"panic,omitempty"`
}

// allocBound is the bound #33's fuzz target holds every parse to.
func allocBound(n int) uint64 { return 4<<20 + 16*uint64(n) }

type allocProbe struct {
	name, issue string
	declared    int64
	block       []byte // one metadata block: header and the bytes present
}

func metaBlock(typ byte, last bool, length int, body []byte) []byte {
	h := typ
	if last {
		h |= 0x80
	}

	b := []byte{h, byte(length >> 16), byte(length >> 8), byte(length)}

	return append(b, body...)
}

func be32(v uint32) []byte    { return binary.BigEndian.AppendUint32(nil, v) }
func le32(v uint32) []byte    { return binary.LittleEndian.AppendUint32(nil, v) }
func cat(bs ...[]byte) []byte { return bytes.Join(bs, nil) }

// streamInfoBlock is a valid, non-last STREAMINFO: 16-bit stereo, 44.1 kHz,
// block size 4096, total samples and MD5 unknown.
func streamInfoBlock() []byte {
	var b [34]byte
	binary.BigEndian.PutUint16(b[0:], 4096)
	binary.BigEndian.PutUint16(b[2:], 4096)

	v := uint64(44100)<<44 | uint64(1)<<41 | uint64(15)<<36
	binary.BigEndian.PutUint64(b[10:], v)

	return metaBlock(0, false, 34, b[:])
}

func allocProbes() []allocProbe {
	const mib = 1 << 20

	ps := []allocProbe{{
		// The first evaluation's probe: an 8-byte VORBIS_COMMENT block whose
		// vendor length claims 32 MiB.
		name: "vorbis-vendor-8-byte-block", issue: "#25", declared: 32 * mib,
		block: []byte{0x84, 0, 0, 4, 0, 0, 0, 2},
	}}
	for _, l := range []uint32{1 * mib, 32 * mib} {
		s := fmt.Sprintf("%dMiB", l/mib)
		seek := int(min(l, 1<<24-1)) / 18 * 18
		app := int(min(l, 1<<24-1))
		ps = append(
			ps,
			allocProbe{"vorbis-vendor-" + s, "#25", int64(l), metaBlock(4, true, 8, cat(le32(l), le32(0)))},
			allocProbe{"vorbis-tag-" + s, "#25", int64(l), metaBlock(4, true, 12, cat(le32(0), le32(1), le32(l)))},
			allocProbe{"picture-mime-" + s, "#25", int64(l), metaBlock(6, true, 8, cat(be32(3), be32(l)))},
			allocProbe{
				"picture-description-" + s,
				"#25",
				int64(l),
				metaBlock(6, true, 12, cat(be32(3), be32(0), be32(l))),
			},
			allocProbe{
				"picture-data-" + s,
				"#25",
				int64(l),
				metaBlock(6, true, 32, cat(be32(3), be32(0), be32(0), make([]byte, 16), be32(l))),
			},
			allocProbe{"seektable-" + s, "#25", int64(seek), metaBlock(3, true, seek, nil)},
			allocProbe{"application-" + s, "#25", int64(app), metaBlock(2, true, app, be32(0x74657374))},
		)
	}

	ps = append(ps,
		allocProbe{"vorbis-tag-count-50000", "#25", 50000, metaBlock(4, true, 8, cat(le32(0), le32(50000)))},
		allocProbe{"padding-16MiB", "#25", 1<<24 - 1, metaBlock(1, true, 1<<24-1, nil)},
	)
	// Many short strings, every byte present: a cost that follows the count
	// of strings, not a declared length. On 7039cb99 and c8830029 a string of
	// up to 4096 bytes is read through a 4 KiB buffer that escapes to the
	// heap, so each costs 4 KiB however short it is (IETF subset/54, 1000
	// VORBIS_COMMENT blocks, is a valid file of this shape).
	tags := cat(le32(0), le32(2000))
	for i := 0; i < 2000; i++ {
		tags = append(tags, cat(le32(3), []byte("a=b"))...)
	}

	var blocks []byte

	for i := 0; i < 1000; i++ {
		body := cat(le32(0), le32(1), le32(3), []byte("a=b"))
		blocks = append(blocks, metaBlock(4, i == 999, len(body), body)...)
	}

	ps = append(ps,
		allocProbe{"vorbis-2000-short-tags", "strings", 2001, metaBlock(4, true, len(tags), tags)},
		allocProbe{"vorbis-1000-blocks", "strings", 2000, blocks},
	)

	return ps
}

type allocEntry struct {
	name   string
	stream bool // the probe's block after fLaC and a STREAMINFO, not alone
	run    func(dec string, b []byte) error
}

var allocEntries = []allocEntry{
	{"meta.Parse", false, func(dec string, b []byte) error {
		if dec == "fork" {
			_, err := fmeta.Parse(bytes.NewReader(b))
			return err
		}

		_, err := umeta.Parse(bytes.NewReader(b))

		return err
	}},
	{"flac.Parse", true, func(dec string, b []byte) error {
		if dec == "fork" {
			_, err := fork.Parse(bytes.NewReader(b))
			return err
		}

		_, err := upstream.Parse(bytes.NewReader(b))

		return err
	}},
	{"flac.NewSeek", true, func(dec string, b []byte) error {
		if dec == "fork" {
			_, err := fork.NewSeek(bytes.NewReader(b))
			return err
		}

		_, err := upstream.NewSeek(bytes.NewReader(b))

		return err
	}},
}

// measure returns the bytes and allocations fn makes, the fewest of three
// runs, and its error or panic.
func measure(fn func() error) (allocBytes, mallocs uint64, err error, panicked string) {
	allocBytes, mallocs = ^uint64(0), ^uint64(0)

	for k := 0; k < 3; k++ {
		runtime.GC()

		var a, z runtime.MemStats
		runtime.ReadMemStats(&a)
		func() {
			defer func() {
				if p := recover(); p != nil {
					panicked = clip(fmt.Sprint(p), 300) + "\n" + trimStack(debug.Stack())
				}
			}()

			err = fn()
		}()
		runtime.ReadMemStats(&z)
		allocBytes = min(allocBytes, z.TotalAlloc-a.TotalAlloc)
		mallocs = min(mallocs, z.Mallocs-a.Mallocs)
	}

	return allocBytes, mallocs, err, panicked
}

func runAllocProbes() []AllocRecord {
	var recs []AllocRecord

	for _, p := range allocProbes() {
		for _, e := range allocEntries {
			in := p.block
			if e.stream {
				in = cat([]byte("fLaC"), streamInfoBlock(), p.block)
			}

			for _, dec := range decoderNames {
				run := e.run
				n, m, err, pn := measure(func() error { return run(dec, in) })
				r := AllocRecord{
					Probe: p.name, Issue: p.issue, Entry: e.name, Decoder: dec, InputBytes: len(in),
					Declared: p.declared, AllocBytes: n, Mallocs: m, Bound: allocBound(len(in)), Panic: pn,
				}

				r.Bounded = pn == "" && n <= r.Bound
				if err != nil {
					r.Error = clip(err.Error(), 200)
				}

				recs = append(recs, r)
			}
		}
	}

	return recs
}

func cmdAlloc(args []string) error {
	fl := flag.NewFlagSet("alloc", flag.ExitOnError)
	out := fl.String("out", "-", "output, JSON lines")
	_ = fl.Parse(args)

	w, err := createOut(*out)
	if err != nil {
		return err
	}
	defer w.Close()

	jl := newJSONLines(w)
	for _, r := range runAllocProbes() {
		if err := jl.write(r); err != nil {
			return err
		}
	}

	return nil
}
