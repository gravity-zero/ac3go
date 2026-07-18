package ac3

import (
	"errors"
	"testing"
)

// fbwAlloc is the shape of an allocInfo for a plain full bandwidth channel:
// the middle of every parameter range, so a test can move one thing at a time.
func fbwAlloc() allocInfo {
	return allocInfo{
		sdecay: slowdec[2],
		fdecay: fastdec[1],
		sgain:  slowgain[1],
		dbknee: dbpbtab[2],
		floor:  floortab[4],
		fgain:  fastgain[4],
		start:  0,
		end:    chbwcodEndMant(20),
	}
}

// cplAlloc is the shape of an allocInfo for a coupling channel: it starts
// partway up the spectrum and enters the leak loop with the levels the encoder
// sent, the way audblk builds it.
func cplAlloc() allocInfo {
	in := fbwAlloc()
	in.coupling = true
	in.start, in.end = 73, 253 // cplbegf 3, the top band
	in.fleak = int32(2)<<8 + 768
	in.sleak = int32(2)<<8 + 768
	return in
}

// TestBitAllocDeltaCoupling pins where a coupling channel's delta segments
// land. The offsets are relative to the channel's own first band, not absolute
// band numbers, so a segment at offset 2 moves the second band of the coupling
// range and not band 2 of the spectrum.
//
// This is the one case that discriminates the two readings of clause 6.2.2.6:
// a full bandwidth channel starts at band 0, where they coincide. Reading the
// offsets as absolute would aim this segment far below the channel's own
// range, where nothing reads the mask back, and the delta would vanish without
// trace, which is what this test would then catch.
func TestBitAllocDeltaCoupling(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap, plain [MaxCoefs]uint8
	for i := range exp {
		exp[i] = 10
	}

	base := cplAlloc()
	base.snroffset = snrOffset(25, 0)
	if err := a.compute(&base, &exp, &plain); err != nil {
		t.Fatal(err)
	}

	in := base
	in.d = dba{mode: DbaNew, nseg: 1, offst: [8]uint8{2}, len: [8]uint8{4}, ba: [8]uint8{7}}
	if err := a.compute(&in, &exp, &bap); err != nil {
		t.Fatal(err)
	}

	// The channel's first band, then two bands up: where the segment lands.
	bndstrt := int(masktab[base.start])
	lo, hi := bndtab[bndstrt+2], bndtab[bndstrt+6]
	for bin := lo; bin < hi; bin++ {
		if bap[bin] >= plain[bin] {
			t.Fatalf("bin %d (band %d): bap %d -> %d, want fewer bits: the segment "+
				"did not land on the coupling channel's own bands",
				bin, masktab[bin], plain[bin], bap[bin])
		}
	}
	// Everything else in the channel is untouched.
	for bin := base.start; bin < base.end; bin++ {
		if bin >= lo && bin < hi {
			continue
		}
		if bap[bin] != plain[bin] {
			t.Fatalf("bin %d (band %d) is outside the segment: bap %d -> %d, want unchanged",
				bin, masktab[bin], plain[bin], bap[bin])
		}
	}
}

func TestLogadd(t *testing.T) {
	tests := []struct {
		name string
		a, b int32
		want int32
	}{
		{"equal powers gain 3 dB", 2048, 2048, 2048 + 64},
		{"6 dB down adds about 1 dB", 2048, 2048 - 128, 2048 + 20},
		{"a much louder than b", 3072, 0, 3072},
		{"b much louder than a", 0, 3072, 3072},
		{"silence plus silence", 0, 0, 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := logadd(tt.a, tt.b); got != tt.want {
				t.Errorf("logadd(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			if got, want := logadd(tt.b, tt.a), logadd(tt.a, tt.b); got != want {
				t.Errorf("logadd is not symmetric: %d vs %d", got, want)
			}
		})
	}
}

func TestCalcLowcomp(t *testing.T) {
	tests := []struct {
		name    string
		a       int32
		b0, b1  int32
		bin     int
		want    int32
		comment string
	}{
		{name: "low band, exact 2 dB step up", a: 0, b0: 1000, b1: 1256, bin: 3, want: 384},
		{name: "mid band, exact 2 dB step up", a: 0, b0: 1000, b1: 1256, bin: 10, want: 320},
		{name: "low band, falling", a: 384, b0: 1256, b1: 1000, bin: 3, want: 320},
		{name: "low band, falling, floors at zero", a: 32, b0: 1256, b1: 1000, bin: 3, want: 0},
		{name: "low band, rising but not by 2 dB", a: 200, b0: 1000, b1: 1100, bin: 3, want: 200},
		{name: "flat holds the state", a: 200, b0: 1000, b1: 1000, bin: 10, want: 200},
		{name: "above band 19 decays whatever happens", a: 300, b0: 0, b1: 3072, bin: 20, want: 172},
		{name: "above band 19, floors at zero", a: 64, b0: 0, b1: 0, bin: 25, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := calcLowcomp(tt.a, tt.b0, tt.b1, tt.bin); got != tt.want {
				t.Errorf("calcLowcomp(%d, %d, %d, %d) = %d, want %d",
					tt.a, tt.b0, tt.b1, tt.bin, got, tt.want)
			}
		})
	}
}

