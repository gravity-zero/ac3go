package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// Mantissa quantization and decoding, clause 6.3.
//
// A mantissa is a fraction in [-1, 1) that the exponent shifts back down to
// the transform coefficient's real size. The bap says how coarsely it was
// quantized, and that in turn says how it is coded. Above 15 levels the
// mantissa is a plain two's complement fraction. At or below 15 levels it is
// an index into a table of evenly spaced values, and the three narrowest of
// those quantizers pack two or three mantissas into one codeword, because at
// that precision a whole number of bits per mantissa would waste a third of
// them.
//
// The three grouped quantizers are why a block's mantissas cannot be read
// channel by channel independently. A codeword sits at the position of the
// first mantissa of its group; the others take no bits at all, and the group
// they belong to may have been opened by a different channel. The pending
// values below are that state.

// mantissaBits maps a bap to the number of bits its mantissa reads on its own
// (qntztab, table 6.17). Baps 1, 2 and 4 read zero: their mantissas come out of
// a group, which is read by whichever mantissa opens it.
var mantissaBits = [16]uint8{0, 0, 0, 3, 0, 4, 5, 6, 7, 8, 9, 10, 11, 12, 14, 16}

// mantissaGroupBits and mantissaGroupSize describe the three grouped
// quantizers: how wide the codeword is, and how many mantissas it carries
// (clause 6.3.1).
var (
	mantissaGroupBits = [16]uint8{1: 5, 2: 7, 4: 7}
	mantissaGroupSize = [16]int{1: 3, 2: 3, 4: 2}
)

// symQuant holds the symmetric quantizer tables, tables 6.19 to 6.23, indexed
// by bap. The spec prints them as fractions and that is what they are: for an
// n level quantizer the codes map onto -(n-1)/n .. (n-1)/n in steps of 2/n,
// with zero in the middle. Generating them says the same thing as 41 hand
// copied fractions would, and cannot get one of them wrong.
var symQuant = func() (out [6][]float32) {
	for bap, levels := range [6]int{1: 3, 2: 5, 3: 7, 4: 11, 5: 15} {
		if levels == 0 {
			continue
		}
		t := make([]float32, levels)
		for code := range levels {
			t[code] = float32(float64(2*code-(levels-1)) / float64(levels))
		}
		out[bap] = t
	}
	return out
}()

// asymScale maps a bap to the reciprocal of the full scale of its two's
// complement mantissa, so the code becomes a fraction in [-1, 1). It is
// indexed the same way as mantissaBits and is exact: every entry is a power of
// two.
var asymScale = func() (out [16]float32) {
	for bap := 6; bap < 16; bap++ {
		out[bap] = 1 / float32(int32(1)<<(mantissaBits[bap]-1))
	}
	return out
}()

// expScale maps an exponent to the factor that shifts a mantissa back down to
// the transform coefficient it came from: the spec's mantissa >> exponent, in
// floating point, where it is exact.
//
// The table is indexed by a raw exponent byte rather than by a value known to
// be in range. Exponents come out of a differential chain over bit stream
// data, and decodeExponents is what keeps them inside 0..24; this table not
// having an edge means a hole there could never turn into a panic. Anything
// out of range reads as silence.
var expScale = func() (out [256]float32) {
	for e := 0; e <= maxExponent; e++ {
		out[e] = float32(1) / float32(int32(1)<<uint(e))
	}
	return out
}()

// ditherScale is the spec's scaling for the dither of unallocated mantissas
// (clause 6.3.4): a uniform distribution over -1 to 1, brought down by 0,707.
const ditherScale = 0.707

// ditherSeed starts the noise sequence of every frame. Reseeding per frame is
// what keeps a frame's decode a pure function of its bytes, which the segmented
// conversion this decoder feeds depends on.
const ditherSeed = 0x1f2e3d4c

// mantissaReader reads the mantissas of one audio block. It holds the groups
// opened but not yet drained, and the noise sequence of unallocated mantissas.
// It is owned by a decoder and reset per block, so it never allocates.
type mantissaReader struct {
	// pending[bap] holds the values of an open group, youngest last, so the
	// next mantissa of the group is the last one in.
	pending [5][3]float32
	n       [5]int

	lfsr   uint32
	dither bool // false replaces the noise with silence, for comparisons
}

// reset drops every open group. A group never spans two audio blocks: each
// block's mantissas start on a fresh bit stream.
func (r *mantissaReader) reset() {
	r.n = [5]int{}
}

// resetFrame drops every open group and rewinds the noise sequence.
func (r *mantissaReader) resetFrame() {
	r.reset()
	r.lfsr = ditherSeed
}

