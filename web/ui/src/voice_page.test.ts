import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createVoicePage, VOICE_AURA_SIZE, type VoiceState } from './voice_page'
import { createEventBlockStore } from './components/event_blocks'
import { renderStreamBlock } from './components/stream_blocks'

// The page renders through the shared event→block pipeline (the controller
// writes QueryEvents into the store; the page's feed sink renders chat-standard
// rows), so every rendering test drives the store — the same object chat.ts
// hands to both sides.

function setup() {
  const store = createEventBlockStore()
  const page = createVoicePage({
    engineName: '小爪',
    level: () => 0,
    blocks: store,
    onHold: vi.fn(),
    onClose: vi.fn(),
  })
  document.body.appendChild(page.root)
  return { page, store }
}

const toolStart = (id: string, name: string, summary = '') =>
  ({ type: 'tool_start', tool_use: { id, name, input: {}, summary } })

describe('createVoicePage', () => {
  let page: ReturnType<typeof createVoicePage>

  beforeEach(() => {
    document.body.innerHTML = ''
    page = setup().page
  })

  it('renders the structural skeleton (X/stage/aura canvas/avatar/controls)', () => {
    expect(page.root.querySelector('.voice-x')).toBeTruthy()
    expect(page.root.querySelector('.voice-stage')).toBeTruthy()
    expect(page.root.querySelector('.voice-aura')).toBeTruthy() // shader field replaces halo + waveform
    expect(page.root.querySelector('.voice-halo')).toBe(null)
    expect(page.root.querySelector('.voice-wave')).toBe(null)
    expect(page.root.querySelector('.voice-avatar')).toBeTruthy()
    expect(page.root.querySelector('.voice-vcontent')).toBeTruthy()
    expect(page.root.querySelector('.voice-mic')).toBeTruthy()
    // 10-28 cut: status is visual only — no text hint element exists.
    expect(page.root.querySelector('.voice-hint')).toBe(null)
  })

  it('mounts with data-st="idle" and the chrome structure pinned', () => {
    // Fresh page: idle before any setState call.
    const fresh = setup().page
    expect(fresh.root.dataset.st).toBe('idle')
    fresh.destroy()
    // X button: the ONLY close affordance — a real button, labeled, with an
    // svg glyph (a text-only X would regress the 10-28 ruling).
    const x = page.root.querySelector('.voice-x') as HTMLElement
    expect(x.tagName).toBe('BUTTON')
    expect(x.getAttribute('aria-label')).toBe('close voice')
    expect(x.querySelector('svg')).toBeTruthy()
    // Top strip: engine label inside .voice-engine inside .voice-top.
    const top = page.root.querySelector(':scope > .voice-top') as HTMLElement
    expect(top).toBeTruthy()
    const engine = top.querySelector('.voice-engine') as HTMLElement
    expect((engine.querySelector('.span') as HTMLElement)?.className).toBe('span')
    expect(engine.textContent).toBe('小爪')
    // Stage children in order: avatar wrap, partial, vcontent.
    const stage = page.root.querySelector(':scope > .voice-stage') as HTMLElement
    expect(stage.children[0].className).toBe('voice-avatar-wrap')
    expect((stage.children[0] as HTMLElement).querySelector('.voice-aura')).toBeTruthy()
    expect((stage.children[0] as HTMLElement).querySelector('.voice-avatar')).toBeTruthy()
    expect(stage.children[1].className).toBe('voice-partial')
    expect(stage.children[2].className).toBe('voice-vcontent')
    // Controls strip under the stage.
    expect(page.root.querySelector(':scope > .voice-controls > .voice-mic')).toBeTruthy()
    // Root child order: x, top, stage, controls.
    const classes = [...page.root.children].map((c) => (c as HTMLElement).className)
    expect(classes).toEqual(['voice-x', 'voice-top', 'voice-stage', 'voice-controls'])
  })

  it('the partial transcript carries the bare hidden attribute', () => {
    // hidden="" hides; any other value would still hide but drifts from the
    // HTML boolean-attribute idiom this DOM relies on.
    const partial = page.root.querySelector('.voice-partial') as HTMLElement
    expect(partial.getAttribute('hidden')).toBe('')
    page.setPartial('hi')
    expect(partial.getAttribute('hidden')).toBe(null)
    page.setPartial('')
    expect(partial.getAttribute('hidden')).toBe('')
  })

  it('the aura canvas keeps the ≤512 WebGL backing-store discipline', () => {
    const canvas = page.root.querySelector('.voice-aura') as HTMLCanvasElement
    expect(canvas.width).toBe(VOICE_AURA_SIZE)
    expect(canvas.height).toBe(VOICE_AURA_SIZE)
    expect(VOICE_AURA_SIZE).toBeLessThanOrEqual(512)
  })

  it('initializes cleanly under jsdom zero-WebGL (no context, no crash)', () => {
    // createVoicePage in beforeEach already ran the aura path with
    // getContext('webgl') → null; reaching here means no throw. The state
    // machine still works.
    page.setState('speak')
    expect(page.root.dataset.st).toBe('speak')
  })

  it('setState switches data-st', () => {
    for (const st of ['idle', 'listen', 'wait', 'think', 'tool', 'speak'] as VoiceState[]) {
      page.setState(st)
      expect(page.root.dataset.st).toBe(st)
    }
  })

  it('mount resets the shared store so a remounted face starts empty', () => {
    const store = createEventBlockStore()
    store.apply({ type: 'thinking_start' })
    store.apply({ type: 'thinking_delta', thinking: { text: '上一回合' } })
    const p = createVoicePage({
      engineName: 'e', level: () => 0, blocks: store, onHold: vi.fn(), onClose: vi.fn(),
    })
    document.body.appendChild(p.root)
    expect(store.blocks()).toEqual([])
    expect(p.root.querySelectorAll('[data-thinking="1"]').length).toBe(0)
    p.destroy()
  })

  it('think fold: the shared pipeline streams the chat thinking block', () => {
    const { page, store } = setup()
    page.setState('think')
    store.apply({ type: 'thinking_start' })
    store.apply({ type: 'thinking_delta', thinking: { text: '先想一部分' } })
    store.apply({ type: 'thinking_delta', thinking: { text: '先想一部分，再补全推理' } })
    const wrap = page.root.querySelector('[data-thinking="1"]') as HTMLElement
    expect(wrap).toBeTruthy()
    const p = wrap.querySelector('p') as HTMLParagraphElement
    expect(p.textContent).toContain('先想一部分，再补全推理')
    expect(p.classList.contains('hidden')).toBe(false) // live while thinking
    store.apply({ type: 'thinking_end', thinking: { duration: 2_500_000_000 } })
    expect(p.classList.contains('hidden')).toBe(true) // chat auto-collapse on finish
    expect(wrap.textContent).toContain('Thought for')
  })

  it('tool rows complete: summary streams via param deltas, finish stamps the duration', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
      const { page, store } = setup()
      page.setState('tool')
      store.apply(toolStart('t1', 'web'))
      store.apply({
        type: 'tool_param_delta',
        partial_input: { id: 't1', name: 'web', delta: '{}', summary: '杭州 天气' },
      })
      const row = page.root.querySelector('[data-tool-root]') as HTMLElement
      expect(row.dataset.toolName).toBe('web')
      expect(row.textContent).toContain('杭州 天气')
      vi.setSystemTime(new Date('2026-01-01T00:00:02Z'))
      store.apply({
        type: 'tool_end',
        tool_result: { tool_use_id: 't1', output: '', display_output: 'Sunny' },
      })
      const done = page.root.querySelector('[data-tool-root]') as HTMLElement
      expect(done.dataset.toolTimingNs).toBe('2000000000')
      expect(done.textContent).toContain('2s')
      expect(done.textContent).toContain('杭州 天气')
    } finally {
      vi.useRealTimers()
    }
  })

  it('voice face: running tool row and thinking label tick live', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
      const { page, store } = setup()
      page.setState('think')
      store.apply({ type: 'thinking_start' })
      store.apply(toolStart('t1', 'web'))
      const row = page.root.querySelector('[data-tool-root]') as HTMLElement
      const wrap = page.root.querySelector('[data-thinking="1"]') as HTMLElement
      expect(row.textContent).toContain('0s')
      expect(wrap.textContent).toContain('0s')

      vi.advanceTimersByTime(1200)
      expect(row.textContent).toContain('1s')
      expect(wrap.textContent).toContain('Thinking (1s)')
    } finally {
      vi.useRealTimers()
    }
  })

  it('think stream follows: pins, releases on scroll-up, resumes near bottom', () => {
    vi.useFakeTimers()
    try {
      const { page, store } = setup()
      const vc = page.root.querySelector('.voice-vcontent') as HTMLElement
      // jsdom has no layout: stub the metrics the follow listener reads.
      Object.defineProperty(vc, 'scrollHeight', { configurable: true, get: () => 500 })
      Object.defineProperty(vc, 'clientHeight', { configurable: true, get: () => 200 })
      page.setState('think')
      store.apply({ type: 'thinking_start' })
      store.apply({ type: 'thinking_delta', thinking: { text: 'part1' } })
      // Following: the growing think stream keeps vcontent pinned to the bottom.
      expect(vc.scrollTop).toBe(500)

      // Scrolled up (500-100-200 = 200 > 40): new deltas must not yank back.
      vc.scrollTop = 100
      vc.dispatchEvent(new Event('scroll'))
      store.apply({ type: 'thinking_delta', thinking: { text: ' part2' } })
      expect(vc.scrollTop).toBe(100)

      // Back within 40px of the bottom (500-480-200 < 0): follow resumes.
      vc.scrollTop = 480
      vc.dispatchEvent(new Event('scroll'))
      store.apply({ type: 'thinking_delta', thinking: { text: ' more' } })
      expect(vc.scrollTop).toBe(500)
    } finally {
      vi.useRealTimers()
    }
  })

  it('reset re-arms following for the next think round', () => {
    vi.useFakeTimers()
    try {
      const { page, store } = setup()
      const vc = page.root.querySelector('.voice-vcontent') as HTMLElement
      Object.defineProperty(vc, 'scrollHeight', { configurable: true, get: () => 500 })
      Object.defineProperty(vc, 'clientHeight', { configurable: true, get: () => 200 })
      page.setState('think')
      // Released follow from the previous round.
      vc.scrollTop = 100
      vc.dispatchEvent(new Event('scroll'))

      page.setState('speak') // reset branch clears vcontent for new content
      page.setState('think')
      store.apply({ type: 'thinking_start' })
      store.apply({ type: 'thinking_delta', thinking: { text: '新一轮' } })
      expect(vc.scrollTop).toBe(500)
    } finally {
      vi.useRealTimers()
    }
  })

  it('tool rows are chat-isomorphic: rendered by the shared stream renderer', () => {
    const { page, store } = setup()
    page.setState('tool')
    store.apply(toolStart('t1', 'web', '杭州 天气'))
    const rows = page.root.querySelectorAll('[data-tool-root]')
    expect(rows.length).toBe(1)
    expect((rows[0] as HTMLElement).dataset.blockKind).toBe('tool')
    // Isomorphism: a reference row rendered by the shared renderer from the
    // SAME block object must be structurally identical — zero parallel
    // rendering code on the voice face.
    const scratch = document.createElement('div')
    renderStreamBlock(scratch, store.blocks()[0], null, null)
    const ref = scratch.firstElementChild as HTMLElement
    expect(rows[0].className).toBe(ref.className)
    expect(rows[0].innerHTML).toBe(ref.innerHTML)
    expect(rows[0].textContent).toContain('web')
    expect(rows[0].textContent).toContain('杭州 天气')
  })

  it('transient blocks: speak/listen/idle clear vcontent AND the store, wait keeps', () => {
    const { page, store } = setup()
    page.setState('tool')
    store.apply(toolStart('t1', 'web'))
    page.setState('think')
    store.apply({ type: 'thinking_start' })
    store.apply({ type: 'thinking_delta', thinking: { text: '推理中' } })
    expect(page.root.querySelectorAll('[data-tool-root]').length).toBe(1)
    expect(page.root.querySelectorAll('[data-thinking="1"]').length).toBe(1)
    page.setState('speak')
    expect(page.root.querySelectorAll('[data-tool-root]').length).toBe(0)
    expect(page.root.querySelectorAll('[data-thinking="1"]').length).toBe(0)
    // The DOM clear rides a store reset: new events start from empty truth,
    // not from blocks the cleared DOM no longer shows.
    expect(store.blocks()).toEqual([])
    page.setState('wait')
    store.apply({ type: 'thinking_start' })
    page.setState('idle')
    expect(page.root.querySelectorAll('[data-thinking="1"]').length).toBe(0)
    expect(store.blocks()).toEqual([])
  })

  it('partial transcript shown and cleared', () => {
    page.setPartial('你好')
    const el = page.root.querySelector('.voice-partial') as HTMLElement
    expect(el.textContent).toBe('你好')
    expect(el.hasAttribute('hidden')).toBe(false)
    page.setPartial('')
    expect(el.hasAttribute('hidden')).toBe(true)
  })

  it('X click fires onClose', () => {
    const onClose = vi.fn()
    document.body.innerHTML = ''
    const p = createVoicePage({ engineName: 'e', level: () => 0, blocks: createEventBlockStore(), onHold: vi.fn(), onClose })
    ;(p.root.querySelector('.voice-x') as HTMLElement).click()
    expect(onClose).toHaveBeenCalledTimes(1)
    p.destroy()
  })

  it('big mic PTT: hold/release callbacks; ignored in speak state', () => {
    const onHold = vi.fn()
    document.body.innerHTML = ''
    const p = createVoicePage({ engineName: 'e', level: () => 0, blocks: createEventBlockStore(), onHold, onClose: vi.fn() })
    document.body.appendChild(p.root)
    const mic = p.root.querySelector('.voice-mic') as HTMLElement
    // Energy-orb structure (ptt-preview .micA): press ring + white mic icon.
    expect(mic.querySelector('.ring')).toBeTruthy()
    expect((mic.querySelector('svg') as SVGElement).getAttribute('stroke')).toBe('#fff')
    p.setState('idle')
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    expect(onHold).toHaveBeenCalledWith(true)
    mic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    expect(onHold).toHaveBeenCalledWith(false)
    // speak: no barge-in — holds are suppressed.
    onHold.mockClear()
    p.setState('speak')
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    mic.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    expect(onHold).not.toHaveBeenCalled()
    p.destroy()
  })

  it('big mic PTT: finger slid off the button releases the turn', () => {
    const onHold = vi.fn()
    document.body.innerHTML = ''
    const p = createVoicePage({ engineName: 'e', level: () => 0, blocks: createEventBlockStore(), onHold, onClose: vi.fn() })
    document.body.appendChild(p.root)
    const mic = p.root.querySelector('.voice-mic') as HTMLElement
    mic.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    expect(onHold).toHaveBeenCalledWith(true)
    mic.dispatchEvent(new MouseEvent('pointerleave', { bubbles: true }))
    expect(onHold).toHaveBeenCalledWith(false)
    p.destroy()
  })

  it('destroy removes the root and detaches the store subscription', () => {
    const { page, store } = setup()
    expect(document.body.contains(page.root)).toBe(true)
    page.destroy()
    expect(document.body.contains(page.root)).toBe(false)
    // Events after destroy must not touch the detached DOM (or throw).
    expect(() => store.apply({ type: 'thinking_start' })).not.toThrow()
    expect(page.root.querySelectorAll('[data-thinking="1"]').length).toBe(0)
  })

  it('setState(listen) resets the transient blocks like speak/idle do', () => {
    const { page, store } = setup()
    page.setState('tool')
    store.apply(toolStart('t1', 'web'))
    expect(page.root.querySelectorAll('[data-tool-root]').length).toBe(1)
    page.setState('listen')
    expect(page.root.querySelectorAll('[data-tool-root]').length).toBe(0)
    expect(store.blocks()).toEqual([])
  })

  it('mic pointerdown prevents default so the browser never synthesizes a tap', () => {
    const mic = page.root.querySelector('.voice-mic') as HTMLElement
    const e = new MouseEvent('pointerdown', { bubbles: true, cancelable: true })
    mic.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
  })

  it('pointerleave in speak state never releases a hold (no barge-in)', () => {
    const onHold = vi.fn()
    document.body.innerHTML = ''
    const p = createVoicePage({ engineName: 'e', level: () => 0, blocks: createEventBlockStore(), onHold, onClose: vi.fn() })
    document.body.appendChild(p.root)
    const mic = p.root.querySelector('.voice-mic') as HTMLElement
    p.setState('speak')
    mic.dispatchEvent(new MouseEvent('pointerleave', { bubbles: true }))
    expect(onHold).not.toHaveBeenCalled()
    p.destroy()
  })

  it('an isolated tool_end stamps the duration without any param delta', () => {
    vi.useFakeTimers()
    try {
      vi.setSystemTime(new Date('2026-01-01T00:00:00Z'))
      const { page, store } = setup()
      page.setState('tool')
      store.apply(toolStart('t1', 'web'))
      vi.setSystemTime(new Date('2026-01-01T00:00:03Z'))
      store.apply({
        type: 'tool_end',
        tool_result: { tool_use_id: 't1', output: '', display_output: 'done' },
      })
      const row = page.root.querySelector('[data-tool-root]') as HTMLElement
      // tool_end alone must reach the patchTool path: duration stamped with
      // no param delta ever arriving.
      expect(row.dataset.toolTimingNs).toBe('3000000000')
      expect(row.textContent).toContain('3s')
      // Render-on-visible: the settled body folds, so expand to see the
      // final output the finish path stored.
      ;(row.querySelector('[data-tool-header]') as HTMLElement).click()
      expect(row.textContent).toContain('done')
    } finally {
      vi.useRealTimers()
    }
  })

  it('an isolated tool_output_delta streams its output into the running row', () => {
    const { page, store } = setup()
    page.setState('tool')
    store.apply(toolStart('t1', 'web'))
    store.apply({
      type: 'tool_output_delta',
      tool_result: { tool_use_id: 't1', output: '', display_output: 'streaming output' },
    })
    const row = page.root.querySelector('[data-tool-root]') as HTMLElement
    // Still exactly one tool row: no duplicate block appended.
    expect(page.root.querySelectorAll('[data-tool-root]').length).toBe(1)
    // tool_output_delta routes through patchTool, which parks the output as
    // pending behind the folded body; expanding reveals it. A root sync
    // instead leaves the running row's pending slot empty.
    ;(row.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(row.textContent).toContain('streaming output')
  })
})
