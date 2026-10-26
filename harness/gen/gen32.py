"""Write stereo WAV signals that make libFLAC choose a 33-bit side channel.

usage: gen32.py <out dir>

The signals behind the 32-bit corpus: full-scale anti-phase, correlated,
noise, square, ramp, low-level, wasted bits, 96 kHz, and short files of 1 to
33 samples. Deterministic: one fixed seed, standard library only.
"""
import math, random, struct, sys, wave, os
args = sys.argv[1:]
bits = 32
if len(args) != 1:
    sys.exit("usage: gen32.py <out dir>")
out = args[0]
os.makedirs(out, exist_ok=True)
MAX, MIN = 2**(bits - 1) - 1, -2**(bits - 1)
rng = random.Random(20261024)
def clamp(v): return max(MIN, min(MAX, int(v)))
def pack(l, r): return struct.pack("<ii", l, r)
def write(name, frames, rate=44100):
    with wave.open(os.path.join(out, name + ".wav"), "wb") as w:
        w.setnchannels(2); w.setsampwidth(4); w.setframerate(rate)
        w.writeframes(b"".join(pack(l, r) for l, r in frames))
N = 9000
sine = [math.sin(2 * math.pi * 997 * i / 44100) for i in range(N)]
write("const-extremes", [(MAX, MIN)] * 4096)
write("anti-sine-full", [(clamp(s * MAX), clamp(-s * MAX)) for s in sine])
write("corr-sine-full", [(clamp(s * MAX), clamp(s * MAX - 3 + rng.randint(-2, 2))) for s in sine])
write("noise-full", [(rng.randint(MIN, MAX), rng.randint(MIN, MAX)) for _ in range(N)])
n = [rng.randint(MIN // 2, MAX // 2) for _ in range(N)]
write("noise-corr", [(clamp(2 * x), clamp(2 * x + rng.randint(-1000, 1000))) for x in n])
write("square-extremes", [((MAX, MIN) if (i // 37) % 2 else (MIN, MAX)) for i in range(N)])
write("ramp-inverse", [(clamp(MIN + i * (2**bits // N)), clamp(-(MIN + i * (2**bits // N)) - 1)) for i in range(N)])
write("low-level", [(clamp(s * 1000), clamp(-s * 900)) for s in sine])
waste = 0xFF
write("wasted-bits", [(clamp(s * MAX) & ~waste, clamp(-s * MAX) & ~waste) for s in sine])
write("hires-96k-anti", [(clamp(s * MAX), clamp(-s * MAX * 0.999)) for s in sine], rate=96000)
for k in (1, 2, 3, 4, 5, 15, 16, 17, 33):
    write("short-%d" % k, [(MAX - i, MIN + i) for i in range(k)])
print(len(os.listdir(out)))
