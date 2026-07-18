package ac3

import (
	"math"
	"testing"
)

// The bit allocation tables are the part of this package a reader cannot check
// by reasoning about the code: they are 700-odd magic numbers, and a single
// wrong one silently shifts one band's allocation, which corrupts every
// mantissa after it. These tests check them against something other than
// themselves.

// TestMasktabMatchesSpec pins the derived bin-to-band map against the values
// clause 6.2.3 prints in table 6.13. The spec says the two banding tables hold
// duplicate information; this is what proves it for this transcription.
func TestMasktabMatchesSpec(t *testing.T) {
	want := [MaxCoefs]uint8{
		0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15,
		16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 28, 28, 29,
		29, 29, 30, 30, 30, 31, 31, 31, 32, 32, 32, 33, 33, 33, 34, 34,
		34, 35, 35, 35, 35, 35, 35, 36, 36, 36, 36, 36, 36, 37, 37, 37,
		37, 37, 37, 38, 38, 38, 38, 38, 38, 39, 39, 39, 39, 39, 39, 40,
		40, 40, 40, 40, 40, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41, 41,
		41, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 42, 43, 43, 43,
		43, 43, 43, 43, 43, 43, 43, 43, 43, 44, 44, 44, 44, 44, 44, 44,
		44, 44, 44, 44, 44, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45,
		45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 45, 46, 46, 46,
		46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46, 46,
		46, 46, 46, 46, 46, 47, 47, 47, 47, 47, 47, 47, 47, 47, 47, 47,
		47, 47, 47, 47, 47, 47, 47, 47, 47, 47, 47, 47, 47, 48, 48, 48,
		48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48, 48,
		48, 48, 48, 48, 48, 49, 49, 49, 49, 49, 49, 49, 49, 49, 49, 49,
		49, 49, 49, 49, 49, 49, 49, 49, 49, 49, 49, 49, 49, 0, 0, 0,
	}
	if masktab != want {
		for i := range want {
			if masktab[i] != want[i] {
				t.Fatalf("masktab[%d] = %d, want %d", i, masktab[i], want[i])
			}
		}
	}
}

// TestBandingIsContiguous checks that the bands tile the coded spectrum
// without a gap or an overlap. A gap would leave a band's psd unwritten, which
// is the kind of defect that shows up as one wrong bap in one stream.
func TestBandingIsContiguous(t *testing.T) {
	next := 0
	for band := range nBands {
		if bndtab[band] != next {
			t.Fatalf("band %d starts at bin %d, previous band ends at %d", band, bndtab[band], next)
		}
		next += bndsz[band]
	}
	// 253 bins, not 256: the top three bins of the transform are never coded,
	// which is why chbwcod tops out at an endmant of 253.
	if next != chbwcodEndMant(maxChbwcod) {
		t.Fatalf("bands cover %d bins, the widest channel codes %d",
			next, chbwcodEndMant(maxChbwcod))
	}
	if next > MaxCoefs {
		t.Fatalf("bands cover %d bins, past the %d-bin transform", next, MaxCoefs)
	}
}

// TestLatabIsLogAddition checks the log-addition table against the arithmetic
// it stands for, rather than against a second copy of itself. One psd step is
// an exponent step, 6,02 dB, divided by 128; latab[i] is what adding a power
// 2*i steps down adds to a level, in those same units.
func TestLatabIsLogAddition(t *testing.T) {
	// dB of power per psd unit: one exponent is one binary place of amplitude.
	const dbPerStep = 20 * math.Ln2 / math.Ln10 / 128
	for i := range latab {
		down := float64(2*i) * dbPerStep // how far below, in dB
		ideal := 10 * math.Log10(1+math.Pow(10, -down/10)) / dbPerStep
		if math.Abs(ideal-float64(latab[i])) > 1 {
			t.Errorf("latab[%d] = %d, log addition gives %.2f", i, latab[i], ideal)
		}
	}
	// The anchor: two equal powers sum to twice one of them, 3,01 dB up, which
	// is half an exponent step.
	if latab[0] != 64 {
		t.Errorf("latab[0] = %d, want 64: doubling a power is 3,01 dB", latab[0])
	}
}

// TestBaptabIsMonotonic checks the one property a signal to mask ratio table
// must have: more headroom over the mask never buys fewer bits.
func TestBaptabIsMonotonic(t *testing.T) {
	for i := 1; i < len(baptab); i++ {
		if baptab[i] < baptab[i-1] {
			t.Errorf("baptab[%d] = %d drops below baptab[%d] = %d", i, baptab[i], i-1, baptab[i-1])
		}
	}
	if baptab[0] != 0 {
		t.Errorf("baptab[0] = %d, want 0: a mantissa at its mask gets no bits", baptab[0])
	}
	if got := baptab[len(baptab)-1]; got != 15 {
		t.Errorf("baptab saturates at %d, want 15", got)
	}
}

// TestHearingThresholdShape checks the hearing threshold rows against what
// they physically are: a curve that dips in the ear's most sensitive range and
// climbs at both ends, and that at a lower sampling rate reads the same
// physical frequency in a higher band.
func TestHearingThresholdShape(t *testing.T) {
	for fscod, row := range hth {
		lo, hi := row[0], row[nBands-1]
		floorBand, floorVal := 0, row[0]
		for band, v := range row {
			if v < floorVal {
				floorBand, floorVal = band, v
			}
		}
		if floorBand < 25 || floorBand > 40 {
			t.Errorf("fscod %d: threshold is lowest at band %d, expected the ear's sensitive range", fscod, floorBand)
		}
		if lo <= floorVal || hi <= floorVal {
			t.Errorf("fscod %d: threshold does not rise at both ends (%d .. %d .. %d)", fscod, lo, floorVal, hi)
		}
	}
	// Band 0 is one bin wide whatever the rate, so at 32 kHz it spans a lower
	// frequency range than at 48 kHz, where hearing is less sensitive: the
	// threshold there has to be higher.
	if hth[2][0] <= hth[0][0] {
		t.Errorf("hth[2][0] = %#x is not above hth[0][0] = %#x", hth[2][0], hth[0][0])
	}
}
