package ac3

import (
	"errors"
	"testing"

	"github.com/gravity-zero/ac3go/bitstream"
)

// group packs three mapped differentials the way an encoder does
// (clause 6.1.3): each is the differential plus the offset of 2, and the three
// pack into 25/5/1 places.
func group(d0, d1, d2 int) uint32 {
	return uint32(25*(d0+2) + 5*(d1+2) + (d2 + 2))
}

// writeExpSet emits an absolute exponent of the given width followed by the
// groups, the way audblk carries an exponent set.
func writeExpSet(absexp uint32, absbits uint, groups ...uint32) []byte {
	var w bitWriter
	w.write(absexp, absbits)
	for _, g := range groups {
		w.write(g, 7)
	}
	// Pad to a byte so the reader never overruns on the last group.
	w.write(0, 8)
	return w.buf
}

func TestDecodeExponents(t *testing.T) {
	tests := []struct {
		name     string
		strategy uint8
		absexp   uint8
		groups   []uint32
		want     []uint8
	}{
		{
			name:     "d15 flat",
			strategy: ExpD15,
			absexp:   5,
			groups:   []uint32{group(0, 0, 0)},
			want:     []uint8{5, 5, 5},
		},
		{
			name:     "d15 every differential",
			strategy: ExpD15,
			absexp:   10,
			groups:   []uint32{group(-2, -1, 0), group(1, 2, -2)},
			want:     []uint8{8, 7, 7, 8, 10, 8},
		},
		{
			name:     "d25 repeats each exponent twice",
			strategy: ExpD25,
			absexp:   3,
			groups:   []uint32{group(1, 0, -1)},
			want:     []uint8{4, 4, 4, 4, 3, 3},
		},
		{
			name:     "d45 repeats each exponent four times",
			strategy: ExpD45,
			absexp:   0,
			groups:   []uint32{group(2, 2, 2)},
			want: []uint8{
				2, 2, 2, 2,
				4, 4, 4, 4,
				6, 6, 6, 6,
			},
		},
		{
			name:     "walks down to zero and back up",
			strategy: ExpD15,
			absexp:   2,
			groups:   []uint32{group(-2, 0, 2), group(2, 2, 2)},
			want:     []uint8{0, 0, 2, 4, 6, 8},
		},
		{
			name:     "reaches the ceiling exactly",
			strategy: ExpD15,
			absexp:   18,
			groups:   []uint32{group(2, 2, 2)},
			want:     []uint8{20, 22, 24},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r bitstream.Reader
			r.Reset(writeExpSet(0, 0, tt.groups...))

			var exp [MaxCoefs]uint8
			if err := decodeExponents(&r, tt.strategy, len(tt.groups), tt.absexp, exp[:]); err != nil {
				t.Fatalf("decodeExponents: %v", err)
			}
			for i, want := range tt.want {
				if exp[i] != want {
					t.Fatalf("exp[%d] = %d, want %d (whole set %v, want %v)",
						i, exp[i], want, exp[:len(tt.want)], tt.want)
				}
			}
		})
	}
}

