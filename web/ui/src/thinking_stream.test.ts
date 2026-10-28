// Threshold-hybrid thinking stream integration tests (chat full chain).
// Harness copied from streaming_dom_isolation.test.ts; renderMarkdown is
// spied (passthrough) so every render in the module graph — chat,
// stream_dom, stream_blocks — is counted.
import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat } from './chat'
import { renderMarkdown } from './markdown'
import { THINKING_RENDER_LIMIT } from './components/stream_dom'

class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

vi.mock('./markdown', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./markdown')>()
  return { ...actual, renderMarkdown: vi.fn(actual.renderMarkdown) }
})

type Listener = (msg: unknown) => void
const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => sent.push(p),
    connected: true,
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}
function events(es: unknown[]) {
  for (const e of es) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

function startQuery() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = 'q'
  ta.dispatchEvent(new Event('input', { bubbles: true }))
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
  events([{ type: 'query_start' }])
  vi.mocked(renderMarkdown).mockClear()
}

const renderCount = () => vi.mocked(renderMarkdown).mock.calls.length

// One delta's worth of accumulated text: bold + inline code + table row in
// the head (always intact), padded to EXACTLY `size` chars with prose. ASCII
// only — no surrogate-pair cuts when slicing.
function chunk(i: number, size: number): string {
  const head = `d${i} reasoning **bold${i}** and \`code${i}\` plus row | a${i} | b${i} |`
  const filler =
    'the streaming renderer keeps the accumulated text and appends a plain tail span once the freeze threshold is crossed'
  let s = head
  while (s.length < size) s += ' ' + filler
  return s.slice(0, size)
}

function thinkingWrap(): HTMLElement {
  return document.querySelector('[data-thinking]') as HTMLElement
}
function thinkingP(): HTMLParagraphElement {
  return document.querySelector('[data-thinking] p') as HTMLParagraphElement
}
// The wrap is [header, p]; the header is [prefix(glyph+chevron), label] — the
// label is the header's last child (the glyph span also carries text-amber).
function labelOf(wrap: HTMLElement): string {
  const header = wrap.firstElementChild as HTMLElement
  return header.lastElementChild?.textContent ?? ''
}

const AGENT = { parent_tool_use_id: 'a-1', agent_type: 'sub', depth: 1 }

