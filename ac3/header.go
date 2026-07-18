// Package ac3 decodes AC-3 audio as specified in ETSI TS 102 366.
//
// This package parses the two headers every syncframe opens with: syncinfo,
// which says how long the frame is and at what rate it plays, and the bit
// stream information, which says how many channels it carries and how to mix
// and level them. Field names follow the spec, so the code reads next to the
// document.
//
// An AC-3 syncframe is self contained on the bit stream side: it needs no
// state from the frame before it to be parsed. Nothing here allocates once the
// caller supplies a Header to fill, so a stream can be walked with a single
// Header value reused frame after frame.
package ac3

import (
	"encoding/binary"

	"github.com/gravity-zero/ac3go/bitstream"
	"github.com/gravity-zero/ac3go/pcm"
)

// Syncword opens every AC-3 syncframe.
const Syncword = 0x0B77

// Frame geometry. Every AC-3 frame carries six audio blocks of 256 samples per
// channel, whatever the rate or the channel count, so a frame is always 1536
// samples long on the time line.
const (
	SyncInfoSize    = 5 // bytes: syncword, crc1, fscod, frmsizecod
	BlocksPerFrame  = 6
	SamplesPerBlock = 256
	SamplesPerFrame = BlocksPerFrame * SamplesPerBlock

	// MinFrameSize and MaxFrameSize bound frame sizes over every valid
	// combination of fscod and frmsizecod.
	MinFrameSize = 128  // 32 kbit/s at 48 kHz
	MaxFrameSize = 3840 // 640 kbit/s at 32 kHz

	// MaxEAC3FrameSize is the largest frame the enhanced syntax can state.
	// frmsiz is eleven bits counting 16 bit words from zero, so a frame runs to
	// 2048 words - which is more than any AC-3 frame can be, and is why the
	// reader sizes its buffer from this rather than from MaxFrameSize.
	MaxEAC3FrameSize = 4096

	// MaxAnyFrameSize is the largest frame of either syntax: what a reader has
	// to be able to hold before it can know which syntax it is holding.
	MaxAnyFrameSize = MaxEAC3FrameSize

	// MaxBSID is the highest bit stream version this package decodes. The spec
	// requires an AC-3 decoder to accept every bsid up to 8; 6 selects the
	// alternate bit stream syntax and 8 is what encoders emit. Enhanced AC-3
	// announces 16 and is not handled here.
	MaxBSID = 8

	// EAC3SyncInfoSize is how many bytes of an enhanced AC-3 frame have to be
	// in hand before its size is known. It is one more than an AC-3 frame
	// needs: bsid runs to bit 44, and an E-AC-3 frame cannot be measured until
	// bsid has said it is one.
	EAC3SyncInfoSize = 6

	// AltBSID is the bsid that selects the alternate bit stream syntax, which
	// replaces the time code fields with the extended mix level fields.
	AltBSID = 6

	// maxAddBSIBytes is the largest additional bit stream information field,
	// addbsil being 6 bits wide and counting from zero.
	maxAddBSIBytes = 64
)

// SyncInfo is the syncinfo field of a syncframe: the first five bytes, which
// are enough to cut the frame out of a stream.
// The two syntaxes overlap only in part, and the fields say which they belong
// to. An AC-3 frame leaves the enhanced fields zero; an enhanced one leaves
// CRC1 and Frmsizecod zero, since it has neither.
type SyncInfo struct {
	// Bsid is here rather than in the bit stream information because the frame
	// cannot be cut out of a stream without it: it says which of the two
	// syntaxes the bytes around it are in, and the two measure a frame
	// differently. It sits at the same bit of both, which is what makes
	// reading it first possible.
	Bsid uint8

	CRC1       uint16 // check word over the first 5/8 of the frame. AC-3 only.
	Fscod      uint8  // sample rate code
	Frmsizecod uint8  // frame size code. AC-3 only.

	SampleRate int // Hz, decoded from fscod, halved when HasFscod2
	FrameSize  int // bytes of the whole frame, syncword included
	BitRate    int // bit/s: what AC-3 announces, what E-AC-3 works out to
	NumBlocks  int // audio blocks in the frame: always 6 in AC-3, 1 to 6 in E-AC-3

	// Enhanced AC-3 only (clause E.1.3.1). These sit in syncinfo rather than in
	// the bit stream information because, unlike AC-3, an enhanced frame states
	// what it is and how long it is before it states anything else, and the
	// fields below are what it states.
	Strmtyp     uint8 // what part this frame plays in the stream
	Substreamid uint8 // which substream it belongs to
	Numblkscod  uint8 // block count code, unused when HasFscod2
	Acmod       uint8 // audio coding mode: in syncinfo here, in the BSI in AC-3
	Lfeon       bool

	HasFscod2 bool
	Fscod2    uint8 // the reduced rate code, present when Fscod is 3
}