// nextDither returns the next noise value, uniform over [-0,707, 0,707).
//
// The spec asks only for "any reasonably random sequence", which is the whole
// problem with these values: two conforming decoders produce different noise
// here, so the bands this fills can never be compared bit for bit against
// another implementation.
func (r *mantissaReader) nextDither() float32 {
	return float32(r.next()) * (ditherScale / float32(1<<31))
}

// nextNoise returns the next noise value, uniform over [-1, 1).
//
// It is the dither's draw without the spec's 0,707. Spectral extension scales
// its noise by the energy of the band it is filling and by a factor that
// assumes unit width, so the dither's headroom would be counted twice.
func (r *mantissaReader) nextNoise() float32 {
	return float32(r.next()) * (1 / float32(1<<31))
}

// nextAHTDither returns the next pre-mantissa of an unallocated AHT bin,
// uniform over -2^22 to 2^22.
//
// It is an integer rather than a fraction because it is drawn before the
// transform, not after: the six values it fills a bin with are handed to idct6
// with everything else, so they have to be in the units idct6 works in. That
// also means the noise a block ends up with is not this noise, it is a rotation
// of six draws - which is fine, since the sequence was arbitrary to begin with.
//
// Half the amplitude of the dither of an AC-3 block, and the reference's. The
// transform is what makes up the difference: six independent draws summed with
// the weights of a DCT come out wider than they went in.
func (r *mantissaReader) nextAHTDither() int32 {
	if !r.dither {
		return 0
	}
	return r.next() >> 9
}

// next advances the noise sequence and returns the raw draw.
//
// Xorshift, which has the two properties that matter: it is cheap, and it
// cannot fall into a short cycle from the seed this decoder uses. It is one
// sequence shared by everything that draws from it, which is what the spec
// describes: the noise is the decoder's, not the field's.
func (r *mantissaReader) next() int32 {
	x := r.lfsr
	x ^= x << 13
	x ^= x >> 17
	x ^= x << 5
	r.lfsr = x
	return int32(x)
}

// read returns the next mantissa for the given bap, as a fraction the caller
// scales by its exponent. dither says whether an unallocated mantissa reads as
// noise or as zero; a channel's dithflag decides that, and the coupling
// channel never dithers, since its bins are dithered per channel after they
// are spread back out.
func (r *mantissaReader) read(br *bitstream.Reader, bap uint8, dither bool) (float32, error) {
	switch bap {
	case 0:
		if dither && r.dither {
			return r.nextDither(), nil
		}
		return 0, nil

	case 1, 2, 4:
		if n := r.n[bap]; n > 0 {
			r.n[bap] = n - 1
			return r.pending[bap][n-1], nil
		}
		code := int(br.Uint32(uint(mantissaGroupBits[bap])))
		if br.Err() != nil {
			return 0, wrap(ErrShortFrame)
		}
		tab := symQuant[bap]
		size := mantissaGroupSize[bap]
		// The codeword is a base-levels number whose most significant digit is
		// the first mantissa of the group (clause 6.3.5). A code that overflows
		// the group cannot come from an encoder.
		if code >= pow(len(tab), size) {
			return 0, badMantissaGroup(bap, code)
		}
		// Digit k is the mantissa size-1-k places along, so keeping the
		// pending values digit-ordered makes the next one the last one in.
		for k := range size - 1 {
			r.pending[bap][k] = tab[code/pow(len(tab), k)%len(tab)]
		}
		r.n[bap] = size - 1
		return tab[code/pow(len(tab), size-1)], nil

	case 3, 5:
		tab := symQuant[bap]
		code := int(br.Uint32(uint(mantissaBits[bap])))
		if br.Err() != nil {
			return 0, wrap(ErrShortFrame)
		}
		if code >= len(tab) {
			return 0, badMantissaCode(bap, code)
		}
		return tab[code], nil

	default:
		if bap >= uint8(len(mantissaBits)) {
			return 0, reservedError("bap", uint32(bap))
		}
		n := uint(mantissaBits[bap])
		code := br.Uint32(n)
		if br.Err() != nil {
			return 0, wrap(ErrShortFrame)
		}
		// Sign extend the two's complement fraction into the full word.
		return float32(int32(code<<(32-n))>>(32-n)) * asymScale[bap], nil
	}
}

// decodeCoeffs reads the mantissas of one channel over bins start..end and
// scales them into transform coefficients.
func (r *mantissaReader) decodeCoeffs(br *bitstream.Reader, bap, exp *[MaxCoefs]uint8,
	start, end int, dither bool, out *[MaxCoefs]float32,
) error {
	for i := start; i < end; i++ {
		v, err := r.read(br, bap[i], dither)
		if err != nil {
			return err
		}
		out[i] = v * expScale[exp[i]]
	}
	return nil
}

// pow returns base**exp for the small exponents the group codes use.
func pow(base, exp int) int {
	v := 1
	for range exp {
		v *= base
	}
	return v
}
