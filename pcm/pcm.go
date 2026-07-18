// Package pcm holds the types shared across this module to describe decoded
// audio: the sample format, the channel layout, and the planar sample buffers
// a decoder writes into.
//
// The decoder works in float32 internally and hands out planar buffers, one
// plane per channel, because that is what the transform stage produces and
// what a downmix or an encoder wants to read. Interleaving to fixed-point is
// an explicit final step, not something the pipeline carries around.
//
// Buffers are meant to be allocated once and reused: a Planes value sized for
// the largest frame a stream can produce is enough for the whole stream.
package pcm

import (
	"errors"
	"fmt"
)

// Channel identifies one loudspeaker feed. The names follow the terms the
// AC-3 bit stream information uses for its audio coding modes.
type Channel uint8

// The channels this module can produce. ChannelCh1 and ChannelCh2 are the two
// independent programs of the dual mono mode, which are not a stereo pair and
// must not be treated as one.
const (
	ChannelLeft Channel = iota
	ChannelCenter
	ChannelRight
	ChannelLeftSurround
	ChannelRightSurround
	ChannelMonoSurround
	ChannelLFE
	ChannelCh1
	ChannelCh2
	// The extra channels of the 7.1 layout: two back speakers and two side
	// speakers. E-AC-3 carries these through a dependent substream that adds
	// them to the 5.1 core; see the ac3 package.
	ChannelBackLeft
	ChannelBackRight
	ChannelSideLeft
	ChannelSideRight
)

var channelNames = [...]string{
	ChannelLeft:          "L",
	ChannelCenter:        "C",
	ChannelRight:         "R",
	ChannelLeftSurround:  "Ls",
	ChannelRightSurround: "Rs",
	ChannelMonoSurround:  "S",
	ChannelLFE:           "LFE",
	ChannelCh1:           "Ch1",
	ChannelCh2:           "Ch2",
	ChannelBackLeft:      "BL",
	ChannelBackRight:     "BR",
	ChannelSideLeft:      "SL",
	ChannelSideRight:     "SR",
}

// String returns the short name of the channel.
func (c Channel) String() string {
	if int(c) < len(channelNames) && channelNames[c] != "" {
		return channelNames[c]
	}
	return fmt.Sprintf("Channel(%d)", uint8(c))
}

// Layout is an ordered channel list. The order is the order of the planes in
// a Planes value and of the samples in an interleaved buffer.
//
// A Layout is not a bit mask: the dual mono mode carries two channels that
// share no spatial meaning, and a Layout has to be able to say so.
type Layout []Channel

// Common layouts. LayoutDualMono is the 1+1 mode: two independent programs.
var (
	LayoutMono     = Layout{ChannelCenter}
	LayoutStereo   = Layout{ChannelLeft, ChannelRight}
	LayoutDualMono = Layout{ChannelCh1, ChannelCh2}
	Layout3F2R     = Layout{ChannelLeft, ChannelCenter, ChannelRight, ChannelLeftSurround, ChannelRightSurround}

	// Layout7point1 is the order an E-AC-3 7.1 programme is delivered in: the
	// 5.1 front and LFE, then the two back speakers, then the two side ones.
	// It matches the channel order a reference decoder emits, so no crossing is
	// needed between the two.
	Layout7point1 = Layout{
		ChannelLeft, ChannelRight, ChannelCenter, ChannelLFE,
		ChannelBackLeft, ChannelBackRight, ChannelSideLeft, ChannelSideRight,
	}
)

// String renders the layout as a channel list, for example "L,C,R,Ls,Rs,LFE".
func (l Layout) String() string {
	if len(l) == 0 {
		return "-"
	}
	s := l[0].String()
	for _, c := range l[1:] {
		s += "," + c.String()
	}
	return s
}

// Index returns the position of c in the layout, or -1 when it is absent.
func (l Layout) Index(c Channel) int {
	for i, got := range l {
		if got == c {
			return i
		}
	}
	return -1
}

// Has reports whether the layout contains c.
func (l Layout) Has(c Channel) bool { return l.Index(c) >= 0 }

// WithLFE returns a copy of the layout with the LFE channel appended. The LFE
// always comes last, after the full-bandwidth channels, and it is excluded
// from a downmix. It allocates, so it belongs to stream setup rather than to
// the per-frame path.
func (l Layout) WithLFE() Layout {
	out := make(Layout, len(l), len(l)+1)
	copy(out, l)
	return append(out, ChannelLFE)
}

// Format describes a stream of decoded audio.
type Format struct {
	SampleRate int    // samples per second and per channel
	Layout     Layout // channel order of the planes
}

// Channels returns the number of channels in the format.
func (f Format) Channels() int { return len(f.Layout) }

// String renders the format as "48000Hz L,R".
func (f Format) String() string {
	return fmt.Sprintf("%dHz %s", f.SampleRate, f.Layout)
}