// Header is syncinfo plus the bit stream information of one syncframe.
//
// Optional fields come with the flag that gated them: a zero Compr means
// nothing unless Compre is set. Fields whose presence depends on acmod
// (Cmixlev, Surmixlev, Dsurmod) carry a Has flag for the same reason.
type Header struct {
	Sync SyncInfo

	Bsmod uint8 // bit stream mode: what kind of service this is
	Acmod uint8 // audio coding mode: which channels are coded

	// The downmix levels, and the one place the two syntaxes disagree about
	// what a field means rather than about where it is. In AC-3 these are the
	// two bit codes of tables 4.16 and 4.17; in E-AC-3 they are three bit
	// indices straight into the gain levels, and they default to -4.5 and -6 dB
	// rather than to nothing when the frame states none. CenterMixLevel and
	// SurroundMixLevel are the way to read them: they know which.
	HasCmixlev bool
	Cmixlev    uint8 // centre downmix level, present when three front channels are coded

	HasSurmixlev bool
	Surmixlev    uint8 // surround downmix level, present when surrounds are coded

	HasDsurmod bool
	Dsurmod    uint8 // surround encode mode, present in 2/0 only

	Lfeon    bool  // low frequency effects channel present
	Dialnorm uint8 // dialogue normalization, 1..31 meaning -1..-31 dB

	Compre bool
	Compr  uint8 // compression gain word

	Langcode bool
	Langcod  uint8 // language code, deprecated by the spec

	Audprodie bool
	Mixlevel  uint8 // peak mixing level, 80 + Mixlevel dB SPL
	Roomtyp   uint8 // mixing room type

	// Second program of the dual mono mode (acmod 0), which carries two
	// independent services that happen to share a frame.
	Dialnorm2  uint8
	Compr2e    bool
	Compr2     uint8
	Langcod2e  bool
	Langcod2   uint8
	Audprodi2e bool
	Mixlevel2  uint8
	Roomtyp2   uint8

	Copyrightb bool // copyrighted material
	Origbs     bool // original bit stream rather than a copy

	// Time codes, present in the normal syntax only (Bsid != AltBSID).
	Timecod1e bool
	Timecod1  uint16
	Timecod2e bool
	Timecod2  uint16

	// Extended mix levels, present in the alternate syntax only
	// (Bsid == AltBSID). They give a downmixer finer levels than
	// Cmixlev and Surmixlev do, per downmix target.
	Xbsi1e        bool
	Dmixmod       uint8 // preferred downmix mode
	Ltrtcmixlev   uint8
	Ltrtsurmixlev uint8
	Lorocmixlev   uint8
	Lorosurmixlev uint8

	Xbsi2e       bool
	Dsurexmod    uint8 // surround EX encode mode
	Dheadphonmod uint8 // headphone encode mode
	Adconvtyp    bool  // HDCD converter used on the source
	Xbsi2        uint8 // reserved for future assignment
	Encinfo      bool  // reserved for the encoder's private use

	Addbsie bool
	addbsi  [maxAddBSIBytes]byte
	addbsil uint8 // bytes of addbsi, minus one

	// Enhanced AC-3 only (clause E.1.3.1). Every one of these is metadata: they
	// say what to do with the programme, not how to decode it.
	Chanmape      bool
	Chanmap       uint16 // which channels a dependent substream carries
	Mixmdate      bool   // mixing metadata present
	Lfemixlevcode bool
	Lfemixlevcod  uint8
	Infomdate     bool // informational metadata present
	Sourcefscod   bool // the source was sampled at twice this rate
	Convsync      bool // start of a set of frames, for a converter
	Blkid         bool // the frame size code below is present
	Frmsizecod    uint8

	// AudioStartBit is the bit offset within the frame of the first audio
	// block, that is the first bit past the bit stream information.
	AudioStartBit int
}

