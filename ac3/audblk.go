package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// Audio block decoding, clause 4.3.3 for the syntax and clauses 6.1 to 6.3 for
// what it means.
//
// A frame's six audio blocks are not independent. Almost every field can be
// left out and inherited from the block before it: exponents, the coupling
// strategy, the bit allocation parameters, the delta bit allocation. That is
// where the format's efficiency comes from, and it is why a block cannot be
// decoded on its own, only in order from block 0, which is required to carry a
// complete set.

const (
	// MaxFBWChannels is the largest number of full bandwidth channels an audio
	// coding mode codes: the 3/2 mode's five.
	MaxFBWChannels = 5

	// MaxChannels counts the LFE channel too.
	MaxChannels = MaxFBWChannels + 1

	// maxCplSubbands is the number of 12-bin sub-bands the coupling channel is
	// carved out of (clause 4.4.3.13). Coupling bands group whole sub-bands,
	// so there are never more bands than sub-bands.
	maxCplSubbands = 18

	// blockTrailerBits is the smallest tail a frame can have past its last
	// audio block: auxdatae, crcrsv and crc2 (clauses 4.3.4 and 4.3.5).
	// Anything else between the two is auxiliary data.
	blockTrailerBits = 1 + 1 + 16
)

// Block is one decoded audio block: the transform coefficients of every coded
// channel, and the side information a caller needs to turn them into samples.
//
// A Block belongs to the Decoder that produced it and is overwritten by the
// next frame.
type Block struct {
	// Blksw reports which full bandwidth channels coded this block as two
	// short transforms rather than one long one.
	Blksw [MaxFBWChannels]bool

	// Dithflag reports which full bandwidth channels asked for their
	// unallocated mantissas to be filled with noise.
	Dithflag [MaxFBWChannels]bool

	// Cplinu reports whether a coupling channel stands in for the top of the
	// channels Chincpl names. Coeffs holds nothing above CplStrtMant for
	// those channels: their high frequencies live in Cpl until they are
	// spread back out.
	Cplinu      bool
	Chincpl     [MaxFBWChannels]bool
	CplStrtMant int
	CplEndMant  int

	// EndMant[ch] is the first bin channel ch does not code. Coeffs is zero
	// from there up.
	EndMant [MaxChannels]int

	// Coeffs holds one channel's transform coefficients per row, in the order
	// Header.Layout gives: the full bandwidth channels of the audio coding
	// mode, then the LFE channel when the frame carries one.
	Coeffs [MaxChannels][MaxCoefs]float32

	// Cpl holds the coupling channel's own coefficients over CplStrtMant to
	// CplEndMant, before they are spread back into the coupled channels.
	Cpl [MaxCoefs]float32
}

