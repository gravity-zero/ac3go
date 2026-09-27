package e2e

import (
	"os"
	"path"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/pcm"
)

// The E-AC-3 downmix against the reference. It goes through the same mixing
// code as AC-3's, but not through the same path to it: the enhanced branch of
// DecodeFrame once returned before the mix, and every E-AC-3 stream asked for
// stereo came out silent while the AC-3 downmix test above stayed green. So the
// enhanced syntax gets its own comparison, on the committed fixtures, on real
// 5.1 and on real 7.1.

var downmixTargets = []struct {
	name     string
	layout   pcm.Layout
	channels int
}{
	{"stereo", pcm.LayoutStereo, 2},
	{"mono", pcm.LayoutMono, 1},
}

// compareDownmix decodes stream mixed down to each target with both decoders
// and holds the result to the reference.
func compareDownmix(t *testing.T, o *Oracle, stream []byte) {
	t.Helper()
	for _, target := range downmixTargets {
		t.Run(target.name, func(t *testing.T) {
			want := o.DecodePCMDownmix(t, stream, 16, target.name)
			got, channels := decodeStreamFunc(t, stream, func(d *ac3.Decoder) {
				if err := d.SetDownmix(target.layout); err != nil {
					t.Fatal(err)
				}
			})
			if channels != target.channels {
				t.Fatalf("downmixed to %d channels, want %d", channels, target.channels)
			}
			if len(got) != len(want) {
				t.Fatalf("sample counts differ: got %d, reference %d", len(got), len(want))
			}
			if silent(want) {
				t.Fatal("the reference's own mix is silent: nothing to compare against")
			}
			res, err := Compare(got, want, Dithered)
			if err != nil {
				t.Fatalf("%v", err)
			}
			t.Logf("%s: %s", target.name, res)
		})
	}
}

// silent reports whether every sample is zero. Two silent outputs agree with
// each other perfectly, which is how the E-AC-3 downmix bug would have passed
// a comparison against a reference that had failed the same way.
func silent(s []int32) bool {
	for _, v := range s {
		if v != 0 {
			return false
		}
	}
	return true
}

// TestDownmixEAC3FixtureAgainstReference folds the committed E-AC-3 fixtures:
// the 5.1 one to stereo and mono, and the stereo one to mono.
func TestDownmixEAC3FixtureAgainstReference(t *testing.T) {
	o := Setup(t)
	for _, name := range []string{"tones_48k_5p1_384k.eac3", "tones_48k_stereo_192k.eac3"} {
		t.Run(name, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			compareDownmix(t, o, stream)
		})
	}
}

// TestDownmixEAC3AgainstReference folds real E-AC-3 5.1 streams down. These
// are the ones that lean on spectral extension and the hybrid transform, which
// no committed fixture reaches.
func TestDownmixEAC3AgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	var tracks []stereoTrack
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.EAC3Tracks(t, f) {
			if tr.Channels == 6 {
				tracks = append(tracks, stereoTrack{f, tr.Index, tr.SampleRate})
			}
		}
		if len(tracks) >= 3 {
			break
		}
	}
	if len(tracks) == 0 {
		t.Skip("no 5.1 E-AC-3 track in the corpus")
	}
	for _, tr := range tracks {
		t.Run(path.Base(tr.file), func(t *testing.T) {
			stream := o.ExtractSpanEAC3(t, tr.file, tr.index, 60, 8)
			if len(stream) == 0 {
				t.Skip("nothing extracted")
			}
			compareDownmix(t, o, stream)
		})
	}
}

// TestDownmix71AgainstReference folds real 7.1 streams down. This decoder mixes
// the 5.1 core and steps over the dependent substream; this is what holds that
// choice to what the reference does with the same access units.
func TestDownmix71AgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	var tracks []stereoTrack
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.EAC3Tracks(t, f) {
			if tr.Channels == 8 {
				tracks = append(tracks, stereoTrack{f, tr.Index, tr.SampleRate})
			}
		}
		if len(tracks) >= 4 {
			break
		}
	}
	if len(tracks) == 0 {
		t.Skip("no 7.1 E-AC-3 track in the corpus")
	}
	for _, tr := range tracks {
		t.Run(path.Base(tr.file), func(t *testing.T) {
			stream := o.ExtractSpanEAC3(t, tr.file, tr.index, 60, 8)
			if len(stream) == 0 {
				t.Skip("nothing extracted")
			}
			if _, channels := decodeStream(t, stream); channels != 8 {
				t.Skipf("decodes to %d channels, not the standard 7.1 extension", channels)
			}
			compareDownmix(t, o, stream)
		})
	}
}
