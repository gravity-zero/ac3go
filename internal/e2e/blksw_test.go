package e2e

import (
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// The short block path - the one a channel takes when the encoder splits a
// block into two 256-point transforms because the signal has a transient in it
// - had nothing behind it until this test. The transform itself is checked
// against its own naive definition in the ac3 package, but the wiring around it
// (the odd/even interleave, the second transform feeding the delay buffer, the
// window applied to each half) is only exercised by a stream that switches, and
// none of the real streams in the corpus do it often enough to notice: a whole
// track carries one to three switched blocks out of a thousand.
//
// The fixture below is the way out, and it does something no other comparison
// in this repo can do: it checks the samples exactly rather than against an RMS
// bound. Every real stream sets the dither flag on every block, which fills the
// bins the encoder spent no bits on with noise that is local to each decoder,
// so two conforming decoders disagree there by construction and only an RMS bar
// is possible. A conforming encoder must switch the dither off around a
// switched block, and the encoder that made this fixture does. That leaves the
// switched spans with no noise in them at all, on either side, so what is left
// to compare is arithmetic - and arithmetic has to match.
//
// How the fixture was made, and why it is not the reference's own encoder: the
// reference cannot produce a switched block at any setting. Its encoder writes
// the flag as a literal zero and compiles in no short transform, so a stream
// out of it is 0 for 0 switched no matter how percussive the input. The fixture
// therefore comes from an independent encoder (Aften, `-s 1`), which is a
// virtue rather than a workaround: the bits under test were produced by neither
// this decoder nor the one it is compared against.
//
//	# a noise floor the transient detector can measure against, plus hard clicks
//	ffmpeg -f lavfi -i "aevalsrc=0.03*random(0)-0.015+0.95*(lt(mod(t*10,1),0.0004)):s=48000:d=1:c=stereo" -c:a pcm_s16le fx.wav
//	aften -s 1 -b 192 fx.wav transients_48k_stereo_192k.ac3
//
// The signal is shaped for the detector rather than for the ear: Aften decides
// on the 8 kHz high-passed signal and ignores any block whose earlier half sits
// under a floor, so clicks on digital silence - the loudest transient there is
// - are never flagged. Clicks over a noise floor are.
const transientFixture = "transients_48k_stereo_192k.ac3"

// blockLabel says what a channel's block was doing, which is what decides
// whether its samples can be compared exactly.
type blockLabel struct {
	short    bool // this block, or the one it overlaps with, used short transforms
	dithered bool // this block, or the one it overlaps with, asked for noise
}

// labelBlocks walks a stream and labels every 256-sample block of every
// channel. A block's output samples come from its own coefficients overlapped
// with its predecessor's, so a span is only free of noise when neither of the
// two blocks that produced it asked for any, and it only exercises the short
// path when either of them took it.
func labelBlocks(t testing.TB, stream []byte, nch int) [][]blockLabel {
	t.Helper()
	labels := make([][]blockLabel, nch)
	d := ac3.NewDecoder()
	prevShort := make([]bool, nch)
	prevDither := make([]bool, nch)
	for len(stream) > 0 {
		var h ac3.Header
		if err := ac3.ParseHeader(stream, &h); err != nil {
			t.Fatalf("parse: %v", err)
		}
		if err := d.DecodeFrame(stream[:h.Sync.FrameSize]); err != nil {
			t.Fatalf("decode: %v", err)
		}
		for b := range ac3.BlocksPerFrame {
			blk := d.Block(b)
			for ch := range nch {
				sw, di := blk.Blksw[ch], blk.Dithflag[ch]
				labels[ch] = append(labels[ch], blockLabel{
					short:    sw || prevShort[ch],
					dithered: di || prevDither[ch],
				})
				prevShort[ch], prevDither[ch] = sw, di
			}
		}
		stream = stream[h.Sync.FrameSize:]
	}
	return labels
}

// TestDecodeBlockSwitchedAgainstReference decodes a stream that uses short
// transforms and holds the samples they produced to an exact comparison.
func TestDecodeBlockSwitchedAgainstReference(t *testing.T) {
	o := Setup(t)

	stream, err := os.ReadFile(filepath.Join("..", "..", "ac3", "testdata", transientFixture))
	if err != nil {
		t.Fatal(err)
	}

	want := o.DecodePCM(t, stream, 16)
	got, nch := decodeStream(t, stream)
	if len(got) != len(want) {
		t.Fatalf("sample counts differ: got %d, reference %d", len(got), len(want))
	}
	labels := labelBlocks(t, stream, nch)

	// Split the error by what produced it. The switched-and-silent group is the
	// one this test exists for; the others are reported to give it context and
	// to show the fixture is not degenerate.
	type group struct {
		n   int
		sum float64
		max int32
	}
	var short, shortNoisy, long group
	add := func(g *group, e int32) {
		g.n++
		g.sum += float64(e) * float64(e)
		if e < 0 {
			e = -e
		}
		if e > g.max {
			g.max = e
		}
	}
	for ch := range nch {
		for b, l := range labels[ch] {
			for i := range 256 {
				idx := (b*256+i)*nch + ch
				if idx >= len(got) {
					continue
				}
				e := got[idx] - want[idx]
				switch {
				case l.short && !l.dithered:
					add(&short, e)
				case l.short:
					add(&shortNoisy, e)
				default:
					add(&long, e)
				}
			}
		}
	}
	rms := func(g group) float64 {
		if g.n == 0 {
			return 0
		}
		return math.Sqrt(g.sum / float64(g.n))
	}
	t.Logf("short, no dither: n=%d rms=%.3f LSB max=%d LSB", short.n, rms(short), short.max)
	t.Logf("short, dithered:  n=%d rms=%.3f LSB max=%d LSB", shortNoisy.n, rms(shortNoisy), shortNoisy.max)
	t.Logf("long:             n=%d rms=%.3f LSB max=%d LSB", long.n, rms(long), long.max)

	// Without this the test could pass by proving nothing at all: a fixture
	// that stopped switching, or a decoder that stopped reporting that it had,
	// would leave the group empty and every bound below trivially true.
	if short.n == 0 {
		t.Fatal("the fixture produced no dither-free short block: it cannot test the short path")
	}

	// The bar the whole point rests on. Nothing separates the two decoders here
	// but the order they round in, so 2 LSB is generous and a real defect in
	// the short path - a half-window misplaced, an interleave reversed - misses
	// it by orders of magnitude rather than by a bit.
	if int(short.max) > QuasiExact.MaxLSB {
		t.Errorf("short blocks differ from the reference by up to %d LSB, allowed %d",
			short.max, QuasiExact.MaxLSB)
	}

	// The short path is what the test is for, but the other two groups are
	// measured and then only printed, and the long one is most of the fixture.
	// Without a bar on the whole buffer, a regression that wrecked every long
	// block would pass here with a line in the log.
	res, err := Compare(got, want, Dithered)
	if err != nil {
		t.Errorf("whole stream: %v", err)
	}
	t.Logf("whole stream: %s (bar: %s)", res, Dithered)
}
