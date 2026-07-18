package ac3

import (
	"math"
	"math/rand"
	"testing"
)

// idct6Naive is the transform idct6 computes, written as the spec defines it
// rather than as the reference factors it (clause E.3.3.4):
//
//	out[n] = sum over k of a[k] * pre[k] * cos(pi*(2n+1)*k/12)
//
// with a[0] = 1 and a[k] = sqrt(2) otherwise. It is 36 multiplies to idct6's 3,
// and it is the point of comparison: a butterfly that agrees with the sum it
// claims to compute is a butterfly whose factoring is right. Nothing here is
// derived from idct6, so this test can fail.
func idct6Naive(pre *[BlocksPerFrame]int32) [BlocksPerFrame]float64 {
	var out [BlocksPerFrame]float64
	for n := range BlocksPerFrame {
		for k := range BlocksPerFrame {
			a := math.Sqrt2
			if k == 0 {
				a = 1
			}
			out[n] += a * float64(pre[k]) * math.Cos(math.Pi*float64(2*n+1)*float64(k)/12)
		}
	}
	return out
}

// idct6Tol is how far idct6 may sit from the sum it computes. Its three fixed
// point products each truncate toward minus infinity, losing up to one unit,
// and up to three of them reach any one output. Four is that bound with a unit
// to spare, and it is absolute rather than relative because the truncation is:
// the error does not grow with the input, which is what makes this a real
// bound and not a fudge factor.
const idct6Tol = 4.0

func TestIDCT6MatchesItsDefinition(t *testing.T) {
	// A basis vector per input, then random ones. The basis vectors are what
	// pin each coefficient of the butterfly separately: a factoring that got
	// one of the three constants wrong still passes on random input often
	// enough to be worth ruling out directly.
	var cases [][BlocksPerFrame]int32
	for k := range BlocksPerFrame {
		var v [BlocksPerFrame]int32
		v[k] = 1 << 20
		cases = append(cases, v)
		v[k] = -(1 << 20)
		cases = append(cases, v)
	}
	rng := rand.New(rand.NewSource(1))
	for range 200 {
		var v [BlocksPerFrame]int32
		for k := range BlocksPerFrame {
			// The full range of a pre-mantissa, which is what the quantizers
			// above produce: 24 bit fixed point with its sign.
			v[k] = rng.Int31n(1<<24) - (1 << 23)
		}
		cases = append(cases, v)
	}

	for _, in := range cases {
		want := idct6Naive(&in)
		got := in
		idct6(&got)
		for n := range BlocksPerFrame {
			if math.Abs(float64(got[n])-want[n]) > idct6Tol {
				t.Errorf("idct6(%v)[%d] = %d, want %.3f (tolerance %.0f)",
					in, n, got[n], want[n], idct6Tol)
			}
		}
	}
}

// TestIDCT6IsLinear pins the property the whole scheme rests on: the encoder
// ran a forward DCT over six blocks and quantized the result, so undoing it has
// to be a linear map or the six blocks do not come back.
func TestIDCT6IsLinear(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	for range 100 {
		var a, b, sum [BlocksPerFrame]int32
		for k := range BlocksPerFrame {
			a[k] = rng.Int31n(1<<22) - (1 << 21)
			b[k] = rng.Int31n(1<<22) - (1 << 21)
			sum[k] = a[k] + b[k]
		}
		ta, tb, tsum := a, b, sum
		idct6(&ta)
		idct6(&tb)
		idct6(&tsum)
		for n := range BlocksPerFrame {
			// Three truncations per transform, and this compares two of them
			// against one, so the slack is twice idct6Tol.
			if d := math.Abs(float64(ta[n]) + float64(tb[n]) - float64(tsum[n])); d > 2*idct6Tol {
				t.Errorf("idct6 is not additive at %d: %d + %d != %d (off by %.0f)",
					n, ta[n], tb[n], tsum[n], d)
			}
		}
	}
}

