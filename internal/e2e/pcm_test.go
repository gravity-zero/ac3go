package e2e

import (
	"math"
	"path"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/pcm"
)

// This is the test the decoder is for: samples out of ac3go against samples
// out of the reference, on real streams, sample by sample at zero offset.
//
// Everything before it checks the decoder against the format's description of
// itself - the spec's tables, its own tones, its own arithmetic. This checks it
// against the thing a listener will actually compare it to.
//
// The bar is an RMS one and cannot be tighter, for a reason that is in the
// format rather than in either decoder: the bins the encoder spent no bits on
// are filled with noise, and the spec asks only for "any reasonably random
// sequence". Two conforming decoders disagree there by design. Every real
// stream measured so far sets the dither flag on every block of every channel,
// so there is no non-dithered stream to compare exactly against. What is left
// is to turn this decoder's own noise off, which leaves the reference's noise
// as the whole of the difference, and to hold that difference to a bound a
// real defect could not hide under.

// findMedia lists every media file of a corpus. The tests here search for a
// track with a given shape and stop when they have enough, so they cannot take
// the capped listing the whole-corpus tests use: the track they want may be the
// four hundredth file.
func findMedia(t testing.TB, o *Oracle, dir string) []string {
	t.Helper()
	return o.mediaFiles(t, dir, 0)
}

// stereoTrack names one stereo AC-3 track of the corpus.
type stereoTrack struct {
	file  string
	index int
	rate  int
}

// findStereoTracks returns up to max stereo AC-3 tracks, preferring a spread of
// sampling rates: the corpus carries 44,1 kHz as well as 48, and the two take
// different paths through the bit allocation's hearing threshold table.
func findStereoTracks(t testing.TB, o *Oracle, corpus string, maxTracks int) []stereoTrack {
	t.Helper()
	var out []stereoTrack
	seenRate := map[int]int{}
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.AC3Tracks(t, f) {
			if tr.Channels != 2 {
				continue
			}
			// Two per rate is enough; the point is coverage, not volume.
			if seenRate[tr.SampleRate] >= 2 {
				continue
			}
			seenRate[tr.SampleRate]++
			out = append(out, stereoTrack{f, tr.Index, tr.SampleRate})
			if len(out) >= maxTracks {
				return out
			}
		}
	}
	return out
}

// referenceOrder is the order the reference writes channels in, which is not
// the order AC-3 codes them: the format codes the front channels left to right
// and puts the LFE last, the reference pairs the fronts and puts the LFE in
// the middle. Every comparison has to cross that.
//
// This mapping is not read off a document. It was measured, by correlating
// every decoded channel against every reference channel on a real 5.1 stream:
// each one matched exactly one, above 0,9999.
var referenceOrder = []pcm.Channel{
	pcm.ChannelLeft,
	pcm.ChannelRight,
	pcm.ChannelCenter,
	pcm.ChannelLFE,
	pcm.ChannelLeftSurround,
	pcm.ChannelRightSurround,
	// The lone surround of the 2/1 and 3/1 modes. The reference writes it
	// after everything above, which never shows: no AC-3 mode codes it
	// alongside a surround pair or an LFE.
	pcm.ChannelMonoSurround,
	// The two programmes of the 1+1 mode, which the reference writes as a
	// stereo pair, first programme left. They coexist with nothing: a dual
	// mono frame codes these two channels and no others.
	pcm.ChannelCh1,
	pcm.ChannelCh2,
	// The extra channels of a 7.1 programme. The reference writes them after
	// the 5.1 core, back pair before side pair, which is the order Layout7point1
	// gives - so the crossing is the identity for 7.1.
	pcm.ChannelBackLeft,
	pcm.ChannelBackRight,
	pcm.ChannelSideLeft,
	pcm.ChannelSideRight,
}

// refIndex returns, for each coded channel, where the reference puts it.
func refIndex(t testing.TB, layout pcm.Layout) []int {
	t.Helper()
	out := make([]int, len(layout))
	for i, ch := range layout {
		out[i] = -1
		for j, r := range referenceOrder {
			if r == ch {
				out[i] = j
				break
			}
		}
		if out[i] < 0 {
			t.Fatalf("channel %v of layout %s is not one the reference emits", ch, layout)
		}
	}
	// The table gives each channel its place in the reference's full order;
	// what the reference actually writes is the channels present, in that
	// order, packed. Compacting the places to ranks is what makes a mono
	// layout's centre the first and only channel written, and the lone
	// surround of a 2/1 stream the third of three rather than the seventh.
	for i, p := range out {
		rank := 0
		for _, q := range out {
			if q < p {
				rank++
			}
		}
		out[i] = rank
	}
	return out
}

// decodeStream decodes a whole elementary stream with ac3go and returns its
// samples interleaved, quantised to 16 bits the way the reference emits them.
func decodeStream(t testing.TB, stream []byte) (samples []int32, channels int) {
	t.Helper()
	return decodeStreamFunc(t, stream, nil)
}

