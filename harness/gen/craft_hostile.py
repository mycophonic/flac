"""One-defect streams for the decoder's refusals, with valid CRC-8 and CRC-16.

usage: craft_hostile.py <out dir>
Each file is a whole stream with exactly one defect, named with the issue it
reproduces; clean-16bit-two-frames.flac is the same stream with none, so a
refusal can be pinned on the defect and not on the builder. libFLAC refuses
every file but the clean one and the three of the open decisions below.
- int32-wasted16-depth16, int32-wasted24-depth16: wasted bits at or above
  a 16-bit depth (#16).
- int32-fixed4-block4: a predictor order equal to the block size (#18).
- wide-midside-wasted33, wide-midside-wasted40: the same on the 33-bit side
  channel of a 32-bit mid/side frame (#34).
- cut-in-padding: the stream ends inside a PADDING block (#19).
- frame-number-ff: a frame number whose UTF-8 lead byte is 0xFF (#23).
- second-streaminfo, seektable-stray-length: a second STREAMINFO block, a
  SEEKTABLE whose length is not whole points (#26).
- rate-change: a frame at 48 kHz in a 44.1 kHz stream (#27).
Three where libFLAC and the fork part ways by design, each an open decision:
- md5-wrong: the clean stream with a wrong STREAMINFO MD5; libFLAC refuses
  it, the fork does not check the MD5 (#20).
- no-flac-marker: the clean stream's two frames alone, no signature and no
  STREAMINFO; libFLAC syncs to the first frame, the fork refuses (#21).
- frame-number-overlong: frame number 0 coded in two bytes (C0 80); libFLAC
  accepts it, the fork refuses (#22).
Deterministic, standard library only.
"""
import os, sys
if len(sys.argv) != 2:
    sys.exit("usage: craft_hostile.py <out dir>")
out = sys.argv[1]
os.makedirs(out, exist_ok=True)
class W:
    def __init__(self): self.bits = []
    def put(self, v, n):
        for i in range(n - 1, -1, -1): self.bits.append((v >> i) & 1)
    def unary(self, q):
        self.bits.extend([0] * q); self.bits.append(1)
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
def block(kind, length, last, body=b""):
    return bytes([(0x80 if last else 0) | kind]) + length.to_bytes(3, "big") + body
def streaminfo_body(bps, nsamples, minbs=16, maxbs=16, rate=44100, channels=2, md5=0):
    w = W()
    w.put(minbs, 16); w.put(maxbs, 16); w.put(0, 24); w.put(0, 24)
    w.put(rate, 20); w.put(channels - 1, 3); w.put(bps - 1, 5); w.put(nsamples, 36)
    w.put(md5, 128)                                   # 0: MD5 unknown
    return w.bytes()
SS = {16: 0b100, 24: 0b110, 32: 0b111}
def frame(subframes, bps, blocksize, chan=0b0001, rate_code=0b1001, number=b"\x00"):
    """subframes: functions writing each channel's subframe into a W."""
    h = W()
    h.put(0b11111111111110, 14); h.put(0, 1); h.put(0, 1)
    h.put(0b0110, 4); h.put(rate_code, 4)             # 8-bit (blocksize-1) at the end
    h.put(chan, 4); h.put(SS[bps], 3); h.put(0, 1)
    for b in number: h.put(b, 8)                      # frame number, UTF-8 coded
    h.put(blocksize - 1, 8)
    hb = h.bytes()
    w = W()
    w.bits = [int(c) for b in hb + bytes([crc8(hb)]) for c in format(b, "08b")]
    for write in subframes: write(w)
    body = w.bytes()
    c = crc16(body)
    return body + bytes([c >> 8, c & 0xFF])
def constant(bps, value):
    def write(w):
        w.put(0, 1); w.put(0b000000, 6); w.put(0, 1); w.put(value & ((1 << bps) - 1), bps)
    return write
def verbatim_wasted(bps, wasted, blocksize):
    def write(w):
        w.put(0, 1); w.put(0b000001, 6); w.put(1, 1); w.unary(wasted - 1)
        width = bps - wasted if wasted < bps else 0
        for _ in range(blocksize): w.put(0, width)
    return write
def fixed(bps, order, warmup, residuals):
    def write(w):
        w.put(0, 1); w.put(0b001000 | order, 6); w.put(0, 1)
        for v in warmup: w.put(v & ((1 << bps) - 1), bps)
        w.put(0, 2); w.put(0, 4); w.put(0, 4)         # Rice, partition order 0, parameter 0
        for _ in range(residuals): w.put(1, 1)        # residual 0
    return write
def sig(*blocks): return b"fLaC" + b"".join(blocks)
SI16 = streaminfo_body(16, 32)
def two_frames(rate1=0b1001):
    f0 = frame([constant(16, 1000), constant(16, -1000)], 16, 16)
    f1 = frame([constant(16, 2000), constant(16, -2000)], 16, 16, rate_code=rate1, number=b"\x01")
    return f0 + f1
files = {
    "clean-16bit-two-frames": sig(block(0, 34, True, SI16)) + two_frames(),
    "int32-wasted16-depth16": sig(block(0, 34, True, streaminfo_body(16, 16))) +
        frame([verbatim_wasted(16, 16, 16), constant(16, 0)], 16, 16),
    "int32-wasted24-depth16": sig(block(0, 34, True, streaminfo_body(16, 16))) +
        frame([verbatim_wasted(16, 24, 16), constant(16, 0)], 16, 16),
    "int32-fixed4-block4": sig(block(0, 34, True, streaminfo_body(16, 4))) +
        frame([fixed(16, 4, [1, 2, 3, 4], 0), constant(16, 0)], 16, 4),
    "wide-midside-wasted33": sig(block(0, 34, True, streaminfo_body(32, 16))) +
        frame([constant(32, 0), verbatim_wasted(33, 33, 16)], 32, 16, chan=0b1010),
    "wide-midside-wasted40": sig(block(0, 34, True, streaminfo_body(32, 16))) +
        frame([constant(32, 0), verbatim_wasted(33, 40, 16)], 32, 16, chan=0b1010),
    "cut-in-padding": sig(block(0, 34, False, SI16), block(1, 1000, True)) + bytes(10),
    "frame-number-ff": sig(block(0, 34, True, streaminfo_body(16, 16))) +
        frame([constant(16, 1000), constant(16, -1000)], 16, 16, number=b"\xff"),
    "second-streaminfo": sig(block(0, 34, False, SI16), block(0, 34, True, SI16)) + two_frames(),
    "seektable-stray-length": sig(block(0, 34, False, SI16), block(3, 20, True, bytes(20))) + two_frames(),
    "rate-change": sig(block(0, 34, True, SI16)) + two_frames(rate1=0b1010),
    "md5-wrong": sig(block(0, 34, True, streaminfo_body(16, 32, md5=0x0123456789ABCDEF0123456789ABCDEF))) + two_frames(),
    "no-flac-marker": two_frames(),
    "frame-number-overlong": sig(block(0, 34, True, SI16)) +
        frame([constant(16, 1000), constant(16, -1000)], 16, 16, number=b"\xc0\x80") +
        frame([constant(16, 2000), constant(16, -2000)], 16, 16, number=b"\x01"),
}
for name, data in files.items():
    open(os.path.join(out, name + ".flac"), "wb").write(data)
print(len(os.listdir(out)))
