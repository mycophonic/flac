package main

import (
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"math/rand/v2"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// cmdGen writes every corpus under -out: the 32-bit signals and their
// libFLAC encodings, the bench fixtures, and the crafted reproducers. Fixed
// seeds: two runs write byte-identical files; -sha lists them.
func cmdGen(args []string) error {
	fl := flag.NewFlagSet("gen", flag.ExitOnError)
	out := fl.String("out", "", "corpus directory (required)")
	sha := fl.String("sha", "", "write the sha256 of every file written, one per line")
	_ = fl.Parse(args)

	if *out == "" {
		return fmt.Errorf("gen: -out is required")
	}

	lib := newLibFLAC()

	steps := []struct {
		name string
		run  func() (int, error)
	}{
		{"wav32", func() (int, error) { return genSignals32(filepath.Join(*out, "wav32")) }},
		{"gen32", func() (int, error) {
			return encodeCorpus(lib, filepath.Join(*out, "wav32"), filepath.Join(*out, "gen32"))
		}},
		{"bench", func() (int, error) { return genBench(lib, filepath.Join(*out, "bench")) }},
		{"targeted", func() (int, error) { return genTargeted(filepath.Join(*out, "targeted")) }},
		{"crafted", func() (int, error) { return genWide(filepath.Join(*out, "crafted")) }},
		{"control", func() (int, error) {
			return genControl(filepath.Join(*out, "control"), filepath.Join(*out, "boundary"))
		}},
		{"hostile", func() (int, error) { return genHostile(filepath.Join(*out, "hostile")) }},
	}
	for _, s := range steps {
		n, err := s.run()
		if err != nil {
			return fmt.Errorf("gen %s: %w", s.name, err)
		}

		fmt.Printf("   %-9s %d files\n", s.name, n)
	}

	if *sha == "" {
		return nil
	}

	return writeCorpusSHA(*out, *sha)
}

func writeCorpusSHA(root, path string) error {
	var lines []string

	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}

		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(root, p)
		sum := sha256.Sum256(b)
		lines = append(lines, hex.EncodeToString(sum[:])+" "+filepath.ToSlash(rel))

		return nil
	})
	if err != nil {
		return err
	}

	sort.Strings(lines)

	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// --- signals ---------------------------------------------------------------

// writeWAV writes interleaved stereo frames as a RIFF WAVE file of the depth.
func writeWAV(path string, frames [][2]int64, bits, rate int) error {
	bytesPer := bits / 8
	pcm := make([]byte, 0, len(frames)*2*bytesPer)

	var b [8]byte

	for _, f := range frames {
		for _, v := range f {
			binary.LittleEndian.PutUint64(b[:], uint64(v))
			pcm = append(pcm, b[:bytesPer]...)
		}
	}

	var w []byte

	put32 := func(v uint32) { w = binary.LittleEndian.AppendUint32(w, v) }
	put16 := func(v uint16) { w = binary.LittleEndian.AppendUint16(w, v) }

	w = append(w, "RIFF"...)

	put32(uint32(36 + len(pcm)))

	w = append(w, "WAVEfmt "...)

	put32(16)
	put16(1)
	put16(2)
	put32(uint32(rate))
	put32(uint32(rate * 2 * bytesPer))
	put16(uint16(2 * bytesPer))
	put16(uint16(bits))

	w = append(w, "data"...)

	put32(uint32(len(pcm)))
	w = append(w, pcm...)

	return os.WriteFile(path, w, 0o644)
}