// decodeStreamFunc is decodeStream with a hook to configure the decoder before
// it sees the stream, for the tests that need it set to something other than
// its defaults.
func decodeStreamFunc(t testing.TB, stream []byte, configure func(*ac3.Decoder)) (samples []int32, channels int) {
	t.Helper()
	d := ac3.NewDecoder()
	// The reference's own noise is the only difference this test tolerates, so
	// this decoder does not add its own on top.
	d.SetDither(false)
	if configure != nil {
		configure(d)
	}

	var planes [][]float32
	var order []int
	for len(stream) > 0 {
		var h ac3.Header
		if err := ac3.ParseHeader(stream, &h); err != nil {
			t.Fatalf("parse: %v", err)
		}
		// The whole remaining stream, not one syncframe: a 7.1 access unit is an
		// independent substream followed by a dependent one, and the decoder
		// needs to see both to merge them. AccessUnitSize is then how far to
		// advance - one syncframe for everything else, both for 7.1.
		if err := d.DecodeFrame(stream); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if planes == nil {
			// The decoder's output layout, not the stream's: a downmixed
			// stream hands back two channels whatever it codes, and they are
			// already the pair the reference writes first, so the crossing
			// below collapses to the identity for them.
			channels = d.OutputChannels()
			order = refIndex(t, d.OutputLayout())
			planes = make([][]float32, channels)
		}
		for ch := range channels {
			planes[ch] = append(planes[ch], d.Samples(ch)...)
		}
		stream = stream[d.AccessUnitSize():]
	}
	if channels == 0 {
		t.Fatal("the stream held no frames")
	}

	samples = make([]int32, len(planes[0])*channels)
	for ch, plane := range planes {
		at := order[ch]
		for i, v := range plane {
			samples[i*channels+at] = toInt16(v)
		}
	}
	return samples, channels
}

// toInt16 quantises a sample to 16 bits, saturating rather than wrapping: the
// spec calls for saturation here (clause 6.9.4, step 6), since a decoded signal
// is the original plus coding error and can pass full scale even when the
// original did not.
func toInt16(v float32) int32 {
	s := math.Round(float64(v) * (1 << 15))
	return int32(min(max(s, math.MinInt16), math.MaxInt16))
}

// TestDecodeRealStereoAgainstReference decodes real stereo streams with both
// decoders and compares their samples.
func TestDecodeRealStereoAgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	tracks := findStereoTracks(t, o, corpus, 4)
	if len(tracks) == 0 {
		t.Skip("no stereo AC-3 track in the corpus")
	}

	for _, tr := range tracks {
		t.Run(path.Base(tr.file), func(t *testing.T) {
			stream := o.ExtractSpan(t, tr.file, tr.index, 30, 4)
			if len(stream) == 0 {
				t.Skip("nothing extracted")
			}

			want := o.DecodePCM(t, stream, 16)
			got, channels := decodeStream(t, stream)

			// Both decoders start with an empty overlap, so even the first
			// frame lines up: there is nothing to skip and no offset to find.
			res, err := Compare(got, want, Dithered)
			if err != nil {
				t.Fatalf("%d Hz, %d channels: %v", tr.rate, channels, err)
			}
			t.Logf("%d Hz: %s (bar: %s)", tr.rate, res, Dithered)
		})
	}
}

// find51Tracks returns up to maxTracks 5.1 AC-3 tracks.
func find51Tracks(t testing.TB, o *Oracle, corpus string, maxTracks int) []stereoTrack {
	t.Helper()
	var out []stereoTrack
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.AC3Tracks(t, f) {
			if tr.Channels != 6 {
				continue
			}
			out = append(out, stereoTrack{f, tr.Index, tr.SampleRate})
			if len(out) >= maxTracks {
				return out
			}
			break
		}
	}
	return out
}

// TestDecodeReal51AgainstReference is the same comparison over 5.1 streams.
//
// It adds two things the stereo case cannot reach. The centre, the surrounds
// and the LFE are decoded and placed, which means the coded channel order is
// under test rather than assumed: a decoder that read the right bits into the
// wrong channel passes every stereo comparison there is. And the LFE takes a
// path of its own through the bit allocation, with its own bandwidth and no
// dither, so it is the one channel two decoders can agree on exactly.
//
// The downmix is not involved here: the reference is asked for the stream's own
// six channels, not for a stereo fold. Folding them is phase 3's business.
func TestDecodeReal51AgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	tracks := find51Tracks(t, o, corpus, 3)
	if len(tracks) == 0 {
		t.Skip("no 5.1 AC-3 track in the corpus")
	}

	for _, tr := range tracks {
		t.Run(path.Base(tr.file), func(t *testing.T) {
			stream := o.ExtractSpan(t, tr.file, tr.index, 60, 4)
			if len(stream) == 0 {
				t.Skip("nothing extracted")
			}

			want := o.DecodePCM(t, stream, 16)
			got, channels := decodeStream(t, stream)
			if channels != 6 {
				t.Fatalf("decoded %d channels, want 6", channels)
			}

			res, err := Compare(got, want, Dithered)
			if err != nil {
				t.Fatalf("%d Hz: %v", tr.rate, err)
			}
			t.Logf("%d Hz: %s (bar: %s)", tr.rate, res, Dithered)
		})
	}
}
