package ac3

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// The tones_ fixtures under testdata are steady sine tones, one frequency per
// channel, encoded to AC-3 by an external encoder. They are synthetic and
// anonymous on purpose: nothing of the corpus this decoder is developed against
// belongs in the repository.
//
// A tone is what makes them worth committing. Transform coefficients cannot be
// compared against a reference decoder, which only ever emits samples, so the
// usual golden file would pin whatever this code happens to do and call that
// correct. A tone has a right answer that comes from outside this package: its
// energy belongs in the transform bin that covers its frequency and nowhere
// else. A decoder that reads one exponent, one bap or one mantissa wrong does
// not produce a slightly wrong tone, it produces noise across the spectrum.
//
// What these do not constrain is the ordering inside a grouped mantissa
// codeword: the bins that get the 3-level quantizer are the ones with almost
// no energy in them, so shuffling them moves no spectrum. That is what
// TestReadMantissaGroups is for.

// mdctBins is the length of the long transform an audio block codes. The 256
// coefficients of a block cover 0 to half the sampling rate, so bin k is
// centred on (k + 0,5) times the rate over this.
const mdctBins = 2 * MaxCoefs

type toneFixture struct {
	file       string
	sampleRate int
	channels   int
	lfe        bool
	// hz gives the frequency of each coded channel, in the order the audio
	// coding mode codes them. A distinct tone per channel means the test also
	// catches a decoder that reads the right bits into the wrong channel.
	hz []float64
}

var toneFixtures = []toneFixture{
	{file: "tones_48k_mono_192k.ac3", sampleRate: 48000, channels: 1, hz: []float64{500}},
	{file: "tones_48k_stereo_192k.ac3", sampleRate: 48000, channels: 2, hz: []float64{500, 1500}},
	{file: "tones_44k1_stereo_192k.ac3", sampleRate: 44100, channels: 2, hz: []float64{500, 1500}},
	{file: "tones_32k_stereo_192k.ac3", sampleRate: 32000, channels: 2, hz: []float64{500, 1500}},
	// The 3/2 mode codes its channels L, C, R, Ls, Rs and then the LFE. Which
	// tone the encoder put in which of them is not this package's claim to
	// make: the frequencies below are what a reference decoder produces from
	// this fixture, measured per channel of its PCM. So this row checks the
	// coded channel order as well as the spectrum, and would catch a decoder
	// that read the right bits into the wrong channel.
	{file: "tones_48k_5p1_448k.ac3", sampleRate: 48000, channels: 5, lfe: true,
		hz: []float64{1500, 500, 900, 1900, 2300, 50}},
}

// toneFrames reads a fixture and returns its frames.
func toneFrames(t *testing.T, name string) [][]byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for len(b) > 0 {
		var h Header
		if err := ParseHeader(b, &h); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if err := CheckCRC(b[:h.Sync.FrameSize]); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		frames = append(frames, b[:h.Sync.FrameSize])
		b = b[h.Sync.FrameSize:]
	}
	if len(frames) < 4 {
		t.Fatalf("%s holds %d frames, too few to test", name, len(frames))
	}
	return frames
}

// TestDecodeToneSpectrum is the phase's deliverable: the transform
// coefficients of a known signal are the ones the signal has.
func TestDecodeToneSpectrum(t *testing.T) {
	for _, f := range toneFixtures {
		t.Run(f.file, func(t *testing.T) {
			frames := toneFrames(t, f.file)
			d := NewDecoder()
			d.SetDither(false) // the noise is not part of the tones

			for i, frame := range frames {
				if err := d.DecodeFrame(frame); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				h := d.Header()
				if h.Sync.SampleRate != f.sampleRate {
					t.Fatalf("frame %d: sample rate %d, want %d", i, h.Sync.SampleRate, f.sampleRate)
				}
				if h.FullBandwidthChannels() != f.channels || h.Lfeon != f.lfe {
					t.Fatalf("frame %d: %d channels, lfe %v, want %d and %v",
						i, h.FullBandwidthChannels(), h.Lfeon, f.channels, f.lfe)
				}
				// The first frame is the encoder settling in and the last one
				// is the tone running out; both are half a window of silence.
				if i == 0 || i == len(frames)-1 {
					continue
				}
				for blk := range BlocksPerFrame {
					b := d.Block(blk)
					for ch, hz := range f.hz {
						if ch < f.channels && b.Blksw[ch] {
							t.Fatalf("frame %d block %d: a steady tone should not switch blocks", i, blk)
						}
						// The bin whose centre frequency is this channel's tone.
						want := hz*float64(mdctBins)/float64(f.sampleRate) - 0.5
						checkTone(t, i, blk, ch, &b.Coeffs[ch], want)
					}
				}
			}
		})
	}
}

