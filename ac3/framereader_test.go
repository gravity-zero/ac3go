package ac3

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// stream concatenates frames into one elementary stream.
func stream(frames ...[]byte) []byte {
	var out []byte
	for _, f := range frames {
		out = append(out, f...)
	}
	return out
}

// drain reads every frame and returns their sizes.
func drain(t *testing.T, fr *FrameReader) []int {
	t.Helper()
	var sizes []int
	for {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			return sizes
		}
		if err != nil {
			t.Fatalf("frame %d: %v", len(sizes), err)
		}
		sizes = append(sizes, len(frame))
	}
}

func TestFrameReader(t *testing.T) {
	small := synth(t, 0, 0, defaultBSI())    // 128 bytes
	big := synth(t, 0, 30, defaultBSI())     // 1792 bytes
	biggest := synth(t, 2, 37, defaultBSI()) // 3840 bytes, the largest a frame gets
	fivePointOne := synth(t, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.acmod, s.lfeon = Acmod3F2R, true
		return s
	}())

	tests := []struct {
		name        string
		in          []byte
		wantSizes   []int
		wantSkipped int64
	}{
		{"empty stream", nil, nil, 0},
		{"one frame", small, []int{128}, 0},
		{"two frames", stream(small, small), []int{128, 128}, 0},
		{"mixed frame sizes", stream(small, big, small), []int{128, 1792, 128}, 0},
		{"the largest frame", biggest, []int{3840}, 0},
		{"changing channel layout mid stream", stream(big, fivePointOne), []int{1792, 1792}, 0},
		{"leading garbage is skipped", stream(bytes.Repeat([]byte{0xAA}, 100), small), []int{128}, 100},
		{
			name:        "a syncword in the garbage does not fool it",
			in:          stream([]byte{0x0B, 0x77, 0x00, 0x00, 0x00, 0xFF}, small),
			wantSizes:   []int{128},
			wantSkipped: 6,
		},
		{"trailing garbage is skipped, not an error", stream(small, bytes.Repeat([]byte{0xAA}, 50)), []int{128}, 50},
		{"a truncated final frame is skipped", stream(small, big[:100]), []int{128}, 100},
		{"a stream that is only garbage", bytes.Repeat([]byte{0xAA}, 500), nil, 500},
		{"a stream shorter than syncinfo", []byte{0x0B, 0x77}, nil, 2},
		{
			name:        "a damaged frame is dropped and the reader resyncs",
			in:          stream(small, corrupt(big, 200), small),
			wantSizes:   []int{128, 128},
			wantSkipped: 1792,
		},
		{
			name: "a frame whose header is destroyed is skipped",
			in: stream(small, func() []byte {
				b := bytes.Clone(big)
				b[0], b[1] = 0xFF, 0xFF // no syncword any more
				return b
			}(), small),
			wantSizes:   []int{128, 128},
			wantSkipped: 1792,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := NewFrameReader(bytes.NewReader(tt.in))
			sizes := drain(t, fr)
			if len(sizes) != len(tt.wantSizes) {
				t.Fatalf("read %d frames %v, want %d %v", len(sizes), sizes, len(tt.wantSizes), tt.wantSizes)
			}
			for i := range sizes {
				if sizes[i] != tt.wantSizes[i] {
					t.Errorf("frame %d is %d bytes, want %d", i, sizes[i], tt.wantSizes[i])
				}
			}
			if got := fr.Frames(); got != int64(len(tt.wantSizes)) {
				t.Errorf("Frames() = %d, want %d", got, len(tt.wantSizes))
			}
			if got := fr.Skipped(); got != tt.wantSkipped {
				t.Errorf("Skipped() = %d, want %d", got, tt.wantSkipped)
			}
			// Every byte is accounted for: in a frame or skipped.
			var inFrames int64
			for _, s := range sizes {
				inFrames += int64(s)
			}
			if got, want := inFrames+fr.Skipped(), int64(len(tt.in)); got != want {
				t.Errorf("%d bytes in frames plus %d skipped = %d, want %d", inFrames, fr.Skipped(), got, want)
			}
		})
	}
}

func TestFrameReaderReturnsWholeFrames(t *testing.T) {
	frames := [][]byte{
		synth(t, 0, 0, defaultBSI()),
		synth(t, 0, 30, defaultBSI()),
		synth(t, 1, 21, defaultBSI()),
	}
	fr := NewFrameReader(bytes.NewReader(stream(frames...)))
	for i, want := range frames {
		got, err := fr.Next()
		if err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("frame %d differs from the frame that was written", i)
		}
	}
	if _, err := fr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("after the last frame: %v, want io.EOF", err)
	}
	// EOF is stable: calling again must not resurrect anything.
	if _, err := fr.Next(); !errors.Is(err, io.EOF) {
		t.Errorf("second call after EOF: %v, want io.EOF", err)
	}
}

