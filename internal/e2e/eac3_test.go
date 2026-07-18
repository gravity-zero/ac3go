package e2e

import (
	"os"
	"path/filepath"
	"testing"
)

// The first enhanced AC-3 samples this decoder has ever been held to.
//
// What is being checked is narrow and worth stating plainly, because the
// headline "E-AC-3 decodes" would be a lie. These fixtures use neither the
// adaptive hybrid transform nor spectral extension, and both are things real
// streams lean on: measured over 60 real 5.1 streams and 6473 frames, half the
// frames carry spectral extension, and every frame that carries it also uses
// AHT on some channel. So what this proves is the frame's scaffolding - the
// audio frame field, the block walk, the exponents and bit allocation reached
// through them - and nothing at all about either of those two.
//
// Spectral extension is written (see ac3/spx.go) and has been measured against
// the reference, but not by anything committed here: no encoder within reach
// emits it, so there is no fixture to commit, and the only content that has it
// is the corpus, which cannot be. ac3/spx_test.go pins the pieces of it that a
// fixture would not have reached anyway.
//
// It is still the thing worth having first. Everything the two features will be
// built on has to be right before either can be checked, and a wrong bit
// anywhere in the walk moves every field after it: there is no check word until
// the end of the frame, so nothing catches a drift except samples that do not
// match. These do.
func TestDecodeEAC3FixtureAgainstReference(t *testing.T) {
	o := Setup(t)

	for _, name := range []string{
		"tones_48k_stereo_192k.eac3",
		"tones_48k_5p1_384k.eac3",
	} {
		t.Run(name, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}

			want := o.DecodePCM(t, stream, 16)
			got, channels := decodeStream(t, stream)
			if len(got) != len(want) {
				t.Fatalf("sample counts differ: got %d, reference %d", len(got), len(want))
			}

			res, err := Compare(got, want, Dithered)
			if err != nil {
				t.Fatalf("%d channels: %v", channels, err)
			}
			t.Logf("%d channels: %s", channels, res)
		})
	}
}
