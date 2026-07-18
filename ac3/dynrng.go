package ac3

// Dynamic range control, clause 7.7.1.
//
// An encoder measures how far each block strays from the programme's dialogue
// level and sends a gain that pulls it back: quiet passages up, loud ones
// down, up to 24 dB either way. It is metadata, not coding - the coefficients
// are complete without it - and a decoder that ignores it still reconstructs
// the signal, just with the dynamics the mix was made with rather than the
// ones the encoder asked for.
//
// It is applied anyway, and it has to be, because the reference applies it:
// two decoders that disagree here disagree on the loudness of every stream
// that carries a gain, which is exactly what a listener would hear.

// dynrngGains maps the 8 bit dynrng code to its gain.
//
// The code is a 3 bit exponent and a 5 bit mantissa. The exponent is signed in
// two's complement, which is what the subtraction of eight when the top bit is
// set does, and the mantissa carries an implicit leading one, which is what the
// or with 0x20 does. Together they cover 1/16 to just under 16, that is -24 to
// +24 dB.
var dynrngGains = func() (out [256]float32) {
	for i := range out {
		exp := i>>5 - i>>7<<3 - 5
		mant := i&0x1f | 0x20
		g := float32(mant)
		if exp >= 0 {
			g *= float32(uint32(1) << uint(exp))
		} else {
			g /= float32(uint32(1) << uint(-exp))
		}
		out[i] = g
	}
	return out
}()

// dynrngNone is the code for a gain of one: a zero exponent, which the bias
// turns into 2^-5, against the smallest mantissa the implicit leading one
// allows, which is 32. A block that sends no gain and has none to inherit uses
// it.
//
// It is code zero and not, as it looks like it should be, the code whose
// exponent field reads 5: the exponent is signed, so a field of 5 has its top
// bit set and means -3, which is a gain of an eighth rather than of one.
const dynrngNone = 0

// dynamicRange returns the gain a channel's block is scaled by.
//
// The dual mono mode carries two independent programmes and a gain for each,
// so which one a channel takes depends on which programme it belongs to. Every
// other mode is one programme and one gain.
func (d *Decoder) dynamicRange(ch int) float32 {
	if d.h.Acmod == AcmodDualMono && ch < 2 {
		return dynrngGains[d.dynrng[1-ch]]
	}
	return dynrngGains[d.dynrng[0]]
}
