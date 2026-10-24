package flac_test

// metaflac, the reference, as the oracle for the metadata parser: over the
// IETF test files, every block the parser reads is held to what `metaflac
// --list` prints for it: the block sequence (type, length, is-last), the
// StreamInfo fields and MD5, the Vorbis vendor and comments, the seek
// points, the picture's fields and data length, the application id, and
// the padding length.

import (
	"bufio"
	"encoding/hex"
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/meta"
)

// listed is one metadata block as metaflac prints it: the header line
// values and the body lines that follow, keyed by their label.
type listed struct {
	typ    int
	isLast bool
	length int64
	fields map[string]string
	// comments and points keep their order.
	comments []string
	points   []string
}

var (
	// An application block's raw data ends without a newline, so the next
	// header can trail it on the same line.
	blockHeader = regexp.MustCompile(`METADATA block #(\d+)$`)
	fieldLine   = regexp.MustCompile(`^ {2}([^:]+): ?(.*)$`)
	commentLine = regexp.MustCompile(`^ {4}comment\[\d+\]: (.*)$`)
	pointLine   = regexp.MustCompile(`^ {4}point \d+: (.*)$`)
)

// metaflacList runs metaflac --list and parses it.
func metaflacList(t *testing.T, metaflacBin, path string) []listed {
	t.Helper()

	out, err := exec.CommandContext(t.Context(), metaflacBin, "--list", path).Output()
	if err != nil {
		t.Skipf("metaflac --list %s: %v", filepath.Base(path), err)
	}

	var blocks []listed

	inData := false
	scanner := bufio.NewScanner(strings.NewReader(string(out)))
	// A comment or a picture line can run to megabytes.
	scanner.Buffer(make([]byte, 0, 1<<20), 64<<20)

	for scanner.Scan() {
		line := scanner.Text()

		if blockHeader.MatchString(line) {
			blocks = append(blocks, listed{fields: map[string]string{}})
			inData = false

			continue
		}

		if len(blocks) == 0 {
			continue
		}

		block := &blocks[len(blocks)-1]

		switch {
		case inData:
			// The picture's hex dump, or an application's raw data, until
			// the next block header.
		case commentLine.MatchString(line):
			block.comments = append(block.comments, commentLine.FindStringSubmatch(line)[1])
		case pointLine.MatchString(line):
			block.points = append(block.points, pointLine.FindStringSubmatch(line)[1])
		case fieldLine.MatchString(line):
			m := fieldLine.FindStringSubmatch(line)
			key, value := m[1], m[2]
			inData = false

			switch key {
			case "type":
				if _, seen := block.fields["type"]; !seen {
					block.typ, _ = strconv.Atoi(strings.Fields(value)[0])
				}
			case "is last":
				block.isLast = value == "true"
			case "length":
				block.length, _ = strconv.ParseInt(value, 10, 64)
			case "data", "data contents":
				inData = true
			}

			if _, seen := block.fields[key]; !seen {
				block.fields[key] = value
			} else {
				// A picture repeats "type": keep the second under its own key.
				block.fields["picture "+key] = value
			}
		}
	}

	if err := scanner.Err(); err != nil {
		t.Fatalf("reading metaflac's listing: %v", err)
	}

	return blocks
}

// number is a listed field's leading number.
func number(value string) string {
	return strings.Fields(value + " ")[0]
}

func TestMetadataMatchesMetaflac(t *testing.T) {
	t.Parallel()

	metaflacBin, err := exec.LookPath("metaflac")
	if err != nil {
		t.Skip("no metaflac on the PATH")
	}

	var files []string

	for _, set := range []string{"subset", "uncommon", "faulty"} {
		matches, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", set, "*.flac"))
		files = append(files, matches...)
	}

	if len(files) == 0 {
		t.Skip("no IETF test files under testdata/flac-test-files")
	}

	for _, path := range files {
		t.Run(filepath.Base(path), func(t *testing.T) {
			t.Parallel()

			stream, err := flac.ParseFile(path)
			if err != nil {
				// What the parser refuses, metaflac may list; the faulty set
				// is made of such files. The refusal is the owner's contract,
				// held by the conformance tests; here only what parses is
				// compared.
				t.Skipf("the parser refuses it: %v", err)
			}

			defer func() { _ = stream.Close() }()

			want := metaflacList(t, metaflacBin, path)

			// StreamInfo is the first block, kept apart by the parser.
			first := &meta.Block{
				Header: meta.Header{Type: meta.TypeStreamInfo, Length: 34, IsLast: len(stream.Blocks) == 0},
				Body:   stream.Info,
			}
			got := append([]*meta.Block{first}, stream.Blocks...)

			if len(got) != len(want) {
				t.Fatalf("%d blocks parsed, metaflac lists %d", len(got), len(want))
			}

			for i, block := range got {
				ref := want[i]
				label := fmt.Sprintf("block %d", i)

				if int(block.Type) != ref.typ || block.Length != ref.length || block.IsLast != ref.isLast {
					t.Errorf("%s: type %d length %d last %v, metaflac: type %d length %d last %v",
						label, block.Type, block.Length, block.IsLast, ref.typ, ref.length, ref.isLast)
				}

				compareBody(t, label, block, ref)
			}
		})
	}
}

