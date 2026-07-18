package ac3

import (
	"math"
	"testing"
)

// TestDynrngNoneIsUnity pins the code that means "leave this block alone".
//
// It is worth its own test because the obvious guess is wrong and the mistake
// is quiet: the exponent field is signed, so the code whose exponent reads 5
// is a gain of an eighth, not of one. A decoder that used it would attenuate
// by 18 dB every stream that does not send a gain in its first block, which is
// most of them, and nothing but a comparison against another decoder would say
// so.
func TestDynrngNoneIsUnity(t *testing.T) {
	if g := dynrngGains[dynrngNone]; g != 1 {
		t.Fatalf("dynrngGains[dynrngNone] = %v, want exactly 1", g)
	}
}

// TestDynrngGains checks the table against the range the format promises and
// against a handful of codes worked out by hand.
func TestDynrngGains(t *testing.T) {
	// The control has to cover +/-24 dB (clause 7.7.1), and nothing past it.
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, g := range dynrngGains {
		lo = math.Min(lo, float64(g))
		hi = math.Max(hi, float64(g))
	}
	if db := 20 * math.Log10(hi); db < 23.5 || db > 24 {
		t.Errorf("the loudest gain is %+.2f dB, want just under +24", db)
	}
	if db := 20 * math.Log10(lo); db > -23.5 || db < -24.1 {
		t.Errorf("the quietest gain is %+.2f dB, want about -24", db)
	}

	tests := []struct {
		code uint8
		want float32
	}{
		{0, 1},            // exponent 0 -> 2^-5, mantissa 32: unity
		{0x1f, 63.0 / 32}, // the largest mantissa at the same exponent
		{0x20, 2},         // one exponent step up doubles it
		{0xe0, 0.5},       // exponent field 7, that is -1 signed: 32 * 2^-6
		{0x80, 0.0625},    // exponent field 4, that is -4 signed: the quietest
		{0x7f, 15.75},     // exponent field 3 and every mantissa bit: the loudest
	}
	for _, tt := range tests {
		if got := dynrngGains[tt.code]; got != tt.want {
			t.Errorf("dynrngGains[%#02x] = %v, want %v", tt.code, got, tt.want)
		}
	}

	// The gain has to rise with the code inside one exponent's run: the
	// mantissa is the low five bits.
	for base := 0; base < 256; base += 32 {
		for i := 1; i < 32; i++ {
			if dynrngGains[base+i] <= dynrngGains[base+i-1] {
				t.Fatalf("gain %#02x is not above %#02x within one exponent",
					base+i, base+i-1)
			}
		}
	}
}
