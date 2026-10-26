package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// Summary is summary.json: per-corpus counts, the state of each issue the
// harness has a reproducer for, and a flat map of verdicts that `compare`
// diffs between two runs.
type Summary struct {
	Fork      string                    `json:"fork"`
	Label     string                    `json:"label,omitempty"`
	Upstream  string                    `json:"upstream"`
	Go        string                    `json:"go"`
	LibFLAC   string                    `json:"libflac"`
	FFmpeg    string                    `json:"ffmpeg,omitempty"`
	CoreAudio string                    `json:"coreaudio,omitempty"`
	Decode    []CorpusSummary           `json:"decode"`
	Issues    []IssueState              `json:"issues"`
	Alloc     []AllocRecord             `json:"alloc,omitempty"`
	Seek      map[string]map[string]int `json:"seek,omitempty"`
	SeekNotes []string                  `json:"seek_not_matching,omitempty"`
	Fuzz      []FuzzSummary             `json:"fuzz,omitempty"`
	Verdicts  map[string]string         `json:"verdicts"`
}

// CorpusSummary counts one corpus's decode verdicts.
type CorpusSummary struct {
	Corpus       string         `json:"corpus"`
	Files        int            `json:"files"`
	Fork         map[string]int `json:"fork"`
	Upstream     map[string]int `json:"upstream"`
	FFmpeg       map[string]int `json:"ffmpeg_vs_libflac,omitempty"`
	CoreAudio    map[string]int `json:"coreaudio_vs_libflac,omitempty"`
	Frames       int64          `json:"fork_frames"`
	SampleFrames int64          `json:"fork_sample_frames"`
	NotMatching  []string       `json:"fork_not_matching,omitempty"`
}

// IssueState is one issue's state at this commit.
type IssueState struct {
	Issue    string `json:"issue"`
	What     string `json:"what"`
	State    string `json:"state"` // fixed, not fixed, changed, not tested
	Evidence string `json:"evidence"`
}

// FuzzSummary is one fuzz target's run.
type FuzzSummary struct {
	Target       string   `json:"target"`
	Elapsed      string   `json:"elapsed,omitempty"`
	Execs        string   `json:"execs,omitempty"`
	Interesting  string   `json:"new_interesting,omitempty"`
	Failed       bool     `json:"failed"`
	Failure      string   `json:"failure,omitempty"`
	Crashers     []string `json:"crashers,omitempty"`
	Observations int      `json:"observations"`
	Allowed      int      `json:"allowed_panics"`
}

func readJSONLines[T any](p string) ([]T, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []T

	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 64<<20)

	for sc.Scan() {
		var v T
		if err := json.Unmarshal(sc.Bytes(), &v); err != nil {
			return nil, fmt.Errorf("%s: %w", p, err)
		}

		out = append(out, v)
	}

	return out, sc.Err()
}

