package ac3

import "math"

// The inverse transform, clause 6.9.4.
//
// Each block codes its samples as the coefficients of a modified discrete
// cosine transform, and getting them back means running that transform
// backwards. Done from the definition that is a sum over every coefficient for
// every output sample, which for a 5.1 frame is two and a half million
// multiplies: an order of magnitude past the whole decoder's time budget.
//
// So it is done the way every decoder does it, and the way the spec's own
// clause 6.9.4 lays out: the transform of L coefficients is rearranged into a
// complex FFT of L/2 points with a rotation before it and a rotation after it.
// The rearrangement is exact, not an approximation - what comes out is the
// definition's answer to the last bit of the arithmetic, which is what
// imdct_test.go pins by running both and comparing.

// maxFFT is the largest complex transform the filter bank needs: a long
// block's 256 coefficients go through a 128 point FFT.
const maxFFT = 128

// imdct is the inverse transform for one block length. It holds its own
// twiddle factors and working buffers, so running it allocates nothing.
type imdct struct {
	l int // coefficients in, samples out
	m int // l/2, the complex FFT's size

	rev  []uint8   // the FFT's bit reversal permutation
	fcos []float32 // the FFT's twiddle factors
	fsin []float32

	// The rotation applied before the FFT and again after it. Both use the
	// same angles, which is why there is one table and not two.
	tcos []float32
	tsin []float32

	re []float32 // the FFT's working buffers
	im []float32
}

// newIMDCT builds the transform for blocks of l coefficients.
func newIMDCT(l int) *imdct {
	m := l / 2
	t := &imdct{
		l: l, m: m,
		rev:  make([]uint8, m),
		fcos: make([]float32, m/2),
		fsin: make([]float32, m/2),
		tcos: make([]float32, m),
		tsin: make([]float32, m),
		re:   make([]float32, m),
		im:   make([]float32, m),
	}

	bits := 0
	for 1<<bits < m {
		bits++
	}
	for i := range t.rev {
		r := 0
		for b := range bits {
			r |= (i >> b & 1) << (bits - 1 - b)
		}
		t.rev[i] = uint8(r)
	}

	for k := range t.fcos {
		a := 2 * math.Pi * float64(k) / float64(m)
		t.fcos[k] = float32(math.Cos(a))
		t.fsin[k] = float32(math.Sin(a))
	}
	// The eighth of a bin offset is what makes this the transform's rotation
	// rather than a plain FFT's: it is the half sample shift the MDCT's
	// cosines carry, folded into a phase.
	n := 2 * l
	for k := range t.tcos {
		a := 2 * math.Pi * (float64(k) + 0.125) / float64(n)
		t.tcos[k] = float32(math.Cos(a))
		t.tsin[k] = float32(math.Sin(a))
	}
	return t
}

// run computes out[0:l] from the coefficients x[0:l].
func (t *imdct) run(x, out []float32) {
	m := t.m
	x, out = x[:t.l], out[:t.l]

	// Pre-rotation. The coefficients are paired from the two ends of the
	// block into one complex number, which is what halves the FFT's size.
	for k := range m {
		re := x[t.l-1-2*k]
		im := x[2*k]
		c, s := t.tcos[k], t.tsin[k]
		t.re[k] = re*c - im*s
		t.im[k] = re*s + im*c
	}

	t.fft()

	// Post-rotation, then unpack: the FFT's m complex outputs hold the
	// transform's l real ones, the even samples in the real parts and the odd
	// samples in the imaginary parts, running the other way.
	for k := range m {
		re, im := t.re[k], t.im[k]
		c, s := t.tcos[k], t.tsin[k]
		t.re[k] = re*c - im*s
		t.im[k] = re*s + im*c
	}
	for k := range m {
		out[2*k] = -t.re[k]
		out[2*k+1] = t.im[m-1-k]
	}
}

// fft is a decimation in time complex FFT, in place over re and im, with the
// positive sign convention and no scaling.
func (t *imdct) fft() {
	m := t.m
	for i := range m {
		if j := int(t.rev[i]); j > i {
			t.re[i], t.re[j] = t.re[j], t.re[i]
			t.im[i], t.im[j] = t.im[j], t.im[i]
		}
	}
	for size := 2; size <= m; size <<= 1 {
		half := size >> 1
		step := m / size
		for i := 0; i < m; i += size {
			k := 0
			for j := i; j < i+half; j++ {
				c, s := t.fcos[k], t.fsin[k]
				xr := t.re[j+half]*c - t.im[j+half]*s
				xi := t.re[j+half]*s + t.im[j+half]*c
				t.re[j+half] = t.re[j] - xr
				t.im[j+half] = t.im[j] - xi
				t.re[j] += xr
				t.im[j] += xi
				k += step
			}
		}
	}
}

