package ac3

import "testing"

// The enhanced band structures cross frames. A block that states none keeps
// what is there, and "what is there" reaches into the previous frame when the
// current one has not touched it: real DDP streams carry frames that only
// couple from block 2 on, state no structure there, and expect the structure
// the previous frame used. Decoded against the default table instead, the
// band count is wrong, the coupling coordinates that follow are read a wrong
// number of times, and the whole rest of the frame shifts - the observed
// failure was "exponent 25 is outside 0..24" two blocks later, on 207 of
// 103125 real frames.
//
// These tests pin the three sides of that: the structures survive the audio
// frame field's per-frame wipe, a block 0 refreshes them to the default, and
// Reset drops them the way it drops the filter bank's carry over.

// cplStrategyBits builds the coupling strategy of a stereo block that couples
// over sub-bands [11, 15) and states no band structure.
func cplStrategyBits() []byte {
	var w bitWriter
	w.write(0, 1)  // ecplinu: standard coupling
	w.write(0, 1)  // phsflginu
	w.write(11, 4) // cplbegf
	w.write(12, 4) // cplendf: end sub-band 15
	w.write(0, 1)  // cplbndstrce: no structure stated
	return bytesWithSlack(&w)
}

// cplDecoder returns a stereo decoder mid-stream, its reader over b, with the
// audio frame field saying block blk couples.
func cplDecoder(b []byte, blk int) *Decoder {
	d := NewDecoder()
	d.h.Sync.Acmod = AcmodStereo
	d.h.Acmod = AcmodStereo
	d.nfchans = 2
	d.eac3.cplInUse[blk] = true
	d.r.Reset(b)
	return d
}

func TestEAC3CouplingBandStructCrossesFrames(t *testing.T) {
	// Sub-bands 12..14 of the default table hold false, true, true: two of the
	// four sub-bands merge and the default yields 2 bands. The structure the
	// "previous frame" left holds false everywhere, which yields 4.
	d := cplDecoder(cplStrategyBits(), 2)
	d.cplBandStruct = [maxCplSubbands]bool{}

	// What parseEAC3AudioFrame does at the top of every frame. The structure
	// has to survive it: it lives on the Decoder precisely so that this wipe
	// cannot reach it.
	d.eac3 = eac3Frame{}
	d.eac3.cplInUse[2] = true

	if err := d.readEAC3CouplingStrategy(2); err != nil {
		t.Fatal(err)
	}
	if d.ncplbnd != 4 {
		t.Errorf("ncplbnd = %d, want 4: a mid-frame strategy with no structure must inherit the previous frame's, not the default", d.ncplbnd)
	}

	// The same bits in a block 0 start from the default table: 2 bands.
	d = cplDecoder(cplStrategyBits(), 0)
	d.cplBandStruct = [maxCplSubbands]bool{}
	if err := d.readEAC3CouplingStrategy(0); err != nil {
		t.Fatal(err)
	}
	if d.ncplbnd != 2 {
		t.Errorf("ncplbnd = %d, want 2: block 0 must refresh the structure to the default", d.ncplbnd)
	}
}

func TestEAC3SpxBandStructCrossesFrames(t *testing.T) {
	// The extension covers sub-bands [3, 5). The default table holds false at
	// sub-band 4, so the default yields 2 bands; the structure the "previous
	// frame" left merges them into 1.
	spxBits := func() []byte {
		var w bitWriter
		w.write(3, 2) // chinspx: both channels extend
		w.write(0, 2) // spxstrtf: copy destination starts at the base
		w.write(1, 3) // spxbegf: begin sub-band 3
		w.write(0, 3) // spxendf: end sub-band 5
		w.write(0, 1) // spxbndstrce: no structure stated
		return bytesWithSlack(&w)
	}

	d := NewDecoder()
	d.h.Sync.Acmod = AcmodStereo
	d.h.Acmod = AcmodStereo
	d.nfchans = 2
	d.spxBandStruct = [spxMaxBands]bool{}
	d.spxBandStruct[4] = true
	d.eac3 = eac3Frame{} // the per-frame wipe, as above
	d.r.Reset(spxBits())
	if err := d.readSpxStrategy(1); err != nil {
		t.Fatal(err)
	}
	if d.nspxbnd != 1 {
		t.Errorf("nspxbnd = %d, want 1: a mid-frame strategy with no structure must inherit the previous frame's, not the default", d.nspxbnd)
	}

	d = NewDecoder()
	d.h.Sync.Acmod = AcmodStereo
	d.h.Acmod = AcmodStereo
	d.nfchans = 2
	d.spxBandStruct = [spxMaxBands]bool{}
	d.spxBandStruct[4] = true
	d.r.Reset(spxBits())
	if err := d.readSpxStrategy(0); err != nil {
		t.Fatal(err)
	}
	if d.nspxbnd != 2 {
		t.Errorf("nspxbnd = %d, want 2: block 0 must refresh the structure to the default", d.nspxbnd)
	}
}

func TestResetDropsBandStructs(t *testing.T) {
	d := NewDecoder()
	d.cplBandStruct[3] = true
	d.spxBandStruct[4] = true
	d.Reset()
	if d.cplBandStruct != ([maxCplSubbands]bool{}) || d.spxBandStruct != ([spxMaxBands]bool{}) {
		t.Error("Reset must drop the band structures: a seek decodes like the reference started cold")
	}
}
