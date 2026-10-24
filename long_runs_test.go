package flac_test

// Codes that never end. A rice partition whose first residual's unary run
// is a megabyte of zero bits, a wasted-bits count coded the same way, and
// a frame whose sample data is cut mid-code: the decoder must come back
// with an error in a bounded time, not read on forever or square its work.

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/mycophonic/flac"
)

func TestLongRunsEndInAnErrorInTime(t *testing.T) {
	t.Parallel()

	zeros := func(n int) func(s *bitSink) {
		return func(s *bitSink) {
			s.put(0, 2) // rice1
			s.put(0, 4) // partition order 0
			s.put(0, 4) // rice parameter 0
			s.bytes(make([]byte, n))
		}
	}

	cases := []struct {
		name   string
		change func(h *hostileFrame)
	}{
		{"a megabyte of zero bits in a unary residual", func(h *hostileFrame) {
			h.subType = 8 // fixed order 0
			h.body = zeros(1 << 20)
		}},
		{"a megabyte of zero bits in a wasted-bits count", func(h *hostileFrame) {
			h.wasted = 1 << 23
			h.body = func(*bitSink) {}
		}},
		{"the stream cut inside a residual", func(h *hostileFrame) {
			h.subType = 8
			h.body = func(s *bitSink) {
				s.put(0, 2)
				s.put(0, 4)
				s.put(0, 4)
				s.put(1, 1) // one residual, then nothing
			}
		}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			h := validFrame()
			c.change(&h)
			data := h.build()

			if c.name == "the stream cut inside a residual" {
				data = data[:len(data)-2] // no CRC-16 either
			}

			start := time.Now()
			done := make(chan error, 1)

			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("%w: %v", errPanic, r)
					}
				}()

				stream, err := flac.New(bytes.NewReader(data))
				if err != nil {
					done <- err

					return
				}

				defer func() { _ = stream.Close() }()

				_, err = stream.ParseNext()
				done <- err
			}()

			select {
			case err := <-done:
				if err == nil {
					t.Fatal("decoded")
				}

				if errors.Is(err, errPanic) {
					t.Fatal(err)
				}

				t.Logf("%v after %v for %d bytes", err, time.Since(start).Round(time.Millisecond), len(data))
			case <-time.After(10 * time.Second):
				t.Fatalf("no answer after 10 s for %d bytes", len(data))
			}
		})
	}
}