// genSignals32 writes the stereo 32-bit WAV signals behind the 32-bit corpus,
// those that make libFLAC choose a 33-bit side channel: full-scale
// anti-phase, correlated, noise, square, ramp, low-level, wasted bits, 96
// kHz, and short files of 1 to 33 samples.
func genSignals32(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	const (
		maxV = math.MaxInt32
		minV = math.MinInt32
		n    = 9000
	)

	rng := rand.New(rand.NewPCG(20261024, 0))
	clamp := func(v float64) int64 { return max(minV, min(maxV, int64(v))) }

	sine := make([]float64, n)
	for i := range sine {
		sine[i] = math.Sin(2 * math.Pi * 997 * float64(i) / 44100)
	}

	rangeInt := func(lo, hi int64) int64 { return lo + rng.Int64N(hi-lo+1) }
	files := map[string]struct {
		frames [][2]int64
		rate   int
	}{}
	add := func(name string, rate int, f func(i int) [2]int64, count int) {
		fr := make([][2]int64, count)
		for i := range fr {
			fr[i] = f(i)
		}

		files[name] = struct {
			frames [][2]int64
			rate   int
		}{fr, rate}
	}
	add("const-extremes", 44100, func(int) [2]int64 { return [2]int64{maxV, minV} }, 4096)
	add(
		"anti-sine-full",
		44100,
		func(i int) [2]int64 { return [2]int64{clamp(sine[i] * maxV), clamp(-sine[i] * maxV)} },
		n,
	)
	add("corr-sine-full", 44100, func(i int) [2]int64 {
		return [2]int64{clamp(sine[i] * maxV), clamp(sine[i]*maxV - 3 + float64(rangeInt(-2, 2)))}
	}, n)
	add("noise-full", 44100, func(int) [2]int64 { return [2]int64{rangeInt(minV, maxV), rangeInt(minV, maxV)} }, n)

	noise := make([]int64, n)
	for i := range noise {
		noise[i] = rangeInt(minV/2, maxV/2)
	}

	add("noise-corr", 44100, func(i int) [2]int64 {
		return [2]int64{clamp(float64(2 * noise[i])), clamp(float64(2*noise[i] + rangeInt(-1000, 1000)))}
	}, n)
	add("square-extremes", 44100, func(i int) [2]int64 {
		if (i/37)%2 == 1 {
			return [2]int64{maxV, minV}
		}

		return [2]int64{minV, maxV}
	}, n)
	step := float64(int64(1)<<32) / n
	add("ramp-inverse", 44100, func(i int) [2]int64 {
		v := clamp(minV + float64(i)*step)
		return [2]int64{v, clamp(float64(-v - 1))}
	}, n)
	add("low-level", 44100, func(i int) [2]int64 { return [2]int64{clamp(sine[i] * 1000), clamp(-sine[i] * 900)} }, n)
	add("wasted-bits", 44100, func(i int) [2]int64 {
		return [2]int64{clamp(sine[i]*maxV) &^ 0xFF, clamp(-sine[i]*maxV) &^ 0xFF}
	}, n)
	add("hires-96k-anti", 96000, func(i int) [2]int64 {
		return [2]int64{clamp(sine[i] * maxV), clamp(-sine[i] * maxV * 0.999)}
	}, n)

	for _, k := range []int{1, 2, 3, 4, 5, 15, 16, 17, 33} {
		add(
			fmt.Sprintf("short-%d", k),
			44100,
			func(i int) [2]int64 { return [2]int64{maxV - int64(i), minV + int64(i)} },
			k,
		)
	}

	for name, f := range files {
		if err := writeWAV(filepath.Join(dir, name+".wav"), f.frames, 32, f.rate); err != nil {
			return 0, err
		}
	}

	return len(files), nil
}

// encodeSettings are libFLAC's settings the 32-bit corpus is encoded under;
// an encoding flac -t refuses is dropped.
var encodeSettings = []struct {
	tag  string
	args []string
}{
	{"m8", []string{"-8", "-m"}},
	{"M5", []string{"-5", "-M"}},
	{"f0", []string{"-0"}},
	{"fx", []string{"-l", "0", "-m", "--lax"}},
	{"e8", []string{"-8", "-e", "-p", "-m"}},
	{"l32", []string{"-l", "32", "-m", "--lax"}},
	{"b16", []string{"-8", "-m", "--blocksize=16"}},
	{"b192", []string{"-8", "-m", "--blocksize=192"}},
	{"b4608", []string{"-8", "-m", "--blocksize=4608"}},
	{"r8", []string{"-8", "-m", "-r", "8"}},
	{"ind", []string{"-8", "--no-mid-side"}},
}

