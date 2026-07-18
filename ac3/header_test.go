package ac3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// bsid8 is what an AC-3 encoder writes, placed where ParseSyncInfo reads it:
// the top five bits of the sixth byte. Every AC-3 case below carries it, since
// without a bsid there is nothing to say the bytes before it are AC-3 at all.
const bsid8 = 8 << 3

func TestParseSyncInfo(t *testing.T) {
	tests := []struct {
		name       string
		in         []byte
		wantErr    error
		fscod      uint8
		frmsizecod uint8
		crc1       uint16
		rate       int
		size       int
		bitrate    int
	}{
		{
			name: "48 kHz 448 kbit/s",
			in:   []byte{0x0B, 0x77, 0x12, 0x34, 0x1E, bsid8},
			crc1: 0x1234, fscod: 0, frmsizecod: 30, rate: 48000, size: 1792, bitrate: 448000,
		},
		{
			name:  "44.1 kHz, even frmsizecod",
			in:    []byte{0x0B, 0x77, 0x00, 0x00, 0x54, bsid8},
			fscod: 1, frmsizecod: 20, rate: 44100, size: 834, bitrate: 192000,
		},
		{
			name:  "44.1 kHz, odd frmsizecod is one word longer",
			in:    []byte{0x0B, 0x77, 0x00, 0x00, 0x55, bsid8},
			fscod: 1, frmsizecod: 21, rate: 44100, size: 836, bitrate: 192000,
		},
		{
			name:  "32 kHz, smallest frame",
			in:    []byte{0x0B, 0x77, 0x00, 0x00, 0x80, bsid8},
			fscod: 2, frmsizecod: 0, rate: 32000, size: 192, bitrate: 32000,
		},
		{
			name:  "largest frame",
			in:    []byte{0x0B, 0x77, 0x00, 0x00, 0xA5, bsid8},
			fscod: 2, frmsizecod: 37, rate: 32000, size: 3840, bitrate: 640000,
		},
		{
			name:  "smallest frame",
			in:    []byte{0x0B, 0x77, 0x00, 0x00, 0x00, bsid8},
			fscod: 0, frmsizecod: 0, rate: 48000, size: 128, bitrate: 32000,
		},

		{name: "empty", in: nil, wantErr: ErrShortFrame},
		{name: "four bytes", in: []byte{0x0B, 0x77, 0x00, 0x00}, wantErr: ErrShortFrame},
		// Five is enough for the AC-3 syncinfo field but not to read bsid,
		// and without bsid there is no telling which syntax the five are in.
		{name: "five bytes", in: []byte{0x0B, 0x77, 0x00, 0x00, 0x00}, wantErr: ErrShortFrame},
		{name: "no syncword", in: []byte{0xDE, 0xAD, 0x00, 0x00, 0x00, 0x00}, wantErr: ErrNoSync},
		{name: "byte-swapped syncword", in: []byte{0x77, 0x0B, 0x00, 0x00, 0x00, 0x00}, wantErr: ErrByteOrder},
		{name: "fscod reserved", in: []byte{0x0B, 0x77, 0x00, 0x00, 0xC0, bsid8}, wantErr: ErrReserved},
		{name: "frmsizecod reserved", in: []byte{0x0B, 0x77, 0x00, 0x00, 0x26, bsid8}, wantErr: ErrReserved},
		{name: "frmsizecod at the top of the range", in: []byte{0x0B, 0x77, 0x00, 0x00, 0x3F, bsid8}, wantErr: ErrReserved},
		{name: "bsid 9 is AC-3 at a rate this decoder does not do", in: []byte{0x0B, 0x77, 0x00, 0x00, 0x00, 9 << 3}, wantErr: ErrUnsupportedBSID},
		{name: "bsid past enhanced AC-3", in: []byte{0x0B, 0x77, 0x00, 0x00, 0x00, 24 << 3}, wantErr: ErrUnsupportedBSID},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var si SyncInfo
			err := ParseSyncInfo(tt.in, &si)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("ParseSyncInfo = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if si.Fscod != tt.fscod {
				t.Errorf("Fscod = %d, want %d", si.Fscod, tt.fscod)
			}
			if si.Frmsizecod != tt.frmsizecod {
				t.Errorf("Frmsizecod = %d, want %d", si.Frmsizecod, tt.frmsizecod)
			}
			if si.CRC1 != tt.crc1 {
				t.Errorf("CRC1 = %#04x, want %#04x", si.CRC1, tt.crc1)
			}
			if si.SampleRate != tt.rate {
				t.Errorf("SampleRate = %d, want %d", si.SampleRate, tt.rate)
			}
			if si.FrameSize != tt.size {
				t.Errorf("FrameSize = %d, want %d", si.FrameSize, tt.size)
			}
			if si.BitRate != tt.bitrate {
				t.Errorf("BitRate = %d, want %d", si.BitRate, tt.bitrate)
			}
		})
	}
}

