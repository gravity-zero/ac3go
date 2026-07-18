package ac3

import (
	"encoding/binary"
	"testing"
)

// Synthetic frame construction for the tests. Real encoders only ever emit a
// narrow slice of the syntax: bsid 8, no time codes, no additional bit stream
// information. Building frames by hand is the only way to exercise the rest of
// what the spec allows, and the malformed inputs the parser has to survive.

// bitWriter is the MSB-first counterpart of bitstream.Reader, kept to the
// tests: the decoder never writes a bit stream.
type bitWriter struct {
	buf  []byte
	nbit int
}

// write appends the low n bits of v, most significant first.
func (w *bitWriter) write(v uint32, n uint) {
	for i := int(n) - 1; i >= 0; i-- {
		if w.nbit%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		if v>>uint(i)&1 != 0 {
			w.buf[w.nbit/8] |= 1 << (7 - uint(w.nbit%8))
		}
		w.nbit++
	}
}

func (w *bitWriter) bool(b bool) {
	if b {
		w.write(1, 1)
	} else {
		w.write(0, 1)
	}
}

// bsiSpec describes the bit stream information to write. The zero value is a
// plain 2/0 stereo frame, which is what most tests want to deviate from by one
// field at a time.
type bsiSpec struct {
	bsid  uint8
	bsmod uint8
	acmod uint8

	cmixlev   uint8
	surmixlev uint8
	dsurmod   uint8

	lfeon    bool
	dialnorm uint8

	compre bool
	compr  uint8

	langcode bool
	langcod  uint8

	audprodie bool
	mixlevel  uint8
	roomtyp   uint8

	dialnorm2  uint8
	compr2e    bool
	compr2     uint8
	langcod2e  bool
	langcod2   uint8
	audprodi2e bool
	mixlevel2  uint8
	roomtyp2   uint8

	copyrightb bool
	origbs     bool

	timecod1e bool
	timecod1  uint16
	timecod2e bool
	timecod2  uint16

	xbsi1e        bool
	dmixmod       uint8
	ltrtcmixlev   uint8
	ltrtsurmixlev uint8
	lorocmixlev   uint8
	lorosurmixlev uint8

	xbsi2e       bool
	dsurexmod    uint8
	dheadphonmod uint8
	adconvtyp    bool
	xbsi2        uint8
	encinfo      bool

	addbsi []byte
}

// defaultBSI is a 2/0 stereo frame at bsid 8, the shape encoders emit.
func defaultBSI() bsiSpec {
	return bsiSpec{bsid: 8, acmod: AcmodStereo, dialnorm: 31, origbs: true}
}

// write emits the bsi fields in the order clause 4.4.2 defines them.
func (s bsiSpec) write(w *bitWriter) {
	w.write(uint32(s.bsid), 5)
	w.write(uint32(s.bsmod), 3)
	w.write(uint32(s.acmod), 3)
	if s.acmod&0x1 != 0 && s.acmod != AcmodMono {
		w.write(uint32(s.cmixlev), 2)
	}
	if s.acmod&0x4 != 0 {
		w.write(uint32(s.surmixlev), 2)
	}
	if s.acmod == AcmodStereo {
		w.write(uint32(s.dsurmod), 2)
	}
	w.bool(s.lfeon)
	w.write(uint32(s.dialnorm), 5)
	w.bool(s.compre)
	if s.compre {
		w.write(uint32(s.compr), 8)
	}
	w.bool(s.langcode)
	if s.langcode {
		w.write(uint32(s.langcod), 8)
	}
	w.bool(s.audprodie)
	if s.audprodie {
		w.write(uint32(s.mixlevel), 5)
		w.write(uint32(s.roomtyp), 2)
	}
	if s.acmod == AcmodDualMono {
		w.write(uint32(s.dialnorm2), 5)
		w.bool(s.compr2e)
		if s.compr2e {
			w.write(uint32(s.compr2), 8)
		}
		w.bool(s.langcod2e)
		if s.langcod2e {
			w.write(uint32(s.langcod2), 8)
		}
		w.bool(s.audprodi2e)
		if s.audprodi2e {
			w.write(uint32(s.mixlevel2), 5)
			w.write(uint32(s.roomtyp2), 2)
		}
	}
	w.bool(s.copyrightb)
	w.bool(s.origbs)
	if s.bsid == AltBSID {
		w.bool(s.xbsi1e)
		if s.xbsi1e {
			w.write(uint32(s.dmixmod), 2)
			w.write(uint32(s.ltrtcmixlev), 3)
			w.write(uint32(s.ltrtsurmixlev), 3)
			w.write(uint32(s.lorocmixlev), 3)
			w.write(uint32(s.lorosurmixlev), 3)
		}
		w.bool(s.xbsi2e)
		if s.xbsi2e {
			w.write(uint32(s.dsurexmod), 2)
			w.write(uint32(s.dheadphonmod), 2)
			w.bool(s.adconvtyp)
			w.write(uint32(s.xbsi2), 8)
			w.bool(s.encinfo)
		}
	} else {
		w.bool(s.timecod1e)
		if s.timecod1e {
			w.write(uint32(s.timecod1), 14)
		}
		w.bool(s.timecod2e)
		if s.timecod2e {
			w.write(uint32(s.timecod2), 14)
		}
	}
	w.bool(len(s.addbsi) > 0)
	if len(s.addbsi) > 0 {
		w.write(uint32(len(s.addbsi)-1), 6)
		for _, b := range s.addbsi {
			w.write(uint32(b), 8)
		}
	}
}