func TestFrameReaderHeader(t *testing.T) {
	mono := synth(t, 0, 30, func() bsiSpec { s := defaultBSI(); s.acmod = AcmodMono; return s }())
	surround := synth(t, 0, 30, func() bsiSpec {
		s := defaultBSI()
		s.acmod, s.lfeon = Acmod3F2R, true
		return s
	}())

	fr := NewFrameReader(bytes.NewReader(stream(mono, surround)))
	if _, err := fr.Next(); err != nil {
		t.Fatal(err)
	}
	if got, want := fr.Header().Acmod, AcmodMono; got != want {
		t.Errorf("Acmod = %d, want %d", got, want)
	}
	if _, err := fr.Next(); err != nil {
		t.Fatal(err)
	}
	if got, want := fr.Header().Acmod, Acmod3F2R; got != want {
		t.Errorf("Acmod = %d, want %d", got, want)
	}
	if !fr.Header().Lfeon {
		t.Error("Lfeon = false, want true")
	}
}

// TestFrameReaderCRCNone covers what turning the check off costs: damage
// inside a frame stops being noticed, and the frame is handed over anyway.
func TestFrameReaderCRCNone(t *testing.T) {
	bad := corrupt(synth(t, 0, 30, defaultBSI()), 200)

	fr := NewFrameReader(bytes.NewReader(bad))
	fr.SetCRCMode(CRCNone)
	sizes := drain(t, fr)
	if len(sizes) != 1 || sizes[0] != 1792 {
		t.Fatalf("with the check off, read %v, want one 1792-byte frame", sizes)
	}
	if got := fr.Skipped(); got != 0 {
		t.Errorf("Skipped = %d, want 0", got)
	}
}

// TestFrameReaderCRCModeReach pins what each mode notices. crc1 covers the
// first 5/8 of a frame and crc2 the rest, so where the damage falls decides
// which check has any chance of seeing it.
func TestFrameReaderCRCModeReach(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	split := crcSplit(len(good))
	inCRC1 := corrupt(good, 200)           // before the 5/8 point
	inCRC2 := corrupt(good, len(good)-100) // after it
	if 200 >= split || len(good)-100 < split {
		t.Fatalf("the fixture no longer straddles the 5/8 point at %d", split)
	}

	tests := []struct {
		name      string
		mode      CRCMode
		in        []byte
		wantFrame bool
	}{
		{"full accepts a sound frame", CRCFull, good, true},
		{"full rejects damage under crc1", CRCFull, inCRC1, false},
		{"full rejects damage under crc2", CRCFull, inCRC2, false},
		{"crc1 accepts a sound frame", CRCFirst, good, true},
		{"crc1 rejects damage under crc1", CRCFirst, inCRC1, false},
		// The gap crc1 alone leaves, stated rather than implied.
		{"crc1 cannot see damage under crc2", CRCFirst, inCRC2, true},
		{"none accepts anything that parses", CRCNone, inCRC1, true},
		{"none accepts damage under crc2", CRCNone, inCRC2, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fr := NewFrameReader(bytes.NewReader(tt.in))
			fr.SetCRCMode(tt.mode)
			if got, want := fr.CRCMode(), tt.mode; got != want {
				t.Errorf("CRCMode() = %v, want %v", got, want)
			}
			sizes := drain(t, fr)
			if got := len(sizes) == 1; got != tt.wantFrame {
				t.Fatalf("read %v frames, want a frame: %v", sizes, tt.wantFrame)
			}
		})
	}
}

// TestFrameReaderDefaultsToFullCRC states the default in its own right: it is
// the whole difference between a frame the reader vouches for and a frame it
// merely found.
func TestFrameReaderDefaultsToFullCRC(t *testing.T) {
	if got := NewFrameReader(bytes.NewReader(nil)).CRCMode(); got != CRCFull {
		t.Errorf("CRCMode() on a fresh reader = %v, want %v", got, CRCFull)
	}
}

