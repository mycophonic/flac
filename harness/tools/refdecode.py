"""An arbitrary-precision FLAC decoder written from RFC 9639, for triage.

usage: refdecode.py <file.flac> [--frames N] [--raw OUT] [--verbose]

Decodes each frame with Python integers, so no prediction or decorrelation
can wrap, and reports every sample outside the stream's bit depth: such a
stream is invalid, whatever a decoder makes of it (libFLAC 1.5.0 does not
bound-check 32-bit streams). --raw writes the samples as libFLAC's raw
output does (signed, little-endian, whole bytes), to compare with
`flac -d --force-raw-format --endian=little --sign=signed`. Slow: meant for
small files and single frames. Exits 1 if a sample is out of range, 2 if
the stream cannot be read. Standard library only.
"""
import sys

class Bits:
    def __init__(self, b, pos=0): self.b, self.pos = b, pos * 8
    def read(self, n):
        v = 0
        for _ in range(n):
            if self.pos >> 3 >= len(self.b): raise EOFError("stream ends inside a frame")
            v = (v << 1) | ((self.b[self.pos >> 3] >> (7 - (self.pos & 7))) & 1)
            self.pos += 1
        return v
    def signed(self, n):
        if n == 0: return 0
        v = self.read(n)
        return v - (1 << n) if v & (1 << (n - 1)) else v
    def unary(self):
        q = 0
        while self.read(1) == 0: q += 1
        return q
    def align(self): self.pos = (self.pos + 7) & ~7
    def at_end(self): return self.pos >> 3 >= len(self.b)

def utf8(br):
    b0 = br.read(8)
    if b0 < 0x80: return b0
    if b0 == 0xFF: raise ValueError("frame number lead byte 0xFF")
    n = 0
    while b0 & (0x80 >> n): n += 1
    v = b0 & (0xFF >> (n + 1))
    for _ in range(n - 1): v = (v << 6) | (br.read(8) & 0x3F)
    return v

def residual(br, block, order):
    method = br.read(2)
    if method > 1: raise ValueError("reserved residual coding method")
    pbits = 4 if method == 0 else 5
    porder = br.read(4)
    if block % (1 << porder) or (block >> porder) < order:
        raise ValueError("partition order %d does not fit block %d and order %d" % (porder, block, order))
    res = []
    for p in range(1 << porder):
        n = (block >> porder) - (order if p == 0 else 0)
        k = br.read(pbits)
        if k == (1 << pbits) - 1:
            w = br.read(5)
            res += [br.signed(w) for _ in range(n)]
        else:
            for _ in range(n):
                u = (br.unary() << k) | br.read(k)
                res.append((u >> 1) ^ -(u & 1))
    return res

FIXED = [[], [1], [2, -1], [3, -3, 1], [4, -6, 4, -1]]

def subframe(br, bps, block):
    if br.read(1): raise ValueError("subframe padding bit set")
    t = br.read(6)
    wasted = br.unary() + 1 if br.read(1) else 0
    if wasted >= bps: raise ValueError("%d wasted bits at or above the %d-bit depth" % (wasted, bps))
    bps -= wasted
    if t == 0:
        s, desc = [br.signed(bps)] * block, "constant"
    elif t == 1:
        s, desc = [br.signed(bps) for _ in range(block)], "verbatim"
    elif 8 <= t <= 12:
        order = t - 8
        if order >= block: raise ValueError("fixed order %d not below block %d" % (order, block))
        s = [br.signed(bps) for _ in range(order)]
        for r in residual(br, block, order):
            s.append(r + sum(c * s[-1 - j] for j, c in enumerate(FIXED[order])))
        desc = "fixed order %d" % order
    elif t >= 32:
        order = t - 31
        if order >= block: raise ValueError("LPC order %d not below block %d" % (order, block))
        s = [br.signed(bps) for _ in range(order)]
        pcode = br.read(4)
        if pcode == 15: raise ValueError("reserved LPC precision")
        prec, shift = pcode + 1, br.signed(5)
        if shift < 0: raise ValueError("negative LPC shift")
        coeffs = [br.signed(prec) for _ in range(order)]
        for r in residual(br, block, order):
            s.append(r + (sum(c * s[-1 - j] for j, c in enumerate(coeffs)) >> shift))
        desc = "LPC order %d precision %d shift %d" % (order, prec, shift)
    else:
        raise ValueError("reserved subframe type %d" % t)
    lo, hi = -(1 << (bps - 1)), (1 << (bps - 1)) - 1
    bad = sum(1 for v in s if not lo <= v <= hi)
    return [v << wasted for v in s], "%s, wasted %d%s" % (desc, wasted, ", %d samples outside %d bits" % (bad, bps) if bad else "")