// Decoder decodes AC-3 syncframes into transform coefficients.
//
// A Decoder holds every buffer a frame needs, so decoding allocates nothing
// after the first frame; reuse one across a stream. Decoding a frame depends
// on nothing but that frame's bytes, the noise of unallocated mantissas
// included, so the same frame always decodes to the same coefficients.
//
// A Decoder is not safe for concurrent use.
type Decoder struct {
	h Header
	r bitstream.Reader

	blocks [BlocksPerFrame]Block

	alloc bitAlloc
	mant  mantissaReader

	// Exponents and bit allocation pointers, kept per channel because a block
	// may reuse the ones before it. Index MaxChannels is the coupling channel.
	exp [MaxChannels + 1][MaxCoefs]uint8
	bap [MaxChannels + 1][MaxCoefs]uint8

	chbwcod [MaxFBWChannels]uint8
	endmant [MaxChannels]int

	cplinu      bool
	cplinuPrev  bool
	chincpl     [MaxFBWChannels]bool
	phsflginu   bool
	cplbegf     uint8
	cplendf     uint8
	cplstrtmant int
	cplendmant  int
	ncplsubnd   int
	ncplbnd     int
	cplbndstrc  [maxCplSubbands]bool

	cplcoe    [MaxFBWChannels]bool
	mstrcplco [MaxFBWChannels]uint8
	cplcoexp  [MaxFBWChannels][maxCplSubbands]uint8
	cplcomant [MaxFBWChannels][maxCplSubbands]uint8
	cplco     [MaxFBWChannels][maxCplSubbands]float32
	phsflg    [maxCplSubbands]bool

	rematflg  [4]bool
	nrematbnd int

	sdcycod   uint8
	fdcycod   uint8
	sgaincod  uint8
	dbpbcod   uint8
	floorcod  uint8
	csnroffst uint8

	fsnroffst    [MaxFBWChannels]uint8
	fgaincod     [MaxFBWChannels]uint8
	cplfsnroffst uint8
	cplfgaincod  uint8
	lfefsnroffst uint8
	lfefgaincod  uint8
	cplfleak     uint8
	cplsleak     uint8

	dbaCh  [MaxFBWChannels]dba
	dbaCpl dba

	gainrng [MaxFBWChannels]uint8
	dynrng  [2]uint8

	// Dialogue normalization. targetLevel is what the caller asked for, zero
	// when it asked for nothing; levelGains holds the resulting gain per
	// programme, and levelGainsFor the header fields it was computed from, so
	// that a stream whose dialogue level never moves recomputes it once.
	targetLevel   int
	levelGains    [2]float32
	levelGainsFor uint16

	// Downmixing. dmixChannels is what the caller asked for, zero when it
	// asked for nothing; dmix holds the mixed planes, and dmixFor the header
	// fields the coefficients were computed from.
	dmixChannels int
	dmixCoeffs   downmixCoeffs
	dmixFor      uint16
	dmix         [2][BlocksPerFrame * windowLen]float32

	// What the enhanced syntax's audio frame field said. Empty for AC-3, which
	// has no such field and decides all of it per block.
	eac3 eac3Frame

	// The coupling and spectral extension band structures, in the spec's
	// numbering - by sub-band of the spectrum rather than from the channel's
	// own start. These are the one piece of enhanced syntax state that crosses
	// frames: a block that states none keeps what is there, and what is there
	// is only refreshed to the default table by a block 0 that reads the
	// strategy. A frame that brings coupling or the extension up mid-frame
	// without stating a structure therefore inherits the previous frame's,
	// which is what the reference decoder does and what real streams encode
	// against; such a frame does not decode on its own, by construction.
	cplBandStruct [maxCplSubbands]bool
	spxBandStruct [spxMaxBands]bool

	// The adaptive hybrid transform's scratch. preMantissa is the one buffer
	// in this decoder that has to hold a whole frame rather than a block: an
	// AHT channel's six blocks are read at once, out of block 0, and the five
	// after it are served from here. gaqGain holds one channel's gain codes,
	// which are read for every bin before any mantissa is. Both are sized for
	// the worst case and live here so that a frame allocates nothing.
	//
	// gaqGain is two longer than there are bins. The 1.67 bit codes come three
	// to a group, so the last group of a channel whose qualifying bins are not
	// a multiple of three writes up to two gains past the last of them.
	preMantissa [MaxChannels + 1][MaxCoefs][BlocksPerFrame]int32
	gaqGain     [MaxCoefs + 2]int32

	// Spectral extension. spxinu carries from block to block, the way the
	// coupling strategy does; everything below it is the strategy's, and is
	// restated by any block that restates the strategy.
	spxinu         bool
	chinspx        [MaxFBWChannels]bool
	spxdststrtmant int // first bin of the source the extension is copied from
	spxstrtmant    int // first bin the extension fills, and the source's end
	spxendmant     int // bin one past the last the extension fills
	nspxbnd        int
	spxbndsz       [spxMaxBands]int
	spxnblend      [MaxFBWChannels][spxMaxBands]float32
	spxsblend      [MaxFBWChannels][spxMaxBands]float32

	// Scratch the extension rebuilds a block with: where the copy restarts,
	// how long each of its sections is, and the energy of each band. None of
	// it outlives the block; it lives here so that a frame allocates nothing.
	spxwrap     [spxMaxBands]bool
	spxCopySize [spxMaxCopySections]int
	nspxCopy    int
	spxrms      [spxMaxBands]float32

	nfchans int
	lfeCh   int // index of the LFE in exp, bap and Block.Coeffs, or -1

	blockEndBit int

	// Dependent substream state. An E-AC-3 7.1 programme is an independent
	// substream carrying 5.1 followed by a dependent substream carrying the
	// extra channels; dep decodes that second substream (it has its own filter
	// bank state, so it is a whole Decoder), output71 says the last access unit
	// was one, and auSize is how many bytes it spanned - both substreams - so a
	// caller advances past the pair. See eac3_71.go.
	dep      *Decoder
	depHdr   Header // the dependent substream's header, reused so a merge allocates nothing
	output71 bool
	auSize   int

	// The filter bank and its state. delay is the tail of the block before,
	// waiting to be overlapped with the next one, and it is the one thing that
	// crosses a frame boundary: see Samples.
	long   *imdct
	short  *imdct
	tmp    [longMants]float32
	scaled [longMants]float32
	half   [shortMants]float32
	delay  [MaxChannels][windowLen / 2]float32
	pcm    [MaxChannels][BlocksPerFrame * windowLen]float32
}

