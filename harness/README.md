# flacharness

A differential harness for [mycophonic/flac](https://github.com/mycophonic/flac):
it decodes, seeks and fuzzes the decoder around it, and
holds every result to libFLAC's `flac` tool and to upstream
[mewkiz/flac](https://github.com/mewkiz/flac) v1.0.14, with ffmpeg as a
fourth implementation on the decode lane and Apple's CoreAudio as a fifth
on macOS.

It was written by Arsenie Coseac and the 1-bit bridge team
(https://github.com/acoseac) during their evaluation of this library, released
into the public domain under the Unlicense (`LICENSE`), and carried here as
sent, with its paths adjusted to this home and its Go formatted by this
repository's linters; `just harness` runs it. Its own module keeps upstream
out of the library's dependency graph, and a replace directive in it points
the fork at the checkout, so `go test ./...` in this directory runs the fuzz
targets' seeds against the code around it (the require line is nominal:
pinned, it had Renovate bumping it after every merge); the recipes never run
it, and it belongs in no gate.

## What it checks

| Step | What | Judged by |
|---|---|---|
| decode | every `.flac` file, frame by frame and sample by sample | `flac -d` (libFLAC); upstream decodes it too, and ffmpeg's decode, and on macOS CoreAudio's through AudioToolbox (agar's codec, cgo), are held to libFLAC's at the depths both write raw (agree, differ, or who refuses) |
| decode | each file's metadata through `Parse` and `NewSeek` as well (`New` skips the bodies it does not need) | the same |
| alloc | the bytes 19 metadata probes make the parsers allocate, through `meta.Parse`, `flac.Parse` and `flac.NewSeek`, measured as `runtime.MemStats.TotalAlloc`: 17 hostile blocks whose lengths claim more than is there, and 2 of many short strings, every byte present | the bound #33's own fuzz target holds: 4 MiB + 16 × the input |
| seek | `Stream.Seek` to the first and last frames, frame edges and seeded random samples, on one stream in a shuffled order, then the next 1000 samples; and past the end | `flac -d --skip=N --until=N+1000` |
| fuzz | `FuzzStream`: bytes as a stream. `FuzzFrame`: bytes driving a frame builder that seals each frame with valid CRCs, so mutations reach the subframe decoders. `FuzzMeta`: the metadata parsers, through `flac.Parse`, `flac.NewSeek` and `meta.Parse` | no panic, no input over 5 s, no allocation over the bound; a sample divergence from upstream is kept for triage |

The corpora are generated, not shipped:

| Corpus | Files | What |
|---|---|---|
| gen32 | 209 | 32-bit stereo signals (`wav32/`) encoded 11 ways by libFLAC and kept when `flac -t` accepts them: mid/side, left/side, side/right and independent, block sizes 16 to 4608, fixed and LPC predictors up to order 32, exhaustive search |
| bench | 3 | 16/44.1, 24/96 and 24/192 stereo, three seconds of tones and noise |
| targeted | 3 | #10's constant 32-bit mid/side frame; #18's warm-up frames on the int32 path |
| crafted | 6 | #34: the side channel of a 32-bit frame declares fixed order 4 or LPC order 32 on a one-sample block |
| control | 6 | the crafted frames at the smallest legal block, order + 1, with a side value only the 33-bit path holds; libFLAC decodes each to L = INT32_MAX, R = INT32_MIN |
| boundary | 6 | #34 at its edge: order 1 on a one-sample block, which libFLAC refuses |
| hostile | 14 | one defect each, for #16, #18, #19, #23, #26, #27 and #34; a clean twin; and the three places libFLAC and the fork part ways by decision (#20, #21, #22) |

`wav32/` holds the gen32 signals before libFLAC encodes them. The encoder
round trip the harness once ran (16, 24 and 32-bit stereo, four channel
assignments, four block sizes, analysis on and off, every stream held to
`flac -d`, the fork's own decoder and its STREAMINFO) is the library's own
test suite now: `TestEncoderMatrixAgainstTheReference` in the checkout.

The generators are the `gen` subcommand of the harness binary, Go with fixed
seeds and the standard library only. Two runs on one machine write
byte-identical files; `out/corpus.sha256` lists them. Everything
`run.sh` writes goes under the checkout's `.build/harness` (limen's build
directory, `--out` picks another); `out/` names it below. The
encoded corpora depend on the libFLAC version that writes them; the signals
do not.

## Requirements

- Go 1.25.8 or newer, the fork's `go` directive. Verified with go1.27.3, darwin/arm64.
- libFLAC's `flac` tool 1.5.0, on `PATH` or named by `FLAC`. Another version runs, with a warning: the verdicts here were checked against 1.5.0.
- ffmpeg, on `PATH` or named by `FFMPEG`, for the fourth implementation's column; optional, the column is empty without it.
- On macOS, a C compiler for cgo, for CoreAudio's column through AudioToolbox (agar's codec); built without cgo, or elsewhere, the column is empty.
- bash 3.2 or newer.
- Network on the first run, for the Go modules: the fork, mewkiz/flac v1.0.14 and its three dependencies.

## Running it

```sh
./run.sh                                  # the checkout this harness lives in
./run.sh --fork 0ebd7f68                  # any commit, branch or version, fetched as a Go module
./run.sh --fork-dir ../../flac            # another local checkout, through a replace directive
./run.sh --flac-dir ~/Music/flac          # also decode your own FLAC folder
./run.sh --fuzz-time 60 --jobs 4          # one minute of fuzzing over the three targets, four workers
```

`run.sh` leaves `go.mod` alone: it copies it to `out/build/fork.mod`
and builds with `-modfile`, after `go get github.com/mycophonic/flac@REV`
(`--fork`) or `go mod edit -replace github.com/mycophonic/flac=DIR`
(`--fork-dir`). By hand:

```sh
cd harness
cp go.mod /tmp/fork.mod && cp go.sum /tmp/fork.sum
go mod edit -modfile=/tmp/fork.mod -replace github.com/mycophonic/flac=/path/to/flac
go build -modfile=/tmp/fork.mod -o /tmp/flacharness .
go test -modfile=/tmp/fork.mod -run '^$' -fuzz '^FuzzFrame$' -fuzztime 60s .
```

On a commit before #37, which fixed #34 (`7039cb99` and earlier), the fuzzers
find #34's warm-up panic within seconds and stop there. To fuzz past
it, pass `--allow-panic decodeFixedWide,decodeFIRWide`: a panic raised in one
of those functions is then kept under `out/fuzz/allowed/` with its stream,
and a panic raised anywhere else still fails the target.

`FuzzMeta` holds every parse to #33's bound, 4 MiB + 16 bytes per input byte.
Before #39 a metadata string of up to 4096 bytes cost 4 KiB of heap however
short it was (see "Issues" below), so an input of many short strings failed
that bound at once; on such a commit `--meta-alloc-per-byte 1040` allows 4
KiB per four input bytes and lets the fuzzer look past it for allocations
that follow declared lengths.

Other options: `--out DIR`, `--label NAME`, `--skip STEP` (gen, decode, alloc,
seek, fuzz), `--fuzz-targets LIST`, `--fuzz-workers N`, `--fuzz-minimize D`,
`--seed-dir DIR` (more fuzz seeds).
`./run.sh --help` lists them all.

## Reading the output

`out/summary.txt` is the short form. `out/summary.json` holds the same, and
every verdict in a flat map. Each decoder's verdict on each file, against
libFLAC:

| Verdict | Meaning |
|---|---|
| match | both accept the stream and every sample is equal |
| mismatch | a sample differs, a sample is outside the depth, a frame is malformed, or the sample counts differ; `first_diff` names the frame, sample and channel |
| panic | the decoder panicked; the stack is in `decode.jsonl` |
| timeout | no result within the deadline |
| extra_error | the decoder refuses a stream libFLAC accepts |
| missed_error | the decoder accepts a stream libFLAC refuses |
| md5_only | libFLAC refuses for the STREAMINFO MD5 alone and every sample is equal (neither Go decoder checks the MD5: #20) |
| both_refuse | both refuse, and the samples before the refusals agree |

Under "Issues", the summary gives each issue the harness has a reproducer for
a state: fixed, not fixed, changed (no longer the reported failure, but not
libFLAC's decision either), or not tested, with the evidence. The three
decisions read their decided state while the fork keeps its choice: #22
"correct, libFLAC lenient" (RFC 9639 section 9.1.5 decodes the frame number
by RFC 3629 section 3, which refuses the overlong form), #20 and #21
"decided" (verification is opt-in, #56; a frame stream gets its own entry
point, #55); a move toward libFLAC reads "agrees with libFLAC".

Expected on every commit: upstream refuses 32-bit frames, so its
`extra_error` on gen32 and control is no finding. libFLAC's raw output takes
8, 16, 24 and 32 bits only; for other depths the harness reads its AIFF output
instead, which carries the same samples. libFLAC 1.5.0 does not bound-check
samples at 32 bits, so a fuzzed 32-bit stream whose prediction overflows may
decode differently under each decoder; `FuzzFrame` keeps only divergences at
24 bits or less whose samples fit their depth.

To compare two runs, for example main and a pull request:

```sh
out/build/flacharness compare out-main/summary.json out-pr/summary.json
```

It prints every verdict that differs (fuzz results only with `-only fuzz/`),
and exits 1 if any does.

The other files: `decode.jsonl`, `alloc.jsonl` and `seek.jsonl` hold one
record per file, probe or seek.
`fuzz/<Target>.log` is Go's log, `fuzz/crashers/` the failing inputs (moved
out of `testdata`), `fuzz/observations/` the divergences and
`fuzz/allowed/` the allowed panics; `fuzz/triage.jsonl` replays each through
all three decoders, with the stream as hex. `out/build/flacharness triage
-dir DIR FILE...` replays any file.

`out/build/flacharness refdecode FILE` decodes a small stream at arbitrary
precision (big integers, from RFC 9639) and reports every sample outside the
depth: a quick way to tell an invalid stream from a decoder bug when libFLAC
accepts both. `-raw OUT` writes the samples as `flac -d --force-raw-format`
does; it matches libFLAC byte for byte on valid files, including 32-bit
mid/side LPC.

## Runtime

On an Apple M3 Pro, with the modules and the build cached: 4 s to generate
the corpora and about 10 s more for decode, the probes and seek; then `--fuzz-time`, 1500 s unless set, split over the targets.
The first run adds the module downloads and a cold build, about a minute.
Your own folder adds roughly what decoding it takes, twice: 300 MB of FLAC
took 7 s at `--jobs 2`.

## Layout

```
run.sh               the entry point
*.go                 the Go module: gen, decode, alloc, seek, triage, refdecode, summarize, compare, and the fuzz targets
```

Released into the public domain under the Unlicense by its authors, as mycophonic/flac is; see `LICENSE`.
