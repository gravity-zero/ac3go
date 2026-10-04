package ac3

// How long a frame is on the time line, read from the frame itself.
//
// A container states a duration for every packet it carries, and that duration
// is only as good as the muxer that wrote it: a sample table can drift a tick
// either side of the truth, and a timestamp in milliseconds cannot say 1536
// samples at 48 kHz at all. The frame has no such trouble. It says how many
// blocks it holds and at what rate, and that is exact.

// Samples returns how many samples per channel the frame holds: 1536 for
// AC-3, and 256, 512, 768 or 1536 for enhanced AC-3.
func (si *SyncInfo) Samples() int { return si.NumBlocks * SamplesPerBlock }

// AdvancesTime reports whether the frame moves the time line forward. Every
// AC-3 frame does. In an enhanced stream only independent substream 0 does:
// a dependent substream extends the frame before it, and an independent
// substream past 0 is another programme, and both cover the span substream 0
// already covered. Summing Samples over every frame of such a stream counts
// that span more than once.
func (si *SyncInfo) AdvancesTime() bool {
	if !isEAC3(si.Bsid) {
		return true
	}
	return si.Strmtyp != StrmtypDependent && si.Substreamid == 0
}

// PacketSamples returns how many samples per channel a packet covers on the
// time line and the sample rate they are at. A packet is whole syncframes laid
// end to end, the way a container stores them: one AC-3 frame, or as many
// enhanced frames as the muxer put together - the short frames that make up
// six blocks, a 5.1 core and the dependent substream that extends it.
//
// Only the syncinfo of each frame is read. No check word is verified and
// nothing is decoded, so this costs a few bytes per frame and allocates
// nothing on a sound packet. A packet that ends inside a frame, or holds
// anything that is not a frame, is an error rather than a shorter duration.
func PacketSamples(b []byte) (samples, rate int, err error) {
	var si SyncInfo
	for len(b) > 0 {
		if err := ParseSyncInfo(b, &si); err != nil {
			return 0, 0, err
		}
		if len(b) < si.FrameSize {
			return 0, 0, shortFrameError(len(b), si.FrameSize)
		}
		if si.AdvancesTime() {
			samples += si.Samples()
			if rate == 0 {
				rate = si.SampleRate
			}
		}
		b = b[si.FrameSize:]
	}
	if rate == 0 {
		// Empty, or dependent substreams with nothing to depend on.
		return 0, 0, wrap(ErrNoSync)
	}
	return samples, rate, nil
}
