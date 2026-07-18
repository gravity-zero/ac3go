package ac3

import "math"

// Spectral extension, clause E.1.3.3.5 for the syntax and E.3.3 for what it
// means.
//
// Above some frequency the ear stops hearing a partial as a tone and starts
// hearing the band it sits in as a texture, so an encoder can throw the top of
// the spectrum away and tell the decoder to build a replacement out of the
// bands below it: copy those coefficients up, level each band to the energy the
// encoder measured there, and mix in noise where the original was noise-like.
//
// It is the same bargain as coupling, one step further along. Coupling still
// sends one real signal for the bands it takes over, and hands it to several
// channels; this sends no signal at all - what comes out above the extension's
// start was never in the encoder's input at that frequency, only something with
// its envelope. Two decoders cannot agree on it bit for bit either, because the
// noise it mixes in is drawn from the same "any reasonably random sequence" the
// unallocated mantissas are. The copied half is exact; the noise half is each
// decoder's own, and a comparison against another implementation can only hold
// on the whole in the way SetDither describes.

const (
	// spxMaxBands is the most bands an extension can have. The grid runs to
	// sub-band 17 and starts at 2, so 15 would do; 17 is the length of the
	// default structure table, which is numbered by sub-band of the spectrum
	// and so has to be as long as the grid is high.
	spxMaxBands = 17

	// spxSubbandBins is the width of a spectral extension sub-band, and
	// spxBandBase the bin its grid starts at (clause E.1.3.3.5).
	//
	// This is not the coupling channel's grid: that one starts at bin 37, this
	// one at 25, so a sub-band number means different things to the two and the
	// numbers cannot be carried between them without the offset. The one place
	// they meet is readEAC3CouplingStrategy, where coupling has to stop where
	// this starts.
	spxSubbandBins = 12
	spxBandBase    = 25

	// spxMaxCopySections bounds how many pieces the copy is cut into.
	//
	// Each band contributes at most one section by being cut short at a wrap,
	// and at most one more per full pass over the source, since a pass consumes
	// a whole sub-band's worth of the extension and a band is one sub-band at
	// the least. Two per band plus the final one is therefore always enough,
	// and this being a bound rather than a guess is what keeps a malformed
	// frame from writing past the array.
	spxMaxCopySections = 2*spxMaxBands + 1
)

// eac3DefaultSpxBandStruct is the extension band structure a frame gets when it
// states none (table E.15). Like the coupling one it is numbered by sub-band of
// the spectrum rather than from the extension's own start.
var eac3DefaultSpxBandStruct = [spxMaxBands]bool{
	false, false, false, false, false, false, false, false, true,
	false, true, false, true, false, true, false, true,
}

// eac3SpxAttenTab is the notch filter the extension is softened with, table
// E.16: five taps, symmetric, given here as the three distinct ones.
//
// The spec prints 96 constants; they are 2^(-(bin+1)*(code+1)/15), which is the
// same statement and cannot get one of them wrong. The code chooses how deep
// the notch cuts and the bin how far from its centre, so the attenuation grows
// in both directions from a corner at code 0, bin 0.
var eac3SpxAttenTab = func() (out [32][3]float32) {
	for code := range out {
		for bin := range out[code] {
			out[code][bin] = float32(math.Exp2(float64((bin+1)*(code+1)) / -15))
		}
	}
	return out
}()

