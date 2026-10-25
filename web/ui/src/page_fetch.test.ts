import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

type Listener = (msg: unknown) => void

const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// ── Fixture helpers ─────────────────────────────────────────────────────
// Every history frame dispatched after a request echoes the nonce read back
// from `sent` (the server does the same); the wrong-nonce cases add 1.

function historyRequests(): { type: string; cursor: string; limit: number; nonce: number }[] {
  return sent.filter((m) => (m as { type?: string }).type === 'history_request') as {
    type: string
    cursor: string
    limit: number
    nonce: number
  }[]
}

function lastHistoryRequestNonce(): number {
  const reqs = historyRequests()
  if (reqs.length === 0) throw new Error('no history_request sent yet')
  const n = reqs[reqs.length - 1].nonce
  if (typeof n !== 'number' || n < 1) throw new Error(`bad nonce ${n}`)
  return n
}

// Each metadata frame gets a fresh session id by default: the page0 cache
// is module-level and survives across tests, and a repeated (engine,
// session, rev) triple would short-circuit instead of requesting. Tests
// that exercise the cache pass one explicit session for every call.
let sessionSeq = 0

function sendMetadata(over: Record<string, unknown> = {}, session?: string) {
  dispatch({
    type: 'metadata',
    connect: { connected: true, engineID: 'e1', sessionID: session ?? `s${++sessionSeq}` },
    config: { models: [], current: { provider: 'p', model: 'm' } },
    engines: { engines: [], activeID: 'e1' },
    stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    ...over,
  })
}

function sendPage(over: Record<string, unknown> = {}, nonce?: number) {
  dispatch({
    type: 'history',
    messages: [],
    nextCursor: '',
    hasMore: false,
    ...(nonce !== undefined ? { nonce } : {}),
    ...over,
  })
}

// Boots a full round trip: metadata → request → page. Returns the echoed
// nonce. The page fixture carries 2 committed rows and a follow-up cursor so
// jsdom's immediate prefetch fires (V16 relies on that).
function bootPaged(over: Record<string, unknown> = {}, page: Record<string, unknown> = {}, session?: string): number {
  sendMetadata(over, session)
  const n = lastHistoryRequestNonce()
  sendPage(
    {
      rev: 'r1',
      messages: [
        {
          id: 'u1', role: 'user', text: 'hello-q', thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1000,
        },
        {
          id: 'a1', role: 'assistant', text: 'hello-a', thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1001,
        },
      ],
      nextCursor: 'a:2:0:7',
      hasMore: true,
      seq: 100,
      ...page,
    },
    n,
  )
  return n
}

function bodyText(): string {
  return document.body.textContent ?? ''
}

function occurrences(needle: string): number {
  return bodyText().split(needle).length - 1
}

function messageOrderIndex(needle: string): number {
  const container = document.querySelector('.space-y-7') as HTMLElement
  const children = Array.from(container.children)
  const idx = children.findIndex((c) => (c.textContent ?? '').includes(needle))
  if (idx < 0) throw new Error(`no message child contains ${needle}`)
  return idx
}

