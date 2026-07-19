package ac3

import (
	"errors"
	"math"
	"testing"

	"github.com/gravity-zero/ac3go/bitstream"
)

// TestSymQuantMatchesSpec pins the generated quantizer tables against the
// fractions clause 6.3.3 prints in tables 6.19 to 6.23, value for value.
func TestSymQuantMatchesSpec(t *testing.T) {
	// Written the way the spec writes them, as numerator over denominator.
	want := map[uint8][]struct{ num, den int }{
		1: {{-2, 3}, {0, 3}, {2, 3}},
		2: {{-4, 5}, {-2, 5}, {0, 5}, {2, 5}, {4, 5}},
		3: {{-6, 7}, {-4, 7}, {-2, 7}, {0, 7}, {2, 7}, {4, 7}, {6, 7}},
		4: {{-10, 11}, {-8, 11}, {-6, 11}, {-4, 11}, {-2, 11}, {0, 11},
			{2, 11}, {4, 11}, {6, 11}, {8, 11}, {10, 11}},
		5: {{-14, 15}, {-12, 15}, {-10, 15}, {-8, 15}, {-6, 15}, {-4, 15}, {-2, 15}, {0, 15},
			{2, 15}, {4, 15}, {6, 15}, {8, 15}, {10, 15}, {12, 15}, {14, 15}},
	}
	for bap, levels := range want {
		got := symQuant[bap]
		if len(got) != len(levels) {
			t.Fatalf("bap %d has %d levels, want %d", bap, len(got), len(levels))
		}
		for code, w := range levels {
			if exp := float32(float64(w.num) / float64(w.den)); got[code] != exp {
				t.Errorf("bap %d code %d = %v, want %d/%d = %v", bap, code, got[code], w.num, w.den, exp)
			}
		}
	}
	if symQuant[0] != nil {
		t.Error("bap 0 has a quantizer table, but it sends no mantissa")
	}
}

// TestMantissaBitsMatchSpec pins table 6.17: how many bits each bap reads, and
// how many levels its quantizer has.
func TestMantissaBitsMatchSpec(t *testing.T) {
	tests := []struct {
		bap    uint8
		levels int
		bits   float64 // per mantissa, group bits over group size
	}{
		{0, 0, 0},
		{1, 3, 5.0 / 3},
		{2, 5, 7.0 / 3},
		{3, 7, 3},
		{4, 11, 3.5},
		{5, 15, 4},
		{6, 32, 5},
		{7, 64, 6},
		{8, 128, 7},
		{9, 256, 8},
		{10, 512, 9},
		{11, 1024, 10},
		{12, 2048, 11},
		{13, 4096, 12},
		{14, 16384, 14},
		{15, 65536, 16},
	}
	for _, tt := range tests {
		bits := float64(mantissaBits[tt.bap])
		if n := mantissaGroupSize[tt.bap]; n != 0 {
			bits = float64(mantissaGroupBits[tt.bap]) / float64(n)
		}
		if math.Abs(bits-tt.bits) > 1e-9 {
			t.Errorf("bap %d reads %.4f bits per mantissa, want %.4f", tt.bap, bits, tt.bits)
		}
		// The quantizer has to be exactly as coarse as its bits allow: an
		// asymmetric mantissa of n bits has 2^n levels, and a group of k
		// mantissas of L levels fits its codeword.
		if tt.bap >= 6 {
			if got := 1 << mantissaBits[tt.bap]; got != tt.levels {
				t.Errorf("bap %d has %d levels for %d bits, want %d",
					tt.bap, got, mantissaBits[tt.bap], tt.levels)
			}
		} else if tt.bap >= 1 {
			if got := len(symQuant[tt.bap]); got != tt.levels {
				t.Errorf("bap %d table has %d levels, want %d", tt.bap, got, tt.levels)
			}
			if n := mantissaGroupSize[tt.bap]; n != 0 {
				if pow(tt.levels, n) > 1<<mantissaGroupBits[tt.bap] {
					t.Errorf("bap %d: %d^%d does not fit %d bits",
						tt.bap, tt.levels, n, mantissaGroupBits[tt.bap])
				}
			}
		}
	}
}

