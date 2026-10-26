# FLAC encoders

The format allows 1 to 8 channels, 4 to 32 bits and 1 Hz to 1,048,575 Hz
(RFC 9639), and it is lossless whatever the settings: compression settings
move file size and encoding time, never the samples. A subset of the format
is defined for streaming and hardware players; a lax mode allows the rest.
This library's encoder writes what the stream can carry and refuses the
rest (see the README); the matrices hold its streams to `flac -t`, `flac
-d`, ffmpeg and this decoder.

## The field

| Encoder | Depths | Levels | Platform | Notes |
|---|---|---|---|---|
| libFLAC | 4 to 32 | 0 to 8 | everywhere | the reference; behind the `flac` tool, EAC, dBpoweramp, XLD, foobar2000. Level 5 is the default and the knee: beyond it, time rises sharply for little compression. Subset keeps LPC order ≤ 12 and block size ≤ 4608 at ≤ 48 kHz; lax allows order 32 and larger blocks. MD5 of the samples in STREAMINFO. SIMD. |
| ffmpeg | 16, 24, 32 | 0 to 12 | everywhere | descended from Flake, long diverged. Its old encoder wrote broken high-order LPC streams; the decoder's `use_buggy_lpc` reads them. Different default block sizes than libFLAC for hi-res; slightly larger files at a comparable level. |
| CUETools libFlake | 16, 24 | 0 to 11 | Windows, .NET | from Flake, reworked in C#; faster than libFLAC at the same ratio; variable block size (`-v 1`, `-v 2`); levels 9 to 11 leave the subset. |
| FLACCL | 16, 24 | 0 to 11 | Windows, OpenCL | LPC on the GPU; about 10× libFLAC at `-8`, and `-8` faster than libFLAC `-0` with smaller files than libFLAC `-8`. |
| Apple CoreAudio | 16, 24 | auto | macOS 10.15+, iOS 13+ | |
| Windows Media Foundation | 16, 24 | auto | Windows 10+ | stereo |
| Android MediaRecorder | 16, 24 | auto | Android 4.1+ | stereo |

Historical: Flake (Justin Ruggles, 2006 to 2009), the first variable block
size encoder, partly merged into ffmpeg and the basis of libFlake; Nayuki's
Java encoder (2016), which searches block sequences by dynamic programming
for the smallest lax files at 100 to 1000× libFLAC's time.

## Which to use

| Use | Pick | Why |
|---|---|---|
| general | libFLAC `-5` | compatibility, good compression, fast |
| smallest subset-safe files | libFLAC `-8` | 1 to 2 % below `-5`, plays everywhere |
| smallest files, software playback only | libFlake `-11 --lax` | leaves the subset |
| fastest | FLACCL `-8` | 10× libFLAC at the same ratio |
| scripting | ffmpeg or libFLAC | libFLAC's files are a little smaller |
| CD ripping | EAC with libFLAC, or CUETools | CUETools adds AccurateRip |
| archival | libFLAC `-8` | proven, subset, compatible |

## The format in numbers (RFC 9639)

| Parameter | Format | Subset |
|---|---|---|
| depth | 4 to 32 | ≤ 24 in practice |
| sample rate | 1 to 1,048,575 Hz | ≤ 655,350 Hz |
| channels | 1 to 8 | |
| block size | 16 to 65,535 | ≤ 4608 at ≤ 48 kHz, ≤ 16,384 above |
| LPC order | 0 to 32 | ≤ 12 at ≤ 48 kHz |
| Rice partition order | 0 to 15 | ≤ 8 |
| integrity | CRC-8 per frame header, CRC-16 per frame, MD5 of the samples in STREAMINFO | |
| stereo | left/right, left/side, side/right, mid/side, chosen per frame | |
| containers | bare `.flac`, Ogg, MP4 | |