func encodeCorpus(lib libFLAC, wavDir, dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	wavs, _ := filepath.Glob(filepath.Join(wavDir, "*.wav"))
	sort.Strings(wavs)

	kept := 0

	for _, w := range wavs {
		base := strings.TrimSuffix(filepath.Base(w), ".wav")
		for _, s := range encodeSettings {
			out := filepath.Join(dir, base+"."+s.tag+".flac")

			args := append(append([]string{"-s", "-f"}, s.args...), "-o", out, w)
			if err := exec.Command(lib.path, args...).Run(); err != nil {
				_ = os.Remove(out)
				continue
			}

			if err := exec.Command(lib.path, "-s", "-t", out).Run(); err != nil {
				_ = os.Remove(out)
				continue
			}

			kept++
		}
	}

	return kept, nil
}

// genBench writes three-second stereo fixtures (16/44.1, 24/96, 24/192): two
// tones per channel plus low-level broadband noise, as raw PCM and as the
// libFLAC -8 encoding, with synthetic.json describing them.
func genBench(lib libFLAC, dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	type record struct {
		File      string `json:"file"`
		Rate      int    `json:"rate"`
		Bits      int    `json:"bits"`
		Seconds   int    `json:"seconds"`
		PCMMD5    string `json:"pcm_md5"`
		PCMBytes  int    `json:"pcm_bytes"`
		FLACBytes int64  `json:"flac_bytes"`
	}

	var records []record

	for _, f := range []struct{ rate, bits int }{{44100, 16}, {96000, 24}, {192000, 24}} {
		rng := rand.New(rand.NewPCG(5388, uint64(f.rate)))
		full := float64(int64(1)<<(f.bits-1) - 1)
		bytesPer := f.bits / 8
		pcm := make([]byte, 0, f.rate*3*2*bytesPer)

		var b [8]byte

		for i := range f.rate * 3 {
			t := float64(i) / float64(f.rate)
			for c := range 2 {
				v := 0.43*math.Sin(2*math.Pi*(997+float64(c)*113)*t) +
					0.21*math.Sin(2*math.Pi*(71+float64(c)*19)*t) +
					0.06*(rng.Float64()*2-1)
				x := int64(math.Round(v * full))
				binary.LittleEndian.PutUint64(b[:], uint64(x))
				pcm = append(pcm, b[:bytesPer]...)
			}
		}

		stem := filepath.Join(dir, fmt.Sprintf("bench-%d-%d-stereo", f.bits, f.rate))
		if err := os.WriteFile(stem+".raw", pcm, 0o644); err != nil {
			return 0, err
		}

		args := []string{
			"--force", "--silent", "-8", "--force-raw-format", "--endian=little", "--sign=signed",
			"--channels=2", fmt.Sprintf("--bps=%d", f.bits), fmt.Sprintf("--sample-rate=%d", f.rate),
			"-o", stem + ".flac", stem + ".raw",
		}
		if out, err := exec.Command(lib.path, args...).CombinedOutput(); err != nil {
			return 0, fmt.Errorf("flac -8 on %s: %w\n%s", stem, err, out)
		}

		st, err := os.Stat(stem + ".flac")
		if err != nil {
			return 0, err
		}

		sum := md5.Sum(pcm)
		records = append(records, record{
			File: filepath.Base(stem) + ".flac", Rate: f.rate, Bits: f.bits, Seconds: 3,
			PCMMD5: hex.EncodeToString(sum[:]), PCMBytes: len(pcm), FLACBytes: st.Size(),
		})
	}

	js, _ := json.MarshalIndent(records, "", "  ")
	if err := os.WriteFile(filepath.Join(dir, "synthetic.json"), append(js, '\n'), 0o644); err != nil {
		return 0, err
	}

	return len(records) * 2, nil
}

// --- crafted streams --------------------------------------------------------

