package ac3

// The enhanced AC-3 audio block, clause E.1.3.3.
//
// Same blocks as clause 4.3.3, read differently. What changed is not the audio -
// the exponents, the bit allocation, the mantissas and the transform are the
// ones this package already has - it is where the block gets its instructions
// from. Several fields moved into the audio frame field ahead of the blocks
// (see audfrm_eac3.go), several became optional in a way an AC-3 block's fields
// are not, and one, spectral extension, is new.
//
// The fields an AC-3 block has and this one does not are the reason this is a
// separate walk rather than a handful of conditionals inside the AC-3 one. A
// field that is absent is not a field with a default value: its bits are simply
// not there, so a reader that looks for them consumes the next field's, and
// from that point every block of the frame is nonsense. The two orders are close
// enough to look interchangeable and are not, which is exactly the kind of thing
// that is better stated once, plainly, than woven through the other path.

// decodeEAC3Block reads one enhanced audio block and fills d.blocks[blk].
func (d *Decoder) decodeEAC3Block(blk int) error {
	b := &d.blocks[blk]
	r := &d.r
	f := &d.eac3

	// Block switching and dither, each present only if the frame said the
	// blocks carry them.
	b.Blksw = [MaxFBWChannels]bool{}
	if f.blockSwitchSyntax {
		for ch := range d.nfchans {
			b.Blksw[ch] = r.Bool()
		}
	}
	// A frame that carries no dither flags is not a frame without dither: the
	// channels all dither. It is the opposite of what the zero value says, and
	// it is why this is written out rather than left to the block's zeroing.
	b.Dithflag = [MaxFBWChannels]bool{}
	for ch := range d.nfchans {
		b.Dithflag[ch] = true
	}
	if f.ditherFlagSyntax {
		for ch := range d.nfchans {
			b.Dithflag[ch] = r.Bool()
		}
	}

	// Dynamic range, unchanged from AC-3.
	if r.Bool() { // dynrnge
		d.dynrng[0] = uint8(r.Uint32(8))
	} else if blk == 0 {
		d.dynrng[0] = dynrngNone
	}
	if d.h.Acmod == AcmodDualMono {
		if r.Bool() { // dynrng2e
			d.dynrng[1] = uint8(r.Uint32(8))
		} else if blk == 0 {
			d.dynrng[1] = dynrngNone
		}
	}

	// Spectral extension: the encoder throws the top of the spectrum away and
	// tells the decoder to rebuild it from the bands below. Block 0 always
	// states whether it is in use; a later block either restates it or carries
	// on with what the block before it said.
	if blk == 0 || r.Bool() { // spxstre
		d.spxinu = r.Bool()
		if d.spxinu {
			if err := d.readSpxStrategy(blk); err != nil {
				return err
			}
		}
	}
	if !d.spxinu {
		// Not in use is not "nothing to do": the channel list and the gains
		// have to be put back where a block that turns the extension on again
		// expects to find them, which is nobody extending and every channel
		// about to state its gains outright.
		d.chinspx = [MaxFBWChannels]bool{}
		for ch := range d.nfchans {
			f.firstSpxCoords[ch] = true
		}
	} else if err := d.readSpxCoords(); err != nil {
		return err
	}

	// The coupling strategy, which this syntax decided a frame at a time: the
	// block does not say whether it is here, the audio frame field did.
	if f.cplStrategyExists[blk] {
		if err := d.readEAC3CouplingStrategy(blk); err != nil {
			return err
		}
	}
	d.cplinu = f.cplInUse[blk]
	if err := d.readEAC3CouplingCoords(blk); err != nil {
		return err
	}

	// Rematrixing. Block 0 always carries it here, where an AC-3 block 0 merely
	// ought to.
	if d.h.Acmod == AcmodStereo {
		if blk == 0 || r.Bool() { // rematstr
			d.readRematrixingFlags()
		}
	}

	// Exponents: the strategies are the frame's, the rest is the block's.
	var chexpstr [MaxFBWChannels]uint8
	for ch := range d.nfchans {
		chexpstr[ch] = f.expStrategy[blk][ch]
	}
	var lfeexpstr uint8
	if d.h.Lfeon {
		lfeexpstr = f.expStrategy[blk][d.lfeCh]
	}
	var cplexpstr uint8
	if d.cplinu {
		cplexpstr = f.expStrategy[blk][MaxChannels]
		if cplexpstr == ExpReuse && !d.cplinuPrev {
			return missingInBlockZero("cplexpstr")
		}
	}
	d.cplinuPrev = d.cplinu
	if err := d.readExponentsWith(cplexpstr, lfeexpstr, chexpstr); err != nil {
		return err
	}

	if err := d.readEAC3BitAllocInfo(blk); err != nil {
		return err
	}

	// Dummy data, present only if the frame said any block might carry some.
	if f.skipSyntax && r.Bool() { // skiple
		r.Skip(int(r.Uint32(9)) * 8)
	}
	if r.Err() != nil {
		return wrap(ErrShortFrame)
	}

	// The bit allocation has to come before the mantissas here for the reason
	// it does in AC-3 - the baps say how wide each mantissa is - and for one
	// more: a channel using the adaptive hybrid transform is allocated out of
	// a different table, and its baps are what decide which of its bins carry
	// a GAQ gain code. See aht.go.
	if err := d.computeBitAlloc(); err != nil {
		return err
	}
	if err := d.readMantissas(b, blk); err != nil {
		return err
	}
	d.decouple(b)
	d.rematrix(b)
	// After the channels are whole again and before the transform. It has to
	// follow decoupling, because a channel that couples has nothing of its own
	// in the bins the extension copies from until the coupling channel is
	// spread back out; and it has to follow rematrixing, because the sum and
	// difference the encoder rematrixed were of the coded bands, and the
	// extension is not one of them.
	d.applySpx(b)
	d.synthesize(b, blk)
	return nil
}

