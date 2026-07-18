//go:build js && wasm

// Command ac3go-wasm builds the decoder for WebAssembly (GOOS=js GOARCH=wasm).
// It registers a global `Ac3Go` object whose methods mirror the library:
//
//	Ac3Go.version()             -> string
//	Ac3Go.probe(bytes)          -> header, plus channel count of the first access unit
//	Ac3Go.decode(bytes, opts?)  -> interleaved float32 PCM for the whole stream
//
// bytes is a Uint8Array holding a whole AC-3 / E-AC-3 elementary stream. Both
// methods are synchronous; a caller that does not want to block the main thread
// runs them in a Web Worker. See web/ac3go.ts for the typed wrapper and
// docs/wasm.md for the build.
package main

import (
	"encoding/binary"
	"math"
	"syscall/js"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/cmaf"
	"github.com/gravity-zero/ac3go/pcm"
)

// isoBoxTypes are the top-level boxes a CMAF audio segment or initialization
// segment opens with. Their presence at bytes 4..8 is how the two methods tell
// a fragmented-MP4 segment from a raw elementary stream, so a page can hand
// over whatever its player already has.
var isoBoxTypes = map[string]bool{
	"ftyp": true, "styp": true, "moov": true, "moof": true, "sidx": true,
	"free": true, "skip": true, "emsg": true,
}

// elementary returns data unchanged when it is already an elementary AC-3 /
// E-AC-3 stream, or the audio bitstream pulled out of it when it is a CMAF
// (fragmented-MP4) segment. An elementary stream opens on the syncword; a
// segment opens on an ISO box.
func elementary(data []byte) ([]byte, error) {
	if len(data) >= 8 && isoBoxTypes[string(data[4:8])] {
		_, es, err := cmaf.Elementary(data)
		return es, err
	}
	return data, nil
}

var version = "dev"

func main() {
	api := js.Global().Get("Object").New()
	api.Set("version", js.FuncOf(func(js.Value, []js.Value) any { return version }))
	api.Set("probe", js.FuncOf(probe))
	api.Set("decode", js.FuncOf(decode))
	js.Global().Set("Ac3Go", api)

	// Keep the Go runtime alive; work happens in the exported callbacks.
	select {}
}

// fail returns a result object carrying an error string, which the wrapper
// turns into a thrown Error. Returning it rather than panicking keeps a
// malformed stream from tearing down the whole wasm instance.
func fail(msg string) any {
	res := js.Global().Get("Object").New()
	res.Set("error", msg)
	return res
}

// bytesArg copies argument i, a Uint8Array, into Go memory.
func bytesArg(args []js.Value, i int) []byte {
	src := args[i]
	b := make([]byte, src.Get("length").Int())
	js.CopyBytesToGo(b, src)
	return b
}

// probe reports the first frame's header and the channel layout of the first
// access unit, so a 7.1 programme reads as eight channels rather than its 5.1
// core. It decodes only that first access unit.
func probe(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return fail("ac3go: probe(bytes) needs one Uint8Array argument")
	}
	data, err := elementary(bytesArg(args, 0))
	if err != nil {
		return fail("ac3go: " + err.Error())
	}

	var h ac3.Header
	if err := ac3.ParseHeader(data, &h); err != nil {
		return fail("ac3go: " + err.Error())
	}
	// Decode the first access unit as well, so a 7.1 programme - an independent
	// substream plus a dependent one - reports eight channels rather than the
	// 5.1 core its first header alone would show. The header fields still come
	// from that first substream.
	channels, layout := h.Channels(), h.Layout()
	d := ac3.NewDecoder()
	if d.DecodeFrame(data) == nil {
		channels, layout = d.OutputChannels(), d.OutputLayout()
	}

	format := "ac3"
	if h.Sync.Bsid == 16 {
		format = "eac3"
	}
	res := js.Global().Get("Object").New()
	res.Set("format", format)
	res.Set("sampleRate", h.Sync.SampleRate)
	res.Set("channels", channels)
	res.Set("layout", layout.String())
	res.Set("mode", h.AcmodName())
	res.Set("lfe", h.Lfeon)
	res.Set("bitRate", h.Sync.BitRate)
	res.Set("blocks", h.Sync.NumBlocks)
	res.Set("dialnorm", h.DialnormDB())
	res.Set("bsid", int(h.Sync.Bsid))
	return res
}

// decodeOptions reads the optional second argument: { downmix, dither,
// targetLevel }. A missing or undefined field leaves the decoder at its
// default (no downmix, dither on, no dialnorm target).
func decodeOptions(d *ac3.Decoder, opts js.Value) error {
	if opts.Type() != js.TypeObject {
		return nil
	}
	switch dm := opts.Get("downmix"); {
	case dm.Type() != js.TypeString:
	case dm.String() == "stereo":
		if err := d.SetDownmix(pcm.LayoutStereo); err != nil {
			return err
		}
	case dm.String() == "mono":
		if err := d.SetDownmix(pcm.LayoutMono); err != nil {
			return err
		}
	}
	if v := opts.Get("dither"); v.Type() == js.TypeBoolean {
		d.SetDither(v.Bool())
	}
	if v := opts.Get("targetLevel"); v.Type() == js.TypeNumber {
		d.SetTargetLevel(v.Int())
	}
	return nil
}

// decode runs the whole elementary stream through the decoder and returns the
// PCM as interleaved little-endian float32, ready to become a Float32Array. It
// stops at the first frame that fails rather than dropping the samples already
// decoded, so a truncated stream still yields what came before the break.
func decode(_ js.Value, args []js.Value) any {
	if len(args) < 1 {
		return fail("ac3go: decode(bytes, opts?) needs a Uint8Array argument")
	}
	data, err := elementary(bytesArg(args, 0))
	if err != nil {
		return fail("ac3go: " + err.Error())
	}

	d := ac3.NewDecoder()
	if len(args) > 1 {
		if err := decodeOptions(d, args[1]); err != nil {
			return fail("ac3go: " + err.Error())
		}
	}

	var planes [][]float32
	var channels, sampleRate int
	var layout pcm.Layout
	// The whole remaining stream is handed to each decode, and the reader
	// advances by the access unit rather than one syncframe: a 7.1 access unit
	// is an independent substream followed by a dependent one, and the decoder
	// needs both in view to merge them into eight channels.
	pos := data
	for len(pos) > 0 {
		var h ac3.Header
		if ac3.ParseHeader(pos, &h) != nil {
			break
		}
		if d.DecodeFrame(pos) != nil {
			break
		}
		if planes == nil {
			channels = d.OutputChannels()
			sampleRate = d.Header().Sync.SampleRate
			layout = d.OutputLayout()
			planes = make([][]float32, channels)
		}
		for ch := 0; ch < channels; ch++ {
			planes[ch] = append(planes[ch], d.Samples(ch)...)
		}
		pos = pos[d.AccessUnitSize():]
	}
	if planes == nil {
		return fail("ac3go: no frame decoded")
	}

	frames := len(planes[0])
	raw := make([]byte, frames*channels*4)
	for i := 0; i < frames; i++ {
		for ch := 0; ch < channels; ch++ {
			binary.LittleEndian.PutUint32(raw[(i*channels+ch)*4:], math.Float32bits(planes[ch][i]))
		}
	}
	out := js.Global().Get("Uint8Array").New(len(raw))
	js.CopyBytesToJS(out, raw)

	res := js.Global().Get("Object").New()
	res.Set("sampleRate", sampleRate)
	res.Set("channels", channels)
	res.Set("layout", layout.String())
	res.Set("frames", frames)
	res.Set("bytes", out)
	return res
}
