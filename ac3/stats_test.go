package ac3

import (
	"reflect"
	"slices"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// Each counter of Stats is moved by a case of its own, on both syntaxes where
// the case exists in both, and the cases that should move nothing else are
// checked to have moved nothing else: a counter that ticks along with another
// one tells a caller nothing the other did not.

// decodeCount decodes every access unit of stream with d and returns how many
// decoded.
func decodeCount(t *testing.T, d *Decoder, stream []byte) int64 {
	t.Helper()
	var n int64
	for len(stream) > 0 {
		if err := d.DecodeFrame(stream); err != nil {
			t.Fatalf("frame %d: %v", n, err)
		}
		n++
		stream = stream[d.AccessUnitSize():]
	}
	return n
}

// firstFrame returns a copy of the first syncframe of a fixture, and its header.
func firstFrame(t *testing.T, name string) ([]byte, Header) {
	t.Helper()
	stream := readFixture(t, name)
	var h Header
	if err := ParseHeader(stream, &h); err != nil {
		t.Fatal(err)
	}
	return append([]byte(nil), stream[:h.Sync.FrameSize]...), h
}

func TestStatsCleanStream(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			d := NewDecoder()
			n := decodeCount(t, d, readFixture(t, f.fivePtOne))
			got := d.Stats()
			want := Stats{Frames: n, LastLayout: layout51}
			if !statsEqual(got, want) {
				t.Errorf("Stats = %+v, want %+v", got, want)
			}
			if n == 0 {
				t.Fatal("no frame decoded")
			}
		})
	}
}

func TestStatsTruncated(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			frame, h := firstFrame(t, f.fivePtOne)
			d := NewDecoder()
			for _, cut := range []int{h.Sync.FrameSize - 1, 3} {
				if d.DecodeFrame(frame[:cut]) == nil {
					t.Fatalf("a frame cut to %d bytes decoded", cut)
				}
			}
			if got := d.Stats(); !statsEqual(got, Stats{Truncated: 2}) {
				t.Errorf("Stats = %+v, want Truncated 2 and nothing else", got)
			}
		})
	}
}

func TestStatsBadHeader(t *testing.T) {
	d := NewDecoder()
	junk := make([]byte, 64)
	junk[0] = 0x12
	if d.DecodeFrame(junk) == nil {
		t.Fatal("junk decoded")
	}
	if got := d.Stats(); !statsEqual(got, Stats{BadHeaders: 1}) {
		t.Errorf("Stats = %+v, want BadHeaders 1 and nothing else", got)
	}
}

// The substream id and the reduced rate are E-AC-3 fields: AC-3 has neither.
func TestStatsUnsupportedEAC3(t *testing.T) {
	frame, _ := firstFrame(t, "tones_48k_5p1_384k.eac3")

	sub := append([]byte(nil), frame...)
	sub[2] |= 1 << 3 // substreamid 1
	d := NewDecoder()
	if d.DecodeFrame(sub) == nil {
		t.Fatal("substream 1 decoded")
	}
	if got := d.Stats(); !statsEqual(got, Stats{UnsupportedSubstreams: 1}) {
		t.Errorf("substream 1: Stats = %+v, want UnsupportedSubstreams 1 and nothing else", got)
	}

	half := append([]byte(nil), frame...)
	half[4] = half[4]&^0xf0 | 0xc0 // fscod 3, fscod2 0: 24 kHz
	d = NewDecoder()
	if d.DecodeFrame(half) == nil {
		t.Fatal("reduced rate decoded")
	}
	if got := d.Stats(); !statsEqual(got, Stats{UnsupportedReducedRate: 1}) {
		t.Errorf("reduced rate: Stats = %+v, want UnsupportedReducedRate 1 and nothing else", got)
	}
}

// TestStatsBlockError zeroes a frame's audio behind a sound header: the first
// block then asks for exponents it has no base for.
func TestStatsBlockError(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			frame, h := firstFrame(t, f.fivePtOne)
			for i := h.AudioStartBit/8 + 1; i < len(frame); i++ {
				frame[i] = 0
			}
			d := NewDecoder()
			if d.DecodeFrame(frame) == nil {
				t.Fatal("a frame of zeroed audio decoded")
			}
			if got := d.Stats(); !statsEqual(got, Stats{BlockErrors: 1}) {
				t.Errorf("Stats = %+v, want BlockErrors 1 and nothing else", got)
			}
		})
	}
}

// TestStatsOverrun restates an E-AC-3 frame's size so that its audio still
// fits in the bytes, and so still decodes, but ends inside the few bits every
// frame keeps after its audio for the check word. E-AC-3 states its size in
// 16 bit words, which is fine enough to land there.
func TestStatsOverrun(t *testing.T) {
	frame, _ := firstFrame(t, "tones_48k_5p1_384k.eac3")
	probe := NewDecoder()
	if err := probe.DecodeFrame(frame); err != nil {
		t.Fatal(err)
	}
	end := probe.BlockEndBit()
	words := (end + 15) / 16 // the fewest that hold the audio: less than 18 bits to spare
	if words*16-end >= blockTrailerBits {
		t.Fatalf("audio ends at bit %d: %d words leave room for the trailer", end, words)
	}
	short := append([]byte(nil), frame[:words*2]...)
	frmsiz := words - 1
	short[2] = short[2]&^0x07 | byte(frmsiz>>8)
	short[3] = byte(frmsiz)

	d := NewDecoder()
	err := d.DecodeFrame(short)
	if err == nil {
		t.Fatal("an overrunning frame decoded")
	}
	if got := d.Stats(); !statsEqual(got, Stats{Overruns: 1}) {
		t.Errorf("Stats = %+v (%v), want Overruns 1 and nothing else", got, err)
	}
}

