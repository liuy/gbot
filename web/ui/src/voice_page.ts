// Voice page view: full-screen overlay — status core (avatar + aura
// shader), transient think/tool blocks, PTT mic. Pure view: state arrives
// via setState (data-st drives all CSS), gestures leave via callbacks. The
// aura canvas replaces the old halo + waveform pair as the single audio
// visualizer (user ruling: the shader is the only waveform carrier), fed by
// the controller's live RMS through the page's ~33fps rAF loop. Status is
// visual only (aura field + mic shape, 10-28 ruling: no text hints). The
// think/tool rows render through the shared event→block pipeline's feed
// sink — the exact chat-standard DOM the task panel uses (10-30 ruling:
// zero parallel rendering code on the voice face). The X button is the
// ONLY close affordance (10-28 ruling).
import { createFeedSink, type FeedSinkHandle } from './components/stream_sink'
import type { EventBlockChange, EventBlockStore } from './components/event_blocks'
import { createAuraVisual } from './voice_aura'

export type VoiceState = 'idle' | 'listen' | 'wait' | 'think' | 'tool' | 'speak'

// ≤512 backing store (aura WebGL discipline); CSS scales the square field
// around the avatar.
export const VOICE_AURA_SIZE = 512

export interface VoicePageOpts {
  engineName: string
  // Live audio level [0,1] (RMS — mic while listening, TTS while speaking):
  // the aura's volume-driven states read it once per frame.
  level(): number
  // Shared event→Block store: the controller feeds engine events in, the
  // page renders them through the feed sink. speak/listen/idle entries reset
  // it together with the DOM — the transient blocks' lifecycle.
  blocks: EventBlockStore
  onHold(held: boolean): void
  onClose(): void
}

export interface VoicePage {
  root: HTMLElement
  setState(s: VoiceState): void
  setPartial(text: string): void
  destroy(): void
}

const el = (cls: string, tag = 'div'): HTMLElement => {
  const n = document.createElement(tag)
  n.className = cls
  return n
}

export function createVoicePage(opts: VoicePageOpts): VoicePage {
  const root = el('voice-page')
  root.dataset.st = 'idle'

  const x = el('voice-x', 'button')
  x.setAttribute('aria-label', 'close voice')
  x.innerHTML =
    '<svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M6 6l12 12M18 6L6 18"/></svg>'
  x.addEventListener('click', () => opts.onClose())

  const top = el('voice-top')
  const engine = el('voice-engine')
  const engineLabel = el('span')
  engineLabel.textContent = opts.engineName
  engine.appendChild(engineLabel)
  top.appendChild(engine)

  const stage = el('voice-stage')
  const wrap = el('voice-avatar-wrap')
  const auraCanvas = document.createElement('canvas')
  auraCanvas.className = 'voice-aura'
  auraCanvas.width = VOICE_AURA_SIZE
  auraCanvas.height = VOICE_AURA_SIZE
  const avatar = el('voice-avatar')
  wrap.appendChild(auraCanvas)
  wrap.appendChild(avatar)

  const partial = el('voice-partial')
  partial.setAttribute('hidden', '')

  const vcontent = el('voice-vcontent')
  stage.appendChild(wrap)
  stage.appendChild(partial)
  stage.appendChild(vcontent)

  const controls = el('voice-controls')
  const mic = el('voice-mic')
  // Filled energy orb (ptt-preview .micA): white outline mic + press ring.
  mic.innerHTML =
    '<span class="ring"></span><svg viewBox="0 0 24 24" fill="none" stroke="#fff" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><rect x="9" y="3" width="6" height="11" rx="3"/><path d="M5 11a7 7 0 0 0 14 0M12 18v3"/></svg>'
  controls.appendChild(mic)

  root.appendChild(x)
  root.appendChild(top)
  root.appendChild(stage)
  root.appendChild(controls)

  let state: VoiceState = 'idle'

  // Store change → sink call, the voice-sized mirror of jobs_render's dirty
  // records: tool events patch their card, every other event appends at the
  // feed root (the controller consumes the root stream only — no nesting).
  // While a hydration is in flight the sink defers writes, so records
  // survive and one post-onDone flush reconciles.
  const sink: FeedSinkHandle = createFeedSink({
    feedEl: vcontent,
    blocks: () => opts.blocks.blocks(),
    follow: () => true,
  })
  const dirtyTools = new Set<string>()
  let dirtyRoot = false
  const flush = (): void => {
    if (sink.pending()) return
    if (dirtyRoot) sink.syncTail(null)
    for (const id of dirtyTools) sink.patchTool(id)
    dirtyRoot = false
    dirtyTools.clear()
  }
  const unsubBlocks = opts.blocks.onChange((c: EventBlockChange): void => {
    if (c.kind === 'reset') {
      // speak/listen/idle entry: the transient blocks' job is over — drop
      // the DOM and the pipeline truth together (hydrate renders the now
      // empty store over a cleared container).
      dirtyRoot = false
      dirtyTools.clear()
      sink.dispose()
      sink.hydrate(flush)
      return
    }
    const e = c.e
    if (e.type === 'tool_param_delta') {
      if (e.partial_input?.id) dirtyTools.add(e.partial_input.id)
    } else if (e.type === 'tool_output_delta' || e.type === 'tool_end') {
      if (e.tool_result?.tool_use_id) dirtyTools.add(e.tool_result.tool_use_id)
    } else {
      dirtyRoot = true
    }
    flush()
  })

  const setState = (s: VoiceState): void => {
    state = s
    root.dataset.st = s
    // Transient block lifecycle: speak/listen/idle clear the think/tool
    // rows — their job ends when the answer starts.
    if (s === 'speak' || s === 'listen' || s === 'idle') opts.blocks.reset()
  }

  setState('idle')

  // PTT on the big mic: press = listen, release = send. Suppressed in speak
  // (no barge-in, v1 ruling).
  mic.addEventListener('pointerdown', (e) => {
    e.preventDefault()
    if (state === 'speak') return
    opts.onHold(true)
  })
  mic.addEventListener('pointerup', () => {
    if (state === 'speak') return
    opts.onHold(false)
  })
  mic.addEventListener('pointerleave', () => {
    // Finger slid off the button: treat as release so the turn is not stuck
    // in listen forever.
    if (state === 'speak') return
    opts.onHold(false)
  })

  // Aura draw loop: the page owns the ~33fps throttle; a detached root (a
  // test resetting the DOM, or destroy) ends the chain.
  const aura = createAuraVisual(auraCanvas)
  let raf = 0
  let lastR = 0
  const draw = (now: number): void => {
    if (!root.isConnected) return
    raf = requestAnimationFrame(draw)
    if (now - lastR < 30) return // ~33fps 封顶：有机流动不需要 60fps，GPU 负载减半
    lastR = now
    aura.frame(now, state, opts.level())
  }
  raf = requestAnimationFrame(draw)

  return {
    root,
    setState,
    setPartial(text: string): void {
      if (text) {
        partial.textContent = text
        partial.removeAttribute('hidden')
      } else {
        partial.setAttribute('hidden', '')
      }
    },
    destroy(): void {
      unsubBlocks()
      sink.dispose()
      cancelAnimationFrame(raf)
      aura.destroy()
      root.remove()
    },
  }
}
