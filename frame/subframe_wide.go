package frame

import (
	"errors"
	"fmt"

	"github.com/mycophonic/flac/internal/bits"
)

// wideBitsPerSample is the widest sample the int32 decoders hold. A subframe
// above it decodes wide: the side channel of a 32-bit stream, whose samples
// carry 33 bits (RFC 9639, section 4.2), as libFLAC's wide restore functions
// decode it. The wide decoders below mirror the int32 ones in subframe.go;
// Correlate writes the channel's 32-bit result into Subframe.Samples.
const wideBitsPerSample = 32

// coeffPrecReserved is the coefficient precision field's reserved value.
const coeffPrecReserved = 0xF

// The wide decoders' refusals, the int32 decoders' rephrased as sentinels.
var (
	errWideCoeffPrec = errors.New(
		"frame.Subframe.decodeFIRWide: invalid coefficient precision bit pattern (1111)",
	)
	errWideResidualCode = errors.New(
		"frame.Subframe.decodeResidualsWide: reserved residual coding method bit pattern",
	)
	errWidePartitions = errors.New(
		"frame.Subframe.decodeRicePartWide: block size not evenly divisible by the partitions",
	)
	errWidePartOrder = errors.New(
		"frame.Subframe.decodeRicePartWide: partition order too large for the block and predictor order",
	)
	errWideCoeffCount = errors.New(
		"frame.Subframe.decodeLPCWide: prediction order differs from the number of coefficients",
	)
	errWideShift = errors.New("frame.Subframe.decodeLPCWide: invalid negative shift")
)

// parseSubframeWideInto parses a subframe whose samples exceed 32 bits into
// the subframe's wide buffer, pointed to by frame.wide, leaving
// subframe.Samples for Correlate to fill.
func (frame *Frame) parseSubframeWideInto(reader *bits.Reader, bps uint, samples []int32, subframe *Subframe) error {
	partitionsBuf := subframe.partitionsBuf
	verbatimBuf := subframe.verbatimBuf
	wideBuf := subframe.wideBuf

	*subframe = Subframe{}
	subframe.partitionsBuf = partitionsBuf
	subframe.verbatimBuf = verbatimBuf
	subframe.wideBuf = wideBuf

	if err := subframe.parseHeader(reader); err != nil {
		return err
	}

	bps -= subframe.Wasted

	subframe.NSamples = int(frame.BlockSize)
	subframe.Samples = samples[:subframe.NSamples]

	// The wide samples live on the subframe, which ParseReuse keeps across
	// frames as it keeps the partition and verbatim buffers; grow-only.
	if cap(subframe.wideBuf) < subframe.NSamples {
		subframe.wideBuf = make([]int64, subframe.NSamples)
	}

	wide := subframe.wideBuf[:subframe.NSamples]
	frame.wide = wide

	var err error

	switch subframe.Pred {
	case PredConstant:
		err = decodeConstantWide(reader, bps, wide)
	case PredVerbatim:
		err = decodeVerbatimWide(reader, bps, wide)
	case PredFixed:
		err = subframe.decodeFixedWide(reader, bps, wide)
	case PredFIR:
		err = subframe.decodeFIRWide(reader, bps, wide)
	}

	for i, sample := range wide {
		wide[i] = sample << subframe.Wasted
	}

	return err
}

func decodeConstantWide(reader *bits.Reader, bps uint, wide []int64) error {
	value, err := reader.Read(bps)
	if err != nil {
		return unexpected(err)
	}

	sample := bits.IntN(value, bps)
	for i := range wide {
		wide[i] = sample
	}

	return nil
}

func decodeVerbatimWide(reader *bits.Reader, bps uint, wide []int64) error {
	for i := range wide {
		value, err := reader.Read(bps)
		if err != nil {
			return unexpected(err)
		}

		wide[i] = bits.IntN(value, bps)
	}

	return nil
}

func (subframe *Subframe) decodeFixedWide(reader *bits.Reader, bps uint, wide []int64) error {
	for i := range subframe.Order {
		value, err := reader.Read(bps)
		if err != nil {
			return unexpected(err)
		}

		wide[i] = bits.IntN(value, bps)
	}

	if err := subframe.decodeResidualsWide(reader, wide); err != nil {
		return err
	}

	const shift = 0

	return subframe.decodeLPCWide(FixedCoeffs[subframe.Order], shift, wide)
}

