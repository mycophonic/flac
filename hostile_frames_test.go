package flac_test

// Hostile frames against the reference decoder. A one-frame stream is
// built bit by bit and sealed with its CRC-8 and CRC-16, then one field is
// given a value the format reserves, forbids or cannot hold, and both this
// decoder and `flac -t` decide. The decoder may never panic; where the
// table says so, its decision is the reference's.

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/internal/hashutil/crc16"
	"github.com/mycophonic/flac/internal/hashutil/crc8"
)

// bitSink writes big-endian bit fields.
type bitSink struct {
	buf  []byte
	nbit uint
}

func (s *bitSink) put(v uint64, n uint) {
	for i := n; i > 0; i-- {
		if s.nbit%8 == 0 {
			s.buf = append(s.buf, 0)
		}

		if v>>(i-1)&1 == 1 {
			s.buf[len(s.buf)-1] |= 1 << (7 - s.nbit%8)
		}

		s.nbit++
	}
}

func (s *bitSink) bytes(b []byte) {
	for _, c := range b {
		s.put(uint64(c), 8)
	}
}

// hostileFrame is every field of a one-frame mono 16-bit stream of 16
// samples, with the valid values set; a case changes some.
type hostileFrame struct {
	reserved1, reserved2 uint64
	variable             uint64
	blockSizeCode        uint64
	blockSizeExtra       uint64 // the 8 or 16 bits after the number, per code
	sampleRateCode       uint64
	sampleRateExtra      uint64
	channelCode          uint64
	bpsCode              uint64
	number               []byte // the UTF-8 coded frame number
	crc8                 *uint8 // nil: computed
	subPad               uint64
	subType              uint64 // the 6-bit subframe type
	wasted               uint64 // 0: none; k: k wasted bits
	body                 func(s *bitSink)
	framePad             uint64 // bits before the CRC-16, zero normally
	crc16                *uint16
	streamBPS            uint8
	channels             uint64 // STREAMINFO's channel count; 1 unless set
	streamMinMax         uint16
}

func validFrame() hostileFrame {
	return hostileFrame{
		blockSizeCode: 6, blockSizeExtra: 15, // 16 samples
		sampleRateCode: 9, // 44.1 kHz
		channelCode:    0, // mono
		bpsCode:        4, // 16 bits
		number:         []byte{0},
		subType:        1, // verbatim
		body: func(s *bitSink) {
			for i := range 16 {
				s.put(uint64(uint16(int16(i*1000-8000))), 16)
			}
		},
		streamBPS:    16,
		streamMinMax: 16,
	}
}

// build serializes the stream: fLaC, a last STREAMINFO, the frame.
func (h hostileFrame) build() []byte {
	var meta bitSink

	meta.bytes([]byte("fLaC"))
	meta.put(1, 1) // last block
	meta.put(0, 7) // STREAMINFO
	meta.put(34, 24)
	meta.put(uint64(h.streamMinMax), 16)
	meta.put(uint64(h.streamMinMax), 16)
	meta.put(0, 24) // min frame size unknown
	meta.put(0, 24)
	meta.put(44100, 20)
	meta.put(max(h.channels, 1)-1, 3) // channels
	meta.put(uint64(h.streamBPS-1), 5)
	meta.put(16, 36) // 16 samples
	meta.bytes(make([]byte, 16))

	var hdr bitSink

	hdr.put(0x3FFE, 14)
	hdr.put(h.reserved1, 1)
	hdr.put(h.variable, 1)
	hdr.put(h.blockSizeCode, 4)
	hdr.put(h.sampleRateCode, 4)
	hdr.put(h.channelCode, 4)
	hdr.put(h.bpsCode, 3)
	hdr.put(h.reserved2, 1)
	hdr.bytes(h.number)

	switch h.blockSizeCode {
	case 6:
		hdr.put(h.blockSizeExtra, 8)
	case 7:
		hdr.put(h.blockSizeExtra, 16)
	}

	switch h.sampleRateCode {
	case 12:
		hdr.put(h.sampleRateExtra, 8)
	case 13, 14:
		hdr.put(h.sampleRateExtra, 16)
	}

	c8 := crc8.ChecksumATM(hdr.buf)
	if h.crc8 != nil {
		c8 = *h.crc8
	}

	hdr.put(uint64(c8), 8)

	frame := bitSink{buf: append([]byte{}, hdr.buf...), nbit: uint(len(hdr.buf)) * 8}
	frame.put(h.subPad, 1)
	frame.put(h.subType, 6)

	if h.wasted == 0 {
		frame.put(0, 1)
	} else {
		frame.put(1, 1)
		frame.put(0, uint(h.wasted-1))
		frame.put(1, 1)
	}

	h.body(&frame)

	if frame.nbit%8 != 0 {
		frame.put(h.framePad, 8-frame.nbit%8)
	}

	c16 := crc16.ChecksumIBM(frame.buf)
	if h.crc16 != nil {
		c16 = *h.crc16
	}

	frame.put(uint64(c16), 16)

	return append(meta.buf, frame.buf...)
}

