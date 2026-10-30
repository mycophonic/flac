# FLAC decoders

The implementations this library is held to, and the rest of the field.
Decoding is deterministic: a correct decoder produces bit-exact output,
whatever the encoder's settings, and the format was designed so that
decoding is cheap at the expense of encoding. The IETF decoder testbench
([ietf-wg-cellar/flac-test-files](https://github.com/ietf-wg-cellar/flac-test-files))
provides the conformance vectors; the suite decodes every set.

## The ones the suite and the harness compare against

| Decoder | Depths | Role here |
|---|---|---|
| **libFLAC** (`flac -d`) | 4 to 32 | the reference; every test holds samples to it, at the depths it writes raw (8, 16, 24, 32) |
| **ffmpeg** (libavcodec) | 4 to 32 | the fourth implementation, independent of libFLAC; on three channels or more it reorders to its own layouts for some depths, so a difference there is logged, not failed |
| **mewkiz/flac** v1.0.14 | 4 to 32 | upstream, decoded by the harness beside the fork |

Of the three outside implementations, ffmpeg is the lenient one: it decodes
the harness's boundary files (a predictor order equal to a one-sample block)
and several hostile streams that libFLAC refuses. The fork sides with libFLAC
on each.

## The field

Every independent implementation found, roughly by reach: how much software
and hardware runs it first, then GitHub stars or package downloads (October
2026). "Here" says where the Mycophonic and Forkcloser projects already
use one:

- **suite**: this repository's tests, and its differential harness;
- **saprobe-flac**: [saprobe-flac](https://github.com/mycophonic/saprobe-flac)'s
  benchmarks;
- **agar**: a decoder [agar](https://github.com/mycophonic/agar) drives for
  other projects' tests;
- **testbin**: a binary [forkcloser/testbin](https://github.com/forkcloser/testbin)
  builds for six platforms;
- **planned**: an open issue.

| Decoder | Language | Reach | Here | Notes |
|---|---|---|---|---|
| [libFLAC](https://github.com/xiph/flac) | C | behind most software that plays FLAC; 2.5k stars | suite, saprobe-flac, testbin (`flac`, `metaflac`) | Xiph's reference, 4 to 32 bits; SIMD on SSE2, SSSE3, AVX2, NEON; strips to a decode-only build of about 100 KiB. BSD. |
| [ffmpeg](https://github.com/FFmpeg/FFmpeg) (libavcodec) | C | VLC, mpv, Kodi, Plex, Jellyfin, Chrome, Firefox; 66k stars | suite, saprobe-flac, agar | independent of libFLAC, 4 to 32 bits. Keeps a `use_buggy_lpc` option for streams its own old encoder wrote wrong. Converting to PCM needs the output format named (`-c:a pcm_s24le`), or it may truncate. |
| Apple CoreAudio (AudioToolbox) | closed | every macOS 10.13+ and iOS 11+ device, Safari | saprobe-flac, agar | 16 and 24 bits; agar's binding takes no 32-bit FLAC |
| Windows Media Foundation | closed | every Windows 10+ machine | planned ([agar#32](https://github.com/mycophonic/agar/issues/32)) | 16 and 24 bits; FLAC in MKV, MP4 and bare |
| [dr_flac](https://github.com/mackron/dr_libs) | C, one header | 1.8k stars; inside [miniaudio](https://github.com/mackron/miniaudio) (7.4k), SDL_sound and raylib | testbin | 4 to 32 bits; game engines and embedded; no SIMD. Public domain or MIT-0. |
| [Symphonia](https://github.com/pdeljanov/Symphonia) | Rust | 3.5k stars; 19.6M all-time crates.io downloads (`symphonia-bundle-flac`: 12.4M), the most of any Rust FLAC crate | | safe Rust; credits Claxon for its structure. MPL-2.0. |
| [Claxon](https://github.com/ruuda/claxon) | Rust | 330 stars; 5.5M all-time crates.io downloads | | no release since 0.4.3 (2020) |
| [CSCore](https://github.com/filoe/cscore) | C# | 2.4k stars | | the NAudio.Flac packages are derived from its decoder ([BunLabs/NAudio.Flac](https://github.com/BunLabs/NAudio.Flac) among them). MS-PL. |
| [ESP32-audioI2S](https://github.com/schreibfaul1/ESP32-audioI2S) | C++ | 1.7k stars; ESP32 web radios | | its own decoder, blocks of up to 24576 bytes; needs PSRAM |
| CUETools [libFlake](https://github.com/gchudov/cuetools.net) | C# | 672 stars; CUETools and CUERipper | testbin (`CUETools.Flake`) | 4 to 32 bits; the command decodes FLAC input to re-encode it |
| [mewkiz/flac](https://github.com/mewkiz/flac) | Go | 365 stars | suite | this library's upstream; behind faiface/beep and [go-audio-flac](https://github.com/mewspring/go-audio-flac) |
| [mycophonic/flac](https://github.com/mycophonic/flac) | Go | | — | this library; behind saprobe-flac |
| [Rockbox](https://github.com/Rockbox/rockbox) `libffmpegFLAC` | C, ARM and ColdFire assembly | portable players' firmware; 749 stars | | derived from ffmpeg's decoder; 16 and 24 bits; real time at 16/44.1 on an 80 MHz ARM7 |
| [flac.js](https://github.com/audiocogs/flac.js) | JavaScript | 551 stars | | ported from ffmpeg, for Aurora.js; inactive since 2016. LGPL. |
| [FLAC-library-Java](https://github.com/nayuki/FLAC-library-Java), and the [simple implementation](https://www.nayuki.io/page/simple-flac-implementation) | Java, Python | 85 stars | | Project Nayuki's: 4 to 32 bits, seeking, CRC and MD5 checks; the simple decoder is a few hundred lines, written for clarity. [micro-flac](https://github.com/esphome-libs/micro-flac) and [flacat](https://github.com/tpunix/flacat) (C) are derived from it. LGPL-3.0 (the Java library). |
| [libfoxenflac](https://github.com/astoeckel/libfoxenflac) | C | 84 stars | | small, allocates nothing |
| [jFLAC](https://repo1.maven.org/maven2/org/jflac/jflac-codec/) and [JustFLAC](https://github.com/drogatkin/JustFLAC) | Java | jFLAC on Maven, last released 2017; JustFLAC 23 stars | | a port of libFLAC's code; JustFLAC is the fork that adds 24-bit/192 kHz. LGPL. |
| [micro-flac](https://github.com/esphome-libs/micro-flac) | C++, Xtensa assembly | ESPHome's FLAC decoder | | Nayuki-derived; native FLAC and Ogg FLAC; multiply-accumulate LPC on ESP32 and ESP32-S3 |
| [eaburns/flac](https://github.com/eaburns/flac) | Go | 95 stars | | inactive since 2017. MIT. |
| [tphakala/go-flac](https://github.com/tphakala/go-flac) | Go | new | | an independent decoder and encoder. MIT. |
| [takafumiokamoto/flac](https://github.com/takafumiokamoto/flac) | Go | new | | claims 1.5× mewkiz/flac v1.0.14's speed, with 1/29 of its memory and 1/90 of its allocations; checks frame CRCs and MD5 |
| [dart_flac](https://pub.dev/packages/dart_flac) | Dart | pub.dev, 0.0.7 | | about 5× slower than libFLAC by its author's count; no Ogg FLAC |
| [flac](https://github.com/sourrust/flac), [flac-codec](https://crates.io/crates/flac-codec), [oxideav-flac](https://crates.io/crates/oxideav-flac) | Rust | small | | `flac` inactive since 2021 |
| [Senryoku/zflac](https://github.com/Senryoku/zflac), [AidanWelch/zflac](https://github.com/AidanWelch/zflac) | Zig | small | | two independent decoders: the first written from RFC 9639, the second also an encoder |
| [PureFlac](https://github.com/FatJohn/PureFlac) | C# | new | | dr_flac ported to managed code; claims output matching libFLAC on its fixtures; no seeking, no Ogg FLAC |
| [flac-py](https://github.com/turlando/flac-py) | Python | | | a teaching implementation, very slow |
| [fLaCPGA](https://github.com/xavieran/fLaCPGA) | Verilog | 32 stars | | an FPGA decoder and encoder, 250 MHz on a Stratix V |

Every major browser decodes FLAC natively since 2017 (Chrome 56, Firefox
51, Edge 16, Safari 11, Opera 43), bare or in Ogg, but through the
decoders above, not their own.

### Built on one of the above

These decode with another implementation, and add a platform or a
language, not a decoder:

- **libFLAC**: Android MediaCodec (`c2.android.flac.decoder` wraps
  AOSP's copy of libFLAC; planned as
  [agar#33](https://github.com/mycophonic/agar/issues/33) for the platform,
  not a new decoder), ExoPlayer's FLAC extension, VLC's `flac` module,
  GStreamer's `flacdec`, libsndfile and python-soundfile, SoX,
  SFBAudioEngine, foobar2000, EAC, XLD, dBpoweramp, mpd, pyFLAC, plibflac,
  `@wasm-audio-decoders/flac`, libflac.js, emflac, [ludiazv/zflac](https://github.com/ludiazv/zflac) (Zig),
  `libflac-sys` and `flac-bound` (Rust), the cgo bindings in Go,
  arduino-libflac.
- **ffmpeg**: Chrome, Firefox (its bundled copy, ffvpx), Kodi, mpv,
  ffmpeg.wasm, GStreamer's `avdec_flac`; Rockbox and flac.js are ports.
- **CoreAudio**: Safari.
- **dr_flac**: miniaudio, SDL_sound, raylib; PureFlac is a port.

## Speed

Decoding speed barely depends on the compression level: a `-0` file and a
`-8` file decode at about the same rate, since only the predictor order and
block size change. Typical single-core rates on a modern desktop are 500 to
1000× real time at 16/44.1 stereo, 200 to 500× at 24/96 and 100 to 250× at
24/192; a 1.2 GHz Cortex-A53 manages 50 to 100× at 16/44.1 and 5 to 15× at
24/96 six-channel. See [performance.md](performance.md) for where this
decoder's time goes.
