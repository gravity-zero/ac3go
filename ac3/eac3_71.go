package ac3

import "github.com/gravity-zero/ac3go/pcm"

// E-AC-3 7.1 through a dependent substream (clause E.1.2).
//
// A 7.1 programme is not one syncframe. It is an independent substream carrying
// a 5.1 core, immediately followed by a dependent substream carrying the extra
// channels, both at the same timestamp. The dependent substream states a
// channel map saying where its channels go; the reference decoder merges the
// two into 7.1, dropping the core's surround channels in favour of the
// dependent's. This file does the same, for the one extension real streams use.
//
// What is deliberately narrow: only the standard 7.1 extension is merged - a
// 3/2+LFE core plus a dependent whose channel map is exactly the two side and
// two back speakers. That is what every 7.1 DDP stream measured carries, and
// it is what a reference decoder can be checked against. Anything else leaves
// the output at the 5.1 core rather than guessing a layout nothing could
// validate. The core itself may be AC-3 or E-AC-3: some 7.1 streams use an
// alternate-syntax AC-3 core with an enhanced dependent, and it merges the
// same way - only the core's own syntax differs, which the decoder has already
// handled by the time the merge runs.

// eac3Chanmap71 is the dependent channel map of the standard 7.1 extension:
// side left (bit 12) and side right (bit 11), plus the back pair (bit 9). See
// the custom channel map, table E.5.
const eac3Chanmap71 = 0x1A00

// decodeDependent71 is called after the independent substream of an E-AC-3
// frame is decoded, with full the buffer that began with it. If a dependent
// substream extending the core to 7.1 follows, it decodes it and marks the
// output 7.1; otherwise it leaves the 5.1 core in place. Either way it sets
// auSize so the caller advances past whatever was consumed.
func (d *Decoder) decodeDependent71(full []byte) error {
	if d.h.Sync.Strmtyp != StrmtypIndependent {
		return nil // a dependent substream decoded on its own: no merge
	}
	rest := full[d.h.Sync.FrameSize:]
	// The common case is a buffer holding just this frame, or the next one an
	// independent substream too: no room for a dependent, and nothing to parse.
	// Returning before ParseHeader keeps that case from building an error value,
	// which is what a decode with nothing after it would otherwise allocate.
	if len(rest) < EAC3SyncInfoSize {
		return nil
	}

	dh := &d.depHdr // a decoder field, so the common no-dependent case allocates nothing
	if err := ParseHeader(rest, dh); err != nil {
		return nil // nothing decodable follows: an ordinary 5.1 or stereo frame
	}
	if !isEAC3(dh.Sync.Bsid) || dh.Sync.Strmtyp != StrmtypDependent || dh.Sync.Substreamid != 0 {
		return nil
	}

	// Only the standard 7.1 extension is merged; anything else advances past
	// the dependent substream but keeps the 5.1 core, the way a decoder that
	// does not reach for those channels would.
	d.auSize = d.h.Sync.FrameSize + dh.Sync.FrameSize
	if !is71Extension(&d.h, dh) {
		return nil
	}

	if d.dep == nil {
		d.dep = NewDecoder()
	}
	d.dep.SetDither(d.mant.dither)
	if err := d.dep.DecodeFrame(rest[:dh.Sync.FrameSize]); err != nil {
		return err
	}
	if d.dep.OutputChannels() != 4 {
		return nil // not the four side/back channels we mapped; keep 5.1
	}
	d.output71 = true
	return nil
}

// is71Extension reports whether an independent header and a dependent one form
// the standard 7.1 extension: a 3/2+LFE core and a dependent stating the two
// side and two back channels.
func is71Extension(indep, dep *Header) bool {
	return indep.Acmod == Acmod3F2R && indep.Lfeon &&
		dep.Chanmape && dep.Chanmap == eac3Chanmap71
}

// samples71 returns channel ch of the 7.1 output, in Layout7point1 order. The
// front and LFE come from the independent substream's 5.1 core; the two back
// and two side speakers come from the dependent substream, placed by the
// channel map the extension always uses. The core's own surround channels are
// dropped: the dependent replaces them, which is what the channel map means and
// what a reference decoder does.
func (d *Decoder) samples71(ch int) []float32 {
	switch pcm.Layout7point1[ch] {
	case pcm.ChannelLeft:
		return d.pcm[0][:] // core L
	case pcm.ChannelRight:
		return d.pcm[2][:] // core R
	case pcm.ChannelCenter:
		return d.pcm[1][:] // core C
	case pcm.ChannelLFE:
		return d.pcm[d.lfeCh][:]
	case pcm.ChannelSideLeft:
		return d.dep.pcm[0][:]
	case pcm.ChannelSideRight:
		return d.dep.pcm[1][:]
	case pcm.ChannelBackLeft:
		return d.dep.pcm[2][:]
	case pcm.ChannelBackRight:
		return d.dep.pcm[3][:]
	}
	return d.pcm[0][:]
}