// TestFrameReaderRejectsASplicedFrame is the frame the crc1 gate used to
// certify. A stream that loses bytes out of the middle of a frame leaves the
// syncword, the header and the whole of crc1's region intact, so a reader that
// weighs crc1 alone accepts a frame whose tail is the head of the frame that
// followed. Two sound frames go in behind the damage; both have to come out.
func TestFrameReaderRejectsASplicedFrame(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "tone_48k_stereo_192k.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	const size = 768
	if len(data) < 3*size {
		t.Fatalf("fixture holds %d bytes, want at least three %d-byte frames", len(data), size)
	}
	f, g, h := data[0:size], data[size:2*size], data[2*size:3*size]

	// The cut has to fall after crc1's region and before the end of the frame,
	// which is the whole point: everything crc1 covers survives it.
	const cut = 600
	if split := crcSplit(size); cut <= split || cut >= size {
		t.Fatalf("a cut at %d does not sit between the 5/8 point %d and %d", cut, split, size)
	}

	in := stream(f[:cut], g, h)
	fr := NewFrameReader(bytes.NewReader(in))
	var got [][]byte
	for {
		frame, err := fr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		// Whatever the reader vouches for must survive the check a caller
		// holding the whole frame would run.
		if err := CheckCRC(frame); err != nil {
			t.Errorf("the reader returned a frame that fails CheckCRC: %v", err)
		}
		got = append(got, bytes.Clone(frame))
	}

	// The truncated F is unusable and must go; G and H are untouched and must
	// not be collateral. Resyncing by rescanning from the byte after a
	// rejected candidate, rather than by trusting the length it announced, is
	// what keeps G: a reader that skips the frame size it read out of F's
	// header steps straight over G's syncword.
	if len(got) != 2 {
		t.Fatalf("read %d frames, want 2 (G and H)", len(got))
	}
	if !bytes.Equal(got[0], g) {
		t.Error("the first frame returned is not G")
	}
	if !bytes.Equal(got[1], h) {
		t.Error("the second frame returned is not H")
	}
	if got, want := fr.Skipped(), int64(cut); got != want {
		t.Errorf("Skipped = %d, want %d: exactly the truncated frame", got, want)
	}
}

// TestFrameReaderReadsAcrossReadBoundaries feeds the stream in awkward chunks:
// a frame reader that assumes one Read yields one frame breaks here.
func TestFrameReaderReadsAcrossReadBoundaries(t *testing.T) {
	frames := [][]byte{
		synth(t, 0, 30, defaultBSI()),
		synth(t, 2, 37, defaultBSI()),
		synth(t, 0, 0, defaultBSI()),
		synth(t, 0, 30, defaultBSI()),
	}
	data := stream(frames...)
	want := []int{1792, 3840, 128, 1792}

	for _, chunk := range []int{1, 2, 3, 7, 127, 128, 1791, 1792, 4096, len(data)} {
		t.Run("chunk="+itoa(chunk), func(t *testing.T) {
			fr := NewFrameReader(&chunkReader{data: data, chunk: chunk})
			sizes := drain(t, fr)
			if len(sizes) != len(want) {
				t.Fatalf("read %v, want %v", sizes, want)
			}
			for i := range want {
				if sizes[i] != want[i] {
					t.Fatalf("frame %d is %d bytes, want %d", i, sizes[i], want[i])
				}
			}
			if got := fr.Skipped(); got != 0 {
				t.Errorf("Skipped = %d, want 0", got)
			}
		})
	}
}

// TestFrameReaderZeroLengthReads covers a Reader that legally returns (0, nil):
// the reader must not spin on it forever.
func TestFrameReaderZeroLengthReads(t *testing.T) {
	data := synth(t, 0, 0, defaultBSI())
	fr := NewFrameReader(&stallReader{data: data})
	sizes := drain(t, fr)
	if len(sizes) != 1 || sizes[0] != 128 {
		t.Fatalf("read %v, want one 128-byte frame", sizes)
	}
}

func TestFrameReaderPropagatesReadErrors(t *testing.T) {
	want := errors.New("device on fire")
	fr := NewFrameReader(&errReader{err: want})
	if _, err := fr.Next(); !errors.Is(err, want) {
		t.Errorf("Next = %v, want %v wrapped", err, want)
	}
}

