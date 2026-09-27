package ac3

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// The end to end test in internal/e2e is what proves these coefficients, by
// comparing real downmixed streams against the reference. It needs the oracle
// to run, which the CI will not have, and it reports one number for the whole
// stream. These pin the arithmetic where it can be read: what each channel
// contributes, to which side, at what level.

// header51 is a 3/2 header stating mix levels by code.
func header51(cmixlev, surmixlev uint8) *Header {
	h := &Header{Acmod: Acmod3F2R, Lfeon: true}
	h.HasCmixlev, h.Cmixlev = true, cmixlev
	h.HasSurmixlev, h.Surmixlev = true, surmixlev
	return h
}

// TestDownmixCoeffsSumToOne is the property the whole level policy is: every
// coded channel at full scale sums to full scale in each output, and to no
// more. It holds for every mode and every pair of stated levels, which is what
// makes it a property rather than a table.
func TestDownmixCoeffsSumToOne(t *testing.T) {
	for acmod := range uint8(8) {
		for cmixlev := range uint8(4) {
			for surmixlev := range uint8(4) {
				h := &Header{Acmod: acmod}
				h.HasCmixlev = acmod&1 != 0 && acmod != AcmodMono
				h.Cmixlev = cmixlev
				h.HasSurmixlev = acmod&4 != 0
				h.Surmixlev = surmixlev

				var c downmixCoeffs
				setDownmixCoeffs(h, &c)

				for out := range 2 {
					var sum float64
					for ch := range h.FullBandwidthChannels() {
						if c[out][ch] < 0 {
							t.Errorf("acmod %d out %d ch %d: negative coefficient %v: "+
								"the Lo/Ro downmix adds channels, it does not subtract them",
								acmod, out, ch, c[out][ch])
						}
						sum += float64(c[out][ch])
					}
					if math.Abs(sum-1) > 1e-6 {
						t.Errorf("acmod %d, cmixlev %d, surmixlev %d: output %d sums to %v, want 1",
							acmod, cmixlev, surmixlev, out, sum)
					}
				}
			}
		}
	}
}

// TestDownmix51Coeffs works the 3/2 case through by hand, since it is the one
// every real stream uses.
func TestDownmix51Coeffs(t *testing.T) {
	// Code 1 is -4,5 dB for the centre and -6 dB for the surrounds, which is
	// what a real encoder states and what the reserved code falls back to.
	h := header51(1, 1)
	var c downmixCoeffs
	setDownmixCoeffs(h, &c)

	const cmix, smix = 0.5946035575013605, 0.5
	norm := 1 / (1 + cmix + smix)

	// L C R Ls Rs. Left takes L, the centre and the left surround; right takes
	// the mirror. Neither takes the other's front or surround, and the LFE is
	// in neither: it is not a full bandwidth channel and never reaches here.
	for _, w := range []struct {
		out, ch int
		want    float64
	}{
		{0, 0, norm}, {0, 1, cmix * norm}, {0, 2, 0}, {0, 3, smix * norm}, {0, 4, 0},
		{1, 0, 0}, {1, 1, cmix * norm}, {1, 2, norm}, {1, 3, 0}, {1, 4, smix * norm},
	} {
		if got := float64(c[w.out][w.ch]); math.Abs(got-w.want) > 1e-6 {
			t.Errorf("coeff[out %d][ch %d] = %v, want %v", w.out, w.ch, got, w.want)
		}
	}

	// The level this lands on, stated as the number it is: a channel that had
	// the output to itself comes out 6,42 dB down. It is not a rounding of the
	// 7,65 dB the spec's fixed factor would give, it is a different policy, and
	// this is the line that would move if the policy did.
	if db := 20 * math.Log10(norm); math.Abs(db-(-6.42)) > 0.01 {
		t.Errorf("a lone channel comes out %.2f dB down, want -6.42", db)
	}
}