// TestFrameSizeBounds pins the MinFrameSize and MaxFrameSize constants to the
// table rather than to a comment.
func TestFrameSizeBounds(t *testing.T) {
	minSize, maxSize := 1<<30, 0
	for frmsizecod := range frameSizes {
		for fscod := range 3 {
			size := int(frameSizes[frmsizecod][fscod]) * 2
			minSize = min(minSize, size)
			maxSize = max(maxSize, size)
		}
	}
	if minSize != MinFrameSize {
		t.Errorf("MinFrameSize = %d, table says %d", MinFrameSize, minSize)
	}
	if maxSize != MaxFrameSize {
		t.Errorf("MaxFrameSize = %d, table says %d", MaxFrameSize, maxSize)
	}
}

// TestBitRateMatchesFrameSize cross-checks the two tables against each other:
// a 48 kHz frame is exactly 32 ms, so its size must follow from its bit rate.
func TestBitRateMatchesFrameSize(t *testing.T) {
	for frmsizecod := range frameSizes {
		bitrate := bitRates[frmsizecod>>1] * 1000
		want := bitrate * SamplesPerFrame / 48000 / 8
		got := int(frameSizes[frmsizecod][0]) * 2
		if got != want {
			t.Errorf("frmsizecod %d: frame size %d B, but %d kbit/s over 32 ms is %d B",
				frmsizecod, got, bitrate/1000, want)
		}
	}
}

