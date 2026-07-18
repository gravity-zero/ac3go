/**
 * ac3go.ts - typed wrapper around the ac3go WebAssembly build.
 *
 * The wasm module (built with `make wasm` -> web/ac3go.wasm + wasm_exec.js)
 * registers a global `Ac3Go` object; this wrapper loads it and exposes the same
 * API with TypeScript types, plus a Web Audio helper. Zero dependencies, works
 * in browsers, web workers and Node >= 18.
 *
 * Browser:
 *   import { loadAc3Go, decodeToAudioBuffer } from './ac3go'
 *   const ac3 = await loadAc3Go({ wasmUrl: '/ac3go.wasm', wasmExecUrl: '/wasm_exec.js' })
 *   const pcm = await ac3.decode(file)              // File/Blob/ArrayBuffer/Uint8Array
 *   const buf = decodeToAudioBuffer(pcm, new AudioContext())
 *
 * Node:
 *   require('path/to/wasm_exec.js')                 // defines globalThis.Go
 *   const ac3 = await loadAc3Go({ wasmBytes: fs.readFileSync('ac3go.wasm') })
 */

// ---------------------------------------------------------------------------
// Result types - property names mirror what the Go side emits.
// ---------------------------------------------------------------------------

/** The header of a stream's first frame, as returned by probe(). */
export interface ProbeResult {
  /** 'ac3' for AC-3 (bsid <= 8), 'eac3' for Enhanced AC-3 (bsid 16). */
  format: 'ac3' | 'eac3'
  sampleRate: number
  channels: number
  /** Channel layout, e.g. "L,C,R,Ls,Rs,LFE". */
  layout: string
  /** The audio coding mode's short name, e.g. "3/2" or "1+1". */
  mode: string
  lfe: boolean
  /** bit/s: what AC-3 announces, what E-AC-3 works out to. */
  bitRate: number
  /** Audio blocks per frame: always 6 in AC-3, 1 to 6 in E-AC-3. */
  blocks: number
  /** Dialogue normalization level in dBFS (negative). */
  dialnorm: number
  bsid: number
}

export interface DecodeOptions {
  /** Fold multichannel down to a pair or a single channel, per the bitstream. */
  downmix?: 'stereo' | 'mono'
  /**
   * Fill unallocated bins with noise (true, the default and what the format
   * asks for) or with silence (false, for reproducible output).
   */
  dither?: boolean
  /** Apply dialogue normalization toward this target dBFS (negative). */
  targetLevel?: number
}

/** Decoded PCM for a whole stream. `samples` is interleaved by channel. */
export interface DecodeResult {
  sampleRate: number
  channels: number
  /** Channel layout the samples are interleaved in, e.g. "L,R". */
  layout: string
  /** Samples per channel. */
  frames: number
  /** Interleaved float32: samples[frame * channels + channel]. */
  samples: Float32Array
}

/** The methods the wasm module exposes, with the input reading done for you. */
export interface Ac3GoApi {
  version(): string
  /** Read the first frame's header only, no decoding. */
  probe(input: Ac3Input): Promise<ProbeResult>
  /** Decode the whole stream to interleaved float32 PCM. */
  decode(input: Ac3Input, options?: DecodeOptions): Promise<DecodeResult>
}

/**
 * Anything the audio can arrive as: a raw elementary AC-3/E-AC-3 stream, or a
 * CMAF (fragmented-MP4) audio segment - what an HLS/DASH player fetches, or the
 * bytes appended to an MSE SourceBuffer. Both are auto-detected; hand over
 * whatever the player already has.
 */
export type Ac3Input = Uint8Array | ArrayBuffer | Blob

export interface LoadOptions {
  /** URL of ac3go.wasm (browser; fetched with instantiateStreaming). */
  wasmUrl?: string
  /** The wasm binary itself (Node, or a custom fetch). */
  wasmBytes?: ArrayBuffer | Uint8Array
  /**
   * URL of Go's wasm_exec.js runtime, injected as a <script> when
   * globalThis.Go is not already defined (browser convenience). In Node or a
   * bundler, load wasm_exec.js yourself before calling loadAc3Go.
   */
  wasmExecUrl?: string
}

// The raw shape the wasm module registers on globalThis. Each call returns a
// plain object; a failure carries an `error` string instead of a result.
interface RawApi {
  version(): string
  probe(bytes: Uint8Array): RawResult
  decode(bytes: Uint8Array, options?: DecodeOptions): RawResult
}
type RawResult = { error?: string; bytes?: Uint8Array; [k: string]: unknown }

