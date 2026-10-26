"""Twins of craft_wide.py's frames, with valid CRC-8 and CRC-16.

usage: craft_control.py <control dir> [<boundary dir>]

<control dir>: wide-<assignment>-<fixed4|lpc32>-block<order+1>.flac, the
  same 32-bit stereo frame and the same side predictor (fixed order 4, LPC
  order 32) at the smallest block the order allows, order + 1 samples. The
  side channel carries 2^32-1, a value only the 33-bit path holds, and every
  file decodes to L=INT32_MAX, R=INT32_MIN under libFLAC: a valid twin of
  each crafted file.
<boundary dir>: wide-<assignment>-<fixed1|lpc1>-block1.flac, order 1 on a
  one-sample block: the order equals the block size, which libFLAC refuses
  (the boundary of issue #34's refusal).
Deterministic, standard library only.
"""
import sys, os
if len(sys.argv) not in (2, 3):
    sys.exit("usage: craft_control.py <control dir> [<boundary dir>]")
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
def streaminfo(nsamples=1, md5=0):
    w = W()
    w.put(1, 1); w.put(0, 7); w.put(34, 24)         # last block, STREAMINFO, length
    w.put(16, 16); w.put(4096, 16); w.put(0, 24); w.put(0, 24)
    w.put(44100, 20); w.put(1, 3); w.put(31, 5); w.put(nsamples, 36)
    w.put(md5, 128)
    return b"fLaC" + w.bytes()
def header(chan, block):
    h = W()
    h.put(0b11111111111110, 14); h.put(0, 1); h.put(0, 1)
    h.put(0b0110, 4); h.put(0b1001, 4)                # 8-bit (blocksize-1); 44.1 kHz
    h.put(chan, 4); h.put(0b111, 3); h.put(0, 1)      # channel assignment; 32-bit
    h.put(0, 8)                                       # frame number 0
    h.put(block - 1, 8)                               # block size
    hb = h.bytes()
    w = W()
    w.bits = [int(c) for b in hb + bytes([crc8(hb)]) for c in format(b, "08b")]
    return w
def seal(w):
    body = w.bytes()
    c = crc16(body)
    return body + bytes([c >> 8, c & 0xFF])
def boundary_frame(chan, side_index, kind, order):
    """The original control frame: order 1 on a block of 1."""
    w = header(chan, 1)
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
    return seal(w)
# L=INT32_MAX and R=INT32_MIN in each assignment: the side is L-R = 2^32-1.
SIDE = 2**32 - 1
CONST = {0b1000: 0x7FFFFFFF,      # left/side: channel 0 is left
         0b1001: 0x80000000,      # side/right: channel 1 is right (INT32_MIN)
         0b1010: 0xFFFFFFFF}      # mid/side: channel 0 is mid, (L+R)>>1 = -1
def control_frame(chan, side_index, kind, order):
    block = order + 1
    w = header(chan, block)
    for ch in range(2):
        if ch != side_index:
            w.put(0, 1); w.put(0b000000, 6); w.put(0, 1)   # constant, 32 bits
            w.put(CONST[chan], 32)
            continue
        w.put(0, 1)
        if kind == "fixed":
            w.put(0b001000 | order, 6)
        else:
            w.put(0b100000 | (order - 1), 6)
        w.put(0, 1)
        for _ in range(order): w.put(SIDE, 33)            # warm-up samples, 33 bits
        if kind == "lpc":
            w.put(14, 4); w.put(0, 5)                     # precision 15, shift 0
            w.put(1, 15)                                  # predict the previous sample
            for _ in range(order - 1): w.put(0, 15)
        w.put(0, 2); w.put(0, 4); w.put(0, 4)             # Rice, order 0, param 0
        for _ in range(block - order): w.put(1, 1)        # residual 0
    return seal(w)
import hashlib, struct
cases = [("leftside", 0b1000, 1), ("sideright", 0b1001, 0), ("midside", 0b1010, 1)]
control = sys.argv[1]
os.makedirs(control, exist_ok=True)
for name, chan, side in cases:
    for kind, order in (("fixed", 4), ("lpc", 32)):
        block = order + 1
        pcm = struct.pack("<ii", 2147483647, -2147483648) * block
        md5 = int.from_bytes(hashlib.md5(pcm).digest(), "big")
        p = os.path.join(control, f"wide-{name}-{kind}{order}-block{block}.flac")
        open(p, "wb").write(streaminfo(block, md5) + control_frame(chan, side, kind, order))
if len(sys.argv) == 3:
    boundary = sys.argv[2]
    os.makedirs(boundary, exist_ok=True)
    for name, chan, side in cases:
        for kind, order in (("fixed", 1), ("lpc", 1)):
            p = os.path.join(boundary, f"wide-{name}-{kind}{order}-block1.flac")
            open(p, "wb").write(streaminfo() + boundary_frame(chan, side, kind, order))
print(len(os.listdir(control)))