func TestParseHeaderBSI(t *testing.T) {
	tests := []struct {
		name  string
		spec  bsiSpec
		check func(t *testing.T, h *Header)
	}{
		{
			name: "stereo defaults",
			spec: defaultBSI(),
			check: func(t *testing.T, h *Header) {
				if h.Acmod != AcmodStereo {
					t.Errorf("Acmod = %d, want %d", h.Acmod, AcmodStereo)
				}
				if h.HasCmixlev {
					t.Error("HasCmixlev = true on a 2/0 frame, want false")
				}
				if h.HasSurmixlev {
					t.Error("HasSurmixlev = true on a 2/0 frame, want false")
				}
				if !h.HasDsurmod {
					t.Error("HasDsurmod = false on a 2/0 frame, want true")
				}
				if got, want := h.Layout().String(), "L,R"; got != want {
					t.Errorf("Layout = %s, want %s", got, want)
				}
				if got, want := h.Channels(), 2; got != want {
					t.Errorf("Channels = %d, want %d", got, want)
				}
			},
		},
		{
			name: "mono has no cmixlev: nothing to mix a centre into",
			spec: func() bsiSpec { s := defaultBSI(); s.acmod = AcmodMono; return s }(),
			check: func(t *testing.T, h *Header) {
				if h.HasCmixlev {
					t.Error("HasCmixlev = true on a 1/0 frame, want false")
				}
				if h.HasDsurmod {
					t.Error("HasDsurmod = true on a 1/0 frame, want false")
				}
				if got, want := h.CenterMixLevel(), float32(1); got != want {
					t.Errorf("CenterMixLevel = %v, want %v", got, want)
				}
				if got, want := h.Layout().String(), "C"; got != want {
					t.Errorf("Layout = %s, want %s", got, want)
				}
			},
		},
		{
			name: "3/0 has cmixlev but no surmixlev",
			spec: func() bsiSpec { s := defaultBSI(); s.acmod, s.cmixlev = Acmod3F, 2; return s }(),
			check: func(t *testing.T, h *Header) {
				if !h.HasCmixlev {
					t.Fatal("HasCmixlev = false on a 3/0 frame, want true")
				}
				if h.HasSurmixlev {
					t.Error("HasSurmixlev = true on a 3/0 frame, want false")
				}
				if got, want := h.CenterMixLevel(), float32(0.5); got != want {
					t.Errorf("CenterMixLevel = %v, want %v", got, want)
				}
			},
		},
		{
			name: "2/2 has surmixlev but no cmixlev",
			spec: func() bsiSpec { s := defaultBSI(); s.acmod, s.surmixlev = Acmod2F2R, 1; return s }(),
			check: func(t *testing.T, h *Header) {
				if h.HasCmixlev {
					t.Error("HasCmixlev = true on a 2/2 frame, want false")
				}
				if !h.HasSurmixlev {
					t.Fatal("HasSurmixlev = false on a 2/2 frame, want true")
				}
				if got, want := h.SurroundMixLevel(), float32(0.5); got != want {
					t.Errorf("SurroundMixLevel = %v, want %v", got, want)
				}
				if got, want := h.Layout().String(), "L,R,Ls,Rs"; got != want {
					t.Errorf("Layout = %s, want %s", got, want)
				}
			},
		},
		{
			name: "3/2 with LFE reads both mix levels",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.acmod, s.lfeon, s.cmixlev, s.surmixlev = Acmod3F2R, true, 1, 0
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				if !h.HasCmixlev || !h.HasSurmixlev {
					t.Fatalf("HasCmixlev=%v HasSurmixlev=%v, want both true", h.HasCmixlev, h.HasSurmixlev)
				}
				// The exact -4.5 dB and -3 dB, not the three decimals the spec
				// prints for them: 0.595 is what 2^(-4.5/6) rounds to, so the
				// printed table is a rounding of these rather than the other way
				// round. See downmix.go.
				if got, want := h.CenterMixLevel(), float32(levelMinus4Point5dB); got != want {
					t.Errorf("CenterMixLevel = %v, want %v", got, want)
				}
				if got, want := h.SurroundMixLevel(), float32(levelMinus3dB); got != want {
					t.Errorf("SurroundMixLevel = %v, want %v", got, want)
				}
				if got, want := h.Channels(), 6; got != want {
					t.Errorf("Channels = %d, want %d", got, want)
				}
				if got, want := h.FullBandwidthChannels(), 5; got != want {
					t.Errorf("FullBandwidthChannels = %d, want %d", got, want)
				}
				if got, want := h.Layout().String(), "L,C,R,Ls,Rs,LFE"; got != want {
					t.Errorf("Layout = %s, want %s", got, want)
				}
			},
		},
		{
			name: "surmixlev 2 drops the surrounds from a downmix",
			spec: func() bsiSpec { s := defaultBSI(); s.acmod, s.surmixlev = Acmod2F2R, 2; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.SurroundMixLevel(), float32(0); got != want {
					t.Errorf("SurroundMixLevel = %v, want %v", got, want)
				}
			},
		},
		{
			name: "reserved mix levels fall back to the spec's intermediate values",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.acmod, s.lfeon, s.cmixlev, s.surmixlev = Acmod3F2R, true, 3, 3
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.Cmixlev, uint8(3); got != want {
					t.Errorf("Cmixlev = %d, want the raw %d", got, want)
				}
				// Clause 4.4.2.4: -4.5 dB, the intermediate value.
				if got, want := h.CenterMixLevel(), float32(levelMinus4Point5dB); got != want {
					t.Errorf("CenterMixLevel = %v, want %v", got, want)
				}
				// Clause 4.4.2.5: -6 dB.
				if got, want := h.SurroundMixLevel(), float32(levelMinus6dB); got != want {
					t.Errorf("SurroundMixLevel = %v, want %v", got, want)
				}
			},
		},
		{
			name: "dual mono carries a second programme",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.acmod = AcmodDualMono
				s.dialnorm, s.dialnorm2 = 24, 20
				s.compr2e, s.compr2 = true, 0xAB
				s.langcod2e, s.langcod2 = true, 0xCD
				s.audprodi2e, s.mixlevel2, s.roomtyp2 = true, 5, 2
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.DialnormDB(), -24; got != want {
					t.Errorf("DialnormDB = %d, want %d", got, want)
				}
				if got, want := h.Dialnorm2DB(), -20; got != want {
					t.Errorf("Dialnorm2DB = %d, want %d", got, want)
				}
				if !h.Compr2e || h.Compr2 != 0xAB {
					t.Errorf("Compr2e=%v Compr2=%#x, want true 0xab", h.Compr2e, h.Compr2)
				}
				if !h.Langcod2e || h.Langcod2 != 0xCD {
					t.Errorf("Langcod2e=%v Langcod2=%#x, want true 0xcd", h.Langcod2e, h.Langcod2)
				}
				if !h.Audprodi2e || h.Mixlevel2 != 5 || h.Roomtyp2 != 2 {
					t.Errorf("Audprodi2e=%v Mixlevel2=%d Roomtyp2=%d, want true 5 2", h.Audprodi2e, h.Mixlevel2, h.Roomtyp2)
				}
				// The two programmes are not a stereo pair.
				if got, want := h.Layout().String(), "Ch1,Ch2"; got != want {
					t.Errorf("Layout = %s, want %s", got, want)
				}
			},
		},
		{
			name: "every optional field present",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.bsmod = 2
				s.compre, s.compr = true, 0x12
				s.langcode, s.langcod = true, 0x34
				s.audprodie, s.mixlevel, s.roomtyp = true, 25, 1
				s.copyrightb = true
				s.timecod1e, s.timecod1 = true, 0x1234
				s.timecod2e, s.timecod2 = true, 0x0567
				s.addbsi = []byte{0xDE, 0xAD, 0xBE, 0xEF}
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				if !h.Compre || h.Compr != 0x12 {
					t.Errorf("Compre=%v Compr=%#x, want true 0x12", h.Compre, h.Compr)
				}
				if !h.Langcode || h.Langcod != 0x34 {
					t.Errorf("Langcode=%v Langcod=%#x, want true 0x34", h.Langcode, h.Langcod)
				}
				if !h.Audprodie || h.Mixlevel != 25 || h.Roomtyp != 1 {
					t.Errorf("Audprodie=%v Mixlevel=%d Roomtyp=%d, want true 25 1", h.Audprodie, h.Mixlevel, h.Roomtyp)
				}
				if got, want := h.RoomType(), "large room, X curve monitor"; got != want {
					t.Errorf("RoomType = %q, want %q", got, want)
				}
				if !h.Copyrightb || !h.Origbs {
					t.Errorf("Copyrightb=%v Origbs=%v, want both true", h.Copyrightb, h.Origbs)
				}
				if !h.Timecod1e || h.Timecod1 != 0x1234 {
					t.Errorf("Timecod1e=%v Timecod1=%#x, want true 0x1234", h.Timecod1e, h.Timecod1)
				}
				if !h.Timecod2e || h.Timecod2 != 0x0567 {
					t.Errorf("Timecod2e=%v Timecod2=%#x, want true 0x567", h.Timecod2e, h.Timecod2)
				}
				if got, want := h.AddBSI(), []byte{0xDE, 0xAD, 0xBE, 0xEF}; !bytes.Equal(got, want) {
					t.Errorf("AddBSI = %x, want %x", got, want)
				}
			},
		},
		{
			name: "no optional field present",
			spec: defaultBSI(),
			check: func(t *testing.T, h *Header) {
				if h.Compre || h.Langcode || h.Audprodie || h.Timecod1e || h.Timecod2e || h.Addbsie {
					t.Error("an optional field reported present on a bare frame")
				}
				if h.AddBSI() != nil {
					t.Errorf("AddBSI = %x, want nil", h.AddBSI())
				}
				// bsid 8: 40 syncinfo + 11 bsid/bsmod/acmod + 2 dsurmod + 1
				// lfeon + 5 dialnorm + 5 flags + 2 timecod flags + 1 addbsie.
				if got, want := h.AudioStartBit, 67; got != want {
					t.Errorf("AudioStartBit = %d, want %d", got, want)
				}
			},
		},
		{
			name: "addbsi at its maximum length",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.addbsi = make([]byte, maxAddBSIBytes)
				for i := range s.addbsi {
					s.addbsi[i] = byte(i)
				}
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				add := h.AddBSI()
				if got, want := len(add), maxAddBSIBytes; got != want {
					t.Fatalf("len(AddBSI) = %d, want %d", got, want)
				}
				for i, b := range add {
					if b != byte(i) {
						t.Fatalf("AddBSI[%d] = %d, want %d", i, b, i)
					}
				}
			},
		},
		{
			name: "addbsi of one byte",
			spec: func() bsiSpec { s := defaultBSI(); s.addbsi = []byte{0x5A}; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.AddBSI(), []byte{0x5A}; !bytes.Equal(got, want) {
					t.Errorf("AddBSI = %x, want %x", got, want)
				}
			},
		},
		{
			name: "dsurmod is read in 2/0 only",
			spec: func() bsiSpec { s := defaultBSI(); s.dsurmod = DsurmodYes; return s }(),
			check: func(t *testing.T, h *Header) {
				if !h.HasDsurmod || h.Dsurmod != DsurmodYes {
					t.Errorf("HasDsurmod=%v Dsurmod=%d, want true %d", h.HasDsurmod, h.Dsurmod, DsurmodYes)
				}
			},
		},
		{
			name: "alternate syntax carries extended mix levels instead of time codes",
			spec: func() bsiSpec {
				s := defaultBSI()
				s.bsid = AltBSID
				s.xbsi1e = true
				s.dmixmod, s.ltrtcmixlev, s.ltrtsurmixlev, s.lorocmixlev, s.lorosurmixlev = 1, 2, 3, 4, 5
				s.xbsi2e = true
				s.dsurexmod, s.dheadphonmod, s.adconvtyp, s.xbsi2, s.encinfo = 2, 1, true, 0x7E, true
				return s
			}(),
			check: func(t *testing.T, h *Header) {
				if h.Timecod1e || h.Timecod2e {
					t.Error("time codes reported present under the alternate syntax")
				}
				if !h.Xbsi1e {
					t.Fatal("Xbsi1e = false, want true")
				}
				if h.Dmixmod != 1 || h.Ltrtcmixlev != 2 || h.Ltrtsurmixlev != 3 || h.Lorocmixlev != 4 || h.Lorosurmixlev != 5 {
					t.Errorf("xbsi1 = %d %d %d %d %d, want 1 2 3 4 5",
						h.Dmixmod, h.Ltrtcmixlev, h.Ltrtsurmixlev, h.Lorocmixlev, h.Lorosurmixlev)
				}
				if !h.Xbsi2e {
					t.Fatal("Xbsi2e = false, want true")
				}
				if h.Dsurexmod != 2 || h.Dheadphonmod != 1 || !h.Adconvtyp || h.Xbsi2 != 0x7E || !h.Encinfo {
					t.Errorf("xbsi2 = %d %d %v %#x %v, want 2 1 true 0x7e true",
						h.Dsurexmod, h.Dheadphonmod, h.Adconvtyp, h.Xbsi2, h.Encinfo)
				}
			},
		},
		{
			name: "alternate syntax with both extension flags clear",
			spec: func() bsiSpec { s := defaultBSI(); s.bsid = AltBSID; return s }(),
			check: func(t *testing.T, h *Header) {
				if h.Xbsi1e || h.Xbsi2e {
					t.Errorf("Xbsi1e=%v Xbsi2e=%v, want both false", h.Xbsi1e, h.Xbsi2e)
				}
				if h.Timecod1e || h.Timecod2e {
					t.Error("time codes reported present under the alternate syntax")
				}
			},
		},
		{
			name: "dialnorm 0 is reserved and reads as -31 dB",
			spec: func() bsiSpec { s := defaultBSI(); s.dialnorm = 0; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.DialnormDB(), -31; got != want {
					t.Errorf("DialnormDB = %d, want %d", got, want)
				}
			},
		},
		{
			name: "dialnorm 1 is the loudest",
			spec: func() bsiSpec { s := defaultBSI(); s.dialnorm = 1; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.DialnormDB(), -1; got != want {
					t.Errorf("DialnormDB = %d, want %d", got, want)
				}
			},
		},
		{
			name: "bsmod 7 names a different service depending on the channel count",
			spec: func() bsiSpec { s := defaultBSI(); s.bsmod = 7; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.BsmodName(), "main audio service: karaoke"; got != want {
					t.Errorf("BsmodName on 2/0 = %q, want %q", got, want)
				}
			},
		},
		{
			name: "bsmod 7 on a mono frame is voice over",
			spec: func() bsiSpec { s := defaultBSI(); s.bsmod, s.acmod = 7, AcmodMono; return s }(),
			check: func(t *testing.T, h *Header) {
				if got, want := h.BsmodName(), "associated service: voice over (VO)"; got != want {
					t.Errorf("BsmodName on 1/0 = %q, want %q", got, want)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := synth(t, 0, 30, tt.spec)
			var h Header
			if err := ParseHeader(frame, &h); err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if h.Sync.Bsid != tt.spec.bsid {
				t.Errorf("Bsid = %d, want %d", h.Sync.Bsid, tt.spec.bsid)
			}
			tt.check(t, &h)
		})
	}
}

