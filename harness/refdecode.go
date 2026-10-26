package main

import (
	"errors"
	"flag"
	"fmt"
	"math/big"
	"os"
)

// cmdRefDecode is an arbitrary-precision FLAC decoder written from RFC 9639,
// for triage: it decodes each frame with big integers, so no prediction or
// decorrelation can wrap, and reports every sample outside the stream's bit
// depth; such a stream is invalid, whatever a decoder makes of it (libFLAC
// 1.5.0 does not bound-check 32-bit streams). -raw writes the samples as
// libFLAC's raw output does (signed, little-endian, whole bytes). Slow:
// meant for small files and single frames. Exits 1 if a sample is out of
// range, 2 if the stream cannot be read.
func cmdRefDecode(args []string) error {
	fl := flag.NewFlagSet("refdecode", flag.ExitOnError)
	frames := fl.Int("frames", 0, "decode this many frames (0: all)")
	raw := fl.String("raw", "", "write the samples here, signed little-endian whole bytes")
	verbose := fl.Bool("verbose", false, "describe each subframe")
	_ = fl.Parse(args)
	if fl.NArg() != 1 {
		return errors.New("refdecode: one FLAC file")
	}
	data, err := os.ReadFile(fl.Arg(0))
	if err != nil {
		return err
	}
	out, outside, nframes, decErr := refDecode(data, *frames, *verbose, os.Stdout)
	if *raw != "" {
		if werr := os.WriteFile(*raw, out, 0o644); werr != nil {
			return werr
		}
	}
	if decErr != nil {
		fmt.Printf("frame %d: %v\n", nframes, decErr)
		os.Exit(2)
	}
	fmt.Printf("%d frames, %d samples outside the depth\n", nframes, outside)
	if outside > 0 {
		os.Exit(1)
	}
	return nil
}

type bigBits struct {
	b   []byte
	pos int // in bits
}

var errEndInFrame = errors.New("stream ends inside a frame")

func (r *bigBits) read(n int) (uint64, error) {
	var v uint64
	for range n {
		if r.pos>>3 >= len(r.b) {
			return 0, errEndInFrame
		}
		v = v<<1 | uint64(r.b[r.pos>>3]>>(7-uint(r.pos&7))&1)
		r.pos++
	}
	return v, nil
}

// readBig reads n bits as an unsigned big integer, for widths past 64.
func (r *bigBits) readBig(n int) (*big.Int, error) {
	v := new(big.Int)
	for range n {
		if r.pos>>3 >= len(r.b) {
			return nil, errEndInFrame
		}
		v.Lsh(v, 1)
		if r.b[r.pos>>3]>>(7-uint(r.pos&7))&1 == 1 {
			v.SetBit(v, 0, 1)
		}
		r.pos++
	}
	return v, nil
}

func (r *bigBits) signed(n int) (*big.Int, error) {
	if n == 0 {
		return new(big.Int), nil
	}
	v, err := r.readBig(n)
	if err != nil {
		return nil, err
	}
	if v.Bit(n-1) == 1 {
		v.Sub(v, new(big.Int).Lsh(big.NewInt(1), uint(n)))
	}
	return v, nil
}

func (r *bigBits) unary() (uint64, error) {
	var q uint64
	for {
		b, err := r.read(1)
		if err != nil {
			return 0, err
		}
		if b == 1 {
			return q, nil
		}
		q++
	}
}

func (r *bigBits) align()      { r.pos = (r.pos + 7) &^ 7 }
func (r *bigBits) atEnd() bool { return r.pos>>3 >= len(r.b) }

func refUTF8(r *bigBits) (uint64, error) {
	b0, err := r.read(8)
	if err != nil {
		return 0, err
	}
	if b0 < 0x80 {
		return b0, nil
	}
	if b0 == 0xFF {
		return 0, errors.New("frame number lead byte 0xFF")
	}
	n := 0
	for b0&(0x80>>uint(n)) != 0 {
		n++
	}
	v := b0 & (0xFF >> uint(n+1))
	for range n - 1 {
		c, err := r.read(8)
		if err != nil {
			return 0, err
		}
		v = v<<6 | c&0x3F
	}
	return v, nil
}