// readSpxStrategy reads where the extension takes its bins from, where it puts
// them, and how the range it fills is grouped into bands (clause E.1.3.3.5).
func (d *Decoder) readSpxStrategy(blk int) error {
	r := &d.r

	// Which channels extend. A mono frame states nothing: there is one channel,
	// and the strategy is only being read because something extends.
	d.chinspx = [MaxFBWChannels]bool{}
	if d.h.Acmod == AcmodMono {
		d.chinspx[0] = true
	} else {
		for ch := range d.nfchans {
			d.chinspx[ch] = r.Bool()
		}
	}

	spxstrtf := int(r.Uint32(2))
	spxbegf := int(r.Uint32(3))
	spxendf := int(r.Uint32(3))
	if r.Err() != nil {
		return wrap(ErrShortFrame)
	}

	// The sub-bands the codes stand for. The mapping stops being linear above
	// sub-band 7: the top codes step by two rather than one, which buys a reach
	// of 17 sub-bands out of three bits at the cost of a resolution that
	// nothing up there needs - by that height the bands are wide anyway.
	beginSubbnd := spxbegf + 2
	if spxbegf >= 6 {
		beginSubbnd = spxbegf*2 - 3
	}
	endSubbnd := spxendf + 5
	if spxendf >= 3 {
		endSubbnd = spxendf*2 + 3
	}

	d.spxdststrtmant = spxstrtf*spxSubbandBins + spxBandBase
	d.spxstrtmant = beginSubbnd*spxSubbandBins + spxBandBase
	d.spxendmant = endSubbnd*spxSubbandBins + spxBandBase

	if beginSubbnd >= endSubbnd {
		return badSpxRange(beginSubbnd, endSubbnd)
	}
	// The source has to sit below the extension, and not merely differ from it:
	// the copy reads bins the channel coded and writes bins above them, and a
	// source that started at or above its destination would be reading what the
	// copy has just written.
	if d.spxdststrtmant >= d.spxstrtmant {
		return badSpxCopyStart(d.spxdststrtmant, d.spxstrtmant)
	}

	// The band structure. A block that states none keeps what is there, which
	// reaches across frames: only a block 0 refreshes it to the default first,
	// so an extension that comes up mid-frame with no structure of its own
	// inherits the previous frame's, exactly as the reference decoder does.
	//
	// As with coupling, spxBandStruct is numbered the spec's way - by sub-band
	// of the spectrum, so that one default table serves whatever range a frame
	// extends over - and it is read out from beginSubbnd here.
	if blk == 0 {
		d.spxBandStruct = eac3DefaultSpxBandStruct
	}
	nsubbnd := endSubbnd - beginSubbnd
	if r.Bool() { // spxbndstrce
		for bnd := 1; bnd < nsubbnd; bnd++ {
			d.spxBandStruct[beginSubbnd+bnd] = r.Bool()
		}
	}
	d.nspxbnd = nsubbnd
	d.spxbndsz[0] = spxSubbandBins
	for bnd, subbnd := 0, 1; subbnd < nsubbnd; subbnd++ {
		// A set flag means this sub-band joins the band before it rather than
		// opening one of its own, so the band count falls and the band widens.
		if d.spxBandStruct[beginSubbnd+subbnd] {
			d.nspxbnd--
			d.spxbndsz[bnd] += spxSubbandBins
		} else {
			bnd++
			d.spxbndsz[bnd] = spxSubbandBins
		}
	}
	return r.Err()
}

