package ac3

import (
	"math"
	"testing"
)

// TestTargetLevelOffIsTheDefault pins the decision that a fresh Decoder changes
// no levels. It is not a detail: the reference applies no dialogue
// normalization unless asked, so a Decoder that applied it out of the box would
// disagree with the reference on the loudness of every real stream, and every
// comparison in internal/e2e would have to compensate for it.
func TestTargetLevelOffIsTheDefault(t *testing.T) {
	d := NewDecoder()
	if d.targetLevel != targetLevelOff {
		t.Errorf("a new Decoder has target level %d, want %d", d.targetLevel, targetLevelOff)
	}
	for _, dialnorm := range []uint8{1, 10, 20, 31} {
		d.h.Dialnorm = dialnorm
		d.updateLevelGains()
		if g := d.dialogueGain(0); g != 1 {
			t.Errorf("dialnorm %d: gain %v with no target set, want 1", dialnorm, g)
		}
	}
}

// TestLevelGainLaw checks the arithmetic against hand-computed values.
//
// The law is the reference's: a factor of two per six decibels, not the exact
// 10^(dB/20). The two agree only at zero, and the point of the last row is that
// they do not agree anywhere else - it would be an easy and invisible thing to
// write the textbook version instead.
func TestLevelGainLaw(t *testing.T) {
	for _, c := range []struct {
		target, dialnorm int
		want             float64
	}{
		// A stream already at the target is left alone, whatever the target.
		{-31, -31, 1},
		{-20, -20, 1},
		// Six decibels is a factor of two, exactly, by construction.
		{-25, -31, 2},
		{-31, -25, 0.5},
		// Quieter than the target means a boost; louder means a cut.
		{-20, -31, 3.5636},
		{-31, -20, 0.2806},
	} {
		got := float64(levelGain(c.target, c.dialnorm))
		if math.Abs(got-c.want) > 1e-4 {
			t.Errorf("levelGain(target %d, dialnorm %d) = %v, want %v",
				c.target, c.dialnorm, got, c.want)
		}
	}

	// The textbook law would put this at 3.5481. The difference is what the
	// end to end comparison against the reference is tuned to catch, and this
	// is the same claim stated where it can be read.
	if g := float64(levelGain(-20, -31)); math.Abs(g-math.Pow(10, 11.0/20)) < 1e-3 {
		t.Errorf("levelGain(-20, -31) = %v, which is 10^(11/20): the law should be 2^(11/6)", g)
	}
}

// TestSetTargetLevelClamps holds the range to what the dialnorm field can say.
func TestSetTargetLevelClamps(t *testing.T) {
	d := NewDecoder()
	for _, c := range []struct{ set, want int }{
		{0, 0}, {-31, -31}, {-15, -15},
		{-40, -31}, // below anything a programme could state
		{5, 0},     // above full scale, which is off
	} {
		d.SetTargetLevel(c.set)
		if d.targetLevel != c.want {
			t.Errorf("SetTargetLevel(%d) stored %d, want %d", c.set, d.targetLevel, c.want)
		}
	}
}

// TestDialnormZeroIsMinus31 pins the reserved value's meaning through the gain
// rather than through the header alone: a stream that states 0 has to come out
// at the level a stream that states -31 comes out at, since that is what the
// value means, and treating it as "0 dB" instead would make it a 31 dB error.
func TestDialnormZeroIsMinus31(t *testing.T) {
	d := NewDecoder()
	d.SetTargetLevel(-20)

	d.h.Dialnorm = 0
	d.updateLevelGains()
	got := d.dialogueGain(0)

	d.h.Dialnorm = 31
	d.updateLevelGains()
	want := d.dialogueGain(0)

	if got != want {
		t.Errorf("dialnorm 0 gives gain %v, dialnorm 31 gives %v: the reserved value must mean -31 dB", got, want)
	}
}

// TestDialogueGainCrossesTheDualMonoProgrammes pins the channel to programme
// mapping of the one mode that has two of them. It crosses, and it crosses the
// way the dynamic range gain does: the reference numbers the programmes in the
// order opposite to the channels, and a decoder that did not cross here would
// swap the two programmes' levels and nothing else - inaudible on a stream
// whose programmes state the same level, which is most of them.
func TestDialogueGainCrossesTheDualMonoProgrammes(t *testing.T) {
	d := NewDecoder()
	d.SetTargetLevel(-31)
	d.h.Acmod = AcmodDualMono
	d.h.Dialnorm = 25  // programme 1: needs a 6 dB cut to reach -31
	d.h.Dialnorm2 = 19 // programme 2: needs 12 dB
	d.updateLevelGains()

	if g := d.dialogueGain(0); math.Abs(float64(g)-0.25) > 1e-6 {
		t.Errorf("channel 0 gain %v, want 0.25 (the second programme's)", g)
	}
	if g := d.dialogueGain(1); math.Abs(float64(g)-0.5) > 1e-6 {
		t.Errorf("channel 1 gain %v, want 0.5 (the first programme's)", g)
	}
}