// Streams 50 x 400-char deltas (total 20000 chars): the first 40 (16000 <=
// limit) render in full, the rest freeze. Returns the state the finalize
// tests start from.
function streamFrozen() {
  startQuery()
  const deltas = Array.from({ length: 50 }, (_, i) => chunk(i, 400))
  events([{ type: 'thinking_start' }])
  for (let i = 0; i < 40; i++) {
    events([{ type: 'thinking_delta', thinking: { text: deltas[i] } }])
  }
  const firstChild = thinkingP().children[0] as HTMLElement
  for (let i = 40; i < 50; i++) {
    events([{ type: 'thinking_delta', thinking: { text: deltas[i] } }])
  }
  return { deltas, full: deltas.join(''), firstChild }
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

afterEach(() => {
  vi.useRealTimers()
})

describe('thinking stream — threshold hybrid', () => {
  it('within threshold renders markdown per delta and end adds no render (#1)', () => {
    mount()
    startQuery()
    const deltas = Array.from({ length: 30 }, (_, i) => chunk(i, 300))
    events([{ type: 'thinking_start' }, ...deltas.map((t) => ({ type: 'thinking_delta', thinking: { text: t } }))])
    expect(renderCount()).toBe(30)
    const p = thinkingP()
    expect(p.querySelector('strong')?.textContent).toBe('bold0')
    expect(p.querySelector('[data-think-tail]')).toBeNull()

    events([{ type: 'thinking_end' }])
    expect(renderCount()).toBe(30)
    const wrap = thinkingWrap()
    expect(labelOf(wrap)).toContain('Thought for')
    expect(p.classList.contains('hidden')).toBe(true)
    expect(wrap.querySelector('.heartbeat')).toBeNull()
  })

  it('past threshold freezes the rendered tree and appends a plain-text tail (#2)', () => {
    mount()
    const { full, firstChild } = streamFrozen()
    expect(renderCount()).toBe(40)
    const p = thinkingP()
    const span = p.querySelector('span[data-think-tail]') as HTMLSpanElement
    expect(span).not.toBeNull()
    expect(span.textContent).toBe(full.slice(16000))
    expect(p.children[0]).toBe(firstChild)
    expect(span.classList.contains('whitespace-pre-wrap')).toBe(true)
  })

  it('finalize renders the full text once and morphs the frozen prefix in place (#3)', () => {
    mount()
    const { full, firstChild } = streamFrozen()
    events([{ type: 'thinking_end' }])
    expect(renderCount()).toBe(41)
    const p = thinkingP()
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    const ref = document.createElement('p')
    ref.innerHTML = renderMarkdown(full)
    expect(p.innerHTML).toBe(ref.innerHTML)
    expect(p.children[0]).toBe(firstChild)
    const wrap = thinkingWrap()
    expect(labelOf(wrap)).toContain('Thought for')
    expect(p.classList.contains('hidden')).toBe(true)
    ;(wrap.querySelector('span[role="button"]') as HTMLElement).click()
    expect(p.classList.contains('hidden')).toBe(false)
    expect(p.querySelector('strong')?.textContent).toBe('bold0')
  })

  it('abort query_end finalizes the frozen thinking (#4)', () => {
    mount()
    streamFrozen()
    events([{ type: 'query_end', aborted: true }])
    expect(renderCount()).toBe(41)
    const wrap = thinkingWrap()
    expect(wrap.querySelector('[data-think-tail]')).toBeNull()
    expect(labelOf(wrap)).toContain('Thought for')
    expect(wrap.querySelector('p')!.classList.contains('hidden')).toBe(true)
    expect(wrap.querySelector('.heartbeat')).toBeNull()
  })

  it('non-aborted query_end finalizes the frozen thinking the same way (#4)', () => {
    mount()
    streamFrozen()
    events([{ type: 'query_end' }])
    expect(renderCount()).toBe(41)
    const wrap = thinkingWrap()
    expect(wrap.querySelector('[data-think-tail]')).toBeNull()
    expect(labelOf(wrap)).toContain('Thought for')
    expect(wrap.querySelector('.heartbeat')).toBeNull()
  })

  it('abort sweep finalizes a sub-agent thinking and freezes its tail (#4b)', () => {
    mount()
    startQuery()
    events([
      { type: 'tool_start', tool_use: { id: 'a-1', name: 'Agent' } },
      { type: 'thinking_start', agent: AGENT },
      { type: 'thinking_delta', thinking: { text: '**deep** ' + 'A'.repeat(20000) }, agent: AGENT },
      { type: 'thinking_delta', thinking: { text: '0123456789' }, agent: AGENT },
    ])
    const wrap = document.querySelector('[data-tool-children] [data-thinking]') as HTMLElement
    expect(wrap).not.toBeNull()
    const p = wrap.querySelector('p') as HTMLParagraphElement
    const span = p.querySelector('[data-think-tail]') as HTMLSpanElement
    expect(span).not.toBeNull()
    expect(span.textContent).toBe('0123456789')

    events([{ type: 'query_end', aborted: true }])
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    expect(p.querySelector('strong')?.textContent).toBe('deep')
    expect(labelOf(wrap)).toContain('Thought for')
    expect(wrap.querySelector('.heartbeat')).toBeNull()
  })

  it('empty-text abort falls back to wall-clock duration and renders nothing (#4c)', () => {
    vi.useFakeTimers()
    const t0 = 1_700_000_000_000
    vi.setSystemTime(t0)
    mount()
    startQuery()
    events([{ type: 'thinking_start' }])
    vi.setSystemTime(t0 + 2000)
    events([{ type: 'query_end', aborted: true }])
    const wrap = thinkingWrap()
    expect(labelOf(wrap)).toBe('Thought for 2s')
    expect(wrap.querySelector('.heartbeat')).toBeNull()
    const p = thinkingP()
    expect(p.classList.contains('hidden')).toBe(true)
    expect(p.children.length).toBe(0)
    expect(p.dataset.thinkLen).toBeUndefined()
  })

  it('sub-agent thinking_end finalizes normally (#5)', () => {
    mount()
    startQuery()
    events([
      { type: 'tool_start', tool_use: { id: 'a-1', name: 'Agent' } },
      { type: 'thinking_start', agent: AGENT },
      { type: 'thinking_delta', thinking: { text: '**deep** thought' }, agent: AGENT },
    ])
    const wrap = document.querySelector('[data-tool-children] [data-thinking]') as HTMLElement
    const p = wrap.querySelector('p') as HTMLParagraphElement
    expect(p.querySelector('strong')?.textContent).toBe('deep')
    events([{ type: 'thinking_end', agent: AGENT }])
    expect(labelOf(wrap)).toContain('Thought for')
    expect(p.classList.contains('hidden')).toBe(true)
  })

  it('late-mounted thinking renders markdown after the container appears (#6)', () => {
    mount()
    events([
      { type: 'thinking_start' },
      { type: 'thinking_delta', thinking: { text: 'lone **late** reasoning' } },
    ])
    vi.mocked(renderMarkdown).mockClear()
    // streamState on purpose, not query_start: query_start runs
    // cleanupStreamingRefs before initStreaming, which nulls currentThinking
    // (chat.ts), so the late-mount branch (chat.ts:1263) would never fire —
    // "fixing" this back to query_start silently untests #6.
    dispatch({ type: 'streamState', blocks: [{ kind: 'text', id: '', text: 'snapshot text' }] })
    const p = thinkingP()
    expect(p.querySelector('strong')?.textContent).toBe('late')
    events([
      { type: 'thinking_delta', thinking: { text: ' more' } },
      { type: 'thinking_end' },
    ])
    expect(p.textContent).toContain('more')
    expect(labelOf(thinkingWrap())).toContain('Thought for')
  })

  it('exactly at the limit is not over it (#7)', () => {
    mount()
    startQuery()
    events([{ type: 'thinking_start' }])
    events([{ type: 'thinking_delta', thinking: { text: 'A'.repeat(THINKING_RENDER_LIMIT) } }])
    expect(renderCount()).toBe(1)
    const p = thinkingP()
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    events([{ type: 'thinking_delta', thinking: { text: 'x' } }])
    expect(renderCount()).toBe(1)
    expect(p.querySelector('[data-think-tail]')?.textContent).toBe('x')
    events([{ type: 'thinking_end' }])
    expect(renderCount()).toBe(2)
    expect(p.querySelector('[data-think-tail]')).toBeNull()
  })

  it('frozen deltas stay under a frame budget (#8)', () => {
    mount()
    startQuery()
    events([{ type: 'thinking_start' }])
    let maxMs = 0
    for (let i = 0; i < 100; i++) {
      const t0 = performance.now()
      events([{ type: 'thinking_delta', thinking: { text: chunk(i, 400) } }])
      // Only frozen deltas count: within-threshold full renders near 16KB are
      // today's behavior and legally slow in jsdom.
      if (i >= 40) maxMs = Math.max(maxMs, performance.now() - t0)
    }
    expect(renderCount()).toBe(40)
    expect(maxMs).toBeLessThan(16)
  }, 30_000)

  it('error frame finalizes the frozen thinking (#9)', () => {
    mount()
    streamFrozen()
    dispatch({ type: 'error', message: 'boom' })
    expect(renderCount()).toBe(41)
    const wrap = thinkingWrap()
    expect(wrap.querySelector('[data-think-tail]')).toBeNull()
    expect(labelOf(wrap)).toContain('Thought for')
    const box = document.querySelector('.text-red.break-all') as HTMLElement
    expect(box.textContent).toBe('boom')
  })
})