// TestParseHeaderAudioStartBit checks that the parser lands exactly on the
// first audio block, which is what the decode stages will build on.
func TestParseHeaderAudioStartBit(t *testing.T) {
	tests := []struct {
		name string
		spec bsiSpec
		want int
	}{
		// 40 syncinfo + 5 bsid + 3 bsmod + 3 acmod = 51 bits before the
		// acmod-gated fields.
		{"1/0", func() bsiSpec { s := defaultBSI(); s.acmod = AcmodMono; return s }(),
			51 + 0 + 1 + 5 + 1 + 1 + 1 + 2 + 2 + 1},
		{"2/0", defaultBSI(),
			51 + 2 + 1 + 5 + 1 + 1 + 1 + 2 + 2 + 1},
		{"3/2 + LFE", func() bsiSpec {
			s := defaultBSI()
			s.acmod, s.lfeon = Acmod3F2R, true
			return s
		}(), 51 + 2 + 2 + 1 + 5 + 1 + 1 + 1 + 2 + 2 + 1},
		{"1+1", func() bsiSpec { s := defaultBSI(); s.acmod = AcmodDualMono; return s }(),
			51 + 0 + 1 + 5 + 1 + 1 + 1 + (5 + 1 + 1 + 1) + 2 + 2 + 1},
		{"2/0 with addbsi of 4 bytes", func() bsiSpec {
			s := defaultBSI()
			s.addbsi = []byte{1, 2, 3, 4}
			return s
		}(), 51 + 2 + 1 + 5 + 1 + 1 + 1 + 2 + 2 + 1 + 6 + 32},
		{"alternate syntax with both extensions", func() bsiSpec {
			s := defaultBSI()
			s.bsid, s.xbsi1e, s.xbsi2e = AltBSID, true, true
			return s
		}(), 51 + 2 + 1 + 5 + 1 + 1 + 1 + 2 + (1 + 14) + (1 + 14) + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := synth(t, 0, 30, tt.spec)
			var h Header
			if err := ParseHeader(frame, &h); err != nil {
				t.Fatalf("ParseHeader: %v", err)
			}
			if h.AudioStartBit != tt.want {
				t.Errorf("AudioStartBit = %d, want %d", h.AudioStartBit, tt.want)
			}
		})
	}
}

