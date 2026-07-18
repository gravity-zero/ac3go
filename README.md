# ac3go

[![Go Reference](https://pkg.go.dev/badge/github.com/gravity-zero/ac3go.svg)](https://pkg.go.dev/github.com/gravity-zero/ac3go)
[![CI](https://github.com/gravity-zero/ac3go/actions/workflows/ci.yml/badge.svg)](https://github.com/gravity-zero/ac3go/actions/workflows/ci.yml)
[![License: PolyForm NC 1.0.0](https://img.shields.io/badge/License-PolyForm%20NC%201.0.0-blue.svg)](LICENSE.md)

**Pure-Go AC-3 and E-AC-3 decoder.** Turn an AC-3 or Enhanced AC-3 (E-AC-3)
bitstream into PCM - in-process, with no external tools, no cgo and zero
dependencies. The same code runs as a Go library, a small CLI, and a
WebAssembly module that decodes in the browser.

---

## What it does

- **Decode AC-3 and E-AC-3 to PCM** - the full pipeline: exponents, parametric
  bit allocation, mantissas, coupling, rematrixing, the 512/256-point IMDCT with
  KBD windowing and overlap-add, and for E-AC-3 the adaptive hybrid transform
  (AHT/GAQ) and spectral extension (SPX). Output is float sample planes, 1536
  per frame.
- **Sample-accurate** - verified frame by frame against an external reference
  decoder on real 5.1 DDP and stereo streams. Where the format is inherently
  non-reproducible (the noise that fills unallocated bins, and the extension's
  noise), that noise is switchable off so the rest can be checked exactly.
- **Downmix and loudness** - fold 5.1 down to stereo or mono using the
  bitstream's own coefficients (`cmixlev`/`surmixlev`, sum-renormalized), and
  apply `dialnorm` to a target level - the same way a reference decoder does.
- **Streaming, allocation-free** - never holds a whole file; reuses its buffers,
  so decoding allocates nothing per frame in steady state (~390x real time per
  core on amd64).
- **Runs in the browser** - compiled to WebAssembly, it decodes a stream to PCM
  that a page plays through Web Audio, no server involved. It takes either a raw
  elementary stream or a CMAF (fragmented-MP4) audio segment - what an HLS/DASH
  player fetches - so the page hands over whatever it already has.
- **Inspect** - a small CLI (`ac3info`) lists every frame's bit-stream
  information, or a one-line-per-stream summary.

---

## Runs anywhere

The same decoder, reached three ways:

| Runtime | What |
|---|---|
| **Go library** | import the `ac3` package - the decoder, the frame reader, the header parser |
| **CLI** (`ac3info`) | one static binary, inspects a stream frame by frame |
| **WebAssembly** | pure Go, cgo-free; decode in the browser and play through Web Audio - [docs/wasm.md](docs/wasm.md) |

Cross-cutting: **deterministic** (same frame → same samples, the decoder's own
noise included), corruption-tolerant framing (resync on the syncword, CRCs
verified), bounded memory on hostile input, and continuous fuzzing.

---

## Supported

| | Decoded | Notes |
|---|:---:|---|
| **AC-3** (bsid ≤ 8) | ✅ | 32 / 44.1 / 48 kHz; every audio mode incl. 1+1 dual mono, 2/1, 3/1, 3/2+LFE; the alternate bit-stream syntax (xbsi1/xbsi2) |
| **E-AC-3** (bsid 16) | ✅ | independent substream, 1-6 blocks per frame, AHT/GAQ, spectral extension |
| **7.1** (dependent substream) | ✅ | the standard extension: a 3/2+LFE core plus a dependent substream adding the two side and two back channels. Other 7.1 encodings decode to their 5.1 core |
| Enhanced coupling | ✗ | gated - the reference decoder does not implement it either, so no stream in reach uses it and nothing could validate it |
| Reduced sample rate (fscod2) | ✗ | gated, same reason |
| JOC / Atmos | ✗ | ignored by design - the core outputs standard 5.1 |

---

## See it in action

```console
$ ac3info -v -n 1 track.ac3
frame 0      @0           1792 B  48000 Hz 448 kbit/s bsid=8 acmod=7(3/2)+LFE L,C,R,Ls,Rs,LFE dialnorm=-31 dB
    syncinfo   fscod=0 (48000 Hz)  frmsizecod=30 (1792 B, 448 kbit/s)  crc1=0x408e
    bsi        bsid=8  bsmod=0 (main audio service: complete main (CM))  acmod=7 (3/2)  lfeon=true
    cmixlev    1 (x0.595)
    surmixlev  1 (x0.500)
    dialnorm   31 (-31 dB)
    flags      copyrightb=false  origbs=true
    audio      starts at bit 69 (byte 8.5)

$ ac3info -summary track.eac3
frames     32
bytes      49152 in frames, 0 skipped
duration   1.024 s (49152 samples at 48000 Hz)
bitrate    384.0 kbit/s measured
crc        0 bad of 32
shapes     1 distinct
      32 x  48000 Hz 384 kbit/s bsid=16 acmod=7(3/2)+LFE L,C,R,Ls,Rs,LFE dialnorm=-31 dB
```

## Install

```bash
go get github.com/gravity-zero/ac3go                          # library
go install github.com/gravity-zero/ac3go/cmd/ac3info@latest   # inspector CLI
```

## Library

```go
import (
	"errors"
	"fmt"
	"io"

	"github.com/gravity-zero/ac3go/ac3"
	"github.com/gravity-zero/ac3go/pcm"
)
```

**Decode a stream, frame by frame:**

```go
fr := ac3.NewFrameReader(file) // reads syncframes, resyncs, verifies CRCs
d := ac3.NewDecoder()          // reuse across the stream; allocates nothing per frame

for {
	frame, err := fr.Next()
	if errors.Is(err, io.EOF) {
		break
	} else if err != nil {
		return err
	}
	if err := d.DecodeFrame(frame); err != nil {
		return err
	}
	for ch := 0; ch < d.OutputChannels(); ch++ {
		samples := d.Samples(ch) // []float32, 1536 samples in d.OutputLayout() order
		_ = samples
	}
}
```

**Downmix and loudness** (both off by default - the decoder reproduces the
stream's own channels and level unless asked otherwise):

```go
d.SetDownmix(pcm.LayoutStereo) // or pcm.LayoutMono - Lo/Ro per the bitstream coefficients
d.SetTargetLevel(-31)          // apply dialnorm toward a target dBFS
d.SetDither(false)             // fill unallocated bins with silence, not noise (for exact comparison)
```

**Parse a header without decoding** (head-only, for indexing):

```go
var h ac3.Header
if err := ac3.ParseHeader(frame, &h); err != nil {
	return err
}
fmt.Println(h.Sync.SampleRate, h.Channels(), h.Layout(), h.DialnormDB())
```

**From a CMAF / fragmented-MP4 audio segment** (what an HLS or DASH player
fetches) - the `cmaf` package unwraps it to elementary frames, which the reader
above then decodes:

```go
import "github.com/gravity-zero/ac3go/cmaf"

_, elementary, err := cmaf.Elementary(segment) // one AC-3/E-AC-3 track's frames
if err != nil {
	return err
}
fr := ac3.NewFrameReader(bytes.NewReader(elementary))
// ...decode as above
```

`cmaf` reads one audio track's samples out of a CMAF segment and nothing more:
fetching segments and parsing the m3u8/MPD manifest is the player's job, not the
decoder's.

Full API: **[pkg.go.dev](https://pkg.go.dev/github.com/gravity-zero/ac3go)**.

## Browser

Build the WebAssembly bundle and play a stream through Web Audio, no server:

```bash
make wasm                       # → web/ac3go.wasm + web/wasm_exec.js
cd web && python3 -m http.server 8000
# open http://localhost:8000/example/  and drop an .ac3 file in
```

```ts
import { loadAc3Go, decodeToAudioBuffer } from './web/ac3go'

const ac3 = await loadAc3Go({ wasmUrl: '/ac3go.wasm', wasmExecUrl: '/wasm_exec.js' })
const pcm = await ac3.decode(file)              // File/Blob/ArrayBuffer/Uint8Array
const ctx = new AudioContext()
const src = ctx.createBufferSource()
src.buffer = decodeToAudioBuffer(pcm, ctx)
src.connect(ctx.destination)
src.start()
```

Typed wrapper, framework helpers and a runnable demo:

| File | What |
|---|---|
| [`web/ac3go.ts`](web/ac3go.ts) | Typed wrapper: loader, `probe`/`decode`, `decodeToAudioBuffer` |
| [`web/react.ts`](web/react.ts) | React hooks: `useAc3Go`, `useProbe`, `useAc3Player` (Web Audio + A/V sync) |
| [`web/vue.ts`](web/vue.ts) | The same three composables for Vue 3 |
| [`web/example/`](web/example/) | Drag-in-a-file browser demo - probe, decode, play |
| [`scripts/wasm_smoke.mjs`](scripts/wasm_smoke.mjs) | Node end-to-end check (`make wasm-smoke`) |

Full guide: **[docs/wasm.md](docs/wasm.md)**.

## Command reference

```
ac3info [options] <file.ac3|file.eac3>
```

| Flag | Description |
|---|---|
| `-summary` | Print the one-line-per-stream summary only (counts, duration, bitrate, CRC, distinct frame shapes) |
| `-v` | Print every bit-stream information field of each frame |
| `-n <n>` | Stop after `n` frames (0 = all) |
| `-nocrc` | Do not verify the check words |

## Documentation

| Doc | For |
|---|---|
| **[docs/wasm.md](docs/wasm.md)** | The browser build: JS API, TypeScript wrapper, React/Vue helpers, the demo. |
| **[pkg.go.dev](https://pkg.go.dev/github.com/gravity-zero/ac3go)** | Generated Go API reference. |

## Status & roadmap

The **decoder is done and verified**: AC-3 and E-AC-3, mono through 5.1,
downmix, dialnorm - green against an external reference on real streams, and
running in the browser via WebAssembly.

Post-v1: downmixing a 7.1 programme (today its 5.1 core is what downmixes),
Lt/Rt downmix, 24-bit output.

## Architecture

```
bitstream/      MSB-first bit reader
ac3/            the decoder: syncframe → blocks → PCM. AC-3 and E-AC-3 in one
                package - one Decoder, one per-stream state (E-AC-3 is a
                different header plus ~500 lines grafted on, not a separate codec)
cmaf/           unwrap AC-3/E-AC-3 frames from a CMAF/fragmented-MP4 audio segment
pcm/            shared types: format, channel layout, sample planes
cmd/ac3info/    the inspector CLI
cmd/ac3go-wasm/ the WebAssembly entry point (global Ac3Go object)
web/            TypeScript wrapper + React/Vue helpers + runnable demo
internal/e2e/   the oracle harness (compares PCM against a containerized reference)
```

Import graph: `cmd/ac3info` → `ac3` → `bitstream`, `pcm`; `cmaf` → nothing.

`cmaf` is the thin adapter between a container and the decoder: it reads one
audio track's samples out of a CMAF segment and returns the elementary
AC-3/E-AC-3 bytes. It is not a general MP4 library, a demuxer for video, or an
HLS/DASH client - fetching segments and parsing the manifest is the player's
job. The decoder's contract stays: frames in, PCM out.

## Build

```bash
make build         # library + CLI (CGO_ENABLED=0)
make test          # tests (race is CI-only: no cgo locally)
make wasm          # web/ac3go.wasm + web/wasm_exec.js
make wasm-smoke    # build the wasm bundle, then a Node end-to-end check
make vet           # vet for the host and for 32-bit
```

## License

[PolyForm Noncommercial 1.0.0](LICENSE.md). Noncommercial use is free; any
commercial use requires a separate license from the author.
