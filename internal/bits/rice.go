package bits

// ReadRice decodes a single Rice-coded residual with the given Rice parameter k.
// It fuses ReadUnary + Read(k) + ZigZag decode into a single method to avoid
// per-residual function call overhead. After ReadUnary finds the terminating
// 1-bit, any buffered bits are consumed inline for the k low bits, bypassing
// the full Read() entry path.
func (br *Reader) ReadRice(k uint) (int32, error) {
	// Phase 1: decode unary (high bits).
	high, err := br.ReadUnary()
	if err != nil {
		return 0, err
	}

	// Phase 2: read k low bits inline.
	var low uint64
	if k > 0 {
		if k <= br.n {
			// Fast path: all k bits remain in the buffered byte from ReadUnary.
			br.n -= k
			mask := ^uint8(0) << br.n
			low = uint64(br.x&mask) >> br.n
			br.x &^= mask
		} else {
			// Consume any buffered bits first.
			remaining := k
			if br.n > 0 {
				remaining -= br.n
				low = uint64(br.x)
				br.n = 0
				br.x = 0
			}

			// Read remaining bits from buffer bytes (inlined Read logic).
			nBytes := remaining / 8
			nBits := remaining % 8
			if nBits > 0 {
				nBytes++
			}

			if err = br.needBytes(int(nBytes)); err != nil {
				return 0, err
			}

			oldPos := br.pos
			for range nBytes - 1 {
				low <<= 8
				low |= uint64(br.buf[br.pos])
				br.pos++
			}

			b := br.buf[br.pos]
			br.pos++
			br.consumeBytes(oldPos, br.pos)

			if nBits > 0 {
				low <<= nBits
				br.n = 8 - nBits
				mask := ^uint8(0) << br.n
				low |= uint64(b&mask) >> br.n
				br.x = b & ^mask
			} else {
				low <<= 8
				low |= uint64(b)
			}
		}
	}

	// Phase 3: combine and ZigZag decode inline.
	folded := uint32(high<<k | low)
	return int32(folded>>1) ^ -int32(folded&1), nil
}
