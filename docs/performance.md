# Decoder performance

What was measured, what was tried, and what was rejected, so the next
attempt starts where this one stopped. The numbers below were taken through
saprobe-flac's streaming wrapper in early 2026 on an Apple M1 Max, with the
fork as it was then; the README's figures against the fork point are the
current measure of the decoder itself, and `BenchmarkNewSeek_*` and
`BenchmarkFrameParse` are the suite's own.

## Where the time goes

Benchmark file: a 44.1 kHz, 16-bit stereo jazz recording of 11:22 (54 MB of
FLAC), median of ten decodes.

| Decoder | Median | Relative |
|---|---|---|
| this decoder, through saprobe-flac | 1.382 s | 1.0× |
| libFLAC (`flac -d`) | 740 ms | 1.87× faster |
| CoreAudio | 771 ms | 1.79× faster |
| ffmpeg | 215 ms | 6.43× faster |

CPU profile of the decoder path (`parseSubframeInto`, 11.03 s cumulative
across the ten iterations):

| Function | Flat | Share | Note |
|---|---|---|---|
| decodeLPC | 3.61 s | 32.7 % | a multiply-accumulate chain per sample, latency-bound |
| ReadRice | 3.17 s | 28.7 % | bit manipulation and buffer management for the k low bits |
| ReadUnary | 1.71 s | 15.5 % | word-at-a-time zero scanning |
| decodeRicePart | 0.46 s | 4.2 % | the per-sample loop, error checks, the store |
| crc16.Update | 0.46 s | 4.2 % | per-byte CRC on 1 to 8-byte chunks |
| consumeBytes | 0.33 s | 3.0 % | slice creation, flag checks, CRC dispatch |
| LeadingZeros8 | 0.27 s | 2.4 % | the bit scan in ReadUnary |
| memmove | 0.23 s | 2.1 % | buffer shifts |
| needBytes, available | 0.44 s | 4.0 % | the buffer checks |

The residual pipeline (ReadRice, ReadUnary, consumeBytes, the CRC, the
buffer checks and the bit scan) is 62 % of the decoder's time: every Rice
residual, thousands per subframe, costs about six calls and two CRC updates
to extract six to twelve bits. decodeLPC is the other third and is bound by
the latency of its multiply-accumulate chain, not by instruction count.

## What real files ask for

Encoders use LPC prediction for about 99 % of subframes; fixed prediction is
under 1 %. The order distribution depends on the content:

| Material | Orders, by share of subframes |
|---|---|
| CD jazz quartet | 12 (76 %), 10 (20 %), 7 (4 %) |
| CD jazz, long | 12 (25 %), 8 (19 %), 6 (11 %), 11 (10 %), 7 (9 %) |
| CD vinyl rip, two pianos | 8 (88 %), 7 (11 %), 6 (2 %) |
| 96/24 vinyl rip | 8 (48 %), 7 (22 %), 6 (12 %), 5 (10 %) |

Orders 8 and 12 together cover 70 to 95 % of subframes.

## Tried and rejected

**Unrolling decodeLPC for orders 8 and 12.** Explicit multiply-accumulate
chains with the coefficients in registers, to cut the instruction count
about 2.7×. The unrolled versions were about 15 % slower than the generic
loop (28.8 µs against 25.0 µs per 4096 samples at order 8; 34.1 against
30.2 at order 12). The computation is bound by the latency of the chain:
each sample needs eight or twelve dependent multiply-accumulates at three
cycles each, a floor the loop's extra instructions hide behind on the
out-of-order engine. The larger code hurt the instruction cache and the
register allocation instead. ALAC's integer specializations pay off because
their operations are single-cycle; FLAC's do not.

**CRC-16 slicing by four.** Four bytes per iteration over four tables, for
three to four times the CRC throughput. Not reachable: the CRC is fed from
consumeBytes in chunks of one to eight bytes, rarely the four the slicing
needs.

**A 64-bit shift-register bit reader.** Replacing the byte buffer with its
0 to 7-bit remainder by a 64-bit register, so ReadUnary scans the register
with LeadingZeros64 and ReadRice extracts k bits with one shift and mask.
17 % slower (1.621 s against 1.382 s). ReadRice and ReadUnary did shed
1.7 s, but consume, ensureBits, refill and flushCRC took on 5 s: every
`consume(n)` mutates the register and the CRC ring offset, a serial
dependency chain the out-of-order engine cannot overlap, where the byte
buffer's independent loads could be. The per-sample work is the same
21 ns either way. A variant that deferred the CRC to refill boundaries
gained nothing.

**Profile-guided optimization.** A CPU profile fed to the compiler,
expecting better inlining of the deep call chain. No change: 1.38 s with
and without. The compiler inlined the whole hot path, ReadUnary into
ReadRice into decodeRicePart into decodeResiduals, decodeLPC into
decodeFIR, parseSubframeInto into parseSubframes, and the time was the
same, which shows the call overhead was never the cost. This also retired
the idea of a bulk Rice partition decode, which targeted that overhead.

**A larger buffer under NewSeek, or none.** Measured in #15 on the largest
IETF file: 4 KiB, 16 KiB, 64 KiB and 256 KiB buffers decode and seek the
same, and no buffer costs 3.6× over NewSeek and a full decode together, the
whole difference in NewSeek (about 6× at open: 63 to 68 ms against 389 ms),
because the metadata parsers read a field at a time (a 16 MB seek table is
932,067 reads of 18 bytes). Frame data is read in 4 KiB fills either way.

**Not pursued.** SIMD for LPC: each sample depends on the previous one, so
there is no cross-sample parallelism to vectorize. Fixed-prediction
specializations: under 1 % of subframes.

## What remains

- **Frame-level parallelism.** Frames are independent; decoding several at
  once scales throughput with cores without changing per-frame latency. An
  architectural change for a caller, not for the decoder: frame boundary
  detection and ordered reassembly.
- **Assembly for the Rice inner loop.** Hand-written arm64 and amd64 for
  ReadRice and ReadUnary, past the calling convention and the bounds
  checks, could approach libFLAC. High maintenance; the only route left to
  the remaining 1.87×.
