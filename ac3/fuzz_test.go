package ac3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// The parser is the first thing a hostile or damaged stream touches, so the
// bar is: whatever the bytes, it returns an error, it does not panic, and it
// does not report anything a later stage could act on unsafely.

func seedCorpus(f *testing.F) {
	f.Helper()
	f.Add([]byte(nil))
	f.Add([]byte{0x0B, 0x77})
	f.Add([]byte{0x0B, 0x77, 0x00, 0x00, 0x00})
	f.Add([]byte{0x0B, 0x77, 0xFF, 0xFF, 0xFF})
	f.Add(bytes.Repeat([]byte{0x0B, 0x77}, 64))
	f.Add(synth(f, 0, 30, defaultBSI()))
	f.Add(synth(f, 0, 0, defaultBSI()))
	f.Add(synth(f, 1, 21, defaultBSI()))
	f.Add(synth(f, 2, 37, defaultBSI()))
	f.Add(synth(f, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.acmod, s.lfeon = Acmod3F2R, true
		return s
	}()))
	f.Add(synth(f, 0, 30, func() bsiSpec { s := defaultBSI(); s.acmod = AcmodDualMono; return s }()))
	f.Add(synth(f, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.bsid, s.xbsi1e, s.xbsi2e = AltBSID, true, true
		return s
	}()))
	f.Add(synth(f, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.addbsi = make([]byte, maxAddBSIBytes)
		return s
	}()))

	// Real encoder output, which reaches corners a hand-written seed misses.
	for _, pattern := range []string{"*.ac3", "*.eac3"} {
		files, _ := filepath.Glob(filepath.Join("testdata", pattern))
		for _, file := range files {
			if data, err := os.ReadFile(file); err == nil {
				f.Add(data)
			}
		}
	}
}

// validSampleRate reports whether a parsed rate is one the format can name.
// The enhanced syntax adds the halves: a reduced rate frame states one of the
// three and means half of it.
func validSampleRate(hz int, eac3 bool) bool {
	for _, r := range []int{48000, 44100, 32000} {
		if hz == r || (eac3 && hz == r/2) {
			return true
		}
	}
	return false
}

func FuzzParseSyncInfo(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var si SyncInfo
		if err := ParseSyncInfo(data, &si); err != nil {
			return
		}
		// A success has to mean the derived values are usable. What "usable"
		// is depends on the syntax: an enhanced frame states its own size and
		// can halve its own rate, so the tight bounds an AC-3 frame is held to
		// are not bounds it has to meet.
		if !validSampleRate(si.SampleRate, isEAC3(si.Bsid)) {
			t.Fatalf("SampleRate = %d", si.SampleRate)
		}
		if si.FrameSize%2 != 0 {
			t.Fatalf("FrameSize = %d, not a whole number of 16-bit words", si.FrameSize)
		}
		if isEAC3(si.Bsid) {
			// frmsiz is 11 bits counting words from zero, so a frame is at most
			// 4096 bytes; anything shorter than its own header was rejected.
			if si.FrameSize < EAC3SyncInfoSize || si.FrameSize > 4096 {
				t.Fatalf("FrameSize = %d, outside [%d, 4096]", si.FrameSize, EAC3SyncInfoSize)
			}
			if n := si.NumBlocks; n != 1 && n != 2 && n != 3 && n != 6 {
				t.Fatalf("NumBlocks = %d", n)
			}
			if si.Strmtyp >= StrmtypReserved {
				t.Fatalf("Strmtyp = %d accepted", si.Strmtyp)
			}
			// The rate is worked out from the frame size rather than stated, so
			// it is bounded only by what the size and block count can be. It
			// still has to be a positive number of bits per second.
			if si.BitRate <= 0 {
				t.Fatalf("BitRate = %d", si.BitRate)
			}
			return
		}
		if si.FrameSize < MinFrameSize || si.FrameSize > MaxFrameSize {
			t.Fatalf("FrameSize = %d, outside [%d, %d]", si.FrameSize, MinFrameSize, MaxFrameSize)
		}
		if si.NumBlocks != BlocksPerFrame {
			t.Fatalf("NumBlocks = %d, AC-3 has no other count", si.NumBlocks)
		}
		if si.BitRate < 32000 || si.BitRate > 640000 {
			t.Fatalf("BitRate = %d", si.BitRate)
		}
	})
}