// TestDownmixCoeffsPerMode works every remaining mode through by hand, the way
// TestDownmix51Coeffs does for 3/2.
//
// TestDownmixCoeffsSumToOne cannot stand in for this. setDownmixCoeffs ends by
// dividing each output by its own sum, so "sums to one" restates the last four
// lines of the function and holds whatever the coefficients were before them.
// Swapping Ls and Rs in 2/2, or giving the centre of 3/1 two different levels,
// leaves every sum at one - so what needs pinning is which channel reaches
// which output, and at what level relative to the others.
//
// The wants here are the levels the spec names, written out before any
// normalisation; the test divides by its own sum rather than by the code's.
func TestDownmixCoeffsPerMode(t *testing.T) {
	// Code 1 is -4,5 dB at the centre and -6 dB at the surrounds.
	const cmix, smix = levelMinus4Point5dB, 0.5
	// One surround feeding two outputs is 3 dB down on each, which is what
	// splitting a channel across two costs if its power is to survive.
	const split = smix * levelMinus3dB

	for _, c := range []struct {
		name   string
		acmod  uint8
		lo, ro []float64
	}{
		// L C R: the centre is the only channel that reaches both sides.
		{"3/0", Acmod3F, []float64{1, cmix, 0}, []float64{0, cmix, 1}},
		// L R S: the lone surround reaches both, evenly.
		{"2/1", Acmod2F1R, []float64{1, 0, split}, []float64{0, 1, split}},
		// L C R S: both of the above at once.
		{"3/1", Acmod3F1R, []float64{1, cmix, 0, split}, []float64{0, cmix, 1, split}},
		// L R Ls Rs: two surrounds, one per side, no split and no crossing.
		{"2/2", Acmod2F2R, []float64{1, 0, smix, 0}, []float64{0, 1, 0, smix}},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := &Header{Acmod: c.acmod}
			h.HasCmixlev = c.acmod&1 != 0 && c.acmod != AcmodMono
			h.Cmixlev = 1
			h.HasSurmixlev = c.acmod&4 != 0
			h.Surmixlev = 1

			var got downmixCoeffs
			setDownmixCoeffs(h, &got)

			for out, want := range [2][]float64{c.lo, c.ro} {
				var sum float64
				for _, v := range want {
					sum += v
				}
				for ch, v := range want {
					if math.Abs(float64(got[out][ch])-v/sum) > 1e-6 {
						t.Errorf("out %d ch %d: %v, want %v",
							out, ch, got[out][ch], v/sum)
					}
				}
			}
		})
	}
}

// TestDownmixMonoSurroundSplits pins the 2/1 and 3/1 modes, whose single
// surround has to reach both outputs without gaining power on the way.
func TestDownmixMonoSurroundSplits(t *testing.T) {
	h := &Header{Acmod: Acmod2F1R}
	h.HasSurmixlev, h.Surmixlev = true, 0 // -3 dB
	var c downmixCoeffs
	setDownmixCoeffs(h, &c)

	if c[0][2] != c[1][2] {
		t.Errorf("the single surround reaches the two outputs at %v and %v: it must be even",
			c[0][2], c[1][2])
	}
	if c[0][2] == 0 {
		t.Error("the single surround reaches neither output")
	}
}

// TestSetDownmixRejectsWhatItCannotDo keeps the API honest: a layout this
// decoder cannot produce has to say so rather than hand back something else.
func TestSetDownmixRejectsWhatItCannotDo(t *testing.T) {
	d := NewDecoder()
	if err := d.SetDownmix(pcm.Layout3F2R); err == nil {
		t.Error("SetDownmix accepted 3/2: it can only fold down, not up or across")
	}
	for _, l := range []pcm.Layout{nil, pcm.LayoutStereo, pcm.LayoutMono} {
		if err := d.SetDownmix(l); err != nil {
			t.Errorf("SetDownmix(%v): %v", l, err)
		}
	}
}

// TestDownmixLeavesStereoAlone pins that asking a stereo stream for stereo is
// not a mix: the coefficients would be the identity anyway, but going through
// them would cost a pass over every sample of every frame for nothing.
func TestDownmixLeavesStereoAlone(t *testing.T) {
	d := NewDecoder()
	if err := d.SetDownmix(pcm.LayoutStereo); err != nil {
		t.Fatal(err)
	}
	d.h.Acmod = AcmodStereo
	if d.downmixing() {
		t.Error("a 2/0 stream asked for stereo is being mixed down to itself")
	}
	// Dual mono is the exception: two channels, but not a left and a right.
	d.h.Acmod = AcmodDualMono
	if !d.downmixing() {
		t.Error("a dual mono stream asked for stereo is not being mixed")
	}
}

// The coefficients below are written out rather than taken from
// setDownmixCoeffs, so that the decode tests compare the decoder against the
// spec rather than against itself. Both 5.1 fixtures state a centre at -4.5 dB
// and surrounds at -6 dB, the encoder's defaults, and the tests check that the
// headers say so before relying on it.
var (
	fixtureCmix = float32(math.Pow(2, -4.5/6))
	fixtureSmix = float32(0.5)
)