// TestBitAllocEndsOfTheRange checks the two cases the model has to get right
// for anything else to make sense: nothing to code, and everything to code.
func TestBitAllocEndsOfTheRange(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap [MaxCoefs]uint8

	t.Run("silence gets no bits", func(t *testing.T) {
		// The largest exponent is the quietest signal: psd 0, under the
		// hearing threshold everywhere.
		for i := range exp {
			exp[i] = maxExponent
		}
		in := fbwAlloc()
		in.snroffset = snrOffset(63, 15) // the most generous offset there is
		if err := a.compute(&in, &exp, &bap); err != nil {
			t.Fatal(err)
		}
		for i := range in.end {
			if bap[i] != 0 {
				t.Fatalf("bap[%d] = %d on a silent channel, want 0", i, bap[i])
			}
		}
	})

	t.Run("full scale gets bits", func(t *testing.T) {
		for i := range exp {
			exp[i] = 0
		}
		in := fbwAlloc()
		in.snroffset = snrOffset(63, 15)
		if err := a.compute(&in, &exp, &bap); err != nil {
			t.Fatal(err)
		}
		for i := range in.end {
			if bap[i] == 0 {
				t.Fatalf("bap[%d] = 0 on a full scale channel", i)
			}
		}
	})

	t.Run("the offset moves the whole allocation", func(t *testing.T) {
		for i := range exp {
			exp[i] = 6
		}
		var prev int
		for csnroffst := uint8(1); csnroffst <= 63; csnroffst += 2 {
			in := fbwAlloc()
			in.snroffset = snrOffset(csnroffst, 0)
			if err := a.compute(&in, &exp, &bap); err != nil {
				t.Fatal(err)
			}
			steps := 0
			for i := range in.end {
				steps += int(bap[i])
			}
			if steps < prev {
				t.Fatalf("csnroffst %d buys %d bap steps, fewer than the %d the offset below bought",
					csnroffst, steps, prev)
			}
			prev = steps
		}
		if prev == 0 {
			t.Fatal("the widest offset allocates nothing")
		}
	})
}

// TestBitAllocFollowsTheEnvelope is the property the whole model exists for: a
// band that stands above its neighbours is what the listener hears, so it is
// what gets the bits.
func TestBitAllocFollowsTheEnvelope(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap [MaxCoefs]uint8

	in := fbwAlloc()
	in.snroffset = snrOffset(40, 0)
	for i := range exp {
		exp[i] = 12
	}
	// One loud region in the middle of the band.
	for i := 100; i < 112; i++ {
		exp[i] = 0
	}
	if err := a.compute(&in, &exp, &bap); err != nil {
		t.Fatal(err)
	}

	loud, quiet := 0, 0
	for i := 100; i < 112; i++ {
		loud += int(bap[i])
	}
	for i := 180; i < 192; i++ {
		quiet += int(bap[i])
	}
	if loud <= quiet {
		t.Fatalf("the loud region got %d bap steps, a quiet one got %d", loud, quiet)
	}
	// Masking: the bins just above the loud region are hidden by it, so they
	// get less than the same bins would if the region were not there.
	masked := 0
	for i := 112; i < 124; i++ {
		masked += int(bap[i])
	}
	for i := range exp {
		exp[i] = 12
	}
	if err := a.compute(&in, &exp, &bap); err != nil {
		t.Fatal(err)
	}
	unmasked := 0
	for i := 112; i < 124; i++ {
		unmasked += int(bap[i])
	}
	if masked >= unmasked {
		t.Fatalf("bins above a loud region got %d bap steps, %d without it: nothing was masked",
			masked, unmasked)
	}
}

func TestBitAllocDelta(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap, plain [MaxCoefs]uint8
	for i := range exp {
		exp[i] = 10
	}

	// A middling offset on purpose: a generous one drives the whole masking
	// curve under the floor, where it clamps and a delta of any size would
	// vanish without trace.
	base := fbwAlloc()
	base.snroffset = snrOffset(25, 0)
	if err := a.compute(&base, &exp, &plain); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		d    dba
		want func(before, after uint8) bool
		desc string
	}{
		{
			name: "none leaves the curve alone",
			d:    dba{mode: DbaNone, nseg: 1, offst: [8]uint8{30}, len: [8]uint8{4}, ba: [8]uint8{7}},
			want: func(before, after uint8) bool { return before == after },
			desc: "unchanged",
		},
		{
			name: "new raises the mask and spends fewer bits",
			d:    dba{mode: DbaNew, nseg: 1, offst: [8]uint8{30}, len: [8]uint8{4}, ba: [8]uint8{7}},
			want: func(before, after uint8) bool { return after < before },
			desc: "fewer bits",
		},
		{
			name: "new lowers the mask and buys bits",
			d:    dba{mode: DbaNew, nseg: 1, offst: [8]uint8{30}, len: [8]uint8{4}, ba: [8]uint8{0}},
			want: func(before, after uint8) bool { return after > before },
			desc: "more bits",
		},
		{
			name: "reuse applies the segments too",
			d:    dba{mode: DbaReuse, nseg: 1, offst: [8]uint8{30}, len: [8]uint8{4}, ba: [8]uint8{7}},
			want: func(before, after uint8) bool { return after < before },
			desc: "fewer bits",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := base
			in.d = tt.d
			if err := a.compute(&in, &exp, &bap); err != nil {
				t.Fatal(err)
			}
			// Bands 30 to 33, that is bins 34 to 45.
			for bin := bndtab[30]; bin < bndtab[34]; bin++ {
				if !tt.want(plain[bin], bap[bin]) {
					t.Fatalf("bin %d: bap %d -> %d, want %s", bin, plain[bin], bap[bin], tt.desc)
				}
			}
			// Everything outside the segment is untouched.
			for bin := bndtab[34]; bin < in.end; bin++ {
				if bap[bin] != plain[bin] {
					t.Fatalf("bin %d is outside the segment but moved %d -> %d",
						bin, plain[bin], bap[bin])
				}
			}
		})
	}
}