func FuzzParseHeader(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		var h Header
		if err := ParseHeader(data, &h); err != nil {
			return
		}

		// Every accessor must be safe to call on any header that parsed:
		// the decode stages will call them without re-validating.
		if h.Sync.Bsid > MaxBSID && !isEAC3(h.Sync.Bsid) {
			t.Fatalf("Bsid = %d accepted", h.Sync.Bsid)
		}
		if h.Acmod > 7 {
			t.Fatalf("Acmod = %d accepted", h.Acmod)
		}
		layout := h.Layout()
		if got, want := len(layout), h.Channels(); got != want {
			t.Fatalf("Layout has %d channels, Channels() says %d", got, want)
		}
		if h.Channels() < 1 || h.Channels() > 6 {
			t.Fatalf("Channels = %d", h.Channels())
		}
		if layout.Has(pcm.ChannelLFE) != h.Lfeon {
			t.Fatalf("Lfeon = %v but Layout is %s", h.Lfeon, layout)
		}
		if db := h.DialnormDB(); db < -31 || db > -1 {
			t.Fatalf("DialnormDB = %d, outside [-31, -1]", db)
		}
		if db := h.Dialnorm2DB(); db < -31 || db > -1 {
			t.Fatalf("Dialnorm2DB = %d, outside [-31, -1]", db)
		}
		// The enhanced syntax states a direct index into the gain levels, so an
		// enhanced frame can name a boost as well as an attenuation, and index 7
		// asks for the centre to be dropped from the downmix entirely - zero is
		// a level there, not an absence. An AC-3 frame indexes tables 4.16 and
		// 4.17, which name attenuations only, so a gain above unity or a zero
		// would be a table slip.
		maxLevel := float32(1)
		if isEAC3(h.Sync.Bsid) {
			maxLevel = levelPlus3dB
		}
		if lv := h.CenterMixLevel(); lv < 0 || lv > maxLevel || (lv == 0 && !isEAC3(h.Sync.Bsid)) {
			t.Fatalf("CenterMixLevel = %v", lv)
		}
		// The surround level is the exception: the spec gives the field only the
		// bottom half of the table, and the parser clamps it there, so no
		// surround gain above -1.5 dB can reach here whatever the syntax.
		if lv := h.SurroundMixLevel(); lv < 0 || lv > 1 {
			t.Fatalf("SurroundMixLevel = %v", lv)
		}
		_ = h.AcmodName()
		_ = h.BsmodName()
		_ = h.RoomType()
		_ = h.Format().String()

		// addbsi has to stay inside its buffer.
		if add := h.AddBSI(); len(add) > maxAddBSIBytes {
			t.Fatalf("AddBSI is %d bytes", len(add))
		}
		// The audio blocks cannot start outside the frame: the next stage will
		// seek there.
		if h.AudioStartBit < SyncInfoSize*8 || h.AudioStartBit > h.Sync.FrameSize*8 {
			t.Fatalf("AudioStartBit = %d, outside the %d-bit frame", h.AudioStartBit, h.Sync.FrameSize*8)
		}
	})
}

func FuzzCheckCRC(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		_ = CheckCRC(data)
		_ = CheckCRC1(data)
	})
}

func FuzzFrameReader(f *testing.F) {
	seedCorpus(f)
	f.Fuzz(func(t *testing.T, data []byte) {
		fr := NewFrameReader(bytes.NewReader(data))
		var inFrames int64
		var clean [][]byte
		// Both runs stop at the same frame, so that the hostile one is never
		// asked about a frame the clean one was cut off before reaching. The cap
		// bounds the work per input; it is not a property of the reader.
		const maxFrames = 64
		for range maxFrames {
			frame, err := fr.Next()
			if errors.Is(err, io.EOF) {
				break
			}
			// One error is legal on a source that cannot fail: a stream that
			// opens on the byte-swapped syncword is reported as such, once,
			// before anything has been served or skipped.
			if errors.Is(err, ErrByteOrder) && fr.Frames() == 0 && fr.Skipped() == 0 {
				break
			}
			if err != nil {
				t.Fatalf("Next returned %v on a byte slice, which cannot fail to read", err)
			}
			// A returned frame is a promise: it is a whole frame, its header
			// parses, and both of its check words hold. Asserting crc2 as well
			// as crc1 is what pins the reader to returning frames rather than
			// to returning a sound 5/8 of one with anything at all behind it.
			if len(frame) > MaxAnyFrameSize {
				t.Fatalf("returned a %d-byte frame", len(frame))
			}
			var h Header
			if err := ParseHeader(frame, &h); err != nil {
				t.Fatalf("returned a frame that does not parse: %v", err)
			}
			// The floor is the syntax's, not AC-3's for both: MinFrameSize comes
			// from the AC-3 frame size table, while an enhanced frame states its
			// size in 16-bit words counted from zero and so can legally be as
			// small as the six bytes its own sync info needs.
			minSize := MinFrameSize
			if isEAC3(h.Sync.Bsid) {
				minSize = EAC3SyncInfoSize
			}
			if len(frame) < minSize {
				t.Fatalf("returned a %d-byte frame, under the %d-byte floor", len(frame), minSize)
			}
			if h.Sync.FrameSize != len(frame) {
				t.Fatalf("returned %d bytes for a frame of %d", len(frame), h.Sync.FrameSize)
			}
			if err := CheckCRC(frame); err != nil {
				t.Fatalf("returned a frame that fails its check words: %v", err)
			}
			clean = append(clean, bytes.Clone(frame))
			inFrames += int64(len(frame))
		}
		// Nothing is invented and nothing is lost.
		if got := inFrames + fr.Skipped(); got > int64(len(data)) {
			t.Fatalf("%d bytes in frames plus %d skipped exceeds the %d-byte input",
				inFrames, fr.Skipped(), len(data))
		}

		// The same stream through a hostile source: reads that arrive in
		// crumbs, stall for a bounded while, and then fail mid-stream. The
		// promise is that none of it changes a single frame - the reader hands
		// over a prefix of the clean run's frames and then the source's error
		// in place of io.EOF - and the parameters come from the input so the
		// fuzzer explores the boundaries between the three behaviours.
		var chunk, failAt int
		if len(data) > 0 {
			chunk = 1 + int(data[0])%97
			failAt = int(data[len(data)-1]) * len(data) / 256
		}
		boom := errors.New("mid-stream failure")
		fr = NewFrameReader(&faultReader{data: data, chunk: chunk, failAt: failAt, err: boom})
		for i := range maxFrames {
			frame, err := fr.Next()
			if err != nil {
				legalHead := errors.Is(err, ErrByteOrder) && fr.Frames() == 0 && fr.Skipped() == 0
				if !errors.Is(err, boom) && !errors.Is(err, io.EOF) && !legalHead {
					t.Fatalf("hostile source: Next returned %v, want the source's own error or io.EOF", err)
				}
				break
			}
			if i >= len(clean) || !bytes.Equal(frame, clean[i]) {
				t.Fatalf("hostile source: frame %d differs from the clean run's", i)
			}
		}
	})
}

