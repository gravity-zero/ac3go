package ac3

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// eac3Fixtures are real encoder output, tones rather than anyone's audio.
//
//	ffmpeg -f lavfi -i "sine=f=500:r=48000:d=1" -f lavfi -i "sine=f=1500:r=48000:d=1" \
//	  -filter_complex "[0:a][1:a]amerge=inputs=2,volume=0.5[a]" -map "[a]" \
//	  -c:a eac3 -b:a 192k tones_48k_stereo_192k.eac3
//
// The 5.1 one merges six sines, 500, 900, 1500, 1900, 2300 and 50 Hz, at
// 384 kbit/s. A distinct tone per channel is not needed to parse a header, but
// these fixtures outlive this test: the decode has to land each tone in the
// right channel, and a fixture where every channel sounds alike cannot say so.
var eac3Fixtures = []struct {
	file     string
	acmod    uint8
	lfe      bool
	rate     int
	bitrate  int
	frames   int
	frameLen int
}{
	{"tones_48k_stereo_192k.eac3", AcmodStereo, false, 48000, 192000, 32, 768},
	{"tones_48k_5p1_384k.eac3", Acmod3F2R, true, 48000, 384000, 32, 1536},
}

// TestEAC3FixturesFrameExactly is the framing claim, and the only form of it
// worth making: every byte of the stream is inside a frame, every frame's check
// word agrees, and the frames land where the sizes say they should.
//
// Nothing weaker would mean anything. A reader that mis-sizes frames still
// returns frames - it resynchronises on the next syncword it stumbles over and
// skips whatever it walked past - so "it returned some frames" is what a broken
// reader looks like too. Zero bytes skipped is what a correct one looks like.
func TestEAC3FixturesFrameExactly(t *testing.T) {
	for _, f := range eac3Fixtures {
		t.Run(f.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", f.file))
			if err != nil {
				t.Fatal(err)
			}
			fr := NewFrameReader(bytes.NewReader(raw))

			var frames int
			for {
				frame, err := fr.Next()
				if err != nil {
					break
				}
				h := fr.Header()
				if h.Sync.Bsid != 16 {
					t.Fatalf("frame %d: bsid = %d, want 16", frames, h.Sync.Bsid)
				}
				if h.Sync.Strmtyp != StrmtypIndependent {
					t.Fatalf("frame %d: strmtyp = %d, want independent", frames, h.Sync.Strmtyp)
				}
				if h.Sync.Substreamid != 0 {
					t.Fatalf("frame %d: substream %d, want 0", frames, h.Sync.Substreamid)
				}
				if h.Sync.NumBlocks != BlocksPerFrame {
					t.Fatalf("frame %d: %d blocks, want %d", frames, h.Sync.NumBlocks, BlocksPerFrame)
				}
				if h.Acmod != f.acmod || h.Lfeon != f.lfe {
					t.Fatalf("frame %d: acmod %d lfe %v, want %d %v", frames, h.Acmod, h.Lfeon, f.acmod, f.lfe)
				}
				if h.Sync.SampleRate != f.rate || h.Sync.BitRate != f.bitrate {
					t.Fatalf("frame %d: %d Hz %d bit/s, want %d %d",
						frames, h.Sync.SampleRate, h.Sync.BitRate, f.rate, f.bitrate)
				}
				if len(frame) != f.frameLen {
					t.Fatalf("frame %d: %d bytes, want %d", frames, len(frame), f.frameLen)
				}
				frames++
			}

			if frames != f.frames {
				t.Errorf("read %d frames, want %d", frames, f.frames)
			}
			if fr.Skipped() != 0 {
				t.Errorf("%d bytes skipped: the frames do not tile the stream", fr.Skipped())
			}
		})
	}
}

// TestBSIDSitsAtTheSameBitInBothSyntaxes is the property the whole dispatch
// rests on, and it is worth stating on its own because everything else assumes
// it silently.
//
// A stream does not come labelled. The bytes between the syncword and bsid mean
// entirely different things in the two syntaxes - a check word and a frame size
// code in one, a substream and a frame size in the other - so nothing there can
// be read until it is known which. bsid is what says, and it can only say if it
// is in the same place either way, which it is: the fields before it differ in
// meaning but not in total width.
func TestBSIDSitsAtTheSameBitInBothSyntaxes(t *testing.T) {
	for _, bsid := range []uint8{0, 6, 8, 9, 16, 31} {
		frame := synth(t, 0, 30, func() bsiSpec {
			s := defaultBSI()
			s.bsid = bsid
			return s
		}())
		if got := peekBSID(frame); got != bsid {
			t.Errorf("peekBSID = %d, want %d: bsid is not where the dispatch looks", got, bsid)
		}
	}

	// And the other direction: a real enhanced frame, whose bytes before bsid
	// were written by an encoder following the other syntax entirely.
	raw, err := os.ReadFile(filepath.Join("testdata", eac3Fixtures[0].file))
	if err != nil {
		t.Fatal(err)
	}
	if got := peekBSID(raw); got != 16 {
		t.Errorf("peekBSID on a real enhanced frame = %d, want 16", got)
	}
}

