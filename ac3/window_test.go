package ac3

import "testing"

// specWindow is Table 6.33 verbatim, the transform window as the spec prints
// it: 256 values to five decimals, laid out ten to a row the way the table is
// (addr = 10*A + B).
//
// This is the external truth the computed window is checked against. Five
// decimals is too coarse to decode with, which is why the decoder computes the
// window rather than reading it out of here, but it is more than enough to
// catch a wrong shape parameter, a normalisation off by a factor, or a window
// built backwards.
var specWindow = [256]float32{
	0.00014, 0.00024, 0.00037, 0.00051, 0.00067, 0.00086, 0.00107, 0.00130, 0.00157, 0.00187,
	0.00220, 0.00256, 0.00297, 0.00341, 0.00390, 0.00443, 0.00501, 0.00564, 0.00632, 0.00706,
	0.00785, 0.00871, 0.00962, 0.01061, 0.01166, 0.01279, 0.01399, 0.01526, 0.01662, 0.01806,
	0.01959, 0.02121, 0.02292, 0.02472, 0.02662, 0.02863, 0.03073, 0.03294, 0.03527, 0.03770,
	0.04025, 0.04292, 0.04571, 0.04862, 0.05165, 0.05481, 0.05810, 0.06153, 0.06508, 0.06878,
	0.07261, 0.07658, 0.08069, 0.08495, 0.08935, 0.09389, 0.09859, 0.10343, 0.10842, 0.11356,
	0.11885, 0.12429, 0.12988, 0.13563, 0.14152, 0.14757, 0.15376, 0.16011, 0.16661, 0.17325,
	0.18005, 0.18699, 0.19407, 0.20130, 0.20867, 0.21618, 0.22382, 0.23161, 0.23952, 0.24757,
	0.25574, 0.26404, 0.27246, 0.28100, 0.28965, 0.29841, 0.30729, 0.31626, 0.32533, 0.33450,
	0.34376, 0.35311, 0.36253, 0.37204, 0.38161, 0.39126, 0.40096, 0.41072, 0.42054, 0.43040,
	0.44030, 0.45023, 0.46020, 0.47019, 0.48020, 0.49022, 0.50025, 0.51028, 0.52031, 0.53033,
	0.54033, 0.55031, 0.56026, 0.57019, 0.58007, 0.58991, 0.59970, 0.60944, 0.61912, 0.62873,
	0.63827, 0.64774, 0.65713, 0.66643, 0.67564, 0.68476, 0.69377, 0.70269, 0.71150, 0.72019,
	0.72877, 0.73723, 0.74557, 0.75378, 0.76186, 0.76981, 0.77762, 0.78530, 0.79283, 0.80022,
	0.80747, 0.81457, 0.82151, 0.82831, 0.83496, 0.84145, 0.84779, 0.85398, 0.86001, 0.86588,
	0.87160, 0.87716, 0.88257, 0.88782, 0.89291, 0.89785, 0.90264, 0.90728, 0.91176, 0.91610,
	0.92028, 0.92432, 0.92822, 0.93197, 0.93558, 0.93906, 0.94240, 0.94560, 0.94867, 0.95162,
	0.95444, 0.95713, 0.95971, 0.96217, 0.96451, 0.96674, 0.96887, 0.97089, 0.97281, 0.97463,
	0.97635, 0.97799, 0.97953, 0.98099, 0.98236, 0.98366, 0.98488, 0.98602, 0.98710, 0.98811,
	0.98905, 0.98994, 0.99076, 0.99153, 0.99225, 0.99291, 0.99353, 0.99411, 0.99464, 0.99513,
	0.99558, 0.99600, 0.99639, 0.99674, 0.99706, 0.99736, 0.99763, 0.99788, 0.99811, 0.99831,
	0.99850, 0.99867, 0.99882, 0.99895, 0.99908, 0.99919, 0.99929, 0.99938, 0.99946, 0.99953,
	0.99959, 0.99965, 0.99969, 0.99974, 0.99978, 0.99981, 0.99984, 0.99986, 0.99988, 0.99990,
	0.99992, 0.99993, 0.99994, 0.99995, 0.99996, 0.99997, 0.99998, 0.99998, 0.99998, 0.99999,
	0.99999, 0.99999, 0.99999, 1.00000, 1.00000, 1.00000, 1.00000, 1.00000, 1.00000, 1.00000,
	1.00000, 1.00000, 1.00000, 1.00000, 1.00000, 1.00000,
}

// TestWindowMatchesTheSpecTable checks the computed window against every
// printed value. The tolerance is half of the last printed digit: that is the
// most the table can pin down, and the computed value has to round to it.
func TestWindowMatchesTheSpecTable(t *testing.T) {
	for i, want := range specWindow {
		if got := window[i]; got < want-0.5e-5 || got > want+0.5e-5 {
			t.Errorf("window[%d] = %.8f, the spec prints %.5f", i, got, want)
		}
	}
}

// TestWindowSatisfiesPrincenBradley checks the property the filter bank
// actually depends on: the two overlapping halves of the window have to add to
// exactly one in power, or the aliasing the transform introduces would not
// cancel and every block boundary would be audible.
//
// This is a stronger statement than the table check and an independent one: it
// holds to the precision the decoder computes at, not to the five decimals the
// spec had room to print.
func TestWindowSatisfiesPrincenBradley(t *testing.T) {
	for n := range windowLen {
		a := float64(window[n])
		b := float64(window[windowLen-1-n])
		if sum := a*a + b*b; sum < 1-1e-6 || sum > 1+1e-6 {
			t.Fatalf("w[%d]^2 + w[%d]^2 = %.9f, want 1", n, windowLen-1-n, sum)
		}
	}
}

// TestWindowRises checks the window goes up and stays inside [0, 1]. A window
// built from a running sum can only do this, so a failure here means the sum
// or the normalisation is wrong rather than the shape.
func TestWindowRises(t *testing.T) {
	for n := range windowLen {
		if window[n] < 0 || window[n] > 1 {
			t.Fatalf("w[%d] = %v is outside [0, 1]", n, window[n])
		}
		if n > 0 && window[n] < window[n-1] {
			t.Fatalf("w[%d] = %v is below w[%d] = %v", n, window[n], n-1, window[n-1])
		}
	}
}
