package e2e

import (
	"math"
	"math/rand"
	"strings"
	"testing"
)

// The comparator is the thing that will decide whether the decoder is correct,
// so it gets tested harder than the code it judges. These tests need no oracle
// and always run.

func TestCompare(t *testing.T) {
	tests := []struct {
		name      string
		got       []int32
		want      []int32
		tol       Tolerance
		wantErr   bool
		wantEqual bool
		wantMax   int64
	}{
		{
			name: "identical buffers", got: []int32{1, -2, 3}, want: []int32{1, -2, 3},
			tol: Exact, wantEqual: true, wantMax: 0,
		},
		{
			name: "empty buffers", got: []int32{}, want: []int32{},
			tol: Exact, wantEqual: true,
		},
		{
			name: "one LSB off fails Exact", got: []int32{1, 2, 3}, want: []int32{1, 2, 4},
			tol: Exact, wantErr: true, wantMax: 1,
		},
		{
			name: "one LSB off clears QuasiExact", got: []int32{1, 2, 3}, want: []int32{1, 2, 4},
			tol: QuasiExact, wantMax: 1,
		},
		{
			name: "two LSB off clears QuasiExact", got: []int32{0}, want: []int32{2},
			tol: QuasiExact, wantMax: 2,
		},
		{
			name: "three LSB off fails QuasiExact", got: []int32{0}, want: []int32{3},
			tol: QuasiExact, wantErr: true, wantMax: 3,
		},
		{
			name: "the sign of the difference does not matter", got: []int32{5}, want: []int32{3},
			tol: QuasiExact, wantMax: 2,
		},
		{
			name: "negative samples", got: []int32{-32768}, want: []int32{-32766},
			tol: QuasiExact, wantMax: 2,
		},
		{
			name: "full scale samples do not overflow the difference",
			got:  []int32{math.MaxInt32}, want: []int32{math.MinInt32},
			tol: QuasiExact, wantErr: true, wantMax: 1<<32 - 1, // needs int64: the harness must not overflow on the case it exists to catch
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := Compare(tt.got, tt.want, tt.tol)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare = %v, wantErr %v", err, tt.wantErr)
			}
			if r.Equal != tt.wantEqual {
				t.Errorf("Equal = %v, want %v", r.Equal, tt.wantEqual)
			}
			if r.MaxLSB != tt.wantMax {
				t.Errorf("MaxLSB = %d, want %d", r.MaxLSB, tt.wantMax)
			}
			if r.Samples != len(tt.got) {
				t.Errorf("Samples = %d, want %d", r.Samples, len(tt.got))
			}
		})
	}
}

// TestCompareRejectsLengthMismatch is the alignment rule: the harness must
// never compare an overlap and call it a pass.
func TestCompareRejectsLengthMismatch(t *testing.T) {
	tests := []struct {
		name     string
		got      []int32
		want     []int32
		wantHint string
	}{
		{"one frame short", make([]int32, 1536*3), make([]int32, 1536*4), "a whole number of frames apart (1)"},
		{"one block short", make([]int32, 1536), make([]int32, 1536+256), "a whole number of blocks apart (1)"},
		{"a few samples short", make([]int32, 100), make([]int32, 103), "3 samples apart"},
		{"the reference is empty", make([]int32, 10), nil, "the reference decoded nothing"},
		{"we decoded nothing", nil, make([]int32, 10), "this decoder produced nothing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Even with everything zero, so that the samples themselves match,
			// a length mismatch has to fail.
			_, err := Compare(tt.got, tt.want, Dithered)
			if err == nil {
				t.Fatal("Compare = nil on a length mismatch, want an error")
			}
			if !strings.Contains(err.Error(), tt.wantHint) {
				t.Errorf("error = %q, want it to mention %q", err, tt.wantHint)
			}
		})
	}
}

// TestCompareRMSBound covers the dithered mode: a difference that stays small
// on average passes, one that does not fails, even when no single sample is
// over the per-sample ceiling.
func TestCompareRMSBound(t *testing.T) {
	const n = 4096
	tol := Tolerance{MaxLSB: 1000, MaxRMSLSB: 10}

	tests := []struct {
		name    string
		delta   int32
		wantErr bool
	}{
		{"no difference", 0, false},
		{"rms below the bound", 9, false},
		{"rms at the bound", 10, false},
		{"rms above the bound", 11, true},
		{"rms far above the bound, still under the per-sample ceiling", 999, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]int32, n)
			want := make([]int32, n)
			for i := range got {
				got[i] = tt.delta
			}
			r, err := Compare(got, want, tol)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare = %v, wantErr %v (rms %.2f)", err, tt.wantErr, r.RMSLSB)
			}
			if wantRMS := float64(tt.delta); math.Abs(r.RMSLSB-wantRMS) > 1e-9 {
				t.Errorf("RMSLSB = %v, want %v", r.RMSLSB, wantRMS)
			}
		})
	}
}

