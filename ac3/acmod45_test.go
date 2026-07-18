package ac3

import (
	"os"
	"path/filepath"
	"testing"
)

// The 2/1 and 3/1 modes were parsed by nothing until these fixtures: no real
// stream in reach codes them, so they can only come from an encoder driven to.
// Aften produced both (`-acmod 4` and `-acmod 5`), which keeps the virtue the
// transient fixture has: the bits were written by neither this decoder nor the
// reference it is compared against.
func TestAcmod45Fixtures(t *testing.T) {
	tests := []struct {
		file      string
		acmod     uint8
		channels  int
		hasCmix   bool
		hasSurmix bool
	}{
		// 2/1: left, right, one surround. No centre, so no centre mix level;
		// a surround, so a surround mix level.
		{"tones_48k_2_1_192k.ac3", Acmod2F1R, 3, false, true},
		// 3/1: left, centre, right, one surround. Both levels.
		{"tones_48k_3_1_256k.ac3", Acmod3F1R, 4, true, true},
		// 1+1: two independent programmes, so neither mix level - there is
		// nothing to fold a channel into - and a second dialnorm.
		{"tones_48k_dualmono_192k.ac3", AcmodDualMono, 2, false, false},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", tt.file))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			fr := NewFrameReader(f)
			d := NewDecoder()
			frames := 0
			for {
				frame, err := fr.Next()
				if err != nil {
					break
				}
				if err := d.DecodeFrame(frame); err != nil {
					t.Fatalf("frame %d: %v", frames, err)
				}
				h := d.Header()
				if h.Acmod != tt.acmod {
					t.Fatalf("frame %d: Acmod = %d, want %d", frames, h.Acmod, tt.acmod)
				}
				if got := h.Channels(); got != tt.channels {
					t.Fatalf("frame %d: Channels = %d, want %d", frames, got, tt.channels)
				}
				if h.HasCmixlev != tt.hasCmix || h.HasSurmixlev != tt.hasSurmix {
					t.Fatalf("frame %d: HasCmixlev = %v, HasSurmixlev = %v, want %v, %v",
						frames, h.HasCmixlev, h.HasSurmixlev, tt.hasCmix, tt.hasSurmix)
				}
				if got, want := len(h.Layout()), tt.channels; got != want {
					t.Fatalf("frame %d: Layout has %d channels, want %d", frames, got, want)
				}
				frames++
			}
			if frames != 32 {
				t.Fatalf("decoded %d frames, the fixture holds 32", frames)
			}
		})
	}
}

// TestAlternateBSIFixture pins the alternate bit stream syntax against values
// that did not come from this package: every literal below is what the
// encoder was told on its command line (`-xbsi1 1 -dmixmod 2 -ltrtcmix 4
// -ltrtsmix 4 -lorocmix 5 -lorosmix 5 -xbsi2 1 -dsurexmod 2 -dheadphon 1
// -adconvtyp 1`). The synthetic frames the other tests build validate this
// parse only against its own mirror image; these bits were laid down by
// someone else's writer.
func TestAlternateBSIFixture(t *testing.T) {
	f, err := os.Open(filepath.Join("testdata", "tones_48k_stereo_xbsi_192k.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	fr := NewFrameReader(f)
	d := NewDecoder()
	frames := 0
	for {
		frame, err := fr.Next()
		if err != nil {
			break
		}
		if err := d.DecodeFrame(frame); err != nil {
			t.Fatalf("frame %d: %v", frames, err)
		}
		h := d.Header()
		if h.Sync.Bsid != AltBSID {
			t.Fatalf("frame %d: Bsid = %d, want %d", frames, h.Sync.Bsid, AltBSID)
		}
		if !h.Xbsi1e || h.Dmixmod != 2 ||
			h.Ltrtcmixlev != 4 || h.Ltrtsurmixlev != 4 ||
			h.Lorocmixlev != 5 || h.Lorosurmixlev != 5 {
			t.Fatalf("frame %d: xbsi1 = %v %d %d %d %d %d, want true 2 4 4 5 5", frames,
				h.Xbsi1e, h.Dmixmod, h.Ltrtcmixlev, h.Ltrtsurmixlev, h.Lorocmixlev, h.Lorosurmixlev)
		}
		if !h.Xbsi2e || h.Dsurexmod != 2 || h.Dheadphonmod != 1 || !h.Adconvtyp {
			t.Fatalf("frame %d: xbsi2 = %v %d %d %v, want true 2 1 true", frames,
				h.Xbsi2e, h.Dsurexmod, h.Dheadphonmod, h.Adconvtyp)
		}
		frames++
	}
	if frames != 32 {
		t.Fatalf("decoded %d frames, the fixture holds 32", frames)
	}
}
