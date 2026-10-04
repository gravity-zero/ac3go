package ac3

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/gravity-zero/ac3go/pcm"
)

// eac3EmptyFrame writes an enhanced frame whose header is whole and whose
// audio is zeros, with a true check word. No encoder within reach emits a
// frame of fewer than six blocks, a second dependent substream or a second
// programme, so the frames that say those things are written by hand.
func eac3EmptyFrame(strmtyp, substreamid, numblkscod uint32, size int) []byte {
	var w bitWriter
	w.write(uint32(Syncword), 16)
	w.write(strmtyp, 2)
	w.write(substreamid, 3)
	w.write(uint32(size/2-1), 11) // frmsiz
	w.write(0, 2)                 // fscod: 48 kHz
	w.write(numblkscod, 2)
	w.write(2, 3)  // acmod
	w.write(0, 1)  // lfeon
	w.write(16, 5) // bsid
	w.write(31, 5) // dialnorm
	w.write(0, 1)  // compre
	if strmtyp == uint32(StrmtypDependent) {
		w.write(0, 1) // chanmape
	}
	w.write(0, 1) // mixmdate
	w.write(0, 1) // infomdate
	if strmtyp == uint32(StrmtypIndependent) && numblkscod != 3 {
		w.write(0, 1) // convsync
	}
	w.write(0, 1) // addbsie
	frame := append(w.buf, make([]byte, size-len(w.buf))...)
	binary.BigEndian.PutUint16(frame[size-2:], crc16(0, frame[2:size-2]))
	return frame
}

// fixtureFrames cuts a fixture into its syncframes.
func fixtureFrames(t testing.TB, name string) [][]byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	var out [][]byte
	var si SyncInfo
	for len(raw) > 0 {
		if err := ParseSyncInfo(raw, &si); err != nil {
			t.Fatal(err)
		}
		out = append(out, raw[:si.FrameSize])
		raw = raw[si.FrameSize:]
	}
	return out
}

// withSubstreams lays extra after every frame of a real 5.1 stream, which is
// what a programme of more than one substream looks like on the wire.
func withSubstreams(frames [][]byte, extra ...[]byte) (stream []byte, unit int) {
	for _, e := range extra {
		unit += len(e)
	}
	for _, f := range frames {
		stream = append(stream, f...)
		for _, e := range extra {
			stream = append(stream, e...)
		}
	}
	return stream, unit + len(frames[0])
}

var (
	indepSub = uint32(StrmtypIndependent)
	depSub   = uint32(StrmtypDependent)
)

// substreamCases are the ways a stream can carry more than its programme: a
// dependent substream that is not the 7.1 extension, two of them, and a second
// programme.
var substreamCases = []struct {
	name  string
	extra [][]byte
	deps  bool
}{
	{"alone", nil, false},
	{"one dependent", [][]byte{eac3EmptyFrame(depSub, 0, 3, 64)}, true},
	{"two dependents", [][]byte{eac3EmptyFrame(depSub, 0, 3, 64), eac3EmptyFrame(depSub, 1, 3, 96)}, true},
	{"a second programme", [][]byte{eac3EmptyFrame(indepSub, 1, 3, 128)}, false},
	{"a second programme and its dependent", [][]byte{
		eac3EmptyFrame(depSub, 0, 3, 64), eac3EmptyFrame(indepSub, 1, 3, 128), eac3EmptyFrame(depSub, 0, 3, 64)}, true},
	// Larger than what a reader keeps buffered, and than its buffer: the unit
	// has to be held together while more of it is read.
	{"two programmes of the largest frame", [][]byte{
		eac3EmptyFrame(indepSub, 1, 3, MaxEAC3FrameSize), eac3EmptyFrame(indepSub, 2, 3, MaxEAC3FrameSize)}, false},
}

// TestSamplesLengthFollowsTheBlockCount pins how much audio a short frame
// hands back. The planes are six blocks long whatever the frame holds, and a
// decoder that returned all of them gave a one block frame five blocks of the
// frames before it.
func TestSamplesLengthFollowsTheBlockCount(t *testing.T) {
	// Six blocks are every fixture's and are held to 1536 where those are
	// decoded; an empty frame of six does not decode, having no exponents.
	for code, blocks := range []int{1, 2, 3} {
		// The frame is stereo, so mono is the downmix that runs.
		for _, layout := range []pcm.Layout{nil, pcm.LayoutMono} {
			d := NewDecoder()
			if err := d.SetDownmix(layout); err != nil {
				t.Fatal(err)
			}
			if err := d.DecodeFrame(eac3EmptyFrame(indepSub, 0, uint32(code), 256)); err != nil {
				t.Fatalf("%d blocks: %v", blocks, err)
			}
			for ch := range d.OutputChannels() {
				if n := len(d.Samples(ch)); n != blocks*SamplesPerBlock {
					t.Errorf("%d blocks, downmix %q: Samples(%d) holds %d samples, want %d",
						blocks, layout, ch, n, blocks*SamplesPerBlock)
				}
			}
		}
	}
}

