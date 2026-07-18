package ac3

import (
	"math"
	"math/rand/v2"
	"testing"
)

// imdctNaive is the inverse transform written straight from its definition,
// one sum per output sample over every coefficient. It is far too slow to
// decode with, which is the whole reason imdct.go exists, but it is short
// enough to read against the spec and it depends on nothing the fast version
// does: no FFT, no twiddle tables, no pairing, no bit reversal.
//
// That independence is the point. The fast transform is an algebraic
// rearrangement of this sum, so the two have to agree to the arithmetic's
// precision on every input, and anything the rearrangement got wrong shows up
// here rather than as a faint artefact in a decoded file.
func imdctNaive(x []float32) []float32 {
	l := len(x)
	m := l / 2
	phase := math.Pi / (4 * float64(l))
	out := make([]float32, l)
	for i := range m {
		down := phase * float64(2*l-2*i-1)
		up := phase * float64(3*l+2*i+1)
		var sd, su float64
		for j, v := range x {
			a := float64(2*j + 1)
			sd += math.Cos(a*down) * float64(v)
			su += math.Cos(a*up) * float64(v)
		}
		out[i] = float32(sd)
		out[i+m] = float32(-su)
	}
	return out
}

// imdctSizes are the two block lengths the format uses: one long transform per
// block, or two short ones when the encoder flagged a transient.
var imdctSizes = []int{longMants, shortMants}

// TestIMDCTMatchesTheDefinition runs the fast transform and the definition
// over the same coefficients and checks they agree.
func TestIMDCTMatchesTheDefinition(t *testing.T) {
	for _, l := range imdctSizes {
		t.Run(sizeName(l), func(t *testing.T) {
			tr := newIMDCT(l)
			r := rand.New(rand.NewPCG(1, uint64(l)))
			x := make([]float32, l)
			got := make([]float32, l)

			for trial := range 8 {
				for i := range x {
					x[i] = float32(r.NormFloat64())
				}
				tr.run(x, got)
				want := imdctNaive(x)

				var peak float64
				for _, v := range want {
					peak = max(peak, math.Abs(float64(v)))
				}
				for i := range want {
					if d := math.Abs(float64(got[i] - want[i])); d > 1e-4*peak {
						t.Fatalf("trial %d sample %d: fast %v, the definition says %v (peak %v)",
							trial, i, got[i], want[i], peak)
					}
				}
			}
		})
	}
}

// TestIMDCTMatchesTheDefinitionOnImpulses checks the same thing one
// coefficient at a time. A single coefficient produces a pure cosine across
// the whole block, so each of these pins one basis function of the transform
// on its own: a twiddle indexed one place out moves exactly one of them and
// would hide inside the average of a random input.
func TestIMDCTMatchesTheDefinitionOnImpulses(t *testing.T) {
	for _, l := range imdctSizes {
		t.Run(sizeName(l), func(t *testing.T) {
			tr := newIMDCT(l)
			x := make([]float32, l)
			got := make([]float32, l)

			for k := range l {
				clear(x)
				x[k] = 1
				tr.run(x, got)
				want := imdctNaive(x)
				for i := range want {
					if d := math.Abs(float64(got[i] - want[i])); d > 1e-4 {
						t.Fatalf("coefficient %d, sample %d: fast %v, the definition says %v",
							k, i, got[i], want[i])
					}
				}
			}
		})
	}
}

// TestIMDCTRejectsNothingSilently is a guard on the buffers: run must fill
// every output sample it promises and touch nothing past them.
func TestIMDCTRejectsNothingSilently(t *testing.T) {
	for _, l := range imdctSizes {
		tr := newIMDCT(l)
		x := make([]float32, l)
		for i := range x {
			x[i] = 1
		}
		out := make([]float32, l+8)
		for i := range out {
			out[i] = 12345
		}
		tr.run(x, out[:l])
		for i := l; i < len(out); i++ {
			if out[i] != 12345 {
				t.Fatalf("size %d: run wrote past its output at %d", l, i)
			}
		}
	}
}

func sizeName(l int) string {
	if l == longMants {
		return "long block"
	}
	return "short block"
}