// faultReader hands data out at most chunk bytes at a time, stalls with a
// bounded run of empty reads every so often, and once failAt bytes have been
// handed over it returns err forever.
type faultReader struct {
	data   []byte
	chunk  int
	failAt int
	err    error
	stall  int
}

func (r *faultReader) Read(p []byte) (int, error) {
	if r.failAt <= 0 {
		return 0, r.err
	}
	// A bounded stutter: every other read yields nothing, which the contract
	// of io.Reader allows as long as it does not go on forever.
	r.stall++
	if r.stall%2 == 0 {
		return 0, nil
	}
	n := min(len(p), r.chunk, len(r.data), r.failAt)
	if n == 0 {
		return 0, io.EOF
	}
	copy(p, r.data[:n])
	r.data = r.data[n:]
	r.failAt -= n
	return n, nil
}

// maxFuzzCoeff bounds a decoded coefficient in FuzzDecodeFrame: the format's
// worst legal chain (mantissa x coupling gain x extension gain, rematrixed)
// stays under about 500, and this is that with an eightfold margin.
const maxFuzzCoeff = 4096

// FuzzDecodeFrame is the one that matters most here. Bit allocation walks
// fixed size arrays with band and bin numbers computed from the bit stream:
// exponent counts, coupling ranges, delta segment offsets. None of that is
// covered by a check word, so on a damaged frame the only thing standing
// between a computed index and a panic is the decoder's own bounds.
func FuzzDecodeFrame(f *testing.F) {
	seedCorpus(f)
	d := NewDecoder()
	f.Fuzz(func(t *testing.T, data []byte) {
		if err := d.DecodeFrame(data); err != nil {
			return
		}
		// A frame that decoded has to hold together: six blocks of
		// coefficients that are numbers, inside a frame that has room for
		// them.
		h := d.Header()
		if d.BlockEndBit() > len(data)*8 {
			t.Fatalf("the audio blocks end at bit %d, past the %d bytes given",
				d.BlockEndBit(), len(data))
		}
		for blk := range BlocksPerFrame {
			b := d.Block(blk)
			for ch := range h.Channels() {
				if b.EndMant[ch] < 0 || b.EndMant[ch] > MaxCoefs {
					t.Fatalf("block %d channel %d ends at bin %d", blk, ch, b.EndMant[ch])
				}
				for bin, c := range b.Coeffs[ch] {
					// Bounded by what the format's own arithmetic can build,
					// with room. A coded mantissa is a fraction, inside -1 to
					// 1; a bin out of the coupling channel has been through a
					// gain that reaches 7,75; one the extension built,
					// through a gain that reaches 28 on top of that; and
					// rematrixing sums two such bins. That chains to about
					// 500 on the most hostile frame the syntax can spell, so
					// 4096 is an eightfold margin - loose enough that no
					// legal frame can trip it (the reference does not clamp,
					// and neither does this decoder), tight enough that a
					// broken table or a wrong exponent shows up as the
					// astronomical value it produces instead of hiding
					// behind "it was finite". NaN fails the comparison too.
					if !(c > -maxFuzzCoeff && c < maxFuzzCoeff) {
						t.Fatalf("block %d channel %d bin %d = %v", blk, ch, bin, c)
					}
				}
			}
		}
	})
}
