package flac_test

// Hostile metadata against the reference decoder and against memory. A
// stream of fLaC, STREAMINFO, one hostile block and one valid frame is
// parsed with Parse (which reads every block) and tested with `flac -t`;
// the parser may never panic, and where the table says so its decision is
// the reference's. Lengths a block declares (a vendor string, a comment, a
// MIME type, a picture's data, a seek table) are untrusted: parsing a
// stream of a few dozen bytes must not allocate what the lengths say.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"testing"

	"github.com/mycophonic/flac"
)

// be32 and le32 append a 32-bit number.
func be32(b []byte, v uint32) []byte { return binary.BigEndian.AppendUint32(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

// lastBlock is a last metadata block header: type, 24-bit length.
func lastBlock(typ byte, length int) []byte {
	return []byte{0x80 | typ, byte(length >> 16), byte(length >> 8), byte(length)}
}

// streamWith is the valid one-frame stream with the hostile block, and
// its header, inserted after a non-last STREAMINFO; the hostile block is
// the last one, the frame follows it.
func streamWith(block []byte) []byte {
	whole := validFrame().build()
	streamInfo := append([]byte{}, whole[4:42]...)
	streamInfo[0] &= 0x7F // not last

	out := append([]byte("fLaC"), streamInfo...)
	out = append(out, block...)

	return append(out, whole[42:]...)
}

// vorbis is a Vorbis comment block body with an empty vendor string.
func vorbis(vendorLen, count uint32, vectors ...[2]uint32) []byte {
	b := le32(nil, vendorLen)
	b = le32(b, count)

	for _, v := range vectors {
		b = le32(b, v[0])
		b = append(b, bytes.Repeat([]byte{'a'}, int(v[1]))...)
	}

	return b
}

// vorbisWith is a Vorbis comment block body with one comment as given.
func vorbisWith(vendorLen uint32, vendor, comment string) []byte {
	b := le32(nil, vendorLen)
	b = append(b, vendor...)
	b = le32(b, 1)
	b = le32(b, uint32(len(comment)))

	return append(b, comment...)
}

// picture is a picture block body with the given declared lengths and the
// bytes actually present for each.
func picture(mimeLen, mimePresent, descLen, descPresent, dataLen, dataPresent uint32) []byte {
	b := be32(nil, 3) // front cover
	b = be32(b, mimeLen)
	b = append(b, bytes.Repeat([]byte{'m'}, int(mimePresent))...)
	b = be32(b, descLen)
	b = append(b, bytes.Repeat([]byte{'d'}, int(descPresent))...)
	b = be32(b, 1)
	b = be32(b, 1)
	b = be32(b, 24)
	b = be32(b, 0)
	b = be32(b, dataLen)

	return append(b, make([]byte, dataPresent)...)
}

func parseOutcome(data []byte) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("%w: %v", errPanic, r)
		}
	}()

	stream, err := flac.Parse(bytes.NewReader(data))
	if err != nil {
		return err
	}

	defer func() { _ = stream.Close() }()

	_, err = stream.ParseNext()

	return err
}

// allocated is the bytes the function allocates: the total allocated
// before and after, which the collector does not change; the test is not
// parallel so no other test allocates meanwhile.
func allocated(f func()) uint64 {
	var before, after runtime.MemStats

	runtime.ReadMemStats(&before)
	f()
	runtime.ReadMemStats(&after)

	return after.TotalAlloc - before.TotalAlloc
}

//nolint:paralleltest // allocation is measured process-wide; no other test may run meanwhile
func TestHostileMetadataAgainstTheReference(t *testing.T) {
	flacBin, err := exec.LookPath("flac")
	if err != nil {
		t.Skip("no reference flac on the PATH")
	}

	// A few dozen bytes of stream may cost a few dozen kilobytes to parse,
	// never the megabytes or gigabytes the declared lengths name.
	const allocationBound = 1 << 20

	cases := []struct {
		name            string
		block           []byte
		sameAsReference bool
	}{
		{"a valid comment", append(lastBlock(4, 4+4+4+4+3), vorbisWith(4, "test", "a=b")...), true},
		{
			"vendor length 0xFFFFFFFF in a 12-byte block",
			append(lastBlock(4, 12), vorbis(0xFFFFFFFF, 0)...),
			true,
		},
		{"vendor length past the block", append(lastBlock(4, 8), vorbis(100, 0)...), true},
		{
			"a comment of 0xFFFFFFF0 bytes in a 16-byte block",
			append(lastBlock(4, 16), vorbis(0, 1, [2]uint32{0xFFFFFFF0, 4})...),
			true,
		},
		{
			"comment count 0xFFFFFFFF with none present",
			append(lastBlock(4, 8), vorbis(0, 0xFFFFFFFF)...),
			true,
		},
		{"a comment without '='", append(lastBlock(4, 15), vorbis(0, 1, [2]uint32{3, 3})...), false},
		{"MIME length 0xFFFFFFFF", append(lastBlock(6, 40), picture(0xFFFFFFFF, 0, 0, 0, 0, 0)...), true},
		{
			"description length 0xFFFFFFF0",
			append(lastBlock(6, 40), picture(0, 0, 0xFFFFFFF0, 0, 0, 0)...),
			true,
		},
		{
			"picture data of 100 MB in a 40-byte block",
			append(lastBlock(6, 40), picture(0, 0, 0, 0, 100<<20, 8)...),
			true,
		},
		{
			"a seek table of 16 MB declared, 36 bytes present",
			append(lastBlock(3, 0xFFFFFF), make([]byte, 36)...),
			true,
		},
		{
			"a seek table whose length is not a multiple of 18",
			append(lastBlock(3, 20), make([]byte, 20)...),
			true,
		},
		{"an application block of 2 bytes", append(lastBlock(2, 2), 'a', 'b'), true},
		{"a padding block with a non-zero byte", append(lastBlock(1, 8), 0, 0, 0, 1, 0, 0, 0, 0), false},
		// RFC 9639 forbids type 127; libFLAC skips it. The refusal is logged.
		{"block type 127 (invalid)", append(lastBlock(127, 4), 0, 0, 0, 0), false},
		{"block type 100 (reserved), skipped", append(lastBlock(100, 4), 0, 0, 0, 0), true},
		{"block length past the end of the stream", lastBlock(1, 0xFFFFFF), true},
		{"a second STREAMINFO", append(lastBlock(0, 34), validFrame().build()[8:42]...), true},
	}

	for _, c := range cases { //nolint:paralleltest // see above: the cases measure one at a time
		t.Run(c.name, func(t *testing.T) {
			data := streamWith(c.block)

			var ours error

			bytesAllocated := allocated(func() { ours = parseOutcome(data) })

			if errors.Is(ours, errPanic) {
				t.Fatalf("the parser: %v", ours)
			}

			reference := referenceAccepts(t, flacBin, data)

			t.Logf(
				"this decoder: %v; the reference accepts: %v; %d bytes allocated for %d of stream",
				ours,
				reference,
				bytesAllocated,
				len(data),
			)

			if bytesAllocated > allocationBound {
				t.Fatalf("parsing %d bytes of stream allocated %d bytes", len(data), bytesAllocated)
			}

			if c.sameAsReference && (ours == nil) != reference {
				t.Fatalf("this decoder decides %v, the reference accepts: %v", ours == nil, reference)
			}
		})
	}
}