// TestIDCT6DCIsFlat pins the one value that can be reasoned about without any
// arithmetic: a bin whose six blocks are identical has all its energy in the
// first pre-mantissa and nothing in the other five, so decoding that alone must
// give six identical blocks back. It is the case AHT exists for.
func TestIDCT6DCIsFlat(t *testing.T) {
	for _, dc := range []int32{1 << 23, -(1 << 23), 1, 0, -1} {
		v := [BlocksPerFrame]int32{0: dc}
		idct6(&v)
		for n := range BlocksPerFrame {
			if v[n] != dc {
				t.Errorf("idct6 of DC %d gave block %d = %d, want %d", dc, n, v[n], dc)
			}
		}
	}
}

func TestUngroupGaqGains(t *testing.T) {
	// The reference's table, written out as the reference documents it:
	// tab[i] = { i/9, (i%9)/3, (i%9)%3 }. Only the 27 codes an encoder can
	// emit are checked here; the rest are the clamp's business, below.
	for code := range 27 {
		a, b, c := ungroupGaqGains(code)
		if a > 2 || b > 2 || c > 2 {
			t.Errorf("ungroupGaqGains(%d) = %d,%d,%d: a gain above 2 has no entry in the remap tables", code, a, b, c)
		}
		// The digits have to reconstruct the code, or two of the three gains
		// have been swapped - which no test of their range would catch.
		if got := int(a)*9 + int(b)*3 + int(c); got != code {
			t.Errorf("ungroupGaqGains(%d) = %d,%d,%d, which reads back as %d", code, a, b, c, got)
		}
	}
	// The five codes an encoder cannot emit, which the field can still hold.
	for code := 27; code < 32; code++ {
		a, b, c := ungroupGaqGains(code)
		if a != 2 || b != 2 || c != 2 {
			t.Errorf("ungroupGaqGains(%d) = %d,%d,%d, want the clamp to 26 = 2,2,2", code, a, b, c)
		}
	}
	// The first code is no gain at all, on all three.
	if a, b, c := ungroupGaqGains(0); a != 0 || b != 0 || c != 0 {
		t.Errorf("ungroupGaqGains(0) = %d,%d,%d, want 0,0,0", a, b, c)
	}
}

func TestHebapTabShape(t *testing.T) {
	// The same address space as baptab: the model above it is identical, and
	// only the table it indexes differs.
	if len(hebapTab) != len(baptab) {
		t.Fatalf("hebapTab has %d entries, want %d, the same address space as baptab", len(hebapTab), len(baptab))
	}
	if hebapTab[0] != 0 {
		t.Errorf("hebapTab[0] = %d, want 0: a mantissa at its mask gets no bits", hebapTab[0])
	}
	for i := 1; i < len(hebapTab); i++ {
		if hebapTab[i] < hebapTab[i-1] {
			t.Errorf("hebapTab[%d] = %d drops below hebapTab[%d] = %d", i, hebapTab[i], i-1, hebapTab[i-1])
		}
	}
	// Nineteen, where baptab stops at fifteen. This is the whole reason the
	// table exists, so it is worth stating rather than implying.
	if got := hebapTab[len(hebapTab)-1]; got != 19 {
		t.Errorf("hebapTab saturates at %d, want 19", got)
	}
	// Every hebap the table can produce must be a legal index into
	// bitsVsHebap, which is what readAHTMantissas indexes it with unchecked.
	for i, hebap := range hebapTab {
		if int(hebap) >= len(bitsVsHebap) {
			t.Fatalf("hebapTab[%d] = %d, out of range of bitsVsHebap (%d entries)", i, hebap, len(bitsVsHebap))
		}
	}
}

// TestMantissaVQCodebooksAreExhaustive is what makes readAHTMantissas safe to
// index a codebook with a raw bit stream field. A codebook that were one entry
// short of its index's range would turn a legal stream into a panic, so the
// invariant is pinned rather than trusted - and hebap 4's zero filled last row
// is precisely why it is not obviously true.
func TestMantissaVQCodebooksAreExhaustive(t *testing.T) {
	for hebap := 1; hebap < len(mantissaVQ); hebap++ {
		want := 1 << bitsVsHebap[hebap]
		if got := len(mantissaVQ[hebap]); got != want {
			t.Errorf("mantissaVQ[%d] has %d entries, want %d: a %d bit index must reach every one",
				hebap, got, want, bitsVsHebap[hebap])
		}
	}
	// Hebap 0 reads no codeword at all, so it has no codebook.
	if mantissaVQ[0] != nil {
		t.Errorf("mantissaVQ[0] is not nil, but hebap 0 is the unallocated one")
	}
	// The codebooks are for the vector quantized hebaps only. From 8 up a bin
	// is scalar quantized and its width means something else entirely, so a
	// codebook there would mean the two codings had been confused.
	if len(mantissaVQ) != 8 {
		t.Errorf("mantissaVQ has %d codebooks, want 8: hebap 8 and up are not vector quantized", len(mantissaVQ))
	}
}