// NewDecoder returns a Decoder ready to decode a stream.
func NewDecoder() *Decoder {
	d := &Decoder{
		long:  newIMDCT(longMants),
		short: newIMDCT(shortMants),
	}
	d.mant.dither = true
	return d
}

// Reset drops the filter bank's carry over, so the next frame decodes as the
// first of a stream.
//
// Call it when starting somewhere other than where the last frame left off: at
// the head of a stream, or after a seek. Decoding a segment without it, from a
// decoder that has just been handed the segment's first frame, leaves that
// frame's first 256 samples per channel missing the half of the signal that
// lived in the frame before it.
func (d *Decoder) Reset() {
	d.delay = [MaxChannels][windowLen / 2]float32{}
	// The enhanced band structures cross frames the way the filter bank's
	// carry over does, so a seek drops them too. A frame that needed them -
	// one that brings coupling or the extension up mid-frame with no structure
	// of its own - then fares no better than under the reference decoder
	// started cold, which holds the same zeroed state and typically refuses
	// the frame a few fields later.
	d.cplBandStruct = [maxCplSubbands]bool{}
	d.spxBandStruct = [spxMaxBands]bool{}
}

// Samples returns the finished PCM of channel ch of the frame last decoded:
// BlocksPerFrame*256 = 1536 samples, one frame's worth, in the order
// Header.Layout gives. It stays valid until the next call to DecodeFrame.
//
// Unlike the coefficients, these depend on the frame before this one. The
// transform overlaps its blocks by half a block, so a frame's first samples
// are finished by adding them to the tail of the previous frame's last block:
// a frame decoded cold is short that tail and its first 256 samples per
// channel are wrong. That is the format's one piece of state between frames,
// and decoding a segment means handing the decoder the frame before it first
// and throwing that frame's samples away.
// When a downmix was asked for, the planes are the mixed ones and ch indexes
// OutputLayout rather than the layout the stream codes.
func (d *Decoder) Samples(ch int) []float32 {
	if d.output71 {
		return d.samples71(ch)
	}
	if d.downmixing() {
		return d.dmix[ch][:]
	}
	return d.pcm[ch][:]
}

// AccessUnitSize is how many bytes the last decoded frame spanned. It is the
// syncframe size for an ordinary frame, and the independent plus the dependent
// substream together for a 7.1 access unit, so a caller advancing by it lands
// on the next frame rather than in the middle of a dependent substream.
func (d *Decoder) AccessUnitSize() int { return d.auSize }

// SetDither turns the noise that fills unallocated mantissas on or off. It is
// on by default, which is what the format asks for.
//
// The spec calls for "any reasonably random sequence" here, so no two decoders
// agree on these values and no comparison against another implementation can
// hold over the bands they fill. Turning the noise off replaces it with
// silence, which is a thing two decoders can agree on.
func (d *Decoder) SetDither(on bool) { d.mant.dither = on }

// Header returns the header of the frame last decoded. It stays valid until
// the next call to DecodeFrame.
func (d *Decoder) Header() *Header { return &d.h }

// Block returns block i of the frame last decoded, 0 to BlocksPerFrame-1. The
// Block belongs to the decoder and stays valid until the next call to
// DecodeFrame.
func (d *Decoder) Block(i int) *Block { return &d.blocks[i] }

// BlockEndBit returns the bit offset within the frame just past its last audio
// block. What follows is the frame's auxiliary data and its check word, so
// this is the point where the audio ends and the encoder's slack begins.
func (d *Decoder) BlockEndBit() int { return d.blockEndBit }