// streamInfoBody is a STREAMINFO body: block sizes, no frame sizes, the
// rate, channels, depth and count, and the MD5 (zero: unknown).
func streamInfoBody(bps, nsamples, minBS, maxBS, rate, channels int, md5sum [16]byte) []byte {
	w := &bitWriter{}
	w.put(uint64(minBS), 16)
	w.put(uint64(maxBS), 16)
	w.put(0, 24)
	w.put(0, 24)
	w.put(uint64(rate), 20)
	w.put(uint64(channels-1), 3)
	w.put(uint64(bps-1), 5)
	w.put(uint64(nsamples), 36)
	w.put(binary.BigEndian.Uint64(md5sum[:8]), 64)
	w.put(binary.BigEndian.Uint64(md5sum[8:]), 64)
	w.align()

	return w.b
}

// signature is "fLaC" and the blocks.
func signature(blocks ...[]byte) []byte {
	out := []byte("fLaC")
	for _, b := range blocks {
		out = append(out, b...)
	}

	return out
}

var sampleSizeCodeOf = map[int]uint64{16: 0b100, 24: 0b110, 32: 0b111}

// frameHeader starts a frame: sync, fixed block size, the 8-bit block size
// code, the rate code, the assignment, the depth, the number as given (UTF-8
// coded bytes), the block size, and the CRC-8.
func frameHeader(bps, blockSize int, chan_, rateCode uint64, number []byte) *bitWriter {
	h := &bitWriter{}
	h.put(0x3FFE, 14)
	h.put(0, 1)
	h.put(0, 1)
	h.put(0b0110, 4)
	h.put(rateCode, 4)
	h.put(chan_, 4)
	h.put(sampleSizeCodeOf[bps], 3)
	h.put(0, 1)

	for _, b := range number {
		h.put(uint64(b), 8)
	}

	h.put(uint64(blockSize-1), 8)
	h.align()

	w := &bitWriter{}

	w.b = append(append([]byte{}, h.b...), crc8(h.b))

	return w
}

// seal pads the frame and appends its CRC-16.
func seal(w *bitWriter) []byte {
	w.align()
	sum := crc16(w.b)

	return append(append([]byte{}, w.b...), byte(sum>>8), byte(sum))
}

type subframeFn func(w *bitWriter)

func craftFrame(subframes []subframeFn, bps, blockSize int, chan_, rateCode uint64, number []byte) []byte {
	w := frameHeader(bps, blockSize, chan_, rateCode, number)
	for _, f := range subframes {
		f(w)
	}

	return seal(w)
}

func constantSub(bps int, value int64) subframeFn {
	return func(w *bitWriter) {
		w.put(0, 1)
		w.put(0b000000, 6)
		w.put(0, 1)
		w.putSigned(value, uint(bps))
	}
}

func verbatimWastedSub(bps, wasted, blockSize int) subframeFn {
	return func(w *bitWriter) {
		w.put(0, 1)
		w.put(0b000001, 6)
		w.put(1, 1)
		w.unary(uint64(wasted - 1))

		width := 0
		if wasted < bps {
			width = bps - wasted
		}

		for range blockSize {
			w.put(0, uint(width))
		}
	}
}

func fixedSub(bps, order int, warmup []int64, residuals int) subframeFn {
	return func(w *bitWriter) {
		w.put(0, 1)
		w.put(0b001000|uint64(order), 6)
		w.put(0, 1)

		for _, v := range warmup {
			w.putSigned(v, uint(bps))
		}

		w.put(0, 2)
		w.put(0, 4)
		w.put(0, 4)

		for range residuals {
			w.put(1, 1)
		}
	}
}

// cleanTwoFrames is the clean stream's two 16-bit stereo constant frames, the
// second at the rate code given (0b1001 is 44.1 kHz, the first frame's).
func cleanTwoFrames(rate1 uint64) []byte {
	f0 := craftFrame([]subframeFn{constantSub(16, 1000), constantSub(16, -1000)}, 16, 16, 0b0001, 0b1001, []byte{0})
	f1 := craftFrame([]subframeFn{constantSub(16, 2000), constantSub(16, -2000)}, 16, 16, 0b0001, rate1, []byte{1})

	return append(f0, f1...)
}