// wantLoRo mixes one frame of coded 3/2 channels, in coded order L C R Ls Rs,
// into Lo/Ro by clause 7.8.2 with each output normalized to sum to one.
func wantLoRo(native [][]float32) (lo, ro []float32) {
	c, s := fixtureCmix, fixtureSmix
	norm := 1 + c + s
	lo = make([]float32, len(native[0]))
	ro = make([]float32, len(native[0]))
	for i := range lo {
		lo[i] = (native[0][i] + c*native[1][i] + s*native[3][i]) / norm
		ro[i] = (native[2][i] + c*native[1][i] + s*native[4][i]) / norm
	}
	return lo, ro
}

// decodeFrames decodes every access unit of stream, handing each frame's output
// planes to fn. It fails the test if the stream stops decoding early.
func decodeFrames(t *testing.T, stream []byte, layout pcm.Layout, fn func(d *Decoder, planes [][]float32)) int {
	t.Helper()
	d := NewDecoder()
	d.SetDither(false) // the noise differs between two decoders; silence does not
	if err := d.SetDownmix(layout); err != nil {
		t.Fatal(err)
	}
	n := 0
	for len(stream) > 0 {
		if err := d.DecodeFrame(stream); err != nil {
			t.Fatalf("frame %d: %v", n, err)
		}
		planes := make([][]float32, d.OutputChannels())
		for ch := range planes {
			planes[ch] = append([]float32(nil), d.Samples(ch)...)
		}
		fn(d, planes)
		stream = stream[d.AccessUnitSize():]
		n++
	}
	return n
}

// TestDownmixDecoded51 decodes a real 5.1 fixture twice, once natively and once
// mixed down, and holds every mixed sample to the native channels mixed by
// hand. It runs on the AC-3 and the E-AC-3 syntax alike: the two take
// different paths through DecodeFrame, and the E-AC-3 one once returned before
// the mix and handed back silence.
func TestDownmixDecoded51(t *testing.T) {
	for _, name := range []string{"tones_48k_5p1_448k.ac3", "tones_48k_5p1_384k.eac3"} {
		t.Run(name, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}

			var native [][][]float32
			decodeFrames(t, stream, nil, func(d *Decoder, planes [][]float32) {
				if len(native) == 0 {
					if d.h.Acmod != Acmod3F2R {
						t.Fatalf("acmod %d, want 3/2: the fixture is not the 5.1 this test assumes", d.h.Acmod)
					}
					if d.h.CenterMixLevel() != fixtureCmix || d.h.SurroundMixLevel() != fixtureSmix {
						t.Fatalf("mix levels %v/%v, want %v/%v: the fixture is not the one this test assumes",
							d.h.CenterMixLevel(), d.h.SurroundMixLevel(), fixtureCmix, fixtureSmix)
					}
				}
				native = append(native, planes)
			})

			check := func(t *testing.T, layout pcm.Layout, want func(lo, ro []float32) [][]float32) {
				var peak float32
				frame := 0
				n := decodeFrames(t, stream, layout, func(d *Decoder, planes [][]float32) {
					if got := d.OutputLayout(); got.String() != layout.String() {
						t.Fatalf("OutputLayout = %s, want %s", got, layout)
					}
					lo, ro := wantLoRo(native[0])
					exp := want(lo, ro)
					for ch := range exp {
						for i, w := range exp[ch] {
							g := planes[ch][i]
							if math.Abs(float64(g-w)) > 1e-6 {
								t.Fatalf("frame %d, output %d, sample %d: got %v, want %v",
									frame, ch, i, g, w)
							}
							peak = max(peak, float32(math.Abs(float64(g))))
						}
					}
					native = native[1:]
					frame++
				})
				if n == 0 {
					t.Fatal("no frame decoded")
				}
				// An absolute floor, so that two silent outputs cannot agree with
				// each other: every coded channel of the fixture peaks near
				// 0.0625, and no mix of them comes out below a quarter of that.
				if peak < 0.0625/4 {
					t.Errorf("mixed output peaks at %v: the downmix is (nearly) silent", peak)
				}
			}

			all := native
			t.Run("stereo", func(t *testing.T) {
				native = all
				check(t, pcm.LayoutStereo, func(lo, ro []float32) [][]float32 {
					return [][]float32{lo, ro}
				})
			})
			t.Run("mono", func(t *testing.T) {
				native = all
				check(t, pcm.LayoutMono, func(lo, ro []float32) [][]float32 {
					m := make([]float32, len(lo))
					for i := range m {
						m[i] = (lo[i] + ro[i]) * float32(math.Sqrt(0.5))
					}
					return [][]float32{m}
				})
			})
		})
	}
}

