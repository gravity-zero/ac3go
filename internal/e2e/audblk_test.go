package e2e

import (
	"path"
	"sort"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// TestDecodeRealCorpusFillsTheFrame runs the audio block decoder over real
// media and checks where the six blocks of every frame end.
//
// This is the test that stands behind Header.AudioStartBit, which nothing
// before this could check. The check words cover a frame's bytes, not this
// module's reading of them, so a bit stream information field parsed at the
// wrong width would pass every framing test there is and still put the audio
// blocks one bit out. Nothing catches that until something decodes from that
// bit.
//
// Two things make this decisive rather than suggestive. The audio blocks are
// self-delimiting: how many bits each field takes depends on fields read
// before it, so a decode that starts one bit out derails within a few fields
// and either reads an exponent chain out of range, a coupling range that runs
// backwards, or a mantissa code no quantizer has. And the encoder spends the
// frame's bits until they run out, so a decode that starts on the right bit
// ends within a handful of bits of the frame's trailer. A wrong one does not
// land there by chance.
//
// The corpus never enters the repository: only the counts derived from it
// appear here.
func TestDecodeRealCorpusFillsTheFrame(t *testing.T) {
	o := Setup(t)
	dir := o.Corpus(t)

	sources := o.mediaFiles(t, dir, mediaFileLimit)
	if len(sources) == 0 {
		t.Skipf("no media files under %s", dir)
	}

	tested := 0
	for _, src := range sources {
		tracks := o.AC3Tracks(t, src)
		if len(tracks) == 0 {
			continue
		}
		tr := tracks[0]
		t.Run(path.Base(src), func(t *testing.T) {
			stream := o.ExtractSpan(t, src, tr.Index, corpusSpanStart, corpusSpanSeconds)
			if len(stream) == 0 {
				t.Skip("the extracted stream is empty")
			}
			decodeAndReport(t, stream)
		})
		tested++
		if tested >= 3 {
			break
		}
	}
	if tested == 0 {
		t.Skipf("no AC-3 track found under %s", dir)
	}
}

// decodeAndReport decodes every frame of a stream and reports how close the
// audio blocks come to filling it.
func decodeAndReport(t *testing.T, stream []byte) {
	t.Helper()

	d := ac3.NewDecoder()
	var slack []int
	var h ac3.Header

	for len(stream) > 0 {
		if err := ac3.ParseHeader(stream, &h); err != nil {
			t.Fatalf("frame %d: %v", len(slack), err)
		}
		if len(stream) < h.Sync.FrameSize {
			break // a span cut mid-frame
		}
		frame := stream[:h.Sync.FrameSize]
		stream = stream[h.Sync.FrameSize:]

		if err := d.DecodeFrame(frame); err != nil {
			t.Fatalf("frame %d: %v", len(slack), err)
		}
		// 18 bits: auxdatae, crcrsv and crc2, the least a frame can end with.
		slack = append(slack, len(frame)*8-18-d.BlockEndBit())
	}
	if len(slack) == 0 {
		t.Fatal("no frames in the extracted stream")
	}

	sort.Ints(slack)
	worst := slack[len(slack)-1]
	t.Logf("%d frames, %s, bsid %d, %d kbit/s: unused bits per frame min %d, median %d, max %d",
		len(slack), h.Format(), h.Sync.Bsid, h.Sync.BitRate/1000,
		slack[0], slack[len(slack)/2], worst)

	if slack[0] < 0 {
		t.Fatalf("the audio blocks overrun the frame by %d bits", -slack[0])
	}
	// One percent of the frame. Real encoders come far closer than that, most
	// of them to within a handful of bits, but an encoder is allowed to leave
	// room for auxiliary data. The bound only has to be tight enough to rule
	// out a mis-parse, which misses by a different order of magnitude or does
	// not decode at all.
	if limit := h.Sync.FrameSize*8/100 + 2048; worst > limit {
		t.Errorf("a frame left %d bits unused, more than the %d a real encoder leaves", worst, limit)
	}
}