// TestStatsDependent: a 7.1 access unit under a downmix, and a dependent
// substream that is not the 7.1 extension, are both stepped over; a 7.1 one
// that is decoded and fails is an error of its own.
func TestStatsDependent(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			core, _ := firstFrame(t, f.fivePtOne)
			au := append(append([]byte(nil), core...), dependent71Header(256)...)

			d := NewDecoder()
			d.SetDownmix(pcm.LayoutStereo)
			if err := d.DecodeFrame(au); err != nil {
				t.Fatal(err)
			}
			if got := d.Stats(); !statsEqual(got, Stats{Frames: 1, DependentSkipped: 1, Downmixed: 1,
				LastLayout: pcm.Layout7point1}) {
				t.Errorf("7.1 under downmix: Stats = %+v", got)
			}

			// The next access unit is the core alone: nothing skipped, and
			// the layout back to 5.1 - what one access unit did must not
			// carry over to the next.
			if err := d.DecodeFrame(core); err != nil {
				t.Fatal(err)
			}
			if got := d.Stats(); !statsEqual(got, Stats{Frames: 2, DependentSkipped: 1, Downmixed: 2,
				LayoutChanges: 1, LastLayout: layout51}) {
				t.Errorf("7.1 then 5.1 under downmix: Stats = %+v", got)
			}

			other := append([]byte(nil), au...)
			other[len(core)+7] ^= 0x02 // chanmap: not the 7.1 extension any more
			d = NewDecoder()
			if err := d.DecodeFrame(other); err != nil {
				t.Fatal(err)
			}
			if got := d.Stats(); !statsEqual(got, Stats{Frames: 1, DependentSkipped: 1,
				LastLayout: layout51}) {
				t.Errorf("other dependent: Stats = %+v", got)
			}

			d = NewDecoder()
			if d.DecodeFrame(au) == nil {
				t.Fatal("a 7.1 access unit with a dependent of zeros decoded")
			}
			if got := d.Stats(); !statsEqual(got, Stats{DependentErrors: 1}) {
				t.Errorf("failed dependent: Stats = %+v, want DependentErrors 1 and nothing else", got)
			}
		})
	}
}

// TestStatsLayoutChanges runs 5.1, then stereo, then 5.1: two changes, and the
// last layout the one the stream ended on. Downmixed counts the frames that
// were mixed, which the stereo ones asked for stereo are not.
func TestStatsLayoutChanges(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			five, two := readFixture(t, f.fivePtOne), readFixture(t, f.stereo)
			var stream []byte
			stream = append(stream, five...)
			stream = append(stream, two...)
			stream = append(stream, five...)

			nFive := decodeCount(t, NewDecoder(), five)
			nTwo := decodeCount(t, NewDecoder(), two)

			d := NewDecoder()
			d.SetDownmix(pcm.LayoutStereo)
			n := decodeCount(t, d, stream)
			want := Stats{Frames: n, LayoutChanges: 2, Downmixed: 2 * nFive, LastLayout: layout51}
			if got := d.Stats(); !statsEqual(got, want) {
				t.Errorf("stereo downmix: Stats = %+v, want %+v", got, want)
			}

			d = NewDecoder()
			d.SetDownmix(pcm.LayoutMono)
			decodeCount(t, d, stream)
			if got := d.Stats().Downmixed; got != 2*nFive+nTwo {
				t.Errorf("mono downmix: Downmixed = %d, want every frame, %d", got, 2*nFive+nTwo)
			}
		})
	}
}

// TestStatsSurviveReset: Reset is a seek, and the counts are the decoder's
// history rather than the stream position's.
func TestStatsSurviveReset(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			d := NewDecoder()
			n := decodeCount(t, d, readFixture(t, f.fivePtOne))
			d.Reset()
			if got := d.Stats().Frames; got != n {
				t.Errorf("Frames after Reset = %d, want %d", got, n)
			}
		})
	}
}

// TestStatsDoNotAllocate: counting is on the frame path, which allocates
// nothing, and reading the counts allocates nothing either.
func TestStatsDoNotAllocate(t *testing.T) {
	frame, _ := firstFrame(t, "tones_48k_5p1_384k.eac3")
	d := NewDecoder()
	d.SetDownmix(pcm.LayoutStereo)
	if n := testing.AllocsPerRun(50, func() {
		if err := d.DecodeFrame(frame); err != nil {
			t.Fatal(err)
		}
		_ = d.Stats()
	}); n != 0 {
		t.Errorf("%v allocations per frame, want 0", n)
	}
}

// layout51 is the coded order of a 3/2+LFE frame.
var layout51 = pcm.Layout3F2R.WithLFE()

// statsEqual compares the counters, and the layouts by their channels.
func statsEqual(a, b Stats) bool {
	la, lb := a.LastLayout, b.LastLayout
	a.LastLayout, b.LastLayout = nil, nil
	return reflect.DeepEqual(a, b) && slices.Equal(la, lb)
}