// TestDecodeFrameSpansEverySubstream pins that a decoder walking a buffer by
// AccessUnitSize lands on the next frame of the programme, whatever else the
// stream carries in between. Stepping over one dependent substream and no more
// left the walk standing on a frame it refuses, which ended the stream at its
// first access unit.
func TestDecodeFrameSpansEverySubstream(t *testing.T) {
	frames := fixtureFrames(t, "tones_48k_5p1_384k.eac3")
	for _, c := range substreamCases {
		stream, unit := withSubstreams(frames, c.extra...)
		d := NewDecoder()
		units, samples := 0, 0
		for pos := stream; len(pos) > 0; pos = pos[d.AccessUnitSize():] {
			if err := d.DecodeFrame(pos); err != nil {
				t.Errorf("%s: access unit %d: %v", c.name, units, err)
				break
			}
			if got := d.AccessUnitSize(); got != unit {
				t.Fatalf("%s: AccessUnitSize = %d, want %d", c.name, got, unit)
			}
			units++
			samples += len(d.Samples(0))
		}
		if units != len(frames) || samples != len(frames)*SamplesPerFrame {
			t.Errorf("%s: %d access units and %d samples, want %d and %d",
				c.name, units, samples, len(frames), len(frames)*SamplesPerFrame)
		}
		wantSkipped := int64(0)
		if c.deps {
			wantSkipped = int64(len(frames))
		}
		if got := d.Stats().DependentSkipped; got != wantSkipped {
			t.Errorf("%s: DependentSkipped = %d, want %d", c.name, got, wantSkipped)
		}
	}
}

// TestDecodeFrameStopsAtATruncatedSubstream pins that the walk only spans what
// is there: a dependent substream cut short belongs to no access unit.
func TestDecodeFrameStopsAtATruncatedSubstream(t *testing.T) {
	frames := fixtureFrames(t, "tones_48k_5p1_384k.eac3")
	buf := append(append([]byte{}, frames[0]...), eac3EmptyFrame(depSub, 1, 3, 64)[:40]...)
	d := NewDecoder()
	if err := d.DecodeFrame(buf); err != nil {
		t.Fatal(err)
	}
	if got := d.AccessUnitSize(); got != len(frames[0]) {
		t.Errorf("AccessUnitSize = %d, want the syncframe's %d", got, len(frames[0]))
	}
}

// TestNextAccessUnit holds the reader to handing over a programme frame with
// everything that extends it, so that the decoder has it all in view. The
// source is read a few bytes at a time: the unit has to survive the buffer
// moving under it.
func TestNextAccessUnit(t *testing.T) {
	frames := fixtureFrames(t, "tones_48k_5p1_384k.eac3")
	for _, c := range substreamCases {
		for _, chunk := range []int{7, 1000, 1 << 20} {
			stream, unit := withSubstreams(frames, c.extra...)
			fr := NewFrameReader(&chunkReader{data: stream, chunk: chunk})
			d := NewDecoder()
			units, off := 0, 0
			for {
				au, err := fr.NextAccessUnit()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatalf("%s/%d: unit %d: %v", c.name, chunk, units, err)
				}
				if !bytes.Equal(au, stream[off:off+unit]) {
					t.Fatalf("%s/%d: unit %d is %d bytes and not the stream's %d at %d",
						c.name, chunk, units, len(au), unit, off)
				}
				if h := fr.Header(); h.Acmod != Acmod3F2R || h.Sync.Substreamid != 0 {
					t.Fatalf("%s/%d: unit %d: Header is not the programme's", c.name, chunk, units)
				}
				if err := d.DecodeFrame(au); err != nil {
					t.Fatalf("%s/%d: unit %d: %v", c.name, chunk, units, err)
				}
				if d.AccessUnitSize() != len(au) {
					t.Fatalf("%s/%d: the decoder spans %d bytes of a %d byte unit",
						c.name, chunk, d.AccessUnitSize(), len(au))
				}
				off += len(au)
				units++
			}
			if units != len(frames) || fr.Skipped() != 0 {
				t.Errorf("%s/%d: %d units, %d bytes skipped, want %d and 0",
					c.name, chunk, units, fr.Skipped(), len(frames))
			}
			if want := int64(len(frames) * (1 + len(c.extra))); fr.Frames() != want {
				t.Errorf("%s/%d: Frames = %d, want %d", c.name, chunk, fr.Frames(), want)
			}
		}
	}
}