func (subframe *Subframe) decodeFIRWide(reader *bits.Reader, bps uint, wide []int64) error {
	for i := range subframe.Order {
		value, err := reader.Read(bps)
		if err != nil {
			return unexpected(err)
		}

		wide[i] = bits.IntN(value, bps)
	}

	// 4 bits: (coefficients' precision in bits) - 1.
	value, err := reader.Read(4)
	if err != nil {
		return unexpected(err)
	}

	if value == coeffPrecReserved {
		return errWideCoeffPrec
	}

	prec := uint(value) + 1
	subframe.CoeffPrec = prec

	// 5 bits: predictor coefficient shift needed in bits.
	value, err = reader.Read(5)
	if err != nil {
		return unexpected(err)
	}

	shift := signExtend(value, 5)
	subframe.CoeffShift = shift

	coeffs := subframe.coeffsBuf[:subframe.Order]
	for i := range coeffs {
		value, err = reader.Read(prec)
		if err != nil {
			return unexpected(err)
		}

		coeffs[i] = signExtend(value, prec)
	}

	subframe.Coeffs = coeffs

	if err := subframe.decodeResidualsWide(reader, wide); err != nil {
		return err
	}

	return subframe.decodeLPCWide(coeffs, shift, wide)
}

// decodeResidualsWide is decodeResiduals with 64-bit residuals.
func (subframe *Subframe) decodeResidualsWide(reader *bits.Reader, wide []int64) error {
	value, err := reader.Read(2)
	if err != nil {
		return unexpected(err)
	}

	residualCodingMethod := ResidualCodingMethod(value) // #nosec G115 -- a 2-bit field just read
	subframe.ResidualCodingMethod = residualCodingMethod

	switch residualCodingMethod {
	case ResidualCodingMethodRice1:
		return subframe.decodeRicePartWide(reader, 4, wide)
	case ResidualCodingMethodRice2:
		return subframe.decodeRicePartWide(reader, 5, wide)
	default:
		return fmt.Errorf("%w (%02b)", errWideResidualCode, uint8(residualCodingMethod))
	}
}

// decodeRicePartWide is decodeRicePart with 64-bit residuals.
func (subframe *Subframe) decodeRicePartWide(reader *bits.Reader, paramSize uint, wide []int64) error {
	value, err := reader.Read(4)
	if err != nil {
		return unexpected(err)
	}

	partOrder := int(value) // #nosec G115 -- a 4-bit field just read

	nparts := 1 << partOrder
	if subframe.NSamples%nparts != 0 {
		return fmt.Errorf("%w: block size %d, 2^%d partitions", errWidePartitions, subframe.NSamples, partOrder)
	}

	if subframe.NSamples/nparts < subframe.Order {
		return fmt.Errorf("%w: order %d, block size %d, predictor order %d",
			errWidePartOrder, partOrder, subframe.NSamples, subframe.Order)
	}

	riceSubframe := &RiceSubframe{
		PartOrder: partOrder,
	}
	subframe.RiceSubframe = riceSubframe

	if cap(subframe.partitionsBuf) < nparts {
		subframe.partitionsBuf = make([]RicePartition, nparts)
	}

	partitions := subframe.partitionsBuf[:nparts]
	clear(partitions)
	riceSubframe.Partitions = partitions
	sIdx := subframe.Order

	for i := range nparts {
		partition := &partitions[i]

		value, err = reader.Read(paramSize)
		if err != nil {
			return unexpected(err)
		}

		partition.Param = uint(value)

		nsamples := subframe.partitionSamples(partOrder, nparts, i)

		read := readRicePartitionWide
		if paramSize == 4 && partition.Param == 0xF || paramSize == 5 && partition.Param == 0x1F {
			read = readEscapedPartitionWide
		}

		if err := read(reader, partition, wide[sIdx:sIdx+nsamples]); err != nil {
			return err
		}

		sIdx += nsamples
	}

	return nil
}

