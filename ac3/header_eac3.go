package ac3

import "github.com/gravity-zero/ac3go/bitstream"

// Enhanced AC-3, annex E: the syncframe and its bit stream information.
//
// E-AC-3 is not a second format, it is this one with a different front end.
// The audio blocks it carries are the blocks clause 7 describes, decoded by
// the same exponents, the same bit allocation, the same transform and the same
// overlap as anything else in this package; what annex E replaces is the
// header, and what it adds - the adaptive hybrid transform, spectral
// extension, enhanced coupling - it adds inside blocks that are otherwise the
// same. That is why it lives here rather than in a package of its own.
//
// What changes in the header, and it changes a lot:
//
//   - The frame size is stated outright, in 16 bit words, rather than looked up
//     from a bit rate code. Any size, any rate.
//   - A frame carries 1, 2, 3 or 6 blocks rather than always 6, so a frame is
//     no longer 1536 samples.
//   - There is no crc1. AC-3 checks its first five eighths and then the whole
//     frame; E-AC-3 has only the check word at the end.
//   - A frame belongs to a substream, and a stream is a set of them: one
//     independent substream carries the programme, dependent ones carry extra
//     channels to be mixed into it. Only substream 0 is decoded here.
//   - The mix levels moved into optional metadata and changed width, from a two
//     bit code into a table to a three bit index straight into the gain levels.
//     A frame that states nothing gets the defaults, which are not the same as
//     an AC-3 frame that states nothing.

// Frame types (strmtyp, clause E.1.3.1.1). A stream is a set of substreams and
// this says what part the frame plays in it.
const (
	// StrmtypIndependent is a substream that stands on its own. Substream 0 of
	// every E-AC-3 stream is one, and it carries the programme.
	StrmtypIndependent uint8 = iota

	// StrmtypDependent carries channels that extend the independent substream
	// before it, which is how the format reaches beyond 5.1. Decoding one
	// means decoding the substream it depends on first.
	StrmtypDependent

	// StrmtypAC3Convert is an independent substream whose frame was made by
	// converting an AC-3 frame. It decodes as an independent one; the type is
	// there so a converter can undo the conversion.
	StrmtypAC3Convert

	StrmtypReserved
)

// Bit stream versions. The spec numbers E-AC-3 from 11 to 16 and every encoder
// writes 16; the range exists so that a decoder can tell a version it does not
// know from a format it does not know.
const (
	// MinEAC3BSID is the lowest bsid that announces enhanced AC-3.
	MinEAC3BSID = 11

	// MaxEAC3BSID is the highest bsid this package accepts at all. Past it the
	// frame is not a bit stream this decoder has ever heard of.
	MaxEAC3BSID = 16

	// bsidBit is where bsid sits in a syncframe, counted from the syncword.
	//
	// It is the same bit in both syntaxes, which is not a coincidence and is
	// the whole reason a decoder can read a stream it was handed without being
	// told what it is: everything before it - the check word and frame size
	// code of AC-3, the substream and frame size of E-AC-3 - is the same width
	// even though it means different things.
	bsidBit = 40
)

// eac3Blocks maps numblkscod to the number of audio blocks in the frame
// (clause E.1.3.1.3).
var eac3Blocks = [4]int{1, 2, 3, 6}

// isEAC3 reports whether a bsid announces enhanced AC-3.
func isEAC3(bsid uint8) bool { return bsid >= MinEAC3BSID && bsid <= MaxEAC3BSID }

// peekBSID returns the bsid of a syncframe without parsing anything else. The
// frame must hold at least the bytes bsid ends in.
func peekBSID(b []byte) uint8 {
	// Bits 40 to 44: the low three bits of byte 5 and the top two of byte 6.
	return b[5] >> 3
}

