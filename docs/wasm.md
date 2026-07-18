# ac3go in the browser (WebAssembly)

The decoder is pure Go with no cgo, so it compiles to `GOOS=js GOARCH=wasm` and
runs in a browser or in Node. This document covers the build, the JavaScript
API, the TypeScript wrapper, and the React/Vue helpers.

The wasm module does one thing: **decode an AC-3 / E-AC-3 elementary stream to
PCM.** What you do with the samples - play them through Web Audio, draw a
waveform, hold them in sync with a video element - is up to the page.

## Build

```bash
make wasm         # -> web/ac3go.wasm  +  web/wasm_exec.js
```

`wasm_exec.js` is Go's runtime shim, copied verbatim from your Go toolchain
(`$(go env GOROOT)/lib/wasm/wasm_exec.js`). Both files are build artifacts and
are git-ignored; rebuild them with `make wasm` after pulling.

Run the bundled demo:

```bash
make wasm
cd web && python3 -m http.server 8000
# open http://localhost:8000/example/  and drop an .ac3 file in
```

Check the build end to end under Node (also a good CI gate):

```bash
make wasm-smoke
```

## The API

The module registers a global `Ac3Go` object:

| Method | Returns |
|---|---|
| `Ac3Go.version()` | build version string |
| `Ac3Go.probe(bytes)` | the first frame's header - `{ format, sampleRate, channels, layout, mode, lfe, bitRate, blocks, dialnorm, bsid }` |
| `Ac3Go.decode(bytes, opts?)` | the whole stream as PCM - `{ sampleRate, channels, layout, frames, bytes }` |

`bytes` is a `Uint8Array` holding either a raw elementary AC-3/E-AC-3 stream
**or** a CMAF (fragmented-MP4) audio segment - the two are told apart by their
first bytes, so a page can hand over whatever its HLS/DASH player already has
(a fetched audio segment, or the bytes appended to an MSE `SourceBuffer`)
without unwrapping it first. `decode` returns `bytes` as interleaved
little-endian float32 (`samples[frame * channels + channel]`); wrap it as `new
Float32Array(res.bytes.buffer, res.bytes.byteOffset, res.frames *
res.channels)`. On failure a method returns `{ error: "..." }` instead of a
result - the TypeScript wrapper below turns that into a thrown `Error`.

> ac3go does not fetch segments or parse an m3u8/MPD manifest - that is the
> player's job (hls.js, Shaka, native MSE). It takes the AC-3/E-AC-3 bytes the
> player already has and returns PCM. Unwrapping a CMAF audio segment to its
> elementary frames is also available in Go as the `cmaf` package.

`decode` options:

| Option | Meaning |
|---|---|
| `downmix: 'stereo' \| 'mono'` | fold multichannel down using the bitstream's own coefficients |
| `dither: boolean` | fill unallocated bins with noise (default `true`, what the format asks) or silence (`false`) |
| `targetLevel: number` | apply dialogue normalization toward this dBFS (negative) |

Both methods are **synchronous**: `decode` runs the whole stream to completion.
For a large file, run the module in a Web Worker so the main thread stays
responsive - the API is the same, and PCM transfers back cheaply as a
transferable `ArrayBuffer`.

## TypeScript wrapper

[`web/ac3go.ts`](../web/ac3go.ts) loads the module and types the API, reads
`File`/`Blob`/`ArrayBuffer` inputs for you, throws on error, and adds a Web
Audio helper.

```ts
import { loadAc3Go, decodeToAudioBuffer } from './ac3go'

const ac3 = await loadAc3Go({ wasmUrl: '/ac3go.wasm', wasmExecUrl: '/wasm_exec.js' })

const probe = await ac3.probe(file)                 // File/Blob/ArrayBuffer/Uint8Array
console.log(probe.format, probe.sampleRate, probe.layout)

const pcm = await ac3.decode(file, { downmix: 'stereo' })
const ctx = new AudioContext()
const buffer = decodeToAudioBuffer(pcm, ctx)        // ready for an AudioBufferSourceNode
const src = ctx.createBufferSource()
src.buffer = buffer
src.connect(ctx.destination)
src.start()
```

In Node, load `wasm_exec.js` yourself and pass the bytes:

```ts
import { readFile } from 'node:fs/promises'
await import('./web/wasm_exec.js')                  // defines globalThis.Go
const ac3 = await loadAc3Go({ wasmBytes: await readFile('web/ac3go.wasm') })
```

## React

[`web/react.ts`](../web/react.ts) - hooks over the wrapper.

```tsx
import { useAc3Go, useProbe, useAc3Player } from './react'

function Player({ file }: { file: File }) {
  const ac3 = useAc3Go({ wasmUrl: '/ac3go.wasm', wasmExecUrl: '/wasm_exec.js' })
  const { probe } = useProbe(ac3, file)
  const video = useRef<HTMLVideoElement>(null)
  const { play, stop, drift, ready } = useAc3Player(ac3, file, video)

  return (
    <>
      <video ref={video} src="/clip.mp4" muted />
      <button onClick={play} disabled={!ready}>Play</button>
      <button onClick={stop}>Stop</button>
      {probe && <p>{probe.format} {probe.sampleRate} Hz {probe.layout}</p>}
      <p>drift: {drift.toFixed(1)} ms</p>
    </>
  )
}
```

`useAc3Player` plays the decoded audio through Web Audio and, when handed a
`<video>` ref, reports the running **drift** in milliseconds between the audio
clock and the video clock - the picture rides on the video element, the sound on
ac3go. `useAc3Buffer` is the same decode without playback, for drawing a
waveform or feeding your own graph.

## Vue

[`web/vue.ts`](../web/vue.ts) - the same three composables for Vue 3
(`useAc3Go`, `useProbe`, `useAc3Player` / `useAc3Buffer`), mirroring the React
API.

```vue
<script setup lang="ts">
import { ref } from 'vue'
import { useAc3Go, useProbe, useAc3Player } from './vue'

const file = ref<File | null>(null)
const video = ref<HTMLVideoElement | null>(null)
useAc3Go({ wasmUrl: '/ac3go.wasm', wasmExecUrl: '/wasm_exec.js' })
const { probe } = useProbe(file)
const { play, stop, drift } = useAc3Player(file, video)
</script>
```

## Notes and limits

- **Whole-stream in memory.** `decode` takes the whole elementary stream and
  returns the whole PCM. For long content, segment the input yourself and decode
  windows; the decoder is allocation-free per frame, so windowed decoding costs
  nothing extra. (A frame's first 256 samples overlap the previous frame, so a
  window decoded cold has one wrong block at its head - hand it the previous
  frame as a primer, or accept a 32 ms seam.)
- **Sample format.** Output is float32 in [-1, 1). Browsers play float PCM
  natively through Web Audio; there is no quantization step to 16-bit here.
- **7.1.** A 7.1 programme decodes to eight channels (`L,R,C,LFE,BL,BR,SL,SR`),
  merging the independent substream's 5.1 core with a dependent substream's side
  and back channels. Unusual 7.1 encodings that are not the standard extension
  decode to their 5.1 core.
- **What is not decoded** (gated exactly as in the library): enhanced coupling,
  the reduced sample rate syntax, and JOC/Atmos - the core outputs standard 5.1.
  A stream that needs one of these fails the frame rather than guessing.