// energy returns the total energy of a channel's coefficients, and where its
// loudest bin is.
func energy(coeffs *[MaxCoefs]float32) (total float64, peakBin int) {
	peak := 0.0
	for bin, c := range coeffs {
		e := float64(c) * float64(c)
		total += e
		if e > peak {
			peak, peakBin = e, bin
		}
	}
	return total, peakBin
}

// checkTone checks that one block's coefficients hold a tone at the given bin.
func checkTone(t *testing.T, frame, blk, ch int, coeffs *[MaxCoefs]float32, want float64) {
	t.Helper()

	total, peakBin := energy(coeffs)
	if total == 0 {
		t.Fatalf("frame %d block %d channel %d: every coefficient is zero", frame, blk, ch)
	}
	// The tone does not sit exactly on a bin centre and the transform's window
	// spreads it either way, so the peak may land on either side of it.
	if math.Abs(float64(peakBin)-want) > 1.5 {
		t.Fatalf("frame %d block %d channel %d: the loudest coefficient is bin %d, the tone is at bin %.2f",
			frame, blk, ch, peakBin, want)
	}
	// And it has to be a tone, not a peak in a noise floor: the handful of
	// bins around it hold nearly all the energy there is.
	lo, hi := max(0, peakBin-3), min(MaxCoefs, peakBin+4)
	near := 0.0
	for _, c := range coeffs[lo:hi] {
		near += float64(c) * float64(c)
	}
	if near/total < 0.98 {
		t.Fatalf("frame %d block %d channel %d: bins %d..%d hold %.1f%% of the energy, want a tone",
			frame, blk, ch, lo, hi-1, 100*near/total)
	}
}

// TestDecodeToneFillsTheFrame is the structural half of the same claim: after
// six audio blocks the reader sits within a few bits of where the frame's
// trailer begins.
//
// This is what pins Header.AudioStartBit, which nothing before this phase
// checked. The check words cover the frame's bytes, not this package's reading
// of them, so a bit stream information field parsed at the wrong width would
// have passed every earlier test. It cannot pass this one: audio blocks are
// self-delimiting from wherever they are read, so a decode that starts one bit
// out consumes a different number of bits and lands nowhere near the end.
func TestDecodeToneFillsTheFrame(t *testing.T) {
	for _, f := range toneFixtures {
		t.Run(f.file, func(t *testing.T) {
			frames := toneFrames(t, f.file)
			d := NewDecoder()
			for i, frame := range frames {
				if err := d.DecodeFrame(frame); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
				avail := len(frame)*8 - blockTrailerBits
				slack := avail - d.BlockEndBit()
				// The encoder spends the frame's bits on mantissas until they
				// no longer fit, so what is left over is small. A wide bound
				// here still rules out a mis-parse, which misses by hundreds.
				if slack < 0 || slack > 2048 {
					t.Fatalf("frame %d of %d bits: the audio ends %d bits short of its trailer",
						i, len(frame)*8, slack)
				}
			}
		})
	}
}

// TestDecodeToneIsDeterministic checks the property the segmented conversion
// this decoder feeds is built on: a frame decodes to the same coefficients
// whatever was decoded before it, noise included.
func TestDecodeToneIsDeterministic(t *testing.T) {
	frames := toneFrames(t, "tones_48k_stereo_192k.ac3")

	first := NewDecoder()
	for _, frame := range frames {
		if err := first.DecodeFrame(frame); err != nil {
			t.Fatal(err)
		}
	}
	want := first.Block(3).Coeffs

	// A decoder that has seen nothing else, given only the last frame.
	fresh := NewDecoder()
	if err := fresh.DecodeFrame(frames[len(frames)-1]); err != nil {
		t.Fatal(err)
	}
	if fresh.Block(3).Coeffs != want {
		t.Error("the same frame decoded differently after a different history")
	}
}

