package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// The adaptive hybrid transform, annex E.3.
//
// The filter bank this decoder already has turns 256 coefficients into 256
// samples, once per block. AHT puts a second transform in front of it: for a
// channel whose six blocks are stationary enough to share one set of exponents,
// the encoder takes each bin's six values - one per block - and runs a 6 point
// DCT over them before quantizing. A tone that holds still for a frame has all
// its energy in the first of those six, and the other five cost almost nothing,
// so the same bits buy a far finer quantizer.
//
// That is why this cannot be a variation on readMantissas. The unit of coding
// stops being the block: a channel using AHT reads all six blocks' mantissas at
// once, out of block 0, and the five blocks after it read none of its bits at
// all. Reading them per block the AC-3 way does not merely give that channel
// wrong samples, it moves every field of every later block in the frame, since
// there is no check word between here and the end of the frame to notice.
//
// Three things arrive with it, and each one is silent when wrong. The
// allocation runs through hebapTab rather than baptab, so the same masking
// curve buys different codeword widths. The narrow allocations are vector
// quantized, six pre-mantissas to one index, rather than scalar. And the wide
// ones are gain adaptive quantized: a per-bin gain shrinks the quantizer to fit
// the six values' actual range, with an escape code for the outliers that fall
// outside it.

// GAQ modes (gaqmod, table E3.7). The names are the gains the mode can choose
// between: gaq12 picks per bin between gains of 1 and 2, gaq14 between 1 and 4,
// and gaq124 between 1, 2 and 4. gaqNo means no bin has a gain, and every
// mantissa is read at its hebap's full width.
const (
	gaqNo = iota
	gaq12
	gaq14
	gaq124
)

// gaqEndBap is the first hebap a GAQ gain does not reach, per mode
// (clause E.3.3.3.2). It is not the same for every mode, and the asymmetry is
// the mode's own: with only two gains to choose from and the wider one being 2,
// gaq12 cannot usefully shrink the quantizers above hebap 11, so the gain codes
// simply are not there for them. Reading a gain code for a bin the encoder
// wrote none for consumes a bit that belongs to the next field.
var gaqEndBap = [4]uint8{gaqNo: 12, gaq12: 12, gaq14: 17, gaq124: 17}

// gaqRemap1 corrects a mantissa read at gain 1 (table E3.6). The quantizers
// from hebap 8 up are uniform over a range slightly narrower than the mantissa
// they are coded in, so a code read as a plain fraction lands short; these
// factors, in fifteenths of a bit, stretch it back. The correction shrinks as
// the quantizer gets finer, and by hebap 19 there is nothing left to correct.
// Indexed by hebap-8.
var gaqRemap1 = [12]int16{4681, 2185, 1057, 520, 258, 129, 64, 32, 16, 8, 2, 0}

// gaqRemap24A and gaqRemap24B correct a large mantissa, the one the escape code
// introduces (table E3.6). A gain of 2 or 4 means the encoder bet that the six
// values fit in a narrower range, and the escape is what it sends when one of
// them did not: the value is re-read at nearly full width, but out of a
// quantizer that is deliberately asymmetric, coarse where the bet said values
// would be rare. A is the slope of the correction and B its offset for negative
// values. Both are indexed by [hebap-8][gain-1].
var (
	gaqRemap24A = [9][2]int16{
		{-10923, -4681},
		{-14043, -6554},
		{-15292, -7399},
		{-15855, -7802},
		{-16124, -7998},
		{-16255, -8096},
		{-16320, -8144},
		{-16352, -8168},
		{-16368, -8180},
	}
	gaqRemap24B = [9][2]int16{
		{-5461, -1170},
		{-11703, -4915},
		{-14199, -6606},
		{-15327, -7412},
		{-15864, -7805},
		{-16126, -7999},
		{-16255, -8096},
		{-16320, -8144},
		{-16352, -8168},
	}
)

// The 6 point IDCT's coefficients: sqrt(2)*cos(k*pi/12) at k = 2, 0 and 5, in
// 23 bit fixed point.
const (
	idct6Coeff0 = 10273905 // sqrt(2)*cos(2*pi/12) << 23
	idct6Coeff1 = 11863283 // sqrt(2)*cos(0*pi/12) << 23
	idct6Coeff2 = 3070444  // sqrt(2)*cos(5*pi/12) << 23
)

// ahtScale brings a pre-mantissa down to the fraction the rest of this package
// calls a mantissa. A pre-mantissa is 24 bit fixed point with its sign, so full
// scale is 2^23 and it lines up exactly with what mantissa.go's tables produce.
const ahtScale = 1.0 / (1 << 23)