func TestDecodeExponentsRejects(t *testing.T) {
	tests := []struct {
		name     string
		strategy uint8
		absexp   uint8
		groups   []uint32
		buf      int // exponent buffer size, zero means MaxCoefs
		want     error
	}{
		{
			name:     "reuse is not a coding strategy",
			strategy: ExpReuse,
			groups:   []uint32{group(0, 0, 0)},
			want:     ErrReserved,
		},
		{
			name:     "group above 124 cannot be encoded",
			strategy: ExpD15,
			groups:   []uint32{125},
			want:     ErrReserved,
		},
		{
			name:     "widest group value",
			strategy: ExpD15,
			groups:   []uint32{127},
			want:     ErrReserved,
		},
		{
			name:     "differential chain walks below zero",
			strategy: ExpD15,
			absexp:   1,
			groups:   []uint32{group(-2, 0, 0)},
			want:     ErrExponent,
		},
		{
			name:     "differential chain walks past 24",
			strategy: ExpD15,
			absexp:   23,
			groups:   []uint32{group(2, 0, 0)},
			want:     ErrExponent,
		},
		{
			name:     "buffer too small for the strategy",
			strategy: ExpD45,
			groups:   []uint32{group(0, 0, 0)},
			buf:      11,
			want:     ErrExponent,
		},
		{
			name:     "bit stream ends inside the set",
			strategy: ExpD15,
			groups:   []uint32{group(0, 0, 0)},
			buf:      -1, // marker: truncate the input instead
			want:     ErrShortFrame,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := writeExpSet(0, 0, tt.groups...)
			size := tt.buf
			switch size {
			case 0:
				size = MaxCoefs
			case -1:
				size = MaxCoefs
				buf = nil
			}

			var r bitstream.Reader
			r.Reset(buf)
			exp := make([]uint8, size)
			err := decodeExponents(&r, tt.strategy, len(tt.groups), tt.absexp, exp)
			if !errors.Is(err, tt.want) {
				t.Fatalf("decodeExponents = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestExpGroupCountsCoverTheChannel is the property the spec's formulas exist
// to satisfy: a set of nchgrps groups has to carry at least endmant exponents,
// counting the leading absolute one, or the top of the channel would have no
// spectral envelope. It also has to fit the 256-bin arrays, which is what
// keeps the decoder inside its buffers.
func TestExpGroupCountsCoverTheChannel(t *testing.T) {
	for chbwcod := 0; chbwcod <= maxChbwcod; chbwcod++ {
		endmant := chbwcodEndMant(uint8(chbwcod))
		for _, strategy := range []uint8{ExpD15, ExpD25, ExpD45} {
			ngrps := fbwExpGroups(strategy, endmant)
			got := 1 + ngrps*3*expGroupSize[strategy]
			if got < endmant {
				t.Errorf("chbwcod %d strategy %d: %d exponents for %d bins",
					chbwcod, strategy, got, endmant)
			}
			if n := ngrps * 3 * expGroupSize[strategy]; n > MaxCoefs {
				t.Errorf("chbwcod %d strategy %d: writes %d entries, buffer is %d",
					chbwcod, strategy, n, MaxCoefs)
			}
		}
	}
}

// TestCplExpGroupCountsAreExact is the coupling channel's counterpart. It
// codes whole 12-bin sub-bands, so unlike a full bandwidth channel its group
// count divides exactly: the decoded exponents land one per bin over
// cplstrtmant..cplendmant with none left over.
func TestCplExpGroupCountsAreExact(t *testing.T) {
	for cplbegf := 0; cplbegf <= 15; cplbegf++ {
		for cplendf := 0; cplendf <= 15; cplendf++ {
			if cplbegf > cplendf+2 {
				continue
			}
			strt := cplbegfStrtMant(uint8(cplbegf))
			end := cplendfEndMant(uint8(cplendf))
			for _, strategy := range []uint8{ExpD15, ExpD25, ExpD45} {
				ngrps := cplExpGroups(strategy, strt, end)
				if got := ngrps * 3 * expGroupSize[strategy]; got != end-strt {
					t.Errorf("cplbegf %d cplendf %d strategy %d: %d exponents for %d bins",
						cplbegf, cplendf, strategy, got, end-strt)
				}
				if end > MaxCoefs {
					t.Errorf("cplbegf %d cplendf %d: cplendmant %d past %d bins",
						cplbegf, cplendf, end, MaxCoefs)
				}
			}
		}
	}
}

// TestLFEExponentSetIsExact pins the one exponent set the format hard-codes.
func TestLFEExponentSetIsExact(t *testing.T) {
	if got := 1 + lfeExpGroups*3*expGroupSize[ExpD15]; got != lfeMants {
		t.Fatalf("lfe carries %d exponents, want %d", got, lfeMants)
	}
}

func BenchmarkDecodeExponentsD15(b *testing.B) {
	groups := make([]uint32, 84)
	for i := range groups {
		groups[i] = group(1, -1, 0)
	}
	buf := writeExpSet(0, 0, groups...)

	var r bitstream.Reader
	var exp [MaxCoefs]uint8
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(buf)
		if err := decodeExponents(&r, ExpD15, len(groups), 8, exp[:]); err != nil {
			b.Fatal(err)
		}
	}
}