func TestParseHeaderErrors(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())

	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"empty", nil, ErrShortFrame},
		{"syncinfo only", good[:5], ErrShortFrame},
		{"cut inside the bsi", good[:6], ErrShortFrame},
		{"no syncword", append([]byte{0, 0}, good[2:]...), ErrNoSync},
		{"fscod reserved", func() []byte {
			b := bytes.Clone(good)
			b[4] |= 0xC0
			return b
		}(), ErrReserved},
		{"frmsizecod reserved", func() []byte {
			b := bytes.Clone(good)
			b[4] = b[4]&0xC0 | 38
			return b
		}(), ErrReserved},
		// bsid 16 is no longer an error: it announces enhanced AC-3, which this
		// package decodes. What it is here is a lie - the frame around it is
		// AC-3 - and the enhanced parse is what has to notice, which it does
		// because the bits it reads as a frame type read as part of a check
		// word. Which field catches it is not the claim; that something does is.
		{"bsid 17 is past every version there is", synth(t, 0, 30, func() bsiSpec {
			s := defaultBSI()
			s.bsid = 17
			return s
		}()), ErrUnsupportedBSID},
		{"bsid 9 is above what an AC-3 decoder must accept", synth(t, 0, 30, func() bsiSpec {
			s := defaultBSI()
			s.bsid = 9
			return s
		}()), ErrUnsupportedBSID},
		{"bsid 31", synth(t, 0, 30, func() bsiSpec {
			s := defaultBSI()
			s.bsid = 31
			return s
		}()), ErrUnsupportedBSID},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var h Header
			if err := ParseHeader(tt.in, &h); !errors.Is(err, tt.want) {
				t.Errorf("ParseHeader = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestParseHeaderStopsAtTheFrameBoundary guards against a header parse reading
// into the frame that follows it in a buffer.
func TestParseHeaderStopsAtTheFrameBoundary(t *testing.T) {
	// A frame that claims a huge addbsi cannot borrow bytes from its
	// neighbour: the smallest frame is 128 bytes and addbsi can reach 64,
	// so the two overlap only if the parser ignores the boundary.
	spec := defaultBSI()
	spec.addbsi = make([]byte, maxAddBSIBytes)
	for i := range spec.addbsi {
		spec.addbsi[i] = 0xA5
	}
	first := synth(t, 0, 0, spec) // 128 bytes, the smallest frame
	second := synth(t, 0, 0, defaultBSI())
	stream := append(bytes.Clone(first), second...)

	var h Header
	if err := ParseHeader(stream, &h); err != nil {
		t.Fatalf("ParseHeader: %v", err)
	}
	if got, want := h.Sync.FrameSize, 128; got != want {
		t.Fatalf("FrameSize = %d, want %d", got, want)
	}
	for i, b := range h.AddBSI() {
		if b != 0xA5 {
			t.Fatalf("AddBSI[%d] = %#x, want 0xa5", i, b)
		}
	}
	if h.AudioStartBit > 128*8 {
		t.Errorf("AudioStartBit = %d, past the %d-bit frame", h.AudioStartBit, 128*8)
	}
}

func TestParseHeaderNoAllocations(t *testing.T) {
	frame := synth(t, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.acmod, s.lfeon = Acmod3F2R, true
		s.addbsi = []byte{1, 2, 3, 4}
		return s
	}())
	var h Header
	got := testing.AllocsPerRun(200, func() {
		if err := ParseHeader(frame, &h); err != nil {
			t.Fatal(err)
		}
		_ = h.Layout()
		_ = h.Format()
		_ = h.AddBSI()
		_ = h.DialnormDB()
		_ = h.CenterMixLevel()
	})
	if got != 0 {
		t.Errorf("AllocsPerRun = %v, want 0", got)
	}
}

func TestHeaderFormat(t *testing.T) {
	frame := synth(t, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.acmod, s.lfeon = Acmod3F2R, true
		return s
	}())
	var h Header
	if err := ParseHeader(frame, &h); err != nil {
		t.Fatal(err)
	}
	f := h.Format()
	if got, want := f.SampleRate, 48000; got != want {
		t.Errorf("SampleRate = %d, want %d", got, want)
	}
	if got, want := f.Layout.String(), "L,C,R,Ls,Rs,LFE"; got != want {
		t.Errorf("Layout = %s, want %s", got, want)
	}
	if got, want := f.String(), "48000Hz L,C,R,Ls,Rs,LFE"; got != want {
		t.Errorf("Format = %q, want %q", got, want)
	}
}

