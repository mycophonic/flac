"""Craft 32-bit stereo FLAC frames whose side subframe has a predictor order
above the block size, with valid CRC-8 and CRC-16 (issue #34).

usage: craft_wide.py <out dir>
Six files, wide-<assignment>-<fixed4|lpc32>-block1.flac: one frame of one
sample in left/side, side/right and mid/side; the side channel (33 bits, the
wide decode path) declares fixed order 4 or LPC order 32. libFLAC refuses
every one. Deterministic, standard library only.
"""
import sys, os
if len(sys.argv) != 2:
    sys.exit("usage: craft_wide.py <out dir>")
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
class W:
    def __init__(self): self.bits = []
    def put(self, v, n):
        for i in range(n - 1, -1, -1): self.bits.append((v >> i) & 1)
    def pad(self):
        while len(self.bits) % 8: self.bits.append(0)
    def bytes(self):
        self.pad()
        return bytes(int("".join(map(str, self.bits[i:i + 8])), 2) for i in range(0, len(self.bits), 8))
def crc8(b):
    c = 0
    for x in b:
        c ^= x
        for _ in range(8): c = ((c << 1) ^ 0x07) & 0xFF if c & 0x80 else (c << 1) & 0xFF
    return c
def crc16(b):
    c = 0
    for x in b:
        c ^= x << 8
        for _ in range(8): c = ((c << 1) ^ 0x8005) & 0xFFFF if c & 0x8000 else (c << 1) & 0xFFFF
    return c
def streaminfo():
    w = W()
    w.put(1, 1); w.put(0, 7); w.put(34, 24)         # last block, STREAMINFO, length
    w.put(16, 16); w.put(4096, 16); w.put(0, 24); w.put(0, 24)
    w.put(44100, 20); w.put(1, 3); w.put(31, 5); w.put(1, 36)
    w.put(0, 128)
    return b"fLaC" + w.bytes()
def frame(chan, side_index, kind, order):
    h = W()
    h.put(0b11111111111110, 14); h.put(0, 1); h.put(0, 1)
    h.put(0b0110, 4); h.put(0b1001, 4)                # 8-bit (blocksize-1); 44.1 kHz
    h.put(chan, 4); h.put(0b111, 3); h.put(0, 1)      # channel assignment; 32-bit
    h.put(0, 8)                                       # frame number 0
    h.put(0, 8)                                       # block size 1
    hb = h.bytes()
    w = W()
    w.bits = [int(c) for b in hb + bytes([crc8(hb)]) for c in format(b, "08b")]
    for ch in range(2):
        bps = 33 if ch == side_index else 32
        if ch != side_index:
            w.put(0, 1); w.put(0b000000, 6); w.put(0, 1)   # constant
            w.put(0x7FFFFFFF, 32)
            continue
        w.put(0, 1)
        if kind == "fixed":
            w.put(0b001000 | order, 6)
        else:
            w.put(0b100000 | (order - 1), 6)
        w.put(0, 1)
        for _ in range(order): w.put(1, bps)              # warm-up samples
        if kind == "lpc":
            w.put(14, 4); w.put(0, 5)                     # precision 15, shift 0
            for _ in range(order): w.put(1, 15)
        w.put(0, 2); w.put(0, 4); w.put(0, 4)             # Rice, order 0, param 0
    body = w.bytes()
    c = crc16(body)
    return body + bytes([c >> 8, c & 0xFF])
cases = [("leftside", 0b1000, 1), ("sideright", 0b1001, 0), ("midside", 0b1010, 1)]
for name, chan, side in cases:
    for kind, order in (("fixed", 4), ("lpc", 32)):
        p = os.path.join(out, f"wide-{name}-{kind}{order}-block1.flac")
        open(p, "wb").write(streaminfo() + frame(chan, side, kind, order))
print(len(os.listdir(out)))