// TestEAC3HasNoCRC1 pins that asking for a check that does not exist is an
// error rather than a pass. Returning nil would read as "the first five eighths
// are sound" to a caller that cannot tell the syntaxes apart, which is exactly
// the caller that would ask.
func TestEAC3HasNoCRC1(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", eac3Fixtures[0].file))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckCRC1(raw); !errors.Is(err, ErrCRC) {
		t.Errorf("CheckCRC1 on an enhanced frame = %v, want an error: there is no crc1 to pass", err)
	}
	// The check word it does have has to agree, or the fixture is not what the
	// test above thinks it is.
	if err := CheckCRC(raw); err != nil {
		t.Errorf("CheckCRC = %v, want nil", err)
	}
}

// TestEAC3CRCCoversTheWholeFrame checks the one check word by breaking the
// frame a byte at a time. AC-3 splits its frame and gives each half a word;
// this one is not split, so a flip anywhere has to be caught - including in the
// last eighth, which is where a check word copied from the AC-3 path would stop
// looking.
func TestEAC3CRCCoversTheWholeFrame(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", eac3Fixtures[0].file))
	if err != nil {
		t.Fatal(err)
	}
	var si SyncInfo
	if err := ParseSyncInfo(raw, &si); err != nil {
		t.Fatal(err)
	}
	good := raw[:si.FrameSize]

	// Byte 2 is the first the word covers, and the last byte is the word.
	for _, i := range []int{2, 3, 7, si.FrameSize / 2, si.FrameSize * 7 / 8, si.FrameSize - 3, si.FrameSize - 1} {
		bad := bytes.Clone(good)
		bad[i] ^= 1
		if err := checkEAC3CRC(bad, si.FrameSize); err == nil {
			t.Errorf("byte %d of %d flipped and the check word still agreed", i, si.FrameSize)
		}
	}
}

// TestEAC3MixLevelsAreIndicesNotCodes pins the one place the two syntaxes
// disagree about what a field means rather than where it is.
//
// AC-3 states a two bit code into a table of three levels. E-AC-3 states a
// three bit index straight into the nine gain levels. The same number means
// different gains, and reading one as the other is silent: both are small
// integers and both produce a plausible level.
func TestEAC3MixLevelsAreIndicesNotCodes(t *testing.T) {
	var h Header
	h.Sync.Bsid = 16
	h.Sync.Acmod, h.Acmod = Acmod3F2R, Acmod3F2R
	h.HasCmixlev, h.HasSurmixlev = true, true

	// Index 4 is -3 dB. As an AC-3 code, 4 is not even in the table.
	h.Cmixlev, h.Surmixlev = 4, 4
	if got := h.CenterMixLevel(); got != levelMinus3dB {
		t.Errorf("CenterMixLevel = %v, want %v: the enhanced field indexes the gain levels", got, levelMinus3dB)
	}

	// Index 0 is a boost, not an attenuation. The AC-3 tables name none, so a
	// centre level above unity only ever comes from the enhanced syntax - which
	// is why nothing may assume the mix levels sit in [0, 1].
	h.Cmixlev = 0
	if got := h.CenterMixLevel(); got != levelPlus3dB {
		t.Errorf("CenterMixLevel = %v, want %v: index 0 names a 3 dB boost", got, levelPlus3dB)
	}

	// Index 7 is the level that is no level: drop the channel from the downmix.
	h.Cmixlev = 7
	if got := h.CenterMixLevel(); got != 0 {
		t.Errorf("CenterMixLevel = %v, want 0: index 7 asks for the channel to be left out", got)
	}

	// The same header read as AC-3 gives something else entirely, which is the
	// point: the dispatch is what keeps them apart.
	h.Sync.Bsid = 8
	h.Cmixlev = 1
	if got := h.CenterMixLevel(); got != centerMixLevels[1] {
		t.Errorf("CenterMixLevel = %v, want %v: an AC-3 header must read the AC-3 table", got, centerMixLevels[1])
	}
}