// parseEAC3SyncInfo fills the syncinfo of an enhanced AC-3 frame: enough to cut
// it out of a stream.
//
// There is no check word here to reject a false sync with, which matters more
// than it sounds: an AC-3 frame that syncs by chance is caught by crc1 five
// eighths of the way in, and an E-AC-3 frame has nothing to catch it with until
// the end of a frame whose length the false sync itself supplied. The reserved
// codes below are what is left, and they are worth checking for that reason
// rather than out of pedantry.
func parseEAC3SyncInfo(b []byte, si *SyncInfo) error {
	var r bitstream.Reader
	r.Reset(b)
	r.Skip(16) // the syncword, already matched

	si.Strmtyp = uint8(r.Uint32(2))
	if si.Strmtyp == StrmtypReserved {
		return reservedError("strmtyp", uint32(si.Strmtyp))
	}
	si.Substreamid = uint8(r.Uint32(3))

	// frmsiz counts 16 bit words and counts from zero, so the smallest frame it
	// can state is one word and the largest is 2048.
	si.FrameSize = (int(r.Uint32(11)) + 1) * 2

	si.Fscod = uint8(r.Uint32(2))
	if si.Fscod == 3 {
		// The reduced rate syntax: the field that would have been numblkscod is
		// a second rate code, and the frame is six blocks at half the rate it
		// names. A frame cannot say both, which is why numblkscod is not read
		// here and NumBlocks stays at six.
		si.Fscod2 = uint8(r.Uint32(2))
		if si.Fscod2 == 3 {
			return reservedError("fscod2", uint32(si.Fscod2))
		}
		si.HasFscod2 = true
		si.SampleRate = sampleRates[si.Fscod2] / 2
		si.NumBlocks = BlocksPerFrame
	} else {
		si.Numblkscod = uint8(r.Uint32(2))
		si.NumBlocks = eac3Blocks[si.Numblkscod]
		si.SampleRate = sampleRates[si.Fscod]
	}

	si.Acmod = uint8(r.Uint32(3))
	si.Lfeon = r.Bool()
	si.Bsid = uint8(r.Uint32(5))

	if r.Err() != nil {
		return shortFrameError(len(b), EAC3SyncInfoSize)
	}
	if si.FrameSize < EAC3SyncInfoSize {
		return frameTooShort(si.FrameSize, EAC3SyncInfoSize)
	}

	// The rate a frame of this size and this many blocks works out to. E-AC-3
	// states no bit rate: it states a frame size, and the rate is what that
	// comes to once you know how much time the frame covers.
	si.BitRate = 8 * si.FrameSize * si.SampleRate / (si.NumBlocks * SamplesPerBlock)
	return nil
}

// parseEAC3BSI reads the bit stream information of an enhanced AC-3 frame,
// leaving r at the first bit of the first audio block.
//
// Most of what it reads it throws away. The mixing and information metadata are
// long, optional and almost entirely about what a downstream mixer should do
// with the programme rather than about how to decode it - but every field of
// them has to be read anyway, because they stand between the syncword and the
// audio and the audio does not say where it starts.
func (h *Header) parseEAC3BSI(r *bitstream.Reader) error {
	// The mix levels default rather than being absent: a frame that carries no
	// mixing metadata is not a frame with no centre level, it is a frame at the
	// level the spec names. They are indices into the gain levels here, not the
	// two bit codes of an AC-3 frame.
	h.Cmixlev = gainLevelMinus4Point5dB
	h.Surmixlev = gainLevelMinus6dB
	h.HasCmixlev = false
	h.HasSurmixlev = false
	h.Dsurmod = DsurmodNotIndicated

	// Volume control, one set per programme: the dual mono mode carries two.
	nprog := 1
	if h.Sync.Acmod == AcmodDualMono {
		nprog = 2
	}
	for i := range nprog {
		dialnorm := uint8(r.Uint32(5))
		compre := r.Bool()
		var compr uint8
		if compre {
			compr = uint8(r.Uint32(8))
		}
		if i == 0 {
			h.Dialnorm, h.Compre, h.Compr = dialnorm, compre, compr
		} else {
			h.Dialnorm2, h.Compr2e, h.Compr2 = dialnorm, compre, compr
		}
	}

	// A dependent substream says which channels it carries. This decoder does
	// not decode dependent substreams, but it has to be able to walk past one.
	if h.Sync.Strmtyp == StrmtypDependent {
		h.Chanmape = r.Bool()
		if h.Chanmape {
			h.Chanmap = uint16(r.Uint32(16))
		}
	}

	if err := h.parseEAC3MixingMetadata(r); err != nil {
		return err
	}
	h.parseEAC3InfoMetadata(r)

	// A frame of fewer than six blocks carries a flag, once every six blocks,
	// marking where a set of them starts. It is for a converter rebuilding
	// AC-3 frames, and it is nothing to a decoder.
	if h.Sync.Strmtyp == StrmtypIndependent && h.Sync.NumBlocks != BlocksPerFrame {
		h.Convsync = r.Bool()
	}

	// A frame converted from AC-3 can carry the frame size code its AC-3 frame
	// had, so that the conversion can be undone exactly.
	if h.Sync.Strmtyp == StrmtypAC3Convert {
		if h.Sync.NumBlocks == BlocksPerFrame || r.Bool() {
			h.Blkid = true
			h.Frmsizecod = uint8(r.Uint32(6))
		}
	}

	h.Addbsie = r.Bool()
	if h.Addbsie {
		h.addbsil = uint8(r.Uint32(6))
		for i := range int(h.addbsil) + 1 {
			h.addbsi[i] = byte(r.Uint32(8))
		}
	}

	if err := r.Err(); err != nil {
		return shortFrameError(h.Sync.FrameSize, h.Sync.FrameSize+1)
	}
	h.AudioStartBit = r.BitPos()
	return nil
}