// overlapAdd windows one block's transform output against the tail the block
// before it left behind, and produces the block's 256 finished samples
// (clause 6.9.4, steps 5 and 6).
//
// The transform cannot reconstruct a block on its own: what it returns carries
// the block's samples plus a mirror image of them folded back on top, the
// aliasing the encoder's own transform introduced. The fold is antisymmetric
// in one half of the window and symmetric in the other, so windowing the two
// overlapping blocks and adding them cancels it exactly and leaves the
// samples. That cancellation is what the window's Princen-Bradley property
// buys, and it is why a block's output needs its neighbour: decoded alone, a
// block is its own signal plus a reversed copy of it.
func overlapAdd(out, delay, x []float32) {
	half := windowLen / 2
	out, delay, x = out[:windowLen], delay[:half], x[:half]
	for n := range half {
		d := delay[n]
		v := x[half-1-n]
		w1 := window[n]
		w2 := window[windowLen-1-n]
		out[n] = d*w2 - v*w1
		out[windowLen-1-n] = d*w1 + v*w2
	}
}

// The two block lengths. A block always produces 256 samples: either from one
// transform of 256 coefficients, or from two of 128 when the encoder split the
// block to keep a transient's quantisation noise from smearing across it.
const (
	longMants  = 256
	shortMants = 128
)

// synthesisGain is the factor of two the overlap and add step carries (clause
// 6.9.4, step 6): the encoder scaled the signal down to leave itself headroom,
// and this is where it comes back.
const synthesisGain = 2

// synthesize turns one block's coefficients into that block's 256 samples per
// channel, and leaves each channel's tail in delay for the next block.
func (d *Decoder) synthesize(b *Block, blk int) {
	for ch := range d.h.Channels() {
		out := d.pcm[ch][blk*windowLen : (blk+1)*windowLen]
		delay := d.delay[ch][:]

		coeffs := d.blockCoeffs(b, ch)

		// A block the encoder split carries two transforms interleaved
		// coefficient by coefficient: the first half of the block and the
		// second, each of them short. The first overlaps with what came
		// before, and the second becomes the tail, so the block still hands
		// on exactly one tail however it was coded.
		if d.blockSwitched(b, ch) {
			for i := range shortMants {
				d.half[i] = coeffs[2*i]
			}
			d.short.run(d.half[:], d.tmp[:shortMants])
			overlapAdd(out, delay, d.tmp[:windowLen/2])
			for i := range shortMants {
				d.half[i] = coeffs[2*i+1]
			}
			d.short.run(d.half[:], delay)
		} else {
			d.long.run(coeffs, d.tmp[:longMants])
			overlapAdd(out, delay, d.tmp[:windowLen/2])
			copy(delay, d.tmp[windowLen/2:longMants])
		}

		for i := range out {
			out[i] *= synthesisGain
		}
	}
}

// blockCoeffs returns channel ch's coefficients, scaled by the block's dynamic
// range gain and by the dialogue normalization gain, which is one unless the
// caller asked for one.
//
// Both are gains on the whole block and could be applied to the samples, but
// they are applied here for the same reason: consecutive blocks overlap into
// the same samples, and two blocks whose gains differ have to each carry their
// own through the transform, or the crossfade between them would be at neither.
func (d *Decoder) blockCoeffs(b *Block, ch int) []float32 {
	g := d.dynamicRange(ch) * d.dialogueGain(ch)
	if g == 1 {
		return b.Coeffs[ch][:longMants]
	}
	for i, v := range b.Coeffs[ch][:longMants] {
		d.scaled[i] = v * g
	}
	return d.scaled[:longMants]
}

// blockSwitched reports whether channel ch coded this block as two short
// transforms. Only the full bandwidth channels can: the LFE has one seventh of
// a channel's bandwidth and nothing in it is fast enough to need splitting.
func (d *Decoder) blockSwitched(b *Block, ch int) bool {
	return ch < MaxFBWChannels && ch != d.lfeCh && b.Blksw[ch]
}