// dependent71Header builds an E-AC-3 dependent substream of size bytes whose
// header states the standard 7.1 extension, with nothing but zeros after it.
// The header is all a 7.1 merge needs to recognize one; the audio behind it is
// never meant to decode.
func dependent71Header(size int) []byte {
	var w bitWriter
	w.write(0x0B77, 16)
	w.write(uint32(StrmtypDependent), 2)
	w.write(0, 3)                 // substreamid
	w.write(uint32(size/2-1), 11) // frmsiz
	w.write(0, 2)                 // fscod: 48 kHz
	w.write(3, 2)                 // numblkscod: six blocks
	w.write(uint32(Acmod2F2R), 3) // four channels
	w.write(0, 1)                 // lfeon
	w.write(16, 5)                // bsid
	w.write(31, 5)                // dialnorm
	w.write(0, 1)                 // compre
	w.write(1, 1)                 // chanmape
	w.write(eac3Chanmap71, 16)    // chanmap
	for len(w.buf) < size {
		w.write(0, 8)
	}
	return w.buf[:size]
}

// TestDownmix71MixesTheCore pins what a downmix of a 7.1 access unit is: the
// 5.1 core mixed down, the dependent substream stepped over. The core is a
// complete 5.1 mix of the programme - it is what a 5.1 decoder plays - so it is
// what gets folded, and the dependent is not even decoded: the dependent here
// is a header followed by zeros, which would fail or come out as garbage if it
// were.
func TestDownmix71MixesTheCore(t *testing.T) {
	for _, name := range []string{"tones_48k_5p1_448k.ac3", "tones_48k_5p1_384k.eac3"} {
		t.Run(name, func(t *testing.T) {
			stream, err := os.ReadFile(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			var h Header
			if err := ParseHeader(stream, &h); err != nil {
				t.Fatal(err)
			}
			core := stream[:h.Sync.FrameSize]
			dep := dependent71Header(256)
			var dh Header
			if err := ParseHeader(dep, &dh); err != nil {
				t.Fatalf("synthetic dependent header does not parse: %v", err)
			}
			if !is71Extension(&h, &dh) {
				t.Fatal("synthetic dependent is not the standard 7.1 extension")
			}
			au := append(append([]byte(nil), core...), dep...)

			for _, layout := range []pcm.Layout{pcm.LayoutStereo, pcm.LayoutMono} {
				want := NewDecoder()
				want.SetDither(false)
				want.SetDownmix(layout)
				if err := want.DecodeFrame(core); err != nil {
					t.Fatal(err)
				}

				d := NewDecoder()
				d.SetDither(false)
				d.SetDownmix(layout)
				if err := d.DecodeFrame(au); err != nil {
					t.Fatalf("%s: %v", layout, err)
				}
				if got := d.AccessUnitSize(); got != len(au) {
					t.Errorf("%s: AccessUnitSize = %d, want %d: the dependent must still be stepped over",
						layout, got, len(au))
				}
				if got := d.OutputLayout(); got.String() != layout.String() {
					t.Fatalf("%s: OutputLayout = %s: a downmix of 7.1 must not come out as 7.1", layout, got)
				}
				var peak float32
				for ch := range d.OutputChannels() {
					g, w := d.Samples(ch), want.Samples(ch)
					for i := range w {
						if g[i] != w[i] {
							t.Fatalf("%s: output %d sample %d: got %v, want the core's mix %v",
								layout, ch, i, g[i], w[i])
						}
						peak = max(peak, float32(math.Abs(float64(g[i]))))
					}
				}
				if peak == 0 {
					t.Errorf("%s: silent output", layout)
				}
			}
		})
	}
}

// TestDownmixFailedFrameIsAnError pins that a frame that cannot be decoded is
// reported as such with a downmix asked for, on both syntaxes: the mix must
// not turn a decode failure into a silent success.
func TestDownmixFailedFrameIsAnError(t *testing.T) {
	for _, name := range []string{"tones_48k_5p1_448k.ac3", "tones_48k_5p1_384k.eac3"} {
		stream, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		var h Header
		if err := ParseHeader(stream, &h); err != nil {
			t.Fatal(err)
		}
		d := NewDecoder()
		d.SetDownmix(pcm.LayoutStereo)
		if err := d.DecodeFrame(stream[:h.Sync.FrameSize-1]); err == nil {
			t.Errorf("%s: a truncated frame decoded without error under a downmix", name)
		}
	}
}

// altHeader51 is a 3/2 header in the alternate syntax (bsid 6, Annex D) whose
// extended information states Lo/Ro mix levels of its own.
func altHeader51(cmixlev, surmixlev, lorocmixlev, lorosurmixlev uint8) *Header {
	h := header51(cmixlev, surmixlev)
	h.Sync.Bsid = AltBSID
	h.Xbsi1e = true
	h.Lorocmixlev, h.Lorosurmixlev = lorocmixlev, lorosurmixlev
	return h
}

// TestAltSyntaxLoRoMixLevels pins that an alternate syntax frame stating Lo/Ro
// mix levels is mixed at those, not at the two bit codes of the ordinary BSI.
// The extended fields are three bit indices into the same levels the enhanced
// syntax uses, and they are the ones a Lo/Ro downmix is for; the reference
// decoder applies them. Measured on real bsid 6 5.1 cores: cmixlev -3 dB and
// surmixlev -6 dB, lorocmixlev -3 dB and lorosurmixlev -4.5 dB, and the
// reference's stereo mix fits C/L 0.707 and Ls/L 0.594 - the extended pair.
func TestAltSyntaxLoRoMixLevels(t *testing.T) {
	h := altHeader51(0, 1, 4, 5) // the measured case
	if got, want := h.CenterMixLevel(), float32(levelMinus3dB); got != want {
		t.Errorf("CenterMixLevel = %v, want %v (lorocmixlev 4, -3 dB)", got, want)
	}
	if got, want := h.SurroundMixLevel(), float32(levelMinus4Point5dB); got != want {
		t.Errorf("SurroundMixLevel = %v, want %v (lorosurmixlev 5, -4.5 dB), not surmixlev's -6 dB", got, want)
	}

	// The extended centre level reaches the whole table, louder than unity
	// included, which the two bit code never could.
	if got, want := altHeader51(0, 0, 0, 5).CenterMixLevel(), float32(levelPlus3dB); got != want {
		t.Errorf("lorocmixlev 0: CenterMixLevel = %v, want %v (+3 dB)", got, want)
	}
	// The surround one is held to -1.5 dB and below, as the enhanced syntax's is.
	if got, want := altHeader51(0, 0, 4, 0).SurroundMixLevel(), float32(levelMinus1Point5dB); got != want {
		t.Errorf("lorosurmixlev 0: SurroundMixLevel = %v, want %v (clamped to -1.5 dB)", got, want)
	}

	// Without the extended information, the ordinary codes stand.
	plain := altHeader51(0, 1, 4, 5)
	plain.Xbsi1e = false
	if got, want := plain.SurroundMixLevel(), float32(levelMinus6dB); got != want {
		t.Errorf("no xbsi1: SurroundMixLevel = %v, want surmixlev's %v", got, want)
	}
	// And a channel the mode does not code is still not mixed at any level.
	stereo := altHeader51(0, 1, 4, 5)
	stereo.Acmod, stereo.HasCmixlev, stereo.HasSurmixlev = AcmodStereo, false, false
	if stereo.CenterMixLevel() != 1 || stereo.SurroundMixLevel() != 1 {
		t.Errorf("2/0 with xbsi1: levels %v/%v, want 1/1: there is no centre or surround",
			stereo.CenterMixLevel(), stereo.SurroundMixLevel())
	}
}

// TestDownmixRecomputesOnMixLevelChange pins that the coefficients follow the
// header frame by frame: whatever a mix level is stated in, a change of it
// mid-stream reaches the mix, and a change of mode does too.
func TestDownmixRecomputesOnMixLevelChange(t *testing.T) {
	d := NewDecoder()
	if err := d.SetDownmix(pcm.LayoutStereo); err != nil {
		t.Fatal(err)
	}
	steps := []struct {
		name string
		h    *Header
	}{
		{"alt syntax, lorosurmixlev -4.5 dB", altHeader51(0, 1, 4, 5)},
		{"alt syntax, lorosurmixlev -6 dB", altHeader51(0, 1, 4, 6)},
		{"alt syntax, lorocmixlev -4.5 dB", altHeader51(0, 1, 5, 6)},
		{"ordinary syntax", header51(0, 1)},
		{"3/0", func() *Header { h := header51(1, 0); h.Acmod, h.HasSurmixlev = Acmod3F, false; return h }()},
	}
	for _, s := range steps {
		d.h = *s.h
		d.updateDownmix()
		var want downmixCoeffs
		setDownmixCoeffs(&d.h, &want)
		if d.dmixCoeffs != want {
			t.Errorf("%s: kept stale coefficients %v, want %v", s.name, d.dmixCoeffs[0], want[0])
		}
	}
}
