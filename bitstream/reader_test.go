package bitstream

import (
	"errors"
	"math"
	"math/rand"
	"testing"
)

func TestReaderUint32(t *testing.T) {
	// 0xB7 0x2C 0xF0 0x0F = 1011 0111 0010 1100 1111 0000 0000 1111
	buf := []byte{0xB7, 0x2C, 0xF0, 0x0F}

	tests := []struct {
		name   string
		widths []uint
		want   []uint32
	}{
		{"single bits", []uint{1, 1, 1, 1, 1, 1, 1, 1}, []uint32{1, 0, 1, 1, 0, 1, 1, 1}},
		{"nibbles", []uint{4, 4, 4, 4}, []uint32{0xB, 0x7, 0x2, 0xC}},
		{"bytes", []uint{8, 8, 8, 8}, []uint32{0xB7, 0x2C, 0xF0, 0x0F}},
		{"straddling one byte", []uint{4, 8, 4}, []uint32{0xB, 0x72, 0xC}},
		{"straddling three bytes", []uint{3, 17, 4}, []uint32{0x5, 0x172CF, 0x0}},
		{"whole buffer", []uint{32}, []uint32{0xB72CF00F}},
		{"zero width consumes nothing", []uint{0, 8}, []uint32{0, 0xB7}},
		{"unaligned 32 after 1", []uint{1, 31}, []uint32{1, 0x372CF00F}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader(buf)
			for i, w := range tt.widths {
				got := r.Uint32(w)
				if got != tt.want[i] {
					t.Errorf("read %d: Uint32(%d) = %#x, want %#x", i, w, got, tt.want[i])
				}
			}
			if err := r.Err(); err != nil {
				t.Errorf("Err() = %v, want nil", err)
			}
		})
	}
}

func TestReaderBool(t *testing.T) {
	r := NewReader([]byte{0xA0})
	want := []bool{true, false, true, false, false, false, false, false}
	for i, w := range want {
		if got := r.Bool(); got != w {
			t.Errorf("bit %d = %v, want %v", i, got, w)
		}
	}
	if r.Bool() {
		t.Error("Bool past end returned true, want false")
	}
	if !errors.Is(r.Err(), ErrOverrun) {
		t.Errorf("Err() = %v, want ErrOverrun", r.Err())
	}
}

func TestReaderOverrun(t *testing.T) {
	tests := []struct {
		name string
		read func(*Reader)
		want error
	}{
		{"one bit too many", func(r *Reader) { r.Uint32(17) }, ErrOverrun},
		{"exact fit then one more", func(r *Reader) { r.Uint32(16); r.Bool() }, ErrOverrun},
		{"skip past end", func(r *Reader) { r.Skip(17) }, ErrOverrun},
		{"negative skip", func(r *Reader) { r.Skip(-1) }, ErrOverrun},
		{"the most negative skip", func(r *Reader) { r.Skip(math.MinInt) }, ErrOverrun},
		// A skip whose length is large enough that pos+n wraps round to a
		// negative position. The guard has to reject it on the way in: once the
		// position is negative every later read indexes outside the buffer.
		{"skip that overflows int", func(r *Reader) { r.Skip(math.MaxInt) }, ErrOverrun},
		{"skip that overflows int after a first skip", func(r *Reader) { r.Skip(1); r.Skip(math.MaxInt) }, ErrOverrun},
		{"width above 32", func(r *Reader) { r.Uint32(33) }, ErrWidth},
		{"seek past end", func(r *Reader) { r.SeekBit(17) }, ErrOverrun},
		{"seek negative", func(r *Reader) { r.SeekBit(-1) }, ErrOverrun},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader([]byte{0xFF, 0xFF})
			tt.read(r)
			if !errors.Is(r.Err(), tt.want) {
				t.Fatalf("Err() = %v, want %v", r.Err(), tt.want)
			}
			// The error is sticky and later reads yield zero, not stale data.
			if got := r.Uint32(1); got != 0 {
				t.Errorf("Uint32 after error = %d, want 0", got)
			}
			if r.Bool() {
				t.Error("Bool after error = true, want false")
			}
			if got := r.BitsRemaining(); got != 0 {
				t.Errorf("BitsRemaining after error = %d, want 0", got)
			}
		})
	}
}

