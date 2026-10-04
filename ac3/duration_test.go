package ac3

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// eac3SyncFrame writes an enhanced frame that is a syncinfo and nothing else:
// all PacketSamples reads.
func eac3SyncFrame(strmtyp, substreamid, numblkscod uint32, size int) []byte {
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
	return append(w.buf, make([]byte, size-len(w.buf))...)
}

// TestPacketSamplesFixtures holds the walk to streams an encoder wrote. The
// frame counts are the files' own: a 192 kbit/s frame is 768 bytes at 48 kHz.
func TestPacketSamplesFixtures(t *testing.T) {
	for _, c := range []struct {
		file          string
		samples, rate int
	}{
		{"tones_48k_stereo_192k.ac3", 13 * 1536, 48000},
		{"tones_44k1_stereo_192k.ac3", 12 * 1536, 44100},
		{"tones_32k_stereo_192k.ac3", 9 * 1536, 32000},
		{"tones_48k_stereo_192k.eac3", 32 * 1536, 48000},
		{"tones_48k_5p1_384k.eac3", 32 * 1536, 48000},
	} {
		raw, err := os.ReadFile(filepath.Join("testdata", c.file))
		if err != nil {
			t.Fatal(err)
		}
		samples, rate, err := PacketSamples(raw)
		if err != nil {
			t.Errorf("%s: %v", c.file, err)
			continue
		}
		if samples != c.samples || rate != c.rate {
			t.Errorf("%s: %d samples at %d Hz, want %d at %d", c.file, samples, rate, c.samples, c.rate)
		}
	}
}

// TestPacketSamplesSubstreams pins what does and does not move the time line.
func TestPacketSamplesSubstreams(t *testing.T) {
	indep := uint32(StrmtypIndependent)
	dep := uint32(StrmtypDependent)
	cat := func(frames ...[]byte) []byte {
		var out []byte
		for _, f := range frames {
			out = append(out, f...)
		}
		return out
	}
	for _, c := range []struct {
		name   string
		packet []byte
		want   int
	}{
		{"one block", eac3SyncFrame(indep, 0, 0, 64), 256},
		{"two blocks", eac3SyncFrame(indep, 0, 1, 64), 512},
		{"three blocks", eac3SyncFrame(indep, 0, 2, 64), 768},
		{"six blocks", eac3SyncFrame(indep, 0, 3, 64), 1536},
		{"six frames of one block", cat(
			eac3SyncFrame(indep, 0, 0, 64), eac3SyncFrame(indep, 0, 0, 64),
			eac3SyncFrame(indep, 0, 0, 64), eac3SyncFrame(indep, 0, 0, 64),
			eac3SyncFrame(indep, 0, 0, 64), eac3SyncFrame(indep, 0, 0, 64)), 1536},
		{"core and dependent", cat(
			eac3SyncFrame(indep, 0, 3, 128), eac3SyncFrame(dep, 0, 3, 64)), 1536},
		{"two programmes", cat(
			eac3SyncFrame(indep, 0, 3, 128), eac3SyncFrame(indep, 1, 3, 64)), 1536},
		{"short frames each with a dependent", cat(
			eac3SyncFrame(indep, 0, 2, 64), eac3SyncFrame(dep, 0, 2, 64),
			eac3SyncFrame(indep, 0, 2, 64), eac3SyncFrame(dep, 0, 2, 64)), 1536},
	} {
		samples, rate, err := PacketSamples(c.packet)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
			continue
		}
		if samples != c.want || rate != 48000 {
			t.Errorf("%s: %d samples at %d Hz, want %d at 48000", c.name, samples, rate, c.want)
		}
	}
}

// TestPacketSamplesRejects pins that a packet which is not whole frames has no
// duration, rather than the duration of whatever part of it parsed.
func TestPacketSamplesRejects(t *testing.T) {
	frame := eac3SyncFrame(uint32(StrmtypIndependent), 0, 3, 64)
	for _, c := range []struct {
		name   string
		packet []byte
		want   error
	}{
		{"empty", nil, ErrNoSync},
		{"truncated", frame[:63], ErrShortFrame},
		{"second frame truncated", append(append([]byte{}, frame...), frame[:10]...), ErrShortFrame},
		{"trailing garbage", append(append([]byte{}, frame...), 1, 2, 3, 4, 5, 6), ErrNoSync},
		{"dependent alone", eac3SyncFrame(uint32(StrmtypDependent), 0, 3, 64), ErrNoSync},
	} {
		samples, rate, err := PacketSamples(c.packet)
		if !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
		if samples != 0 || rate != 0 {
			t.Errorf("%s: %d samples at %d Hz alongside an error", c.name, samples, rate)
		}
	}
}

func TestPacketSamplesDoesNotAllocate(t *testing.T) {
	packet := append(eac3SyncFrame(uint32(StrmtypIndependent), 0, 3, 128),
		eac3SyncFrame(uint32(StrmtypDependent), 0, 3, 64)...)
	if n := testing.AllocsPerRun(100, func() { PacketSamples(packet) }); n != 0 {
		t.Errorf("%v allocations per packet, want 0", n)
	}
}
