package ac3

import (
	"errors"
	"slices"

	"github.com/gravity-zero/ac3go/pcm"
)

// Stats counts what a Decoder has done since NewDecoder: the access units it
// decoded, the ones it could not by what stopped them, and the decisions that
// leave no trace in an error - a dependent substream stepped over, a downmix
// applied, a change of layout mid-stream.
//
// It is for telemetry, read on demand. The Decoder keeps the counts on its
// frame path at no cost and never resets them: Reset is a seek, and a seek
// does not undo what was decoded before it. A caller that wants a count over a
// span takes the difference of two readings.
//
// A 7.1 access unit - an independent and a dependent substream - counts once,
// the way DecodeFrame takes it.
type Stats struct {
	// Frames is how many access units decoded without error.
	Frames int64

	// Why the rest did not, one count each, so that they sum to the calls to
	// DecodeFrame that returned an error.
	Truncated              int64 // the buffer, or the size the frame states, is short of what it needs
	BadHeaders             int64 // no syncword, or a header naming something reserved or unknown
	UnsupportedSubstreams  int64 // an E-AC-3 substream other than the independent one
	UnsupportedReducedRate int64 // the E-AC-3 half rate syntax
	BlockErrors            int64 // the audio itself does not decode
	Overruns               int64 // the audio decodes but runs past the room the frame has for it
	DependentErrors        int64 // a 7.1 extension that the merge needed did not decode

	// DependentSkipped is how many dependent substreams were stepped over
	// undecoded - one that is not the standard 7.1 extension, or the 7.1
	// extension itself under a downmix, which folds the 5.1 core instead - or
	// decoded and then dropped for not carrying the four channels it states.
	DependentSkipped int64

	// LayoutChanges is how many times the layout the stream codes changed from
	// one decoded access unit to the next. It does not count the first.
	LayoutChanges int64

	// Downmixed is how many decoded access units were mixed down. A downmix
	// asked for a stream that already is the layout asked for is not applied
	// and not counted.
	Downmixed int64

	// LastLayout is the layout the stream coded in the last access unit
	// decoded, before any downmix: 7.1 for a 7.1 access unit even when it was
	// folded to its core, nil before the first. It is a shared value; a caller
	// that intends to modify it must copy it.
	LastLayout pcm.Layout
}

// Stats returns the counts so far.
func (d *Decoder) Stats() Stats { return d.stats }

// Why a DecodeFrame failed, as the frame path knows it when it returns rather
// than as the error's text would have to be picked apart to tell. An overrun
// and a bad block wrap the same sentinel, and are not the same thing to a
// caller watching a stream.
type failure uint8

const (
	failNone failure = iota
	failTruncated
	failHeader
	failSubstream
	failReducedRate
	failBlock
	failOverrun
	failDependent
)

// headerFailure classifies an error from ParseHeader.
func headerFailure(err error) failure {
	if errors.Is(err, ErrShortFrame) {
		return failTruncated
	}
	return failHeader
}

// count records the outcome of one DecodeFrame.
func (d *Decoder) count(f failure) {
	s := &d.stats
	switch f {
	case failNone:
	case failTruncated:
		s.Truncated++
		return
	case failHeader:
		s.BadHeaders++
		return
	case failSubstream:
		s.UnsupportedSubstreams++
		return
	case failReducedRate:
		s.UnsupportedReducedRate++
		return
	case failBlock:
		s.BlockErrors++
		return
	case failOverrun:
		s.Overruns++
		return
	case failDependent:
		s.DependentErrors++
		return
	}

	s.Frames++
	if d.depSkipped {
		s.DependentSkipped++
	}
	if d.downmixing() {
		s.Downmixed++
	}
	layout := d.h.Layout()
	if d.coded71 {
		layout = pcm.Layout7point1
	}
	if s.LastLayout != nil && !slices.Equal(s.LastLayout, layout) {
		s.LayoutChanges++
	}
	s.LastLayout = layout
}
