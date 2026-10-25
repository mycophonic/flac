"""Three-second stereo bench fixtures (16/44.1, 24/96, 24/192): two tones per
channel plus seeded low-level broadband noise, encoded with libFLAC -8.

usage: generate.py <out dir>
Writes bench-<bits>-<rate>-stereo.raw (the PCM), .flac, and synthetic.json.
Deterministic: fixed seeds, standard library only. Set FLAC to use a flac
binary that is not first on PATH.
"""
import math, random, pathlib, subprocess, hashlib, json, os, sys
if len(sys.argv) != 2:
    sys.exit("usage: generate.py <out dir>")
out = pathlib.Path(sys.argv[1])
out.mkdir(parents=True, exist_ok=True)
flac = os.environ.get("FLAC", "flac")
records = []
for rate, bits in [(44100, 16), (96000, 24), (192000, 24)]:
    rng = random.Random(5388 + rate)
    data = bytearray()
    for i in range(rate * 3):
        t = i / rate
        for c in range(2):
            v = (.43 * math.sin(2 * math.pi * (997 + c * 113) * t) + .21 * math.sin(2 * math.pi * (71 + c * 19) * t) + .06 * (rng.random() * 2 - 1))
            x = round(v * ((1 << (bits - 1)) - 1))
            data.extend(x.to_bytes(bits // 8, 'little', signed=True))
    stem = out / f'bench-{bits}-{rate}-stereo'
    raw = stem.with_suffix('.raw')
    raw.write_bytes(data)
    subprocess.run([flac, '--force', '--silent', '-8', '--force-raw-format', '--endian=little', '--sign=signed',
                    '--channels=2', f'--bps={bits}', f'--sample-rate={rate}', '-o', str(stem.with_suffix('.flac')), str(raw)],
                   check=True)
    records.append(dict(file=stem.name + '.flac', rate=rate, bits=bits, seconds=3, pcm_md5=hashlib.md5(data).hexdigest(),
                        pcm_bytes=len(data), flac_bytes=stem.with_suffix('.flac').stat().st_size))
(out / 'synthetic.json').write_text(json.dumps(records, indent=2) + '\n')