// errPanic marks a recovered panic, which is the one outcome the table
// never allows.
var errPanic = errors.New("the decoder panicked")

// decodeOutcome is what this decoder did with the stream: nil for a decoded
// frame, the error, or a recovered panic wrapped in errPanic.
func decodeOutcome(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errPanic, r)
		}
	}()

	stream, err := flac.New(bytes.NewReader(data))
	if err != nil {
		return err
	}

	defer func() { _ = stream.Close() }()

	if _, parseErr := stream.ParseNext(); parseErr != nil {
		return parseErr
	}

	// To the end: a second ParseNext is the clean EOF.
	_, err = stream.ParseNext()
	if errors.Is(err, io.EOF) {
		return nil
	}

	return err
}

// referenceAccepts is `flac -t`'s decision.
func referenceAccepts(t *testing.T, flacBin string, data []byte) bool {
	t.Helper()

	path := filepath.Join(t.TempDir(), "frame.flac")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	return exec.CommandContext(t.Context(), flacBin, "-t", "-s", path).Run() == nil
}

func u8(v uint8) *uint8    { return &v }
func u16(v uint16) *uint16 { return &v }

func TestHostileFramesAgainstTheReference(t *testing.T) {
	t.Parallel()

	// The decoder's half runs everywhere; the reference is consulted where
	// flac is on the PATH (CI installs none).
	flacBin, err := exec.LookPath("flac")
	if err != nil {
		flacBin = ""

		t.Log("no flac on the PATH: the reference is not consulted")
	}

	// wideBody is 20 warm-up samples' worth of bits and more, for predictor
	// subframes whose order exceeds the block: whatever follows the header,
	// the decoder must refuse before indexing past its 16 samples.
	wideBody := func(s *bitSink) {
		for range 64 {
			s.put(0xA5A5, 16)
		}
	}

	cases := []struct {
		name   string
		change func(h *hostileFrame)
		// sameAsReference: the decision must match `flac -t`; otherwise the
		// case only forbids a panic and logs both decisions.
		sameAsReference bool
	}{
		{"valid", func(*hostileFrame) {}, true},
		{"block size code 0000 (reserved)", func(h *hostileFrame) { h.blockSizeCode = 0 }, true},
		{"block size 65536", func(h *hostileFrame) { h.blockSizeCode, h.blockSizeExtra = 7, 0xFFFF }, true},
		{"sample rate code 1111 (invalid)", func(h *hostileFrame) { h.sampleRateCode = 15 }, true},
		{"channel assignment 1011 (reserved)", func(h *hostileFrame) { h.channelCode = 11 }, true},
		{"channel assignment 1111 (reserved)", func(h *hostileFrame) { h.channelCode = 15 }, true},
		{"sample size 011 (reserved)", func(h *hostileFrame) { h.bpsCode = 3 }, true},
		{"reserved bit after the sync set", func(h *hostileFrame) { h.reserved1 = 1 }, true},
		{"reserved bit before the number set", func(h *hostileFrame) { h.reserved2 = 1 }, true},
		// libFLAC accepts an overlong UTF-8 coding of the frame number; this
		// decoder refuses it. The reference is logged, not imposed.
		{"frame number overlong (C0 80 for 0)", func(h *hostileFrame) { h.number = []byte{0xC0, 0x80} }, false},
		{"frame number with a bad continuation", func(h *hostileFrame) { h.number = []byte{0xC2, 0x00} }, true},
		{"frame number lead byte 0xFF", func(h *hostileFrame) { h.number = []byte{0xFF} }, true},
		{"frame number lead byte 0xFF with continuations", func(h *hostileFrame) {
			h.number = []byte{0xFF, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
		}, true},
		{"frame number of 7 bytes in a fixed-blocksize frame", func(h *hostileFrame) {
			h.number = []byte{0xFE, 0x80, 0x80, 0x80, 0x80, 0x80, 0x80}
		}, true},
		{"CRC-8 wrong", func(h *hostileFrame) { h.crc8 = u8(0x00) }, true},
		{"CRC-16 wrong", func(h *hostileFrame) { h.crc16 = u16(0x1234) }, true},
		{"subframe padding bit set", func(h *hostileFrame) { h.subPad = 1 }, true},
		{"subframe type 000010 (reserved)", func(h *hostileFrame) { h.subType = 2 }, true},
		{"subframe type 001101 (fixed order 5, reserved)", func(h *hostileFrame) { h.subType = 13 }, true},
		{"subframe type 010000 (reserved)", func(h *hostileFrame) { h.subType = 16 }, true},
		{"wasted bits equal to the sample size", func(h *hostileFrame) {
			h.wasted = 16
			h.body = wideBody
		}, true},
		// The boundary, both sides: the reference refuses an order equal to
		// the block size (the warm-up would be the whole block, the one
		// partition empty) and accepts one below it.
		{"LPC order 16 on a 16-sample block, equal", func(h *hostileFrame) {
			h.subType = 32 + 15
			h.body = func(s *bitSink) {
				for range 16 {
					s.put(0, 16) // warm-up samples
				}

				s.put(14, 4) // coefficient precision 15
				s.put(0, 5)  // shift

				for range 16 {
					s.put(0, 15) // coefficients
				}

				s.put(0, 2) // rice1
				s.put(0, 4) // partition order 0
				s.put(0, 4) // rice parameter; no residuals follow
			}
		}, true},
		{"LPC order 15 on a 16-sample block, one residual", func(h *hostileFrame) {
			h.subType = 32 + 14
			h.body = func(s *bitSink) {
				for range 15 {
					s.put(0, 16)
				}

				s.put(14, 4)
				s.put(0, 5)

				for range 15 {
					s.put(0, 15)
				}

				s.put(0, 2)
				s.put(0, 4)
				s.put(0, 4)
				s.put(1, 1) // one residual of 0 under Rice parameter 0
			}
		}, true},
		// The 33-bit side channel of a 32-bit mid/side frame decodes on the
		// wide path, which refuses the same two subframes: the mid is 16
		// verbatim samples, the side's header follows in the body.
		{"LPC order 20 on the 33-bit side of a 16-sample mid/side block", func(h *hostileFrame) {
			h.streamBPS, h.bpsCode, h.channels, h.channelCode = 32, 7, 2, 10
			h.body = func(s *bitSink) {
				for range 16 {
					s.put(0, 32) // the mid, verbatim
				}

				s.put(0, 1)     // the side's padding bit
				s.put(32+19, 6) // LPC order 20
				s.put(0, 1)     // no wasted bits
				wideBody(s)
			}
		}, true},
		// 33 wasted bits at the side's 33-bit depth leave a 0-bit sample: the
		// verbatim samples read nothing, the frame aligns and its CRC holds,
		// so the decoder accepted it where the reference refuses it.
		{"33 wasted bits on the 33-bit side of a 16-sample mid/side block", func(h *hostileFrame) {
			h.streamBPS, h.bpsCode, h.channels, h.channelCode = 32, 7, 2, 10
			h.body = func(s *bitSink) {
				for range 16 {
					s.put(0, 32)
				}

				s.put(0, 1)  // padding bit
				s.put(1, 6)  // verbatim
				s.put(1, 1)  // wasted bits follow
				s.put(0, 32) // unary: 33 wasted bits
				s.put(1, 1)  // the terminator; nothing follows, each sample is 0 bits
			}
		}, true},
		// The order boundary on the wide path: 16 on 16 is refused as on the
		// narrow one (the reference refuses it), the body otherwise whole.
		{"LPC order 16 on the 33-bit side of a 16-sample mid/side block, equal", func(h *hostileFrame) {
			h.streamBPS, h.bpsCode, h.channels, h.channelCode = 32, 7, 2, 10
			h.body = func(s *bitSink) {
				for range 16 {
					s.put(0, 32) // the mid, verbatim
				}

				s.put(0, 1)     // padding bit
				s.put(32+15, 6) // LPC order 16
				s.put(0, 1)     // no wasted bits

				for range 16 {
					s.put(0, 33) // warm-up samples at the side's depth
				}

				s.put(14, 4) // coefficient precision 15
				s.put(0, 5)  // shift

				for range 16 {
					s.put(0, 15) // coefficients
				}

				s.put(0, 2) // rice1
				s.put(0, 4) // partition order 0
				s.put(0, 4) // Rice parameter; no residuals follow
			}
		}, true},
		{"LPC order 20 on a 16-sample block", func(h *hostileFrame) {
			h.subType = 32 + 19
			h.body = wideBody
		}, true},
		{"LPC order 32 on a 16-sample block", func(h *hostileFrame) {
			h.subType = 63
			h.body = wideBody
		}, true},
		{"fixed order 4 on a 2-sample block", func(h *hostileFrame) {
			h.blockSizeExtra = 1
			h.subType = 8 + 4
			h.body = wideBody
		}, true},
		{"LPC coefficient precision 1111 (invalid)", func(h *hostileFrame) {
			h.subType = 32 // order 1
			h.body = func(s *bitSink) {
				s.put(0, 16) // warm-up
				s.put(15, 4) // precision
				s.put(0, 5)  // shift
				s.put(0, 16) // a coefficient of 16 bits, if read
				s.put(0, 2)  // residual method
				s.put(0, 4)  // partition order 0
				s.put(0, 4)  // rice parameter 0
				s.put(0, 64) // residuals
			}
		}, true},
		{"LPC negative shift", func(h *hostileFrame) {
			h.subType = 32 // order 1
			h.body = func(s *bitSink) {
				s.put(0, 16)   // warm-up
				s.put(11, 4)   // precision 12
				s.put(0x1F, 5) // shift -1
				s.put(1, 12)   // coefficient 1
				s.put(0, 2)    // rice1
				s.put(0, 4)    // partition order 0
				s.put(0, 4)    // rice parameter 0

				for range 15 {
					s.put(1, 1) // residual 0: unary 1
				}
			}
		}, false},
		{"residual coding method 10 (reserved)", func(h *hostileFrame) {
			h.subType = 8 // fixed order 0
			h.body = func(s *bitSink) {
				s.put(2, 2)
				s.put(0, 4)
				s.put(0, 4)
				s.put(0, 64)
			}
		}, true},
		{"rice partition order 5 on a 16-sample block", func(h *hostileFrame) {
			h.subType = 8 // fixed order 0
			h.body = func(s *bitSink) {
				s.put(0, 2)
				s.put(5, 4)
				s.put(0, 64)
			}
		}, true},
		{"non-zero padding before the CRC-16", func(h *hostileFrame) {
			h.subType = 8 // fixed order 0: 16 residuals of 0 under rice parameter 0 is 16 bits, then 7 pad bits
			h.body = func(s *bitSink) {
				s.put(0, 2)
				s.put(0, 4)
				s.put(0, 4)

				for range 16 {
					s.put(1, 1)
				}
			}
			h.framePad = 0x7F
		}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			h := validFrame()
			c.change(&h)
			data := h.build()

			ours := decodeOutcome(data)
			if errors.Is(ours, errPanic) || errors.Is(ours, io.EOF) {
				t.Fatalf("the decoder: %v", ours)
			}

			if flacBin == "" {
				t.Logf("this decoder: %v", ours)

				return
			}

			reference := referenceAccepts(t, flacBin, data)

			t.Logf("this decoder: %v; the reference accepts: %v", ours, reference)

			if c.sameAsReference && (ours == nil) != reference {
				t.Fatalf("this decoder decides %v, the reference accepts: %v", ours == nil, reference)
			}
		})
	}
}
