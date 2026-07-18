package e2e

import (
	"path"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/pcm"
)

// The downmix is where a 5.1 stream meets a pair of speakers, and it is the
// last thing in the decode that a listener hears as loudness rather than as
// detail: get the coefficients right and the level wrong and every channel is
// perfect while the film is too quiet.
//
// It is also the one part of the decode where the spec offers a choice rather
// than an answer - what to scale the coefficients by so the sum cannot overflow
// - so a test against the reference is the only thing that pins which of the
// conforming answers this decoder gives. See ac3/downmix.go for which and why.
//
// One trap is worth naming because it is easy to fall into and quiet when you
// do: `-ac 2` does NOT ask the reference for this downmix. It asks its
// resampler for a generic one afterwards, which uses its own coefficients,
// ignores what the stream states, and does not attenuate at all. The flag that
// asks the AC-3 decoder to do its own is `-downmix`, and the difference between
// them is 6 dB of level on a real stream - measured, not guessed.

// findTracks returns up to maxTracks AC-3 tracks with the given channel count.
func findTracks(t testing.TB, o *Oracle, corpus string, channels, maxTracks int) []stereoTrack {
	t.Helper()
	var out []stereoTrack
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.AC3Tracks(t, f) {
			if tr.Channels != channels {
				continue
			}
			out = append(out, stereoTrack{f, tr.Index, tr.SampleRate})
			if len(out) >= maxTracks {
				return out
			}
		}
	}
	return out
}

// TestDownmix51AgainstReference folds real 5.1 streams down with both decoders
// and compares the result.
//
// Mono is here as well as stereo because it is not stereo with a channel
// dropped: it is the two stereo outputs summed and pulled 3 dB down so the sum
// cannot overflow, so it exercises a step that the stereo path never reaches.
func TestDownmix51AgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	tracks := findTracks(t, o, corpus, 6, 3)
	if len(tracks) == 0 {
		t.Skip("no 5.1 AC-3 track in the corpus")
	}

	targets := []struct {
		name     string
		layout   pcm.Layout
		channels int
	}{
		{"stereo", pcm.LayoutStereo, 2},
		{"mono", pcm.LayoutMono, 1},
	}
	for _, tr := range tracks {
		for _, target := range targets {
			t.Run(path.Base(tr.file)+"/"+target.name, func(t *testing.T) {
				stream := o.ExtractSpan(t, tr.file, tr.index, 30, 4)
				if len(stream) == 0 {
					t.Skip("nothing extracted")
				}

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

				res, err := Compare(got, want, Dithered)
				if err != nil {
					t.Fatalf("%d Hz to %s: %v", tr.rate, target.name, err)
				}
				t.Logf("%d Hz to %s: %s", tr.rate, target.name, res)
			})
		}
	}
}