// idct6 replaces a bin's six pre-mantissas with the six block values they were
// transformed from (clause E.3.3.4).
//
// It is written as a butterfly rather than as the sum the spec prints, and it is
// integer rather than float, both for the same reason: this is the reference's
// arithmetic, down to the truncation of each fixed point product, and the
// reference is what this decoder is measured against. The savings are real but
// incidental - a 6 point DCT done as written is 36 multiplies, and this is 3.
//
// The three coefficients are the only irrational numbers in it. Everything else
// is the symmetry of a real even-length DCT: the even-indexed outputs of the
// transform depend on the input's even part and the odd-indexed on its odd
// part, so each half can be built once and then added and subtracted to give
// two outputs apiece.
func idct6(preMant *[BlocksPerFrame]int32) {
	odd1 := preMant[1] - preMant[3] - preMant[5]

	even2 := int32((int64(preMant[2]) * idct6Coeff0) >> 23)
	tmp := int32((int64(preMant[4]) * idct6Coeff1) >> 23)
	odd0 := int32((int64(preMant[1]+preMant[5]) * idct6Coeff2) >> 23)

	even0 := preMant[0] + tmp>>1
	even1 := preMant[0] - tmp

	tmp = even0
	even0 = tmp + even2
	even2 = tmp - even2

	tmp = odd0
	odd0 = tmp + preMant[1] + preMant[3]
	odd2 := tmp + preMant[5] - preMant[3]

	preMant[0] = even0 + odd0
	preMant[1] = even1 + odd1
	preMant[2] = even2 + odd2
	preMant[3] = even2 - odd2
	preMant[4] = even1 - odd1
	preMant[5] = even0 - odd0
}

// sbits reads a two's complement signed field of n bits.
func sbits(r *bitstream.Reader, n uint) int32 {
	return int32(r.Uint32(n)<<(32-n)) >> (32 - n)
}

// ungroupGaqGains unpacks the three gains of one 1.67 bit group (clause
// E.3.3.3.2).
//
// The code is a base 3 number, most significant digit first, so 27 of the 32
// codes a 5 bit field can hold are used and three gains cost five bits instead
// of six. A code above 26 has no three digits below 3 and cannot come from an
// encoder; it is clamped rather than refused, because the reference clamps, and
// refusing would drop frames the decoder this is measured against renders.
func ungroupGaqGains(code int) (int32, int32, int32) {
	if code > 26 {
		code = 26
	}
	return int32(code / 9), int32(code % 9 / 3), int32(code % 3)
}

// readAHTGains reads the per-bin GAQ gain codes that precede the mantissas
// (clause E.3.3.3.2), and returns how many it read.
//
// Only bins whose hebap is 8 or more and below the mode's end have one: below 8
// the bin is vector quantized and there is no scalar quantizer to shrink, and
// above the end the mode has no gain fine enough to be worth a bit. The gains
// are read for the whole channel before any mantissa is, so a bin's gain and
// its mantissas are nowhere near each other in the bit stream, and this is why
// the walk over the bins happens twice.
func (d *Decoder) readAHTGains(ch int, gaqMode int, start, end int) int {
	r := &d.r
	endBap := gaqEndBap[gaqMode]
	gs := 0

	switch gaqMode {
	case gaq12, gaq14:
		// One bit per bin, saying which of the mode's two gains applies. The
		// shift is what turns that bit into the gain's log: gaq12 chooses
		// between 2^0 and 2^1, gaq14 between 2^0 and 2^2.
		for bin := start; bin < end; bin++ {
			if hebap := d.bap[ch][bin]; hebap > 7 && hebap < endBap {
				g := int32(0)
				if r.Bool() {
					g = 1 << (gaqMode - 1)
				}
				d.gaqGain[gs] = g
				gs++
			}
		}

	case gaq124:
		// Three gains to a group, so only every third qualifying bin reads a
		// codeword. The group is opened by whichever bin comes first, the same
		// way an AC-3 mantissa group is, and the counter starts full so that
		// the very first one opens it.
		gc := 2
		for bin := start; bin < end; bin++ {
			if hebap := d.bap[ch][bin]; hebap > 7 && hebap < endBap {
				if gc == 2 {
					gc = 0
					d.gaqGain[gs], d.gaqGain[gs+1], d.gaqGain[gs+2] =
						ungroupGaqGains(int(r.Uint32(5)))
					gs += 3
				} else {
					gc++
				}
			}
		}
	}
	return gs
}