// TestCompareExceedFraction covers the other half of the dithered mode: a few
// samples may exceed the per-sample ceiling, but not many.
func TestCompareExceedFraction(t *testing.T) {
	const n = 1000
	tol := Tolerance{MaxLSB: 2, MaxExceedFraction: 0.01} // 10 samples of 1000

	tests := []struct {
		name    string
		bad     int
		wantErr bool
	}{
		{"none over", 0, false},
		{"just under the allowance", 9, false},
		{"at the allowance", 10, false},
		{"one over the allowance", 11, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := make([]int32, n)
			want := make([]int32, n)
			for i := range tt.bad {
				got[i] = 3 // one over the 2 LSB ceiling
			}
			r, err := Compare(got, want, tol)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Compare = %v, wantErr %v", err, tt.wantErr)
			}
			if r.Exceeding != tt.bad {
				t.Errorf("Exceeding = %d, want %d", r.Exceeding, tt.bad)
			}
			if tt.bad > 0 && r.FirstBad != 0 {
				t.Errorf("FirstBad = %d, want 0", r.FirstBad)
			}
			if tt.bad == 0 && r.FirstBad != -1 {
				t.Errorf("FirstBad = %d, want -1", r.FirstBad)
			}
		})
	}
}

// TestDitheredToleranceAcceptsDitherNoise is the tolerance calibration: noise
// of the shape bap 0 produces must pass, and a real defect must not.
func TestDitheredToleranceAcceptsDitherNoise(t *testing.T) {
	const n = 1 << 16
	rng := rand.New(rand.NewSource(7))

	// A pair of decoders disagreeing only on unallocated mantissas: small,
	// zero mean, uncorrelated noise.
	ref := make([]int32, n)
	noisy := make([]int32, n)
	for i := range ref {
		ref[i] = int32(rng.Intn(1 << 15))
		noisy[i] = ref[i] + int32(rng.Intn(41)-20)
	}
	if _, err := Compare(noisy, ref, Dithered); err != nil {
		t.Errorf("Dithered rejected plausible dither noise: %v", err)
	}
	if _, err := Compare(noisy, ref, QuasiExact); err == nil {
		t.Error("QuasiExact accepted dither noise; it must not be used on dithered streams")
	}

	// A real defect: one channel is silent. That must fail even the loose bar.
	broken := make([]int32, n)
	if _, err := Compare(broken, ref, Dithered); err == nil {
		t.Error("Dithered accepted a silent buffer against real audio")
	}

	// A real defect: everything scaled, as a wrong dialnorm or downmix would.
	scaled := make([]int32, n)
	for i := range ref {
		scaled[i] = ref[i] / 2
	}
	if _, err := Compare(scaled, ref, Dithered); err == nil {
		t.Error("Dithered accepted a buffer at half level; a level bug must not pass")
	}
}

func TestFindOffset(t *testing.T) {
	const n = 8192
	rng := rand.New(rand.NewSource(3))
	base := make([]int32, n)
	for i := range base {
		base[i] = int32(rng.Intn(1<<16) - 1<<15)
	}

	tests := []struct {
		name    string
		lag     int
		wantLag int
	}{
		{"aligned", 0, 0},
		{"late by one sample", 1, 1},
		{"late by a block", 256, 256},
		{"early by a block", -256, -256},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// got[i] = base[i+lag]: got runs ahead of base by lag.
			got := make([]int32, n)
			for i := range got {
				j := i + tt.lag
				if j >= 0 && j < n {
					got[i] = base[j]
				}
			}
			lag, aligned := FindOffset(got, base, 512)
			if lag != tt.wantLag {
				t.Errorf("FindOffset = %d, want %d", lag, tt.wantLag)
			}
			if aligned != (tt.wantLag == 0) {
				t.Errorf("aligned = %v, want %v", aligned, tt.wantLag == 0)
			}
		})
	}
}

func TestFindOffsetEmpty(t *testing.T) {
	lag, aligned := FindOffset(nil, nil, 16)
	if lag != 0 || !aligned {
		t.Errorf("FindOffset on empty = (%d, %v), want (0, true)", lag, aligned)
	}
}

func TestDeinterleave(t *testing.T) {
	tests := []struct {
		name     string
		in       []int32
		channels int
		want     [][]int32
	}{
		{"stereo", []int32{1, 2, 3, 4, 5, 6}, 2, [][]int32{{1, 3, 5}, {2, 4, 6}}},
		{"mono", []int32{1, 2, 3}, 1, [][]int32{{1, 2, 3}}},
		{"5.1", []int32{1, 2, 3, 4, 5, 6}, 6, [][]int32{{1}, {2}, {3}, {4}, {5}, {6}}},
		{"zero channels", []int32{1, 2}, 0, nil},
		{"negative channels", []int32{1, 2}, -1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Deinterleave(tt.in, tt.channels)
			if len(got) != len(tt.want) {
				t.Fatalf("got %d planes, want %d", len(got), len(tt.want))
			}
			for c := range tt.want {
				if len(got[c]) != len(tt.want[c]) {
					t.Fatalf("plane %d has %d samples, want %d", c, len(got[c]), len(tt.want[c]))
				}
				for i := range tt.want[c] {
					if got[c][i] != tt.want[c][i] {
						t.Errorf("plane %d sample %d = %d, want %d", c, i, got[c][i], tt.want[c][i])
					}
				}
			}
		})
	}
}

