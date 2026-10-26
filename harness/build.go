package main

import (
	"encoding/binary"
)

// buildStream turns arbitrary bytes into a FLAC stream whose frames carry
// valid CRC-8 and CRC-16, so a fuzzer's mutations reach the subframe
// decoders instead of stopping at a checksum. The bytes choose the depth,
// the channels and their assignment, the block size, each subframe's
// predictor, order, wasted bits, coefficients, partitions, Rice parameters
// and residuals; past their end every choice is zero. Some choices are
// out of range on purpose: predictor orders above the block, wasted bits at
// or above the depth, reserved codes, partitions that do not divide the
// block, negative shifts.
func buildStream(data []byte) []byte {
	s := &frameSource{b: data}
	bps := s.pick([]int{16, 24, 32, 8, 12, 20, 4, 17, 31})
	channels := 1 + s.pick([]int{1, 0, 1, 1, 2, 5, 7})

	block := s.pick([]int{16, 1, 2, 3, 4, 5, 15, 17, 32, 33, 64, 192, 256, 576, 1152, 4096})
	if s.intn(8) == 0 {
		block = 1 + s.intn(4608)
	}

	assign := channels - 1
	if channels == 2 {
		assign = s.pick([]int{1, 8, 9, 10, 8, 9, 10})
	}

	if s.intn(32) == 0 {
		assign = 11 + s.intn(5) // reserved
	}

	nframes := 1 + s.intn(2)

	var out []byte

	out = append(out, "fLaC"...)
	si := make([]byte, 34)
	binary.BigEndian.PutUint16(si[0:], uint16(block))
	binary.BigEndian.PutUint16(si[2:], uint16(block))

	total := uint64(0)
	if s.intn(2) == 0 {
		total = uint64(block * nframes)
	}

	v := uint64(44100)<<44 | uint64(channels-1)<<41 | uint64(bps-1)<<36 | total
	binary.BigEndian.PutUint64(si[10:], v)

	out = append(out, 0x80, 0, 0, 34)

	out = append(out, si...)
	for n := 0; n < nframes; n++ {
		out = append(out, buildFrame(s, n, bps, channels, assign, block)...)
	}

	return out
}

// frameSource hands out the fuzz input's bytes as choices.
type frameSource struct {
	b []byte
	i int
}

func (s *frameSource) byte() byte {
	if s.i >= len(s.b) {
		return 0
	}

	v := s.b[s.i]
	s.i++

	return v
}

func (s *frameSource) intn(n int) int {
	if n <= 1 {
		return 0
	}

	v := int(s.byte())
	if n > 256 {
		v = v<<8 | int(s.byte())
	}

	return v % n
}

func (s *frameSource) pick(xs []int) int { return xs[s.intn(len(xs))] }

// signed returns a value of n bits, sign-extended.
func (s *frameSource) signed(n int) int64 {
	if n <= 0 {
		return 0
	}

	var u uint64
	for k := 0; k < n; k += 8 {
		u = u<<8 | uint64(s.byte())
	}

	if n < 64 {
		u &= 1<<uint(n) - 1
		if u&(1<<uint(n-1)) != 0 {
			u |= ^uint64(0) << uint(n)
		}
	}

	return int64(u)
}

// bitWriter writes bits most significant first.
type bitWriter struct {
	b   []byte
	cur byte
	n   uint
}

func (w *bitWriter) put(v uint64, n uint) {
	for n > 0 {
		k := min(n, 8-w.n)
		bits := byte(v>>(n-k)) & byte(1<<k-1)
		w.cur = w.cur<<k | bits
		w.n += k

		n -= k
		if w.n == 8 {
			w.b = append(w.b, w.cur)
			w.cur, w.n = 0, 0
		}
	}
}

func (w *bitWriter) putSigned(v int64, n uint) {
	if n == 0 {
		return
	}

	w.put(uint64(v)&(1<<n-1), n)
}

func (w *bitWriter) unary(q uint64) {
	for q >= 32 {
		w.put(0, 32)

		q -= 32
	}

	w.put(0, uint(q))
	w.put(1, 1)
}

func (w *bitWriter) align() {
	if w.n > 0 {
		w.put(0, 8-w.n)
	}
}

func crc8(b []byte) byte {
	var c byte
	for _, x := range b {
		c ^= x
		for k := 0; k < 8; k++ {
			if c&0x80 != 0 {
				c = c<<1 ^ 0x07
			} else {
				c <<= 1
			}
		}
	}

	return c
}

func crc16(b []byte) uint16 {
	var c uint16
	for _, x := range b {
		c ^= uint16(x) << 8
		for k := 0; k < 8; k++ {
			if c&0x8000 != 0 {
				c = c<<1 ^ 0x8005
			} else {
				c <<= 1
			}
		}
	}

	return c
}

var sampleSizeCode = map[int]uint64{8: 1, 12: 2, 16: 4, 20: 5, 24: 6, 32: 7}