func TestReadMantissaAsymmetric(t *testing.T) {
	tests := []struct {
		name string
		bap  uint8
		code uint32
		want float32
	}{
		{"5-bit zero", 6, 0, 0},
		{"5-bit most positive", 6, 15, 15.0 / 16},
		{"5-bit most negative", 6, 16, -1},
		{"5-bit minus one step", 6, 31, -1.0 / 16},
		{"16-bit most positive", 15, 0x7fff, 32767.0 / 32768},
		{"16-bit most negative", 15, 0x8000, -1},
		{"14-bit half scale", 14, 1 << 12, 0.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w bitWriter
			w.write(tt.code, uint(mantissaBits[tt.bap]))
			w.write(0, 16)

			var br bitstream.Reader
			br.Reset(w.buf)
			var r mantissaReader
			got, err := r.read(&br, tt.bap, false)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("bap %d code %d = %v, want %v", tt.bap, tt.code, got, tt.want)
			}
		})
	}
}

// TestReadMantissaGroups is the property the grouping exists for: the codeword
// holds its mantissas in frequency order, and only the first of them reads any
// bits.
func TestReadMantissaGroups(t *testing.T) {
	tests := []struct {
		name  string
		bap   uint8
		codes []int // the mantissa codes to pack, first to last
	}{
		{"3-level, lowest", 1, []int{0, 0, 0}},
		{"3-level, highest", 1, []int{2, 2, 2}},
		{"3-level, all different", 1, []int{0, 1, 2}},
		{"3-level, order matters", 1, []int{2, 1, 0}},
		{"5-level, all different", 2, []int{4, 0, 2}},
		{"5-level, highest", 2, []int{4, 4, 4}},
		{"11-level, all different", 4, []int{10, 0}},
		{"11-level, lowest", 4, []int{0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tab := symQuant[tt.bap]
			code := 0
			for _, c := range tt.codes {
				code = code*len(tab) + c
			}

			var w bitWriter
			w.write(uint32(code), uint(mantissaGroupBits[tt.bap]))
			w.write(0, 16)

			var br bitstream.Reader
			br.Reset(w.buf)
			var r mantissaReader

			for i, c := range tt.codes {
				before := br.BitPos()
				got, err := r.read(&br, tt.bap, false)
				if err != nil {
					t.Fatalf("mantissa %d: %v", i, err)
				}
				if got != tab[c] {
					t.Fatalf("mantissa %d = %v, want code %d = %v", i, got, c, tab[c])
				}
				// Only the first mantissa of the group pays for the codeword.
				want := 0
				if i == 0 {
					want = int(mantissaGroupBits[tt.bap])
				}
				if n := br.BitPos() - before; n != want {
					t.Fatalf("mantissa %d read %d bits, want %d", i, n, want)
				}
			}
		})
	}
}

