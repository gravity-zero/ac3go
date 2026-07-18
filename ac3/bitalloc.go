package ac3

// Bit allocation, clause 6.2.
//
// The encoder never tells the decoder how many bits a mantissa got. Instead
// both sides run the same psychoacoustic model over the exponents, which the
// decoder already has, and arrive at the same answer: the model turns the
// spectral envelope into a masking curve, and every mantissa gets bits in
// proportion to how far it stands above its own mask. The encoder only sends
// the model's parameters, plus an offset it iterated on until the mantissas
// fit the frame.
//
// That makes this routine load bearing in an unusual way. It is not an
// approximation of the encoder's intent, it is a bit-exact replay of it: get
// one bap wrong and every mantissa after it in the block is read from the
// wrong bits. So the arithmetic below stays integral and follows the spec's
// pseudo-code step for step, down to the truncations.

// dbaMaxSegs is the largest number of delta bit allocation segments a channel
// can carry: deltnseg is three bits and counts from zero (clause 4.4.3.51).
const dbaMaxSegs = 8

// Delta bit allocation modes (deltbae and cpldeltbae, clause 4.4.3.49,
// table 6.25).
const (
	DbaReuse    uint8 = iota // keep the previous block's segments
	DbaNew                   // segments follow in the bit stream
	DbaNone                  // no delta bit allocation on this channel
	DbaReserved              // invalid
)

// dba is the delta bit allocation of one channel: a handful of segments, each
// nudging a run of bands of the masking curve up or down in 6 dB steps. It is
// the one way an encoder can overrule the parametric model.
type dba struct {
	mode  uint8
	nseg  int
	offst [dbaMaxSegs]uint8
	len   [dbaMaxSegs]uint8
	ba    [dbaMaxSegs]uint8
}

// allocInfo is everything the model needs for one channel of one block: the
// block's parameters (the baie fields, shared by every channel), the channel's
// own fgain and snroffset, and its bandwidth.
type allocInfo struct {
	fscod uint8

	sdecay int32
	fdecay int32
	sgain  int32
	dbknee int32
	floor  int32

	fgain     int32
	snroffset int32

	start int
	end   int

	// coupling reports whether this is the coupling channel, which enters the
	// leak loop with the levels the encoder sent rather than with levels
	// computed from its own lowest bands: the coupling channel has no lowest
	// bands, it starts partway up the spectrum.
	coupling bool
	fleak    int32
	sleak    int32

	// hebap reports whether this channel's allocation is read out of the
	// adaptive hybrid transform's table instead of the AC-3 one. The model
	// above it is identical - the same masking curve, the same signal to mask
	// ratio - and only the last step, turning that ratio into a quantizer,
	// differs. See hebapTab.
	hebap bool

	d dba
}

// bitAlloc is the scratch of the model. It belongs to a decoder and is reused
// for every channel of every block, so running the model allocates nothing.
type bitAlloc struct {
	psd    [MaxCoefs]int32
	bndpsd [nBands]int32
	excite [nBands]int32
	mask   [nBands]int32
}

// logadd returns the log-domain sum of two log-domain powers (clause 6.2.2.3).
func logadd(a, b int32) int32 {
	c := a - b
	addr := c
	if addr < 0 {
		addr = -addr
	}
	addr = min(addr>>1, 255)
	if c >= 0 {
		return a + latab[addr]
	}
	return b + latab[addr]
}

// calcLowcomp tracks the low frequency compensation (clause 6.2.2.4). It pulls
// the masking curve down over the lowest bands, where the ear resolves
// frequency finely enough that a loud band masks its neighbours far less than
// the leak model would suggest. The exact 256 step it looks for is the spec's
// own: a band exactly 2 dB below the next one.
func calcLowcomp(a, b0, b1 int32, bin int) int32 {
	switch {
	case bin < 7:
		if b0+256 == b1 {
			return 384
		}
		if b0 > b1 {
			return max(0, a-64)
		}
	case bin < 20:
		if b0+256 == b1 {
			return 320
		}
		if b0 > b1 {
			return max(0, a-64)
		}
	default:
		return max(0, a-128)
	}
	return a
}