// acmodLayouts and acmodLayoutsLFE give the coded channel order of each audio
// coding mode. They are shared, read-only values so that Layout costs nothing
// on the per-frame path; a caller that intends to modify one must copy it.
var acmodLayouts = [8]pcm.Layout{
	AcmodDualMono: {pcm.ChannelCh1, pcm.ChannelCh2},
	AcmodMono:     {pcm.ChannelCenter},
	AcmodStereo:   {pcm.ChannelLeft, pcm.ChannelRight},
	Acmod3F:       {pcm.ChannelLeft, pcm.ChannelCenter, pcm.ChannelRight},
	Acmod2F1R:     {pcm.ChannelLeft, pcm.ChannelRight, pcm.ChannelMonoSurround},
	Acmod3F1R:     {pcm.ChannelLeft, pcm.ChannelCenter, pcm.ChannelRight, pcm.ChannelMonoSurround},
	Acmod2F2R:     {pcm.ChannelLeft, pcm.ChannelRight, pcm.ChannelLeftSurround, pcm.ChannelRightSurround},
	Acmod3F2R:     {pcm.ChannelLeft, pcm.ChannelCenter, pcm.ChannelRight, pcm.ChannelLeftSurround, pcm.ChannelRightSurround},
}

var acmodLayoutsLFE = func() (out [8]pcm.Layout) {
	for i, l := range acmodLayouts {
		out[i] = l.WithLFE()
	}
	return out
}()

// ParseSyncInfo fills si from the head of b and derives the frame size, sample
// rate and bit rate. It does not need the whole frame - six bytes are enough
// for either syntax.
//
// Which syntax it reads is decided by bsid, which both put at the same bit for
// exactly this reason: nothing before bsid can be trusted to mean what it looks
// like until bsid has been read, so it is read first and everything else after.
func ParseSyncInfo(b []byte, si *SyncInfo) error {
	if len(b) < EAC3SyncInfoSize {
		return shortFrameError(len(b), EAC3SyncInfoSize)
	}
	switch binary.BigEndian.Uint16(b) {
	case Syncword:
	case 0x770B:
		return wrap(ErrByteOrder)
	default:
		return wrap(ErrNoSync)
	}

	*si = SyncInfo{Bsid: peekBSID(b)}
	if isEAC3(si.Bsid) {
		return parseEAC3SyncInfo(b, si)
	}
	if si.Bsid > MaxBSID {
		return unsupportedBSID(si.Bsid)
	}

	si.NumBlocks = BlocksPerFrame
	si.CRC1 = binary.BigEndian.Uint16(b[2:])
	si.Fscod = b[4] >> 6
	si.Frmsizecod = b[4] & 0x3F

	if si.Fscod > 2 {
		return reservedError("fscod", uint32(si.Fscod))
	}
	if int(si.Frmsizecod) >= len(frameSizes) {
		return reservedError("frmsizecod", uint32(si.Frmsizecod))
	}
	si.SampleRate = sampleRates[si.Fscod]
	si.FrameSize = int(frameSizes[si.Frmsizecod][si.Fscod]) * 2
	si.BitRate = bitRates[si.Frmsizecod>>1] * 1000
	return nil
}

