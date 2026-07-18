package ac3

// Coupling, clause 4.4.3.14 onwards for the syntax and clause 6.3.2 for what
// it means.
//
// Above some frequency the ear stops locating sound by waveform and starts
// locating it by envelope, so an encoder can stop sending each channel's own
// high frequencies and send one shared channel instead, plus a gain per
// channel per band to give each one its own envelope back. That shared channel
// is the coupling channel, the gains are the coupling coordinates, and undoing
// it is what this file does.
//
// The result is not the original: the fine structure above the coupling start
// is one channel's, handed to several. That is the point of the technique, not
// a shortcoming of this decoder.

// cplSubbandBins is the width of a coupling sub-band (clause 4.4.3.13), and
// cplBandBase the bin its grid starts at. Bands group whole sub-bands, so every
// band is a multiple of the one and offset from the other.
const (
	cplSubbandBins = 12
	cplBandBase    = 37
)

// pow2neg[i] is 2^-i. The coupling coordinate's exponent reaches 24 (a 4 bit
// exponent plus a master of 3 times 3) and its mantissa shifts two more, so 32
// entries is room to spare.
var pow2neg = func() (out [32]float32) {
	for i := range out {
		out[i] = float32(1) / float32(uint64(1)<<uint(i))
	}
	return out
}()

// couplingCoord turns one band's coded coupling coordinate into the gain that
// spreads the coupling channel back into one channel (clause 6.3.2).
//
// The mantissa carries an implicit leading one, which is what the 16 adds:
// the coordinate is a normalised fraction times a power of two, so it spans a
// huge range with eight bits. Exponent 15 is the denormal end of it, where the
// leading one is dropped so the gain can reach all the way down to zero.
//
// The constant offsets look arbitrary written out like this. They are what two
// independent reference decoders agree on to the bit, which is the only thing
// that matters here: the absolute scale of a coupling coordinate is not
// something a spectrum test can catch, only a comparison against a reference.
func couplingCoord(exp, mant, mstr uint8) float32 {
	shift := uint(exp) + 3*uint(mstr)
	if exp == 15 {
		return float32(mant) * pow2neg[1+shift]
	}
	return float32(uint(mant)+16) * pow2neg[2+shift]
}

// decouple spreads the coupling channel back into the channels that gave up
// their high frequencies to it (clause 6.3.2).
//
// It runs after the whole block's mantissas are read, not during, because the
// coupling channel's mantissas arrive in the middle of the channel loop, after
// the first coupled channel and before the rest.
func (d *Decoder) decouple(b *Block) {
	if !d.cplinu {
		return
	}

	band := 0
	for sub := range d.ncplsubnd {
		if sub > 0 && !d.cplbndstrc[sub] {
			band++
		}
		start := d.cplstrtmant + sub*cplSubbandBins
		end := min(start+cplSubbandBins, d.cplendmant)

		for bin := start; bin < end; bin++ {
			// The coupling channel's own allocation decides which bins were
			// worth bits, so it is the coupling channel's bap that says where
			// the noise goes, for every channel that shares it.
			unallocated := d.bap[MaxChannels][bin] == 0
			cpl := b.Cpl[bin]
			e := expScale[d.exp[MaxChannels][bin]]

			for ch := range d.nfchans {
				if !d.chincpl[ch] {
					continue
				}
				co := d.cplco[ch][band]
				// A phase flag says this band of the right channel was in
				// antiphase with the left before they were summed into one
				// channel, so putting it back means inverting it.
				if ch == 1 && d.phsflginu && d.phsflg[band] {
					co = -co
				}
				if !unallocated {
					b.Coeffs[ch][bin] = cpl * co
					continue
				}
				// Dither here rather than in the coupling channel: these bins
				// go out to several channels, and noise they all shared would
				// be one sound in the middle rather than noise. Each channel
				// draws its own (clause 6.3.4).
				if b.Dithflag[ch] && d.mant.dither {
					b.Coeffs[ch][bin] = d.mant.nextDither() * e * co
				} else {
					b.Coeffs[ch][bin] = 0
				}
			}
		}
	}

	// A coupled channel codes nothing of its own above the coupling start, but
	// it now carries what came out of the coupling channel, so its coefficients
	// run to the coupling channel's end.
	for ch := range d.nfchans {
		if d.chincpl[ch] {
			b.EndMant[ch] = d.cplendmant
		}
	}
}
