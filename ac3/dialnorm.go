package ac3

import "math"

// Dialogue normalization, clause 7.7.2 (the dialnorm field, clause 5.4.2.8).
//
// Every AC-3 programme carries the level its dialogue was mixed at, in dB below
// full scale. It is a measurement, not a gain: nothing in the coefficients has
// been scaled by it, and two programmes that state -31 and -20 dB are equally
// loud in the bit stream while their dialogue sits eleven decibels apart. It
// exists so that a decoder can put them back on the same footing, which is what
// a listener expects when a channel cuts from a film to an advertisement.
//
// Doing that is a decision this package leaves to its caller, because there is
// no answer that is right for everyone and because the reference declines to
// make it too: it applies no dialogue normalization unless it is asked for one,
// and a decoder that quietly applied it would disagree with the reference on
// the loudness of every real stream. So the zero value is off, the samples come
// out as the bit stream carries them, and SetTargetLevel turns it on.

// targetLevelOff is the target level that means "leave the level alone". It is
// zero so that it is the zero value of the field, which makes an unconfigured
// Decoder agree with the reference.
const targetLevelOff = 0

// SetTargetLevel makes the decoder normalize every programme so that its
// dialogue lands at dbfs decibels below full scale, whatever level it was mixed
// at. Zero, the default, applies nothing at all.
//
// The gain is 2^((dbfs - dialnorm)/6), which is the reference's arithmetic
// rather than the exact 10^((dbfs - dialnorm)/20): it treats a factor of two as
// six decibels, which it is not, quite. The difference is 0,03 dB per factor of
// two and would be beneath notice were it not that agreeing with the reference
// to the last bit is the whole point of comparing against it. It is one of the
// two, and this is the one it uses.
//
// Passing -31 asks for what the format was designed around: dialogue at -31 dB
// full scale, which is the level a stream that states -31 is already at, so
// that stream comes out untouched and every other one is pulled onto it.
//
// Values outside -31 to 0 are clamped to that range, which is the range the
// dialnorm field itself can express: there is no programme a wider target could
// be about.
func (d *Decoder) SetTargetLevel(dbfs int) {
	d.targetLevel = min(max(dbfs, -31), 0)
	d.levelGainsFor = 0 // force a recompute: the header has not changed but the target has
}

// updateLevelGains recomputes the per programme gain for the current header. It
// is called once a frame rather than once a block, and it skips the work when
// the header says what the last one said, which is what every real stream does:
// the dialogue level of a programme does not move.
func (d *Decoder) updateLevelGains() {
	if d.targetLevel == targetLevelOff {
		d.levelGains = [2]float32{1, 1}
		return
	}
	// Both programmes' levels in one key, so that a change in either is a
	// change in the key. Neither field is wider than five bits.
	key := uint16(d.h.Dialnorm)<<8 | uint16(d.h.Dialnorm2) | 1<<15
	if key == d.levelGainsFor {
		return
	}
	d.levelGainsFor = key
	d.levelGains[0] = levelGain(d.targetLevel, d.h.DialnormDB())
	d.levelGains[1] = levelGain(d.targetLevel, d.h.Dialnorm2DB())
}

// levelGain is the gain that moves dialogue mixed at dialnorm dBFS to target
// dBFS. Both are negative and the gain is a boost exactly when the programme is
// quieter than the target.
func levelGain(target, dialnorm int) float32 {
	return float32(math.Pow(2, float64(target-dialnorm)/6))
}

// dialogueGain returns the dialogue normalization gain for a channel.
//
// The dual mono mode carries two programmes with a dialogue level each, and the
// channels are the programmes, so a channel takes its own. It crosses the same
// way the dynamic range gain does, for the same reason: the reference numbers
// the two programmes in the order opposite to the channels, and this comparison
// is against the reference.
func (d *Decoder) dialogueGain(ch int) float32 {
	if d.targetLevel == targetLevelOff {
		return 1
	}
	if d.h.Acmod == AcmodDualMono && ch < 2 {
		return d.levelGains[1-ch]
	}
	return d.levelGains[0]
}
