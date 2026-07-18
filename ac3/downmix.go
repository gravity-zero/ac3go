package ac3

import (
	"fmt"

	"github.com/gravity-zero/ac3go/pcm"
)

// Downmixing, clause 7.8.
//
// A 5.1 programme has to be playable on two speakers, and the format does not
// leave that to whoever is listening: the encoder states, in the bit stream,
// how loud it wants its centre and its surrounds to be when they are folded
// into left and right. Clause 7.8 turns those two levels into the coefficients
// below, so that a mix monitored through a downmix at the studio comes out of a
// pair of speakers the way it was monitored.
//
// The LFE is not in any of it. It carries an effects channel that the mix does
// not depend on, its band stops at 120 Hz, and the spec's equations leave it
// out; so does the reference, and so does this.
//
// This is the Lo/Ro downmix, the plain one. The other, Lt/Rt, folds the
// surrounds in out of phase so that a Dolby Surround decoder downstream can
// pull them back out, and it is not implemented: it is a different product
// (matrix-encoded stereo, for a decoder that expects it) rather than a better
// version of this one.

// The level question, which is the whole of the difficulty here.
//
// The equations of clause 7.8.2 are, for 3/2:
//
//	Lo = 1.0*L + clev*C + slev*Ls
//	Ro = 1.0*R + clev*C + slev*Rs
//
// and if a decoder emitted that, a mix with everything at full scale would come
// out at nearly two and a half times full scale. The spec introduces those
// equations as the ones that hold "prior to the scaling needed to prevent
// overflow", and never as an output: the un-scaled form is the one thing here
// that no reading of the spec sanctions.
//
// What to scale by is not settled, and it is worth being honest that this code
// picks one of several conforming answers rather than the only one:
//
//   - Clause 7.8.1 permits normalizing, and defines it as "attenuating all
//     downmix coefficients equally, such that the sum of coefficients used to
//     create any single output channel never exceeds 1". That is per output and
//     depends on the levels the stream states.
//   - Clause 7.8.2 recommends, "for simplicity", one fixed factor for every
//     mode and every stream: the worst case, clev = slev = 0.707, giving
//     1/2.414, an unconditional 7.65 dB down. Its Table 7.31 is built on it.
//   - The ETSI edition of the same text softens the ATSC "must be scaled
//     downwards" to "may need to be", which leaves a float decoder with
//     headroom room to argue it has no overflow to prevent.
//
// This takes the first, dividing each output by the sum of its own
// coefficients. Two reasons, and neither is that the spec demands it: it is
// what the reference decoder does, so the oracle can be exact rather than
// approximate; and it is also, independently, what liba52 does, which makes it
// two conforming decoders converging on one answer rather than one decoder's
// habit. It is a decibel and change louder than the fixed factor of 7.8.2, and
// a stream with quiet surrounds keeps more of its level than the worst case
// would give it.

// downmixCoeffs holds, per coded full bandwidth channel, how much of it goes
// into each of the two outputs.
type downmixCoeffs [2][MaxFBWChannels]float32

// setDownmixCoeffs fills the Lo/Ro coefficients for a header, normalized so
// each output's coefficients sum to one.
//
// The coefficients before normalizing are the spec's: a front channel goes to
// its own side at unity, the centre goes to both at the level the stream states
// for it, and a surround goes to its own side at the level the stream states
// for surrounds. A mono surround - the 2/1 and 3/1 modes, one S rather than an
// Ls and an Rs - goes to both sides, 3 dB down on each, which is what splitting
// one channel across two without changing its power costs.
func setDownmixCoeffs(h *Header, c *downmixCoeffs) {
	*c = downmixCoeffs{}
	cmix := h.CenterMixLevel()
	smix := h.SurroundMixLevel()

	set := func(ch int, lo, ro float32) {
		c[0][ch], c[1][ch] = lo, ro
	}
	switch h.Acmod {
	case AcmodDualMono: // two programmes, one per output: not a mix at all
		set(0, 1, 0)
		set(1, 0, 1)
	case AcmodMono:
		set(0, levelMinus3dB, levelMinus3dB)
	case AcmodStereo:
		set(0, 1, 0)
		set(1, 0, 1)
	case Acmod3F: // L C R
		set(0, 1, 0)
		set(1, cmix, cmix)
		set(2, 0, 1)
	case Acmod2F1R: // L R S
		set(0, 1, 0)
		set(1, 0, 1)
		set(2, smix*levelMinus3dB, smix*levelMinus3dB)
	case Acmod3F1R: // L C R S
		set(0, 1, 0)
		set(1, cmix, cmix)
		set(2, 0, 1)
		set(3, smix*levelMinus3dB, smix*levelMinus3dB)
	case Acmod2F2R: // L R Ls Rs
		set(0, 1, 0)
		set(1, 0, 1)
		set(2, smix, 0)
		set(3, 0, smix)
	case Acmod3F2R: // L C R Ls Rs
		set(0, 1, 0)
		set(1, cmix, cmix)
		set(2, 0, 1)
		set(3, smix, 0)
		set(4, 0, smix)
	}

	// Normalize: each output's coefficients sum to one, so that every channel
	// at full scale sums to full scale and no further.
	nf := h.FullBandwidthChannels()
	for out := range 2 {
		var sum float32
		for ch := range nf {
			sum += c[out][ch]
		}
		if sum == 0 {
			continue
		}
		for ch := range nf {
			c[out][ch] /= sum
		}
	}
}

