package ac3

import (
	"math"
	"testing"
)

// bytesWithSlack pads a hand-built stream so that a reader which runs past the
// fields under test reads zeros rather than an error, which would hide a field
// that is read too wide.
func bytesWithSlack(w *bitWriter) []byte { return append(w.buf, 0, 0, 0, 0, 0, 0, 0, 0) }

// spxDecoder returns a 3/2 decoder with a reader over b, ready for the
// extension's readers.
func spxDecoder(b []byte) *Decoder {
	d := NewDecoder()
	d.h.Sync.Acmod = Acmod3F2R
	d.nfchans = 5
	d.spxBandStruct = eac3DefaultSpxBandStruct
	d.r.Reset(b)
	return d
}

// TestSpxAttenTabMatchesSpec pins the generated notch table against the
// constants table E.16 prints.
//
// The table is generated from 2^(-(bin+1)*(code+1)/15), and the risk in that is
// not a typo but a misread exponent: a table that is off by one in either index
// is still a plausible looking set of decaying numbers. These are the spec's
// own values at the corners and the middle, which no off-by-one reproduces.
func TestSpxAttenTabMatchesSpec(t *testing.T) {
	for _, c := range []struct {
		code, bin int
		want      float64
	}{
		{0, 0, 0.954841603910416503},
		{0, 1, 0.911722488558216804},
		{0, 2, 0.870550563296124125},
		{1, 0, 0.911722488558216804},
		{2, 2, 0.659753955386447100},
		{4, 2, 0.500000000000000000},
		{9, 2, 0.250000000000000000},
		{14, 0, 0.500000000000000000},
		{14, 2, 0.125000000000000000},
	} {
		if got, want := eac3SpxAttenTab[c.code][c.bin], float32(c.want); got != want {
			t.Errorf("atten[%d][%d] = %v, want %v", c.code, c.bin, got, want)
		}
	}
	// The filter only ever attenuates, and it attenuates more the further from
	// the seam and the deeper the code asks: a table that grew anywhere would
	// be amplifying the discontinuity it exists to hide.
	for code := range eac3SpxAttenTab {
		for bin := range eac3SpxAttenTab[code] {
			v := eac3SpxAttenTab[code][bin]
			if v <= 0 || v > 1 {
				t.Errorf("atten[%d][%d] = %v, outside (0, 1]", code, bin, v)
			}
			if bin > 0 && v >= eac3SpxAttenTab[code][bin-1] {
				t.Errorf("atten[%d][%d] = %v does not fall below bin %d", code, bin, v, bin-1)
			}
			if code > 0 && v >= eac3SpxAttenTab[code-1][bin] {
				t.Errorf("atten[%d][%d] = %v does not fall below code %d", code, bin, v, code-1)
			}
		}
	}
}

