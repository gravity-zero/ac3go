package ac3

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// The decoder's options against both syntaxes. The AC-3 and E-AC-3 paths
// through DecodeFrame are separate code, and an option that only the AC-3 one
// honoured would pass every AC-3 test and go unnoticed: that is how the E-AC-3
// downmix came to hand back silence. So each option here is run on a fixture
// of each syntax, and held to the same answer.

var parityFormats = []struct {
	name      string
	fivePtOne string
	stereo    string
}{
	{"ac3", "tones_48k_5p1_448k.ac3", "tones_48k_stereo_192k.ac3"},
	{"eac3", "tones_48k_5p1_384k.eac3", "tones_48k_stereo_192k.eac3"},
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// decodeAll decodes a whole stream with a decoder set up by configure, and
// returns every frame's output planes, copied.
func decodeAll(t *testing.T, stream []byte, configure func(*Decoder)) [][][]float32 {
	t.Helper()
	d := NewDecoder()
	d.SetDither(false)
	if configure != nil {
		configure(d)
	}
	var frames [][][]float32
	for len(stream) > 0 {
		if err := d.DecodeFrame(stream); err != nil {
			t.Fatalf("frame %d: %v", len(frames), err)
		}
		planes := make([][]float32, d.OutputChannels())
		for ch := range planes {
			planes[ch] = append([]float32(nil), d.Samples(ch)...)
		}
		frames = append(frames, planes)
		stream = stream[d.AccessUnitSize():]
	}
	return frames
}

func peakOf(frames [][][]float32) float64 {
	var p float64
	for _, f := range frames {
		for _, pl := range f {
			for _, v := range pl {
				p = math.Max(p, math.Abs(float64(v)))
			}
		}
	}
	return p
}

// TestDitherParity: turning the dither on puts noise where the dither goes and
// changes the output, and turning it off takes it out, on both syntaxes. Off,
// two decoders agree sample for sample; that is what the option is for.
func TestDitherParity(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			stream := readFixture(t, f.fivePtOne)
			off1 := decodeAll(t, stream, nil)
			off2 := decodeAll(t, stream, nil)
			on := decodeAll(t, stream, func(d *Decoder) { d.SetDither(true) })
			differs := false
			for i := range off1 {
				for ch := range off1[i] {
					for n := range off1[i][ch] {
						if off1[i][ch][n] != off2[i][ch][n] {
							t.Fatalf("dither off: two decodes differ at frame %d ch %d sample %d", i, ch, n)
						}
						if on[i][ch][n] != off1[i][ch][n] {
							differs = true
						}
					}
				}
			}
			if !differs {
				t.Error("dither on and off decode identically: the option does not reach this syntax")
			}
		})
	}
}

// TestTargetLevelParity: a dialogue level target scales every sample by the
// gain the stated dialnorm and the target give, and nothing else, on both
// syntaxes, and whether or not the output is mixed down.
func TestTargetLevelParity(t *testing.T) {
	const target = -20
	for _, f := range parityFormats {
		for _, layout := range []pcm.Layout{nil, pcm.LayoutStereo} {
			t.Run(f.name+"/"+layout.String(), func(t *testing.T) {
				stream := readFixture(t, f.fivePtOne)
				var h Header
				if err := ParseHeader(stream, &h); err != nil {
					t.Fatal(err)
				}
				gain := math.Pow(2, float64(target-h.DialnormDB())/6)
				if gain == 1 {
					t.Fatalf("dialnorm %d dB: pick a target that moves the level", h.DialnormDB())
				}
				plain := decodeAll(t, stream, func(d *Decoder) { d.SetDownmix(layout) })
				level := decodeAll(t, stream, func(d *Decoder) {
					d.SetDownmix(layout)
					d.SetTargetLevel(target)
				})
				for i := range plain {
					for ch := range plain[i] {
						for n, v := range plain[i][ch] {
							want := float64(v) * gain
							if got := float64(level[i][ch][n]); math.Abs(got-want) > 1e-6 {
								t.Fatalf("frame %d ch %d sample %d: got %v, want %v (x%.4f)", i, ch, n, got, want, gain)
							}
						}
					}
				}
				if peakOf(level) == 0 {
					t.Fatal("silent output")
				}
			})
		}
	}
}

// TestResetParity: after Reset, a frame decodes exactly as it does from a
// decoder that has seen nothing before it, on both syntaxes. The enhanced one
// carries band structures from frame to frame as well as the filter bank's
// tail, and Reset has to drop both.
func TestResetParity(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			stream := readFixture(t, f.fivePtOne)
			var h Header
			if err := ParseHeader(stream, &h); err != nil {
				t.Fatal(err)
			}
			size := h.Sync.FrameSize
			const at = 5
			frame := stream[at*size : (at+1)*size]

			cold := NewDecoder()
			cold.SetDither(false)
			if err := cold.DecodeFrame(frame); err != nil {
				t.Fatal(err)
			}

			warm := NewDecoder()
			warm.SetDither(false)
			for i := range at {
				if err := warm.DecodeFrame(stream[i*size : (i+1)*size]); err != nil {
					t.Fatal(err)
				}
			}
			warm.Reset()
			if err := warm.DecodeFrame(frame); err != nil {
				t.Fatal(err)
			}
			for ch := range cold.OutputChannels() {
				c, w := cold.Samples(ch), warm.Samples(ch)
				for n := range c {
					if c[n] != w[n] {
						t.Fatalf("ch %d sample %d: after Reset %v, cold %v", ch, n, w[n], c[n])
					}
				}
			}
			if peakOf([][][]float32{{cold.Samples(0)}}) == 0 {
				t.Fatal("silent frame: nothing compared")
			}
		})
	}
}