declare global {
  // eslint-disable-next-line no-var
  var Go: { new (): { importObject: WebAssembly.Imports; run(i: WebAssembly.Instance): Promise<void> } }
  // eslint-disable-next-line no-var
  var Ac3Go: RawApi | undefined
}

// ---------------------------------------------------------------------------
// Loading
// ---------------------------------------------------------------------------

let loaded: Promise<Ac3GoApi> | undefined

/**
 * Load the wasm module once (subsequent calls return the same instance).
 * Provide either wasmUrl (browser) or wasmBytes (Node).
 */
export function loadAc3Go(options: LoadOptions): Promise<Ac3GoApi> {
  if (!loaded) loaded = doLoad(options)
  return loaded
}

async function doLoad(options: LoadOptions): Promise<Ac3GoApi> {
  if (typeof globalThis.Go === 'undefined') {
    if (!options.wasmExecUrl) throw new Error('ac3go: load wasm_exec.js first, or pass wasmExecUrl')
    await injectScript(options.wasmExecUrl)
  }
  const go = new globalThis.Go()
  let instance: WebAssembly.Instance
  if (options.wasmBytes) {
    ;({ instance } = await WebAssembly.instantiate(options.wasmBytes as BufferSource, go.importObject))
  } else if (options.wasmUrl) {
    ;({ instance } = await WebAssembly.instantiateStreaming(fetch(options.wasmUrl), go.importObject))
  } else {
    throw new Error('ac3go: pass wasmUrl or wasmBytes')
  }
  void go.run(instance) // runs for the module's lifetime
  while (typeof globalThis.Ac3Go === 'undefined') await new Promise((r) => setTimeout(r, 5))
  return wrap(globalThis.Ac3Go)
}

function injectScript(url: string): Promise<void> {
  return new Promise((resolve, reject) => {
    if (typeof document === 'undefined')
      return reject(new Error('ac3go: no document - load wasm_exec.js manually in this environment'))
    const s = document.createElement('script')
    s.src = url
    s.onload = () => resolve()
    s.onerror = () => reject(new Error(`ac3go: failed to load ${url}`))
    document.head.appendChild(s)
  })
}

// ---------------------------------------------------------------------------
// Wrapping - normalize input, throw on error, type the results.
// ---------------------------------------------------------------------------

async function toBytes(input: Ac3Input): Promise<Uint8Array> {
  if (input instanceof Uint8Array) return input
  if (input instanceof ArrayBuffer) return new Uint8Array(input)
  if (typeof Blob !== 'undefined' && input instanceof Blob) return new Uint8Array(await input.arrayBuffer())
  throw new Error('ac3go: input must be a Uint8Array, ArrayBuffer or Blob')
}

function check(r: RawResult): RawResult {
  if (r && typeof r.error === 'string') throw new Error(r.error)
  return r
}

function wrap(raw: RawApi): Ac3GoApi {
  return {
    version: () => raw.version(),
    async probe(input) {
      const r = check(raw.probe(await toBytes(input)))
      return r as unknown as ProbeResult
    },
    async decode(input, options) {
      const r = check(raw.decode(await toBytes(input), options))
      const bytes = r.bytes as Uint8Array
      // The wasm side hands back interleaved little-endian float32; reinterpret
      // it in place. The offset/length guard covers a non-zero byteOffset.
      const samples = new Float32Array(bytes.buffer, bytes.byteOffset, (r.frames as number) * (r.channels as number))
      return {
        sampleRate: r.sampleRate as number,
        channels: r.channels as number,
        layout: r.layout as string,
        frames: r.frames as number,
        samples,
      }
    },
  }
}

// ---------------------------------------------------------------------------
// Web Audio helper
// ---------------------------------------------------------------------------

/**
 * Turn a DecodeResult into an AudioBuffer ready for an AudioBufferSourceNode.
 * De-interleaves the samples into the buffer's per-channel planes. The context
 * only supplies the buffer factory, so an OfflineAudioContext works too.
 */
export function decodeToAudioBuffer(dec: DecodeResult, ctx: BaseAudioContext): AudioBuffer {
  const buf = ctx.createBuffer(dec.channels, dec.frames, dec.sampleRate)
  for (let ch = 0; ch < dec.channels; ch++) {
    const plane = buf.getChannelData(ch)
    for (let i = 0; i < dec.frames; i++) plane[i] = dec.samples[i * dec.channels + ch]
  }
  return buf
}