// TestAcmodTablesAgree keeps the channel counts, names and layouts from
// drifting apart from each other.
func TestAcmodTablesAgree(t *testing.T) {
	for acmod := range uint8(8) {
		layout := acmodLayouts[acmod]
		if got, want := len(layout), acmodChannels[acmod]; got != want {
			t.Errorf("acmod %d (%s): layout has %d channels, table says %d",
				acmod, acmodNames[acmod], got, want)
		}
		if layout.Has(pcm.ChannelLFE) {
			t.Errorf("acmod %d: the base layout carries an LFE", acmod)
		}
		withLFE := acmodLayoutsLFE[acmod]
		if got, want := len(withLFE), len(layout)+1; got != want {
			t.Errorf("acmod %d: LFE layout has %d channels, want %d", acmod, got, want)
		}
		if withLFE[len(withLFE)-1] != pcm.ChannelLFE {
			t.Errorf("acmod %d: LFE is not last in %s", acmod, withLFE)
		}
	}
}

// TestParseHeaderOnFixtures walks the committed synthetic streams: they are
// real encoder output, so they are the check that the parser agrees with what
// an encoder actually writes rather than only with the tests' own writer.
func TestParseHeaderOnFixtures(t *testing.T) {
	tests := []struct {
		file   string
		rate   int
		acmod  uint8
		lfeon  bool
		layout string
	}{
		{"tone_48k_stereo_192k.ac3", 48000, AcmodStereo, false, "L,R"},
		{"tone_48k_mono_96k.ac3", 48000, AcmodMono, false, "C"},
		{"tone_48k_5p1_448k.ac3", 48000, Acmod3F2R, true, "L,C,R,Ls,Rs,LFE"},
		{"tone_44k1_stereo_192k.ac3", 44100, AcmodStereo, false, "L,R"},
		{"tone_32k_stereo_192k.ac3", 32000, AcmodStereo, false, "L,R"},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			fr := NewFrameReader(bytes.NewReader(data))
			frames := 0
			for {
				frame, err := fr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("frame %d: %v", frames, err)
				}
				frames++
				h := fr.Header()
				if h.Sync.SampleRate != tt.rate {
					t.Fatalf("frame %d: SampleRate = %d, want %d", frames, h.Sync.SampleRate, tt.rate)
				}
				if h.Acmod != tt.acmod {
					t.Fatalf("frame %d: Acmod = %d, want %d", frames, h.Acmod, tt.acmod)
				}
				if h.Lfeon != tt.lfeon {
					t.Fatalf("frame %d: Lfeon = %v, want %v", frames, h.Lfeon, tt.lfeon)
				}
				if got := h.Layout().String(); got != tt.layout {
					t.Fatalf("frame %d: Layout = %s, want %s", frames, got, tt.layout)
				}
				if h.Sync.Bsid > MaxBSID {
					t.Fatalf("frame %d: Bsid = %d", frames, h.Sync.Bsid)
				}
				// Both check words must hold on real encoder output.
				if err := CheckCRC(frame); err != nil {
					t.Fatalf("frame %d: %v", frames, err)
				}
			}
			if frames == 0 {
				t.Fatal("no frames found")
			}
			// The fixtures are whole streams: nothing may be skipped, and
			// every byte must land inside a frame.
			if got := fr.Skipped(); got != 0 {
				t.Errorf("Skipped = %d, want 0", got)
			}
		})
	}
}