// TestReadSpxStrategy pins the strategy's fields: the widths they are read at,
// the sub-bands the codes stand for, and the band sizes the structure produces.
//
// The band structure is the part worth pinning hardest, because the streams
// this decoder is checked against do not exercise it: every real frame measured
// states a structure of its own, over one range, so the default table and every
// other range are reached by nothing but this.
func TestReadSpxStrategy(t *testing.T) {
	for _, c := range []struct {
		name                 string
		chinspx              []bool
		spxstrtf, begf, endf uint32
		bndstrce             bool
		bndstrc              []uint32
		dst, src, end, nbnd  int
		sizes                []int
	}{{
		// The one strategy every real frame measured uses, structure and all.
		name:    "the corpus's own",
		chinspx: []bool{true, true, true, true, true},
		begf:    5, endf: 5, bndstrce: true,
		bndstrc: []uint32{0, 1, 0, 1, 0},
		dst:     25, src: 109, end: 181, nbnd: 4,
		sizes: []int{12, 24, 24, 12},
	}, {
		// The same range with the structure left to the default, which no real
		// frame does and which the table exists for. The default merges every
		// second sub-band from 8 up, so six sub-bands become three bands of two.
		name:    "the default structure",
		chinspx: []bool{true, true, true, true, true},
		begf:    5, endf: 5, bndstrce: false,
		dst: 25, src: 109, end: 181, nbnd: 3,
		sizes: []int{24, 24, 24},
	}, {
		// The bottom of both codes: the extension starts as low as it can and
		// the source is one sub-band wide.
		name:     "the lowest range",
		chinspx:  []bool{true, false, false, false, false},
		spxstrtf: 1, begf: 0, endf: 0, bndstrce: true,
		bndstrc: []uint32{0, 0},
		dst:     37, src: 49, end: 85, nbnd: 3,
		sizes: []int{12, 12, 12},
	}, {
		// The top of both codes, where the mapping stops being linear: begf 7
		// means sub-band 11 rather than 9, and endf 7 means 17 rather than 12.
		name:    "the top of the non-linear codes",
		chinspx: []bool{true, true, false, false, false},
		begf:    7, endf: 7, bndstrce: true,
		bndstrc: []uint32{1, 1, 1, 1, 1},
		dst:     25, src: 157, end: 229, nbnd: 1,
		sizes: []int{72},
	}} {
		t.Run(c.name, func(t *testing.T) {
			var w bitWriter
			for _, in := range c.chinspx {
				w.write(b2u(in), 1)
			}
			w.write(c.spxstrtf, 2)
			w.write(c.begf, 3)
			w.write(c.endf, 3)
			w.write(b2u(c.bndstrce), 1)
			for _, v := range c.bndstrc {
				w.write(v, 1)
			}
			want := w.nbit

			d := spxDecoder(bytesWithSlack(&w))
			if err := d.readSpxStrategy(0); err != nil {
				t.Fatalf("readSpxStrategy: %v", err)
			}
			if got := d.r.BitPos(); got != want {
				t.Errorf("read %d bits, want %d: a field is the wrong width", got, want)
			}
			if got := d.chinspx[:len(c.chinspx)]; !equalBool(got, c.chinspx) {
				t.Errorf("chinspx = %v, want %v", got, c.chinspx)
			}
			if d.spxdststrtmant != c.dst || d.spxstrtmant != c.src || d.spxendmant != c.end {
				t.Errorf("dst/src/end = %d/%d/%d, want %d/%d/%d",
					d.spxdststrtmant, d.spxstrtmant, d.spxendmant, c.dst, c.src, c.end)
			}
			if d.nspxbnd != c.nbnd {
				t.Fatalf("nspxbnd = %d, want %d", d.nspxbnd, c.nbnd)
			}
			if got := d.spxbndsz[:c.nbnd]; !equalInt(got, c.sizes) {
				t.Errorf("band sizes = %v, want %v", got, c.sizes)
			}
			// The bands have to tile the range exactly: a structure that lost
			// or invented a sub-band would leave the copy and the gains
			// disagreeing about where each band is.
			total := 0
			for _, n := range d.spxbndsz[:c.nbnd] {
				total += n
			}
			if total != c.end-c.src {
				t.Errorf("the bands span %d bins, but the extension is %d wide", total, c.end-c.src)
			}
		})
	}
}

// TestReadSpxStrategyRejectsImpossibleRanges pins the two checks that keep the
// rest of the extension inside its buffers.
func TestReadSpxStrategyRejectsImpossibleRanges(t *testing.T) {
	// A source that starts above the bins it feeds: strtf 3 puts the copy's
	// source at bin 61 and begf 0 puts the extension's start at 49, so the copy
	// would be reading bins the extension has already written.
	var w bitWriter
	for range 5 {
		w.write(1, 1)
	}
	w.write(3, 2) // spxstrtf: bin 25 + 3*12 = 61
	w.write(0, 3) // spxbegf: sub-band 2, bin 49
	w.write(5, 3) // spxendf
	if err := spxDecoder(bytesWithSlack(&w)).readSpxStrategy(0); err == nil {
		t.Error("a copy whose source starts at its destination was accepted")
	}

	// An extension that ends below where it starts: begf 7 is sub-band 11 and
	// endf 0 is sub-band 5.
	w = bitWriter{}
	for range 5 {
		w.write(1, 1)
	}
	w.write(0, 2)
	w.write(7, 3)
	w.write(0, 3)
	if err := spxDecoder(bytesWithSlack(&w)).readSpxStrategy(0); err == nil {
		t.Error("an extension that ends below its start was accepted")
	}
}