// genHostile writes one-defect streams for the decoder's refusals, with
// valid CRC-8 and CRC-16, each named with its issue, a clean twin, and the
// three streams of the decisions #20, #21 and #22.
func genHostile(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	var zero [16]byte

	si := func(bps, n int, md5sum [16]byte) []byte { return streamInfoBody(bps, n, 16, 16, 44100, 2, md5sum) }
	si16 := si(16, 32, zero)
	twoFrames := cleanTwoFrames

	var wrong [16]byte
	for i := range wrong {
		wrong[i] = []byte{0x01, 0x23, 0x45, 0x67, 0x89, 0xAB, 0xCD, 0xEF}[i%8]
	}

	files := map[string][]byte{
		"clean-16bit-two-frames": append(signature(metaBlock(0, true, 34, si16)), twoFrames(0b1001)...),
		"int32-wasted16-depth16": append(
			signature(metaBlock(0, true, 34, si(16, 16, zero))),
			craftFrame(
				[]subframeFn{verbatimWastedSub(16, 16, 16), constantSub(16, 0)},
				16,
				16,
				0b0001,
				0b1001,
				[]byte{0},
			)...,
		),
		"int32-wasted24-depth16": append(
			signature(metaBlock(0, true, 34, si(16, 16, zero))),
			craftFrame(
				[]subframeFn{verbatimWastedSub(16, 24, 16), constantSub(16, 0)},
				16,
				16,
				0b0001,
				0b1001,
				[]byte{0},
			)...,
		),
		"int32-fixed4-block4": append(
			signature(metaBlock(0, true, 34, si(16, 4, zero))),
			craftFrame(
				[]subframeFn{fixedSub(16, 4, []int64{1, 2, 3, 4}, 0), constantSub(16, 0)},
				16,
				4,
				0b0001,
				0b1001,
				[]byte{0},
			)...,
		),
		"wide-midside-wasted33": append(
			signature(metaBlock(0, true, 34, si(32, 16, zero))),
			craftFrame(
				[]subframeFn{constantSub(32, 0), verbatimWastedSub(33, 33, 16)},
				32,
				16,
				0b1010,
				0b1001,
				[]byte{0},
			)...,
		),
		"wide-midside-wasted40": append(
			signature(metaBlock(0, true, 34, si(32, 16, zero))),
			craftFrame(
				[]subframeFn{constantSub(32, 0), verbatimWastedSub(33, 40, 16)},
				32,
				16,
				0b1010,
				0b1001,
				[]byte{0},
			)...,
		),
		"cut-in-padding": append(
			signature(metaBlock(0, false, 34, si16), metaBlock(1, true, 1000, nil)),
			make([]byte, 10)...,
		),
		"frame-number-ff": append(
			signature(metaBlock(0, true, 34, si(16, 16, zero))),
			craftFrame(
				[]subframeFn{constantSub(16, 1000), constantSub(16, -1000)},
				16,
				16,
				0b0001,
				0b1001,
				[]byte{0xFF},
			)...,
		),
		"second-streaminfo": append(
			signature(metaBlock(0, false, 34, si16), metaBlock(0, true, 34, si16)),
			twoFrames(0b1001)...,
		),
		"seektable-stray-length": append(
			signature(metaBlock(0, false, 34, si16), metaBlock(3, true, 20, make([]byte, 20))),
			twoFrames(0b1001)...,
		),
		"rate-change":    append(signature(metaBlock(0, true, 34, si16)), twoFrames(0b1010)...),
		"md5-wrong":      append(signature(metaBlock(0, true, 34, si(16, 32, wrong))), twoFrames(0b1001)...),
		"no-flac-marker": twoFrames(0b1001),
		"frame-number-overlong": append(
			append(
				signature(metaBlock(0, true, 34, si16)),
				craftFrame(
					[]subframeFn{constantSub(16, 1000), constantSub(16, -1000)},
					16,
					16,
					0b0001,
					0b1001,
					[]byte{0xC0, 0x80},
				)...,
			),
			craftFrame(
				[]subframeFn{constantSub(16, 2000), constantSub(16, -2000)},
				16,
				16,
				0b0001,
				0b1001,
				[]byte{1},
			)...,
		),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name+".flac"), data, 0o644); err != nil {
			return 0, err
		}
	}

	return len(files), nil
}

