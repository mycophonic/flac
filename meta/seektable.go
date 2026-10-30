package meta

import (
	"encoding/binary"
	"errors"
	"fmt"
)

const (
	maxSeekPoints = 1000000
	// initialSeekPoints is the capacity a seek table starts with: a block's
	// declared length sizes nothing before its bytes arrive.
	initialSeekPoints = 256
)

// SeekTable contains one or more pre-calculated audio frame seek points.
//
// ref: https://www.xiph.org/flac/format.html#metadata_block_seektable
type SeekTable struct {
	// One or more seek points.
	Points []SeekPoint
}

// parseSeekTable reads and parses the body of a SeekTable metadata block.
func (block *Block) parseSeekTable() error {
	// The number of seek points is derived from the header length, divided by
	// the size of a SeekPoint; which is 18 bytes.
	n := block.Length / 18
	if n < 1 {
		return errors.New("meta.Block.parseSeekTable: at least one seek point is required")
	}

	// The table is whole points: a length that is not a multiple of 18 is
	// a malformed block, which the reference decoder refuses too.
	if block.Length%18 != 0 {
		return fmt.Errorf("meta.Block.parseSeekTable: block length %d is not a multiple of 18", block.Length)
	}

	if n > maxSeekPoints {
		return fmt.Errorf("meta.parseSeekTable: %w, number of seekpoints: %d", ErrDeclaredBlockTooBig, n)
	}

	// The points are appended as they are read, so a block cut short in the
	// stream allocates what it holds, not what its length declares.
	table := &SeekTable{Points: make([]SeekPoint, 0, min(n, initialSeekPoints))}
	block.Body = table

	// The points are sorted in ascending order by sample number and unique
	// by it, placeholders excepted, and so every placeholder comes after
	// every real point (RFC 9639, section 8.5). prev is the last real
	// point's sample number once one has been seen; a placeholder marks the
	// table's end for real points.
	var (
		prev        uint64
		seenReal    bool
		placeholder bool
	)

	for idx := range n {
		var point SeekPoint

		err := binary.Read(block.lr, binary.BigEndian, &point)
		if err != nil {
			return unexpected(err)
		}

		table.Points = append(table.Points, point)

		sampleNum := point.SampleNum
		if sampleNum == PlaceholderPoint {
			placeholder = true

			continue
		}

		if placeholder {
			return fmt.Errorf(
				"%w: seek point %d (sample %d) after a placeholder, which may only end the table",
				ErrPlaceholderNotLast, idx, sampleNum,
			)
		}

		if seenReal {
			switch {
			case sampleNum < prev:
				return fmt.Errorf(
					"meta.Block.parseSeekTable: invalid seek point order; sample number (%d) < prev (%d)",
					sampleNum,
					prev,
				)
			case sampleNum == prev:
				return fmt.Errorf("meta.Block.parseSeekTable: duplicate seek point with sample number (%d)", sampleNum)
			}
		}

		prev, seenReal = sampleNum, true
	}

	return nil
}

// A SeekPoint specifies the byte offset and initial sample number of a given
// target frame.
//
// ref: https://www.xiph.org/flac/format.html#seekpoint
type SeekPoint struct {
	// Sample number of the first sample in the target frame, or
	// 0xFFFFFFFFFFFFFFFF for a placeholder point.
	SampleNum uint64
	// Offset in bytes from the first byte of the first frame header to the first
	// byte of the target frame's header.
	Offset uint64
	// Number of samples in the target frame.
	NSamples uint16
}

// ErrPlaceholderNotLast is returned by parseSeekTable when a real seek point
// follows a placeholder: placeholders may only end the table (RFC 9639,
// section 8.5). libFLAC and metaflac accept such a table.
var ErrPlaceholderNotLast = errors.New("meta.Block.parseSeekTable: a placeholder seek point before a real one")

// PlaceholderPoint represent the sample number used to specify placeholder seek
// points.
const PlaceholderPoint = 0xFFFFFFFFFFFFFFFF