// integrate maps exponents into the fine grain psd and then integrates it into
// the 50 bands (clauses 6.2.2.2 and 6.2.2.3).
func (a *bitAlloc) integrate(exp *[MaxCoefs]uint8, start, end int) {
	for bin := start; bin < end; bin++ {
		a.psd[bin] = 3072 - int32(exp[bin])<<7
	}

	j := start
	k := int(masktab[start])
	for {
		lastbin := min(bndtab[k]+bndsz[k], end)
		a.bndpsd[k] = a.psd[j]
		for j++; j < lastbin; j++ {
			a.bndpsd[k] = logadd(a.bndpsd[k], a.psd[j])
		}
		k++
		if end <= lastbin || k >= nBands {
			return
		}
	}
}

// excitation computes the excitation function over the bands (clause 6.2.2.4):
// a spreading of each band's power onto the ones above it, as a fast leak and
// a slow leak decaying at different rates, of which the louder wins.
func (a *bitAlloc) excitation(in *allocInfo, bndstrt, bndend int) {
	var fastleak, slowleak, lowcomp int32
	begin := bndstrt

	if !in.coupling {
		// A full bandwidth or LFE channel starts at band 0, so the model can
		// look at the lowest bands and decide where the leak loop should pick
		// up. The bndend == 7 guards are the LFE channel: it has no band above
		// 6, so nothing may read bndpsd past it.
		lowcomp = calcLowcomp(lowcomp, a.bndpsd[0], a.bndpsd[1], 0)
		a.excite[0] = a.bndpsd[0] - in.fgain - lowcomp
		lowcomp = calcLowcomp(lowcomp, a.bndpsd[1], a.bndpsd[2], 1)
		a.excite[1] = a.bndpsd[1] - in.fgain - lowcomp

		begin = 7
		for bin := 2; bin < 7; bin++ {
			if bndend != 7 || bin != 6 {
				lowcomp = calcLowcomp(lowcomp, a.bndpsd[bin], a.bndpsd[bin+1], bin)
			}
			fastleak = a.bndpsd[bin] - in.fgain
			slowleak = a.bndpsd[bin] - in.sgain
			a.excite[bin] = fastleak - lowcomp
			if bndend != 7 || bin != 6 {
				if a.bndpsd[bin] <= a.bndpsd[bin+1] {
					begin = bin + 1
					break
				}
			}
		}

		for bin := begin; bin < min(bndend, 22); bin++ {
			if bndend != 7 || bin != 6 {
				lowcomp = calcLowcomp(lowcomp, a.bndpsd[bin], a.bndpsd[bin+1], bin)
			}
			fastleak = max(fastleak-in.fdecay, a.bndpsd[bin]-in.fgain)
			slowleak = max(slowleak-in.sdecay, a.bndpsd[bin]-in.sgain)
			a.excite[bin] = max(fastleak-lowcomp, slowleak)
		}
		begin = 22
	} else {
		fastleak = in.fleak
		slowleak = in.sleak
	}

	for bin := begin; bin < bndend; bin++ {
		fastleak = max(fastleak-in.fdecay, a.bndpsd[bin]-in.fgain)
		slowleak = max(slowleak-in.sdecay, a.bndpsd[bin]-in.sgain)
		a.excite[bin] = max(fastleak, slowleak)
	}
}

// masking turns the excitation into the masking curve (clause 6.2.2.5).
func (a *bitAlloc) masking(in *allocInfo, bndstrt, bndend int) {
	h := &hth[in.fscod]
	for bin := bndstrt; bin < bndend; bin++ {
		if a.bndpsd[bin] < in.dbknee {
			a.excite[bin] += (in.dbknee - a.bndpsd[bin]) >> 2
		}
		a.mask[bin] = max(a.excite[bin], h[bin])
	}
}

