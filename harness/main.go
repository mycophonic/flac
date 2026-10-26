// Command flacharness holds github.com/mycophonic/flac to upstream
// github.com/mewkiz/flac and to libFLAC's flac tool.
//
// Subcommands:
//
//	gen        the corpora: signals, libFLAC encodings, crafted reproducers
//	decode     differential decode of FLAC files: fork, upstream and libFLAC
//	alloc      bytes allocated parsing hostile metadata blocks
//	seek       Stream.Seek against flac -d --skip/--until
//	triage     replay fuzz failures and FLAC files against all three decoders
//	refdecode  an arbitrary-precision decoder from RFC 9639, for triage
//	summarize  verdicts from an output directory
//	compare    the verdicts of two summaries
//	version    the fork and upstream module versions in this binary
//
// The fork commit is chosen at build time (see run.sh and README.md).
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strings"
)

const (
	forkModule     = "github.com/mycophonic/flac"
	upstreamModule = "github.com/mewkiz/flac"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}

	args := os.Args[2:]

	var err error

	switch os.Args[1] {
	case "gen":
		err = cmdGen(args)
	case "decode":
		err = cmdDecode(args)
	case "alloc":
		err = cmdAlloc(args)
	case "seek":
		err = cmdSeek(args)
	case "triage":
		err = cmdTriage(args)
	case "refdecode":
		err = cmdRefDecode(args)
	case "summarize":
		err = cmdSummarize(args)
	case "compare":
		err = cmdCompare(args)
	case "version":
		err = cmdVersion(args)
	default:
		usage()
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "flacharness:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: flacharness decode|alloc|seek|encode|triage|summarize|compare|version [flags]")
	os.Exit(2)
}

// Versions names the modules this binary was built against, and the outside
// implementations it reached: the summary's head reads them from here, so a
// probe made once (CoreAudio's, on a file the lane names) is what it prints.
type Versions struct {
	Fork        string `json:"fork"`
	ForkReplace string `json:"fork_replace,omitempty"`
	Upstream    string `json:"upstream"`
	Go          string `json:"go"`
	LibFLAC     string `json:"libflac,omitempty"`
	FFmpeg      string `json:"ffmpeg,omitempty"`
	CoreAudio   string `json:"coreaudio,omitempty"`
}

// cmdVersion prints Versions; -probe names a libFLAC-written 16-bit file
// CoreAudio is tried on first, as the decode lane tries it.
func cmdVersion(args []string) error {
	fl := flag.NewFlagSet("version", flag.ExitOnError)
	probe := fl.String("probe", "", "a FLAC file to probe CoreAudio with before naming it")
	_ = fl.Parse(args)

	v := moduleVersions()
	v.LibFLAC = newLibFLAC().version()
	v.FFmpeg = newFFmpeg().version()

	var probeData []byte
	if *probe != "" {
		probeData, _ = os.ReadFile(*probe) // #nosec G304 -- the file run.sh names
	}

	v.CoreAudio = newCoreAudio(probeData).version()

	return json.NewEncoder(os.Stdout).Encode(v)
}

func moduleVersions() Versions {
	var v Versions

	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return v
	}

	v.Go = bi.GoVersion
	for _, d := range bi.Deps {
		switch d.Path {
		case forkModule:
			v.Fork = d.Version
			if d.Replace != nil {
				// A local replace is named by its directory's last element
				// only, so outputs carry no local paths.
				r := d.Replace.Path
				if d.Replace.Version != "" && d.Replace.Version != "(devel)" {
					r += "@" + d.Replace.Version
				} else {
					r = "local:" + lastElem(r)
				}

				v.ForkReplace = r
			}
		case upstreamModule:
			v.Upstream = d.Version
		}
	}

	return v
}

func lastElem(p string) string {
	p = strings.TrimRight(p, "/\\")
	if i := strings.LastIndexAny(p, "/\\"); i >= 0 {
		return p[i+1:]
	}

	return p
}

// jsonLines writes one JSON value per line.
type jsonLines struct {
	enc *json.Encoder
}

func newJSONLines(w io.Writer) *jsonLines {
	e := json.NewEncoder(w)
	e.SetEscapeHTML(false)

	return &jsonLines{enc: e}
}

func (j *jsonLines) write(v any) error { return j.enc.Encode(v) }

// createOut opens path for writing, or stdout for "" or "-".
func createOut(path string) (io.WriteCloser, error) {
	if path == "" || path == "-" {
		return nopCloser{os.Stdout}, nil
	}

	return os.Create(path)
}

type nopCloser struct{ io.Writer }

func (nopCloser) Close() error { return nil }

// capBuffer keeps the first max bytes written to it.
type capBuffer struct {
	b   []byte
	max int
}

func (c *capBuffer) Write(p []byte) (int, error) {
	if room := c.max - len(c.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}

		c.b = append(c.b, p[:room]...)
	}

	return len(p), nil
}

func (c *capBuffer) String() string { return string(c.b) }

// trimStack keeps a panic's stack short enough for a JSON line: the
// goroutine header and the frames below the runtime's panic machinery.
func trimStack(s []byte) string {
	lines := strings.Split(string(s), "\n")

	var out []string

	skip := true

	for i, l := range lines {
		if i == 0 {
			out = append(out, l)
			continue
		}

		if skip && strings.HasPrefix(l, "panic(") {
			skip = false
			continue
		}

		if !skip {
			out = append(out, strings.TrimSpace(l))
		}

		if len(out) > 40 {
			break
		}
	}

	if skip {
		// No panic frame found: keep the head of the stack.
		out = lines
		if len(out) > 40 {
			out = out[:40]
		}
	}

	return strings.Join(out, "\n")
}
