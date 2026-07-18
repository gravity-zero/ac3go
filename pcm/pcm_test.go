package pcm

import (
	"errors"
	"fmt"
	"testing"
)

func TestChannelString(t *testing.T) {
	tests := []struct {
		c    Channel
		want string
	}{
		{ChannelLeft, "L"},
		{ChannelCenter, "C"},
		{ChannelRight, "R"},
		{ChannelLeftSurround, "Ls"},
		{ChannelRightSurround, "Rs"},
		{ChannelMonoSurround, "S"},
		{ChannelLFE, "LFE"},
		{ChannelCh1, "Ch1"},
		{ChannelCh2, "Ch2"},
		{Channel(200), "Channel(200)"},
	}
	for _, tt := range tests {
		if got := tt.c.String(); got != tt.want {
			t.Errorf("Channel(%d).String() = %q, want %q", uint8(tt.c), got, tt.want)
		}
	}
}

func TestLayout(t *testing.T) {
	tests := []struct {
		name    string
		layout  Layout
		want    string
		hasLFE  bool
		idxOfLs int
	}{
		{"empty", Layout{}, "-", false, -1},
		{"mono", LayoutMono, "C", false, -1},
		{"stereo", LayoutStereo, "L,R", false, -1},
		{"dual mono", LayoutDualMono, "Ch1,Ch2", false, -1},
		{"3/2", Layout3F2R, "L,C,R,Ls,Rs", false, 3},
		{"3/2 + LFE", Layout3F2R.WithLFE(), "L,C,R,Ls,Rs,LFE", true, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.layout.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
			if got := tt.layout.Has(ChannelLFE); got != tt.hasLFE {
				t.Errorf("Has(LFE) = %v, want %v", got, tt.hasLFE)
			}
			if got := tt.layout.Index(ChannelLeftSurround); got != tt.idxOfLs {
				t.Errorf("Index(Ls) = %d, want %d", got, tt.idxOfLs)
			}
		})
	}
}

func TestLayoutWithLFEDoesNotAliasSource(t *testing.T) {
	base := Layout{ChannelLeft, ChannelRight}
	withLFE := base.WithLFE()
	withLFE[0] = ChannelCenter
	if base[0] != ChannelLeft {
		t.Error("WithLFE aliased its receiver; the source layout was mutated")
	}
	if got := len(base); got != 2 {
		t.Errorf("len(base) = %d, want 2", got)
	}
}

func TestFormat(t *testing.T) {
	f := Format{SampleRate: 48000, Layout: Layout3F2R.WithLFE()}
	if got, want := f.Channels(), 6; got != want {
		t.Errorf("Channels() = %d, want %d", got, want)
	}
	if got, want := f.String(), "48000Hz L,C,R,Ls,Rs,LFE"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}
}

func TestPlanesLayout(t *testing.T) {
	f := Format{SampleRate: 48000, Layout: LayoutStereo}
	p := NewPlanes(f, 1536)

	if got, want := p.Channels(), 2; got != want {
		t.Fatalf("Channels() = %d, want %d", got, want)
	}
	if got, want := p.Len(), 1536; got != want {
		t.Errorf("Len() = %d, want %d", got, want)
	}
	if got, want := p.Cap(), 1536; got != want {
		t.Errorf("Cap() = %d, want %d", got, want)
	}

	// The planes must be disjoint: writing one must not disturb the other.
	p.Plane(0)[0] = 1
	p.Plane(1)[0] = 2
	if got := p.Plane(0)[0]; got != 1 {
		t.Errorf("plane 0 sample 0 = %v, want 1 (planes overlap)", got)
	}
	if got := p.PlaneFor(ChannelRight)[0]; got != 2 {
		t.Errorf("PlaneFor(R) sample 0 = %v, want 2", got)
	}
	if p.PlaneFor(ChannelLFE) != nil {
		t.Error("PlaneFor(LFE) on a stereo format returned a plane, want nil")
	}
}

func TestPlanesResize(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutStereo}, 1536)

	tests := []struct {
		name    string
		n       int
		wantErr error
	}{
		{"shrink to a block", 256, nil},
		{"grow back to a frame", 1536, nil},
		{"empty", 0, nil},
		{"above capacity", 1537, ErrShortPlane},
		{"negative", -1, ErrShortPlane},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			before := p.Len()
			err := p.Resize(tt.n)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Resize(%d) = %v, want %v", tt.n, err, tt.wantErr)
			}
			if tt.wantErr != nil {
				if got := p.Len(); got != before {
					t.Errorf("Len after failed Resize = %d, want %d unchanged", got, before)
				}
				return
			}
			if got := p.Len(); got != tt.n {
				t.Errorf("Len() = %d, want %d", got, tt.n)
			}
			for c := 0; c < p.Channels(); c++ {
				if got := len(p.Plane(c)); got != tt.n {
					t.Errorf("len(Plane(%d)) = %d, want %d", c, got, tt.n)
				}
			}
		})
	}
}