// TestOutputShapeParity: with a downmix set, what the decoder says it hands
// back - layout, channel count, bytes consumed - is the same for both syntaxes,
// for a source that needs mixing and for one that already is the layout.
func TestOutputShapeParity(t *testing.T) {
	for _, f := range parityFormats {
		for _, src := range []string{f.fivePtOne, f.stereo} {
			for _, layout := range []pcm.Layout{pcm.LayoutStereo, pcm.LayoutMono} {
				t.Run(src+"/"+layout.String(), func(t *testing.T) {
					stream := readFixture(t, src)
					var h Header
					if err := ParseHeader(stream, &h); err != nil {
						t.Fatal(err)
					}
					d := NewDecoder()
					d.SetDither(false)
					if err := d.SetDownmix(layout); err != nil {
						t.Fatal(err)
					}
					if err := d.DecodeFrame(stream); err != nil {
						t.Fatal(err)
					}
					if got := d.OutputLayout(); got.String() != layout.String() {
						t.Errorf("OutputLayout = %s, want %s", got, layout)
					}
					if got := d.OutputChannels(); got != len(layout) {
						t.Errorf("OutputChannels = %d, want %d", got, len(layout))
					}
					if got := d.AccessUnitSize(); got != h.Sync.FrameSize {
						t.Errorf("AccessUnitSize = %d, want the syncframe's %d", got, h.Sync.FrameSize)
					}
					for ch := range d.OutputChannels() {
						if n := len(d.Samples(ch)); n != BlocksPerFrame*SamplesPerBlock {
							t.Errorf("Samples(%d) holds %d samples, want %d", ch, n, BlocksPerFrame*SamplesPerBlock)
						}
					}
				})
			}
		}
	}
}

// TestDownmixAcrossModeChange runs a stream whose mode changes mid-stream - 5.1,
// then stereo, then 5.1 again - through a stereo and a mono downmix, on both
// syntaxes. Every frame has to come out as its own mode mixed by its own
// coefficients: a stereo frame asked for stereo is its own two channels, not
// a mix at the 5.1 coefficients and not silence, and the 5.1 after it is
// mixed again.
//
// The reference for each frame is the same concatenated stream decoded without
// a downmix, so the filter bank's carry over across the change is the same on
// both sides and only the mix is being compared.
func TestDownmixAcrossModeChange(t *testing.T) {
	for _, f := range parityFormats {
		t.Run(f.name, func(t *testing.T) {
			five, two := readFixture(t, f.fivePtOne), readFixture(t, f.stereo)
			var stream []byte
			stream = append(stream, five...)
			stream = append(stream, two...)
			stream = append(stream, five...)

			native := decodeAll(t, stream, nil)
			for _, target := range []struct {
				layout pcm.Layout
				mix    func(planes [][]float32) [][]float32
			}{
				{pcm.LayoutStereo, func(p [][]float32) [][]float32 { return mixTo(p, false) }},
				{pcm.LayoutMono, func(p [][]float32) [][]float32 { return mixTo(p, true) }},
			} {
				mixed := decodeAll(t, stream, func(d *Decoder) { d.SetDownmix(target.layout) })
				if len(mixed) != len(native) {
					t.Fatalf("%s: %d frames, native %d", target.layout, len(mixed), len(native))
				}
				var modes [2]int // frames seen of each source width
				for i := range native {
					if len(native[i]) == 6 {
						modes[0]++
					} else {
						modes[1]++
					}
					want := target.mix(native[i])
					if len(mixed[i]) != len(want) {
						t.Fatalf("%s frame %d: %d planes, want %d", target.layout, i, len(mixed[i]), len(want))
					}
					var peak float64
					for ch := range want {
						for n, w := range want[ch] {
							g := mixed[i][ch][n]
							if math.Abs(float64(g-w)) > 1e-6 {
								t.Fatalf("%s frame %d (%d channels coded) out %d sample %d: got %v, want %v",
									target.layout, i, len(native[i]), ch, n, g, w)
							}
							peak = math.Max(peak, math.Abs(float64(g)))
						}
					}
					if peak == 0 {
						t.Fatalf("%s frame %d (%d channels coded): silent", target.layout, i, len(native[i]))
					}
				}
				if modes[0] == 0 || modes[1] == 0 {
					t.Fatalf("the stream did not change mode: %v", modes)
				}
			}
		})
	}
}

// mixTo folds one frame of native planes - 3/2+LFE in coded order, or a 2/0
// pair - into stereo or mono by the spec's Lo/Ro, written out here rather than
// taken from the decoder. The 5.1 levels are the fixtures' (see
// wantLoRo); a stereo pair is its own Lo/Ro.
func mixTo(planes [][]float32, mono bool) [][]float32 {
	var lo, ro []float32
	if len(planes) == 6 {
		lo, ro = wantLoRo(planes)
	} else {
		lo, ro = planes[0], planes[1]
	}
	if !mono {
		return [][]float32{lo, ro}
	}
	m := make([]float32, len(lo))
	for i := range m {
		// Mono is Lo plus Ro, 3 dB down so the sum cannot pass full scale.
		m[i] = (lo[i] + ro[i]) * float32(math.Sqrt(0.5))
	}
	return [][]float32{m}
}