// readEAC3CouplingStrategy reads the coupling strategy of a block the frame said
// carries one.
//
// The differences from AC-3 are all bit positions rather than meanings, and
// there are four of them. The strategy opens with a flag for enhanced coupling,
// which this decoder does not read past. A stereo frame states no channel list:
// with two channels and a coupling channel, the two are what is coupled, and a
// list could only say the one thing. The band structure is optional, defaulting
// to a table rather than to nothing. And a block that does not couple resets
// what the next one that does will need.
func (d *Decoder) readEAC3CouplingStrategy(blk int) error {
	r := &d.r
	f := &d.eac3

	if !f.cplInUse[blk] {
		d.chincpl = [MaxFBWChannels]bool{}
		for ch := range d.nfchans {
			f.firstCplCoords[ch] = true
		}
		f.firstCplLeak = true
		d.phsflginu = false
		return nil
	}

	if d.h.Acmod < AcmodStereo {
		return couplingNotAllowed(d.h.Acmod)
	}
	if r.Bool() { // ecplinu
		return errEAC3EnhancedCoupling
	}

	if d.h.Acmod == AcmodStereo {
		// Two channels and a coupling channel: the two are the two.
		d.chincpl[0], d.chincpl[1] = true, true
		d.phsflginu = r.Bool()
	} else {
		for ch := range d.nfchans {
			d.chincpl[ch] = r.Bool()
		}
	}

	d.cplbegf = uint8(r.Uint32(4))

	// Where the coupling channel stops. A block that extends its spectrum does
	// not say: the two techniques divide the spectrum between them, so coupling
	// necessarily ends where the extension's source begins, and the syntax
	// spends no bits restating it. Reading the four bits anyway - which is what
	// the AC-3 syntax at this position does - puts every field of every block
	// after this one four bits out, on the frames that use both.
	//
	// The end is kept as a sub-band rather than as cplendf because the two
	// disagree about what is representable: cplendf counts from three
	// sub-bands, and an extension whose source starts low leaves coupling a
	// range the code cannot express. The spec says as much - the value of
	// cplendf may be negative - and the arithmetic below is the same
	// arithmetic in a form that cannot underflow.
	cplEndSubbnd := 0
	if d.spxinu {
		cplEndSubbnd = (d.spxstrtmant - cplBandBase) / cplSubbandBins
	} else {
		d.cplendf = uint8(r.Uint32(4))
		cplEndSubbnd = int(d.cplendf) + 3
	}
	if r.Err() != nil {
		return wrap(ErrShortFrame)
	}
	if int(d.cplbegf) >= cplEndSubbnd {
		return badCouplingRange(int(d.cplbegf), cplEndSubbnd-1)
	}
	d.ncplsubnd = cplEndSubbnd - int(d.cplbegf)
	d.cplstrtmant = cplbegfStrtMant(d.cplbegf)
	d.cplendmant = cplEndSubbnd*cplSubbandBins + cplBandBase

	// The band structure. A block 0 starts from the default table; a block
	// that states none keeps what is there, which for a frame that only
	// couples from mid-frame on is the previous frame's structure - the state
	// crosses frames, see Decoder.cplBandStruct. AC-3 has no default and
	// states the whole thing every time.
	//
	// This syntax numbers the structure by sub-band of the spectrum, so the
	// default table can be one table whatever range a frame couples over. The
	// rest of this package numbers it from the coupling channel's first
	// sub-band, which is what the decoupling reads. The two are kept apart
	// here: cplBandStruct is the frame's, in the spec's numbering, and it is
	// projected onto the decoder's at the end.
	if blk == 0 {
		d.cplBandStruct = eac3DefaultCplBandStruct
	}
	if r.Bool() { // cplbndstrce
		for bnd := 1; bnd < d.ncplsubnd; bnd++ {
			d.cplBandStruct[int(d.cplbegf)+bnd] = r.Bool()
		}
	}
	d.cplbndstrc = [maxCplSubbands]bool{}
	d.ncplbnd = d.ncplsubnd
	for bnd := 1; bnd < d.ncplsubnd; bnd++ {
		d.cplbndstrc[bnd] = d.cplBandStruct[int(d.cplbegf)+bnd]
		if d.cplbndstrc[bnd] {
			d.ncplbnd--
		}
	}
	return nil
}