// TestPlanesResizeKeepsPlanesDisjoint guards the slice arithmetic: a shrunken
// plane must still not be able to reach into its neighbour, even by appending.
func TestPlanesResizeKeepsPlanesDisjoint(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: Layout3F2R}, 1536)
	if err := p.Resize(256); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < p.Channels(); c++ {
		pl := p.Plane(c)
		if got, want := cap(pl), 1536; got != want {
			t.Errorf("cap(Plane(%d)) = %d, want %d", c, got, want)
		}
		for i := range pl[:cap(pl)] {
			pl[:cap(pl)][i] = float32(c)
		}
	}
	// Every plane owns its full capacity window; nothing was overwritten by a
	// neighbour writing past Len.
	if err := p.Resize(1536); err != nil {
		t.Fatal(err)
	}
	for c := 0; c < p.Channels(); c++ {
		for i, v := range p.Plane(c) {
			if v != float32(c) {
				t.Fatalf("plane %d sample %d = %v, want %v: planes overlap", c, i, v, float32(c))
			}
		}
	}
}

func TestPlanesZero(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutStereo}, 8)
	for c := 0; c < p.Channels(); c++ {
		for i := range p.Plane(c) {
			p.Plane(c)[i] = 0.5
		}
	}
	p.Zero()
	for c := 0; c < p.Channels(); c++ {
		for i, v := range p.Plane(c) {
			if v != 0 {
				t.Fatalf("plane %d sample %d = %v after Zero, want 0", c, i, v)
			}
		}
	}
}

func TestPlanesInterleave(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutStereo}, 4)
	if err := p.Resize(3); err != nil {
		t.Fatal(err)
	}
	// Full scale, silence, and values that must clip in both directions.
	copy(p.Plane(0), []float32{0, 1, -1})
	copy(p.Plane(1), []float32{0.5, 2, -2})

	dst := make([]int32, 6)
	if err := p.Interleave(dst, 16); err != nil {
		t.Fatal(err)
	}
	want := []int32{0, 16384, 32767, 32767, -32768, -32768}
	for i := range want {
		if dst[i] != want[i] {
			t.Errorf("dst[%d] = %d, want %d (got %v)", i, dst[i], want[i], dst)
			break
		}
	}
}

// TestPlanesInterleaveFullScale pins the clipping bounds at every depth the
// module offers. Full scale is where a depth that cannot be represented in the
// arithmetic doing the clipping shows up: at 32 bits, 2^31 - 1 is not a
// float32, and a peak that wraps to the opposite sign is the loudest defect a
// decoder can produce.
func TestPlanesInterleaveFullScale(t *testing.T) {
	tests := []struct {
		bits uint
		max  int32
		min  int32
	}{
		{16, 32767, -32768},
		{24, 8388607, -8388608},
		{32, 2147483647, -2147483648},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%d bits", tt.bits), func(t *testing.T) {
			p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutMono}, 5)
			// Full scale both ways, past full scale both ways, and silence.
			copy(p.Plane(0), []float32{1, -1, 2, -2, 0})
			dst := make([]int32, 5)
			if err := p.Interleave(dst, tt.bits); err != nil {
				t.Fatal(err)
			}
			want := []int32{tt.max, tt.min, tt.max, tt.min, 0}
			for i := range want {
				if dst[i] != want[i] {
					t.Errorf("dst[%d] = %d, want %d (got %v)", i, dst[i], want[i], dst)
				}
			}
		})
	}
}

func TestPlanesInterleaveRounding(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutMono}, 4)
	if err := p.Resize(4); err != nil {
		t.Fatal(err)
	}
	// 1/32768 is one LSB at 16 bits; check both signs round to nearest.
	const lsb = 1.0 / 32768.0
	copy(p.Plane(0), []float32{0.4 * lsb, 0.6 * lsb, -0.4 * lsb, -0.6 * lsb})
	dst := make([]int32, 4)
	if err := p.Interleave(dst, 16); err != nil {
		t.Fatal(err)
	}
	want := []int32{0, 1, 0, -1}
	for i := range want {
		if dst[i] != want[i] {
			t.Errorf("dst[%d] = %d, want %d", i, dst[i], want[i])
		}
	}
}

func TestPlanesInterleaveErrors(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: LayoutStereo}, 4)
	tests := []struct {
		name string
		dst  []int32
		bits uint
		want error
	}{
		{"short destination", make([]int32, 7), 16, ErrShortPlane},
		{"zero bit depth", make([]int32, 8), 0, ErrBadDepth},
		{"bit depth above 32", make([]int32, 8), 33, ErrBadDepth},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := p.Interleave(tt.dst, tt.bits); !errors.Is(err, tt.want) {
				t.Errorf("Interleave = %v, want %v", err, tt.want)
			}
		})
	}
}

func TestPlanesReuseDoesNotAllocate(t *testing.T) {
	p := NewPlanes(Format{SampleRate: 48000, Layout: Layout3F2R.WithLFE()}, 1536)
	dst := make([]int32, 1536*6)
	got := testing.AllocsPerRun(50, func() {
		if err := p.Resize(1536); err != nil {
			t.Fatal(err)
		}
		p.Zero()
		if err := p.Interleave(dst, 16); err != nil {
			t.Fatal(err)
		}
	})
	if got != 0 {
		t.Errorf("AllocsPerRun = %v, want 0", got)
	}
}