// readAHTMantissas reads one channel's six blocks of mantissas, out of block 0,
// and leaves them in d.preMantissa (clause E.3.3.3).
func (d *Decoder) readAHTMantissas(ch, start, end int) error {
	r := &d.r

	gaqMode := int(r.Uint32(2))
	endBap := gaqEndBap[gaqMode]
	d.readAHTGains(ch, gaqMode, start, end)

	gs := 0
	for bin := start; bin < end; bin++ {
		hebap := d.bap[ch][bin]
		bits := bitsVsHebap[hebap]
		preMant := &d.preMantissa[ch][bin]

		switch {
		case hebap == 0:
			// An unallocated bin is noise, drawn per block and then
			// transformed with everything else. Note what is missing: the
			// channel's dither flag. The reference draws here whatever the
			// flag said, and a coupled channel's flag is honoured later, when
			// the coupling channel is spread back out and each channel decides
			// for itself. So the flag is not ignored, it is applied one step
			// further along than an AC-3 block applies it.
			for blk := range BlocksPerFrame {
				preMant[blk] = d.mant.nextAHTDither()
			}

		case hebap < 8:
			// Vector quantization: one index buys all six blocks at once. The
			// codebook entries are 16 bit and a pre-mantissa is 24, hence the
			// shift.
			v := r.Uint32(uint(bits))
			entry := &mantissaVQ[hebap][v]
			for blk := range BlocksPerFrame {
				preMant[blk] = int32(entry[blk]) << 8
			}

		default:
			// Gain adaptive quantization: six mantissas, read at a width the
			// bin's gain narrows.
			logGain := int32(0)
			if gaqMode != gaqNo && hebap < endBap {
				logGain = d.gaqGain[gs]
				gs++
			}
			gbits := uint(int32(bits) - logGain)

			for blk := range BlocksPerFrame {
				mant := sbits(r, gbits)

				if logGain != 0 && mant == -(1<<(gbits-1)) {
					// The escape code: the most negative value the narrowed
					// quantizer can express is spent saying "this one did not
					// fit", and the real value follows at nearly full width.
					// Costing the range one code is what makes the gain safe
					// to apply at all - the encoder can shrink the quantizer
					// on a bet it is allowed to lose.
					mbits := uint(int32(bits) - (2 - logGain))
					mant = gaqLarge(hebap, logGain, sbits(r, mbits), mbits)
				} else {
					mant = gaqSmall(hebap, logGain, mant)
				}
				preMant[blk] = mant
			}
		}

		if r.Err() != nil {
			return wrap(ErrShortFrame)
		}
		idct6(preMant)
	}
	return nil
}

// gaqSmall dequantizes a mantissa that fitted inside its bin's quantizer: a
// plain fraction, scaled up to a pre-mantissa.
//
// Only an ungained mantissa gets the uniform quantizer's correction. A gained
// one has already been corrected by being read narrow, which is what the gain
// is; correcting it twice would scale it away from the value the encoder sent.
func gaqSmall(hebap uint8, logGain, mant int32) int32 {
	bits := bitsVsHebap[hebap]
	mant *= 1 << (24 - bits)
	if logGain == 0 {
		mant += int32((int64(gaqRemap1[hebap-8]) * int64(mant)) >> 15)
	}
	return mant
}

// gaqLarge dequantizes the mantissa an escape code introduced, read mbits wide.
//
// The large quantizer is asymmetric, so undoing it is a line rather than a
// scale: a slope, and an offset whose value depends on the sign because the two
// tails are not the same width. The sign is taken after the shift, which is the
// step that turned the code into a pre-mantissa - taking it before would read
// the sign of the code rather than of the value.
func gaqLarge(hebap uint8, logGain, mant int32, mbits uint) int32 {
	mant = int32(uint32(mant) << (23 - (mbits - 1)))
	var b int32
	if mant >= 0 {
		b = 1 << (23 - uint(logGain))
	} else {
		b = int32(gaqRemap24B[hebap-8][logGain-1]) << 8
	}
	return mant + int32((int64(gaqRemap24A[hebap-8][logGain-1])*int64(mant))>>15) + b
}

// ahtCoeffs hands one block its share of a channel's pre-mantissas, scaled into
// transform coefficients by the exponents (clause E.3.3.4).
//
// This is the only thing the five blocks after block 0 do for an AHT channel.
// They read no bits for it at all.
func (d *Decoder) ahtCoeffs(ch, blk, start, end int, out *[MaxCoefs]float32) {
	for bin := start; bin < end; bin++ {
		// The shift is the reference's, truncation included, rather than a
		// multiply by expScale. The two differ by less than a pre-mantissa's
		// last bit, which is a hundred times below the last bit of the PCM
		// this ends up as, but the reference is the yardstick and matching it
		// costs nothing. Exponents are held to 0..24 by decodeExponents, and a
		// shift past a word's width reads as zero in Go rather than as the
		// reference's undefined behaviour, so a hole there cannot panic.
		out[bin] = float32(d.preMantissa[ch][bin][blk]>>d.exp[ch][bin]) * ahtScale
	}
}

// decodeChannelCoeffs fills one channel's transform coefficients for one block,
// by whichever of the two paths the frame said this channel uses.
//
// The AHT read sits here, in the middle of the channel walk, rather than in a
// pass of its own before it, because that is where its bits are: the coupling
// channel's mantissas arrive after the first coupled channel's, and an AHT
// channel's six blocks arrive at the point in block 0 where its one block's
// mantissas would have. The bit stream interleaves the two codings and the
// reader has to interleave with it.
func (d *Decoder) decodeChannelCoeffs(ch, blk, start, end int, dither bool, out *[MaxCoefs]float32) error {
	if !d.eac3.usesAHT[ch] {
		return d.mant.decodeCoeffs(&d.r, &d.bap[ch], &d.exp[ch], start, end, dither, out)
	}
	if blk == 0 {
		if err := d.readAHTMantissas(ch, start, end); err != nil {
			return err
		}
	}
	d.ahtCoeffs(ch, blk, start, end, out)
	return nil
}