// readEAC3CouplingCoords reads the coupling gains.
//
// The one difference: the first block in which a channel couples carries its
// gains with no flag to say so. There is nothing for that block to inherit, so
// a bit asking "are the gains here?" could only ever answer yes, and the syntax
// spends it on nothing.
func (d *Decoder) readEAC3CouplingCoords(blk int) error {
	r := &d.r
	if !d.cplinu {
		return nil
	}
	for ch := range d.nfchans {
		if !d.chincpl[ch] {
			d.cplcoe[ch] = false
			// A channel that has dropped out of coupling has nothing to hand
			// the block that couples it again, so that block will state its
			// gains outright and carry no flag saying so. Leaving this unset
			// costs a bit exactly there - a channel that leaves coupling and
			// comes back inside one frame - and every field after it, which
			// is a rare enough shape that only the extension's arrival brought
			// streams that do it into reach.
			d.eac3.firstCplCoords[ch] = true
			continue
		}
		first := d.eac3.firstCplCoords[ch]
		d.cplcoe[ch] = first || r.Bool()
		if !d.cplcoe[ch] {
			if blk == 0 {
				return missingInBlockZero("cplcoe")
			}
			continue
		}
		d.eac3.firstCplCoords[ch] = false
		d.mstrcplco[ch] = uint8(r.Uint32(2))
		for bnd := range d.ncplbnd {
			d.cplcoexp[ch][bnd] = uint8(r.Uint32(4))
			d.cplcomant[ch][bnd] = uint8(r.Uint32(4))
			d.cplco[ch][bnd] = couplingCoord(d.cplcoexp[ch][bnd], d.cplcomant[ch][bnd], d.mstrcplco[ch])
		}
	}
	if d.h.Acmod == AcmodStereo && d.phsflginu && (d.cplcoe[0] || d.cplcoe[1]) {
		for bnd := range d.ncplbnd {
			d.phsflg[bnd] = r.Bool()
		}
	}
	if r.Err() != nil {
		return wrap(ErrShortFrame)
	}
	return nil
}

