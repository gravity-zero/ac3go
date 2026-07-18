package ac3

// Rematrixing, clause 4.4.3.20 for the syntax and clause 6.3.3 for what it
// means.
//
// A stereo encoder that finds a band nearly the same in both channels can send
// the sum and the difference instead of the left and the right. The difference
// is then tiny and costs almost nothing to code, which is where the saving
// comes from: two loud correlated channels become one loud channel and one
// quiet one. Undoing it is the same butterfly again, which is why the encoder
// spends no bits saying what to undo, only where.
//
// This runs on the stereo mode alone. The other modes carry no rematrix flags
// at all: the technique needs a pair of channels that hold the same thing, and
// only 2/0 has one by construction.

// rematrixBands are the bin boundaries of the four rematrix bands (clause
// 4.4.3.20). The first band starts at bin 13: below that the channels are too
// far apart in frequency for the trick to pay, and the encoder sends them
// plainly.
var rematrixBands = [5]int{13, 25, 37, 61, 253}

// rematrix turns the sum and difference bands back into left and right.
func (d *Decoder) rematrix(b *Block) {
	if d.h.Acmod != AcmodStereo {
		return
	}

	// Where the two channels stop coding for themselves. A coupled channel
	// stops at the coupling start, and what came out of the coupling channel
	// above that was never summed with anything, so it is not rematrixed: the
	// bins here are the ones each channel coded on its own.
	end := min(d.endmant[0], d.endmant[1])

	for bnd := range d.nrematbnd {
		if !d.rematflg[bnd] {
			continue
		}
		for bin := rematrixBands[bnd]; bin < min(rematrixBands[bnd+1], end); bin++ {
			l := b.Coeffs[0][bin]
			r := b.Coeffs[1][bin]
			b.Coeffs[0][bin] = l + r
			b.Coeffs[1][bin] = l - r
		}
	}
}
