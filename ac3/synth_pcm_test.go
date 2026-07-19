package ac3

import (
	"math"
	"testing"
)

// goertzel returns the energy of x at the given frequency.
//
// It is the one bin of a DFT that this test cares about, computed on its own,
// which is both cheaper than a whole transform and shorter to read than one.
func goertzel(x []float32, hz float64, rate int) float64 {
	w := 2 * math.Pi * hz / float64(rate)
	c := 2 * math.Cos(w)
	var s1, s2 float64
	for _, v := range x {
		s := float64(v) + c*s1 - s2
		s2, s1 = s1, s
	}
	// |X|^2 from the recurrence's last two states.
	return s1*s1 + s2*s2 - c*s1*s2
}

// TestSynthesisProducesTheTone is phase 2's deliverable in the one form this
// package can check without a reference decoder: the samples coming out of the
// filter bank hold the tone that went into the encoder, in the right channel,
// at the right frequency.
//
// The coefficient tests already pin the spectrum before the transform. This
// pins the transform itself, and it is a different claim: an IMDCT with a
// twiddle out of place, a window applied backwards, or an overlap added with
// the wrong sign all leave the coefficients untouched and wreck the samples.
// Those mistakes do not produce a quiet tone, they produce a loud mess, which
// is why measuring where the energy sits catches them.
func TestSynthesisProducesTheTone(t *testing.T) {
	for _, f := range toneFixtures {
		t.Run(f.file, func(t *testing.T) {
			frames := toneFrames(t, f.file)
			d := NewDecoder()
			d.SetDither(false)

			nch := f.channels
			if f.lfe {
				nch++
			}
			// One frame per channel of accumulated samples, skipping the first
			// frame: it has no predecessor to overlap with, so its samples are
			// half a window short of the signal.
			got := make([][]float32, nch)
			for i, frame := range frames {
				if err := d.DecodeFrame(frame); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				// The first frame is the encoder settling in and the last is the
				// tone running out.
				if i == 0 || i == 1 || i >= len(frames)-1 {
					continue
				}
				for ch := range nch {
					got[ch] = append(got[ch], d.Samples(ch)...)
				}
			}

			for ch, hz := range f.hz {
				x := got[ch]
				if len(x) == 0 {
					t.Fatalf("channel %d produced no samples", ch)
				}
				at := goertzel(x, hz, f.sampleRate)
				if at == 0 {
					t.Fatalf("channel %d is silent at its own %v Hz", ch, hz)
				}

				// How loud, not just where. Every assertion below compares the
				// channel against itself, so they all hold under a flat gain
				// error: halving synthesisGain leaves every ratio intact and
				// every one of them green.
				//
				// The anchor is outside this package. The fixtures were encoded
				// from tones at exactly an eighth of full scale, which is the
				// -18 dBFS alignment level, and a sine of amplitude A over n
				// samples puts (A*n/2)^2 in its own bin. So the amplitude the
				// bin implies has to come back to an eighth.
				const wantAmp = 0.125
				if amp := 2 * math.Sqrt(at) / float64(len(x)); math.Abs(amp-wantAmp) > wantAmp*0.01 {
					t.Errorf("channel %d comes back at amplitude %.6f, want %.3f "+
						"(%.2f dB out): the filter bank has the tone but not its level",
						ch, amp, wantAmp, 20*math.Log10(amp/wantAmp))
				}
				// The tone has to dominate: every other channel's frequency,
				// and a few frequencies that belong to nothing, have to come
				// back far quieter than this channel's own.
				for other, ohz := range f.hz {
					if other == ch || ohz == hz {
						continue
					}
					if e := goertzel(x, ohz, f.sampleRate); e > at/100 {
						t.Fatalf("channel %d: %v Hz is its own tone at energy %.4g, but %v Hz "+
							"(channel %d's) comes back at %.4g, within 20 dB of it",
							ch, hz, at, ohz, other, e)
					}
				}
				for _, ohz := range []float64{hz * 2, hz / 2, hz + 3000} {
					if ohz <= 20 || ohz >= float64(f.sampleRate)/2-100 {
						continue
					}
					if e := goertzel(x, ohz, f.sampleRate); e > at/100 {
						t.Fatalf("channel %d: %v Hz comes back at %.4g against its own tone's %.4g: "+
							"the filter bank is spreading energy where the signal has none",
							ch, ohz, e, at)
					}
				}
			}
		})
	}
}

// TestResetDropsTheCarryOver checks that Reset does what decoding a segment
// depends on: two decoders handed the same frame agree only if neither is
// carrying a tail from somewhere else.
func TestResetDropsTheCarryOver(t *testing.T) {
	frames := toneFrames(t, "tones_48k_stereo_192k.ac3")

	fresh := NewDecoder()
	fresh.SetDither(false)
	if err := fresh.DecodeFrame(frames[3]); err != nil {
		t.Fatal(err)
	}
	want := append([]float32(nil), fresh.Samples(0)...)

	// A decoder that has been down the stream, then reset, has to produce
	// exactly what a new one does.
	used := NewDecoder()
	used.SetDither(false)
	for _, f := range frames[:3] {
		if err := used.DecodeFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	used.Reset()
	if err := used.DecodeFrame(frames[3]); err != nil {
		t.Fatal(err)
	}
	for i, v := range used.Samples(0) {
		if v != want[i] {
			t.Fatalf("sample %d: a reset decoder gave %v, a new one gives %v", i, v, want[i])
		}
	}

	// And without the reset it must differ, or the carry over is not carrying.
	other := NewDecoder()
	other.SetDither(false)
	for _, f := range frames[:3] {
		if err := other.DecodeFrame(f); err != nil {
			t.Fatal(err)
		}
	}
	if err := other.DecodeFrame(frames[3]); err != nil {
		t.Fatal(err)
	}
	same := true
	for i, v := range other.Samples(0) {
		if v != want[i] {
			same = false
			break
		}
		_ = i
	}
	if same {
		t.Fatal("a decoder carrying the previous frame's tail produced the same samples as a " +
			"cold one: the overlap is not reaching across frames")
	}
}