func TestBitAllocRejects(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap [MaxCoefs]uint8

	tests := []struct {
		name string
		in   func() allocInfo
		want error
	}{
		{
			name: "reserved fscod",
			in:   func() allocInfo { in := fbwAlloc(); in.fscod = 3; return in },
			want: ErrReserved,
		},
		{
			name: "empty channel",
			in:   func() allocInfo { in := fbwAlloc(); in.start, in.end = 40, 40; return in },
			want: ErrBitAlloc,
		},
		{
			name: "channel past the coded spectrum",
			in:   func() allocInfo { in := fbwAlloc(); in.end = 254; return in },
			want: ErrBitAlloc,
		},
		{
			name: "delta segment past the last band",
			in: func() allocInfo {
				in := fbwAlloc()
				in.d = dba{mode: DbaNew, nseg: 2, offst: [8]uint8{31, 31}, len: [8]uint8{1, 1}, ba: [8]uint8{7, 7}}
				return in
			},
			want: ErrBitAlloc,
		},
		{
			name: "delta segment runs off the end",
			in: func() allocInfo {
				in := fbwAlloc()
				in.d = dba{mode: DbaNew, nseg: 1, offst: [8]uint8{49}, len: [8]uint8{15}, ba: [8]uint8{7}}
				return in
			},
			want: ErrBitAlloc,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := tt.in()
			if err := a.compute(&in, &exp, &bap); !errors.Is(err, tt.want) {
				t.Fatalf("compute = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestBitAllocCoupling checks the one structural difference the coupling
// channel makes: it starts partway up the spectrum, so the model skips the low
// frequency compensation and takes its leak levels from the bit stream.
func TestBitAllocCoupling(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap, quiet [MaxCoefs]uint8
	for i := range exp {
		exp[i] = 8
	}

	in := fbwAlloc()
	in.coupling = true
	in.start = cplbegfStrtMant(0)
	in.end = cplendfEndMant(9)
	in.snroffset = snrOffset(40, 0)
	in.fleak, in.sleak = 0, 0
	if err := a.compute(&in, &exp, &quiet); err != nil {
		t.Fatal(err)
	}

	// A high leak level says the bands below the coupling channel were loud,
	// which masks its lowest bands, which costs them bits.
	in.fleak = 7<<8 + 768
	in.sleak = 7<<8 + 768
	if err := a.compute(&in, &exp, &bap); err != nil {
		t.Fatal(err)
	}
	if bap[in.start] >= quiet[in.start] {
		t.Fatalf("bin %d got %d bap steps with a loud leak, %d without: the leak did nothing",
			in.start, bap[in.start], quiet[in.start])
	}
	// Below the coupling channel nothing is written at all.
	for i := range in.start {
		if bap[i] != 0 {
			t.Fatalf("bap[%d] = %d, below cplstrtmant %d", i, bap[i], in.start)
		}
	}
}

func TestBitAllocDoesNotAllocate(t *testing.T) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap [MaxCoefs]uint8
	for i := range exp {
		exp[i] = uint8(i % 25)
	}
	in := fbwAlloc()
	in.snroffset = snrOffset(40, 0)

	if n := testing.AllocsPerRun(100, func() {
		if err := a.compute(&in, &exp, &bap); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("compute allocates %v times per call", n)
	}
}

func BenchmarkBitAlloc(b *testing.B) {
	var a bitAlloc
	var exp [MaxCoefs]uint8
	var bap [MaxCoefs]uint8
	for i := range exp {
		exp[i] = uint8(i % 25)
	}
	in := fbwAlloc()
	in.end = chbwcodEndMant(maxChbwcod)
	in.snroffset = snrOffset(40, 0)

	b.ReportAllocs()
	for b.Loop() {
		if err := a.compute(&in, &exp, &bap); err != nil {
			b.Fatal(err)
		}
	}
}