// ErrShortPlane is returned when a caller asks a Planes value to hold more
// samples than it was allocated for.
var ErrShortPlane = errors.New("pcm: sample count exceeds plane capacity")

// Planes is a set of planar sample buffers, one per channel of a Format, cut
// out of a single backing array.
//
// Len reports how many samples of each plane are currently valid; Cap is how
// many the buffers can hold. Resize moves Len within Cap without allocating,
// which is how a decoder reuses one Planes value for every frame of a stream.
type Planes struct {
	format Format
	data   []float32 // channels * capacity, plane i at [i*capacity : (i+1)*capacity]
	planes [][]float32
	length int
	cap    int
}

// NewPlanes allocates buffers for format holding up to capacity samples per
// channel. It is the only allocating call on the decode path and belongs to
// stream setup.
func NewPlanes(format Format, capacity int) *Planes {
	if capacity < 0 {
		capacity = 0
	}
	n := format.Channels()
	p := &Planes{
		format: format,
		data:   make([]float32, n*capacity),
		planes: make([][]float32, n),
		length: capacity,
		cap:    capacity,
	}
	for i := range p.planes {
		p.planes[i] = p.data[i*capacity : (i+1)*capacity : (i+1)*capacity]
	}
	return p
}

// Format returns the format the planes were allocated for.
func (p *Planes) Format() Format { return p.format }

// Channels returns the number of planes.
func (p *Planes) Channels() int { return len(p.planes) }

// Len returns the number of valid samples in each plane.
func (p *Planes) Len() int { return p.length }

// Cap returns the number of samples each plane can hold.
func (p *Planes) Cap() int { return p.cap }

// Resize sets the number of valid samples per plane. It does not allocate and
// it does not clear the samples; n above Cap returns ErrShortPlane and leaves
// the value untouched.
func (p *Planes) Resize(n int) error {
	if n < 0 || n > p.cap {
		return fmt.Errorf("resize to %d samples (capacity %d): %w", n, p.cap, ErrShortPlane)
	}
	p.length = n
	for i := range p.planes {
		p.planes[i] = p.data[i*p.cap : i*p.cap+n : (i+1)*p.cap]
	}
	return nil
}

// Plane returns the samples of channel i. The slice aliases the backing array
// and stays valid until the next Resize.
func (p *Planes) Plane(i int) []float32 { return p.planes[i] }

// PlaneFor returns the samples of channel c, or nil when the format has no
// such channel.
func (p *Planes) PlaneFor(c Channel) []float32 {
	if i := p.format.Layout.Index(c); i >= 0 {
		return p.planes[i]
	}
	return nil
}

// Zero clears every valid sample of every plane.
func (p *Planes) Zero() {
	clear(p.data[:p.Channels()*p.cap])
}

// Interleave writes the valid samples into dst as interleaved int32, scaling
// full scale (+/-1.0) to the given bit depth and clipping anything beyond it.
// dst must hold at least Len()*Channels() samples.
//
// It rounds to nearest and does not dither: dithering is a decision for the
// output stage, which must be able to turn it off to compare against a
// reference decoder.
func (p *Planes) Interleave(dst []int32, bits uint) error {
	if bits == 0 || bits > 32 {
		return fmt.Errorf("pcm: interleave to %d bits: %w", bits, ErrBadDepth)
	}
	n := p.length
	ch := p.Channels()
	if len(dst) < n*ch {
		return fmt.Errorf("interleave %d samples of %d channels into %d slots: %w", n, ch, len(dst), ErrShortPlane)
	}
	// The scaling and the clipping bounds are computed in float64, not float32.
	// At 32 bits the positive bound is 2^31 - 1, which float32 cannot hold: it
	// rounds up to 2^31, and converting that back to int32 overflows, turning a
	// full scale peak into full scale of the opposite sign. float64 holds every
	// int32 exactly, so the bound clips where it says it does.
	//
	// Scaling by a power of two only shifts the exponent, so widening does not
	// change any result the float32 path already got right.
	max := float64(int64(1)<<(bits-1) - 1)
	min := float64(-(int64(1) << (bits - 1)))
	scale := float64(int64(1) << (bits - 1))
	for c := range ch {
		src := p.planes[c]
		for i := range n {
			v := float64(src[i]) * scale
			switch {
			case v >= max:
				v = max
			case v <= min:
				v = min
			default:
				// Round half away from zero, as fixed-point audio conventionally does.
				if v >= 0 {
					v += 0.5
				} else {
					v -= 0.5
				}
			}
			dst[i*ch+c] = int32(v)
		}
	}
	return nil
}

// ErrBadDepth is returned for a bit depth outside [1, 32].
var ErrBadDepth = errors.New("pcm: bit depth out of range")
