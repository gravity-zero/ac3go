package ac3

import (
	"os"
	"path/filepath"
	"testing"
)

// TestIs71Extension pins the gate that decides whether a dependent substream is
// merged into 7.1. Only the standard extension - a 3/2+LFE core plus a
// dependent stating the two side and two back channels - qualifies; everything
// else keeps the 5.1 core, which is what a decoder that does not reach for those
// channels would do.
func TestIs71Extension(t *testing.T) {
	core := func() Header {
		var h Header
		h.Acmod = Acmod3F2R
		h.Lfeon = true
		return h
	}
	dep := func() Header {
		var h Header
		h.Chanmape = true
		h.Chanmap = eac3Chanmap71
		return h
	}

	if i, d := core(), dep(); !is71Extension(&i, &d) {
		t.Error("the standard 3/2+LFE core plus side/back dependent is not recognized as 7.1")
	}

	// A core that is not 3/2+LFE.
	if i, d := core(), dep(); func() bool { i.Lfeon = false; return is71Extension(&i, &d) }() {
		t.Error("a core without LFE was merged")
	}
	if i, d := core(), dep(); func() bool { i.Acmod = AcmodStereo; return is71Extension(&i, &d) }() {
		t.Error("a stereo core was merged")
	}
	// A dependent stating some other channel map.
	if i, d := core(), dep(); func() bool { d.Chanmap = 0x0600; return is71Extension(&i, &d) }() {
		t.Error("a dependent with an unrecognized channel map was merged")
	}
	if i, d := core(), dep(); func() bool { d.Chanmape = false; return is71Extension(&i, &d) }() {
		t.Error("a dependent stating no channel map was merged")
	}
}

// TestNormal51IsNot71 guards against the 7.1 path misfiring on an ordinary 5.1
// stream: the committed E-AC-3 5.1 fixture must decode to six channels, and the
// decoder must advance by one syncframe, not consume the frame after it as a
// dependent substream.
func TestNormal51IsNot71(t *testing.T) {
	stream, err := os.ReadFile(filepath.Join("testdata", "tones_48k_5p1_384k.eac3"))
	if err != nil {
		t.Fatal(err)
	}
	d := NewDecoder()
	var h Header
	if err := ParseHeader(stream, &h); err != nil {
		t.Fatal(err)
	}
	if err := d.DecodeFrame(stream); err != nil {
		t.Fatal(err)
	}
	if got := d.OutputChannels(); got != 6 {
		t.Errorf("OutputChannels = %d, want 6: a 5.1 stream must not become 7.1", got)
	}
	if got, want := d.AccessUnitSize(), h.Sync.FrameSize; got != want {
		t.Errorf("AccessUnitSize = %d, want %d: no dependent substream should have been consumed", got, want)
	}
}