// parseEAC3MixingMetadata reads the mixing metadata (clause E.1.3.1.8 and on),
// keeping the mix levels and skipping the rest.
//
// The rest is real metadata, not padding: how loudly to fold this programme
// into another one, where to pan a mono source, what an external mixer should
// scale it by. None of it changes a sample this decoder produces - it is about
// mixing two programmes together, which is a thing this decoder does not do -
// but all of it has to be walked past exactly, field by field, because the
// audio starts wherever it ends.
func (h *Header) parseEAC3MixingMetadata(r *bitstream.Reader) error {
	h.Mixmdate = r.Bool()
	if !h.Mixmdate {
		return nil
	}

	if h.Sync.Acmod > AcmodStereo {
		h.Dmixmod = uint8(r.Uint32(2))
		if h.Sync.Acmod&1 != 0 { // three front channels
			h.Ltrtcmixlev = uint8(r.Uint32(3))
			h.Cmixlev = uint8(r.Uint32(3))
			h.HasCmixlev = true
		}
		if h.Sync.Acmod&4 != 0 { // a surround channel
			h.Ltrtsurmixlev = clampSurroundGainLevel(uint8(r.Uint32(3)))
			h.Surmixlev = clampSurroundGainLevel(uint8(r.Uint32(3)))
			h.HasSurmixlev = true
		}
	}

	if h.Sync.Lfeon {
		h.Lfemixlevcode = r.Bool()
		if h.Lfemixlevcode {
			h.Lfemixlevcod = uint8(r.Uint32(5))
		}
	}

	if h.Sync.Strmtyp == StrmtypIndependent {
		nprog := 1
		if h.Sync.Acmod == AcmodDualMono {
			nprog = 2
		}
		for range nprog {
			if r.Bool() { // pgmscle
				r.Skip(6)
			}
		}
		if r.Bool() { // extpgmscle
			r.Skip(6)
		}
		// mixdef: how much mixing configuration follows, if any.
		switch r.Uint32(2) {
		case 1:
			r.Skip(5)
		case 2:
			r.Skip(12)
		case 3:
			r.Skip((int(r.Uint32(5)) + 2) * 8)
		}
		// Pan information, for a source that is one channel or two unrelated
		// ones and so has a direction rather than a stage.
		if h.Sync.Acmod < AcmodStereo {
			nprog := 1
			if h.Sync.Acmod == AcmodDualMono {
				nprog = 2
			}
			for range nprog {
				if r.Bool() { // paninfoe
					r.Skip(14)
				}
			}
		}
		// A gain per block, so that a mix can move within a frame.
		if r.Bool() { // frmmixcfginfoe
			for range h.Sync.NumBlocks {
				if h.Sync.NumBlocks == 1 || r.Bool() {
					r.Skip(5)
				}
			}
		}
	}
	return nil
}

// parseEAC3InfoMetadata reads the informational metadata (clause E.1.3.1.21 and
// on): what kind of service this is and what was done to it, none of which
// changes the decode.
func (h *Header) parseEAC3InfoMetadata(r *bitstream.Reader) {
	h.Infomdate = r.Bool()
	if !h.Infomdate {
		return
	}

	h.Bsmod = uint8(r.Uint32(3))
	h.Copyrightb = r.Bool()
	h.Origbs = r.Bool()

	if h.Sync.Acmod == AcmodStereo {
		h.Dsurmod = uint8(r.Uint32(2))
		h.HasDsurmod = true
		h.Dheadphonmod = uint8(r.Uint32(2))
	} else if h.Sync.Acmod >= Acmod2F2R {
		h.Dsurexmod = uint8(r.Uint32(2))
	}

	nprog := 1
	if h.Sync.Acmod == AcmodDualMono {
		nprog = 2
	}
	for i := range nprog {
		audprodie := r.Bool()
		var mixlevel, roomtyp uint8
		var adconvtyp bool
		if audprodie {
			mixlevel = uint8(r.Uint32(5))
			roomtyp = uint8(r.Uint32(2))
			adconvtyp = r.Bool()
		}
		if i == 0 {
			h.Audprodie, h.Mixlevel, h.Roomtyp, h.Adconvtyp = audprodie, mixlevel, roomtyp, adconvtyp
		} else {
			h.Audprodi2e, h.Mixlevel2, h.Roomtyp2 = audprodie, mixlevel, roomtyp
		}
	}

	// The rate the source was sampled at before it was halved. A reduced rate
	// frame has already spent this field on fscod2.
	if !h.Sync.HasFscod2 {
		h.Sourcefscod = r.Bool()
	}
}