function streamContent(): HTMLElement {
  const containers = document.querySelectorAll('.space-y-3')
  // The streaming shell is the last .space-y-3 (appended after committed).
  return containers[containers.length - 1] as HTMLElement
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('page fetch: cold start (V13)', () => {
  it('metadata triggers the page request; the page renders without extra shells; prefetch follows', () => {
    mount()
    sendMetadata({ rev: 'r1' })

    // Request order: session list first, then the tail page.
    expect((sent[0] as { type: string }).type).toBe('session_list_request')
    expect(sent[1]).toMatchObject({ type: 'history_request', cursor: '', limit: 30 })
    const bootReq = sent[1] as { nonce: number }
    expect(typeof bootReq.nonce).toBe('number')
    expect(bootReq.nonce).toBeGreaterThanOrEqual(1)

    sendPage(
      {
        rev: 'r1',
        messages: [
          {
            id: 'u1', role: 'user', text: 'hello-q', thinking: [], tools: [],
            usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1000,
          },
          {
            id: 'a1', role: 'assistant', text: 'hello-a', thinking: [], tools: [],
            usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1001,
          },
        ],
        nextCursor: 'a:2:0:7',
        hasMore: true,
        seq: 100,
      },
      bootReq.nonce,
    )

    expect(bodyText()).toContain('hello-q')
    expect(bodyText()).toContain('hello-a')
    // Non-busy page without a stream window: committed rows only — no
    // streaming shell may be created.
    expect(document.querySelectorAll('.space-y-3').length).toBe(1)

    // jsdom prefetch: the anchor cursor is echoed back verbatim with a fresh
    // random nonce.
    const reqs = historyRequests()
    expect(reqs[reqs.length - 1]).toMatchObject({ type: 'history_request', cursor: 'a:2:0:7', limit: 30 })
    expect(typeof reqs[reqs.length - 1].nonce).toBe('number')
    expect(reqs[reqs.length - 1].nonce).toBeGreaterThanOrEqual(1)
  })
})

describe('page fetch: rev short-circuit (V14/V15)', () => {
  it('same rev re-applies the cached page with zero requests; busy skips the cache', () => {
    mount()
    // No hasMore on the fixture page: a cached replay with a follow-up
    // cursor legitimately re-fires prefetch, which is not a page0 request.
    bootPaged({}, { nextCursor: '', hasMore: false }, 'cache-test')
    const reqsBefore = historyRequests().length
    expect(reqsBefore).toBe(1)

    sendMetadata({ rev: 'r1' }, 'cache-test')
    expect(historyRequests().length).toBe(reqsBefore)
    expect(bodyText()).toContain('hello-q')
    expect(bodyText()).toContain('hello-a')

    // A busy engine's tail page is stale the moment it is minted — the
    // short-circuit must not apply and the page must not enter the cache.
    sendMetadata({ rev: 'r1', busy: true }, 'cache-test')
    expect(historyRequests().length).toBe(reqsBefore + 1)
  })

  it('changed rev re-fetches; the cache key is engine-scoped', () => {
    mount()
    bootPaged({}, { nextCursor: '', hasMore: false }, 'cache-test')
    const reqsBefore = historyRequests().length
    expect(reqsBefore).toBe(1)

    sendMetadata({ rev: 'r2' }, 'cache-test')
    expect(historyRequests().length).toBe(reqsBefore + 1)

    // Same session, same rev, different engine: a different key.
    sendMetadata({ connect: { connected: true, engineID: 'e2', sessionID: 'cache-test' }, rev: 'r1' })
    expect(historyRequests().length).toBe(reqsBefore + 2)
  })
})

describe('page fetch: stale nonce gate (V16)', () => {
  it('pending page0: wrong-nonce frame dropped whole, right-nonce frame still applies', () => {
    mount()
    sendMetadata({ rev: 'r1' })
    const n = lastHistoryRequestNonce()
    const messages = [
      {
        id: 'u1', role: 'user', text: 'nonce-msg', thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1000,
      },
    ]

    // Wrong nonce: a page minted for someone else's request.
    sendPage({ rev: 'r1', messages, seq: 100 }, n + 1)
    expect(occurrences('nonce-msg')).toBe(0)

    // The drop must not have consumed pendingPageZero or poisoned the state.
    sendPage({ rev: 'r1', messages, seq: 100 }, n)
    expect(occurrences('nonce-msg')).toBe(1)
  })

  it('non-pending prefetch: wrong-nonce frame dropped, DOM unchanged', () => {
    mount()
    // hasMore + cursor make jsdom prefetch right after the page lands, so
    // the expected nonce is the prefetch's.
    bootPaged()
    const m = lastHistoryRequestNonce()

    sendPage(
      {
        messages: [
          {
            id: 'old1', role: 'user', text: 'stale-old-msg', thinking: [], tools: [],
            usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 900,
          },
        ],
      },
      m + 1,
    )
    expect(occurrences('stale-old-msg')).toBe(0)
    expect(occurrences('hello-q')).toBe(1)
  })

  it('frames without a nonce always pass (undefined-echo rule)', () => {
    mount()
    bootPaged({ busy: true }, { nextCursor: '', hasMore: false })
    // No prefetch fired (hasMore false), so expected still equals the boot
    // nonce — but a nonce-ABSENT frame must pass regardless.
    sendPage({
      messages: [
        {
          id: 'legacy', role: 'user', text: 'legacy-no-nonce', thinking: [], tools: [],
          usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 900,
        },
      ],
    })
    expect(occurrences('legacy-no-nonce')).toBe(1)
  })
})

// V17 fixtures: one committed user row + an in-flight window holding a text
// block ('xx') and a running tool (t1).
function v17Page(): Record<string, unknown> {
  return {
    busy: true,
    rev: undefined,
    messages: [
      {
        id: 'cq', role: 'user', text: 'committed-query', thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1000,
      },
    ],
    stream: [
      { kind: 'text', id: '', text: 'xx' },
      {
        kind: 'tool', id: 't1', name: 'Bash', summary: 'run tests', state: 'running',
        startedAt: 1000, children: [],
      },
    ],
    nextCursor: '',
    hasMore: false,
    seq: 100,
  }
}

function bootV17(): void {
  mount()
  sendMetadata({ busy: true })
  const n = lastHistoryRequestNonce()
  dispatch({ type: 'event', event: { type: 'text_delta', text: 'xx' }, seq: 98 })
  dispatch({ type: 'event', event: { type: 'text_delta', text: 'yy' }, seq: 102 })
  // Mid-window DOM check: nothing may render before the page lands.
  if (bodyText().includes('xx') || bodyText().includes('yy')) {
    throw new Error('buffering violated: event rendered before page0')
  }
  sendPage(v17Page(), n)
}

describe('page fetch: mixed page + buffering (V17)', () => {
  it('buffers events across the page window; seq sorts absorbed vs live', () => {
    bootV17()

    // Committed rows sit above the streaming shell in DOM order.
    expect(messageOrderIndex('committed-query')).toBeLessThan(messageOrderIndex('xx'))

    // 'xx': in the page's stream window exactly once — the buffered seq-98
    // frame (same text) must be dropped as absorbed.
    expect(occurrences('xx')).toBe(1)
    // 'yy': numbered above the page — replayed and rendered.
    expect(occurrences('yy')).toBe(1)
    // Exactly one tool root for t1.
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(1)

    // Live completion lands in place on the registered tool. The folded body
    // buffers output render-on-visible, so expanding the row flushes it.
    dispatch({
      type: 'event',
      event: { type: 'tool_output_delta', tool_result: { tool_use_id: 't1', display_output: 'out1' } },
      seq: 103,
    })
    dispatch({
      type: 'event',
      event: { type: 'tool_end', tool_result: { tool_use_id: 't1', display_output: 'out1' } },
      seq: 104,
    })
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(1)
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    expect(bodyText()).toContain('out1')
  })
})

describe('page fetch: running-tool shell uniqueness (V17b)', () => {
  it('running tool in committed rows + non-empty stream window: one shell, one tool root', () => {
    mount()
    sendMetadata({ busy: true })
    const n = lastHistoryRequestNonce()
    sendPage(
      {
        busy: true,
        messages: [
          {
            id: 'a1', role: 'assistant', startedAt: 1000, text: '', thinking: [], tools: [],
            blocks: [
              { kind: 'text', text: 'partial work' },
              { kind: 'tool', tool: { id: 'rt1', name: 'Bash', summary: 'long job', isRunning: true } },
            ],
            usage: { inputTokens: 0, outputTokens: 0 },
          },
        ],
        stream: [{ kind: 'text', id: '', text: 'more in flight' }],
        nextCursor: '',
        hasMore: false,
        seq: 100,
      },
      n,
    )
    expect(document.querySelectorAll('.space-y-3').length).toBe(1)
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(1)
    expect(bodyText()).toContain('partial work')
    expect(bodyText()).toContain('more in flight')
  })

  it('busy page without a stream window: the busy flag alone builds exactly one shell', () => {
    mount()
    sendMetadata({ busy: true })
    const n = lastHistoryRequestNonce()
    sendPage(
      {
        busy: true,
        messages: [
          {
            id: 'a1', role: 'assistant', startedAt: 1000, text: '', thinking: [], tools: [],
            blocks: [
              { kind: 'text', text: 'prefill partial' },
              { kind: 'tool', tool: { id: 'rt2', name: 'Bash', summary: 'prefill job', isRunning: true } },
            ],
            usage: { inputTokens: 0, outputTokens: 0 },
          },
        ],
        nextCursor: '',
        hasMore: false,
        seq: 100,
      },
      n,
    )
    expect(document.querySelectorAll('.space-y-3').length).toBe(1)
    expect(document.querySelectorAll('[data-tool-root]').length).toBe(1)
  })
})

describe('page fetch: prefetch into the stream region (V18)', () => {
  it('older stream blocks prepend above the live window content', () => {
    bootV17()
    // jsdom fired no prefetch (hasMore false); dispatch one manually with
    // the expected nonce.
    const m = lastHistoryRequestNonce()
    sendPage(
      {
        stream: [{ kind: 'text', id: '', text: 'older-window-text' }],
      },
      m,
    )
    const content = streamContent()
    const children = Array.from(content.children)
    const olderIdx = children.findIndex((c) => (c.textContent ?? '').includes('older-window-text'))
    const xxIdx = children.findIndex((c) => (c.textContent ?? '').includes('xx'))
    expect(olderIdx).toBeGreaterThanOrEqual(0)
    expect(xxIdx).toBeGreaterThan(olderIdx)
    expect(occurrences('older-window-text')).toBe(1)
  })
})

describe('page fetch: prefill shell (V19)', () => {
  it('busy page with no stream window builds the empty shell; deltas flow into it', () => {
    mount()
    sendMetadata({ busy: true })
    const n = lastHistoryRequestNonce()
    sendPage(
      {
        busy: true,
        messages: [
          {
            id: 'q1', role: 'user', text: 'just sent', thinking: [], tools: [],
            usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 1000,
          },
        ],
        nextCursor: '',
        hasMore: false,
        seq: 100,
      },
      n,
    )
    // Progress area (the shell's own streaming bar) exists in the DOM.
    expect(document.querySelector('.status-line')).toBeTruthy()
    // A live delta renders into the shell.
    dispatch({ type: 'event', event: { type: 'text_delta', text: 'first tokens' }, seq: 101 })
    expect(occurrences('first tokens')).toBe(1)
  })
})

describe('page fetch: empty session (V20)', () => {
  it('empty page renders nothing and throws nothing; same rev re-applies with zero requests', () => {
    mount()
    sendMetadata({ rev: '0:' }, 'empty-session')
    const n = lastHistoryRequestNonce()
    sendPage({ rev: '0:', messages: [], nextCursor: '', hasMore: false, seq: 1 }, n)
    expect(document.querySelectorAll('.space-y-3').length).toBe(0)

    const reqs = historyRequests().length
    sendMetadata({ rev: '0:' }, 'empty-session')
    expect(historyRequests().length).toBe(reqs)
  })
})

describe('page fetch: commit transition (V21)', () => {
  it('query_end preserves rendered content; an older prefetch page adds no duplicates', () => {
    bootV17()
    dispatch({ type: 'event', event: { type: 'query_end' }, seq: 105 })
    // Already-rendered content survives the commit.
    expect(occurrences('xx')).toBe(1)
    expect(occurrences('yy')).toBe(1)
    expect(occurrences('committed-query')).toBe(1)

    // An older page (committed rows strictly before the turn) must not
    // duplicate the turn's retained content.
    const m = lastHistoryRequestNonce()
    sendPage(
      {
        messages: [
          {
            id: 'old0', role: 'user', text: 'ancient question', thinking: [], tools: [],
            usage: { inputTokens: 0, outputTokens: 0 }, error: '', status: 'done', startedAt: 900,
          },
        ],
      },
      m,
    )
    expect(occurrences('ancient question')).toBe(1)
    expect(occurrences('xx')).toBe(1)
  })
})

describe('page fetch: late-frame seq sorting (V22)', () => {
  it('seq at/below the page is dropped, above passes, absent always passes', () => {
    bootV17()

    dispatch({ type: 'event', event: { type: 'text_delta', text: 'zz' }, seq: 99 })
    expect(occurrences('zz')).toBe(0)
    expect(occurrences('xx')).toBe(1)

    dispatch({ type: 'event', event: { type: 'text_delta', text: 'ww' }, seq: 101 })
    expect(occurrences('ww')).toBe(1)

    dispatch({ type: 'event', event: { type: 'text_delta', text: 'w2' } })
    expect(occurrences('w2')).toBe(1)
  })
})
