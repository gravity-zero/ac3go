package e2e

import (
	"path"
	"testing"
)

// TestDecode71AgainstReference holds the 7.1 path to the reference on real
// streams. There is no committed fixture for it and there cannot be: 7.1 comes
// only from a dependent substream, which no encoder within reach emits, so the
// only 7.1 content is the corpus. The test therefore runs against whatever 7.1
// E-AC-3 tracks the local corpus holds and skips when there are none - like the
// spectral-extension and adaptive-hybrid-transform checks before it.
//
// What it proves that nothing committed can: an access unit is two syncframes,
// an independent substream carrying 5.1 and a dependent one carrying the two
// side and two back channels, merged into eight. The back and side channels
// come from the second substream entirely, and a wrong merge would put silence
// or the wrong signal in four of the eight outputs - which the per-channel
// comparison below would catch at once.
//
// Only the standard extension is decoded to 7.1: a 3/2+LFE core plus a
// dependent stating the four side and back channels, which is what the mass of
// 7.1 streams carry. The core may be AC-3 or E-AC-3 - both merge. A stream
// built some other way (an unusual channel map, a core that is not 3/2+LFE)
// decodes to its core rather than to a guessed layout, and this test records
// that it was seen without failing over it - but it does require that at least
// one track decoded to a verified 7.1, so a corpus that has 7.1 cannot pass by
// skipping all of it.
func TestDecode71AgainstReference(t *testing.T) {
	o := Setup(t)
	corpus := o.Corpus(t)

	var found []stereoTrack
	for _, f := range findMedia(t, o, corpus) {
		for _, tr := range o.EAC3Tracks(t, f) {
			if tr.Channels == 8 {
				found = append(found, stereoTrack{f, tr.Index, tr.SampleRate})
			}
		}
		if len(found) >= 4 {
			break
		}
	}
	if len(found) == 0 {
		t.Skip("no 7.1 E-AC-3 track in the corpus")
	}

	verified := 0
	for _, tr := range found {
		t.Run(path.Base(tr.file), func(t *testing.T) {
			stream := o.ExtractSpanEAC3(t, tr.file, tr.index, 60, 8)
			if len(stream) == 0 {
				t.Skip("nothing extracted")
			}

			got, channels := decodeStream(t, stream)
			if channels != 8 {
				t.Skipf("decoded %d channels, not the standard 7.1 extension - decoded as its core", channels)
			}

			want := o.DecodePCM(t, stream, 16)
			if len(got) != len(want) {
				t.Fatalf("sample counts differ: got %d, reference %d", len(got), len(want))
			}
			res, err := Compare(got, want, Dithered)
			if err != nil {
				t.Fatalf("%v", err)
			}
			verified++
			t.Logf("7.1: %s", res)
		})
	}
	if verified == 0 {
		t.Fatal("the corpus has 7.1 tracks but none decoded to a verifiable 7.1")
	}
}