def main():
    args = sys.argv[1:]
    path, nframes, raw, verbose = None, None, None, False
    while args:
        a = args.pop(0)
        if a == "--frames": nframes = int(args.pop(0))
        elif a == "--raw": raw = args.pop(0)
        elif a == "--verbose": verbose = True
        else: path = a
    if path is None: sys.exit(__doc__)
    b = open(path, "rb").read()
    if b[:4] != b"fLaC": print("no fLaC signature"); sys.exit(2)
    p, sbps, nch = 4, None, None
    while True:
        last, typ, n = b[p] & 0x80, b[p] & 0x7F, int.from_bytes(b[p + 1:p + 4], "big")
        if typ == 0:
            x = int.from_bytes(b[p + 4 + 10:p + 4 + 18], "big")
            sbps, nch = ((x >> 36) & 0x1F) + 1, ((x >> 41) & 7) + 1
        p += 4 + n
        if last: break
    br, out, outside, f = Bits(b, p), bytearray(), 0, 0
    width = (sbps + 7) // 8
    try:
        while not br.at_end() and (nframes is None or f < nframes):
            if br.read(14) != 0x3FFE: raise ValueError("no frame sync")
            br.read(2)
            bscode, rcode, chan, sscode = br.read(4), br.read(4), br.read(4), br.read(3)
            br.read(1)
            num = utf8(br)
            block = {1: 192}.get(bscode)
            if bscode == 6: block = br.read(8) + 1
            elif bscode == 7: block = br.read(16) + 1
            elif 2 <= bscode <= 5: block = 576 << (bscode - 2)
            elif bscode >= 8: block = 256 << (bscode - 8)
            if block is None: raise ValueError("reserved block size code")
            if rcode == 12: br.read(8)
            elif rcode in (13, 14): br.read(16)
            bps = {0: sbps, 1: 8, 2: 12, 4: 16, 5: 20, 6: 24, 7: 32}.get(sscode)
            if bps is None: raise ValueError("reserved sample size code")
            br.read(8)  # CRC-8, not checked
            if chan > 10: raise ValueError("reserved channel assignment")
            subs = []
            for c in range(chan + 1 if chan < 8 else 2):
                side = (chan in (8, 10) and c == 1) or (chan == 9 and c == 0)
                s, desc = subframe(br, bps + side, block)
                subs.append(s)
                if verbose: print("frame %d (number %d) subframe %d: %s" % (f, num, c, desc))
            br.align(); br.read(16)  # CRC-16, not checked
            if chan == 8: chs = [subs[0], [l - s for l, s in zip(subs[0], subs[1])]]
            elif chan == 9: chs = [[s + r for s, r in zip(subs[0], subs[1])], subs[1]]
            elif chan == 10:
                chs = [[], []]
                for m, s in zip(subs[0], subs[1]):
                    m = (m << 1) | (s & 1)
                    chs[0].append((m + s) >> 1); chs[1].append((m - s) >> 1)
            else: chs = subs
            lo, hi = -(1 << (bps - 1)), (1 << (bps - 1)) - 1
            bad = [(i, c) for c, ch in enumerate(chs) for i, v in enumerate(ch) if not lo <= v <= hi]
            if bad:
                outside += len(bad)
                print("frame %d: %d samples outside %d bits, the first at sample %d channel %d" % (f, len(bad), bps, bad[0][0], bad[0][1]))
            for i in range(block):
                for ch in chs: out += (ch[i] & ((1 << (8 * width)) - 1)).to_bytes(width, "little")
            f += 1
    except (ValueError, EOFError) as e:
        print("frame %d: %s" % (f, e))
        if raw: open(raw, "wb").write(out)
        sys.exit(2)
    if raw: open(raw, "wb").write(out)
    print("%d frames, %d samples outside the depth" % (f, outside))
    sys.exit(1 if outside else 0)

main()
