package ac3

import "testing"

// rematDecoder is a decoder wound up just enough to run rematrix: the stereo
// mode, both channels coding their whole bandwidth, and every band flagged
// unless a test says otherwise.
func rematDecoder() *Decoder {
	d := NewDecoder()
	d.h.Acmod = AcmodStereo
	d.endmant[0], d.endmant[1] = 253, 253
	d.nrematbnd = 4
	d.rematflg = [4]bool{true, true, true, true}
	return d
}

// rematBlock fills both channels so that a rematrixed bin is unmistakable:
// left holds 1 and right holds 2, so the butterfly leaves 3 and -1.
func rematBlock() *Block {
	b := &Block{}
	for bin := range MaxCoefs {
		b.Coeffs[0][bin] = 1
		b.Coeffs[1][bin] = 2
	}
	return b
}

// rematerialised reports whether a bin went through the butterfly.
func rematerialised(b *Block, bin int) bool {
	return b.Coeffs[0][bin] == 3 && b.Coeffs[1][bin] == -1
}

// untouched reports whether a bin still holds what rematBlock put there.
func untouched(b *Block, bin int) bool {
	return b.Coeffs[0][bin] == 1 && b.Coeffs[1][bin] == 2
}

// TestRematrixBands pins which bins each band covers. The boundaries are the
// whole content of the feature: get one wrong and a handful of bins come out
// as the sum and difference of a stereo pair rather than the pair itself,
// which is a channel swap on part of the spectrum and nothing more subtle.
func TestRematrixBands(t *testing.T) {
	tests := []struct {
		name  string
		flags [4]bool
		lo    int // first bin expected to change
		hi    int // first bin past it
	}{
		{"band 0 covers bins 13 to 24", [4]bool{true, false, false, false}, 13, 25},
		{"band 1 covers bins 25 to 36", [4]bool{false, true, false, false}, 25, 37},
		{"band 2 covers bins 37 to 60", [4]bool{false, false, true, false}, 37, 61},
		{"band 3 covers bins 61 to 252", [4]bool{false, false, false, true}, 61, 253},
		{"every band covers bins 13 to 252", [4]bool{true, true, true, true}, 13, 253},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := rematDecoder()
			d.rematflg = tt.flags
			b := rematBlock()
			d.rematrix(b)

			for bin := range MaxCoefs {
				want := bin >= tt.lo && bin < tt.hi
				if want && !rematerialised(b, bin) {
					t.Fatalf("bin %d: got %v/%v, want the butterfly's 3/-1",
						bin, b.Coeffs[0][bin], b.Coeffs[1][bin])
				}
				if !want && !untouched(b, bin) {
					t.Fatalf("bin %d is outside the flagged band but changed to %v/%v",
						bin, b.Coeffs[0][bin], b.Coeffs[1][bin])
				}
			}
		})
	}
}

// TestRematrixStopsAtTheNarrowerChannel checks the bound. Rematrixing pairs
// two channels bin for bin, so it can only run as far as the narrower of the
// two codes: past that there is no pair, only one channel and silence, and
// summing them would spread the surviving channel into both.
func TestRematrixStopsAtTheNarrowerChannel(t *testing.T) {
	d := rematDecoder()
	d.endmant[0], d.endmant[1] = 253, 40
	b := rematBlock()
	d.rematrix(b)

	for bin := range MaxCoefs {
		want := bin >= 13 && bin < 40
		if want && !rematerialised(b, bin) {
			t.Fatalf("bin %d: got %v/%v, want the butterfly's 3/-1",
				bin, b.Coeffs[0][bin], b.Coeffs[1][bin])
		}
		if !want && !untouched(b, bin) {
			t.Fatalf("bin %d is past the narrower channel's %d but changed to %v/%v",
				bin, 40, b.Coeffs[0][bin], b.Coeffs[1][bin])
		}
	}
}

// TestRematrixOnlyStereo checks that the other modes are left alone. Only 2/0
// carries rematrix flags, so a decoder that ran the butterfly anywhere else
// would be mixing two unrelated channels together.
func TestRematrixOnlyStereo(t *testing.T) {
	for _, acmod := range []uint8{AcmodDualMono, AcmodMono, Acmod3F, Acmod2F1R, Acmod3F2R} {
		d := rematDecoder()
		d.h.Acmod = acmod
		b := rematBlock()
		d.rematrix(b)
		for bin := range MaxCoefs {
			if !untouched(b, bin) {
				t.Fatalf("acmod %d bin %d: rematrixed a mode that carries no rematrix flags", acmod, bin)
			}
		}
	}
}
