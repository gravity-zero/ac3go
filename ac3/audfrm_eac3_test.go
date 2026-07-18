package ac3

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestEAC3FrmExpstrIsAWhitelist pins the shape of table E2.14 rather than its
// 192 entries, which a transcription error would leave looking fine.
//
// The property: no combination starts with reuse. Block 0 has no block before
// it, so a frame whose first block reused would be a frame with no exponents at
// all, and the table exists precisely so that a five bit code cannot say that -
// where six free two bit strategies could.
func TestEAC3FrmExpstrIsAWhitelist(t *testing.T) {
	for code, combo := range eac3FrmExpstr {
		if combo[0] == ExpReuse {
			t.Errorf("combination %d starts with reuse: block 0 has nothing to reuse", code)
		}
		for blk, strat := range combo {
			if strat > ExpD45 {
				t.Errorf("combination %d block %d: strategy %d is not one of the four", code, blk, strat)
			}
		}
	}
	// The two ends of the table, spot-checked against the spec's own listing:
	// the cheapest a frame can be (one full set, reused throughout) and the
	// dearest (a new set every block).
	if got := eac3FrmExpstr[0]; got != [6]uint8{ExpD15, ExpReuse, ExpReuse, ExpReuse, ExpReuse, ExpReuse} {
		t.Errorf("combination 0 = %v", got)
	}
	if got := eac3FrmExpstr[31]; got != [6]uint8{ExpD45, ExpD45, ExpD45, ExpD45, ExpD45, ExpD45} {
		t.Errorf("combination 31 = %v", got)
	}
}

// TestFloorLog2 pins the width arithmetic of the block start field. It is the
// reference's av_log2, and it is a floor: reading it as a ceiling would add a
// bit per block and land the first audio block in the wrong place.
func TestFloorLog2(t *testing.T) {
	for _, c := range []struct{ in, want int }{
		{1, 0}, {2, 1}, {3, 1}, {4, 2}, {511, 8}, {512, 9}, {1022, 9}, {1024, 10},
	} {
		if got := floorLog2(c.in); got != c.want {
			t.Errorf("floorLog2(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

// TestEAC3AudioFrameParses walks the audio frame field of the fixtures and
// checks the invariants the blocks then depend on.
//
// That these fixtures decode at all is the strongest thing said about the audio
// frame field: it is read before any block and every block's syntax depends on
// what it said, so a field misread here moves every bit after it, and the
// samples would be noise rather than nearly the reference's. That comparison is
// in internal/e2e; this pins the pieces where they can be named.
func TestEAC3AudioFrameParses(t *testing.T) {
	for _, f := range eac3Fixtures {
		t.Run(f.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", f.file))
			if err != nil {
				t.Fatal(err)
			}
			fr := NewFrameReader(bytes.NewReader(raw))
			d := NewDecoder()

			var frames int
			for {
				frame, err := fr.Next()
				if err != nil {
					break
				}
				if err := d.DecodeFrame(frame); err != nil {
					t.Fatalf("frame %d: %v", frames, err)
				}
				frames++

				// Every channel's strategy for block 0 has to be a real one:
				// there is no previous block to reuse from, whichever way the
				// frame stated them.
				for ch := range d.h.FullBandwidthChannels() {
					if got := d.eac3.expStrategy[0][ch]; got == ExpReuse {
						t.Fatalf("frame %d channel %d: block 0 reuses exponents that do not exist", frames, ch)
					}
				}
				// The coupling channel is the exception, and not really one: a
				// block that does not couple has no coupling exponents to state.
				if d.eac3.cplInUse[0] && d.eac3.expStrategy[0][MaxChannels] == ExpReuse {
					t.Fatalf("frame %d: block 0 couples but reuses coupling exponents", frames)
				}
				// A channel can only use the adaptive hybrid transform when its
				// six blocks are one set of exponents. This is the invariant the
				// parse derives it from, checked from the other side.
				for ch, uses := range d.eac3.usesAHT {
					if !uses {
						continue
					}
					for blk := 1; blk < BlocksPerFrame; blk++ {
						if d.eac3.expStrategy[blk][ch] != ExpReuse {
							t.Fatalf("frame %d channel %d: uses AHT but block %d states new exponents",
								frames, ch, blk)
						}
					}
				}
			}
			if frames != f.frames {
				t.Errorf("walked %d frames, want %d", frames, f.frames)
			}
		})
	}
}

// TestEAC3RejectsWhatItCannotDecode pins that the two things this decoder does
// not reach say so, rather than being decoded as something else.
//
// The reduced rate one is not a gap to be filled later: the reference declines
// it too, because the spec does not say how bit allocation works there. There
// is nothing to check an implementation against, and no stream measured so far
// uses it.
func TestEAC3RejectsWhatItCannotDecode(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", eac3Fixtures[0].file))
	if err != nil {
		t.Fatal(err)
	}
	var si SyncInfo
	if err := ParseSyncInfo(raw, &si); err != nil {
		t.Fatal(err)
	}
	frame := bytes.Clone(raw[:si.FrameSize])
	d := NewDecoder()

	// substreamid is bits 18 to 20 of the frame, which are bits 5, 4 and 3 of
	// byte 2: strmtyp has the two above them and frmsiz starts just below.
	dep := bytes.Clone(frame)
	dep[2] |= 0x08 // substreamid's low bit: substream 1
	if err := d.DecodeFrame(dep); !errors.Is(err, ErrUnsupportedEAC3) {
		t.Errorf("a frame of substream 1 decoded as %v, want the enhanced sentinel", err)
	}
}