// wideCases are the decorrelated assignments and which channel is the side.
var wideCases = []struct {
	name  string
	chan_ uint64
	side  int
}{{"leftside", 0b1000, 1}, {"sideright", 0b1001, 0}, {"midside", 0b1010, 1}}

// wideStreamInfo is the STREAMINFO of the wide reproducers: 32-bit stereo,
// 44.1 kHz, blocks 16..4096.
func wideStreamInfo(nsamples int, md5sum [16]byte) []byte {
	return signature(metaBlock(0, true, 34, streamInfoBody(32, nsamples, 16, 4096, 44100, 2, md5sum)))
}

// wideSubframes writes a 32-bit stereo frame's two subframes: a constant on
// the plain channel and a predictor of the kind and order on the side, whose
// warm-up samples are warm (33 bits when the side) with the coefficients and
// residuals given.
func wideSide(w *bitWriter, kind string, order, bps int, warm int64, coeffs func(i int) int64, residuals int) {
	w.put(0, 1)

	if kind == "fixed" {
		w.put(0b001000|uint64(order), 6)
	} else {
		w.put(0b100000|uint64(order-1), 6)
	}

	w.put(0, 1)

	for range order {
		w.putSigned(warm, uint(bps))
	}

	if kind == "lpc" {
		w.put(14, 4)
		w.put(0, 5)

		for i := range order {
			w.putSigned(coeffs(i), 15)
		}
	}

	w.put(0, 2)
	w.put(0, 4)
	w.put(0, 4)

	for range residuals {
		w.put(1, 1)
	}
}

// genWide writes #34's reproducers: one frame of one sample in each
// decorrelated assignment, the side channel declaring fixed order 4 or LPC
// order 32; libFLAC refuses every one.
func genWide(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	n := 0

	for _, c := range wideCases {
		for _, k := range []struct {
			kind  string
			order int
		}{{"fixed", 4}, {"lpc", 32}} {
			w := frameHeader(32, 1, c.chan_, 0b1001, []byte{0})
			for ch := range 2 {
				if ch != c.side {
					constantSub(32, math.MaxInt32)(w)
					continue
				}

				wideSide(w, k.kind, k.order, 33, 1, func(int) int64 { return 1 }, 0)
			}

			name := fmt.Sprintf("wide-%s-%s%d-block1.flac", c.name, k.kind, k.order)
			if err := os.WriteFile(
				filepath.Join(dir, name),
				append(wideStreamInfo(1, [16]byte{}), seal(w)...),
				0o644,
			); err != nil {
				return 0, err
			}

			n++
		}
	}

	return n, nil
}

// genControl writes the twins of genWide's frames: in control, the same side
// predictor at the smallest block the order allows (order + 1 samples), the
// side carrying 2^32-1, which only the 33-bit path holds, decoding to
// L=INT32_MAX, R=INT32_MIN; in boundary, order 1 on a one-sample block,
// which libFLAC refuses.
func genControl(control, boundary string) (int, error) {
	for _, d := range []string{control, boundary} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return 0, err
		}
	}
	// L=INT32_MAX and R=INT32_MIN in each assignment: the plain channel's
	// constant, with the side L-R = 2^32-1.
	plain := map[uint64]int64{0b1000: math.MaxInt32, 0b1001: math.MinInt32, 0b1010: -1}

	const side = int64(1)<<32 - 1

	n := 0

	for _, c := range wideCases {
		for _, k := range []struct {
			kind  string
			order int
		}{{"fixed", 4}, {"lpc", 32}} {
			block := k.order + 1

			var pcm []byte
			for range block {
				pcm = binary.LittleEndian.AppendUint32(pcm, uint32(math.MaxInt32))
				pcm = binary.LittleEndian.AppendUint32(pcm, uint32(1<<31))
			}

			w := frameHeader(32, block, c.chan_, 0b1001, []byte{0})
			for ch := range 2 {
				if ch != c.side {
					constantSub(32, plain[c.chan_])(w)
					continue
				}

				wideSide(w, k.kind, k.order, 33, side, func(i int) int64 {
					if i == 0 {
						return 1
					}

					return 0
				}, block-k.order)
			}

			name := fmt.Sprintf("wide-%s-%s%d-block%d.flac", c.name, k.kind, k.order, block)

			data := append(wideStreamInfo(block, md5.Sum(pcm)), seal(w)...)
			if err := os.WriteFile(filepath.Join(control, name), data, 0o644); err != nil {
				return 0, err
			}

			n++
		}

		for _, kind := range []string{"fixed", "lpc"} {
			w := frameHeader(32, 1, c.chan_, 0b1001, []byte{0})
			for ch := range 2 {
				if ch != c.side {
					constantSub(32, math.MaxInt32)(w)
					continue
				}

				wideSide(w, kind, 1, 33, 1, func(int) int64 { return 1 }, 0)
			}

			name := fmt.Sprintf("wide-%s-%s1-block1.flac", c.name, kind)
			if err := os.WriteFile(
				filepath.Join(boundary, name),
				append(wideStreamInfo(1, [16]byte{}), seal(w)...),
				0o644,
			); err != nil {
				return 0, err
			}
		}
	}

	return n, nil
}