func TestFrameReaderNoAllocationsPerFrame(t *testing.T) {
	data := stream(bytes.Repeat([]byte{0}, 0))
	frame := synth(t, 0, 30, defaultBSI())
	for range 64 {
		data = append(data, frame...)
	}
	r := bytes.NewReader(data)
	fr := NewFrameReader(r)

	// The first pass grows the buffer; every pass after it must reuse it.
	if _, err := fr.Next(); err != nil {
		t.Fatal(err)
	}
	got := testing.AllocsPerRun(50, func() {
		if _, err := fr.Next(); errors.Is(err, io.EOF) {
			r.Reset(data)
			fr = NewFrameReader(r)
			if _, err := fr.Next(); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	})
	// A pass that hits EOF rebuilds the reader, so allow that one; the steady
	// state is what matters and it is well under one allocation per frame.
	if got > 1 {
		t.Errorf("AllocsPerRun = %v, want at most 1", got)
	}
}

func TestSwapBytes(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want []byte
	}{
		{"a syncword", []byte{0x77, 0x0B}, []byte{0x0B, 0x77}},
		{"empty", []byte{}, []byte{}},
		{"a lone byte is left alone", []byte{0xAB}, []byte{0xAB}},
		{"an odd tail is left alone", []byte{0x77, 0x0B, 0xAB}, []byte{0x0B, 0x77, 0xAB}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := bytes.Clone(tt.in)
			SwapBytes(got)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("SwapBytes = %x, want %x", got, tt.want)
			}
		})
	}
}

// TestSwapBytesRecoversAByteSwappedStream is the whole point of the ErrByteOrder
// signal: it tells a caller what to do rather than just refusing.
func TestSwapBytesRecoversAByteSwappedStream(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	swapped := bytes.Clone(good)
	SwapBytes(swapped)

	var si SyncInfo
	if err := ParseSyncInfo(swapped, &si); !errors.Is(err, ErrByteOrder) {
		t.Fatalf("ParseSyncInfo on a swapped stream = %v, want ErrByteOrder", err)
	}
	SwapBytes(swapped)
	if !bytes.Equal(swapped, good) {
		t.Fatal("swapping twice did not restore the stream")
	}
	if err := CheckCRC(swapped); err != nil {
		t.Fatalf("CheckCRC after the swap: %v", err)
	}
}

// TestFrameReaderOnFixtures walks the committed streams end to end.
func TestFrameReaderOnFixtures(t *testing.T) {
	files, err := filepath.Glob(filepath.Join("testdata", "*.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			// A chunked reader, because a real stream arrives in pieces.
			fr := NewFrameReader(&chunkReader{data: data, chunk: 97})
			var total int64
			for {
				frame, err := fr.Next()
				if errors.Is(err, io.EOF) {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
				total += int64(len(frame))
			}
			if got, want := total, int64(len(data)); got != want {
				t.Errorf("%d bytes in frames, want the whole %d-byte stream", got, want)
			}
			if got := fr.Skipped(); got != 0 {
				t.Errorf("Skipped = %d, want 0", got)
			}
		})
	}
}

// chunkReader hands out at most chunk bytes per Read.
type chunkReader struct {
	data  []byte
	chunk int
	pos   int
}

func (r *chunkReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := min(len(p), r.chunk, len(r.data)-r.pos)
	copy(p, r.data[r.pos:r.pos+n])
	r.pos += n
	return n, nil
}

// stallReader returns (0, nil) between every byte, which io.Reader allows.
type stallReader struct {
	data  []byte
	pos   int
	stall bool
}

func (r *stallReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	r.stall = !r.stall
	if r.stall || len(p) == 0 {
		return 0, nil
	}
	p[0] = r.data[r.pos]
	r.pos++
	return 1, nil
}

type errReader struct{ err error }

func (r *errReader) Read([]byte) (int, error) { return 0, r.err }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func BenchmarkFrameReader(b *testing.B) {
	frame := synth(b, 0, 30, defaultBSI())
	var data []byte
	for range 256 {
		data = append(data, frame...)
	}
	r := bytes.NewReader(data)
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(data)
		fr := NewFrameReader(r)
		for {
			if _, err := fr.Next(); errors.Is(err, io.EOF) {
				break
			} else if err != nil {
				b.Fatal(err)
			}
		}
	}
}

// TestHeaderIsNeverARejectedCandidate pins that a caller reading Header sees a
// frame's header or nothing, never a candidate that was thrown away.
//
// Finding frames means parsing headers out of things that turn out not to be
// frames: a syncword that fell inside audio data by chance, a frame whose check
// word disagrees. Those parses produce plausible fields - a sample rate, a
// bit rate, an acmod - and the caller that asks after the stream has ended is
// the one that would read them. That is not a corner: summarising a stream is
// exactly "read to EOF, then report", which is what cmd/ac3info does.
func TestHeaderIsNeverARejectedCandidate(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI()) // 48 kHz
	fixCRC(t, good)

	// A stream of one good frame, then a syncword whose header parses to
	// something else entirely and whose frame never arrives.
	junk := []byte{0x0B, 0x77, 0xFF, 0xFF, 0x80, 8 << 3, 0x00, 0x00} // fscod 2: 32 kHz
	stream := append(bytes.Clone(good), junk...)

	fr := NewFrameReader(bytes.NewReader(stream))
	if _, err := fr.Next(); err != nil {
		t.Fatalf("first frame: %v", err)
	}
	rate := fr.Header().Sync.SampleRate
	if rate != 48000 {
		t.Fatalf("the good frame reports %d Hz, want 48000", rate)
	}

	// Drain the stream. The junk is not a frame, so this ends it.
	for {
		if _, err := fr.Next(); err != nil {
			break
		}
	}
	if got := fr.Header().Sync.SampleRate; got != rate {
		t.Errorf("after the stream ended, Header reports %d Hz: that came from bytes "+
			"that were never a frame, not from the last frame's %d Hz", got, rate)
	}
}

