package ac3

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestCRC16KnownVectors pins the CRC parameters themselves. The check value of
// a CRC over the polynomial 0x8005 seeded with zero, neither input nor output
// reflected, computed over the digits "123456789", is 0xfee8. If this fails,
// the polynomial or the bit order is wrong and every frame check below is
// meaningless.
func TestCRC16KnownVectors(t *testing.T) {
	tests := []struct {
		name string
		in   []byte
		want uint16
	}{
		{"standard check string", []byte("123456789"), 0xFEE8},
		{"empty", nil, 0},
		{"a single zero byte", []byte{0x00}, 0x0000},
		{"a single 0x01", []byte{0x01}, 0x8005},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := crc16(0, tt.in); got != tt.want {
				t.Errorf("crc16 = %#04x, want %#04x", got, tt.want)
			}
		})
	}
}

// TestCRC16Incremental checks that folding a message in two pieces is the same
// as folding it whole, which is what lets the frame checks run over slices.
func TestCRC16Incremental(t *testing.T) {
	msg := []byte("the quick brown fox jumps over the lazy dog")
	whole := crc16(0, msg)
	for split := range len(msg) + 1 {
		if got := crc16(crc16(0, msg[:split]), msg[split:]); got != whole {
			t.Fatalf("split at %d: %#04x, want %#04x", split, got, whole)
		}
	}
}

func TestCRCSplit(t *testing.T) {
	// The split is the 5/8 point of the frame, rounded to a 16-bit word.
	tests := []struct {
		frameSize int
		want      int
	}{
		{128, 80},
		{192, 120},
		{1792, 1120},
		{3840, 2400},
	}
	for _, tt := range tests {
		if got := crcSplit(tt.frameSize); got != tt.want {
			t.Errorf("crcSplit(%d) = %d, want %d", tt.frameSize, got, tt.want)
		}
		if got, want := crcSplit(tt.frameSize), tt.frameSize*5/8; got != want {
			t.Errorf("crcSplit(%d) = %d, want the 5/8 point %d", tt.frameSize, got, want)
		}
	}
}

func TestCheckCRC(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	split := crcSplit(len(good))

	tests := []struct {
		name string
		in   []byte
		want error
	}{
		{"a well formed frame", good, nil},
		{"more bytes than the frame is fine", append(bytes.Clone(good), 1, 2, 3), nil},
		{"empty", nil, ErrShortFrame},
		{"no syncword", append([]byte{0, 0}, good[2:]...), ErrNoSync},
		{"one byte short", good[:len(good)-1], ErrShortFrame},
		{"only the crc1 region", good[:split], ErrShortFrame},
		{"crc1 corrupted", corrupt(good, 2), ErrCRC},
		{"a byte inside the crc1 region corrupted", corrupt(good, 40), ErrCRC},
		{"the last byte of the crc1 region corrupted", corrupt(good, split-1), ErrCRC},
		{"the first byte of the crc2 region corrupted", corrupt(good, split), ErrCRC},
		{"crc2 corrupted", corrupt(good, len(good)-1), ErrCRC},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := CheckCRC(tt.in); !errors.Is(err, tt.want) {
				t.Errorf("CheckCRC = %v, want %v", err, tt.want)
			}
		})
	}
}

// TestCheckCRC1NeedsOnlyThePrefix is the point of having a separate crc1: a
// corrupt frame can be rejected before the whole of it has arrived.
func TestCheckCRC1NeedsOnlyThePrefix(t *testing.T) {
	good := synth(t, 0, 30, defaultBSI())
	split := crcSplit(len(good))

	if err := CheckCRC1(good[:split]); err != nil {
		t.Errorf("CheckCRC1 on the 5/8 prefix = %v, want nil", err)
	}
	if err := CheckCRC1(good[:split-1]); !errors.Is(err, ErrShortFrame) {
		t.Errorf("CheckCRC1 one byte short = %v, want ErrShortFrame", err)
	}
	// crc2 is not checked here, so damage past the split must go unnoticed.
	if err := CheckCRC1(corrupt(good, len(good)-1)); err != nil {
		t.Errorf("CheckCRC1 with a broken crc2 = %v, want nil", err)
	}
	if err := CheckCRC1(corrupt(good, 40)); !errors.Is(err, ErrCRC) {
		t.Errorf("CheckCRC1 with damage in its own region = %v, want ErrCRC", err)
	}
}