// applyDelta folds the delta bit allocation into the masking curve
// (clause 6.2.2.6).
//
// The band cursor starts at the channel's own first band: the segment offsets
// are relative to where the channel begins, not absolute band numbers. For a
// full bandwidth channel the two readings coincide, since it starts at band 0
// anyway. They part only on the coupling channel, which starts partway up the
// spectrum, and there the reference decoder starts the cursor at the channel's
// first band. Bit exactness with it is the whole point of this decoder, so
// that is what this does.
//
// No real stream has ever exercised this: every channel of every frame in the
// corpus said deltbae = none, so the choice rests on the reference's code and
// not on a stream that discriminates. If one ever does carry a coupling
// channel's delta segments, this is still the first thing to doubt.
func (a *bitAlloc) applyDelta(d *dba, bndstrt int) error {
	if d.mode != DbaReuse && d.mode != DbaNew {
		return nil
	}
	band := bndstrt
	for seg := range d.nseg {
		band += int(d.offst[seg])
		if band >= nBands || int(d.len[seg]) > nBands-band {
			return badDeltaSegment(band, int(d.len[seg]))
		}
		// The 3-bit code covers -4 to +4 steps of 6 dB, skipping zero: the
		// encoder would not spend a segment on a band it does not want moved.
		delta := int32(d.ba[seg]) - 4
		if d.ba[seg] >= 4 {
			delta++
		}
		delta <<= 7
		for range int(d.len[seg]) {
			a.mask[band] += delta
			band++
		}
	}
	return nil
}

// compute runs the whole model for one channel and fills bap over the
// channel's bandwidth. Bins outside it are left alone.
func (a *bitAlloc) compute(in *allocInfo, exp *[MaxCoefs]uint8, bap *[MaxCoefs]uint8) error {
	// These two hold by construction for every field the parser accepts. They
	// are checked anyway because everything below indexes fixed size arrays
	// with numbers derived from the bit stream, and a decoder that panics on a
	// corrupt frame is worse than one that rejects it.
	if in.fscod >= uint8(len(hth)) {
		return reservedError("fscod", uint32(in.fscod))
	}
	if in.start >= in.end || in.end > bndtab[nBands-1]+bndsz[nBands-1] {
		return badBandwidth(in.start, in.end)
	}

	a.integrate(exp, in.start, in.end)
	bndstrt := int(masktab[in.start])
	bndend := int(masktab[in.end-1]) + 1
	a.excitation(in, bndstrt, bndend)
	a.masking(in, bndstrt, bndend)
	if err := a.applyDelta(&in.d, bndstrt); err != nil {
		return err
	}

	// Compute the allocation (clause 6.2.2.7). The masking curve is coarse,
	// one value per band; the psd is fine, one per bin. Their difference,
	// scaled and clamped, is the address of the quantizer to use.
	tab := &baptab
	if in.hebap {
		tab = &hebapTab
	}
	i := in.start
	j := int(masktab[in.start])
	for {
		lastbin := min(bndtab[j]+bndsz[j], in.end)

		m := a.mask[j] - in.snroffset - in.floor
		if m < 0 {
			m = 0
		}
		m = m&0x1fe0 + in.floor

		for ; i < lastbin; i++ {
			address := min(max((a.psd[i]-m)>>5, 0), 63)
			bap[i] = tab[address]
		}
		j++
		if in.end <= lastbin || j >= nBands {
			return nil
		}
	}
}

// snrOffset returns the channel's snroffset (clause 6.2.2.1).
//
// The spec writes this as ((csnroffst - 15) << 4 + fsnroffst) << 2, which in C
// would shift by 4 + fsnroffst; the intent, and what every field is sized for,
// is a coarse offset in 16-step units plus a fine one, all scaled by 4.
func snrOffset(csnroffst, fsnroffst uint8) int32 {
	return ((int32(csnroffst)-15)<<4 + int32(fsnroffst)) << 2
}
