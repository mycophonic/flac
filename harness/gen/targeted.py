"""Small CRC-valid reproducers; none requests a large allocation.

usage: targeted.py <out dir>
- fixed-order4-block1.flac, lpc-order32-block1.flac: a 16-bit frame of one
  sample whose subframe declares fixed order 4 / LPC order 32, more warm-up
  samples than the block holds (issue #18; libFLAC refuses both).
- valid-32bit-midside.flac: a valid 32-bit mid/side constant frame,
  L=INT32_MAX, R=INT32_MIN, whose side needs 33 signed bits (issue #10);
  valid-32bit-midside.expected.raw is its PCM.
- reserved-meta.bin: a metadata header of reserved type 7.
Deterministic, standard library only.
"""
from pathlib import Path
import hashlib, struct, sys
if len(sys.argv) != 2:
    sys.exit("usage: targeted.py <out dir>")
out = Path(sys.argv[1])
out.mkdir(parents=True, exist_ok=True)
def crc(data, bits, poly):
    c = 0
    for b in data:
        c ^= b << (bits - 8)
        for _ in range(8): c = ((c << 1) ^ poly if c & (1 << (bits - 1)) else c << 1) & ((1 << bits) - 1)
    return c
si = struct.pack('>HH', 16, 4096) + bytes(6) + ((44100 << 44) | (1 << 41) | (15 << 36) | 1).to_bytes(8, 'big') + bytes(16)
h = bytes.fromhex('fff869180000'); h += bytes([crc(h, 8, 7)])
for kind, sub in [('fixed-order4-block1', bytes([0x18]) + bytes(8) + bytes(4)), ('lpc-order32-block1', bytes([0x7e]) + bytes(64) + bytes(32))]:
    frame = h + sub
    frame += crc(frame, 16, 0x8005).to_bytes(2, 'big')
    (out / (kind + '.flac')).write_bytes(b'fLaC' + bytes.fromhex('80000022') + si + frame)
# Reserved metadata type 7.
(out / 'reserved-meta.bin').write_bytes(bytes.fromhex('07000000'))
# Valid 32-bit mid/side constant frame: L=INT32_MAX, R=INT32_MIN.
# Side needs 33 signed bits, even though each output sample fits int32.
raw = (struct.pack('<ii', 2147483647, -2147483648)) * 16
si32 = struct.pack('>HH', 16, 16) + bytes(6) + ((44100 << 44) | (1 << 41) | (31 << 36) | 16).to_bytes(8, 'big') + hashlib.md5(raw).digest()
h32 = bytes.fromhex('fff869ae000f'); h32 += bytes([crc(h32, 8, 7)])
bitstring = '00000000' + '1' * 32 + '00000000' + format(4294967295, '033b')
bitstring += '0' * ((-len(bitstring)) % 8)
fr = h32 + int(bitstring, 2).to_bytes(len(bitstring) // 8, 'big')
fr += crc(fr, 16, 0x8005).to_bytes(2, 'big')
(out / 'valid-32bit-midside.flac').write_bytes(b'fLaC' + bytes.fromhex('80000022') + si32 + fr)
(out / 'valid-32bit-midside.expected.raw').write_bytes(raw)