// DecodeFrame decodes the six audio blocks of one syncframe into transform
// coefficients. frame must hold a whole syncframe; extra bytes are ignored.
//
// It does not verify the check words: call CheckCRC for that.
func (d *Decoder) DecodeFrame(frame []byte) error {
	if err := ParseHeader(frame, &d.h); err != nil {
		return err
	}
	full := frame
	if len(frame) > d.h.Sync.FrameSize {
		frame = frame[:d.h.Sync.FrameSize]
	}
	d.output71 = false
	d.auSize = d.h.Sync.FrameSize

	d.reset()
	d.updateLevelGains()
	d.updateDownmix()
	d.r.Reset(frame)
	d.r.SeekBit(d.h.AudioStartBit)

	if isEAC3(d.h.Sync.Bsid) {
		// Only the independent substream carries the programme. The dependent
		// ones extend it with channels this decoder does not reach for, and a
		// caller handed one has been handed something it cannot use rather than
		// something it can ignore.
		if d.h.Sync.Substreamid != 0 {
			return unsupportedSubstream(d.h.Sync.Substreamid)
		}
		if d.h.Sync.HasFscod2 {
			return unsupportedReducedRate(d.h.Sync.SampleRate)
		}
		if err := d.parseEAC3AudioFrame(&d.r); err != nil {
			return err
		}
		for blk := range d.h.Sync.NumBlocks {
			if err := d.decodeEAC3Block(blk); err != nil {
				return blockError(blk, err)
			}
		}
		d.blockEndBit = d.r.BitPos()
		if avail := len(frame)*8 - blockTrailerBits; d.blockEndBit > avail {
			return frameOverrun(d.blockEndBit, avail)
		}
		return d.decodeDependent71(full)
	}

	for blk := range BlocksPerFrame {
		if err := d.decodeBlock(blk); err != nil {
			return blockError(blk, err)
		}
	}
	d.blockEndBit = d.r.BitPos()

	// The samples are all there now, which is what the downmix needs: it mixes
	// finished samples rather than coefficients, so it runs once per frame
	// rather than once per block.
	if d.downmixing() {
		d.downmix()
	}

	// The audio has to leave room for what the frame ends with. This is the
	// one check that the audio blocks were read from the right bit: they are
	// self-delimiting from there on, so a decode that starts one bit out
	// walks off the end of the frame or lands nowhere near it.
	if avail := len(frame)*8 - blockTrailerBits; d.blockEndBit > avail {
		return frameOverrun(d.blockEndBit, avail)
	}
	// An AC-3 frame can be the 5.1 core of a 7.1 programme, with an enhanced
	// dependent substream adding the side and back channels. This is the same
	// merge as for an enhanced core; only the core's own syntax differs.
	return d.decodeDependent71(full)
}

// reset clears the state the blocks of a frame inherit from each other, so
// that a frame never sees anything of the frame before it.
func (d *Decoder) reset() {
	d.nfchans = d.h.FullBandwidthChannels()
	d.lfeCh = -1
	if d.h.Lfeon {
		d.lfeCh = d.nfchans
	}

	d.cplinu = false
	d.cplinuPrev = false
	d.chincpl = [MaxFBWChannels]bool{}
	d.cplcoe = [MaxFBWChannels]bool{}
	// The coupling gains and phase flags carry from block to block within a
	// frame, so they have to be dropped between frames: a frame that inherited
	// them would not decode to the same thing on its own, and this decoder
	// promises that it does.
	d.cplco = [MaxFBWChannels][maxCplSubbands]float32{}
	d.phsflg = [maxCplSubbands]bool{}
	d.phsflginu = false
	d.spxinu = false
	d.chinspx = [MaxFBWChannels]bool{}
	d.rematflg = [4]bool{}
	d.nrematbnd = 0
	d.dbaCpl = dba{mode: DbaNone}
	for ch := range d.dbaCh {
		d.dbaCh[ch] = dba{mode: DbaNone}
	}
	d.endmant = [MaxChannels]int{}
	d.chbwcod = [MaxFBWChannels]uint8{}
	d.mant.resetFrame()
}

