package flac_test

// FuzzMetadata feeds arbitrary bytes, as the metadata section after the
// fLaC marker, to the three ways the package reads metadata: Parse, which
// walks and parses every block, NewSeek, which walks them for the seek
// table, and meta.New with Block.Parse on the bytes as one block. A
// section may be refused anywhere; the parsers may never panic, and the
// bytes they allocate while reading it are bounded by its length, since the
// lengths a block declares are untrusted. The seeds are the metadata
// sections of the IETF subset files under 256 KiB and blocks whose
// declared lengths dwarf their bytes.

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/mycophonic/flac"
	"github.com/mycophonic/flac/meta"
)

// metadataSection is the bytes between the fLaC marker and the first
// frame, walked by the block headers.
func metadataSection(data []byte) []byte {
	pos := 4
	for pos+4 <= len(data) {
		last := data[pos]&0x80 != 0
		length := int(data[pos+1])<<16 | int(data[pos+2])<<8 | int(data[pos+3])
		pos += 4 + length

		if last {
			break
		}
	}

	if pos > len(data) {
		pos = len(data)
	}

	return data[4:pos]
}

// fuzzBlock is a last metadata block of the given type with the body.
func fuzzBlock(typ byte, body []byte) []byte {
	n := len(body)

	return append([]byte{0x80 | typ, byte(n >> 16), byte(n >> 8), byte(n)}, body...)
}

// streamInfoBlock is a valid, non-last STREAMINFO for a mono 16-bit stream.
func streamInfoBlock() []byte {
	body := []byte{0, 16, 0, 16, 0, 0, 0, 0, 0, 0, 0x0A, 0xC4, 0x40, 0xF0, 0, 0, 0, 16}
	body = append(body, make([]byte, 16)...)

	return append([]byte{0, 0, 0, 34}, body...)
}

func allocatedDuring(fn func()) uint64 {
	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)
	fn()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

// The allocation bound reads the process-wide total, so this target and
// first_block_test.go run sequentially: a t.Parallel on either would make
// the bound flaky.
func FuzzMetadata(f *testing.F) {
	files, _ := filepath.Glob(filepath.Join("testdata", "flac-test-files", "subset", "*.flac"))
	for _, path := range files {
		data, err := os.ReadFile(path)
		if err != nil || len(data) < 42 {
			continue
		}

		if section := metadataSection(data); len(section) <= 256<<10 {
			f.Add(section)
		}
	}

	be32 := binary.BigEndian.AppendUint32
	le32 := binary.LittleEndian.AppendUint32

	// afterInfo is a fresh section: the STREAMINFO, then the block.
	afterInfo := func(block []byte) []byte {
		return append(streamInfoBlock(), block...)
	}

	pictureHead := be32(be32(be32(be32(be32(be32(be32(be32(nil, 3), 0), 0), 1), 1), 24), 0), 100<<20)

	// Declared lengths far beyond the bytes present.
	f.Add(afterInfo(fuzzBlock(4, le32(nil, 0xFFFFFFFF))))                   // vendor length
	f.Add(afterInfo(fuzzBlock(4, le32(le32(le32(nil, 0), 1), 0xFFFFFFF0)))) // comment length
	f.Add(afterInfo(fuzzBlock(6, be32(be32(nil, 3), 0xFFFFFFFF))))          // MIME length
	f.Add(afterInfo(fuzzBlock(6, pictureHead)))                             // picture data of 100 MiB
	f.Add(afterInfo([]byte{0x83, 0xFF, 0xFF, 0xFF, 0, 0, 0, 0}))            // seek table of 16 MiB declared
	// A seek table of 932,067 whole points with 8 bytes present; a comment
	// block of 6.4 MB declared with a 4.3 MB vendor inside and 4 bytes present;
	// a picture block of 6 MiB declared with 5 MiB of data inside and nothing
	// present: the growing reads, not the bounds.
	f.Add(afterInfo([]byte{0x83, 0xFF, 0xFF, 0xF6, 0, 0, 0, 0}))
	f.Add(afterInfo([]byte{0x84, 0x62, 0x30, 0x30, 0x30, 0x20, 0x41, 0x00}))
	f.Add(afterInfo(append([]byte{0x86, 0x60, 0x00, 0x00},
		be32(be32(be32(be32(be32(be32(be32(be32(nil, 3), 0), 0), 1), 1), 24), 0), 5<<20)...)))
	f.Add(afterInfo([]byte{0x81, 0xFF, 0xFF, 0xFF})) // padding past the end
	f.Add(streamInfoBlock())
	f.Add([]byte{})

	f.Fuzz(func(t *testing.T, data []byte) {
		stream := append([]byte("fLaC"), data...)

		// A few MiB for the parsers' own buffers, then a modest multiple of
		// the input: never what a declared length says.
		bound := uint64(4<<20) + 16*uint64(len(data))
		if raceDetector {
			// The detector's shadow memory and its own allocations ride
			// along; the bound doubles so the parsers' share still tells.
			bound *= 2
		}

		check := func(name string, fn func()) {
			if got := allocatedDuring(fn); got > bound {
				t.Fatalf("%s allocated %d bytes for %d bytes of metadata (bound %d)", name, got, len(data), bound)
			}
		}

		check("Parse", func() {
			if s, err := flac.Parse(bytes.NewReader(stream)); err == nil {
				_ = s.Close()
			}
		})

		check("NewSeek", func() {
			if s, err := flac.NewSeek(bytes.NewReader(stream)); err == nil {
				_ = s.Close()
			}
		})

		check("meta.New and Parse", func() {
			if block, err := meta.New(bytes.NewReader(data)); err == nil {
				_ = block.Parse()
			}
		})
	})
}
