package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// Exponent coding, clause 6.1.
//
// Exponents are a spectral envelope: one value per frequency bin, each a
// right-shift count applied to the bin's mantissa. They dominate the side
// information, so the format codes them differentially and packs three
// differentials into a 7-bit word. Three strategies trade frequency resolution
// for bits: d15 sends an exponent per bin, d25 one per pair, d45 one per quad.
// A fourth value asks the decoder to reuse the previous block's set unchanged,
// which is why exponents are the one piece of per-frame state that survives a
// block boundary.

// Exponent strategies (cplexpstr and chexpstr, clause 4.4.3.21 and 4.4.3.22,
// table 6.4). lfeexpstr is a single bit and selects ExpReuse or ExpD15 only
// (clause 4.4.3.23, table 6.5).
const (
	ExpReuse uint8 = iota // reuse the previous block's exponents
	ExpD15                // one exponent per mantissa
	ExpD25                // one exponent per pair of mantissas
	ExpD45                // one exponent per quad of mantissas
)

const (
	// MaxCoefs is the number of frequency bins an audio block codes per
	// channel, and so the number of exponents, baps and transform
	// coefficients a channel can carry.
	MaxCoefs = 256

	// maxExponent is the largest exponent the format defines. Clause 6.2.2.2
	// pins the psd dynamic range to exponents 0 to 24; a decoded exponent
	// outside that range means the bit stream is not what it claims to be.
	maxExponent = 24

	// maxGexp is the largest valid 7-bit exponent group. A group codes three
	// mapped values in 0..4 as 25*M1 + 5*M2 + M3, so 124 is the ceiling and
	// no encoder can emit more.
	maxGexp = 124

	// lfeMants is the number of mantissas the LFE channel always carries
	// (lfeendmant, clause 6.1.3).
	lfeMants = 7

	// lfeExpGroups is nlfegrps, which clause 6.1.3 fixes at 2.
	lfeExpGroups = 2
)

// maxChbwcod is the largest channel bandwidth code the spec allows. Clause
// 4.4.3.24 is unusually blunt about the rest: a value above 60 makes the bit
// stream invalid and the decoder must mute.
const maxChbwcod = 60

// chbwcodEndMant returns endmant[ch], the bin one past the last one an
// uncoupled full bandwidth channel codes (clause 6.1.3). Every channel starts
// at bin 0, so this is also its mantissa count.
func chbwcodEndMant(chbwcod uint8) int { return (int(chbwcod)+12)*3 + 37 }

// cplbegfStrtMant returns cplstrtmant, the first bin of the coupling channel
// (clause 6.1.3). It is also endmant of every channel coupled into it: below
// that bin a coupled channel codes itself, above it the coupling channel
// stands in for all of them.
func cplbegfStrtMant(cplbegf uint8) int { return int(cplbegf)*12 + 37 }

// cplendfEndMant returns cplendmant, the bin one past the last one the
// coupling channel codes (clause 6.1.3).
func cplendfEndMant(cplendf uint8) int { return (int(cplendf)+3)*12 + 37 }

// expGroupSize maps an exponent strategy to grpsize: how many bins each
// decoded exponent covers (clause 6.1.3).
var expGroupSize = [4]int{ExpReuse: 0, ExpD15: 1, ExpD25: 2, ExpD45: 4}

// fbwExpGroups returns nchgrps, the number of 7-bit groups a full bandwidth or
// coupled channel sends, not counting the leading absolute exponent
// (clause 6.1.3).
func fbwExpGroups(strategy uint8, endmant int) int {
	switch strategy {
	case ExpD15:
		return (endmant - 1) / 3
	case ExpD25:
		return (endmant - 1 + 3) / 6
	case ExpD45:
		return (endmant - 1 + 9) / 12
	}
	return 0
}

// cplExpGroups returns ncplgrps, the number of 7-bit groups the coupling
// channel sends (clause 6.1.3). The coupling channel codes a whole number of
// 12-bin sub-bands, so unlike a full bandwidth channel its group count divides
// exactly.
func cplExpGroups(strategy uint8, strtmant, endmant int) int {
	if n := expGroupSize[strategy&3]; n != 0 {
		return (endmant - strtmant) / (3 * n)
	}
	return 0
}

// decodeExponents unpacks ngrps coded groups from r and expands them into exp,
// starting at exp[0] and stepping by the strategy's group size. absexp seeds
// the differential chain; the caller has already read it from the bit stream
// and is responsible for placing it (a full bandwidth channel's absexp is the
// exponent of bin 0 and belongs in exp[0], the coupling channel's is a
// reference only and belongs nowhere).
//
// exp must have room for ngrps*3*group size entries. It never allocates.
func decodeExponents(r *bitstream.Reader, strategy uint8, ngrps int, absexp uint8, exp []uint8) error {
	grpsize := expGroupSize[strategy&3]
	if grpsize == 0 {
		return reservedError("exponent strategy", uint32(strategy))
	}
	if ngrps*3*grpsize > len(exp) {
		return shortExponentBuffer(ngrps*3*grpsize, len(exp))
	}

	prevexp := int(absexp)
	n := 0
	for grp := 0; grp < ngrps; grp++ {
		gexp := int(r.Uint32(7))
		if r.Err() != nil {
			return wrap(ErrShortFrame)
		}
		if gexp > maxGexp {
			return reservedError("exponent group", uint32(gexp))
		}
		// Undo the 25/5/1 packing, then the +2 bias each mapped value
		// carries, and run the differentials into absolute exponents.
		for _, dexp := range [3]int{gexp / 25, gexp / 5 % 5, gexp % 5} {
			prevexp += dexp - 2
			// Reading uint(prevexp) catches the negative case too: a
			// differential chain that walks below zero wraps to a huge
			// value rather than passing the upper bound.
			if uint(prevexp) > maxExponent {
				return badExponent(prevexp)
			}
			for j := 0; j < grpsize; j++ {
				exp[n] = uint8(prevexp)
				n++
			}
		}
	}
	return nil
}