// TestNextAccessUnitDropsOrphans pins what happens to substreams with no
// programme frame before them, which is how a stream cut mid unit begins: they
// extend a frame that is not there, and are counted as skipped.
func TestNextAccessUnitDropsOrphans(t *testing.T) {
	frames := fixtureFrames(t, "tones_48k_5p1_384k.eac3")
	dep := eac3EmptyFrame(depSub, 0, 3, 64)
	stream := append(append(append([]byte{}, dep...), dep...), frames[0]...)
	stream = append(stream, dep...)

	fr := NewFrameReader(bytes.NewReader(stream))
	au, err := fr.NextAccessUnit()
	if err != nil {
		t.Fatal(err)
	}
	if want := len(frames[0]) + len(dep); len(au) != want {
		t.Errorf("unit is %d bytes, want %d", len(au), want)
	}
	if fr.Skipped() != int64(2*len(dep)) {
		t.Errorf("Skipped = %d, want %d", fr.Skipped(), 2*len(dep))
	}
	if _, err := fr.NextAccessUnit(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last unit: %v, want io.EOF", err)
	}
}

// TestNextAccessUnitOnAC3 pins that a stream of one substream reads the same
// through either method.
func TestNextAccessUnitOnAC3(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "tones_48k_5p1_448k.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	a, b := NewFrameReader(bytes.NewReader(raw)), NewFrameReader(bytes.NewReader(raw))
	for n := 0; ; n++ {
		fa, ea := a.Next()
		fb, eb := b.NextAccessUnit()
		if !bytes.Equal(fa, fb) || !errors.Is(eb, ea) && ea != eb {
			t.Fatalf("frame %d: Next and NextAccessUnit disagree (%v, %v)", n, ea, eb)
		}
		if ea != nil {
			if n == 0 {
				t.Fatal("no frame read")
			}
			break
		}
	}
}

// FuzzNextAccessUnit holds the reader's units to what they claim to be on
// arbitrary bytes: whole verified frames laid end to end, the first one the
// programme's and the rest not, found among exactly the frames Next finds.
func FuzzNextAccessUnit(f *testing.F) {
	seedCorpus(f)
	frames := fixtureFrames(f, "tones_48k_stereo_192k.eac3")[:2]
	for _, c := range substreamCases {
		stream, _ := withSubstreams(frames, c.extra...)
		f.Add(stream)
		f.Add(stream[len(frames[0]):]) // opens on the substreams of a unit that is gone
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		byFrame := NewFrameReader(bytes.NewReader(data))
		for {
			if _, err := byFrame.Next(); err != nil {
				break
			}
		}

		fr := NewFrameReader(bytes.NewReader(data))
		d := NewDecoder()
		var total int64
		var end error
		for {
			au, err := fr.NextAccessUnit()
			if err != nil {
				end = err
				break
			}
			total += int64(len(au))
			var si SyncInfo
			n := 0
			for rest := au; len(rest) > 0; rest = rest[si.FrameSize:] {
				if err := ParseSyncInfo(rest, &si); err != nil {
					t.Fatalf("frame %d of a unit does not parse: %v", n, err)
				}
				if len(rest) < si.FrameSize {
					t.Fatalf("frame %d of a unit is cut short", n)
				}
				if err := CheckCRC(rest[:si.FrameSize]); err != nil {
					t.Fatalf("frame %d of a unit: %v", n, err)
				}
				if si.AdvancesTime() != (n == 0) {
					t.Fatalf("frame %d of a unit: AdvancesTime = %v", n, si.AdvancesTime())
				}
				n++
			}
			if n > maxAccessUnitFrames {
				t.Fatalf("a unit of %d frames", n)
			}
			if d.DecodeFrame(au) == nil {
				if got := d.AccessUnitSize(); got > len(au) {
					t.Fatalf("the decoder spans %d bytes of a %d byte unit", got, len(au))
				}
				if got := len(d.Samples(0)); got != d.Header().Sync.Samples() {
					t.Fatalf("Samples holds %d, the header says %d", got, d.Header().Sync.Samples())
				}
			}
		}
		if fr.Frames() != byFrame.Frames() {
			t.Fatalf("%d frames found by unit, %d by frame", fr.Frames(), byFrame.Frames())
		}
		// A stream that opens byte-swapped is refused where it stands, with
		// nothing read: every other stream is accounted for to the byte.
		if errors.Is(end, io.EOF) && total+fr.Skipped() != int64(len(data)) {
			t.Fatalf("%d bytes in units and %d skipped of %d", total, fr.Skipped(), len(data))
		}
	})
}