func compareBody(t *testing.T, label string, block *meta.Block, ref listed) {
	t.Helper()

	check := func(name, got, want string) {
		t.Helper()

		if got == want {
			return
		}

		if len(got) > 120 || len(want) > 120 {
			at := 0
			for at < len(got) && at < len(want) && got[at] == want[at] {
				at++
			}

			t.Errorf("%s %s: %d bytes against metaflac's %d, first difference at %d: %q against %q",
				label, name, len(got), len(want), at, clip(got[at:]), clip(want[at:]))

			return
		}

		t.Errorf("%s %s: %q, metaflac %q", label, name, got, want)
	}

	switch body := block.Body.(type) {
	case *meta.StreamInfo:
		check("minimum blocksize", strconv.Itoa(int(body.BlockSizeMin)), number(ref.fields["minimum blocksize"]))
		check("maximum blocksize", strconv.Itoa(int(body.BlockSizeMax)), number(ref.fields["maximum blocksize"]))
		check("minimum framesize", strconv.Itoa(int(body.FrameSizeMin)), number(ref.fields["minimum framesize"]))
		check("maximum framesize", strconv.Itoa(int(body.FrameSizeMax)), number(ref.fields["maximum framesize"]))
		check("sample rate", strconv.Itoa(int(body.SampleRate)), number(ref.fields["sample_rate"]))
		check("channels", strconv.Itoa(int(body.NChannels)), number(ref.fields["channels"]))
		check("bits per sample", strconv.Itoa(int(body.BitsPerSample)), number(ref.fields["bits-per-sample"]))
		check("total samples", strconv.FormatUint(body.NSamples, 10), number(ref.fields["total samples"]))
		check("md5", hex.EncodeToString(body.MD5sum[:]), ref.fields["MD5 signature"])
	case *meta.VorbisComment:
		check("vendor", body.Vendor, ref.fields["vendor string"])
		check("comments", strconv.Itoa(len(body.Tags)), number(ref.fields["comments"]))

		// metaflac prints a comment's own newlines, so a listing line is the
		// comment's first line; the whole comment is compared by its length
		// against the block's, below, through the count and the lines.
		for i, tag := range body.Tags {
			if i < len(ref.comments) {
				first, _, _ := strings.Cut(tag[0]+"="+tag[1], "\n")
				check(fmt.Sprintf("comment %d", i), first, ref.comments[i])
			}
		}
	case *meta.SeekTable:
		check("seek points", strconv.Itoa(len(body.Points)), number(ref.fields["seek points"]))

		for i, p := range body.Points {
			if i >= len(ref.points) {
				break
			}

			want := ref.points[i]
			if p.SampleNum == 0xFFFFFFFFFFFFFFFF {
				check(fmt.Sprintf("point %d", i), "PLACEHOLDER", want)

				continue
			}

			got := fmt.Sprintf("sample_number=%d, stream_offset=%d, frame_samples=%d",
				p.SampleNum, p.Offset, p.NSamples)
			check(fmt.Sprintf("point %d", i), got, want)
		}
	case *meta.Picture:
		check("picture type", strconv.Itoa(int(body.Type)), number(ref.fields["picture type"]))
		check("mime", body.MIME, ref.fields["MIME type"])
		check("description", body.Desc, ref.fields["description"])
		check("width", strconv.Itoa(int(body.Width)), number(ref.fields["width"]))
		check("height", strconv.Itoa(int(body.Height)), number(ref.fields["height"]))
		check("depth", strconv.Itoa(int(body.Depth)), number(ref.fields["depth"]))
		check("colors", strconv.Itoa(int(body.NPalColors)), number(ref.fields["colors"]))
		check("data length", strconv.Itoa(len(body.Data)), number(ref.fields["data length"]))
	case *meta.Application:
		check("application id", hexID(body.ID), ref.fields["application ID"])
	case *meta.CueSheet:
		check("tracks", strconv.Itoa(len(body.Tracks)), number(ref.fields["number of tracks"]))
	case nil:
		// Padding carries nothing; its length was compared above.
	default:
		t.Logf("%s: body %T not compared", label, body)
	}
}

func clip(s string) string {
	if len(s) > 40 {
		return s[:40] + "..."
	}

	return s
}

// hexID is an application id as metaflac prints it, four hex bytes.
func hexID(id uint32) string {
	return fmt.Sprintf("%08x", id)
}