func refResiduals(r *bigBits, block, order int) ([]*big.Int, error) {
	method, err := r.read(2)
	if err != nil {
		return nil, err
	}
	if method > 1 {
		return nil, errors.New("reserved residual coding method")
	}
	pbits := 4
	if method == 1 {
		pbits = 5
	}
	porder, err := r.read(4)
	if err != nil {
		return nil, err
	}
	parts := 1 << porder
	if block%parts != 0 || block/parts < order {
		return nil, fmt.Errorf("partition order %d does not fit block %d and order %d", porder, block, order)
	}
	var res []*big.Int
	for p := range parts {
		n := block / parts
		if p == 0 {
			n -= order
		}
		k, err := r.read(pbits)
		if err != nil {
			return nil, err
		}
		if k == uint64(1<<pbits-1) {
			w, err := r.read(5)
			if err != nil {
				return nil, err
			}
			for range n {
				v, err := r.signed(int(w))
				if err != nil {
					return nil, err
				}
				res = append(res, v)
			}
			continue
		}
		for range n {
			q, err := r.unary()
			if err != nil {
				return nil, err
			}
			low, err := r.read(int(k))
			if err != nil {
				return nil, err
			}
			u := new(big.Int).SetUint64(q)
			u.Lsh(u, uint(k))
			u.Or(u, new(big.Int).SetUint64(low))
			// zigzag: (u >> 1) ^ -(u & 1)
			v := new(big.Int).Rsh(u, 1)
			if u.Bit(0) == 1 {
				v.Not(v)
			}
			res = append(res, v)
		}
	}
	return res, nil
}

var fixedCoeffs = [][]int64{{}, {1}, {2, -1}, {3, -3, 1}, {4, -6, 4, -1}}

// refSubframe decodes one subframe at depth bps over block samples; the
// samples come back shifted by the wasted bits, with a description.
func refSubframe(r *bigBits, bps, block int) ([]*big.Int, string, error) {
	pad, err := r.read(1)
	if err != nil {
		return nil, "", err
	}
	if pad != 0 {
		return nil, "", errors.New("subframe padding bit set")
	}
	t, err := r.read(6)
	if err != nil {
		return nil, "", err
	}
	wflag, err := r.read(1)
	if err != nil {
		return nil, "", err
	}
	wasted := 0
	if wflag == 1 {
		q, err := r.unary()
		if err != nil {
			return nil, "", err
		}
		wasted = int(q) + 1
	}
	if wasted >= bps {
		return nil, "", fmt.Errorf("%d wasted bits at or above the %d-bit depth", wasted, bps)
	}
	bps -= wasted
	var (
		s    []*big.Int
		desc string
	)
	predict := func(order int, coeff func(j int) *big.Int, shift uint) error {
		if order >= block {
			return fmt.Errorf("predictor order %d not below block %d", order, block)
		}
		for range order {
			v, err := r.signed(bps)
			if err != nil {
				return err
			}
			s = append(s, v)
		}
		res, err := refResiduals(r, block, order)
		if err != nil {
			return err
		}
		for _, rv := range res {
			sum := new(big.Int)
			for j := range order {
				sum.Add(sum, new(big.Int).Mul(coeff(j), s[len(s)-1-j]))
			}
			sum.Rsh(sum, shift)
			s = append(s, sum.Add(sum, rv))
		}
		return nil
	}
	switch {
	case t == 0:
		v, err := r.signed(bps)
		if err != nil {
			return nil, "", err
		}
		for range block {
			s = append(s, new(big.Int).Set(v))
		}
		desc = "constant"
	case t == 1:
		for range block {
			v, err := r.signed(bps)
			if err != nil {
				return nil, "", err
			}
			s = append(s, v)
		}
		desc = "verbatim"
	case t >= 8 && t <= 12:
		order := int(t - 8)
		if err := predict(order, func(j int) *big.Int { return big.NewInt(fixedCoeffs[order][j]) }, 0); err != nil {
			return nil, "", err
		}
		desc = fmt.Sprintf("fixed order %d", order)
	case t >= 32:
		order := int(t - 31)
		if order >= block {
			return nil, "", fmt.Errorf("LPC order %d not below block %d", order, block)
		}
		warm := make([]*big.Int, 0, order)
		for range order {
			v, err := r.signed(bps)
			if err != nil {
				return nil, "", err
			}
			warm = append(warm, v)
		}
		pcode, err := r.read(4)
		if err != nil {
			return nil, "", err
		}
		if pcode == 15 {
			return nil, "", errors.New("reserved LPC precision")
		}
		prec := int(pcode) + 1
		shiftV, err := r.signed(5)
		if err != nil {
			return nil, "", err
		}
		if shiftV.Sign() < 0 {
			return nil, "", errors.New("negative LPC shift")
		}
		shift := uint(shiftV.Int64())
		coeffs := make([]*big.Int, order)
		for j := range coeffs {
			if coeffs[j], err = r.signed(prec); err != nil {
				return nil, "", err
			}
		}
		s = warm
		res, err := refResiduals(r, block, order)
		if err != nil {
			return nil, "", err
		}
		for _, rv := range res {
			sum := new(big.Int)
			for j := range order {
				sum.Add(sum, new(big.Int).Mul(coeffs[j], s[len(s)-1-j]))
			}
			sum.Rsh(sum, shift)
			s = append(s, sum.Add(sum, rv))
		}
		desc = fmt.Sprintf("LPC order %d precision %d shift %d", order, prec, shift)
	default:
		return nil, "", fmt.Errorf("reserved subframe type %d", t)
	}
	lo := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), uint(bps-1)))
	hi := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(bps-1)), big.NewInt(1))
	bad := 0
	for _, v := range s {
		if v.Cmp(lo) < 0 || v.Cmp(hi) > 0 {
			bad++
		}
	}
	for i := range s {
		s[i] = new(big.Int).Lsh(s[i], uint(wasted))
	}
	desc = fmt.Sprintf("%s, wasted %d", desc, wasted)
	if bad > 0 {
		desc += fmt.Sprintf(", %d samples outside %d bits", bad, bps)
	}
	return s, desc, nil
}

