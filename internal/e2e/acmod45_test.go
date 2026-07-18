package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/pcm"
)

// The 2/1 and 3/1 modes reach code that no other stream in this repo touches:
// a lone surround channel, and a downmix that folds it into both sides at the
// surround mix level rather than splitting a pair. Until this test their
// downmix had only ever been compared with itself.
func TestAcmod45AgainstReference(t *testing.T) {
	o := Setup(t)

	targets := []struct {
		name     string
		layout   pcm.Layout
		channels int
	}{
		{"direct", nil, 0},
		{"stereo", pcm.LayoutStereo, 2},
		{"mono", pcm.LayoutMono, 1},
	}
	for _, name := range []string{
		"tones_48k_2_1_192k.ac3",
		"tones_48k_3_1_256k.ac3",
		"tones_48k_dualmono_192k.ac3",
		"tones_48k_stereo_xbsi_192k.ac3",
	} {
		stream, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		for _, target := range targets {
			// Dual mono is two programmes, not a soundfield: the reference
			// refuses to fold it, and so does SetDownmix. And a stereo source
			// has nothing to fold. Direct only for both.
			if target.layout != nil && (name == "tones_48k_dualmono_192k.ac3" ||
				name == "tones_48k_stereo_xbsi_192k.ac3") {
				continue
			}
			t.Run(name+"/"+target.name, func(t *testing.T) {
				var want []int32
				var got []int32
				var channels int
				if target.layout == nil {
					want = o.DecodePCM(t, stream, 16)
					got, channels = decodeStream(t, stream)
					_ = channels
				} else {
					want = o.DecodePCMDownmix(t, stream, 16, target.name)
					got, channels = decodeStreamFunc(t, stream, func(d *ac3.Decoder) {
						if err := d.SetDownmix(target.layout); err != nil {
							t.Fatal(err)
						}
					})
					if channels != target.channels {
						t.Fatalf("downmixed to %d channels, want %d", channels, target.channels)
					}
				}
				if len(got) != len(want) {
					t.Fatalf("sample counts differ: got %d, reference %d", len(got), len(want))
				}
				res, err := Compare(got, want, Dithered)
				if err != nil {
					t.Fatalf("%s: %v", target.name, err)
				}
				t.Logf("%s: %s", target.name, res)
			})
		}
	}
}