// decodeBlock reads one audblk and fills d.blocks[blk].
func (d *Decoder) decodeBlock(blk int) error {
	b := &d.blocks[blk]
	r := &d.r

	b.Blksw = [MaxFBWChannels]bool{}
	b.Dithflag = [MaxFBWChannels]bool{}
	for ch := range d.nfchans {
		b.Blksw[ch] = r.Bool()
	}
	for ch := range d.nfchans {
		b.Dithflag[ch] = r.Bool()
	}

	// A block that sends no gain keeps the one before it. Block 0 has nothing
	// to keep, so a gain it does not send is no gain at all.
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

	if err := d.readCouplingStrategy(blk); err != nil {
		return err
	}
	if err := d.readCouplingCoords(blk); err != nil {
		return err
	}
	d.readRematrixing()
	if err := d.readExponents(blk); err != nil {
		return err
	}
	if err := d.readBitAllocInfo(blk); err != nil {
		return err
	}
	if r.Bool() { // skiple
		r.Skip(int(r.Uint32(9)) * 8)
	}
	if err := r.Err(); err != nil {
		return wrap(ErrShortFrame)
	}

	if err := d.computeBitAlloc(); err != nil {
		return err
	}
	if err := d.readMantissas(b, blk); err != nil {
		return err
	}
	d.decouple(b)
	d.rematrix(b)
	d.synthesize(b, blk)
	return nil
}

// readCouplingStrategy reads the cplstre block (clause 4.4.3.7 onwards). When
// it is absent the whole coupling arrangement carries over unchanged from the
// block before.
func (d *Decoder) readCouplingStrategy(blk int) error {
	r := &d.r

	cplstre := r.Bool()
	if blk == 0 && !cplstre {
		return missingInBlockZero("cplstre")
	}
	if !cplstre {
		return nil
	}

	d.cplinu = r.Bool()
	if !d.cplinu {
		d.chincpl = [MaxFBWChannels]bool{}
		return nil
	}

	for ch := range d.nfchans {
		d.chincpl[ch] = r.Bool()
	}
	if d.h.Acmod == AcmodStereo {
		d.phsflginu = r.Bool()
	}
	d.cplbegf = uint8(r.Uint32(4))
	d.cplendf = uint8(r.Uint32(4))
	if r.Err() != nil {
		return wrap(ErrShortFrame)
	}
	// Coupling has to span at least one sub-band, and the sub-bands it spans
	// have to exist: the format defines 18 of them, ending at bin 253.
	if int(d.cplbegf) > int(d.cplendf)+2 {
		return badCouplingRange(int(d.cplbegf), int(d.cplendf)+2)
	}
	d.ncplsubnd = 3 + int(d.cplendf) - int(d.cplbegf)
	d.cplstrtmant = cplbegfStrtMant(d.cplbegf)
	d.cplendmant = cplendfEndMant(d.cplendf)

	d.ncplbnd = d.ncplsubnd
	d.cplbndstrc[0] = false
	for bnd := 1; bnd < d.ncplsubnd; bnd++ {
		d.cplbndstrc[bnd] = r.Bool()
		if d.cplbndstrc[bnd] {
			d.ncplbnd--
		}
	}
	return nil
}