// TestReaderSkipBounds pins the exact edge of Skip: the last skip that fits is
// accepted, the first one that does not is refused, and neither is decided by
// arithmetic that can wrap.
func TestReaderSkipBounds(t *testing.T) {
	const bits = 4 * 8

	tests := []struct {
		name    string
		pre     int // bits skipped first
		n       int
		wantErr error
		wantPos int
	}{
		{"nothing", 0, 0, nil, 0},
		{"every bit of the buffer", 0, bits, nil, bits},
		{"one bit past the buffer", 0, bits + 1, ErrOverrun, bits},
		{"the rest of the buffer", 1, bits - 1, nil, bits},
		{"one bit past the rest", 1, bits, ErrOverrun, bits},
		{"a skip at the very end", bits, 0, nil, bits},
		{"one bit at the very end", bits, 1, ErrOverrun, bits},
		{"negative", 1, -1, ErrOverrun, bits},
		{"MinInt", 1, math.MinInt, ErrOverrun, bits},
		{"MaxInt", 1, math.MaxInt, ErrOverrun, bits},
		{"MaxInt from the start", 0, math.MaxInt, ErrOverrun, bits},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader([]byte{0xAA, 0xBB, 0xCC, 0xDD})
			r.Skip(tt.pre)
			r.Skip(tt.n)
			if !errors.Is(r.Err(), tt.wantErr) {
				t.Fatalf("Err() = %v, want %v", r.Err(), tt.wantErr)
			}
			// A rejected skip parks the reader at the end of the buffer, never
			// outside it: BitPos is what the next read indexes with.
			if got := r.BitPos(); got != tt.wantPos {
				t.Errorf("BitPos = %d, want %d", got, tt.wantPos)
			}
			// The read that follows must not panic, which is what a wrapped
			// position used to cost. At the end of the buffer it has to report
			// the overrun rather than return whatever byte it landed on.
			got := r.Uint32(1)
			if tt.wantPos == bits {
				if got != 0 {
					t.Errorf("Uint32 at the end = %d, want 0", got)
				}
				if !errors.Is(r.Err(), ErrOverrun) {
					t.Errorf("Err() after reading at the end = %v, want ErrOverrun", r.Err())
				}
			}
		})
	}
}

// TestReaderSkipOverflowIsSticky is the regression proper: the skip whose
// length wraps pos+n negative used to leave Err nil and the position at
// MinInt, so the reader reported success and the next read panicked with an
// index far outside the buffer.
func TestReaderSkipOverflowIsSticky(t *testing.T) {
	r := NewReader([]byte{0xAA, 0xBB, 0xCC, 0xDD})
	r.Skip(1)
	r.Skip(math.MaxInt)
	if !errors.Is(r.Err(), ErrOverrun) {
		t.Fatalf("Err() = %v, want ErrOverrun", r.Err())
	}
	if got := r.BitPos(); got < 0 || got > 32 {
		t.Fatalf("BitPos = %d, outside [0, 32]", got)
	}
	if got := r.BitsRemaining(); got != 0 {
		t.Errorf("BitsRemaining = %d, want 0", got)
	}
	if got := r.Uint32(1); got != 0 {
		t.Errorf("Uint32 = %d, want 0", got)
	}
}

func TestReaderEmpty(t *testing.T) {
	var r Reader
	if got := r.Uint32(1); got != 0 {
		t.Errorf("Uint32 on zero Reader = %d, want 0", got)
	}
	if !errors.Is(r.Err(), ErrOverrun) {
		t.Errorf("Err() = %v, want ErrOverrun", r.Err())
	}
	if got := r.BitsRemaining(); got != 0 {
		t.Errorf("BitsRemaining = %d, want 0", got)
	}
}