// synth builds a whole syncframe: syncinfo, the bsi that spec describes, zero
// padding out to the announced frame size, and both check words fixed up so
// the frame validates.
func synth(t testing.TB, fscod, frmsizecod uint8, spec bsiSpec) []byte {
	t.Helper()

	var w bitWriter
	w.write(Syncword, 16)
	w.write(0, 16) // crc1, filled in below
	w.write(uint32(fscod), 2)
	w.write(uint32(frmsizecod), 6)
	spec.write(&w)

	size := int(frameSizes[frmsizecod][fscod]) * 2
	frame := make([]byte, size)
	if copy(frame, w.buf) < len(w.buf) {
		t.Fatalf("synth: bsi is %d bytes, frame size is %d", len(w.buf), size)
	}
	fixCRC(t, frame)
	return frame
}

// fixCRC rewrites crc1 and crc2 of frame in place.
//
// crc2 sits at the end of the region it covers, so it is just the running CRC
// of everything before it: shifting a CRC's own value back into the register
// clears it. crc1 sits at the *start* of its region, which the spec handles by
// pre-multiplying it at encode time. Tests do not need that machinery: the
// search space is 16 bits and the region is short, so trying every value is
// simpler and provably right.
func fixCRC(t testing.TB, frame []byte) {
	t.Helper()
	split := crcSplit(len(frame))

	// crc2 over frame[split:len-2], stored in the last two bytes.
	binary.BigEndian.PutUint16(frame[len(frame)-2:], crc16(0, frame[split:len(frame)-2]))

	for v := range 1 << 16 {
		binary.BigEndian.PutUint16(frame[2:], uint16(v))
		if crc16(0, frame[2:split]) == 0 {
			return
		}
	}
	t.Fatal("fixCRC: no crc1 value satisfies the frame")
}

func TestSynthFramesValidate(t *testing.T) {
	// The builder is load-bearing for every other test here, so it gets its
	// own: whatever it emits must pass the check the parser applies.
	tests := []struct {
		name       string
		fscod      uint8
		frmsizecod uint8
		spec       bsiSpec
	}{
		{"smallest frame", 0, 0, defaultBSI()},
		{"largest frame", 2, 37, defaultBSI()},
		{"44.1 kHz odd size", 1, 21, defaultBSI()},
		{"5.1", 0, 30, func() bsiSpec {
			s := defaultBSI()
			s.acmod, s.lfeon, s.cmixlev, s.surmixlev = Acmod3F2R, true, 1, 1
			return s
		}()},
		{"with addbsi", 0, 10, func() bsiSpec {
			s := defaultBSI()
			s.addbsi = []byte{1, 2, 3}
			return s
		}()},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			frame := synth(t, tt.fscod, tt.frmsizecod, tt.spec)
			if err := CheckCRC(frame); err != nil {
				t.Fatalf("CheckCRC on a freshly built frame: %v", err)
			}
			var h Header
			if err := ParseHeader(frame, &h); err != nil {
				t.Fatalf("ParseHeader on a freshly built frame: %v", err)
			}
			if got, want := h.Sync.FrameSize, len(frame); got != want {
				t.Errorf("FrameSize = %d, want %d", got, want)
			}
		})
	}
}