// TestMantissaGroupsSpanChannels is the subtle part of clause 6.3.1: a block's
// mantissas are one bit stream, so a group opened while reading one channel is
// drained by the next channel to ask for that bap. Getting this wrong shifts
// every mantissa of the block that follows.
func TestMantissaGroupsSpanChannels(t *testing.T) {
	var w bitWriter
	// One 3-level group holding codes 2, 0, 1, then a plain 5-bit mantissa.
	w.write(uint32(2*9+0*3+1), 5)
	w.write(7, 5)
	w.write(0, 16)

	var br bitstream.Reader
	br.Reset(w.buf)
	var r mantissaReader

	// First channel takes one mantissa of the group and stops there.
	got, err := r.read(&br, 1, false)
	if err != nil || got != symQuant[1][2] {
		t.Fatalf("first channel: %v, %v", got, err)
	}
	if n := br.BitPos(); n != 5 {
		t.Fatalf("first channel consumed %d bits, want 5", n)
	}

	// Second channel picks the group up where the first left it, still without
	// touching the bit stream.
	for i, want := range []float32{symQuant[1][0], symQuant[1][1]} {
		got, err := r.read(&br, 1, false)
		if err != nil || got != want {
			t.Fatalf("second channel mantissa %d: %v, %v", i, got, err)
		}
	}
	if n := br.BitPos(); n != 5 {
		t.Fatalf("draining the group consumed %d bits, want none", n-5)
	}

	// The group is empty now, so the next 5 bits are the plain mantissa.
	got, err = r.read(&br, 6, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := float32(7) / 16; got != want {
		t.Fatalf("mantissa after the group = %v, want %v", got, want)
	}

	// A block boundary drops whatever is open: the next block starts clean.
	r.read(&br, 1, false)
	r.reset()
	before := br.BitPos()
	r.read(&br, 1, false)
	if n := br.BitPos() - before; n != 5 {
		t.Fatalf("a reset reader read %d bits for a group, want 5", n)
	}
}

func TestReadMantissaRejects(t *testing.T) {
	tests := []struct {
		name string
		bap  uint8
		bits uint
		code uint32
		want error
	}{
		{"3-level group code 27", 1, 5, 27, ErrMantissa},
		{"3-level group code 31", 1, 5, 31, ErrMantissa},
		{"5-level group code 125", 2, 7, 125, ErrMantissa},
		{"11-level group code 121", 4, 7, 121, ErrMantissa},
		{"7-level code 7", 3, 3, 7, ErrMantissa},
		{"15-level code 15", 5, 4, 15, ErrMantissa},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var w bitWriter
			w.write(tt.code, tt.bits)
			w.write(0, 16)

			var br bitstream.Reader
			br.Reset(w.buf)
			var r mantissaReader
			if _, err := r.read(&br, tt.bap, false); !errors.Is(err, tt.want) {
				t.Fatalf("read = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestReadMantissaShortFrame(t *testing.T) {
	for bap := uint8(1); bap <= 15; bap++ {
		var br bitstream.Reader
		br.Reset(nil)
		var r mantissaReader
		if _, err := r.read(&br, bap, false); !errors.Is(err, ErrShortFrame) {
			t.Errorf("bap %d on an empty buffer = %v, want %v", bap, err, ErrShortFrame)
		}
	}
	// bap 0 reads nothing, so it cannot run out of bits.
	var br bitstream.Reader
	br.Reset(nil)
	var r mantissaReader
	if v, err := r.read(&br, 0, false); v != 0 || err != nil {
		t.Errorf("bap 0 on an empty buffer = %v, %v", v, err)
	}
}

func TestDither(t *testing.T) {
	var br bitstream.Reader
	br.Reset(nil)

	t.Run("off by default", func(t *testing.T) {
		var r mantissaReader
		r.resetFrame()
		for range 100 {
			if v, _ := r.read(&br, 0, true); v != 0 {
				t.Fatalf("dither is off but bap 0 read %v", v)
			}
		}
	})

	t.Run("the channel flag gates it", func(t *testing.T) {
		var r mantissaReader
		r.dither = true
		r.resetFrame()
		for range 100 {
			if v, _ := r.read(&br, 0, false); v != 0 {
				t.Fatalf("dithflag is clear but bap 0 read %v", v)
			}
		}
	})

	t.Run("stays inside the spec's scaling", func(t *testing.T) {
		var r mantissaReader
		r.dither = true
		r.resetFrame()
		var sum, minv, maxv float64
		const n = 1 << 16
		for range n {
			v, _ := r.read(&br, 0, true)
			sum += float64(v)
			minv = math.Min(minv, float64(v))
			maxv = math.Max(maxv, float64(v))
		}
		// The spec's own number, clause 6.3.4, not the constant under test:
		// writing ditherScale on both sides would assert only that the dither
		// fills whatever range the code happened to pick.
		const specScale = 0.707
		if minv < -specScale || maxv >= specScale {
			t.Errorf("dither spans %v..%v, want within +/-%v", minv, maxv, specScale)
		}
		// A uniform distribution either side of zero: the mean of 65 536 draws
		// should sit far below one draw's worth of the range.
		if mean := sum / n; math.Abs(mean) > 0.01 {
			t.Errorf("dither mean is %v, want about zero", mean)
		}
		if maxv < specScale*0.99 || minv > -specScale*0.99 {
			t.Errorf("dither only spans %v..%v, want to fill the range", minv, maxv)
		}
	})

	t.Run("a frame's noise does not depend on the frames before it", func(t *testing.T) {
		var r mantissaReader
		r.dither = true

		var first [16]float32
		r.resetFrame()
		for i := range first {
			first[i], _ = r.read(&br, 0, true)
		}
		var again [16]float32
		r.resetFrame()
		for i := range again {
			again[i], _ = r.read(&br, 0, true)
		}
		if first != again {
			t.Error("two frames seeded the same produced different noise")
		}
	})
}

func TestDecodeCoeffsScalesByExponent(t *testing.T) {
	var w bitWriter
	// Four 5-bit mantissas at minus half scale, exponents 0, 1, 2 and 24.
	for range 4 {
		w.write(24, 5)
	}
	w.write(0, 16)

	var br bitstream.Reader
	br.Reset(w.buf)

	var bap, exp [MaxCoefs]uint8
	var out [MaxCoefs]float32
	for i := range 4 {
		bap[i] = 6
	}
	exp[0], exp[1], exp[2], exp[3] = 0, 1, 2, maxExponent

	var r mantissaReader
	if err := r.decodeCoeffs(&br, &bap, &exp, 0, 4, false, &out); err != nil {
		t.Fatal(err)
	}
	// Code 24 of a 5-bit two's complement fraction is -1/2.
	want := [4]float32{-0.5, -0.25, -0.125, -0.5 / (1 << maxExponent)}
	for i, w := range want {
		if out[i] != w {
			t.Errorf("out[%d] = %v, want %v", i, out[i], w)
		}
	}
}

// TestExpScaleHasNoEdge is the invariant that keeps a corrupt exponent from
// reaching past the table: every byte is a valid index, and the ones the format
// does not define read as silence.
func TestExpScaleHasNoEdge(t *testing.T) {
	for e := range 256 {
		got := expScale[e]
		switch {
		case e <= maxExponent:
			if want := float32(1) / float32(int32(1)<<uint(e)); got != want {
				t.Errorf("expScale[%d] = %v, want %v", e, got, want)
			}
		case got != 0:
			t.Errorf("expScale[%d] = %v, want 0", e, got)
		}
	}
}

func TestMantissaReaderDoesNotAllocate(t *testing.T) {
	var w bitWriter
	for range 256 {
		w.write(0x5a5a, 16)
	}

	var br bitstream.Reader
	var bap, exp [MaxCoefs]uint8
	var out [MaxCoefs]float32
	for i := range bap {
		bap[i] = uint8(i%15) + 1
		exp[i] = uint8(i % 25)
	}
	var r mantissaReader
	r.dither = true

	if n := testing.AllocsPerRun(100, func() {
		br.Reset(w.buf)
		r.resetFrame()
		if err := r.decodeCoeffs(&br, &bap, &exp, 0, 200, true, &out); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("decodeCoeffs allocates %v times per call", n)
	}
}

func BenchmarkDecodeCoeffs(b *testing.B) {
	var w bitWriter
	for range 512 {
		w.write(0x5a5a, 16)
	}

	var br bitstream.Reader
	var bap, exp [MaxCoefs]uint8
	var out [MaxCoefs]float32
	for i := range bap {
		bap[i] = uint8(i%15) + 1
		exp[i] = uint8(i % 25)
	}
	var r mantissaReader
	r.dither = true

	b.ReportAllocs()
	for b.Loop() {
		br.Reset(w.buf)
		r.resetFrame()
		if err := r.decodeCoeffs(&br, &bap, &exp, 0, 200, true, &out); err != nil {
			b.Fatal(err)
		}
	}
}