// TestMapSpxCopy pins the copy's section mapping and its wrap points.
//
// None of this is reached by the streams this decoder is compared against: the
// one strategy every real frame measured uses has a source wider than the range
// it fills, so the copy is one section and wraps nowhere. Everything below is
// the arithmetic that a stream with a narrower source would take, worked out
// against the reference's own loop.
func TestMapSpxCopy(t *testing.T) {
	for _, c := range []struct {
		name          string
		dst, src, end int
		sizes         []int
		sections      []int
		wrap          []bool
	}{{
		// The corpus's: 84 bins of source for 72 to fill, so it never wraps.
		name: "source wider than the extension",
		dst:  25, src: 109, end: 181,
		sizes:    []int{12, 24, 24, 12},
		sections: []int{72},
		wrap:     []bool{true, false, false, false},
	}, {
		// 24 bins of source for 132 to fill: the copy runs over it five and a
		// half times, and every band that would straddle a restart is pushed to
		// the next pass instead.
		name: "source narrower than the extension",
		dst:  25, src: 49, end: 181,
		sizes:    []int{12, 12, 12, 12, 12, 12, 12, 12, 12, 12, 12},
		sections: []int{24, 24, 24, 24, 24, 12},
		wrap:     []bool{true, false, true, false, true, false, true, false, true, false, true},
	}, {
		// One band wider than the whole source. It cannot be pushed to a fresh
		// pass and made to fit, so it wraps within itself - and the section the
		// outer test pushes is empty, because there was nothing copied yet to
		// end.
		name: "a band wider than the source",
		dst:  25, src: 49, end: 97,
		sizes:    []int{48},
		sections: []int{0, 24, 24},
		wrap:     []bool{true},
	}} {
		t.Run(c.name, func(t *testing.T) {
			d := NewDecoder()
			d.spxdststrtmant, d.spxstrtmant, d.spxendmant = c.dst, c.src, c.end
			d.nspxbnd = len(c.sizes)
			copy(d.spxbndsz[:], c.sizes)
			d.mapSpxCopy()

			if got := d.spxCopySize[:d.nspxCopy]; !equalInt(got, c.sections) {
				t.Errorf("sections = %v, want %v", got, c.sections)
			}
			if got := d.spxwrap[:len(c.wrap)]; !equalBool(got, c.wrap) {
				t.Errorf("wrap = %v, want %v", got, c.wrap)
			}
			// The sections have to fill the extension exactly: one bin short
			// and the top of the spectrum keeps whatever the block before it
			// left there.
			total := 0
			for _, n := range d.spxCopySize[:d.nspxCopy] {
				total += n
				if n > c.src-c.dst {
					t.Errorf("a section of %d bins is longer than the %d the source has", n, c.src-c.dst)
				}
			}
			if total != c.end-c.src {
				t.Errorf("the sections copy %d bins into an extension %d wide", total, c.end-c.src)
			}
		})
	}
}

// TestReadSpxCoords pins the blend factors and the gains.
//
// The gains are checked against the reference's arithmetic written out longhand
// rather than against numbers copied from a run of this code, which would pin
// nothing but itself.
func TestReadSpxCoords(t *testing.T) {
	// The corpus's strategy, so the bands are the ones a real frame has.
	const src, end = 109, 181
	sizes := []int{12, 24, 24, 12}

	for _, c := range []struct {
		name     string
		blndCode uint32
		mstr     uint32
		exps     []uint32
		mants    []uint32
	}{
		{"no blend, so no noise is asked for at the bottom", 0, 0, []uint32{3, 4, 5, 6}, []uint32{0, 1, 2, 3}},
		{"the blend past the top of the extension, which clips to all signal", 31, 2, []uint32{1, 2, 3, 4}, []uint32{3, 2, 1, 0}},
		{"the denormal exponent, where the gain can reach zero", 8, 3, []uint32{15, 15, 0, 15}, []uint32{0, 3, 0, 1}},
	} {
		t.Run(c.name, func(t *testing.T) {
			var w bitWriter
			w.write(c.blndCode, 5)
			w.write(c.mstr, 2)
			for bnd := range sizes {
				w.write(c.exps[bnd], 4)
				w.write(c.mants[bnd], 2)
			}
			want := w.nbit

			d := spxDecoder(bytesWithSlack(&w))
			d.spxstrtmant, d.spxendmant = src, end
			d.nspxbnd = len(sizes)
			copy(d.spxbndsz[:], sizes)
			d.chinspx[0] = true
			// Channel 0 has never stated its gains, so it states them here with
			// no flag to say so and the others state nothing at all.
			d.eac3.firstSpxCoords[0] = true

			if err := d.readSpxCoords(); err != nil {
				t.Fatalf("readSpxCoords: %v", err)
			}
			if got := d.r.BitPos(); got != want {
				t.Errorf("read %d bits, want %d", got, want)
			}
			if d.eac3.firstSpxCoords[0] {
				t.Error("the channel still expects to state its gains outright")
			}
			for ch := 1; ch < d.nfchans; ch++ {
				if !d.eac3.firstSpxCoords[ch] {
					t.Errorf("channel %d does not extend but is not set to state its gains", ch)
				}
			}

			blnd := float64(c.blndCode) / 32
			bin := src
			for bnd := range sizes {
				nratio := math.Min(math.Max(float64(float32(bin+sizes[bnd]/2)/float32(end))-blnd, 0), 1)
				bin += sizes[bnd]

				mant := int(c.mants[bnd])
				if c.exps[bnd] == 15 {
					mant <<= 1
				} else {
					mant += 4
				}
				co := float64(mant<<(25-int(c.exps[bnd])-3*int(c.mstr))) / (1 << 23)

				wantN := float32(math.Sqrt(3*nratio) * co)
				wantS := float32(math.Sqrt(1-nratio) * co)
				if !closeEnough(d.spxnblend[0][bnd], wantN) {
					t.Errorf("band %d noise blend = %v, want %v", bnd, d.spxnblend[0][bnd], wantN)
				}
				if !closeEnough(d.spxsblend[0][bnd], wantS) {
					t.Errorf("band %d signal blend = %v, want %v", bnd, d.spxsblend[0][bnd], wantS)
				}
			}
		})
	}
}