// readCouplingCoords reads the per channel coupling coordinates and the phase
// flags (clauses 4.4.3.14 to 4.4.3.19), and turns the coordinates into the
// gains decouple applies.
//
// A channel that sends no coordinates keeps the ones it had, which is why they
// are decoded here and kept rather than decoded where they are used.
func (d *Decoder) readCouplingCoords(blk int) error {
	r := &d.r
	if !d.cplinu {
		return nil
	}
	for ch := range d.nfchans {
		if !d.chincpl[ch] {
			d.cplcoe[ch] = false
			continue
		}
		d.cplcoe[ch] = r.Bool()
		if !d.cplcoe[ch] {
			// Block 0 has nothing to inherit: a channel that coupled there
			// without saying at what gain would be decoded against whatever
			// the block before it left behind, and there is no block before it.
			if blk == 0 {
				return missingInBlockZero("cplcoe")
			}
			continue
		}
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

// readRematrixing reads the rematrix flags (clause 4.4.3.20). How many there
// are depends on how much of the spectrum the coupling channel has taken over:
// the bands above it are already shared, so there is nothing left to rematrix.
func (d *Decoder) readRematrixing() {
	if d.h.Acmod != AcmodStereo {
		return
	}
	if !d.r.Bool() { // rematstr
		return
	}
	d.readRematrixingFlags()
}

// readRematrixingFlags reads the flags themselves, given that the block states
// them. The two syntaxes differ only in when they are stated - an enhanced
// block 0 always does, where an AC-3 block only may - so the band count and the
// flags are shared.
func (d *Decoder) readRematrixingFlags() {
	r := &d.r
	// A band the two channels no longer code for themselves carries no flag:
	// there is nothing left there to have been summed and differenced. Coupling
	// answers this first because it starts lower than any extension does, so
	// where a block does both it is coupling that decides how far the flags
	// reach.
	switch {
	case d.cplinu && d.cplbegf == 0:
		d.nrematbnd = 2
	case d.cplinu && d.cplbegf <= 2:
		d.nrematbnd = 3
	case !d.cplinu && d.spxinu && d.spxstrtmant <= 61:
		d.nrematbnd = 3
	default:
		d.nrematbnd = 4
	}
	for rbnd := range d.nrematbnd {
		d.rematflg[rbnd] = r.Bool()
	}
}

// readExponents reads the exponent strategies, the channel bandwidths and the
// exponent sets themselves (clause 6.1.3).
func (d *Decoder) readExponents(blk int) error {
	r := &d.r

	var cplexpstr, lfeexpstr uint8
	var chexpstr [MaxFBWChannels]uint8
	if d.cplinu {
		cplexpstr = uint8(r.Uint32(2))
	}
	for ch := range d.nfchans {
		chexpstr[ch] = uint8(r.Uint32(2))
	}
	if d.h.Lfeon {
		// One bit only: the LFE has seven exponents, so the two coarse
		// strategies would save nothing (table 6.5).
		if r.Bool() {
			lfeexpstr = ExpD15
		}
	}
	if err := r.Err(); err != nil {
		return wrap(ErrShortFrame)
	}
	if blk == 0 {
		for ch := range d.nfchans {
			if chexpstr[ch] == ExpReuse {
				return missingInBlockZero("chexpstr")
			}
		}
		if d.h.Lfeon && lfeexpstr == ExpReuse {
			return missingInBlockZero("lfeexpstr")
		}
	}
	return d.readExponentsWith(cplexpstr, lfeexpstr, chexpstr)
}

// readExponentsWith reads the bandwidths and exponents, given the strategies.
//
// It is split from readExponents because the two syntaxes differ in where the
// strategies come from and in nothing else: AC-3 states them here, at the top
// of the block, and enhanced AC-3 states them once for the whole frame in the
// audio frame field. Everything from the bandwidths on - which channel codes
// how far, the leading absolute exponent, the differential chain, the order the
// channels come in - is the same bit stream in both.
func (d *Decoder) readExponentsWith(cplexpstr, lfeexpstr uint8, chexpstr [MaxFBWChannels]uint8) error {
	r := &d.r
	// A coupling channel that has just come into use has nothing to reuse:
	// there is no previous block of it. The spec forbids it, and this decoder
	// depends on it, since it is what guarantees the coupling exponents were
	// written by this frame.
	if d.cplinu && cplexpstr == ExpReuse && !d.cplinuPrev {
		return missingInBlockZero("cplexpstr")
	}
	d.cplinuPrev = d.cplinu

	// Channel bandwidths. A channel that gives its top away states none: a
	// coupled channel stops at the coupling channel's start and an extended one
	// at the extension's source start, because everything above comes back out
	// of the coupling channel or out of the extension and the channel itself
	// codes nothing there. Reading the six bits anyway would move every field
	// after them.
	for ch := range d.nfchans {
		if chexpstr[ch] != ExpReuse && !d.chincpl[ch] && !d.chinspx[ch] {
			d.chbwcod[ch] = uint8(r.Uint32(6))
			if d.chbwcod[ch] > maxChbwcod {
				return reservedError("chbwcod", uint32(d.chbwcod[ch]))
			}
		}
	}
	for ch := range d.nfchans {
		switch {
		case d.chincpl[ch]:
			d.endmant[ch] = d.cplstrtmant
		case d.chinspx[ch]:
			d.endmant[ch] = d.spxstrtmant
		default:
			d.endmant[ch] = chbwcodEndMant(d.chbwcod[ch])
		}
	}
	if d.lfeCh >= 0 {
		d.endmant[d.lfeCh] = lfeMants
	}

	// The coupling channel's exponents. Its leading absolute exponent is a
	// reference to start the chain, not a value: the set it produces begins
	// one place along, at cplstrtmant (clause 6.1.3).
	if d.cplinu && cplexpstr != ExpReuse {
		absexp := uint8(r.Uint32(4)) << 1
		if err := r.Err(); err != nil {
			return wrap(ErrShortFrame)
		}
		ngrps := cplExpGroups(cplexpstr, d.cplstrtmant, d.cplendmant)
		e := &d.exp[MaxChannels]
		if err := decodeExponents(r, cplexpstr, ngrps, absexp, e[d.cplstrtmant:]); err != nil {
			return err
		}
	}

	for ch := range d.nfchans {
		if chexpstr[ch] == ExpReuse {
			continue
		}
		e := &d.exp[ch]
		e[0] = uint8(r.Uint32(4))
		if err := r.Err(); err != nil {
			return wrap(ErrShortFrame)
		}
		ngrps := fbwExpGroups(chexpstr[ch], d.endmant[ch])
		if err := decodeExponents(r, chexpstr[ch], ngrps, e[0], e[1:]); err != nil {
			return err
		}
		d.gainrng[ch] = uint8(r.Uint32(2))
	}

	if d.h.Lfeon && lfeexpstr != ExpReuse {
		e := &d.exp[d.lfeCh]
		e[0] = uint8(r.Uint32(4))
		if err := r.Err(); err != nil {
			return wrap(ErrShortFrame)
		}
		if err := decodeExponents(r, lfeexpstr, lfeExpGroups, e[0], e[1:]); err != nil {
			return err
		}
	}
	return r.Err()
}

// readBitAllocInfo reads the parametric model's settings, the snr offsets, the
// coupling leak levels and the delta bit allocation (clauses 4.4.3.30 to
// 4.4.3.55).
func (d *Decoder) readBitAllocInfo(blk int) error {
	r := &d.r

	baie := r.Bool()
	if blk == 0 && !baie {
		return missingInBlockZero("baie")
	}
	if baie {
		d.sdcycod = uint8(r.Uint32(2))
		d.fdcycod = uint8(r.Uint32(2))
		d.sgaincod = uint8(r.Uint32(2))
		d.dbpbcod = uint8(r.Uint32(2))
		d.floorcod = uint8(r.Uint32(3))
	}

	snroffste := r.Bool()
	if blk == 0 && !snroffste {
		return missingInBlockZero("snroffste")
	}
	if snroffste {
		d.csnroffst = uint8(r.Uint32(6))
		if d.cplinu {
			d.cplfsnroffst = uint8(r.Uint32(4))
			d.cplfgaincod = uint8(r.Uint32(3))
		}
		for ch := range d.nfchans {
			d.fsnroffst[ch] = uint8(r.Uint32(4))
			d.fgaincod[ch] = uint8(r.Uint32(3))
		}
		if d.h.Lfeon {
			d.lfefsnroffst = uint8(r.Uint32(4))
			d.lfefgaincod = uint8(r.Uint32(3))
		}
	}

	if d.cplinu {
		if r.Bool() { // cplleake
			d.cplfleak = uint8(r.Uint32(3))
			d.cplsleak = uint8(r.Uint32(3))
		}
	}

	if !r.Bool() { // deltbaie
		return r.Err()
	}
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
	return r.Err()
}

// readDbaSegments reads one channel's delta bit allocation segments.
func (d *Decoder) readDbaSegments(t *dba) {
	r := &d.r
	t.nseg = int(r.Uint32(3)) + 1
	for seg := range t.nseg {
		t.offst[seg] = uint8(r.Uint32(5))
		t.len[seg] = uint8(r.Uint32(4))
		t.ba[seg] = uint8(r.Uint32(3))
	}
}

// allSnrOffsetsZero reports the special case clause 6.2.2.1 opens with: when
// every snr offset in the block is zero the encoder is saying it spent no bits
// at all, and the model is skipped rather than run.
func (d *Decoder) allSnrOffsetsZero() bool {
	if d.csnroffst != 0 {
		return false
	}
	for ch := range d.nfchans {
		if d.fsnroffst[ch] != 0 {
			return false
		}
	}
	if d.cplinu && d.cplfsnroffst != 0 {
		return false
	}
	return !d.h.Lfeon || d.lfefsnroffst == 0
}

// computeBitAlloc runs the model for every channel of the block.
//
// It runs on every block rather than only when something changed. Everything
// it reads is carried from block to block, so a block that changes nothing
// gets the same answer twice, which makes the rule about when to recompute an
// optimization rather than a correctness matter.
func (d *Decoder) computeBitAlloc() error {
	if d.allSnrOffsetsZero() {
		d.bap = [MaxChannels + 1][MaxCoefs]uint8{}
		return nil
	}

	in := allocInfo{
		fscod:  d.h.Sync.Fscod,
		sdecay: slowdec[d.sdcycod],
		fdecay: fastdec[d.fdcycod],
		sgain:  slowgain[d.sgaincod],
		dbknee: dbpbtab[d.dbpbcod],
		floor:  floortab[d.floorcod],
	}

	if d.cplinu {
		c := in
		c.coupling = true
		c.start, c.end = d.cplstrtmant, d.cplendmant
		c.fgain = fastgain[d.cplfgaincod]
		c.snroffset = snrOffset(d.csnroffst, d.cplfsnroffst)
		c.fleak = int32(d.cplfleak)<<8 + 768
		c.sleak = int32(d.cplsleak)<<8 + 768
		c.hebap = d.eac3.usesAHT[MaxChannels]
		c.d = d.dbaCpl
		if err := d.alloc.compute(&c, &d.exp[MaxChannels], &d.bap[MaxChannels]); err != nil {
			return err
		}
	}

	for ch := range d.nfchans {
		c := in
		c.start, c.end = 0, d.endmant[ch]
		c.fgain = fastgain[d.fgaincod[ch]]
		c.snroffset = snrOffset(d.csnroffst, d.fsnroffst[ch])
		c.hebap = d.eac3.usesAHT[ch]
		c.d = d.dbaCh[ch]
		if err := d.alloc.compute(&c, &d.exp[ch], &d.bap[ch]); err != nil {
			return err
		}
	}

	if d.lfeCh >= 0 {
		c := in
		c.start, c.end = 0, lfeMants
		c.fgain = fastgain[d.lfefgaincod]
		c.snroffset = snrOffset(d.csnroffst, d.lfefsnroffst)
		c.hebap = d.eac3.usesAHT[d.lfeCh]
		if err := d.alloc.compute(&c, &d.exp[d.lfeCh], &d.bap[d.lfeCh]); err != nil {
			return err
		}
	}
	return nil
}

// readMantissas reads the block's mantissas and scales them into transform
// coefficients (clause 4.3.3, the last loop of audblk).
//
// The coupling channel's mantissas sit inside the loop, right after the first
// channel coupled into it, rather than after all of them.
func (d *Decoder) readMantissas(b *Block, blk int) error {
	d.mant.reset()
	gotCplchan := false

	b.Cplinu = d.cplinu
	b.Chincpl = d.chincpl
	b.CplStrtMant, b.CplEndMant = 0, 0
	if d.cplinu {
		b.CplStrtMant, b.CplEndMant = d.cplstrtmant, d.cplendmant
	}

	for ch := range d.nfchans {
		b.EndMant[ch] = d.endmant[ch]
		if err := d.decodeChannelCoeffs(ch, blk, 0, d.endmant[ch],
			b.Dithflag[ch], &b.Coeffs[ch]); err != nil {
			return channelError(ch, err)
		}
		clear(b.Coeffs[ch][d.endmant[ch]:])

		if d.cplinu && d.chincpl[ch] && !gotCplchan {
			// The coupling channel never dithers here: its bins are handed to
			// several channels, and the spec wants each of them to get its own
			// uncorrelated noise once they are back apart.
			if err := d.decodeChannelCoeffs(MaxChannels, blk, d.cplstrtmant,
				d.cplendmant, false, &b.Cpl); err != nil {
				return couplingError(err)
			}
			clear(b.Cpl[:d.cplstrtmant])
			clear(b.Cpl[d.cplendmant:])
			gotCplchan = true
		}
	}
	if !d.cplinu {
		clear(b.Cpl[:])
	}

	if d.lfeCh >= 0 {
		b.EndMant[d.lfeCh] = lfeMants
		if err := d.decodeChannelCoeffs(d.lfeCh, blk, 0, lfeMants,
			false, &b.Coeffs[d.lfeCh]); err != nil {
			return channelError(d.lfeCh, err)
		}
		clear(b.Coeffs[d.lfeCh][lfeMants:])
	}
	for ch := d.h.Channels(); ch < MaxChannels; ch++ {
		b.EndMant[ch] = 0
		clear(b.Coeffs[ch][:])
	}
	return d.r.Err()
}