// readSpxCoords reads each extending channel's blend point and its gain per
// band (clause E.1.3.3.6).
//
// The gains are what make the copy sound like the band it replaces: the copied
// coefficients carry the wrong level, since they came from a different part of
// the spectrum, and these are the levels the encoder measured where they are
// going.
func (d *Decoder) readSpxCoords() error {
	r := &d.r
	f := &d.eac3

	for ch := range d.nfchans {
		if !d.chinspx[ch] {
			// A channel that is not extending in this block will state its
			// gains outright in the next block that it does: there is nothing
			// for that block to inherit.
			f.firstSpxCoords[ch] = true
			continue
		}
		// The first block in which a channel extends carries its gains with no
		// flag to say so, exactly as the coupling gains do: a bit asking "are
		// they here?" could only ever answer yes, so the syntax does not spend
		// it. Reading it anyway shifts every field after it.
		if !f.firstSpxCoords[ch] && !r.Bool() { // spxcoe
			continue
		}
		f.firstSpxCoords[ch] = false

		// spxblnd says where the reconstruction stops being mostly the copied
		// signal and starts being mostly noise, as a fraction of the
		// extension's top in thirty-seconds.
		spxblnd := float32(r.Uint32(5)) * (1.0 / 32)
		mstrspxco := int(r.Uint32(2)) * 3

		bin := d.spxstrtmant
		for bnd := range d.nspxbnd {
			bandsize := d.spxbndsz[bnd]

			// How much of this band should be noise rather than copied signal,
			// from where its middle sits against the blend point: nothing at
			// the bottom of the extension, everything at the top.
			//
			// The two halves are mixed by square roots because they are
			// uncorrelated, so it is their powers that add up to the band's
			// energy rather than their amplitudes. The three under the noise
			// root is what turns a draw uniform over -1 to 1, whose variance is
			// a third, into one whose variance is one.
			nratio := float32(bin+bandsize/2)/float32(d.spxendmant) - spxblnd
			nratio = min(max(nratio, 0), 1)
			nblend := float32(math.Sqrt(float64(3 * nratio)))
			sblend := float32(math.Sqrt(float64(1 - nratio)))
			bin += bandsize

			// The gain itself, coded the way a coupling coordinate is: a
			// mantissa with an implicit leading one, which is what the four
			// adds, against an exponent, with 15 the denormal end where the
			// leading one is dropped so the gain can reach zero. The master
			// exponent is shared by every band of the channel and buys three
			// bits of range each.
			//
			// The shift can be written as a shift and not as a division
			// because it cannot go negative: the exponent reaches 15 and the
			// master 9, and 25 less both is still one.
			spxcoexp := int(r.Uint32(4))
			spxcomant := int(r.Uint32(2))
			if spxcoexp == 15 {
				spxcomant <<= 1
			} else {
				spxcomant += 4
			}
			spxco := float32(spxcomant<<(25-spxcoexp-mstrspxco)) * (1.0 / (1 << 23))

			d.spxnblend[ch][bnd] = nblend * spxco
			d.spxsblend[ch][bnd] = sblend * spxco
		}
	}
	return r.Err()
}

// mapSpxCopy cuts the copy into the sections it takes to fill the extension
// from a source shorter than it, and marks the bands the copy restarts at.
//
// The source is the bins from spxdststrtmant up to spxstrtmant, and the range
// to fill is usually wider than that, so the copy runs over the source again
// and again. Where it restarts there is a discontinuity that was never in the
// signal - the end of the source butted against its own start - and those are
// the points the notch filter softens later.
//
// A band is cut short at a restart rather than allowed to straddle one, which
// is what the test before the inner loop is for: it looks at the whole band
// before any of it is copied, so a band that would not fit in what is left of
// the source starts a fresh pass instead. The result is that a band's copied
// bins are contiguous in the source, which is what makes the energy the next
// step measures mean anything.
//
// None of this depends on the channel, only on the strategy, so it is computed
// once per block and used for every channel that extends.
func (d *Decoder) mapSpxCopy() {
	d.spxwrap = [spxMaxBands]bool{}
	// Band 0 restarts by definition: it is where the coded spectrum ends and
	// the invented one begins, which is the discontinuity the notch exists for
	// in the first place.
	d.spxwrap[0] = true

	// Everything here counts bins from the start of the source rather than from
	// the start of the spectrum, which is what a section's length is.
	src := d.spxstrtmant - d.spxdststrtmant
	bin, n := 0, 0
	for bnd := range d.nspxbnd {
		bandsize := d.spxbndsz[bnd]
		if bin+bandsize > src {
			d.spxCopySize[n] = bin
			n++
			bin = 0
			d.spxwrap[bnd] = true
		}
		for i := 0; i < bandsize; {
			if bin == src {
				d.spxCopySize[n] = bin
				n++
				bin = 0
			}
			copysize := min(bandsize-i, src-bin)
			bin += copysize
			i += copysize
		}
	}
	d.spxCopySize[n] = bin
	d.nspxCopy = n + 1
}

