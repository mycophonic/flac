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

| Decoder | Depths | Platform | Notes |
|---|---|---|---|
| libFLAC | 4 to 32 | everywhere | Xiph's reference; behind foobar2000, EAC, XLD, dBpoweramp, mpd, libsndfile and most software that plays FLAC. SIMD on SSE2, SSSE3, AVX2, NEON. Strips to a decode-only build of about 100 KiB. BSD. |
| ffmpeg | 4 to 32 | everywhere | independent; behind VLC, mpv, Kodi, Plex, Jellyfin. Keeps a `use_buggy_lpc` option for streams its own old encoder wrote wrong. Converting to PCM needs the output format named (`-c:a pcm_s24le`), or it may truncate. |
| Apple CoreAudio | 16, 24 | macOS 10.15+, iOS 13+ | native since 2019; timed against this decoder by saprobe-flac's benchmark through agar's AudioToolbox binding, which takes no 32-bit FLAC |
| Windows Media Foundation | 16, 24 | Windows 10+ | native since 2015; FLAC in MKV, MP4 and bare |
| Android MediaCodec | 16, 24 | Android 3.1+ | decoding since 2011, encoding since 4.1, FLAC in MP4 since 10 |
| dr_flac | 4 to 32 | everywhere | single header, public domain or MIT; game engines and embedded; no SIMD |
| CUETools libFlake | 4 to 32 | Windows, .NET | pure C# |
| Rockbox | 16, 24 | portable players | libFLAC-derived with ARM and ColdFire assembly; real-time 16/44.1 on an 80 MHz ARM7; stereo only by hardware |

Every major browser decodes FLAC natively since 2017 (Chrome 56, Firefox
51, Edge 16, Safari 11, Opera 43), bare or in Ogg.

## Speed

Decoding speed barely depends on the compression level: a `-0` file and a
`-8` file decode at about the same rate, since only the predictor order and
block size change. Typical single-core rates on a modern desktop are 500 to
1000× real time at 16/44.1 stereo, 200 to 500× at 24/96 and 100 to 250× at
24/192; a 1.2 GHz Cortex-A53 manages 50 to 100× at 16/44.1 and 5 to 15× at
24/96 six-channel. See [performance.md](performance.md) for where this
decoder's time goes.
