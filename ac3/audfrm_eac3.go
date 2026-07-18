package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// The enhanced AC-3 audio frame, clause E.1.3.2.
//
// This is the field AC-3 does not have, and understanding why it exists is most
// of understanding the format. An AC-3 block is self-describing: it says which
// exponent strategy each channel uses, whether coupling is in, how the bits are
// allocated, and it says it again in the next block. That costs bits, and it
// costs them six times a frame whether or not anything changed.
//
// E-AC-3 hoists those decisions out of the blocks and into one field at the top
// of the frame. Some it states once for all six blocks (the exponent strategies,
// through a table of the 32 combinations that are worth having); some it
// replaces with a flag saying "nobody uses this, do not look for it in any
// block" (block switching, dither, the delta bit allocation); some it turns into
// a default that no block has to restate. The blocks that follow are then
// smaller, and - this is the part that bites - they are not parseable on their
// own. A block's syntax depends on what this field said. Read it wrong and every
// block after it reads wrong, with no check word until the end of the frame.

// eac3FrmExpstr is table E2.14, the 32 exponent strategy combinations a channel
// may use across the six blocks of a frame.
//
// A frame states one five bit code per channel instead of six two bit
// strategies, which is smaller and, more to the point, is a whitelist: every
// combination in it starts with a strategy other than reuse, because there is
// nothing in the block before to reuse. The combinations left out are the ones
// that make no sense rather than the ones nobody got round to.
var eac3FrmExpstr = [32][6]uint8{
	{ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpReuse, ExpReuse},
	{ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpReuse, ExpD45},
	{ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpD25, ExpReuse},
	{ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpD45, ExpD45},
	{ExpD25, ExpReuse, ExpReuse, ExpD25, ExpReuse, ExpReuse},
	{ExpD25, ExpReuse, ExpReuse, ExpD25, ExpReuse, ExpD45},
	{ExpD25, ExpReuse, ExpReuse, ExpD45, ExpD25, ExpReuse},
	{ExpD25, ExpReuse, ExpReuse, ExpD45, ExpD45, ExpD45},
	{ExpD25, ExpReuse, ExpD15, ExpReuse, ExpReuse, ExpReuse},
	{ExpD25, ExpReuse, ExpD25, ExpReuse, ExpReuse, ExpD45},
	{ExpD25, ExpReuse, ExpD25, ExpReuse, ExpD25, ExpReuse},
	{ExpD25, ExpReuse, ExpD25, ExpReuse, ExpD45, ExpD45},
	{ExpD25, ExpReuse, ExpD45, ExpD25, ExpReuse, ExpReuse},
	{ExpD25, ExpReuse, ExpD45, ExpD25, ExpReuse, ExpD45},
	{ExpD25, ExpReuse, ExpD45, ExpD45, ExpD25, ExpReuse},
	{ExpD25, ExpReuse, ExpD45, ExpD45, ExpD45, ExpD45},
	{ExpD45, ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpReuse},
	{ExpD45, ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpD45},
	{ExpD45, ExpD25, ExpReuse, ExpReuse, ExpD25, ExpReuse},
	{ExpD45, ExpD25, ExpReuse, ExpReuse, ExpD45, ExpD45},
	{ExpD45, ExpD25, ExpReuse, ExpD25, ExpReuse, ExpReuse},
	{ExpD45, ExpD25, ExpReuse, ExpD25, ExpReuse, ExpD45},
	{ExpD45, ExpD25, ExpReuse, ExpD45, ExpD25, ExpReuse},
	{ExpD45, ExpD25, ExpReuse, ExpD45, ExpD45, ExpD45},
	{ExpD45, ExpD45, ExpD15, ExpReuse, ExpReuse, ExpReuse},
	{ExpD45, ExpD45, ExpD25, ExpReuse, ExpReuse, ExpD45},
	{ExpD45, ExpD45, ExpD25, ExpReuse, ExpD25, ExpReuse},
	{ExpD45, ExpD45, ExpD25, ExpReuse, ExpD45, ExpD45},
	{ExpD45, ExpD45, ExpD45, ExpD25, ExpReuse, ExpReuse},
	{ExpD45, ExpD45, ExpD45, ExpD25, ExpReuse, ExpD45},
	{ExpD45, ExpD45, ExpD45, ExpD45, ExpD25, ExpReuse},
	{ExpD45, ExpD45, ExpD45, ExpD45, ExpD45, ExpD45},
}

// The default bit allocation parameters, used when a frame says the blocks
// carry none (clause E.1.3.2.11). They are the codes a real AC-3 encoder
// almost always sends, made into the "said nothing" case.
const (
	defaultSdcycod  = 2
	defaultFdcycod  = 1
	defaultSgaincod = 1
	defaultDbpbcod  = 2
	defaultFloorcod = 7
)

