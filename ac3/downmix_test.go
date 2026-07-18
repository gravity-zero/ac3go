package ac3

import (
	"math"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// The end to end test in internal/e2e is what proves these coefficients, by
// comparing real downmixed streams against the reference. It needs the oracle
// to run, which the CI will not have, and it reports one number for the whole
// stream. These pin the arithmetic where it can be read: what each channel
// contributes, to which side, at what level.

// header51 is a 3/2 header stating mix levels by code.
func header51(cmixlev, surmixlev uint8) *Header {
	h := &Header{Acmod: Acmod3F2R, Lfeon: true}
	h.HasCmixlev, h.Cmixlev = true, cmixlev
	h.HasSurmixlev, h.Surmixlev = true, surmixlev
	return h
}

// TestDownmixCoeffsSumToOne is the property the whole level policy is: every
// coded channel at full scale sums to full scale in each output, and to no
// more. It holds for every mode and every pair of stated levels, which is what
// makes it a property rather than a table.
func TestDownmixCoeffsSumToOne(t *testing.T) {
	for acmod := range uint8(8) {
		for cmixlev := range uint8(4) {
			for surmixlev := range uint8(4) {
				h := &Header{Acmod: acmod}
				h.HasCmixlev = acmod&1 != 0 && acmod != AcmodMono
				h.Cmixlev = cmixlev
				h.HasSurmixlev = acmod&4 != 0
				h.Surmixlev = surmixlev

				var c downmixCoeffs
				setDownmixCoeffs(h, &c)

				for out := range 2 {
					var sum float64
					for ch := range h.FullBandwidthChannels() {
						if c[out][ch] < 0 {
							t.Errorf("acmod %d out %d ch %d: negative coefficient %v: "+
								"the Lo/Ro downmix adds channels, it does not subtract them",
								acmod, out, ch, c[out][ch])
						}
						sum += float64(c[out][ch])
					}
					if math.Abs(sum-1) > 1e-6 {
						t.Errorf("acmod %d, cmixlev %d, surmixlev %d: output %d sums to %v, want 1",
							acmod, cmixlev, surmixlev, out, sum)
					}
				}
			}
		}
	}
}

// TestDownmix51Coeffs works the 3/2 case through by hand, since it is the one
// every real stream uses.
func TestDownmix51Coeffs(t *testing.T) {
	// Code 1 is -4,5 dB for the centre and -6 dB for the surrounds, which is
	// what a real encoder states and what the reserved code falls back to.
	h := header51(1, 1)
	var c downmixCoeffs
	setDownmixCoeffs(h, &c)

	const cmix, smix = 0.5946035575013605, 0.5
	norm := 1 / (1 + cmix + smix)

	// L C R Ls Rs. Left takes L, the centre and the left surround; right takes
	// the mirror. Neither takes the other's front or surround, and the LFE is
	// in neither: it is not a full bandwidth channel and never reaches here.
	for _, w := range []struct {
		out, ch int
		want    float64
	}{
		{0, 0, norm}, {0, 1, cmix * norm}, {0, 2, 0}, {0, 3, smix * norm}, {0, 4, 0},
		{1, 0, 0}, {1, 1, cmix * norm}, {1, 2, norm}, {1, 3, 0}, {1, 4, smix * norm},
	} {
		if got := float64(c[w.out][w.ch]); math.Abs(got-w.want) > 1e-6 {
			t.Errorf("coeff[out %d][ch %d] = %v, want %v", w.out, w.ch, got, w.want)
		}
	}

	// The level this lands on, stated as the number it is: a channel that had
	// the output to itself comes out 6,42 dB down. It is not a rounding of the
	// 7,65 dB the spec's fixed factor would give, it is a different policy, and
	// this is the line that would move if the policy did.
	if db := 20 * math.Log10(norm); math.Abs(db-(-6.42)) > 0.01 {
		t.Errorf("a lone channel comes out %.2f dB down, want -6.42", db)
	}
}

// TestDownmixMonoSurroundSplits pins the 2/1 and 3/1 modes, whose single
// surround has to reach both outputs without gaining power on the way.
func TestDownmixMonoSurroundSplits(t *testing.T) {
	h := &Header{Acmod: Acmod2F1R}
	h.HasSurmixlev, h.Surmixlev = true, 0 // -3 dB
	var c downmixCoeffs
	setDownmixCoeffs(h, &c)

	if c[0][2] != c[1][2] {
		t.Errorf("the single surround reaches the two outputs at %v and %v: it must be even",
			c[0][2], c[1][2])
	}
	if c[0][2] == 0 {
		t.Error("the single surround reaches neither output")
	}
}

// TestSetDownmixRejectsWhatItCannotDo keeps the API honest: a layout this
// decoder cannot produce has to say so rather than hand back something else.
func TestSetDownmixRejectsWhatItCannotDo(t *testing.T) {
	d := NewDecoder()
	if err := d.SetDownmix(pcm.Layout3F2R); err == nil {
		t.Error("SetDownmix accepted 3/2: it can only fold down, not up or across")
	}
	for _, l := range []pcm.Layout{nil, pcm.LayoutStereo, pcm.LayoutMono} {
		if err := d.SetDownmix(l); err != nil {
			t.Errorf("SetDownmix(%v): %v", l, err)
		}
	}
}

// TestDownmixLeavesStereoAlone pins that asking a stereo stream for stereo is
// not a mix: the coefficients would be the identity anyway, but going through
// them would cost a pass over every sample of every frame for nothing.
func TestDownmixLeavesStereoAlone(t *testing.T) {
	d := NewDecoder()
	if err := d.SetDownmix(pcm.LayoutStereo); err != nil {
		t.Fatal(err)
	}
	d.h.Acmod = AcmodStereo
	if d.downmixing() {
		t.Error("a 2/0 stream asked for stereo is being mixed down to itself")
	}
	// Dual mono is the exception: two channels, but not a left and a right.
	d.h.Acmod = AcmodDualMono
	if !d.downmixing() {
		t.Error("a dual mono stream asked for stereo is not being mixed")
	}
}