func buildFrame(s *frameSource, num, bps, channels, assign, block int) []byte {
	w := &bitWriter{}
	w.put(0x3FFE, 14)
	w.put(0, 1) // reserved
	w.put(0, 1) // fixed block size

	if block <= 256 {
		w.put(0b0110, 4)
	} else {
		w.put(0b0111, 4)
	}

	w.put(uint64(s.pick([]int{0, 0, 0, 9})), 4) // sample rate: from STREAMINFO, or 44.1 kHz
	w.put(uint64(assign), 4)

	ss := uint64(0)
	if c, ok := sampleSizeCode[bps]; ok && s.intn(2) == 0 {
		ss = c
	}

	if s.intn(64) == 0 {
		ss = 3 // reserved
	}

	w.put(ss, 3)
	w.put(0, 1)           // reserved
	w.put(uint64(num), 8) // frame number, below 128: one UTF-8 byte

	if block <= 256 {
		w.put(uint64(block-1), 8)
	} else {
		w.put(uint64(block-1), 16)
	}

	header := w.b
	w.b = append(header, crc8(header))

	nch := channels
	if assign >= 8 && assign <= 10 {
		nch = 2
	}

	for c := 0; c < nch; c++ {
		sbps := bps
		if (assign == 8 || assign == 10) && c == 1 || assign == 9 && c == 0 {
			sbps++ // the side channel
		}

		buildSubframe(s, w, sbps, block)
	}

	w.align()
	sum := crc16(w.b)

	return append(w.b, byte(sum>>8), byte(sum))
}

func buildSubframe(s *frameSource, w *bitWriter, bps, block int) {
	wasted := 0
	if s.intn(8) == 0 {
		wasted = 1 + s.intn(bps+2) // sometimes at or above the depth
	}

	eff := bps - wasted
	if eff < 1 {
		eff = 1
	}

	kind := s.intn(4)
	order := 0

	switch kind {
	case 2:
		order = s.intn(5)
	case 3:
		order = 1 + s.intn(32)
	}

	if s.intn(4) != 0 && order >= block {
		// Usually a legal order; sometimes one the block cannot hold.
		if kind == 3 {
			order = max(1, block-1)
		} else {
			order = max(0, min(order, block-1))
		}
	}

	w.put(0, 1)

	switch kind {
	case 0:
		w.put(0b000000, 6)
	case 1:
		w.put(0b000001, 6)
	case 2:
		w.put(uint64(0b001000|order), 6)
	case 3:
		w.put(uint64(0b100000|(order-1)), 6)
	}

	if wasted > 0 {
		w.put(1, 1)
		w.unary(uint64(wasted - 1))
	} else {
		w.put(0, 1)
	}

	switch kind {
	case 0:
		w.putSigned(s.signed(eff), uint(eff))
		return
	case 1:
		for i := 0; i < block; i++ {
			w.putSigned(s.signed(eff), uint(eff))
		}

		return
	}

	for i := 0; i < order; i++ {
		w.putSigned(s.signed(eff), uint(eff))
	}

	if kind == 3 {
		prec := 1 + s.intn(15)

		code := uint64(prec - 1)
		if s.intn(32) == 0 {
			code = 15 // reserved
		}

		w.put(code, 4)

		shift := int64(s.intn(16))
		if s.intn(16) == 0 {
			shift = -1 - int64(s.intn(16)) // negative: refused
		}

		w.putSigned(shift, 5)

		for i := 0; i < order; i++ {
			w.putSigned(s.signed(prec), uint(prec))
		}
	}

	method := uint64(s.pick([]int{0, 0, 0, 1}))
	if s.intn(64) == 0 {
		method = 2 + uint64(s.intn(2)) // reserved
	}

	w.put(method, 2)

	if method > 1 {
		return
	}

	paramBits := uint(4 + method)
	escape := uint64(1)<<paramBits - 1

	part := s.intn(9)
	if s.intn(4) != 0 {
		// Usually partitions that divide the block and hold the warm-up.
		for part > 0 && (block%(1<<part) != 0 || block>>part <= order) {
			part--
		}
	}

	w.put(uint64(part), 4)

	for p := 0; p < 1<<part; p++ {
		count := block >> part
		if p == 0 {
			count -= order
		}

		if count < 0 {
			return
		}

		k := uint64(s.intn(int(escape)))
		if s.intn(16) == 0 {
			k = escape
		}

		w.put(k, paramBits)

		if k == escape {
			raw := s.intn(32)
			w.put(uint64(raw), 5)

			for i := 0; i < count; i++ {
				w.putSigned(s.signed(raw), uint(raw))
			}

			continue
		}

		for i := 0; i < count; i++ {
			v := int64(int8(s.byte()))
			u := uint64(v<<1) ^ uint64(v>>63) // zigzag
			w.unary(u >> k)

			if k > 0 {
				w.put(u&(1<<k-1), uint(k))
			}
		}
	}
}
