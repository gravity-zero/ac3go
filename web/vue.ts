/**
 * vue.ts - copyable Vue 3 composables over the ac3go wasm module, mirroring
 * web/react.ts. Zero dependencies beyond vue and ./ac3go. See docs/wasm.md.
 */
import { onScopeDispose, ref, shallowRef, watch, type Ref } from 'vue'
import {
  loadAc3Go,
  decodeToAudioBuffer,
  type Ac3GoApi,
  type Ac3Input,
  type DecodeOptions,
  type LoadOptions,
  type ProbeResult,
} from './ac3go'

/** Load the wasm module once (shared across callers); null until ready. */
const api = shallowRef<Ac3GoApi | null>(null)
let started = false

export function useAc3Go(options: LoadOptions): Ref<Ac3GoApi | null> {
  if (!started) {
    started = true
    loadAc3Go(options).then((m) => (api.value = m))
  }
  return api
}

/** Probe an input's first frame; re-probes when the input changes. */
export function useProbe(input: Ref<Ac3Input | null>) {
  const probe = ref<ProbeResult | null>(null)
  const error = ref<Error | null>(null)
  watch(
    [api, input],
    ([ac3, i], _prev, onCleanup) => {
      probe.value = null
      error.value = null
      if (!ac3 || !i) return
      let live = true
      onCleanup(() => (live = false))
      ac3
        .probe(i)
        .then((p) => live && (probe.value = p))
        .catch((e) => live && (error.value = e))
    },
    { immediate: true },
  )
  return { probe, error }
}

/**
 * Decode an input to a Web Audio AudioBuffer, ready to play. Re-decodes when
 * the input or the options change; an AudioContext is created lazily and
 * reused.
 */
export function useAc3Buffer(input: Ref<Ac3Input | null>, options?: DecodeOptions) {
  const buffer = ref<AudioBuffer | null>(null)
  const error = ref<Error | null>(null)
  let ctx: AudioContext | null = null
  watch(
    [api, input],
    ([ac3, i], _prev, onCleanup) => {
      buffer.value = null
      error.value = null
      if (!ac3 || !i) return
      let live = true
      onCleanup(() => (live = false))
      ctx ??= new AudioContext()
      ac3
        .decode(i, options)
        .then((pcm) => live && (buffer.value = decodeToAudioBuffer(pcm, ctx!)))
        .catch((e) => live && (error.value = e))
    },
    { immediate: true },
  )
  return { buffer, error }
}

/**
 * Play a decoded input through Web Audio, optionally holding it in sync with a
 * <video> element's clock. Exposes `play`/`stop` and the live drift in
 * milliseconds. Cleans up its audio graph on scope dispose.
 */
export function useAc3Player(
  input: Ref<Ac3Input | null>,
  video?: Ref<HTMLVideoElement | null>,
  options?: DecodeOptions,
) {
  const { buffer, error } = useAc3Buffer(input, options)
  const drift = ref(0)
  let ctx: AudioContext | null = null
  let src: AudioBufferSourceNode | null = null
  let raf = 0

  function stop() {
    cancelAnimationFrame(raf)
    src?.stop()
    src = null
  }

  async function play() {
    if (!buffer.value) return
    stop()
    ctx ??= new AudioContext()
    await ctx.resume()
    src = ctx.createBufferSource()
    src.buffer = buffer.value
    src.connect(ctx.destination)
    const startCtx = ctx.currentTime + 0.1
    const el = video?.value
    if (el) {
      el.currentTime = 0
      await el.play().catch(() => {})
    }
    src.start(startCtx)
    const tick = () => {
      if (!src) return
      const audioTime = ctx!.currentTime - startCtx
      const videoTime = el ? el.currentTime : audioTime
      drift.value = (audioTime - videoTime) * 1000
      raf = requestAnimationFrame(tick)
    }
    raf = requestAnimationFrame(tick)
  }

  onScopeDispose(stop)
  return { play, stop, drift, buffer, error }
}
