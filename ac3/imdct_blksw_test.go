package ac3

import "testing"

// TestShortBlockInterleaveSplitsEvenFromOdd pins which coefficients each of a
// switched block's two transforms reads.
//
// A block the encoder split carries its two short transforms interleaved: the
// even coefficients are the first half of the block, the odd ones the second.
// The first becomes this block's samples, the second becomes the tail the next
// block overlaps with. Reading both halves from the same set - taking coeffs[2i]
// twice, or coeffs[2i+1] twice - leaves the block the right length and the
// spectrum in the right bins, and wrecks the time ordering inside it.
//
// Nothing else in this package sees it. Every fixture in testdata codes long
// blocks only - TestDecodeToneSpectrum asserts that they do - so the whole
// switched path is unreached here; the fixture that exercises it is aften's, in
// internal/e2e, which needs the oracle and so does not run in CI.
//
// The check needs no reference: put energy in one half only and the other
// transform has nothing to make, so it must produce silence. Which output goes
// silent is what names the half.
func TestShortBlockInterleaveSplitsEvenFromOdd(t *testing.T) {
	// A one channel decoder with no LFE, so channel 0 is a full bandwidth
	// channel and may switch.
	newSwitched := func() (*Decoder, *Block) {
		d := NewDecoder()
		d.h.Sync.Acmod, d.h.Acmod = AcmodMono, AcmodMono
		d.h.Sync.Lfeon, d.h.Lfeon = false, false
		d.lfeCh = -1
		d.nfchans = 1
		b := &Block{}
		b.Blksw[0] = true
		if !d.blockSwitched(b, 0) {
			t.Fatal("the block did not come out switched: the test would prove nothing")
		}
		return d, b
	}

	nonZero := func(x []float32) bool {
		for _, v := range x {
			if v != 0 {
				return true
			}
		}
		return false
	}

	// Energy in the even coefficients only. The first transform has something
	// to make and the second has nothing, so the block sounds and the tail it
	// hands on is silent.
	t.Run("even coefficients are the first half", func(t *testing.T) {
		d, b := newSwitched()
		for i := range shortMants {
			b.Coeffs[0][2*i] = 1
		}
		d.synthesize(b, 0)

		if !nonZero(d.pcm[0][:windowLen]) {
			t.Error("the even coefficients produced no samples: the first transform is not reading them")
		}
		if nonZero(d.delay[0][:]) {
			t.Error("the even coefficients left a tail: the second transform is reading them too")
		}
	})

	// The mirror. Now the first transform has nothing, so the block itself is
	// silent - it carries only the previous block's tail, which is zero here -
	// and all of the energy is in the tail handed to the next block.
	t.Run("odd coefficients are the second half", func(t *testing.T) {
		d, b := newSwitched()
		for i := range shortMants {
			b.Coeffs[0][2*i+1] = 1
		}
		d.synthesize(b, 0)

		if nonZero(d.pcm[0][:windowLen]) {
			t.Error("the odd coefficients produced samples in this block: the first transform is reading them")
		}
		if !nonZero(d.delay[0][:]) {
			t.Error("the odd coefficients left no tail: the second transform is not reading them")
		}
	})
}
