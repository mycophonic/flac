package main

import (
	"bytes"
	"context"
	"os/exec"
	"testing"
	"time"
)

// TestBuildStream checks that buildStream writes streams decoders accept:
// FuzzFrame is only useful if most of its streams pass the CRC checks and
// reach the subframe decoders. With flac on PATH, it also holds the fork to
// libFLAC on the streams both accept whose samples fit their depth, at 24
// bits or less (at 32 bits libFLAC 1.5.0 does not bound-check samples, so
// an invalid stream may decode differently under each).
func TestBuildStream(t *testing.T) {
	const n = 3000
	forkOK, libOK, both, same := 0, 0, 0, 0
	var differ []int
	_, err := exec.LookPath(newLibFLAC().path)
	haveFLAC := err == nil
	lib := newLibFLAC()
	for i := 0; i < n; i++ {
		stream := buildStream(seedBytes(i, 32+i%512))
		r := decodeAll("fork", stream)
		ok := r.err == "" && r.panic == "" && len(r.frames) > 0
		if ok {
			forkOK++
		}
		if !haveFLAC || i%3 != 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		pcm, lr := lib.decodeBytes(ctx, stream)
		cancel()
		if !lr.OK {
			continue
		}
		libOK++
		if !ok || r.outOfRange || r.bps > 24 {
			continue
		}
		both++
		var got []byte
		d, err := openDecoder("fork", bytes.NewReader(stream))
		if err != nil {
			continue
		}
		w := sampleWidth(int(d.Info().BitsPerSample))
		for {
			fr, err := d.Next()
			if err != nil {
				break
			}
			got = append(got, pack(nil, fr, w)...)
		}
		if bytes.Equal(got, pcm) {
			same++
		} else {
			differ = append(differ, i)
		}
	}
	t.Logf(
		"fork accepts %d of %d built streams; libFLAC accepts %d of %d tried; of those both accept, in range at 24 bits or less: %d, equal samples %d",
		forkOK,
		n,
		libOK,
		n/3,
		both,
		same,
	)
	if len(differ) > 0 {
		t.Errorf(
			"the fork's samples differ from libFLAC's on the streams built from seedBytes(i, 32+i%%512) for i in %v",
			differ,
		)
	}
	if forkOK < n/20 {
		t.Fatalf("the fork accepts only %d of %d built streams: the builder is broken", forkOK, n)
	}
	if haveFLAC && libOK < n/3/20 {
		t.Fatalf("libFLAC accepts only %d of %d built streams: the builder is broken", libOK, n/3)
	}
}
