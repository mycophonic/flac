# flac

A FLAC decoder and encoder in Go, with no dependencies. It is a fork of
[mewkiz/flac](https://github.com/mewkiz/flac) that reads 32-bit streams, refuses
what the format forbids instead of panicking or guessing, seeks to the right
frame, and decodes faster. It is the decoder behind
[saprobe](https://github.com/mycophonic/saprobe)'s FLAC support; anything the
upstream maintainers want back is theirs to take.

The library is a release candidate (`v1.0.0-rc2`). The API is upstream's, with
the differences described below. Pure Go, Go 1.25, public domain (Unlicense, as
upstream). Three packages make it up: `flac` holds streams, with their metadata,
frames and seeking; `flac/frame` the audio frames and their subframes;
`flac/meta` the metadata blocks (STREAMINFO, VORBIS_COMMENT, PICTURE, SEEKTABLE,
CUESHEET, APPLICATION, PADDING). The reference is
[pkg.go.dev/github.com/mycophonic/flac](https://pkg.go.dev/github.com/mycophonic/flac).

```go
import "github.com/mycophonic/flac"
```

## Usage

```go
stream, err := flac.Open("song.flac") // ParseFile also parses the metadata
if err != nil {
	return err
}
defer stream.Close()

for {
	f, err := stream.ParseNext()
	if errors.Is(err, io.EOF) {
		break
	}
	if err != nil {
		return err
	}
	// f.Subframes[c].Samples holds channel c's samples, as int32, for every
	// bit depth up to 32; a decorrelated stereo frame is already undone.
}
```

`flac.NewSeek` opens a stream for `Seek`; `flac.NewEncoder` writes one.
`frame.ParseInto` and `frame.ParseReuse` decode into buffers the caller owns.

## What differs from upstream

In brief; the fork's history and its
[issues](https://github.com/mycophonic/flac/issues?q=is%3Aissue) carry the
detail, and each defect found has an issue, closed once fixed.

**Decoding.** 32-bit streams decode, the 33-bit side channel of a decorrelated
stereo frame included, on a wide path with 64-bit mid/side arithmetic. Every
frame is held to STREAMINFO: channel count, bit depth, sample rate and the total
sample count, and `SampleNumber` follows the stream's block sizes.

**Performance.** Half of the fork's work is the decoder's speed. The frame I/O
chain is one CRC reader where it was two `io.TeeReader` layers, with CRC-8
switched off once the header is verified, so the subframe hot path computes
nothing it does not need; the bit reader and the unary decode are tighter;
verbatim subframes at aligned bit depths are read whole; samples are written by
index into buffers sized once; mid/side decorrelation runs in `int64`; and
`ParseInto` and `ParseReuse` let a caller decode into buffers it owns, so a
steady-state decode allocates almost nothing. Measured against the fork point
(`f26319f`) on one Apple silicon machine, decoding three IETF conformance files
end to end (16-bit at block size 1937, 24-bit at the default block size and at
16384), medians of eight runs with go1.27: 103 ms to 56 ms per pass,
15.8 MB to 294 KB allocated, 8440 to 2258 allocations. The CRC reader alone was
measured at a quarter off the decode time when it landed (a864bd7).

**Refusals.** The decoder refuses what the format forbids rather than guess at
it. A subframe with wasted bits at or above the bit depth, or a predictor order
at or above the block size, is refused on both decode paths; so are a frame
number with a 0xFF lead byte or past 36 bits, a Rice partition count the block
size does not divide, and a garbage block type. A stream cut inside its metadata
reports the cut rather than opening as a clean, empty stream; a second
STREAMINFO and a seek table of a stray length are refused. The metadata parsers
allocate within the block, never a declared length, so a hundred-byte stream
cannot ask for gigabytes and a short string costs its length. Zero bytes before
a frame's sync code, undeclared padding, are scanned past, up to 32 MB; an
ID3v2 tag before the stream is skipped, as before.

**Seeking.** `Seek` lands on the frame holding the sample: the buffered reader
serves fresh bytes after a direct read, sample numbers come from the stream, and
a target before the first seek point seeks to the start.

**Encoding.** The encoder refuses a frame the stream cannot carry, a predictor
precision or shift out of range, a Rice partition order or count the block size
does not take, a missing Rice table, a sample beyond the bit depth, instead of
writing a stream libFLAC rejects. It writes a decorrelated 32-bit frame with
independent channels, since the side channel would not fit, writes 4-bit
samples with the header deferring to STREAMINFO, and sets each frame's number.
`Close` rewrites STREAMINFO with the minimum block size excluding the last
block, the measured frame sizes, and valid sizes for a stream of one short
block.

**Divergences from libFLAC, under decision.** The STREAMINFO MD5 is not checked
at the end of a stream (#20); a stream without the `fLaC` marker is not synced
to its first frame (#21); an overlong UTF-8 frame number is refused where
libFLAC accepts it (#22).

**API.** `frame.New` and `frame.Parse` take the stream's bit reader, which is
internal: frames are read through a `Stream`, which owns the reader across
frames. `ParseInto`, `ParseReuse`, `SetStreamBlockSizes` and
`Channels.Decorrelated` are added. There are no dependencies: the bit writer is
vendored and trimmed to what the encoder uses, and the `pkg` and `term` modules
are gone.

**Verification.** The IETF conformance files (`testdata/flac-test-files`, a
submodule) decode to libFLAC's samples and MD5s, and to ffmpeg's; the metadata
parser is held to `metaflac`, the encoder to `flac -t`, `flac -d` and ffmpeg
over a matrix of lengths, depths, assignments, block sizes and sample rates,
the bit reader to a plain one, the CRCs to long division. Hostile tables of
crafted frames and metadata run against the reference, each row held to its
verdict except the three that forbid a panic and record the reference's
decision; a random damage sweep holds damaged streams to the reference's
decision and samples; fuzz targets cover the stream and the metadata section
under an allocation bound; a race lane runs.
`harness/` is a differential harness by Arsenie Coseac and the 1-bit bridge
team that runs decode, seek and fuzz lanes against libFLAC, upstream and ffmpeg
(`just harness`, weekly in CI); it found #39 and #40, and its encoder lane is
the suite's own matrix now. Lint, test, security and
CI come through [limen](https://github.com/farcloser/limen): `just lint`,
`just test`.

## Credits

[mewkiz](https://github.com/mewkiz) and the contributors to mewkiz/flac, whose
library this is; Arsenie Coseac and the 1-bit bridge team for the harness and
the review that came with it.
