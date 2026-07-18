package e2e

import (
	"fmt"
	"math"
	"strings"
)

// Comparing decoded PCM against a reference.
//
// The comparison is sample against sample at zero offset: a decoder that is
// right but late is wrong, because the frames it produces have to land on the
// same time line as the ones it replaced.
//
// Bit exactness is not always available, and pretending otherwise would make
// the harness lie. AC-3 fills the mantissas it allocated no bits to (bap 0)
// with noise generated locally by the decoder. That noise is not carried in
// the stream, so two correct decoders disagree on it by design. Where a stream
// uses it, the only honest bar is an energy bound on the difference; where it
// does not, samples must line up to within the rounding of the last stage.
// Hence two tolerances, and a caller that has to say which one applies.

// A Tolerance is the bar a comparison has to clear.
type Tolerance struct {
	// MaxLSB is the largest difference allowed on any single sample, in units
	// of the least significant bit of the compared depth.
	MaxLSB int

	// MaxRMSLSB bounds the root mean square of the difference over the whole
	// buffer, in LSB. Zero means unbounded, which only makes sense when MaxLSB
	// already pins every sample.
	MaxRMSLSB float64

	// MaxExceedFraction is the fraction of samples allowed to exceed MaxLSB.
	// Zero means none may.
	MaxExceedFraction float64
}

// The tolerances this project compares at.
var (
	// Exact demands identical samples. It is what a round trip through an
	// encoder owes, and what two runs of this decoder owe each other.
	Exact = Tolerance{}

	// QuasiExact allows the last bit of rounding to differ, and nothing more.
	// It applies to streams and channels that do not use the bap 0 dither,
	// where two correct decoders must otherwise agree.
	QuasiExact = Tolerance{MaxLSB: 2}

	// Dithered is for streams that do use the bap 0 dither. The noise is
	// bounded in energy but not per sample, so the bar is an RMS one, with a
	// per-sample ceiling loose enough to admit the noise and tight enough to
	// catch a real defect.
	Dithered = Tolerance{MaxLSB: 512, MaxRMSLSB: 64, MaxExceedFraction: 0.01}
)

// String renders the tolerance as the bar it sets.
func (tol Tolerance) String() string {
	var parts []string
	parts = append(parts, fmt.Sprintf("max %d LSB", tol.MaxLSB))
	if tol.MaxRMSLSB > 0 {
		parts = append(parts, fmt.Sprintf("rms %.1f LSB", tol.MaxRMSLSB))
	}
	if tol.MaxExceedFraction > 0 {
		parts = append(parts, fmt.Sprintf("up to %.2f%% over", tol.MaxExceedFraction*100))
	}
	return strings.Join(parts, ", ")
}

// A Result is what a comparison found, whether or not it passed.
type Result struct {
	Samples int   // samples compared, per channel times channels
	MaxLSB  int64 // largest difference on any one sample; int64 because
	// two full-scale samples of opposite sign differ by more than a 32-bit
	// int holds, and a comparison harness must not overflow on the exact
	// case it exists to catch
	MaxAt     int     // index of that sample, or -1
	RMSLSB    float64 // root mean square of the difference
	Exceeding int     // samples that exceeded Tolerance.MaxLSB
	FirstBad  int     // index of the first such sample, or -1
	Equal     bool    // every sample identical
}

// String renders the result as a line fit for a test failure.
func (r Result) String() string {
	if r.Equal {
		return fmt.Sprintf("%d samples, identical", r.Samples)
	}
	return fmt.Sprintf("%d samples, max %d LSB at %d, rms %.2f LSB, %d over (%.3f%%), first at %d",
		r.Samples, r.MaxLSB, r.MaxAt, r.RMSLSB, r.Exceeding,
		100*float64(r.Exceeding)/math.Max(1, float64(r.Samples)), r.FirstBad)
}