// genTargeted writes the small CRC-valid reproducers: #18's one-sample
// frames declaring fixed order 4 and LPC order 32, #10's valid 32-bit
// mid/side constant frame with its expected PCM, and a reserved metadata
// header.
func genTargeted(dir string) (int, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, err
	}

	unhex := func(s string) []byte {
		b, err := hex.DecodeString(s)
		if err != nil {
			panic(err)
		}

		return b
	}
	withCRC16 := func(frame []byte) []byte {
		sum := crc16(frame)
		return append(frame, byte(sum>>8), byte(sum))
	}
	si := streamInfoBody(16, 1, 16, 4096, 44100, 2, [16]byte{})
	h := unhex("fff869180000")

	h = append(h, crc8(h))
	for _, t := range []struct {
		name string
		sub  []byte
	}{
		{"fixed-order4-block1", append([]byte{0x18}, make([]byte, 12)...)},
		{"lpc-order32-block1", append([]byte{0x7e}, make([]byte, 96)...)},
	} {
		frame := withCRC16(append(append([]byte{}, h...), t.sub...))

		data := append(signature(metaBlock(0, true, 34, si)), frame...)
		if err := os.WriteFile(filepath.Join(dir, t.name+".flac"), data, 0o644); err != nil {
			return 0, err
		}
	}

	if err := os.WriteFile(filepath.Join(dir, "reserved-meta.bin"), unhex("07000000"), 0o644); err != nil {
		return 0, err
	}
	// A valid 32-bit mid/side constant frame, L=INT32_MAX, R=INT32_MIN: the
	// side needs 33 signed bits though each output sample fits int32.
	var raw []byte
	for range 16 {
		raw = binary.LittleEndian.AppendUint32(raw, uint32(math.MaxInt32))
		raw = binary.LittleEndian.AppendUint32(raw, uint32(1<<31))
	}

	si32 := streamInfoBody(32, 16, 16, 16, 44100, 2, md5.Sum(raw))
	h32 := unhex("fff869ae000f")
	h32 = append(h32, crc8(h32))
	w := &bitWriter{}

	w.b = append([]byte{}, h32...)
	w.put(0, 8)        // constant subframe, no wasted bits
	w.put(1<<32-1, 32) // mid: all ones, -1
	w.put(0, 8)        // constant subframe
	w.put(1<<32-1, 33) // side: 2^32-1 in 33 bits

	data := append(signature(metaBlock(0, true, 34, si32)), withCRC16(seal(w)[:len(seal(w))-2])...)
	if err := os.WriteFile(filepath.Join(dir, "valid-32bit-midside.flac"), data, 0o644); err != nil {
		return 0, err
	}

	if err := os.WriteFile(filepath.Join(dir, "valid-32bit-midside.expected.raw"), raw, 0o644); err != nil {
		return 0, err
	}

	return 5, nil
}