// TestSpxBlendSplitsPower pins the property the two blend factors exist for:
// the noise and the copy are uncorrelated, so it is their powers that have to
// add up to the band's, not their amplitudes. The noise carries an extra factor
// of the square root of three because the draw it scales is uniform over -1 to
// 1, whose variance is a third rather than one.
//
// A decoder that mixed by nratio rather than by its square root would pass
// every field width test above and be quietly wrong about the level of the top
// of every extended channel.
func TestSpxBlendSplitsPower(t *testing.T) {
	const src, end = 109, 181
	sizes := []int{12, 24, 24, 12}

	for blndCode := range uint32(32) {
		var w bitWriter
		w.write(blndCode, 5)
		w.write(0, 2) // mstrspxco
		for range sizes {
			w.write(0, 4) // spxcoexp: a gain of exactly one, so the blends stand alone
			w.write(0, 2) // spxcomant
		}

		d := spxDecoder(bytesWithSlack(&w))
		d.spxstrtmant, d.spxendmant = src, end
		d.nspxbnd = len(sizes)
		copy(d.spxbndsz[:], sizes)
		d.chinspx[0] = true
		d.eac3.firstSpxCoords[0] = true
		if err := d.readSpxCoords(); err != nil {
			t.Fatal(err)
		}
		// Exponent 0 and mantissa 0 make the gain (0+4) << 25 over 2^23, which
		// is 16: the blends come back scaled by that and nothing else.
		const gain = 16
		for bnd := range sizes {
			n := float64(d.spxnblend[0][bnd]) / gain
			s := float64(d.spxsblend[0][bnd]) / gain
			if got := n*n/3 + s*s; math.Abs(got-1) > 1e-6 {
				t.Errorf("blend %d band %d: noise^2/3 + signal^2 = %v, want 1", blndCode, bnd, got)
			}
		}
	}
	// The blend point walks the extension: at zero every band is all noise it
	// can be, and past the top every band is all signal.
	var all, none *Decoder
	for _, p := range []struct {
		code uint32
		into **Decoder
	}{{0, &all}, {31, &none}} {
		var w bitWriter
		w.write(p.code, 5)
		w.write(0, 2)
		for range sizes {
			w.write(0, 4)
			w.write(0, 2)
		}
		d := spxDecoder(bytesWithSlack(&w))
		d.spxstrtmant, d.spxendmant = src, end
		d.nspxbnd = len(sizes)
		copy(d.spxbndsz[:], sizes)
		d.chinspx[0] = true
		d.eac3.firstSpxCoords[0] = true
		if err := d.readSpxCoords(); err != nil {
			t.Fatal(err)
		}
		*p.into = d
	}
	for bnd := range sizes {
		if all.spxnblend[0][bnd] <= none.spxnblend[0][bnd] {
			t.Errorf("band %d: blend 0 asks for no more noise than blend 31 does", bnd)
		}
	}
	// Blend 31 is 0,969 of the extension's top, which is above every band's
	// middle here, so every band clips to no noise at all.
	for bnd := range sizes {
		if none.spxnblend[0][bnd] != 0 {
			t.Errorf("band %d: a blend past the top still asks for noise: %v", bnd, none.spxnblend[0][bnd])
		}
	}
}