func TestDecoderRejects(t *testing.T) {
	frames := toneFrames(t, "tones_48k_stereo_192k.ac3")
	good := frames[1]

	tests := []struct {
		name  string
		frame func() []byte
		want  error
	}{
		{
			name:  "not a frame",
			frame: func() []byte { return []byte{0, 1, 2, 3, 4, 5, 6, 7} },
			want:  ErrNoSync,
		},
		{
			name:  "truncated to the header",
			frame: func() []byte { return good[:16] },
			want:  ErrShortFrame,
		},
		{
			// The header still parses, so the decode gets as far as the audio
			// and then finds a bit stream no encoder could have written.
			name: "audio blocks are all ones",
			frame: func() []byte {
				b := make([]byte, len(good))
				copy(b, good)
				for i := 8; i < len(b); i++ {
					b[i] = 0xff
				}
				return b
			},
			want: ErrReserved,
		},
		{
			// Block 0 has no block before it, so every field that could be
			// inherited has to be there. A frame padded with zeros says the
			// opposite from its very first bit.
			name: "block 0 inherits from a block that does not exist",
			frame: func() []byte {
				s := defaultBSI()
				s.acmod, s.lfeon = Acmod3F2R, true
				return synth(t, 0, 30, s)
			},
			want: ErrAudBlk,
		},
		{
			name:  "the frame is cut short of its audio",
			frame: func() []byte { return good[:len(good)/2] },
			want:  ErrShortFrame,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := NewDecoder()
			if err := d.DecodeFrame(tt.frame()); !errors.Is(err, tt.want) {
				t.Fatalf("DecodeFrame = %v, want %v", err, tt.want)
			}
		})
	}
}

// closeTo reports whether got and want agree to within a relative tolerance.
// The values compared with it come out of float32 arithmetic, so they carry
// rounding but nothing larger.
func closeTo(got, want, tol float32) bool {
	diff := math.Abs(float64(got) - float64(want))
	if mag := math.Abs(float64(want)); mag > 1 {
		return diff/mag <= float64(tol)
	}
	return diff <= float64(tol)
}