// readEAC3BitAllocInfo reads what is left of the block's side information.
//
// Every field here is gated by a flag the frame set, and the SNR offsets are
// gated twice over: by the frame's strategy and by being in block 0. A frame
// that states its offsets once, which every real stream does, carries none of
// this in any block.
func (d *Decoder) readEAC3BitAllocInfo(blk int) error {
	r := &d.r
	f := &d.eac3

	if f.bitAllocationSyntax {
		if r.Bool() { // baie
			d.sdcycod = uint8(r.Uint32(2))
			d.fdcycod = uint8(r.Uint32(2))
			d.sgaincod = uint8(r.Uint32(2))
			d.dbpbcod = uint8(r.Uint32(2))
			d.floorcod = uint8(r.Uint32(3))
		} else if blk == 0 {
			return missingInBlockZero("baie")
		}
	}

	// SNR offsets. Strategy 0 means the frame stated one for everything and no
	// block restates it; otherwise block 0 may, and only block 0.
	if blk == 0 && f.snrOffsetStrategy != 0 {
		if r.Bool() { // snroffste
			d.csnroffst = uint8(r.Uint32(6))
			// Strategy 1 gives one fine offset to every channel; strategy 2
			// gives each its own. The coupling channel comes first when it is
			// in use, and it is the one that sets the shared value.
			fine := uint8(r.Uint32(4))
			if d.cplinu {
				d.cplfsnroffst = fine
				if f.snrOffsetStrategy == 2 {
					fine = uint8(r.Uint32(4))
				}
			}
			for ch := range d.nfchans {
				d.fsnroffst[ch] = fine
				if f.snrOffsetStrategy == 2 && !(ch == 0 && !d.cplinu) {
					d.fsnroffst[ch] = uint8(r.Uint32(4))
				}
			}
			if d.h.Lfeon {
				d.lfefsnroffst = fine
				if f.snrOffsetStrategy == 2 {
					d.lfefsnroffst = uint8(r.Uint32(4))
				}
			}
		}
	}

	// Fast gain has a syntax of its own here, and a default rather than an
	// inheritance: a frame that carries none leaves every channel at the middle
	// of the table.
	if f.fastGainSyntax && r.Bool() { // fgaincode
		if d.cplinu {
			d.cplfgaincod = uint8(r.Uint32(3))
		}
		for ch := range d.nfchans {
			d.fgaincod[ch] = uint8(r.Uint32(3))
		}
		if d.h.Lfeon {
			d.lfefgaincod = uint8(r.Uint32(3))
		}
	} else if blk == 0 {
		d.cplfgaincod = defaultFgaincod
		for ch := range d.nfchans {
			d.fgaincod[ch] = defaultFgaincod
		}
		d.lfefgaincod = defaultFgaincod
	}

	// The SNR offset an AC-3 converter should use. Nothing here needs it.
	if d.h.Sync.Strmtyp == StrmtypIndependent && r.Bool() { // convsnroffste
		r.Skip(10)
	}

	if d.cplinu {
		if f.firstCplLeak || r.Bool() { // cplleake
			d.cplfleak = uint8(r.Uint32(3))
			d.cplsleak = uint8(r.Uint32(3))
		}
		f.firstCplLeak = false
	}

	if f.dbaSyntax && r.Bool() { // deltbaie
		if d.cplinu {
			d.dbaCpl.mode = uint8(r.Uint32(2))
			if d.dbaCpl.mode == DbaReserved {
				return reservedError("cpldeltbae", uint32(d.dbaCpl.mode))
			}
		}
		for ch := range d.nfchans {
			d.dbaCh[ch].mode = uint8(r.Uint32(2))
			if d.dbaCh[ch].mode == DbaReserved {
				return reservedError("deltbae", uint32(d.dbaCh[ch].mode))
			}
		}
		if d.cplinu && d.dbaCpl.mode == DbaNew {
			d.readDbaSegments(&d.dbaCpl)
		}
		for ch := range d.nfchans {
			if d.dbaCh[ch].mode == DbaNew {
				d.readDbaSegments(&d.dbaCh[ch])
			}
		}
	} else if blk == 0 {
		d.dbaCpl.mode = DbaNone
		for ch := range d.dbaCh {
			d.dbaCh[ch].mode = DbaNone
		}
	}
	return r.Err()
}