// TestGaqRemapTablesCoverTheirIndices pins the ranges readAHTMantissas indexes
// these with. The gain tables are reached only for a hebap below the mode's
// end, which is 17, so they need entries for hebaps 8 to 16; the gain 1 table
// is reached for any hebap from 8 to 19.
func TestGaqRemapTablesCoverTheirIndices(t *testing.T) {
	if len(gaqRemap1) != 20-8 {
		t.Errorf("gaqRemap1 has %d entries, want %d: hebaps 8 to 19", len(gaqRemap1), 20-8)
	}
	maxEndBap := 0
	for _, e := range gaqEndBap {
		maxEndBap = max(maxEndBap, int(e))
	}
	if len(gaqRemap24A) != maxEndBap-8 {
		t.Errorf("gaqRemap24A has %d entries, want %d: hebaps 8 to %d",
			len(gaqRemap24A), maxEndBap-8, maxEndBap-1)
	}
	if len(gaqRemap24B) != maxEndBap-8 {
		t.Errorf("gaqRemap24B has %d entries, want %d: hebaps 8 to %d",
			len(gaqRemap24B), maxEndBap-8, maxEndBap-1)
	}
	// The last correction is nothing: by hebap 19 the quantizer is fine enough
	// that a code read as a plain fraction is already where it belongs.
	if gaqRemap1[len(gaqRemap1)-1] != 0 {
		t.Errorf("gaqRemap1 ends at %d, want 0", gaqRemap1[len(gaqRemap1)-1])
	}
}

// TestIDCT6Coefficients pins the three constants against the trigonometry they
// are named for. A digit wrong in any of them is a bug no structural test of
// the butterfly could see, because the butterfly would still be self-consistent.
func TestIDCT6Coefficients(t *testing.T) {
	for _, c := range []struct {
		name string
		got  int64
		k    float64
	}{
		{"idct6Coeff0", idct6Coeff0, 2},
		{"idct6Coeff1", idct6Coeff1, 0},
		{"idct6Coeff2", idct6Coeff2, 5},
	} {
		want := math.Round(math.Sqrt2 * math.Cos(c.k*math.Pi/12) * (1 << 23))
		if float64(c.got) != want {
			t.Errorf("%s = %d, want %.0f (sqrt(2)*cos(%.0f*pi/12) << 23)", c.name, c.got, want, c.k)
		}
	}
}

// TestGaqDequantIsMonotonic pins the one property a quantizer cannot be wrong
// about: a larger code must mean a larger value. It is worth pinning here
// because the GAQ dequantizers are the only arithmetic in this package that
// corrects a value rather than merely scaling it, and the corrections are
// signed table lookups - a slope of the wrong sign, or the two tails' offsets
// swapped, still produces plausible-looking audio and would only ever show up
// as a stream that does not quite match the reference.
func TestGaqDequantIsMonotonic(t *testing.T) {
	for hebap := uint8(8); hebap < 20; hebap++ {
		bits := bitsVsHebap[hebap]

		// The ungained quantizer, over every code its width can hold.
		t.Run("small", func(t *testing.T) {
			for _, logGain := range []int32{0, 1, 2} {
				if logGain > 0 && int(hebap) >= int(gaqEndBap[gaq124]) {
					continue // no gain reaches this hebap
				}
				gbits := uint(int32(bits) - logGain)
				prev := int32(math.MinInt32)
				for code := -(1 << (gbits - 1)); code < 1<<(gbits-1); code++ {
					got := gaqSmall(hebap, logGain, int32(code))
					if got <= prev {
						t.Fatalf("gaqSmall(hebap %d, gain %d, code %d) = %d, not above the previous code's %d",
							hebap, logGain, code, got, prev)
					}
					prev = got
				}
			}
		})

		// The escape's quantizer, which only exists where a gain does.
		if int(hebap) >= int(gaqEndBap[gaq124]) {
			continue
		}
		t.Run("large", func(t *testing.T) {
			for _, logGain := range []int32{1, 2} {
				mbits := uint(int32(bits) - (2 - logGain))
				prev := int32(math.MinInt32)
				for code := -(1 << (mbits - 1)); code < 1<<(mbits-1); code++ {
					got := gaqLarge(hebap, logGain, int32(code), mbits)
					if got <= prev {
						t.Fatalf("gaqLarge(hebap %d, gain %d, code %d) = %d, not above the previous code's %d",
							hebap, logGain, code, got, prev)
					}
					prev = got
				}
			}
		})
	}
}

