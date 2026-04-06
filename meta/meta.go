// Package meta implements access to FLAC metadata blocks.
//
// A brief introduction of the FLAC metadata format [1] follows. FLAC metadata
// is stored in blocks; each block contains a header followed by a body. The
// block header describes the type of the block body, its length in bytes, and
// specifies if the block was the last metadata block in a FLAC stream. The
// contents of the block body depends on the type specified in the block header.
//
// At the time of this writing, the FLAC metadata format defines seven different
// metadata block types, namely:
//   - StreamInfo [2]
//   - Padding [3]
//   - Application [4]
//   - SeekTable [5]
//   - VorbisComment [6]
//   - CueSheet [7]
//   - Picture [8]
//
// Please refer to their respective documentation for further information.
//
//	[1]: https://www.xiph.org/flac/format.html#format_overview
//	[2]: https://godoc.org/github.com/mewkiz/flac/meta#StreamInfo
//	[3]: https://www.xiph.org/flac/format.html#metadata_block_padding
//	[4]: https://godoc.org/github.com/mewkiz/flac/meta#Application
//	[5]: https://godoc.org/github.com/mewkiz/flac/meta#SeekTable
//	[6]: https://godoc.org/github.com/mewkiz/flac/meta#VorbisComment
//	[7]: https://godoc.org/github.com/mewkiz/flac/meta#CueSheet
//	[8]: https://godoc.org/github.com/mewkiz/flac/meta#Picture
package meta

import (
	"errors"
	"io"
	"io/ioutil"
)

// A Block contains the header and body of a metadata block.
//
// ref: https://www.xiph.org/flac/format.html#metadata_block
type Block struct {
	// Metadata block header.
	Header
	// Metadata block body of type *StreamInfo, *Application, ... etc. Body is
	// initially nil, and gets populated by a call to Block.Parse.
	Body any
	// Underlying io.Reader; limited by the length of the block body.
	lr io.Reader
}

// New creates a new Block for accessing the metadata of r. It reads and parses
// a metadata block header.
//
// Call Block.Parse to parse the metadata block body, and call Block.Skip to
// ignore it.
func New(r io.Reader) (block *Block, err error) {
	block = new(Block)
	if err = block.parseHeader(r); err != nil {
		return block, err
	}
	block.lr = io.LimitReader(r, block.Length)

	// Validate block type after the LimitReader is set up, so callers can
	// still call block.Skip() on reserved types (7-126) as the FLAC spec
	// requires. Type 127 is invalid per spec.
	//
	// This catches garbage headers produced when a preceding block has an
	// incorrect length (e.g. IETF faulty/11), where the parser reads audio
	// frame data as a metadata header and gets type 0x7F from 0xFF bytes.
	if block.Type == 127 {
		return block, ErrInvalidType
	}
	if block.Type >= 7 {
		return block, ErrReservedType
	}

	return block, nil
}

// Parse reads and parses the header and body of a metadata block. Use New for
// additional granularity.
func Parse(r io.Reader) (block *Block, err error) {
	block, err = New(r)
	if err != nil {
		return block, err
	}
	if err = block.Parse(); err != nil {
		return block, err
	}
	return block, nil
}

// Errors returned by Parse.
var (
	ErrReservedType        = errors.New("meta.Block.Parse: reserved block type")
	ErrInvalidType         = errors.New("meta.Block.Parse: invalid block type")
	ErrDeclaredBlockTooBig = errors.New("declared block size is too big to allocate")
)

// Parse reads and parses the metadata block body.
func (block *Block) Parse() error {
	switch block.Type {
	case TypeStreamInfo:
		return block.parseStreamInfo()
	case TypePadding:
		return block.verifyPadding()
	case TypeApplication:
		return block.parseApplication()
	case TypeSeekTable:
		return block.parseSeekTable()
	case TypeVorbisComment:
		return block.parseVorbisComment()
	case TypeCueSheet:
		return block.parseCueSheet()
	case TypePicture:
		return block.parsePicture()
	}
	if block.Type >= 7 && block.Type <= 126 {
		return ErrReservedType
	}
	return ErrInvalidType
}

// Skip ignores the contents of the metadata block body.
func (block *Block) Skip() error {
	if sr, ok := block.lr.(io.Seeker); ok {
		_, err := sr.Seek(0, io.SeekEnd)
		return err
	}
	_, err := io.Copy(ioutil.Discard, block.lr)
	return err
}

// A Header contains information about the type and length of a metadata block.
//
// ref: https://www.xiph.org/flac/format.html#metadata_block_header
type Header struct {
	// Metadata block body type.
	Type Type
	// Length of body data in bytes.
	Length int64
	// IsLast specifies if the block is the last metadata block.
	IsLast bool
}

// parseHeader reads and parses the header of a metadata block.
// The header is always exactly 4 bytes (32 bits): 1 bit IsLast, 7 bits Type,
// 24 bits Length. Read directly to avoid buffering overhead.
func (block *Block) parseHeader(r io.Reader) error {
	var buf [4]byte
	if _, err := io.ReadFull(r, buf[:]); err != nil {
		// This is the only place a metadata block may return io.EOF, which
		// signals a graceful end of a FLAC stream (from a metadata point of
		// view).
		//
		// Note that valid FLAC streams always contain at least one audio frame
		// after the last metadata block. Therefore an io.EOF error at this
		// location is always invalid. This logic is to be handled by the flac
		// package however.
		if err == io.ErrUnexpectedEOF {
			return io.EOF
		}
		return err
	}

	// 1 bit: IsLast.
	block.IsLast = buf[0]&0x80 != 0
	// 7 bits: Type.
	block.Type = Type(buf[0] & 0x7F)
	// 24 bits: Length.
	block.Length = int64(buf[1])<<16 | int64(buf[2])<<8 | int64(buf[3])

	return nil
}

// Type represents the type of a metadata block body.
type Type uint8

// Metadata block body types.
const (
	TypeStreamInfo    Type = 0
	TypePadding       Type = 1
	TypeApplication   Type = 2
	TypeSeekTable     Type = 3
	TypeVorbisComment Type = 4
	TypeCueSheet      Type = 5
	TypePicture       Type = 6
)

func (t Type) String() string {
	switch t {
	case TypeStreamInfo:
		return "stream info"
	case TypePadding:
		return "padding"
	case TypeApplication:
		return "application"
	case TypeSeekTable:
		return "seek table"
	case TypeVorbisComment:
		return "vorbis comment"
	case TypeCueSheet:
		return "cue sheet"
	case TypePicture:
		return "picture"
	default:
		return "<unknown block type>"
	}
}

// unexpected returns io.ErrUnexpectedEOF if err is io.EOF, and returns err
// otherwise.
func unexpected(err error) error {
	if err == io.EOF {
		return io.ErrUnexpectedEOF
	}
	return err
}
