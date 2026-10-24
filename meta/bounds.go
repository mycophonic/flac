package meta

import "io"

// remaining is what the block's declared length still allows to be read. A
// length field inside the block is bounded by it before anything is
// allocated from the field: a block's bytes that are not in the stream end
// in io.ErrUnexpectedEOF, not in an allocation of what the field claims.
func (block *Block) remaining() int64 {
	if lr, ok := block.lr.(*io.LimitedReader); ok {
		return lr.N
	}

	return block.Length
}

// readString reads a string of n bytes declared inside the block, refusing
// one the block cannot hold before allocating it.
func (block *Block) readString(n uint32) (string, error) {
	if int64(n) > block.remaining() {
		return "", io.ErrUnexpectedEOF
	}

	return readString(block.lr, int(n))
}