func cmdSummarize(args []string) error {
	fl := flag.NewFlagSet("summarize", flag.ExitOnError)
	dir := fl.String("dir", "out", "the run's output directory")
	label := fl.String("label", "", "a name for the fork commit, printed in the summary")
	_ = fl.Parse(args)
	s := Summary{Verdicts: map[string]string{}, Label: *label}

	var v Versions
	if b, err := os.ReadFile(filepath.Join(*dir, "version.json")); err == nil {
		_ = json.Unmarshal(b, &v)
	} else {
		v = moduleVersions()
	}

	s.Fork, s.Upstream, s.Go = v.Fork, v.Upstream, v.Go
	if v.ForkReplace != "" {
		s.Fork += " => " + v.ForkReplace
	}

	// The tools as version.json saw them (run.sh writes it with the probe);
	// without the file, as this process sees them, CoreAudio unprobed.
	s.LibFLAC, s.FFmpeg, s.CoreAudio = v.LibFLAC, v.FFmpeg, v.CoreAudio
	if s.LibFLAC == "" {
		s.LibFLAC = newLibFLAC().version()
	}

	if s.FFmpeg == "" {
		s.FFmpeg = newFFmpeg().version()
	}

	if s.CoreAudio == "" {
		s.CoreAudio = newCoreAudio(nil).version() + " (unprobed)"
	}

	decode, err := readJSONLines[DecodeRecord](filepath.Join(*dir, "decode.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	s.summarizeDecode(decode)

	alloc, err := readJSONLines[AllocRecord](filepath.Join(*dir, "alloc.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	s.summarizeAlloc(alloc)

	seek, err := readJSONLines[SeekRecord](filepath.Join(*dir, "seek.jsonl"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}

	s.summarizeIssues(decode, alloc, seek)
	s.summarizeSeek(seek)
	s.summarizeFuzz(filepath.Join(*dir, "fuzz"))

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(*dir, "summary.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}

	txt := s.text()
	if err := os.WriteFile(filepath.Join(*dir, "summary.txt"), []byte(txt), 0o644); err != nil {
		return err
	}

	fmt.Print(txt)

	return nil
}

func inc(m map[string]int, k string) { m[k]++ }

func detail(r DecResult) string {
	switch {
	case r.Panic != "":
		return "panic: " + strings.SplitN(r.Panic, "\n", 2)[0]
	case r.FirstDiff != nil:
		d := r.FirstDiff

		s := fmt.Sprintf(
			"%s at frame %d (frame number %d), sample %d, channel %d: got %d",
			d.Kind,
			d.Frame,
			d.FrameNumber,
			d.Index,
			d.Channel,
			d.Got,
		)
		if d.Want != nil {
			s += fmt.Sprintf(", libFLAC %d", *d.Want)
		}

		if d.Note != "" {
			s += " (" + d.Note + ")"
		}

		if r.DiffSamples > 0 {
			s += fmt.Sprintf("; %d samples in %d frames differ", r.DiffSamples, r.DiffFrames)
		}

		return s
	case r.Error != "":
		return r.Error
	}

	return ""
}

func (s *Summary) summarizeDecode(recs []DecodeRecord) {
	by := map[string]*CorpusSummary{}

	var order []string

	for _, r := range recs {
		c, ok := by[r.Corpus]
		if !ok {
			c = &CorpusSummary{Corpus: r.Corpus, Fork: map[string]int{}, Upstream: map[string]int{}}
			by[r.Corpus] = c
			order = append(order, r.Corpus)
		}

		c.Files++
		inc(c.Fork, r.Fork.Verdict)
		inc(c.Upstream, r.Upstream.Verdict)

		if r.FFmpegVsLibFLAC != "" {
			if c.FFmpeg == nil {
				c.FFmpeg = map[string]int{}
			}

			inc(c.FFmpeg, r.FFmpegVsLibFLAC)
		}

		if r.CoreAudioVsLibFLAC != "" {
			if c.CoreAudio == nil {
				c.CoreAudio = map[string]int{}
			}

			inc(c.CoreAudio, r.CoreAudioVsLibFLAC)
		}

		c.Frames += r.Fork.Frames

		c.SampleFrames += r.Fork.SampleFrames
		if r.Fork.Verdict != "match" && len(c.NotMatching) < 60 {
			n := fmt.Sprintf("%s: %s", r.File, r.Fork.Verdict)
			if d := detail(r.Fork); d != "" {
				n += ": " + d
			}

			if !r.LibFLAC.OK {
				n += " | libFLAC: " + r.LibFLAC.Error
			}

			c.NotMatching = append(c.NotMatching, n)
		}
	}

	for _, name := range order {
		c := by[name]
		s.Decode = append(s.Decode, *c)

		s.Verdicts["decode/"+name+"/files"] = fmt.Sprint(c.Files)
		for k, n := range c.Fork {
			s.Verdicts["decode/"+name+"/fork/"+k] = fmt.Sprint(n)
		}

		for k, n := range c.Upstream {
			s.Verdicts["decode/"+name+"/upstream/"+k] = fmt.Sprint(n)
		}

		for k, n := range c.FFmpeg {
			s.Verdicts["decode/"+name+"/ffmpeg/"+k] = fmt.Sprint(n)
		}

		for k, n := range c.CoreAudio {
			s.Verdicts["decode/"+name+"/coreaudio/"+k] = fmt.Sprint(n)
		}
	}
}

type decodeRule struct {
	issue, what string
	files       func(r DecodeRecord) bool
	want        string // the fork verdict every file should have
	broken      string // the verdict the issue reported
	// check, if set, gives the verdict to judge instead of the decode's.
	check func(r DecodeRecord) string
}

// metadataVerdict judges a stream whose defect is in its metadata through
// all three constructors: refused by New (the decode), Parse and NewSeek.
// New skips a SEEKTABLE's body, so a seek table is judged by the other two.
func metadataVerdict(r DecodeRecord) string {
	if r.LibFLAC.OK {
		return r.Fork.Verdict
	}

	newRefuses := r.Fork.Verdict == "both_refuse" || strings.Contains(r.File, "seektable")
	if newRefuses && r.Fork.ParseError != "" && r.Fork.NewSeekError != "" {
		return "both_refuse"
	}

	if r.Fork.Verdict == "panic" || strings.HasPrefix(r.Fork.ParseError, "panic") ||
		strings.HasPrefix(r.Fork.NewSeekError, "panic") {
		return "panic"
	}

	return "missed_error"
}

func inCorpus(corpus string, names ...string) func(DecodeRecord) bool {
	return func(r DecodeRecord) bool {
		if r.Corpus != corpus {
			return false
		}

		if len(names) == 0 {
			return true
		}

		for _, n := range names {
			if ok, _ := path.Match(n, r.File); ok {
				return true
			}
		}

		return false
	}
}

func either(fs ...func(DecodeRecord) bool) func(DecodeRecord) bool {
	return func(r DecodeRecord) bool {
		for _, f := range fs {
			if f(r) {
				return true
			}
		}

		return false
	}
}

var decodeRules = []decodeRule{
	{
		"#10",
		"valid 32-bit mid/side, left/side and side/right streams decode to libFLAC's samples",
		either(
			inCorpus("gen32"),
			inCorpus("targeted", "valid-32bit-midside.flac"),
			inCorpus("control"),
		),
		"match",
		"mismatch",
		nil,
	},
	{
		"#16", "wasted bits at or above the depth, int32 path: refused, no panic",
		inCorpus("hostile", "int32-wasted*"), "both_refuse", "panic", nil,
	},
	{
		"#18",
		"a predictor order at or above the block size, int32 path: refused, no panic",
		either(
			inCorpus("targeted", "fixed-order4-block1.flac", "lpc-order32-block1.flac"),
			inCorpus("hostile", "int32-fixed4-block4.flac"),
		),
		"both_refuse",
		"panic",
		nil,
	},
	{
		"#34", "the same two refusals on the 33-bit side channel's wide path",
		either(inCorpus("crafted"), inCorpus("boundary"), inCorpus("hostile", "wide-*")), "both_refuse", "panic", nil,
	},
	{
		"#19", "a stream cut inside its metadata is refused by New, Parse and NewSeek",
		inCorpus("hostile", "cut-in-padding.flac"), "both_refuse", "missed_error", metadataVerdict,
	},
	{
		"#23", "a frame number with a 0xFF lead byte is refused",
		inCorpus("hostile", "frame-number-ff.flac"), "both_refuse", "missed_error", nil,
	},
	{
		"#26",
		"a second STREAMINFO (New, Parse, NewSeek) and a seek table of a stray length (Parse, NewSeek; New skips its body) are refused",
		inCorpus(
			"hostile",
			"second-streaminfo.flac",
			"seektable-stray-length.flac",
		),
		"both_refuse",
		"missed_error",
		metadataVerdict,
	},
	{
		"#27", "a frame whose sample rate differs from STREAMINFO is refused",
		inCorpus("hostile", "rate-change.flac"), "both_refuse", "missed_error", nil,
	},
	{
		"controls", "the crafted files' valid twins decode to libFLAC's samples",
		either(inCorpus("control"), inCorpus("hostile", "clean-*")), "match", "mismatch", nil,
	},
}

// divergenceRules are libFLAC's decisions the fork does not take, each an
// open issue: want is the fork's verdict while it stands.
var divergenceRules = []decodeRule{
	{
		"#20", "a wrong STREAMINFO MD5: libFLAC refuses the stream, the fork does not check (md5_only)",
		inCorpus("hostile", "md5-wrong.flac"), "md5_only", "", nil,
	},
	{
		"#21", "a stream starting at a frame header: libFLAC syncs to it, the fork refuses (extra_error)",
		inCorpus("hostile", "no-flac-marker.flac"), "extra_error", "", nil,
	},
	{
		"#22", "an overlong UTF-8 frame number: libFLAC accepts it, the fork refuses (extra_error)",
		inCorpus("hostile", "frame-number-overlong.flac"), "extra_error", "", nil,
	},
}

func countsText(m map[string]int) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%d %s", m[k], k))
	}

	return strings.Join(parts, ", ")
}

func (s *Summary) addIssue(is IssueState) {
	s.Issues = append(s.Issues, is)
	s.Verdicts["issue/"+is.Issue] = is.State
}

func (s *Summary) summarizeIssues(decode []DecodeRecord, alloc []AllocRecord, seek []SeekRecord) {
	for _, rule := range decodeRules {
		var rs []DecodeRecord

		for _, r := range decode {
			if rule.files(r) {
				rs = append(rs, r)
			}
		}

		is := IssueState{Issue: rule.issue, What: rule.what}

		switch {
		case len(rs) == 0:
			is.State, is.Evidence = "not tested", "no reproducer in this run"
		default:
			ok, broken, failure := 0, 0, false
			verdict := func(r DecodeRecord) string {
				if rule.check != nil {
					return rule.check(r)
				}

				return r.Fork.Verdict
			}
			m := map[string]int{}

			for _, r := range rs {
				v := verdict(r)

				m[v]++
				switch v {
				case rule.want:
					ok++
				case rule.broken, "panic", "timeout":
					broken++
				}

				if v == "panic" || v == "timeout" {
					failure = true
				}
			}

			switch {
			case ok == len(rs):
				is.State = "fixed"
			case broken > 0 || failure:
				is.State = "not fixed"
			default:
				is.State = "changed"
			}

			is.Evidence = fmt.Sprintf("%d files: %s", len(rs), countsText(m))
			if is.State != "fixed" {
				for _, r := range rs {
					if verdict(r) != rule.want {
						is.Evidence += fmt.Sprintf("; e.g. %s/%s: %s", r.Corpus, r.File, detail(r.Fork))
						break
					}
				}
			}
		}

		s.addIssue(is)
	}
	// Divergences from libFLAC that open decisions record.
	for _, rule := range divergenceRules {
		var rs []DecodeRecord

		for _, r := range decode {
			if rule.files(r) {
				rs = append(rs, r)
			}
		}

		is := IssueState{Issue: rule.issue, What: rule.what}

		m := map[string]int{}
		for _, r := range rs {
			m[r.Fork.Verdict]++
		}

		switch {
		case len(rs) == 0:
			is.State = "not tested"
		case m[rule.want] == len(rs):
			is.State = "diverges"
		case m["match"]+m["both_refuse"] == len(rs):
			is.State = "agrees with libFLAC"
		default:
			is.State = "changed"
		}

		is.Evidence = fmt.Sprintf("%d files: %s", len(rs), countsText(m))
		s.addIssue(is)
	}
	// The cost of metadata strings, whatever their lengths declare.
	if len(alloc) > 0 {
		is := IssueState{Issue: "#39", What: "a short metadata string costs about its length, not 4 KiB"}

		var parts []string

		over := 0

		for _, r := range alloc {
			if r.Decoder != "fork" || r.Issue != "strings" || r.Entry == "meta.Parse" {
				continue
			}

			if !r.Bounded {
				over++
			}

			parts = append(
				parts,
				fmt.Sprintf("%s via %s: %d bytes for %d input bytes", r.Probe, r.Entry, r.AllocBytes, r.InputBytes),
			)
		}

		is.State = "fixed"
		if over > 0 {
			is.State = "not fixed"
		}

		if len(parts) == 0 {
			is.State = "not tested"
		}

		is.Evidence = strings.Join(parts, "; ")
		s.addIssue(is)
	}
	// #25: metadata allocations.
	if len(alloc) > 0 {
		is := IssueState{Issue: "#25", What: "metadata parsers allocate within the block, not the declared lengths"}
		over, n := 0, 0

		var (
			worst AllocRecord
			first *AllocRecord
		)

		for i, r := range alloc {
			if r.Decoder != "fork" || r.Issue != "#25" {
				continue
			}

			n++

			if !r.Bounded {
				over++
			}

			if r.AllocBytes > worst.AllocBytes {
				worst = r
			}

			if r.Probe == "vorbis-vendor-8-byte-block" && r.Entry == "meta.Parse" {
				first = &alloc[i]
			}
		}

		is.State = "fixed"
		if over > 0 {
			is.State = "not fixed"
		}

		if first != nil {
			is.Evidence = fmt.Sprintf(
				"the 8-byte VORBIS_COMMENT block (vendor length 32 MiB): %d bytes; ",
				first.AllocBytes,
			)
		}

		is.Evidence += fmt.Sprintf("%d of %d fork probes over the bound; the most: %s via %s, %d bytes",
			over, n, worst.Probe, worst.Entry, worst.AllocBytes)
		s.addIssue(is)
	}
	// #13, #14, #17: seeking.
	if len(seek) > 0 {
		is := IssueState{
			Issue: "#17",
			What:  "Seek lands on the frame holding the sample, and the samples after it are libFLAC's (#13, #14)",
		}
		m := map[string]int{}
		bad := 0

		for _, r := range seek {
			if r.Decoder != "fork" {
				continue
			}

			m[r.Verdict]++
			switch r.Verdict {
			case "match", "refused", "libflac_error", "skipped":
			default:
				bad++
			}
		}

		is.State = "fixed"
		if bad > 0 {
			is.State = "not fixed"
		}

		is.Evidence = "fork seeks: " + countsText(m)
		s.addIssue(is)
	}
}

func (s *Summary) summarizeSeek(recs []SeekRecord) {
	if len(recs) == 0 {
		return
	}

	s.Seek = map[string]map[string]int{}
	for _, r := range recs {
		if s.Seek[r.Decoder] == nil {
			s.Seek[r.Decoder] = map[string]int{}
		}

		s.Seek[r.Decoder][r.Verdict]++
		if r.Decoder == "fork" && r.Verdict != "match" && r.Verdict != "refused" && len(s.SeekNotes) < 40 {
			n := fmt.Sprintf("%s/%s Seek(%d): %s", r.Corpus, r.File, r.Target, r.Verdict)
			if r.Error != "" {
				n += ": " + r.Error
			}

			if r.Panic != "" {
				n += ": panic: " + strings.SplitN(r.Panic, "\n", 2)[0]
			}

			s.SeekNotes = append(s.SeekNotes, n)
		}
	}

	for dec, m := range s.Seek {
		for k, n := range m {
			s.Verdicts["seek/"+dec+"/"+k] = fmt.Sprint(n)
		}
	}
}

func (s *Summary) summarizeAlloc(recs []AllocRecord) {
	s.Alloc = recs
	for _, r := range recs {
		v := "bounded"
		if r.Panic != "" {
			v = "panic"
		} else if !r.Bounded {
			v = "over"
		}

		s.Verdicts["alloc/"+r.Probe+"/"+r.Entry+"/"+r.Decoder] = v
	}
}

var statusLine = regexp.MustCompile(`fuzz: elapsed: (\S+), execs: (\d+) .*new interesting: (\d+)`)

func (s *Summary) summarizeFuzz(dir string) {
	logs, _ := filepath.Glob(filepath.Join(dir, "Fuzz*.log"))
	sort.Strings(logs)

	for _, l := range logs {
		f := FuzzSummary{Target: strings.TrimSuffix(filepath.Base(l), ".log")}
		b, _ := os.ReadFile(l)

		lines := strings.Split(string(b), "\n")
		for i, line := range lines {
			if m := statusLine.FindStringSubmatch(line); m != nil {
				f.Elapsed, f.Execs, f.Interesting = m[1], m[2], m[3]
			}

			if strings.HasPrefix(line, "--- FAIL") && !f.Failed {
				f.Failed = true
				end := min(len(lines), i+14)
				f.Failure = strings.TrimSpace(strings.Join(lines[i:end], "\n"))
			}
		}

		crashers, _ := filepath.Glob(filepath.Join(dir, "crashers", f.Target, "*"))
		for _, c := range crashers {
			f.Crashers = append(f.Crashers, filepath.Base(c))
		}

		obs, _ := filepath.Glob(filepath.Join(dir, "observations", f.Target, "*.flac"))
		f.Observations = len(obs)
		allowed, _ := filepath.Glob(filepath.Join(dir, "allowed", f.Target, "*.flac"))
		f.Allowed = len(allowed)
		s.Fuzz = append(s.Fuzz, f)

		failed := "no"
		if f.Failed {
			failed = "yes"
		}

		s.Verdicts["fuzz/"+f.Target+"/failed"] = failed
	}
}

func (s *Summary) text() string {
	var b strings.Builder
	fmt.Fprintf(&b, "fork      %s", s.Fork)

	if s.Label != "" {
		fmt.Fprintf(&b, " (%s)", s.Label)
	}

	fmt.Fprintf(
		&b,
		"\nupstream  %s\nlibFLAC   %s\nffmpeg    %s\ncoreaudio %s\ngo        %s\n\n",
		s.Upstream,
		s.LibFLAC,
		s.FFmpeg,
		s.CoreAudio,
		s.Go,
	)
	fmt.Fprintf(&b, "Decode, each decoder against libFLAC:\n")

	for _, c := range s.Decode {
		fmt.Fprintf(&b, "  %-12s %4d files  fork: %s\n", c.Corpus, c.Files, countsText(c.Fork))
		fmt.Fprintf(&b, "  %-12s             upstream: %s\n", "", countsText(c.Upstream))

		if len(c.FFmpeg) > 0 {
			fmt.Fprintf(&b, "  %-12s             ffmpeg: %s\n", "", countsText(c.FFmpeg))
		}

		if len(c.CoreAudio) > 0 {
			fmt.Fprintf(&b, "  %-12s             coreaudio: %s\n", "", countsText(c.CoreAudio))
		}

		for _, n := range c.NotMatching {
			fmt.Fprintf(&b, "      %s\n", n)
		}
	}

	fmt.Fprintf(&b, "\nIssues:\n")

	for _, is := range s.Issues {
		fmt.Fprintf(&b, "  %-9s %-10s %s\n            %s\n", is.Issue, is.State, is.What, is.Evidence)
	}

	if s.Seek != nil {
		fmt.Fprintf(&b, "\nSeek against flac -d --skip/--until:\n")

		for _, dec := range decoderNames {
			fmt.Fprintf(&b, "  %-9s %s\n", dec, countsText(s.Seek[dec]))
		}

		for _, n := range s.SeekNotes {
			fmt.Fprintf(&b, "      %s\n", n)
		}
	}

	if len(s.Fuzz) > 0 {
		fmt.Fprintf(&b, "\nFuzz:\n")

		for _, f := range s.Fuzz {
			state := "no failure"
			if f.Failed {
				state = "FAILED"
			}

			fmt.Fprintf(
				&b,
				"  %-11s %s, %s execs, %s new interesting: %s; %d observations, %d allowed panics\n",
				f.Target,
				f.Elapsed,
				f.Execs,
				f.Interesting,
				state,
				f.Observations,
				f.Allowed,
			)

			if f.Failed {
				for _, l := range strings.Split(f.Failure, "\n") {
					fmt.Fprintf(&b, "      %s\n", l)
				}
			}
		}
	}

	return b.String()
}

func cmdCompare(args []string) error {
	fl := flag.NewFlagSet("compare", flag.ExitOnError)
	only := fl.String(
		"only",
		"",
		"compare only keys with these prefixes, comma separated (default: every key but fuzz/)",
	)

	_ = fl.Parse(args)
	if fl.NArg() != 2 {
		return errors.New("usage: flacharness compare [-only prefixes] a/summary.json b/summary.json")
	}

	var sa, sb Summary
	if err := readSummary(fl.Arg(0), &sa); err != nil {
		return err
	}

	if err := readSummary(fl.Arg(1), &sb); err != nil {
		return err
	}

	prefixes := []string{}
	if *only != "" {
		prefixes = strings.Split(*only, ",")
	}

	want := func(k string) bool {
		if len(prefixes) == 0 {
			return !strings.HasPrefix(k, "fuzz/")
		}

		for _, p := range prefixes {
			if strings.HasPrefix(k, p) {
				return true
			}
		}

		return false
	}

	keys := map[string]bool{}
	for k := range sa.Verdicts {
		keys[k] = true
	}

	for k := range sb.Verdicts {
		keys[k] = true
	}

	var ks []string

	for k := range keys {
		if want(k) {
			ks = append(ks, k)
		}
	}

	sort.Strings(ks)

	diff := 0

	for _, k := range ks {
		x, y := sa.Verdicts[k], sb.Verdicts[k]
		if x != y {
			diff++

			fmt.Printf("%s: %q -> %q\n", k, x, y)
		}
	}

	fmt.Printf("%d verdicts compared, %d differ\n", len(ks), diff)

	if diff > 0 {
		os.Exit(1)
	}

	return nil
}

func readSummary(p string, s *Summary) error {
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}

	return json.Unmarshal(b, s)
}