// eac3Frame is what the audio frame field says, and what the blocks after it
// need in order to be read at all.
type eac3Frame struct {
	// expStrategy is every channel's strategy for every block, which in this
	// syntax is known before any block is read. Index MaxChannels is the
	// coupling channel, as everywhere else here.
	expStrategy [BlocksPerFrame][MaxChannels + 1]uint8

	// Coupling, decided per frame rather than per block: cplStrategyExists says
	// which blocks restate the strategy, cplInUse which blocks couple at all.
	cplStrategyExists [BlocksPerFrame]bool
	cplInUse          [BlocksPerFrame]bool

	// usesAHT says which channels code their six blocks as one adaptive hybrid
	// transform rather than six separate ones.
	usesAHT [MaxChannels + 1]bool

	// The syntax flags: each says whether the blocks carry a field at all. A
	// false one is not a default value, it is an absence - the blocks do not
	// hold those bits, so a decoder that looks for them reads the next field's
	// bits instead and every block after is nonsense.
	blockSwitchSyntax   bool
	ditherFlagSyntax    bool
	bitAllocationSyntax bool
	fastGainSyntax      bool
	dbaSyntax           bool
	skipSyntax          bool

	snrOffsetStrategy uint8
	spxAttenCode      [MaxFBWChannels]int8 // -1 when the channel states none

	// Per frame state the blocks carry between them. Each says "the next block
	// that needs this is the first to, so it states it outright rather than
	// inheriting". The audio frame field sets them; the blocks clear them.
	//
	// The band structures are not here: they cross frames, so they live on the
	// Decoder, out of this struct's per-frame wipe. See Decoder.cplBandStruct.
	firstCplCoords [MaxFBWChannels]bool
	firstSpxCoords [MaxFBWChannels]bool
	firstCplLeak   bool
}

// eac3DefaultCplBandStruct is the coupling band structure a frame gets when it
// states none (table E.13). AC-3 has no such default and spends the bits every
// block.
var eac3DefaultCplBandStruct = [maxCplSubbands]bool{
	false, false, false, false, false, false, false, false, true,
	false, true, true, false, true, true, true, true, true,
}

// defaultFgaincod is the fast gain every channel starts at when a frame carries
// no fast gain of its own: the middle of the table.
const defaultFgaincod = 4

// parseEAC3AudioFrame reads the audio frame field, leaving r at the first bit of
// the first audio block.
func (d *Decoder) parseEAC3AudioFrame(r *bitstream.Reader) error {
	f := &d.eac3
	*f = eac3Frame{}

	nfchans := d.h.FullBandwidthChannels()
	nblks := d.h.Sync.NumBlocks

	// A frame of fewer than six blocks has no room for the table of six
	// strategies and no room for a transform over six blocks, so it falls back
	// to stating a strategy per block the way AC-3 does, and cannot use AHT.
	ac3ExpStrategy, parseAHT := true, false
	if nblks == BlocksPerFrame {
		ac3ExpStrategy = r.Bool()
		parseAHT = r.Bool()
	}

	f.snrOffsetStrategy = uint8(r.Uint32(2))
	parseTransientProc := r.Bool()

	f.blockSwitchSyntax = r.Bool()
	f.ditherFlagSyntax = r.Bool()
	f.bitAllocationSyntax = r.Bool()
	if !f.bitAllocationSyntax {
		// Not "no bit allocation": the parameters an AC-3 block would have
		// stated, made into the case where nobody says anything. A frame that
		// leaves them out is the common one, so getting this wrong does not
		// produce a rare bug, it produces a decoder that works on nothing.
		d.sdcycod = defaultSdcycod
		d.fdcycod = defaultFdcycod
		d.sgaincod = defaultSgaincod
		d.dbpbcod = defaultDbpbcod
		d.floorcod = defaultFloorcod
	}
	f.fastGainSyntax = r.Bool()
	f.dbaSyntax = r.Bool()
	f.skipSyntax = r.Bool()
	parseSpxAtten := r.Bool()

	// Coupling, per block. Block 0 always states a strategy - there is nothing
	// before it to inherit one from - and later blocks either restate it or
	// carry on with what the block before them used.
	var cplBlocks int
	if d.h.Acmod > AcmodMono {
		for blk := range nblks {
			f.cplStrategyExists[blk] = blk == 0 || r.Bool()
			if f.cplStrategyExists[blk] {
				f.cplInUse[blk] = r.Bool()
			} else {
				f.cplInUse[blk] = f.cplInUse[blk-1]
			}
			if f.cplInUse[blk] {
				cplBlocks++
			}
		}
	}

	// Exponent strategies, either the AC-3 way - two bits per channel per block
	// - or the table of combinations, five bits per channel for the whole frame.
	if ac3ExpStrategy {
		for blk := range nblks {
			// The coupling channel only has a strategy in a block that couples.
			for ch := range nfchans + 1 {
				if ch == 0 && !f.cplInUse[blk] {
					continue
				}
				f.expStrategy[blk][d.eac3ChannelIndex(ch)] = uint8(r.Uint32(2))
			}
		}
	} else {
		for ch := range nfchans + 1 {
			// The coupling channel is in the table only if some block couples.
			if ch == 0 && !(d.h.Acmod > AcmodMono && cplBlocks > 0) {
				continue
			}
			code := uint8(r.Uint32(5))
			if int(code) >= len(eac3FrmExpstr) {
				return reservedError("frmchexpstr", uint32(code))
			}
			for blk := range BlocksPerFrame {
				f.expStrategy[blk][d.eac3ChannelIndex(ch)] = eac3FrmExpstr[code][blk]
			}
		}
	}

	// The LFE states one bit per block: its exponents are either new or reused,
	// and when they are new they are always D15. It is one seventh of a
	// channel's bandwidth; there is nothing to gain by coding it coarsely.
	if d.h.Lfeon {
		for blk := range nblks {
			if r.Bool() {
				f.expStrategy[blk][d.lfeCh] = ExpD15
			} else {
				f.expStrategy[blk][d.lfeCh] = ExpReuse
			}
		}
	}

	// A converted frame can carry the strategies its AC-3 frame had, so that
	// the conversion can be undone. Nothing here needs them.
	if d.h.Sync.Strmtyp == StrmtypIndependent {
		if nblks == BlocksPerFrame || r.Bool() {
			r.Skip(5 * nfchans)
		}
	}

	d.parseEAC3AHT(r, parseAHT, cplBlocks)

	// One SNR offset for the whole frame, rather than one per block per channel.
	if f.snrOffsetStrategy == 0 {
		csnroffst := uint8(r.Uint32(6))
		fsnroffst := uint8(r.Uint32(4))
		d.csnroffst = csnroffst
		for ch := range MaxFBWChannels {
			d.fsnroffst[ch] = fsnroffst
		}
		d.cplfsnroffst = fsnroffst
		d.lfefsnroffst = fsnroffst
	}

	// Where a transient sits inside a block, for a decoder that means to soften
	// the noise before it. This one does not: it is a post-process on the
	// samples, not part of reconstructing them, and a decoder that skips it
	// produces the samples the encoder coded.
	if parseTransientProc {
		for range nfchans {
			if r.Bool() {
				r.Skip(10 + 8)
			}
		}
	}

	// How much to attenuate what spectral extension invents at the top of each
	// channel.
	for ch := range MaxFBWChannels {
		f.spxAttenCode[ch] = -1
	}
	for ch := range nfchans {
		if parseSpxAtten && r.Bool() {
			f.spxAttenCode[ch] = int8(r.Uint32(5))
		}
	}

	// Where each block starts within the frame. The spec does not say what it
	// is for, and nothing here needs it, but its width has to be computed
	// exactly or the first audio block starts in the wrong place.
	if nblks > 1 && r.Bool() {
		r.Skip((nblks - 1) * (4 + floorLog2(d.h.Sync.FrameSize-2)))
	}

	// The blocks inherit a great deal from each other; this is where they start
	// from, and it has to be set per frame rather than per stream.
	for ch := range MaxFBWChannels {
		f.firstCplCoords[ch] = true
		f.firstSpxCoords[ch] = true
	}
	f.firstCplLeak = true

	// The band structures are deliberately not touched here: they cross
	// frames. A frame whose extension or coupling only comes up mid-frame and
	// states no structure there inherits the previous frame's, exactly as the
	// reference decoder does; the default table only comes in through a block
	// 0 that reads the strategy.

	return r.Err()
}