// TestEAC3DefaultMixLevels pins that a frame stating no mixing metadata gets the
// levels the spec names rather than nothing. An AC-3 frame with no mix level has
// no such channel to mix; an enhanced one has the channel and a default level,
// so treating "absent" as "unity" would put the centre 4.5 dB too loud.
func TestEAC3DefaultMixLevels(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", eac3Fixtures[1].file)) // the 5.1 one
	if err != nil {
		t.Fatal(err)
	}
	var h Header
	if err := ParseHeader(raw, &h); err != nil {
		t.Fatal(err)
	}
	if h.Mixmdate {
		t.Skip("this fixture states mixing metadata, so it says nothing about the defaults")
	}
	if got := h.CenterMixLevel(); got != levelMinus4Point5dB {
		t.Errorf("CenterMixLevel = %v, want %v by default", got, levelMinus4Point5dB)
	}
	if got := h.SurroundMixLevel(); got != levelMinus6dB {
		t.Errorf("SurroundMixLevel = %v, want %v by default", got, levelMinus6dB)
	}
}

// TestClampSurroundGainLevel pins the range a stated surround level is held to.
func TestClampSurroundGainLevel(t *testing.T) {
	for _, c := range []struct{ in, want uint8 }{
		{0, 3}, {1, 3}, {2, 3}, // louder than -1.5 dB: not something a frame may ask for
		{3, 3}, {5, 5}, {7, 7}, // in range, untouched
	} {
		if got := clampSurroundGainLevel(c.in); got != c.want {
			t.Errorf("clampSurroundGainLevel(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEAC3ReducedRate pins the syntax that halves the sample rate, which no
// fixture here carries and no real stream measured so far uses: when fscod is 3
// the field that would have been numblkscod is a second rate code, and the frame
// is six blocks at half of what it names. A decoder that read numblkscod there
// would get a block count out of a rate code and a rate out of nothing.
func TestEAC3ReducedRate(t *testing.T) {
	// syncword, then: strmtyp 0, substreamid 0, frmsiz 255 (512 bytes),
	// fscod 3, fscod2 1 (44.1 kHz, so 22.05), acmod 2, lfeon 0, bsid 16.
	var w bitWriter
	w.write(uint32(Syncword), 16)
	w.write(0, 2)    // strmtyp
	w.write(0, 3)    // substreamid
	w.write(255, 11) // frmsiz
	w.write(3, 2)    // fscod: the reduced rate escape
	w.write(1, 2)    // fscod2: 44.1 kHz
	w.write(2, 3)    // acmod
	w.write(0, 1)    // lfeon
	w.write(16, 5)   // bsid

	var si SyncInfo
	if err := ParseSyncInfo(w.buf, &si); err != nil {
		t.Fatalf("ParseSyncInfo: %v", err)
	}
	if !si.HasFscod2 {
		t.Fatal("HasFscod2 is false: fscod 3 was not read as the reduced rate escape")
	}
	if si.SampleRate != 44100/2 {
		t.Errorf("SampleRate = %d, want %d", si.SampleRate, 44100/2)
	}
	if si.NumBlocks != BlocksPerFrame {
		t.Errorf("NumBlocks = %d, want %d: a reduced rate frame spends numblkscod on fscod2",
			si.NumBlocks, BlocksPerFrame)
	}

	// fscod2 3 is reserved, and it is the one thing that field cannot say.
	var w2 bitWriter
	w2.write(uint32(Syncword), 16)
	w2.write(0, 2)
	w2.write(0, 3)
	w2.write(255, 11)
	w2.write(3, 2)
	w2.write(3, 2) // fscod2 reserved
	w2.write(2, 3)
	w2.write(0, 1)
	w2.write(16, 5)
	if err := ParseSyncInfo(w2.buf, &si); !errors.Is(err, ErrReserved) {
		t.Errorf("ParseSyncInfo with fscod2 = 3: %v, want ErrReserved", err)
	}
}

// TestEAC3BlockCounts pins the block count code, which is the field that makes
// an enhanced frame stop being 1536 samples.
func TestEAC3BlockCounts(t *testing.T) {
	for code, want := range map[uint8]int{0: 1, 1: 2, 2: 3, 3: 6} {
		var w bitWriter
		w.write(uint32(Syncword), 16)
		w.write(0, 2)
		w.write(0, 3)
		w.write(255, 11)
		w.write(0, 2) // fscod: 48 kHz
		w.write(uint32(code), 2)
		w.write(2, 3)
		w.write(0, 1)
		w.write(16, 5)

		var si SyncInfo
		if err := ParseSyncInfo(w.buf, &si); err != nil {
			t.Fatalf("numblkscod %d: %v", code, err)
		}
		if si.NumBlocks != want {
			t.Errorf("numblkscod %d gives %d blocks, want %d", code, si.NumBlocks, want)
		}
		// The rate a frame works out to depends on how much time it covers, so
		// the same 512 bytes is four times the rate over one block that it is
		// over six... which is the whole reason the count has to be right.
		wantRate := 8 * si.FrameSize * si.SampleRate / (want * SamplesPerBlock)
		if si.BitRate != wantRate {
			t.Errorf("numblkscod %d: BitRate = %d, want %d", code, si.BitRate, wantRate)
		}
	}
}
