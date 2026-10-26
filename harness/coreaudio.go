package main

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/mycophonic/agar/pkg/coreaudio"
)

// coreAudioTool runs Apple's CoreAudio through agar's AudioToolbox codec,
// the fifth implementation the decode lane holds the files to: in process,
// on macOS with cgo; elsewhere the column stays empty. CoreAudio writes
// 20-bit sources as 24-bit and takes no 32-bit FLAC, so a file is related
// to libFLAC only where both write the same raw depth.
type coreAudioTool struct {
	codec   coreaudio.Codec
	present bool
	reason  string // why the column is empty, when it is
}

// newCoreAudio probes the codec on probe, a stream libFLAC wrote, when one
// is given: agar's binding reports itself available wherever it is compiled
// in, and a machine whose codecs are unreachable (a sandbox) would otherwise
// read as CoreAudio refusing every file. The crafted clean stream is no
// probe: CoreAudio refuses it, two 16-sample frames being too little for it.
func newCoreAudio(probe []byte) coreAudioTool {
	codec := coreaudio.NewCGO()
	if runtime.GOOS != "darwin" || !codec.Available() {
		return coreAudioTool{codec: codec, reason: toolAbsent}
	}

	if probe != nil {
		if _, _, err := codec.Decode(probe); err != nil {
			return coreAudioTool{codec: codec, reason: "unavailable: " + clip(err.Error(), errorClip)}
		}
	}

	return coreAudioTool{codec: codec, present: true}
}

// coreAudioProbe is the first libFLAC-written 16-bit file among the inputs
// (the bench corpus's), read for the probe, or nil.
func coreAudioProbe(files []inputFile) []byte {
	for _, f := range files {
		if f.corpus == "bench" && strings.Contains(f.rel, "bench-16-") {
			data, err := os.ReadFile(f.path()) // #nosec G304 -- a corpus file the lane is walking
			if err == nil {
				return data
			}
		}
	}

	return nil
}

// version names the platform, as the codec has no version of its own, or
// says why the column is empty.
func (c coreAudioTool) version() string {
	if !c.present {
		return c.reason
	}

	return "AudioToolbox, " + runtime.GOOS + "/" + runtime.GOARCH
}

// decode decodes file through CoreAudio and returns its verdict with the
// PCM's length and hash, comparable to libFLAC's at the same depth.
func (c coreAudioTool) decode(file string, bps int) LibResult {
	data, err := os.ReadFile(file) // #nosec G304 -- a corpus file the lane is walking
	if err != nil {
		return LibResult{Exit: -1, Error: err.Error()}
	}

	pcm, format, err := c.codec.Decode(data)
	if err != nil {
		return LibResult{Exit: 1, Error: clip(err.Error(), errorClip)}
	}

	if !rawDepth(bps) || format.BitDepth != bps {
		return LibResult{
			Exit: -1,
			Error: "no raw output at " + strconv.Itoa(
				bps,
			) + " bits (CoreAudio writes " + strconv.Itoa(
				format.BitDepth,
			) + ")",
		}
	}

	sum := sha256.Sum256(pcm)

	return LibResult{OK: true, Bytes: int64(len(pcm)), SHA256: hex.EncodeToString(sum[:])}
}

// coreAudioRelation is CoreAudio's verdict against libFLAC's on one file,
// in ffmpeg's terms with coreaudio in its place.
func coreAudioRelation(coreAudio, lib LibResult) string {
	switch {
	case coreAudio.Exit == -1 && strings.HasPrefix(coreAudio.Error, "no raw output"):
		return relNoRawDepth
	case coreAudio.OK && lib.OK:
		if coreAudio.SHA256 == lib.SHA256 {
			return relAgree
		}

		return relDiffer
	case coreAudio.OK:
		return relLibFLACRefuses
	case lib.OK:
		return relCoreAudioRefuses
	default:
		return relBothRefuse
	}
}

const relCoreAudioRefuses = "coreaudio_refuses"