// TestByteSwappedStreamSaysSo pins that a stream written as 16-bit
// little-endian words is reported rather than ground through.
//
// Every frame of such a stream is swapped, so a reader that resynced past the
// first one would resync past all of them and report an empty stream: the one
// answer that tells the caller nothing about what is actually wrong, when the
// fix is one call to SwapBytes away. ParseSyncInfo has always been able to tell
// - the signal just had nowhere to go.
func TestByteSwappedStreamSaysSo(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	fixCRC(t, good)

	swapped := bytes.Clone(good)
	SwapBytes(swapped)

	fr := NewFrameReader(bytes.NewReader(swapped))
	_, err := fr.Next()
	if !errors.Is(err, ErrByteOrder) {
		t.Fatalf("a byte-swapped stream reads as %v, want ErrByteOrder", err)
	}
	// And the fix the error points at has to actually work.
	SwapBytes(swapped)
	fr = NewFrameReader(bytes.NewReader(swapped))
	if _, err := fr.Next(); err != nil {
		t.Fatalf("after SwapBytes: %v", err)
	}
}

// TestSwappedSyncwordInsideAudioIsNotAByteOrderError is the other half, and the
// reason the report above is limited to the head of the stream: 0x770B is two
// bytes like any other and turns up inside audio data. A reader that cried
// "swapped" at every one would abort a perfectly good stream.
func TestSwappedSyncwordInsideAudioIsNotAByteOrderError(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	fixCRC(t, good)

	// Two good frames with a swapped syncword's worth of bytes between them.
	stream := bytes.Clone(good)
	stream = append(stream, 0x77, 0x0B)
	second := bytes.Clone(good)
	fixCRC(t, second)
	stream = append(stream, second...)

	fr := NewFrameReader(bytes.NewReader(stream))
	var frames int
	for {
		if _, err := fr.Next(); err != nil {
			if errors.Is(err, ErrByteOrder) {
				t.Fatal("a swapped syncword inside the stream was reported as a swapped stream")
			}
			break
		}
		frames++
	}
	if frames != 2 {
		t.Errorf("read %d frames, want 2: the two bytes between them are junk to step over", frames)
	}
}

// errAfter is a Reader that hands over n bytes and then fails, the way a
// network read or a failing disk does mid-stream.
type errAfter struct {
	b    []byte
	n    int
	err  error
	read int
}

func (r *errAfter) Read(p []byte) (int, error) {
	if r.read >= r.n {
		return 0, r.err
	}
	n := copy(p, r.b[r.read:min(r.n, len(r.b))])
	r.read += n
	return n, nil
}

// TestFramesSurviveASourceThatFails pins that a source failing does not unmake
// the frames it already handed over.
//
// The frames are in the buffer, whole and check-word-verified, before the
// source is asked for anything more. Reporting the error instead of them throws
// away sound that arrived intact - up to a frame's worth of it - and does so
// precisely when the stream is already damaged and every frame counts. The
// error is not swallowed: it takes the place of the io.EOF that would otherwise
// end the stream, so a caller still learns the stream was cut rather than
// finished.
func TestFramesSurviveASourceThatFails(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	fixCRC(t, good)

	stream := bytes.Clone(good)
	stream = append(stream, good...)
	stream = append(stream, good...)

	boom := errors.New("the disk went away")
	// Hand over the first two frames whole, then fail partway through the third.
	src := &errAfter{b: stream, n: 2*len(good) + 7, err: boom}

	fr := NewFrameReader(src)
	var frames int
	var err error
	for {
		if _, err = fr.Next(); err != nil {
			break
		}
		frames++
	}

	if frames != 2 {
		t.Errorf("read %d frames before the source failed, want 2: the bytes were there", frames)
	}
	if !errors.Is(err, boom) {
		t.Errorf("the stream ended with %v, want the source's error: a cut stream must not "+
			"read as a finished one", err)
	}
}
