package main

import (
	"bytes"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// TriageRecord is one replayed input: the stream it is, as hex when small,
// and the three decoders' verdicts on it.
type TriageRecord struct {
	Input     string       `json:"input"`
	Target    string       `json:"target,omitempty"`
	Stream    string       `json:"stream_file"`
	StreamHex string       `json:"stream_hex,omitempty"`
	Bytes     int          `json:"bytes"`
	Decode    DecodeRecord `json:"decode"`
}

// readFuzzInput reads a file in Go's corpus format (go test fuzz v1, one
// []byte value) or, failing that, takes the file's bytes as they are.
func readFuzzInput(p string) ([]byte, error) {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}

	const header = "go test fuzz v1\n"
	if !bytes.HasPrefix(b, []byte(header)) {
		return b, nil
	}

	s := strings.TrimSpace(strings.TrimPrefix(string(b), header))
	if !strings.HasPrefix(s, "[]byte(") || !strings.HasSuffix(s, ")") {
		return nil, errors.New("not a single []byte corpus entry")
	}

	v, err := strconv.Unquote(strings.TrimSuffix(strings.TrimPrefix(s, "[]byte("), ")"))
	if err != nil {
		return nil, err
	}

	return []byte(v), nil
}

func cmdTriage(args []string) error {
	fl := flag.NewFlagSet("triage", flag.ExitOnError)
	out := fl.String("out", "-", "output, JSON lines")
	dir := fl.String("dir", "", "where the replayed streams are written as .flac files (required)")
	target := fl.String(
		"target",
		"",
		"the fuzz target the inputs came from; FuzzFrame inputs are built into their stream first",
	)
	maxHex := fl.Int("hex", 4096, "streams up to this many bytes are kept as hex")
	_ = fl.Parse(args)

	if *dir == "" {
		return errors.New("triage: -dir is required")
	}

	if err := os.MkdirAll(*dir, 0o755); err != nil {
		return err
	}

	w, err := createOut(*out)
	if err != nil {
		return err
	}
	defer w.Close()

	jl := newJSONLines(w)
	lib := newLibFLAC()

	for _, p := range fl.Args() {
		in, err := readFuzzInput(p)
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}

		stream := in
		if *target == "FuzzFrame" && !bytes.HasPrefix(in, []byte("fLaC")) {
			stream = buildStream(in)
		}

		name := strings.TrimSuffix(filepath.Base(p), filepath.Ext(p)) + ".flac"
		if err := os.WriteFile(filepath.Join(*dir, name), stream, 0o644); err != nil {
			return err
		}

		rec := TriageRecord{Input: filepath.Base(p), Target: *target, Stream: name, Bytes: len(stream)}
		if len(stream) <= *maxHex {
			rec.StreamHex = hex.EncodeToString(stream)
		}

		rec.Decode = decodeOne(lib, inputFile{corpus: "triage", root: *dir, rel: name}, time.Minute)
		if err := jl.write(rec); err != nil {
			return err
		}
	}

	return nil
}
