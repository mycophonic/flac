# flac

A FLAC decoder and encoder in Go, with no dependencies: a fork of
[mewkiz/flac](https://github.com/mewkiz/flac) that reads 32-bit streams,
refuses what the format forbids instead of panicking or guessing, seeks to
the right frame, and decodes faster. It is the decoder behind
[saprobe](https://github.com/mycophonic/saprobe)'s FLAC support; anything
the upstream maintainers want back is theirs to take.

Status: release candidate (`v1.0.0-rc2`). The API is upstream's, with the
differences listed below. Pure Go, Go 1.25, public domain (Unlicense, as
upstream).

```go
import "github.com/mycophonic/flac"
```

- `flac`: streams, with their metadata, frames and seeking.
- `flac/frame`: the audio frames and their subframes.
- `flac/meta`: the metadata blocks (STREAMINFO, VORBIS_COMMENT, PICTURE,
  SEEKTABLE, CUESHEET, APPLICATION, PADDING).

Documentation: [pkg.go.dev/github.com/mycophonic/flac](https://pkg.go.dev/github.com/mycophonic/flac).

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

Succinctly, since the fork's history and
[issues](https://github.com/mycophonic/flac/issues?q=is%3Aissue) carry the
detail; each defect found has an issue, fixed ones closed.

**Decoding**

- 32-bit streams decode, including the 33-bit side channel of a decorrelated
  stereo frame, on a wide path; mid/side arithmetic is 64-bit.
- Faster: one CRC reader in the frame I/O chain, a tighter bit reader and
  unary decode, aligned verbatim reads, fewer allocations, and buffer reuse
  through `ParseInto` and `ParseReuse`. Decoding ran about twice as fast as
  upstream when the fork began; measure on your own streams.
- A frame is held to STREAMINFO: channel count, bit depth, sample rate, and
  the total sample count; `SampleNumber` follows the stream's block sizes.

**Refusals, where upstream panics, loops, over-allocates or decodes garbage**

- A subframe with wasted bits at or above the bit depth, or a predictor order
  at or above the block size, on both decode paths.
- A frame number with a 0xFF lead byte or past 36 bits; a Rice partition
  count the block size does not divide; a garbage block type.
- A stream cut inside its metadata reports the cut rather than opening as a
  clean, empty stream; a second STREAMINFO and a seek table of a stray length
  are refused.
- Metadata parsers allocate within the block, never a declared length, so a
  hundred-byte stream cannot ask for gigabytes; a short string costs its
  length.
- Zero bytes before a frame's sync code, undeclared padding, are scanned
  past (up to 32 MB); an ID3v2 tag before the stream is skipped, as before.

**Seeking**

- `Seek` lands on the frame holding the sample: the buffered reader serves
  fresh bytes after a direct read, sample numbers come from the stream, and a
  target before the first seek point seeks to the start.

**Encoding**

- Refuses a frame the stream cannot carry (predictor precision and shift, a
  Rice partition order or count the block size does not take, a missing Rice
  table, a sample beyond the bit depth) instead of writing a stream libFLAC
  rejects.
- Writes a decorrelated 32-bit frame with independent channels, since the
  side channel would not fit; writes 4-bit samples with the header deferring
  to STREAMINFO; sets each frame's number.
- `Close` rewrites STREAMINFO with the minimum block size excluding the last
  block, measured frame sizes, and valid sizes for a stream of one short
  block.

**Divergences from libFLAC, under decision**

- The STREAMINFO MD5 is not checked at the end of a stream (#20).
- A stream without the `fLaC` marker is not synced to its first frame (#21).
- An overlong UTF-8 frame number is refused where libFLAC accepts it (#22).

**API**

- `frame.New` and `frame.Parse` take the stream's bit reader, which is
  internal: frames are read through a `Stream`, which owns the reader across
  frames. `ParseInto`, `ParseReuse`, `SetStreamBlockSizes` and
  `Channels.Decorrelated` are added.
- No dependencies: the bit writer is vendored and trimmed to what the encoder
  uses; the `pkg` and `term` modules are gone.

**Verification**

- The IETF conformance files (`testdata/flac-test-files`, a submodule) decode
  to libFLAC's samples and MD5s; the metadata parser is held to `metaflac`,
  the encoder to `flac -t` and `flac -d`, the bit reader to a plain one, the
  CRCs to long division.
- Hostile tables of crafted frames and metadata, each row verified against
  the reference; fuzz targets for the stream and the metadata section with an
  allocation bound; a race lane.
- `harness/`: a differential harness by Arsenie Coseac and the 1-bit bridge
  team, running decode, seek, encode and fuzz lanes against libFLAC and
  upstream (`just harness`, weekly in CI). It found #39 and #40.
- Lint, test, security and CI through
  [limen](https://github.com/farcloser/limen): `just lint`, `just test`.

## Credits

[mewkiz](https://github.com/mewkiz) and the contributors to mewkiz/flac,
whose library this is; Arsenie Coseac and the 1-bit bridge team for the
harness and the review that came with it.
