/**
 * react.ts - copyable React hooks over the ac3go wasm module. Zero
 * dependencies beyond react and ./ac3go. See docs/wasm.md.
 */
import { useEffect, useRef, useState } from 'react'
import {
  loadAc3Go,
  decodeToAudioBuffer,
  type Ac3GoApi,
  type Ac3Input,
  type DecodeOptions,
  type LoadOptions,
  type ProbeResult,
} from './ac3go'

/** Load the wasm module once; null until ready. */
export function useAc3Go(options: LoadOptions): Ac3GoApi | null {
  const [api, setApi] = useState<Ac3GoApi | null>(null)
  const opts = useRef(options)
  useEffect(() => {
    let live = true
    loadAc3Go(opts.current).then((m) => live && setApi(m))
    return () => {
      live = false
    }
  }, [])
  return api
}

/** Probe an input's first frame; re-probes when the input changes. */
export function useProbe(ac3: Ac3GoApi | null, input: Ac3Input | null) {
  const [probe, setProbe] = useState<ProbeResult | null>(null)
  const [error, setError] = useState<Error | null>(null)
  useEffect(() => {
    setProbe(null)
    setError(null)
    if (!ac3 || !input) return
    let live = true
    ac3
      .probe(input)
      .then((p) => live && setProbe(p))
      .catch((e) => live && setError(e))
    return () => {
      live = false
    }
  }, [ac3, input])
  return { probe, error }
}

/**
 * Decode an input to a Web Audio AudioBuffer, ready to play. Re-decodes when
 * the input or the options change; an AudioContext is created lazily and
 * reused. Pass options to downmix (`{ downmix: 'stereo' }`) or to switch off
 * the decoder's noise (`{ dither: false }`).
 */
export function useAc3Buffer(ac3: Ac3GoApi | null, input: Ac3Input | null, options?: DecodeOptions) {
  const [buffer, setBuffer] = useState<AudioBuffer | null>(null)
  const [error, setError] = useState<Error | null>(null)
  const ctxRef = useRef<AudioContext | null>(null)
  const optsKey = JSON.stringify(options ?? {})
  useEffect(() => {
    setBuffer(null)
    setError(null)
    if (!ac3 || !input) return
    let live = true
    const ctx = (ctxRef.current ??= new AudioContext())
    ac3
      .decode(input, options)
      .then((pcm) => live && setBuffer(decodeToAudioBuffer(pcm, ctx)))
      .catch((e) => live && setError(e))
    return () => {
      live = false
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ac3, input, optsKey])
  return { buffer, error }
}

/**
 * Play a decoded input through Web Audio, optionally holding it in sync with a
 * <video> element's clock (the video carries the picture, ac3go the sound).
 * Returns `play`/`stop` and the current drift in milliseconds so a UI can show
 * how tight the sync is. Cleans up its audio graph on unmount.
 */
export function useAc3Player(
  ac3: Ac3GoApi | null,
  input: Ac3Input | null,
  video?: React.RefObject<HTMLVideoElement | null>,
  options?: DecodeOptions,
) {
  const { buffer, error } = useAc3Buffer(ac3, input, options)
  const [drift, setDrift] = useState(0)
  const ctxRef = useRef<AudioContext | null>(null)
  const srcRef = useRef<AudioBufferSourceNode | null>(null)
  const rafRef = useRef(0)

  useEffect(
    () => () => {
      cancelAnimationFrame(rafRef.current)
      srcRef.current?.stop()
      srcRef.current?.disconnect()
    },
    [],
  )

  function stop() {
    cancelAnimationFrame(rafRef.current)
    srcRef.current?.stop()
    srcRef.current = null
  }

  async function play() {
    if (!buffer) return
    stop()
    const ctx = (ctxRef.current ??= new AudioContext())
    await ctx.resume()
    const src = ctx.createBufferSource()
    src.buffer = buffer
    src.connect(ctx.destination)
    const startCtx = ctx.currentTime + 0.1
    const el = video?.current
    if (el) {
      el.currentTime = 0
      await el.play().catch(() => {})
    }
    src.start(startCtx)
    srcRef.current = src
    const tick = () => {
      if (!srcRef.current) return
      const audioTime = ctx.currentTime - startCtx
      const videoTime = el ? el.currentTime : audioTime
      setDrift((audioTime - videoTime) * 1000)
      rafRef.current = requestAnimationFrame(tick)
    }
    rafRef.current = requestAnimationFrame(tick)
  }

  return { play, stop, drift, ready: buffer != null, error }
}