func TestReaderAlign(t *testing.T) {
	tests := []struct {
		name    string
		skip    int
		wantPos int
	}{
		{"already aligned", 0, 0},
		{"one bit in", 1, 8},
		{"seven bits in", 7, 8},
		{"one byte in", 8, 8},
		{"nine bits in", 9, 16},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := NewReader([]byte{0xFF, 0xFF, 0xFF})
			r.Skip(tt.skip)
			r.Align()
			if got := r.BitPos(); got != tt.wantPos {
				t.Errorf("BitPos after Align = %d, want %d", got, tt.wantPos)
			}
			if err := r.Err(); err != nil {
				t.Errorf("Err() = %v, want nil", err)
			}
		})
	}
}

func TestReaderPositionAccounting(t *testing.T) {
	buf := make([]byte, 8)
	r := NewReader(buf)
	if got := r.BitsRemaining(); got != 64 {
		t.Fatalf("BitsRemaining = %d, want 64", got)
	}
	r.Uint32(5)
	if got, want := r.BitPos(), 5; got != want {
		t.Errorf("BitPos = %d, want %d", got, want)
	}
	if got, want := r.BitsRemaining(), 59; got != want {
		t.Errorf("BitsRemaining = %d, want %d", got, want)
	}
	r.SeekBit(0)
	if got := r.BitPos(); got != 0 {
		t.Errorf("BitPos after SeekBit(0) = %d, want 0", got)
	}
}

func TestReaderResetClearsError(t *testing.T) {
	r := NewReader([]byte{0x01})
	r.Uint32(9)
	if r.Err() == nil {
		t.Fatal("expected an error before Reset")
	}
	r.Reset([]byte{0xAB})
	if err := r.Err(); err != nil {
		t.Fatalf("Err after Reset = %v, want nil", err)
	}
	if got := r.Uint32(8); got != 0xAB {
		t.Errorf("Uint32 after Reset = %#x, want 0xab", got)
	}
}

// TestReaderMatchesReference cross-checks the reader against a deliberately
// naive bit-at-a-time implementation over random buffers and random widths.
func TestReaderMatchesReference(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	buf := make([]byte, 64)
	for iter := 0; iter < 2000; iter++ {
		rng.Read(buf)
		r := NewReader(buf)
		pos := 0
		for {
			n := uint(rng.Intn(33))
			if pos+int(n) > len(buf)*8 {
				break
			}
			want := refRead(buf, pos, n)
			got := r.Uint32(n)
			if got != want {
				t.Fatalf("iter %d: Uint32(%d) at bit %d = %#x, want %#x", iter, n, pos, got, want)
			}
			pos += int(n)
		}
		if err := r.Err(); err != nil {
			t.Fatalf("iter %d: Err() = %v, want nil", iter, err)
		}
	}
}

// refRead is the obvious, slow definition of an MSB-first read.
func refRead(buf []byte, pos int, n uint) uint32 {
	var v uint32
	for i := 0; i < int(n); i++ {
		p := pos + i
		bit := buf[p>>3] >> (7 - uint(p&7)) & 1
		v = v<<1 | uint32(bit)
	}
	return v
}

func TestReaderNoAllocations(t *testing.T) {
	buf := make([]byte, 256)
	var r Reader
	got := testing.AllocsPerRun(100, func() {
		r.Reset(buf)
		for r.BitsRemaining() >= 7 {
			r.Uint32(3)
			r.Bool()
			r.Uint32(3)
		}
	})
	if got != 0 {
		t.Errorf("AllocsPerRun = %v, want 0", got)
	}
}