// TestSpxNoiseIsGatedByDither pins that the extension draws nothing when the
// noise is off.
//
// The extension's noise is the bap 0 dither's sequence, and no two decoders
// agree on it. SetDither(false) is what a comparison against another decoder
// turns off, and it has to reach here as well: an extension that kept drawing
// would leave a comparison measuring this decoder's noise against another's
// rather than the copy against the copy.
func TestSpxNoiseIsGatedByDither(t *testing.T) {
	build := func(dither bool) *Block {
		d := NewDecoder()
		d.SetDither(dither)
		// A frame rewinds the noise sequence before its blocks; without it the
		// generator sits at zero, which is a fixed point, and this test would
		// pass over silence.
		d.mant.resetFrame()
		d.h.Sync.Acmod = Acmod3F2R
		d.nfchans = 5
		d.spxinu = true
		d.chinspx[0] = true
		d.spxdststrtmant, d.spxstrtmant, d.spxendmant = 25, 109, 181
		d.nspxbnd = 4
		copy(d.spxbndsz[:], []int{12, 24, 24, 12})
		d.eac3.spxAttenCode[0] = -1
		for bnd := range 4 {
			// Ask for noise and nothing else, so the copy cannot be what is
			// being compared below.
			d.spxnblend[0][bnd] = 1
			d.spxsblend[0][bnd] = 0
		}
		b := &d.blocks[0]
		for bin := range 109 {
			b.Coeffs[0][bin] = float32(bin%7) / 7
		}
		d.applySpx(b)
		return b
	}
	on, off := build(true), build(false)

	var noisy int
	for bin := 109; bin < 181; bin++ {
		if off.Coeffs[0][bin] != 0 {
			t.Fatalf("bin %d is %v with the noise off, want silence", bin, off.Coeffs[0][bin])
		}
		if on.Coeffs[0][bin] != 0 {
			noisy++
		}
	}
	// The gate has to be the only difference: if the noise were off in both,
	// the check above would pass over silence and prove nothing.
	if noisy == 0 {
		t.Error("the extension drew no noise with the noise on, so the check above is vacuous")
	}
}

// TestApplySpxCopiesAndScales pins the reconstruction itself on a copy that
// wraps, which no stream this decoder is compared against does.
func TestApplySpxCopiesAndScales(t *testing.T) {
	d := NewDecoder()
	d.SetDither(false)
	d.h.Sync.Acmod = Acmod3F2R
	d.nfchans = 5
	d.spxinu = true
	d.chinspx[0] = true
	// 24 bins of source for 36 to fill, so the copy runs over it one and a half
	// times and the second band starts a fresh pass.
	d.spxdststrtmant, d.spxstrtmant, d.spxendmant = 25, 49, 85
	d.nspxbnd = 3
	copy(d.spxbndsz[:], []int{12, 12, 12})
	d.eac3.spxAttenCode[0] = -1
	for bnd := range 3 {
		d.spxnblend[0][bnd] = 0
		d.spxsblend[0][bnd] = float32(bnd + 1)
	}

	b := &d.blocks[0]
	for bin := 25; bin < 49; bin++ {
		b.Coeffs[0][bin] = float32(bin)
	}
	d.applySpx(b)

	// Bands 0 and 1 take the source's first 24 bins in order; band 2 starts the
	// source again, because band 2 would not have fitted in what was left.
	want := make([]float32, 0, 36)
	for bin := 25; bin < 49; bin++ {
		want = append(want, float32(bin))
	}
	for bin := 25; bin < 37; bin++ {
		want = append(want, float32(bin))
	}
	for i, src := range want {
		// The energy each band came out with is the mean square of the copy,
		// and the gain is applied on top of it; with no noise asked for, the
		// bin is simply the copy times its band's gain.
		gain := float32(i/12 + 1)
		if got := b.Coeffs[0][49+i]; got != src*gain {
			t.Errorf("bin %d = %v, want %v * %v = %v", 49+i, got, src, gain, src*gain)
		}
	}
	if b.EndMant[0] != 85 {
		t.Errorf("the channel ends at %d, want 85: it carries what the extension built", b.EndMant[0])
	}
}

func b2u(b bool) uint32 {
	if b {
		return 1
	}
	return 0
}

func equalBool(a, b []bool) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInt(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// closeEnough allows the last bit of a float32, which is all the difference
// between computing the blends in single precision and checking them against
// the same arithmetic in double.
func closeEnough(got, want float32) bool {
	if got == want {
		return true
	}
	d := math.Abs(float64(got - want))
	return d <= 1e-6*math.Max(1, math.Abs(float64(want)))
}