// Compare measures got against want, sample against sample at zero offset, and
// reports whether the difference clears tol.
//
// A length mismatch is a failure with no result to report: the two buffers do
// not describe the same span of time, and comparing their overlap would hide
// exactly the bug that produced the mismatch.
func Compare(got, want []int32, tol Tolerance) (Result, error) {
	if len(got) != len(want) {
		return Result{}, fmt.Errorf("decoded %d samples, reference has %d: %s",
			len(got), len(want), lengthHint(len(got), len(want)))
	}

	r := Result{Samples: len(got), MaxAt: -1, FirstBad: -1, Equal: true}
	var sumSquares float64
	for i := range got {
		d := int64(got[i]) - int64(want[i])
		if d != 0 {
			r.Equal = false
		}
		if d < 0 {
			d = -d
		}
		if d > r.MaxLSB {
			r.MaxLSB, r.MaxAt = d, i
		}
		if d > int64(tol.MaxLSB) {
			r.Exceeding++
			if r.FirstBad < 0 {
				r.FirstBad = i
			}
		}
		sumSquares += float64(d) * float64(d)
	}
	if len(got) > 0 {
		r.RMSLSB = math.Sqrt(sumSquares / float64(len(got)))
	}

	if err := check(r, tol); err != nil {
		return r, err
	}
	return r, nil
}

func check(r Result, tol Tolerance) error {
	allowed := int(tol.MaxExceedFraction * float64(r.Samples))
	if r.Exceeding > allowed {
		return fmt.Errorf("%d samples differ by more than %d LSB (%d allowed): %s",
			r.Exceeding, tol.MaxLSB, allowed, r)
	}
	if tol.MaxRMSLSB > 0 && r.RMSLSB > tol.MaxRMSLSB {
		return fmt.Errorf("rms difference %.2f LSB exceeds %.2f: %s", r.RMSLSB, tol.MaxRMSLSB, r)
	}
	return nil
}

// lengthHint turns a length mismatch into the likeliest cause, since that is
// what the reader wants to know next.
func lengthHint(got, want int) string {
	d := got - want
	if d < 0 {
		d = -d
	}
	switch {
	case want == 0:
		return "the reference decoded nothing"
	case got == 0:
		return "this decoder produced nothing"
	case d%1536 == 0:
		return fmt.Sprintf("a whole number of frames apart (%d)", d/1536)
	case d%256 == 0:
		return fmt.Sprintf("a whole number of blocks apart (%d)", d/256)
	default:
		return fmt.Sprintf("%d samples apart", d)
	}
}

// FindOffset returns the lag, within plus or minus maxLag, at which got best
// matches want, and whether that lag is zero.
//
// It is a diagnostic, not a comparison: Compare demands zero offset. When
// Compare fails on a decoder that sounds right, this says whether the transform
// stage is delaying its output, which is the usual reason.
func FindOffset(got, want []int32, maxLag int) (lag int, aligned bool) {
	n := min(len(got), len(want))
	if n == 0 {
		return 0, true
	}
	// Compare over a window that every candidate lag can cover.
	window := min(n-min(maxLag, n-1), 1<<16)
	if window <= 0 {
		return 0, true
	}

	best, bestErr := 0, math.Inf(1)
	for lag := -maxLag; lag <= maxLag; lag++ {
		var sum float64
		count := 0
		for i := range window {
			j := i + lag
			if j < 0 || j >= len(want) || i >= len(got) {
				continue
			}
			d := float64(got[i]) - float64(want[j])
			sum += d * d
			count++
		}
		if count == 0 {
			continue
		}
		if err := sum / float64(count); err < bestErr {
			best, bestErr = lag, err
		}
	}
	return best, best == 0
}

// Deinterleave splits interleaved samples into one slice per channel, which is
// what a per-channel comparison needs: the bap 0 dither is decided per channel,
// so a stream can be exactly comparable on some channels and not others.
func Deinterleave(samples []int32, channels int) [][]int32 {
	if channels <= 0 {
		return nil
	}
	n := len(samples) / channels
	out := make([][]int32, channels)
	for c := range out {
		out[c] = make([]int32, n)
		for i := range n {
			out[c][i] = samples[i*channels+c]
		}
	}
	return out
}