// ParseHeader fills h with the syncinfo and the bit stream information found
// at the start of frame. frame may hold more than one frame's worth of bytes;
// parsing never reads past the frame boundary syncinfo announces.
//
// It does not verify the check words: call CheckCRC for that. It does not
// allocate.
func ParseHeader(frame []byte, h *Header) error {
	if err := ParseSyncInfo(frame, &h.Sync); err != nil {
		return err
	}
	// Never let the bit reader wander into whatever follows this frame.
	if len(frame) > h.Sync.FrameSize {
		frame = frame[:h.Sync.FrameSize]
	}

	var r bitstream.Reader
	r.Reset(frame)
	r.Skip(SyncInfoSize * 8)

	// bsid is not read again here: ParseSyncInfo read it, and it decided which
	// of the two syntaxes these bytes are in.
	if isEAC3(h.Sync.Bsid) {
		h.Acmod = h.Sync.Acmod
		h.Lfeon = h.Sync.Lfeon
		r.SeekBit(bsidBit + 5)
		return h.parseEAC3BSI(&r)
	}

	r.Skip(5) // bsid
	h.Bsmod = uint8(r.Uint32(3))
	h.Acmod = uint8(r.Uint32(3))
	if r.Err() != nil {
		// bsid, bsmod and acmod end 51 bits in: the fixed head of the BSI
		// needs a seventh byte.
		return shortFrameError(len(frame), SyncInfoSize+2)
	}

	// Three front channels are coded when the low bit of acmod is set, except
	// in the 1/0 mode which has a centre and nothing to mix it into.
	h.HasCmixlev = h.Acmod&0x1 != 0 && h.Acmod != AcmodMono
	if h.HasCmixlev {
		h.Cmixlev = uint8(r.Uint32(2))
	} else {
		h.Cmixlev = 0
	}
	h.HasSurmixlev = h.Acmod&0x4 != 0
	if h.HasSurmixlev {
		h.Surmixlev = uint8(r.Uint32(2))
	} else {
		h.Surmixlev = 0
	}
	h.HasDsurmod = h.Acmod == AcmodStereo
	if h.HasDsurmod {
		h.Dsurmod = uint8(r.Uint32(2))
	} else {
		h.Dsurmod = 0
	}

	h.Lfeon = r.Bool()
	h.Dialnorm = uint8(r.Uint32(5))

	h.Compre = r.Bool()
	h.Compr = 0
	if h.Compre {
		h.Compr = uint8(r.Uint32(8))
	}
	h.Langcode = r.Bool()
	h.Langcod = 0
	if h.Langcode {
		h.Langcod = uint8(r.Uint32(8))
	}
	h.Audprodie = r.Bool()
	h.Mixlevel, h.Roomtyp = 0, 0
	if h.Audprodie {
		h.Mixlevel = uint8(r.Uint32(5))
		h.Roomtyp = uint8(r.Uint32(2))
	}

	h.Dialnorm2, h.Compr2e, h.Compr2 = 0, false, 0
	h.Langcod2e, h.Langcod2 = false, 0
	h.Audprodi2e, h.Mixlevel2, h.Roomtyp2 = false, 0, 0
	if h.Acmod == AcmodDualMono {
		h.Dialnorm2 = uint8(r.Uint32(5))
		h.Compr2e = r.Bool()
		if h.Compr2e {
			h.Compr2 = uint8(r.Uint32(8))
		}
		h.Langcod2e = r.Bool()
		if h.Langcod2e {
			h.Langcod2 = uint8(r.Uint32(8))
		}
		h.Audprodi2e = r.Bool()
		if h.Audprodi2e {
			h.Mixlevel2 = uint8(r.Uint32(5))
			h.Roomtyp2 = uint8(r.Uint32(2))
		}
	}

	h.Copyrightb = r.Bool()
	h.Origbs = r.Bool()

	h.Timecod1e, h.Timecod1, h.Timecod2e, h.Timecod2 = false, 0, false, 0
	h.Xbsi1e, h.Dmixmod = false, 0
	h.Ltrtcmixlev, h.Ltrtsurmixlev, h.Lorocmixlev, h.Lorosurmixlev = 0, 0, 0, 0
	h.Xbsi2e, h.Dsurexmod, h.Dheadphonmod = false, 0, 0
	h.Adconvtyp, h.Xbsi2, h.Encinfo = false, 0, false
	if h.Sync.Bsid == AltBSID {
		h.Xbsi1e = r.Bool()
		if h.Xbsi1e {
			h.Dmixmod = uint8(r.Uint32(2))
			h.Ltrtcmixlev = uint8(r.Uint32(3))
			h.Ltrtsurmixlev = uint8(r.Uint32(3))
			h.Lorocmixlev = uint8(r.Uint32(3))
			h.Lorosurmixlev = uint8(r.Uint32(3))
		}
		h.Xbsi2e = r.Bool()
		if h.Xbsi2e {
			h.Dsurexmod = uint8(r.Uint32(2))
			h.Dheadphonmod = uint8(r.Uint32(2))
			h.Adconvtyp = r.Bool()
			h.Xbsi2 = uint8(r.Uint32(8))
			h.Encinfo = r.Bool()
		}
	} else {
		h.Timecod1e = r.Bool()
		if h.Timecod1e {
			h.Timecod1 = uint16(r.Uint32(14))
		}
		h.Timecod2e = r.Bool()
		if h.Timecod2e {
			h.Timecod2 = uint16(r.Uint32(14))
		}
	}

	h.Addbsie = r.Bool()
	h.addbsil = 0
	if h.Addbsie {
		h.addbsil = uint8(r.Uint32(6))
		for i := 0; i <= int(h.addbsil); i++ {
			h.addbsi[i] = byte(r.Uint32(8))
		}
	}

	if err := r.Err(); err != nil {
		// The BSI ran off the end of the buffer, so however many bytes are
		// there, at least one more was needed.
		return shortFrameError(len(frame), len(frame)+1)
	}
	h.AudioStartBit = r.BitPos()
	return nil
}