// refDecode decodes up to nframes frames (0: all) and returns the raw PCM,
// the count of samples outside the depth, the frames decoded, and the error
// that stopped it.
func refDecode(data []byte, nframes int, verbose bool, log *os.File) (out []byte, outside, decoded int, err error) {
	if len(data) < 4 || string(data[:4]) != "fLaC" {
		return nil, 0, 0, errors.New("no fLaC signature")
	}
	p, sbps := 4, 0
	for {
		if p+4 > len(data) {
			return nil, 0, 0, errors.New("stream ends inside the metadata")
		}
		last, typ := data[p]&0x80 != 0, data[p]&0x7F
		n := int(data[p+1])<<16 | int(data[p+2])<<8 | int(data[p+3])
		if typ == 0 && p+4+18 <= len(data) {
			var x uint64
			for _, b := range data[p+4+10 : p+4+18] {
				x = x<<8 | uint64(b)
			}
			sbps = int(x>>36&0x1F) + 1
		}
		p += 4 + n
		if last {
			break
		}
	}
	if p > len(data) {
		return nil, 0, 0, errors.New("stream ends inside the metadata")
	}
	r := &bigBits{b: data, pos: p * 8}
	width := (sbps + 7) / 8
	mask := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(8*width)), big.NewInt(1))
	for !r.atEnd() && (nframes == 0 || decoded < nframes) {
		sync, err := r.read(14)
		if err != nil {
			return out, outside, decoded, err
		}
		if sync != 0x3FFE {
			return out, outside, decoded, errors.New("no frame sync")
		}
		if _, err := r.read(2); err != nil {
			return out, outside, decoded, err
		}
		var hdr [4]uint64
		for i := range hdr {
			if hdr[i], err = r.read(4); err != nil {
				return out, outside, decoded, err
			}
		}
		bscode, rcode, chanCode := hdr[0], hdr[1], hdr[2]
		sscode := hdr[3] >> 1 // 3 bits of sample size, then the reserved bit
		num, err := refUTF8(r)
		if err != nil {
			return out, outside, decoded, err
		}
		block := 0
		switch {
		case bscode == 1:
			block = 192
		case bscode == 6:
			v, err := r.read(8)
			if err != nil {
				return out, outside, decoded, err
			}
			block = int(v) + 1
		case bscode == 7:
			v, err := r.read(16)
			if err != nil {
				return out, outside, decoded, err
			}
			block = int(v) + 1
		case bscode >= 2 && bscode <= 5:
			block = 576 << (bscode - 2)
		case bscode >= 8:
			block = 256 << (bscode - 8)
		default:
			return out, outside, decoded, errors.New("reserved block size code")
		}
		switch rcode {
		case 12:
			_, err = r.read(8)
		case 13, 14:
			_, err = r.read(16)
		}
		if err != nil {
			return out, outside, decoded, err
		}
		bps, ok := map[uint64]int{0: sbps, 1: 8, 2: 12, 4: 16, 5: 20, 6: 24, 7: 32}[sscode]
		if !ok {
			return out, outside, decoded, errors.New("reserved sample size code")
		}
		if _, err := r.read(8); err != nil { // CRC-8, not checked
			return out, outside, decoded, err
		}
		if chanCode > 10 {
			return out, outside, decoded, errors.New("reserved channel assignment")
		}
		nch := int(chanCode) + 1
		if chanCode >= 8 {
			nch = 2
		}
		subs := make([][]*big.Int, 0, nch)
		for c := range nch {
			side := (chanCode == 8 || chanCode == 10) && c == 1 || chanCode == 9 && c == 0
			sbpsc := bps
			if side {
				sbpsc++
			}
			s, desc, err := refSubframe(r, sbpsc, block)
			if err != nil {
				return out, outside, decoded, err
			}
			subs = append(subs, s)
			if verbose {
				fmt.Fprintf(log, "frame %d (number %d) subframe %d: %s\n", decoded, num, c, desc)
			}
		}
		r.align()
		if _, err := r.read(16); err != nil { // CRC-16, not checked
			return out, outside, decoded, err
		}
		chs := subs
		switch chanCode {
		case 8:
			chs = [][]*big.Int{subs[0], make([]*big.Int, block)}
			for i := range block {
				chs[1][i] = new(big.Int).Sub(subs[0][i], subs[1][i])
			}
		case 9:
			chs = [][]*big.Int{make([]*big.Int, block), subs[1]}
			for i := range block {
				chs[0][i] = new(big.Int).Add(subs[0][i], subs[1][i])
			}
		case 10:
			chs = [][]*big.Int{make([]*big.Int, block), make([]*big.Int, block)}
			for i := range block {
				m := new(big.Int).Lsh(subs[0][i], 1)
				m.SetBit(m, 0, subs[1][i].Bit(0))
				chs[0][i] = new(big.Int).Rsh(new(big.Int).Add(m, subs[1][i]), 1)
				chs[1][i] = new(big.Int).Rsh(new(big.Int).Sub(m, subs[1][i]), 1)
			}
		}
		lo := new(big.Int).Neg(new(big.Int).Lsh(big.NewInt(1), uint(bps-1)))
		hi := new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), uint(bps-1)), big.NewInt(1))
		bad, firstI, firstC := 0, -1, -1
		for c, ch := range chs {
			for i, v := range ch {
				if v.Cmp(lo) < 0 || v.Cmp(hi) > 0 {
					if bad == 0 {
						firstI, firstC = i, c
					}
					bad++
				}
			}
		}
		if bad > 0 {
			outside += bad
			fmt.Fprintf(
				log,
				"frame %d: %d samples outside %d bits, the first at sample %d channel %d\n",
				decoded,
				bad,
				bps,
				firstI,
				firstC,
			)
		}
		for i := range block {
			for _, ch := range chs {
				v := new(big.Int).And(ch[i], mask)
				b := v.FillBytes(make([]byte, width))
				for k := width - 1; k >= 0; k-- {
					out = append(out, b[k])
				}
			}
		}
		decoded++
	}
	return out, outside, decoded, nil
}
