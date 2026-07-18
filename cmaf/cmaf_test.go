package cmaf

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/ac3"
)

// The fixtures are our own AC-3 / E-AC-3 tone fixtures wrapped into a fragmented
// MP4 (two moof/mdat fragments each). The elementary stream pulled back out has
// to be exactly the syncframes that went in - byte for byte - which is the one
// thing this package promises and the one thing that would break silently if a
// box offset were off.
var cases = []struct {
	fmp4       string
	elementary string
	codec      Codec
	wantFrames int
	sampleRate int
	channels   int
}{
	{"tone_48k_5p1_448k.ac3.mp4", "tone_48k_5p1_448k.ac3", CodecAC3, 8, 48000, 6},
	{"tones_48k_5p1_384k.eac3.mp4", "tones_48k_5p1_384k.eac3", CodecEAC3, 32, 48000, 6},
}

func TestElementaryRoundTrip(t *testing.T) {
	for _, c := range cases {
		t.Run(c.fmp4, func(t *testing.T) {
			fmp4 := read(t, filepath.Join("testdata", c.fmp4))
			want := read(t, filepath.Join("..", "ac3", "testdata", c.elementary))

			codec, got, err := Elementary(fmp4)
			if err != nil {
				t.Fatal(err)
			}
			if codec != c.codec {
				t.Errorf("codec = %q, want %q", codec, c.codec)
			}
			if !bytes.Equal(got, want) {
				t.Fatalf("extracted %d bytes, the source elementary stream is %d; they differ",
					len(got), len(want))
			}

			// And it decodes: prove the bytes are real frames, not a lucky
			// length match.
			frames := decodeCount(t, got, c.sampleRate, c.channels)
			if frames != c.wantFrames {
				t.Errorf("decoded %d frames, want %d", frames, c.wantFrames)
			}
		})
	}
}

// TestInitThenSegments splits a fragmented file into its initialization boxes
// and each media fragment, and feeds them the way a player would: Init once,
// then Segment per fragment. The concatenation must equal the one-shot form.
func TestInitThenSegments(t *testing.T) {
	fmp4 := read(t, filepath.Join("testdata", "tone_48k_5p1_448k.ac3.mp4"))
	initSeg, segments := split(t, fmp4)

	d := NewDemuxer()
	codec, err := d.Init(initSeg)
	if err != nil {
		t.Fatal(err)
	}
	if codec != CodecAC3 {
		t.Fatalf("codec = %q, want ac-3", codec)
	}
	if len(segments) < 2 {
		t.Fatalf("expected at least two fragments, got %d", len(segments))
	}

	var got []byte
	for _, seg := range segments {
		frames, err := d.Segment(seg)
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, frames...)
	}

	_, want, err := Elementary(fmp4)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("init+segments gave %d bytes, one-shot gave %d", len(got), len(want))
	}
}

func TestNoAudioTrack(t *testing.T) {
	// A moov with no audio track (an empty box body) is the "nothing to decode"
	// case, and it has to be reported rather than returning empty success.
	if _, err := NewDemuxer().Init([]byte{0, 0, 0, 8, 'm', 'o', 'o', 'v'}); err == nil {
		t.Error("Init on a trackless moov returned no error")
	}
	if _, _, err := Elementary([]byte{0, 0, 0, 8, 'f', 't', 'y', 'p'}); err == nil {
		t.Error("Elementary on a file with no fragments returned no error")
	}
}

// split cuts a fragmented file into the initialization segment (everything up to
// the first moof) and one media segment per moof+mdat pair.
func split(t *testing.T, fmp4 []byte) (initSeg []byte, segments [][]byte) {
	t.Helper()
	firstMoof := -1
	var bounds []int
	_ = walk(fmp4, func(b box) error {
		if b.typ == "moof" {
			if firstMoof < 0 {
				firstMoof = b.off
			}
			bounds = append(bounds, b.off)
		}
		return nil
	})
	if firstMoof < 0 {
		t.Fatal("no moof in fixture")
	}
	initSeg = fmp4[:firstMoof]
	bounds = append(bounds, len(fmp4))
	// Trim a trailing mfra (random-access index) off the last segment: it is not
	// media and sits after the final mdat.
	for i := 0; i < len(bounds)-1; i++ {
		segments = append(segments, fmp4[bounds[i]:bounds[i+1]])
	}
	return initSeg, segments
}

func decodeCount(t *testing.T, elementary []byte, wantRate, wantCh int) int {
	t.Helper()
	fr := ac3.NewFrameReader(bytes.NewReader(elementary))
	d := ac3.NewDecoder()
	n := 0
	for {
		frame, err := fr.Next()
		if err != nil {
			break
		}
		if err := d.DecodeFrame(frame); err != nil {
			t.Fatalf("frame %d: %v", n, err)
		}
		if r := d.Header().Sync.SampleRate; r != wantRate {
			t.Errorf("frame %d: sample rate %d, want %d", n, r, wantRate)
		}
		if ch := d.OutputChannels(); ch != wantCh {
			t.Errorf("frame %d: %d channels, want %d", n, ch, wantCh)
		}
		n++
	}
	return n
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}