// TestDecoderDecouplesIntoCoupledChannels checks the shape of decoupling
// without depending on the coupling coordinates themselves.
//
// A coupling band carries one gain, however many sub-bands the encoder merged
// into it, so a channel's coefficients are the coupling channel's times a
// single constant across the whole band: the ratio between the two has to come
// out the same at every bin of it. That is what pins the band walk. The
// fixture merges its three sub-bands into one band, so a decoder that ignored
// cplbndstrc would reach for two gains the encoder never sent and the ratio
// would break at the sub-band boundary.
//
// This says nothing about what the gain should be. Only the oracle can, and
// that is the phase's real deliverable; this catches the structural mistakes
// that would otherwise be read as a wrong coordinate formula.
func TestDecoderDecouplesIntoCoupledChannels(t *testing.T) {
	frames := toneFrames(t, "tones_48k_5p1_448k.ac3")
	d := NewDecoder()
	d.SetDither(false)

	coupled, checked, nonzero := 0, 0, 0
	for i, frame := range frames {
		if err := d.DecodeFrame(frame); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		for blk := range BlocksPerFrame {
			b := d.Block(blk)
			if !b.Cplinu {
				continue
			}
			for ch := range d.h.FullBandwidthChannels() {
				if !b.Chincpl[ch] {
					continue
				}
				coupled++
				// The channel now reaches as far as the coupling channel did.
				if b.EndMant[ch] != b.CplEndMant {
					t.Fatalf("frame %d block %d channel %d: reaches bin %d, the coupling channel ends at %d",
						i, blk, ch, b.EndMant[ch], b.CplEndMant)
				}
				for bin := b.CplEndMant; bin < MaxCoefs; bin++ {
					if b.Coeffs[ch][bin] != 0 {
						t.Fatalf("frame %d block %d channel %d bin %d is above the coupling channel's end but not zero",
							i, blk, ch, bin)
					}
				}

				// Walk the bands the way the encoder built them: a sub-band
				// joins the band before it unless cplbndstrc opens a new one.
				band, start := 0, b.CplStrtMant
				var want float32
				var have bool
				for sub := range d.ncplsubnd {
					if sub > 0 && !d.cplbndstrc[sub] {
						band, want, have = band+1, 0, false
					}
					lo := start + sub*12
					for bin := lo; bin < min(lo+12, b.CplEndMant); bin++ {
						if b.Cpl[bin] == 0 {
							continue
						}
						if b.Coeffs[ch][bin] != 0 {
							nonzero++
						}
						got := b.Coeffs[ch][bin] / b.Cpl[bin]
						if !have {
							want, have = got, true
							continue
						}
						checked++
						if !closeTo(got, want, 1e-4) {
							t.Fatalf("frame %d block %d channel %d bin %d (band %d): ratio to the coupling "+
								"channel is %v, but %v earlier in the same band: one band carries one gain",
								i, blk, ch, bin, band, got, want)
						}
					}
				}
			}
			for bin := 0; bin < b.CplStrtMant; bin++ {
				if b.Cpl[bin] != 0 {
					t.Fatalf("frame %d block %d: the coupling channel wrote bin %d, below its start %d",
						i, blk, bin, b.CplStrtMant)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatal("no band carried two non-zero bins, so no two ratios were ever compared: " +
			"the test proved nothing")
	}
	// A coupled channel whose every bin came out silent while the coupling
	// channel had energy would satisfy every ratio above, constantly and
	// vacuously.
	if nonzero == 0 {
		t.Fatal("the coupling channel had energy and every coupled channel came out silent")
	}
	t.Logf("checked %d coupled channel blocks, %d ratio comparisons, %d non-zero bins", coupled, checked, nonzero)
}

func TestDecodeFrameDoesNotAllocate(t *testing.T) {
	frames := toneFrames(t, "tones_48k_5p1_448k.ac3")
	d := NewDecoder()
	if err := d.DecodeFrame(frames[1]); err != nil {
		t.Fatal(err)
	}
	if n := testing.AllocsPerRun(50, func() {
		if err := d.DecodeFrame(frames[1]); err != nil {
			t.Fatal(err)
		}
	}); n != 0 {
		t.Fatalf("DecodeFrame allocates %v times per frame", n)
	}
}

func BenchmarkDecodeFrame(b *testing.B) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tones_48k_5p1_448k.ac3"))
	if err != nil {
		b.Fatal(err)
	}
	var h Header
	if err := ParseHeader(raw, &h); err != nil {
		b.Fatal(err)
	}
	frame := raw[h.Sync.FrameSize : 2*h.Sync.FrameSize]

	d := NewDecoder()
	b.ReportAllocs()
	b.SetBytes(int64(len(frame)))
	for b.Loop() {
		if err := d.DecodeFrame(frame); err != nil {
			b.Fatal(err)
		}
	}
}

// BenchmarkDecodeFrameDownmix prices the downmix: it was added without its cost
// ever being measured against the real-time budget.
func BenchmarkDecodeFrameDownmix(b *testing.B) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tones_48k_5p1_448k.ac3"))
	if err != nil {
		b.Fatal(err)
	}
	var h Header
	if err := ParseHeader(raw, &h); err != nil {
		b.Fatal(err)
	}
	frame := raw[h.Sync.FrameSize : 2*h.Sync.FrameSize]

	for _, bench := range []struct {
		name   string
		layout pcm.Layout
	}{
		{"stereo", pcm.Layout{pcm.ChannelLeft, pcm.ChannelRight}},
		{"mono", pcm.Layout{pcm.ChannelCenter}},
	} {
		b.Run(bench.name, func(b *testing.B) {
			d := NewDecoder()
			if err := d.SetDownmix(bench.layout); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(len(frame)))
			for b.Loop() {
				if err := d.DecodeFrame(frame); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