// partitionSamples is the number of residuals in partition i of nparts: the
// first partition gives up the warm-up samples.
func (subframe *Subframe) partitionSamples(partOrder, nparts, i int) int {
	switch {
	case partOrder == 0:
		return subframe.NSamples - subframe.Order
	case i != 0:
		return subframe.NSamples / nparts
	default:
		return subframe.NSamples/nparts - subframe.Order
	}
}

// readEscapedPartitionWide reads an escaped partition: n bits per residual,
// n from a 5-bit field, stored as signed two's complement.
func readEscapedPartitionWide(reader *bits.Reader, partition *RicePartition, residuals []int64) error {
	value, err := reader.Read(5)
	if err != nil {
		return unexpected(err)
	}

	nbits := uint(value)
	partition.EscapedBitsPerSample = nbits

	for idx := range residuals {
		sample, err := reader.Read(nbits)
		if err != nil {
			return unexpected(err)
		}

		residuals[idx] = bits.IntN(sample, nbits)
	}

	return nil
}

// readRicePartitionWide reads a partition's Rice-coded residuals. A residual
// fits int32 whatever the sample width: RFC 9639 section 9.2.7 bounds every
// residual's absolute value under 2^31, and an encoder that cannot keep it
// there writes the subframe verbatim.
func readRicePartitionWide(reader *bits.Reader, partition *RicePartition, residuals []int64) error {
	for idx := range residuals {
		residual, err := reader.ReadRice(partition.Param)
		if err != nil {
			return unexpected(err)
		}

		residuals[idx] = int64(residual)
	}

	return nil
}

// decodeLPCWide is decodeLPC over 64-bit samples: a 33-bit sample times a
// 15-bit coefficient, 32 of them, stays within int64.
func (subframe *Subframe) decodeLPCWide(coeffs []int32, shift int32, wide []int64) error {
	if len(coeffs) != subframe.Order {
		return fmt.Errorf("%w: %d and %d", errWideCoeffCount, subframe.Order, len(coeffs))
	}

	if shift < 0 {
		return errWideShift
	}

	for i := subframe.Order; i < len(wide); i++ {
		var sample int64
		for j, c := range coeffs {
			sample += int64(c) * wide[i-j-1]
		}

		wide[i] += sample >> uint(shift)
	}

	return nil
}

// correlateWide is Correlate for a frame whose side channel decoded wide: the
// 33-bit side and the 32-bit other channel give two 32-bit channels.
func (frame *Frame) correlateWide() {
	side := frame.wide

	switch frame.Channels {
	case ChannelsLeftSide:
		left := frame.Subframes[0].Samples
		right := frame.Subframes[1].Samples

		for i := range side {
			// The right channel is the left less the side.
			right[i] = int32(int64(left[i]) - side[i]) // #nosec G115 -- a 32-bit channel of a valid stream
		}
	case ChannelsSideRight:
		left := frame.Subframes[0].Samples
		right := frame.Subframes[1].Samples

		for i := range side {
			// The left channel is the right plus the side.
			left[i] = int32(int64(right[i]) + side[i]) // #nosec G115 -- a 32-bit channel of a valid stream
		}
	case ChannelsMidSide:
		mid := frame.Subframes[0].Samples
		right := frame.Subframes[1].Samples

		for i := range side {
			// Left is half of twice the mid plus the side, right half of twice
			// the mid less the side, the sum's lowest bit being the difference's.
			m := int64(mid[i])<<1 | side[i]&1
			s := side[i]
			mid[i] = int32((m + s) >> 1)   // #nosec G115 -- a 32-bit channel of a valid stream
			right[i] = int32((m - s) >> 1) // #nosec G115 -- a 32-bit channel of a valid stream
		}
	case ChannelsMono, ChannelsLR, ChannelsLRC, ChannelsLRLsRs, ChannelsLRCLsRs,
		ChannelsLRCLfeLsRs, ChannelsLRCLfeCsSlSr, ChannelsLRCLfeLsRsSlSr:
		// Independent channels have no side; parseSubframes decodes none wide.
	}

	frame.wide = nil
}