// applySpx rebuilds the top of every extending channel's spectrum (clause
// E.3.3).
//
// It runs after the mantissas are unpacked and the channels are decoupled and
// unrematrixed, and before the transform, which is where the reference puts it
// too. The one difference is the dynamic range gain: the reference has already
// applied it by this point and this decoder applies it on the way into the
// filter bank, and that is a difference without a distinction, because the gain
// is one number for the whole channel and everything below is linear in it. The
// copy scales with it, the energy measured off the copy scales with it, and so
// the noise built from that energy scales with it too.
func (d *Decoder) applySpx(b *Block) {
	if !d.spxinu {
		return
	}
	d.mapSpxCopy()

	for ch := range d.nfchans {
		if !d.chinspx[ch] {
			continue
		}
		coeffs := &b.Coeffs[ch]

		bin := d.spxstrtmant
		for _, n := range d.spxCopySize[:d.nspxCopy] {
			copy(coeffs[bin:bin+n], coeffs[d.spxdststrtmant:d.spxdststrtmant+n])
			bin += n
		}

		// The energy each band came out with, measured on the copy rather than
		// on the source it was taken from: a band's source bins are contiguous
		// but they are not the same bins for every band, and it is the copy the
		// gains are about to be applied to.
		bin = d.spxstrtmant
		for bnd := range d.nspxbnd {
			var acc float32
			for range d.spxbndsz[bnd] {
				acc += coeffs[bin] * coeffs[bin]
				bin++
			}
			d.spxrms[bnd] = float32(math.Sqrt(float64(acc / float32(d.spxbndsz[bnd]))))
		}

		// The notch, at the seam between the coded spectrum and the invented
		// one and at every restart of the copy. Both are steps the signal never
		// had, and a step in the spectrum is a tone in the samples - a whistle
		// sitting at the extension's start, which is precisely where a listener
		// would notice it. The filter reaches two bins below the seam, into
		// coefficients the channel really coded, because a notch cut on one
		// side of a step only moves the step.
		//
		// A channel that stated no attenuation code wants none: this is the
		// encoder's judgement of its own content, not a stage of the decode.
		if code := d.eac3.spxAttenCode[ch]; code >= 0 {
			atten := &eac3SpxAttenTab[code]
			bin := d.spxstrtmant - 2
			for bnd := range d.nspxbnd {
				if d.spxwrap[bnd] {
					coeffs[bin+0] *= atten[0]
					coeffs[bin+1] *= atten[1]
					coeffs[bin+2] *= atten[2]
					coeffs[bin+3] *= atten[1]
					coeffs[bin+4] *= atten[0]
				}
				bin += d.spxbndsz[bnd]
			}
		}

		// The copy, levelled to the band's gain, plus noise at the share of the
		// band the blend asked for. The noise is scaled by the energy the copy
		// had before the gain, so the two halves are mixed in the proportion
		// the encoder chose whatever the band's level turns out to be.
		bin = d.spxstrtmant
		for bnd := range d.nspxbnd {
			nscale := d.spxnblend[ch][bnd] * d.spxrms[bnd]
			sscale := d.spxsblend[ch][bnd]
			for range d.spxbndsz[bnd] {
				coeffs[bin] *= sscale
				// Off means the caller is comparing this decode against another
				// decoder's, and no two agree here. Leaving the noise out
				// leaves the copied half, which they do agree on, and makes
				// the other decoder's noise the whole of the difference rather
				// than the difference between two unrelated sequences.
				if d.mant.dither {
					coeffs[bin] += nscale * d.mant.nextNoise()
				}
				bin++
			}
		}

		// The channel coded nothing above the extension's start, but it now
		// carries what the extension built, so its coefficients run to the
		// extension's end.
		b.EndMant[ch] = d.spxendmant
	}
}
