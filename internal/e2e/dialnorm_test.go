package e2e

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// Dialogue normalization is metadata: the encoder states how loud the dialogue
// was mixed and scales nothing by it, so a decoder that ignores it and one that
// applies it disagree on the level of every stream whose dialogue was not mixed
// at -31 dB. This checks that when ac3go is asked to apply it, it lands where
// the reference lands when asked the same thing.
//
// The reference has to be asked, and that is worth knowing before reading any
// of this: it applies no dialogue normalization by default. Measured here
// rather than assumed - three streams differing only in the dialnorm field
// (-31, -20, -10) decode to identical levels through the reference at its
// defaults. So ac3go's default is off too, and the test below is the only
// place either decoder is asked to turn it on.
//
// The fixtures are synthetic and tiny: a tone encoded three times with nothing
// changed but the field under test, which makes the field the only thing that
// can move the result.
//
//	ffmpeg -f lavfi -i "sine=frequency=1000:sample_rate=48000:duration=1" \
//	  -af volume=0.25 -ac 2 -c:a ac3 -b:a 192k -dialnorm -N dialnorm_N.ac3   # N = 10, 20, 31
var dialnormFixtures = []struct {
	file string
	// dialnorm is what the file states, and what ac3info reads back from it:
	// the encoder is asked for it but the bit stream is what decides.
	dialnorm int
}{
	{"dialnorm_m31_48k_stereo_192k.ac3", -31},
	{"dialnorm_m20_48k_stereo_192k.ac3", -20},
	{"dialnorm_m10_48k_stereo_192k.ac3", -10},
}

// dialnormBar is far tighter than the bar a real stream is held to, because the
// mistake it exists to catch is a small one. The gain is 2^((target-dialnorm)/6)
// and the obvious reading - a decibel is a decibel, so 10^((target-dialnorm)/20)
// - differs from it by 0,44% at eleven decibels. Held to the bar a dithered
// stream gets, the wrong law would look right.
//
// Both bounds were set from measurement rather than picked. With the law the
// reference uses, these fixtures come out at most 6 LSB from it and 0,44 LSB
// RMS; with the other law, 15 LSB and 7,81. So the RMS bound is the one that
// discriminates, and the per sample bound is deliberately not tight enough to:
// at 15 LSB the wrong law slips under any per sample bound loose enough to be
// safe. That is worth knowing before tightening MaxLSB in the belief it is what
// guards this.
//
// The tone's amplitude is load bearing for the same reason. It is 0,25 so that
// the largest boost the test asks for, eleven decibels, lands at 0,89 and does
// not clip: a clipped tone is two decoders agreeing on full scale, which hides
// the very difference being measured. An earlier pass at 0,1 was quiet enough
// that the wrong law came in under the bar.
var dialnormBar = Tolerance{MaxLSB: 32, MaxRMSLSB: 4}

// TestDialnormMatchesReference asks both decoders to normalize dialogue to the
// same target and compares their samples.
func TestDialnormMatchesReference(t *testing.T) {
	o := Setup(t)

	// -31 is the level the format is built around and 0 would mean "off", so
	// the interesting targets are between. -20 makes every fixture move: it is
	// a boost for two of them and a cut for the third.
	for _, target := range []int{-31, -20} {
		for _, f := range dialnormFixtures {
			t.Run(f.file, func(t *testing.T) {
				stream, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", f.file))
				if err != nil {
					t.Fatal(err)
				}

				var h ac3.Header
				if err := ac3.ParseHeader(stream, &h); err != nil {
					t.Fatal(err)
				}
				if h.DialnormDB() != f.dialnorm {
					t.Fatalf("fixture states dialnorm %d dB, expected %d", h.DialnormDB(), f.dialnorm)
				}

				want := o.DecodePCMTargetLevel(t, stream, 16, target)
				got, _ := decodeStreamFunc(t, stream, func(d *ac3.Decoder) {
					d.SetTargetLevel(target)
				})

				res, err := Compare(got, want, dialnormBar)
				if err != nil {
					t.Fatalf("target %d dB: %v", target, err)
				}
				t.Logf("dialnorm %d dB, target %d dB: %s", f.dialnorm, target, res)
			})
		}
	}
}