// TestCheckCRCDetectsEverySingleBitFlip is the property that matters: a CRC
// that misses a single flipped bit is not doing its job.
func TestCheckCRCDetectsEverySingleBitFlip(t *testing.T) {
	// The smallest frame keeps this exhaustive rather than sampled.
	good := synth(t, 0, 0, defaultBSI())
	for i := 2; i < len(good); i++ {
		for bit := range 8 {
			bad := bytes.Clone(good)
			bad[i] ^= 1 << bit
			err := CheckCRC(bad)
			if err == nil {
				t.Fatalf("byte %d bit %d flipped, CheckCRC = nil", i, bit)
			}
			if errors.Is(err, ErrCRC) {
				continue
			}
			// Byte 4 holds fscod and frmsizecod: flipping a bit there can
			// name a reserved value, or resize the frame past the buffer.
			// Both are caught before the check words and are just as fatal.
			if i == 4 && (errors.Is(err, ErrReserved) || errors.Is(err, ErrShortFrame)) {
				continue
			}
			// Byte 5 holds bsid, and bsid decides which syntax the bytes are
			// in, so a flip there is not a corrupt frame of this format but a
			// claim to be a different one. All four ways that goes are still
			// caught, just not by the check words: 8 to 9, 10 or 24 names a
			// version nothing decodes, and 8 to 12 names enhanced AC-3, which
			// measures its frame from other bits entirely and lands far off the
			// end of this one. The remaining flip names bsid 0, which is a real
			// AC-3 version, and there the CRC does catch it.
			if i == 5 && (errors.Is(err, ErrUnsupportedBSID) || errors.Is(err, ErrShortFrame)) {
				continue
			}
			t.Fatalf("byte %d bit %d flipped: %v, want ErrCRC", i, bit, err)
		}
	}
}

func TestCheckCRCOnFixtures(t *testing.T) {
	// Real encoder output: the check has to agree with what an encoder writes,
	// not only with what the tests' own writer produces.
	files, err := filepath.Glob(filepath.Join("testdata", "*.ac3"))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("no fixtures found")
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			frames := 0
			for off := 0; off < len(data); {
				var si SyncInfo
				if err := ParseSyncInfo(data[off:], &si); err != nil {
					t.Fatalf("frame %d at %d: %v", frames, off, err)
				}
				if err := CheckCRC(data[off:]); err != nil {
					t.Fatalf("frame %d at %d: %v", frames, off, err)
				}
				off += si.FrameSize
				frames++
			}
			if frames == 0 {
				t.Fatal("no frames")
			}
		})
	}
}

func TestCheckCRCNoAllocations(t *testing.T) {
	frame := synth(t, 0, 30, defaultBSI())
	got := testing.AllocsPerRun(100, func() {
		if err := CheckCRC(frame); err != nil {
			t.Fatal(err)
		}
	})
	if got != 0 {
		t.Errorf("AllocsPerRun = %v, want 0", got)
	}
}

// corrupt returns a copy of frame with byte i flipped.
func corrupt(frame []byte, i int) []byte {
	out := bytes.Clone(frame)
	out[i] ^= 0xFF
	return out
}

func BenchmarkCheckCRC(b *testing.B) {
	frame := synth(b, 0, 30, defaultBSI())
	b.SetBytes(int64(len(frame)))
	b.ReportAllocs()
	for b.Loop() {
		if err := CheckCRC(frame); err != nil {
			b.Fatal(err)
		}
	}
}