// SetDownmix asks the decoder to fold the coded channels into layout, which
// must be pcm.LayoutStereo or pcm.LayoutMono. Passing nil turns it off, which
// is the default: a Decoder left alone hands back the channels the stream
// codes.
//
// It takes effect from the next frame. The channels it hands back afterwards
// are the layout's, in the layout's order, and Samples indexes them; the LFE is
// not among them, whatever the stream carries.
//
// A stream that already codes the layout asked for is not downmixed, it is left
// alone - with one exception that is not an exception: the dual mono mode codes
// two channels that are two different programmes rather than a left and a
// right, and asking for stereo puts one in each output, which is what every
// decoder does with it and is why it is not an error.
func (d *Decoder) SetDownmix(layout pcm.Layout) error {
	switch {
	case len(layout) == 0:
		d.dmixChannels = 0
	case len(layout) == 2 && layout[0] == pcm.ChannelLeft && layout[1] == pcm.ChannelRight:
		d.dmixChannels = 2
	case len(layout) == 1 && layout[0] == pcm.ChannelCenter:
		d.dmixChannels = 1
	default:
		return fmt.Errorf("ac3: cannot downmix to %s: only stereo and mono", layout)
	}
	d.dmixFor = 0 // force a recompute: the header has not changed but the request has
	return nil
}

// OutputLayout returns the channels Samples hands back for the frame last
// decoded, which is the coded layout unless a downmix was asked for.
func (d *Decoder) OutputLayout() pcm.Layout {
	switch {
	case d.output71:
		return pcm.Layout7point1
	case !d.downmixing():
		return d.h.Layout()
	case d.dmixChannels == 1:
		return pcm.LayoutMono
	default:
		return pcm.LayoutStereo
	}
}

// OutputChannels is the number of planes Samples has, which is len of
// OutputLayout and is not the number of channels the stream codes.
func (d *Decoder) OutputChannels() int {
	switch {
	case d.output71:
		return len(pcm.Layout7point1)
	case d.downmixing():
		return d.dmixChannels
	}
	return d.h.Channels()
}

// downmixing reports whether the frame last decoded needs mixing down. A
// request to reach a layout the stream already codes is not one - except for
// dual mono, whose two channels are not a stereo pair and are spread into one
// by the coefficients like anything else.
func (d *Decoder) downmixing() bool {
	if d.dmixChannels == 0 {
		return false
	}
	if d.dmixChannels == 2 && d.h.Acmod == AcmodStereo {
		return false
	}
	return true
}

// updateDownmix recomputes the coefficients for the current header, skipping
// the work when nothing they depend on has moved - which, in a real stream, is
// after the first frame.
func (d *Decoder) updateDownmix() {
	if !d.downmixing() {
		return
	}
	// The three header fields the coefficients are a function of, plus the
	// output width, in one key. None is wider than three bits.
	key := uint16(d.h.Acmod)<<8 | uint16(d.h.Cmixlev)<<5 | uint16(d.h.Surmixlev)<<2 |
		uint16(d.dmixChannels) | 1<<15
	if key == d.dmixFor {
		return
	}
	d.dmixFor = key
	setDownmixCoeffs(&d.h, &d.dmixCoeffs)

	// Mono is the stereo downmix summed, 3 dB down so that the sum of two
	// outputs that each reach full scale does not reach twice it.
	if d.dmixChannels == 1 {
		for ch := range MaxFBWChannels {
			d.dmixCoeffs[0][ch] = (d.dmixCoeffs[0][ch] + d.dmixCoeffs[1][ch]) * levelMinus3dB
		}
	}
}

// downmix folds the decoded channels into d.dmix.
//
// It works on the samples rather than on the coefficients. The reference mixes
// the coefficients when it can, which is the same arithmetic in a cheaper place
// - everything between them, the transform included, is linear - but it cannot
// when the channels of a block did not all use the same transform, and then it
// has to undo the mixing it did to the overlap buffer and mix again afterwards.
// Doing it here costs one pass over the samples and has no such case.
func (d *Decoder) downmix() {
	nf := d.h.FullBandwidthChannels()
	for out := range d.dmixChannels {
		dst := d.dmix[out][:]
		// The first contributing channel is written rather than added, which
		// saves clearing the buffer first.
		first := true
		for ch := range nf {
			g := d.dmixCoeffs[out][ch]
			if g == 0 {
				continue
			}
			src := d.pcm[ch][:]
			if first {
				for i := range dst {
					dst[i] = src[i] * g
				}
				first = false
				continue
			}
			for i := range dst {
				dst[i] += src[i] * g
			}
		}
		if first {
			clear(dst)
		}
	}
}
