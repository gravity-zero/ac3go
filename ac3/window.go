package ac3

import "math"

// The transform window, clause 6.9.3 and Table 6.33.
//
// The filter bank overlaps each block with the next by half a block, so every
// output sample is the sum of two windowed halves. For that sum to reconstruct
// the signal rather than a lumpy version of it, the window has to satisfy
// w[n]^2 + w[255-n]^2 = 1: the two halves' contributions add to exactly one
// everywhere. That is the Princen-Bradley condition, and it is what makes the
// aliasing the transform introduces cancel out.
//
// The spec prints the window as a table of 256 values to five decimals. Five
// decimals is not enough to decode with: the reference decoders compute the
// window instead, and so does this one. The table is what the computation is
// checked against.

// windowLen is the length of the printed window, one half of the 512 sample
// transform. The other half is its mirror.
const windowLen = 256

// kbdAlpha is the window's shape parameter (clause 6.9.3). It trades the width
// of the filter's main lobe against how far down its side lobes sit, and 5 is
// what the format settled on.
const kbdAlpha = 5.0

// window is the Kaiser-Bessel derived window of Table 6.33.
var window = kbdWindow(windowLen, kbdAlpha)

// kbdWindow builds the rising half of a Kaiser-Bessel derived window of 2n
// points.
//
// A KBD window is the normalised running sum of a Kaiser window, square
// rooted. The running sum is what buys the Princen-Bradley condition: taking
// the sum up to n and the sum from n to the end gives two numbers that add to
// the whole, so the square roots square back to one.
func kbdWindow(n int, alpha float64) [windowLen]float32 {
	// The Kaiser window's own shape, sampled over half its length. The
	// argument is the standard pi*alpha*sqrt(1-x^2) written so that it stays
	// in terms of integers for as long as possible.
	alpha2 := 4 * (alpha * math.Pi / float64(n)) * (alpha * math.Pi / float64(n))
	kaiser := make([]float64, n/2+1)
	total := 0.0
	for i := range kaiser {
		kaiser[i] = besselI0(math.Sqrt(float64(i) * float64(n-i) * alpha2))
		// Every term but the first and the last stands for two points of the
		// full window, which is symmetric about its middle.
		if i > 0 && i < n/2 {
			total += kaiser[i]
		}
		total += kaiser[i]
	}
	// The point past the end, where the Kaiser window is I0(0) = 1.
	total++

	var out [windowLen]float32
	sum := 0.0
	for i := 0; i <= n/2; i++ {
		sum += kaiser[i]
		out[i] = float32(math.Sqrt(sum / total))
	}
	for i := n/2 + 1; i < n; i++ {
		sum += kaiser[n-i]
		out[i] = float32(math.Sqrt(sum / total))
	}
	return out
}

// besselI0 is the zeroth order modified Bessel function of the first kind,
// summed until the terms stop moving the total. The series converges quickly
// over the range the window uses, and the terms are all positive, so there is
// no cancellation to worry about.
func besselI0(x float64) float64 {
	q := x * x / 4
	term, sum := 1.0, 1.0
	for i := 1; ; i++ {
		term *= q / (float64(i) * float64(i))
		next := sum + term
		if next == sum {
			return sum
		}
		sum = next
	}
}