// parseEAC3AHT reads which channels use the adaptive hybrid transform.
//
// A channel may only use it when its six blocks share one set of exponents,
// which is what makes them one transform rather than six: every block after the
// first has to reuse. The coupling channel has the same rule and one more, that
// every block couples the same way, since a coupling channel that comes and goes
// is not one signal over the frame.
func (d *Decoder) parseEAC3AHT(r *bitstream.Reader, parseAHT bool, cplBlocks int) {
	f := &d.eac3
	if !parseAHT {
		return
	}
	for ch := range d.h.Channels() + 1 {
		// The coupling channel is only asked about when every block couples.
		if ch == 0 && cplBlocks != BlocksPerFrame {
			continue
		}
		idx := d.eac3ChannelIndex(ch)
		use := true
		for blk := 1; blk < BlocksPerFrame; blk++ {
			if f.expStrategy[blk][idx] != ExpReuse || (ch == 0 && f.cplStrategyExists[blk]) {
				use = false
				break
			}
		}
		f.usesAHT[idx] = use && r.Bool()
	}
}

// eac3ChannelIndex maps the reference's channel numbering onto this package's.
//
// The reference numbers the coupling channel 0 and the coded channels from 1;
// this package numbers the coded channels from 0 and puts the coupling channel
// last, at MaxChannels. The loops above are the reference's, because the order
// they read fields in is the bit stream's order and cannot be rearranged; this
// is where that order lands.
func (d *Decoder) eac3ChannelIndex(ch int) int {
	if ch == 0 {
		return MaxChannels
	}
	return ch - 1
}

// floorLog2 returns the index of the highest set bit of v, that is
// floor(log2(v)), and 0 for v <= 0.
//
// The spec writes this width as ceiling(log2(words_per_frame)); the reference
// computes floor(log2(bytes_per_frame - 2)), and the two agree on every frame
// size that exists. The reference's is the one used here, because the reference
// is what this decoder is checked against and a width that disagreed would put
// the first audio block at the wrong bit.
func floorLog2(v int) int {
	n := 0
	for v > 1 {
		n++
		v >>= 1
	}
	return n
}