// AddBSI returns the additional bit stream information bytes, or nil when the
// frame carries none. The slice points into h and stays valid until h is
// parsed into again.
//
// The spec reserves the contents for the encoder: a decoder must skip what it
// does not recognise rather than reject the frame.
func (h *Header) AddBSI() []byte {
	if !h.Addbsie {
		return nil
	}
	return h.addbsi[:int(h.addbsil)+1]
}

// FullBandwidthChannels returns the number of coded channels excluding the LFE.
func (h *Header) FullBandwidthChannels() int { return acmodChannels[h.Acmod&7] }

// Channels returns the number of coded channels, the LFE included.
func (h *Header) Channels() int {
	n := h.FullBandwidthChannels()
	if h.Lfeon {
		n++
	}
	return n
}

// Layout returns the coded channel order of the frame. The returned slice is
// shared and read-only; copy it before modifying it.
func (h *Header) Layout() pcm.Layout {
	if h.Lfeon {
		return acmodLayoutsLFE[h.Acmod&7]
	}
	return acmodLayouts[h.Acmod&7]
}

// Format returns the sample rate and channel layout the frame decodes to.
func (h *Header) Format() pcm.Format {
	return pcm.Format{SampleRate: h.Sync.SampleRate, Layout: h.Layout()}
}

// AcmodName returns the spec's short name of the audio coding mode, such as
// "3/2" or "1+1".
func (h *Header) AcmodName() string { return acmodNames[h.Acmod&7] }

// BsmodName describes the service the frame carries. bsmod 7 names two
// different services depending on the channel count, which is why this reads
// acmod as well.
func (h *Header) BsmodName() string {
	if h.Bsmod == 7 {
		if h.FullBandwidthChannels() == 1 {
			return "associated service: voice over (VO)"
		}
		return "main audio service: karaoke"
	}
	return bsmodNames[h.Bsmod&7]
}

// RoomType describes the mixing room the material was mastered in. It is
// meaningful only when Audprodie is set.
func (h *Header) RoomType() string { return roomTypes[h.Roomtyp&3] }

// DialnormDB returns the dialogue normalization level in dB, always negative.
// It is how far below full scale the average dialogue of the programme sits;
// a decoder attenuates by -31 - DialnormDB to bring every programme out at the
// same loudness.
//
// The spec reserves dialnorm 0 and tells decoders to treat it as -31 dB, which
// makes it a no-op rather than an error.
func (h *Header) DialnormDB() int {
	if h.Dialnorm == 0 || h.Dialnorm > 31 {
		return -31
	}
	return -int(h.Dialnorm)
}

// Dialnorm2DB is DialnormDB for the second programme of the dual mono mode.
func (h *Header) Dialnorm2DB() int {
	if h.Dialnorm2 == 0 || h.Dialnorm2 > 31 {
		return -31
	}
	return -int(h.Dialnorm2)
}

// CenterMixLevel returns the gain to apply to the centre channel when mixing
// it into left and right. It is 1 when the frame codes no centre channel.
//
// The reserved code falls back to the intermediate -4.5 dB, which is what
// clause 4.4.2.4 recommends ("the intermediate value of cmixlev may be used in
// this case") and what the reference decoder does. The spec defines no default
// here, so a stream carrying the reserved code is the encoder's mistake either
// way; matching the reference is what keeps the downmix aligned with it.
func (h *Header) CenterMixLevel() float32 {
	// The enhanced syntax states an index into the gain levels rather than one
	// of three codes, and it states a default rather than nothing when it says
	// nothing, so Cmixlev is already the answer either way it got there.
	if isEAC3(h.Sync.Bsid) {
		return gainLevels[h.Cmixlev&7]
	}
	if !h.HasCmixlev {
		return 1
	}
	if h.Cmixlev > 2 {
		return centerMixLevels[1]
	}
	return centerMixLevels[h.Cmixlev]
}

// SurroundMixLevel returns the gain to apply to the surround channels in a
// downmix. It is 1 when the frame codes no surround channel.
//
// The reserved code falls back to -6 dB, per clause 4.4.2.5 and the reference
// decoder. See CenterMixLevel.
func (h *Header) SurroundMixLevel() float32 {
	if isEAC3(h.Sync.Bsid) {
		return gainLevels[h.Surmixlev&7]
	}
	if !h.HasSurmixlev {
		return 1
	}
	if h.Surmixlev > 2 {
		return surroundMixLevels[1]
	}
	return surroundMixLevels[h.Surmixlev]
}