// TestGaqLargeReachesFurtherThanSmall pins why the escape exists at all. A gain
// narrows the quantizer on a bet that the six values are small; the escape is
// how the encoder pays when one is not, so the value it recovers has to reach
// beyond what the narrowed quantizer could have said on its own. An escape that
// did not would be a bit spent on nothing.
func TestGaqLargeReachesFurtherThanSmall(t *testing.T) {
	for hebap := uint8(8); hebap < gaqEndBap[gaq124]; hebap++ {
		bits := bitsVsHebap[hebap]
		for _, logGain := range []int32{1, 2} {
			gbits := uint(int32(bits) - logGain)
			mbits := uint(int32(bits) - (2 - logGain))

			// The largest the narrowed quantizer can say, skipping the code
			// that the escape has taken over.
			smallMax := gaqSmall(hebap, logGain, int32(1<<(gbits-1))-1)
			largeMax := gaqLarge(hebap, logGain, int32(1<<(mbits-1))-1, mbits)
			if largeMax <= smallMax {
				t.Errorf("hebap %d gain %d: the escape reaches %d, no further than the %d the plain code already reaches",
					hebap, logGain, largeMax, smallMax)
			}
			smallMin := gaqSmall(hebap, logGain, -(1<<(gbits-1))+1)
			largeMin := gaqLarge(hebap, logGain, -(1 << (mbits - 1)), mbits)
			if largeMin >= smallMin {
				t.Errorf("hebap %d gain %d: the escape reaches %d, no further than the %d the plain code already reaches",
					hebap, logGain, largeMin, smallMin)
			}
		}
	}
}

// TestGaqSmallStretches pins the direction of the ungained correction, which
// monotonicity alone cannot see: a factor of the wrong sign still gives a
// well-ordered quantizer, just one that shrinks every mantissa by up to a
// seventh instead of stretching it. The correction exists because the uniform
// quantizer's range is narrower than the mantissa it is coded in, so it can
// only ever move a value away from zero, never toward it.
func TestGaqSmallStretches(t *testing.T) {
	for hebap := uint8(8); hebap < 20; hebap++ {
		bits := bitsVsHebap[hebap]
		for code := -(1 << (bits - 1)); code < 1<<(bits-1); code++ {
			plain := int32(code) * (1 << (24 - bits))
			got := gaqSmall(hebap, 0, int32(code))
			if abs32(got) < abs32(plain) {
				t.Fatalf("gaqSmall(hebap %d, gain 0, code %d) = %d, short of the %d it started from: the correction shrank it",
					hebap, code, got, plain)
			}
			// It is a correction, not a rescale: the last table entry is zero
			// and the largest is 4681/32768, so it can never reach a seventh.
			if abs32(got) > abs32(plain)+abs32(plain)/6 {
				t.Fatalf("gaqSmall(hebap %d, gain 0, code %d) = %d, more than a sixth above the %d it started from",
					hebap, code, got, plain)
			}
			// A gained mantissa is not corrected at all.
			if hebap < gaqEndBap[gaq124] {
				if got := gaqSmall(hebap, 1, int32(code)); got != plain {
					t.Fatalf("gaqSmall(hebap %d, gain 1, code %d) = %d, want the uncorrected %d",
						hebap, code, got, plain)
				}
			}
		}
	}
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}