// FuzzReader drives the reader with arbitrary widths and arbitrary skip and
// seek lengths. skip and seek are full ints rather than a small range on
// purpose: a bit stream parser derives its skip lengths from length fields it
// read out of the stream, so those arguments are as adversarial as the buffer,
// and it is exactly a skip too large to add to the position that used to wrap
// the reader past the end of the buffer.
func FuzzReader(f *testing.F) {
	f.Add([]byte{0x0B, 0x77, 0x00}, uint8(5), 3, 0)
	f.Add([]byte{}, uint8(0), 0, 0)
	f.Add([]byte{0xFF}, uint8(33), 8, 8)
	f.Add([]byte{0xAA, 0xBB, 0xCC, 0xDD}, uint8(1), math.MaxInt, math.MaxInt)
	f.Add([]byte{0xAA, 0xBB, 0xCC, 0xDD}, uint8(1), math.MinInt, math.MinInt)
	f.Add([]byte{0xAA, 0xBB, 0xCC, 0xDD}, uint8(1), math.MaxInt-1, -1)

	// The reader must never panic and must never report a position outside the
	// buffer, whatever the input, the requested widths and the skip lengths.
	f.Fuzz(func(t *testing.T, buf []byte, seed uint8, skip, seek int) {
		r := NewReader(buf)
		widths := []uint{uint(seed % 33), uint(seed>>2) % 33, 1, 8, 32, 0}
		for i := 0; i < 64; i++ {
			switch i % 6 {
			case 0:
				r.Uint32(widths[i%len(widths)])
			case 1:
				r.Bool()
			case 2:
				r.Skip(int(seed % 9))
			case 3:
				r.Align()
			case 4:
				r.Skip(skip)
			case 5:
				// SeekBit clears the latched error, which puts the reader back
				// in play and lets the rest of the loop keep probing it.
				r.SeekBit(seek)
			}
			if p := r.BitPos(); p < 0 || p > len(buf)*8 {
				t.Fatalf("BitPos = %d, outside [0, %d]", p, len(buf)*8)
			}
			if r.BitsRemaining() < 0 {
				t.Fatalf("BitsRemaining = %d, want >= 0", r.BitsRemaining())
			}
		}
	})
}

func BenchmarkReaderUint32(b *testing.B) {
	buf := make([]byte, 1024)
	for i := range buf {
		buf[i] = byte(i)
	}
	var r Reader
	b.ReportAllocs()
	for b.Loop() {
		r.Reset(buf)
		for r.BitsRemaining() >= 5 {
			r.Uint32(5)
		}
	}
}

// TestResetRefusesUnaddressableBuffer exercises the one guard in this package
// that a 64-bit build cannot reach: a buffer whose length in bits does not fit
// an int. On 32-bit that stands at 256 MiB, which is allocatable, so the test
// runs there - `GOARCH=386 go test ./bitstream` - and skips on 64-bit rather
// than pretending to cover it.
func TestResetRefusesUnaddressableBuffer(t *testing.T) {
	if uint64(maxBufLen) > 1<<40 {
		t.Skip("64-bit: no allocatable buffer can reach maxBufLen")
	}

	// The largest addressable buffer is accepted...
	r := NewReader(make([]byte, maxBufLen))
	if r.Err() != nil {
		t.Fatalf("Reset(maxBufLen bytes) latched %v, want nil", r.Err())
	}

	// ...and one byte more is refused: the reader comes back empty with the
	// overrun latched, and stays failed rather than reading from a buffer it
	// cannot address bit by bit.
	r.Reset(make([]byte, maxBufLen+1))
	if !errors.Is(r.Err(), ErrOverrun) {
		t.Fatalf("Reset(maxBufLen+1 bytes) latched %v, want ErrOverrun", r.Err())
	}
	if got := r.Uint32(8); got != 0 {
		t.Errorf("Uint32 after the refusal = %d, want 0", got)
	}
	if !errors.Is(r.Err(), ErrOverrun) {
		t.Errorf("the refusal did not stay latched: %v", r.Err())
	}

	// A refused reader recovers on the next valid Reset.
	r.Reset([]byte{0xAB})
	if r.Err() != nil || r.Uint32(8) != 0xAB {
		t.Errorf("Reset after a refusal did not recover: err %v", r.Err())
	}
}
