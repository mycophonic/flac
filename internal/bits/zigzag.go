package bits

// DecodeZigZag decodes a ZigZag encoded integer and returns it.
//
// Examples of ZigZag encoded values on the left and decoded values on the
// right:
//
//	0 =>  0
//	1 => -1
//	2 =>  1
//	3 => -2
//	4 =>  2
//	5 => -3
//	6 =>  3
//
// ref: https://developers.google.com/protocol-buffers/docs/encoding
func DecodeZigZag(x uint32) int32 {
	return int32(x>>1) ^ -int32(x&1)
}

// EncodeZigZag encodes a given integer to ZigZag-encoding.
//
// Examples of integer input on the left and corresponding ZigZag encoded values
// on the right:
//
//	 0 => 0
//	-1 => 1
//	 1 => 2
//	-2 => 3
//	 2 => 4
//	-3 => 5
//	 3 => 6
//
// ref: https://developers.google.com/protocol-buffers/docs/encoding
func EncodeZigZag(x int32) uint32 {
	// ZigZag encoding maps signed int32 to unsigned uint32 via bit-shifts
	// and XOR; the int32→uint32 cast is an intentional bit reinterpretation,
	// not a value range conversion. No external/untrusted input is involved.
	return uint32((x << 1) ^ (x >> 31)) //nolint:gosec // intentional ZigZag bit reinterpretation
}