func TestDecodeLE(t *testing.T) {
	tests := []struct {
		name  string
		in    []byte
		width int
		want  int32
	}{
		{"16-bit zero", []byte{0x00, 0x00}, 2, 0},
		{"16-bit one", []byte{0x01, 0x00}, 2, 1},
		{"16-bit minus one", []byte{0xFF, 0xFF}, 2, -1},
		{"16-bit full scale positive", []byte{0xFF, 0x7F}, 2, 32767},
		{"16-bit full scale negative", []byte{0x00, 0x80}, 2, -32768},
		{"32-bit minus one", []byte{0xFF, 0xFF, 0xFF, 0xFF}, 4, -1},
		{"32-bit full scale negative", []byte{0x00, 0x00, 0x00, 0x80}, 4, math.MinInt32},
		{"24-bit in a 32-bit word", []byte{0x00, 0x00, 0x80, 0xFF}, 4, -8388608},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := decodeLE(tt.in, tt.width); got != tt.want {
				t.Errorf("decodeLE = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestToleranceString(t *testing.T) {
	tests := []struct {
		tol  Tolerance
		want string
	}{
		{Exact, "max 0 LSB"},
		{QuasiExact, "max 2 LSB"},
		{Tolerance{MaxLSB: 512, MaxRMSLSB: 64, MaxExceedFraction: 0.01}, "max 512 LSB, rms 64.0 LSB, up to 1.00% over"},
	}
	for _, tt := range tests {
		if got := tt.tol.String(); got != tt.want {
			t.Errorf("Tolerance.String() = %q, want %q", got, tt.want)
		}
	}
}

func TestResultString(t *testing.T) {
	r, err := Compare([]int32{1, 2, 3}, []int32{1, 2, 3}, Exact)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := r.String(), "3 samples, identical"; got != want {
		t.Errorf("String() = %q, want %q", got, want)
	}

	r, _ = Compare([]int32{1, 2, 9}, []int32{1, 2, 3}, Exact)
	if got := r.String(); !strings.Contains(got, "max 6 LSB at 2") {
		t.Errorf("String() = %q, want it to locate the worst sample", got)
	}
}

// TestExpand covers the argv templating, and above all that a substituted
// value stays one argument: the paths of real media are full of spaces, and a
// harness that splits on them tests nothing.
func TestExpand(t *testing.T) {
	o := &Oracle{}
	tests := []struct {
		name string
		argv []string
		vars map[string]string
		want []string
	}{
		{
			name: "a path with spaces stays one argument",
			argv: []string{"{tool}", "-i", "{src}", "-y", "{out}"},
			vars: map[string]string{"tool": "dec", "src": "/media/A Film - 1080P.mkv", "out": "/tmp/o.ac3"},
			want: []string{"dec", "-i", "/media/A Film - 1080P.mkv", "-y", "/tmp/o.ac3"},
		},
		{
			name: "an empty value drops its argument",
			argv: []string{"{tool}", "{durflag}", "{dur}", "-i", "{src}"},
			vars: map[string]string{"tool": "dec", "durflag": "", "dur": "", "src": "in.mkv"},
			want: []string{"dec", "-i", "in.mkv"},
		},
		{
			name: "a present value keeps its argument",
			argv: []string{"{tool}", "{durflag}", "{dur}", "-i", "{src}"},
			vars: map[string]string{"tool": "dec", "durflag": "-t", "dur": "30", "src": "in.mkv"},
			want: []string{"dec", "-t", "30", "-i", "in.mkv"},
		},
		{
			name: "a placeholder inside a larger argument",
			argv: []string{"-map", "0:{track}"},
			vars: map[string]string{"track": "3"},
			want: []string{"-map", "0:3"},
		},
		{
			name: "an unknown placeholder is left alone rather than silently dropped",
			argv: []string{"{tool}", "{mystery}"},
			vars: map[string]string{"tool": "dec"},
			want: []string{"dec", "{mystery}"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := o.expand(tt.argv, tt.vars)
			if len(got) != len(tt.want) {
				t.Fatalf("expand = %q, want %q", got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Errorf("arg %d = %q, want %q", i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestTrimFloat(t *testing.T) {
	tests := []struct {
		in   float64
		want string
	}{
		{0, "0"},
		{30, "30"},
		{600, "600"},
		{1.5, "1.5"},
		{0.25, "0.25"},
	}
	for _, tt := range tests {
		if got := trimFloat(tt.in); got != tt.want {
			t.Errorf("trimFloat(%v) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