// TestFixturesCoverTheFrameSizeAlternation pins the reason the 44.1 kHz
// fixture exists: at that rate frames alternate between two sizes.
func TestFixturesCoverTheFrameSizeAlternation(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "tone_44k1_stereo_192k.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	fr := NewFrameReader(bytes.NewReader(data))
	sizes := map[int]int{}
	for {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		sizes[len(frame)]++
	}
	if len(sizes) < 2 {
		t.Fatalf("frame sizes seen: %v, want at least two distinct sizes at 44.1 kHz", sizes)
	}
}

// TestShortFrameErrorsAskForMoreThanTheyHave sweeps every truncation of a
// valid frame and checks that a short-frame error never contradicts itself:
// whatever it says it has, it must say it needs more. "have 6 bytes, need 6"
// once shipped, from a bound that named the field before the one that failed.
func TestShortFrameErrorsAskForMoreThanTheyHave(t *testing.T) {
	eac3, err := os.ReadFile("testdata/tones_48k_stereo_192k.eac3")
	if err != nil {
		t.Fatal(err)
	}
	var si SyncInfo
	if err := ParseSyncInfo(eac3, &si); err != nil {
		t.Fatal(err)
	}
	frames := [][]byte{
		synth(t, 0, 30, defaultBSI()),
		eac3[:si.FrameSize],
	}
	re := regexp.MustCompile(`have (\d+) bytes, need (\d+)`)
	for _, good := range frames {
		for n := 0; n < len(good); n++ {
			var h Header
			err := ParseHeader(good[:n], &h)
			if err == nil || !errors.Is(err, ErrShortFrame) {
				continue
			}
			m := re.FindStringSubmatch(err.Error())
			if m == nil {
				continue // a short-frame error phrased another way
			}
			have, _ := strconv.Atoi(m[1])
			want, _ := strconv.Atoi(m[2])
			if want <= have {
				t.Errorf("cut at %d: %q says it needs no more than it has", n, err)
			}
		}
	}
}
