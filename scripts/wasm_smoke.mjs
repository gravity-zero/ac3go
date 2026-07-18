// wasm_smoke.mjs - end-to-end check of the ac3go wasm build under Node.
//
//   make wasm        # builds web/ac3go.wasm + web/wasm_exec.js
//   node scripts/wasm_smoke.mjs [file.ac3]
//
// Loads the wasm module the same way a browser would (Go's wasm_exec.js runtime
// + WebAssembly.instantiate), probes and decodes a stream, and asserts the
// result is coherent. Exits non-zero on any failure, so CI can gate on it.

import { readFile } from 'node:fs/promises'
import { fileURLToPath } from 'node:url'
import { dirname, join } from 'node:path'

const here = dirname(fileURLToPath(import.meta.url))
const web = join(here, '..', 'web')
const input = process.argv[2] ?? join(here, '..', 'ac3', 'testdata', 'tone_48k_5p1_448k.ac3')

// Go's runtime shim defines globalThis.Go. It is emitted next to the wasm by
// `make wasm`; import it for its side effect.
await import(join(web, 'wasm_exec.js'))

const go = new globalThis.Go()
const wasm = await readFile(join(web, 'ac3go.wasm'))
const { instance } = await WebAssembly.instantiate(wasm, go.importObject)
go.run(instance)
const ac3 = globalThis.Ac3Go

const bytes = new Uint8Array(await readFile(input))

const probe = ac3.probe(bytes)
if (probe.error) throw new Error('probe: ' + probe.error)
console.log('probe:', JSON.stringify(probe))

const dec = ac3.decode(bytes)
if (dec.error) throw new Error('decode: ' + dec.error)
const samples = new Float32Array(dec.bytes.buffer, dec.bytes.byteOffset, dec.frames * dec.channels)

const fail = (m) => { console.error('FAIL:', m); process.exit(1) }
if (probe.sampleRate !== dec.sampleRate) fail('probe/decode sample rate disagree')
if (dec.channels < 1 || dec.channels > 8) fail('channels out of range: ' + dec.channels)
if (dec.frames < 1) fail('no samples decoded')
if (samples.length !== dec.frames * dec.channels) fail('sample count mismatch')
let peak = 0
for (const v of samples) {
  if (!Number.isFinite(v)) fail('non-finite sample')
  const a = Math.abs(v)
  if (a > peak) peak = a
}
if (peak === 0) fail('decoded silence')

console.log(`OK: ${dec.channels} ch, ${dec.sampleRate} Hz, ${dec.frames} samples/ch, peak ${peak.toFixed(4)}`)
process.exit(0)
