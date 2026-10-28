import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'
import { renderStreamBlock } from './components/stream_blocks'
import type { Block } from './model'

// jsdom lacks IntersectionObserver — stub it (same as page_fetch).
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

function bootRequestNonce(): number {
  const reqs = sent.filter((m) => (m as { type?: string }).type === 'history_request') as { nonce: number }[]
  if (reqs.length === 0) throw new Error('no history_request sent yet')
  const n = reqs[reqs.length - 1].nonce
  if (typeof n !== 'number' || n < 1) throw new Error(`bad nonce ${n}`)
  return n
}

// ── Fixtures ─────────────────────────────────────────────────────────────

function agentTool(id: string, kept: number, truncated: number, cursor: string): Record<string, unknown> {
  const children: Record<string, unknown>[] = []
  for (let i = 0; i < kept; i++) children.push({ kind: 'text', id: '', text: `${id}-orig${i}` })
  return {
    kind: 'tool', id, name: 'Agent', summary: '', state: 'running', startedAt: 1000,
    children, truncated, truncatedCursor: cursor,
  }
}

const bootStream = () => [agentTool('agent1', 30, 970, 's:970:5'), agentTool('agent0', 0, 500, 's:500:5')]

// Boots a busy page0 whose in-flight window holds agent1 (30 kept children,
// truncated 970) and agent0 (kept=0, truncated 500 — the budget-exhausted
// shape whose children container starts as an empty shell).
function bootSubtree(stream: Record<string, unknown>[] = bootStream()) {
  const chat = mount()
  sendMetadata({ busy: true })
  const n = bootRequestNonce()
  sendPage({ busy: true, stream, nextCursor: '', hasMore: false, seq: 100 }, n)
  return chat
}

// All assertions in this file are synchronous (dispatch → assert): jsdom's
// getBoundingClientRect is always 0, so any MutationObserver microtask would
// out-of-band trigger auto fetch — sync assertions are immune to that.
function containerOf(i: number): HTMLElement {
  const roots = document.querySelectorAll('[data-tool-root]')
  const root = roots[i]
  if (!root) throw new Error(`no [data-tool-root] #${i} (have ${roots.length})`)
  const c = root.querySelector(':scope > [data-tool-children]')
  if (!c) throw new Error(`tool root #${i} has no children container`)
  return c as HTMLElement
}

function subtreeRequests(): { type: string; blockID: string; anchor: string; limit: number; nonce: number }[] {
  return sent.filter((m) => (m as { type?: string }).type === 'subtree_request') as {
    type: string
    blockID: string
    anchor: string
    limit: number
    nonce: number
  }[]
}

function reqOf(blockID: string): { type: string; blockID: string; anchor: string; limit: number; nonce: number } {
  const req = subtreeRequests().filter((r) => r.blockID === blockID).at(-1)
  if (!req) throw new Error(`no subtree_request for ${blockID}`)
  return req
}

function mockNearTop(el: HTMLElement) {
  vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({ top: 0 } as DOMRect)
}

function olderText(id: string, n: number, prefix = 'older'): Record<string, unknown>[] {
  const out: Record<string, unknown>[] = []
  for (let i = 0; i < n; i++) out.push({ kind: 'text', id: '', text: `${id}-${prefix}${i}` })
  return out
}

function occurrences(needle: string): number {
  return (document.body.textContent ?? '').split(needle).length - 1
}

function sendSubtreePage(over: Record<string, unknown>): void {
  dispatch({ type: 'subtree_page', blockID: 'agent1', children: [], nextAnchor: '', hasMore: false, remaining: 0, ...over })
}

// ── C1: render-time auto fetch, including kept=0 and sink-less renders ───

describe('subtree fetch: auto trigger on render', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('fires the first page for every truncated block with no interaction', () => {
    bootSubtree()
    const reqs = subtreeRequests()
    expect(reqs.length).toBe(2)
    expect(reqs.find((r) => r.blockID === 'agent1')).toMatchObject({
      type: 'subtree_request', blockID: 'agent1', anchor: 's:970:5', limit: 30,
    })
    expect(reqs.find((r) => r.blockID === 'agent0')).toMatchObject({
      type: 'subtree_request', blockID: 'agent0', anchor: 's:500:5', limit: 30,
    })
    expect(reqs[0].nonce).toBeGreaterThanOrEqual(1)
    expect(reqs[1].nonce).toBeGreaterThanOrEqual(1)
    expect(reqs[0].nonce).not.toBe(reqs[1].nonce)
    // No button shell anywhere in the DOM.
    expect(document.querySelectorAll('[data-subtree-expand]').length).toBe(0)
    const container1 = containerOf(0)
    expect(container1.childElementCount).toBe(30)
    expect(container1.firstElementChild?.textContent).toContain('agent1-orig0')
  })

  it('renders sink-less without firing (job feeds)', () => {
    const parent = document.createElement('div')
    const before = sent.length
    renderStreamBlock(parent, agentTool('agentX', 2, 30, 's:30:5') as unknown as Block, null, null)
    expect(sent.length).toBe(before)
    const container = parent.querySelector('[data-tool-children]') as HTMLElement
    expect(container).not.toBeNull()
    expect(container.childElementCount).toBe(2)
    expect(parent.textContent).toContain('agentX-orig0')
  })
})

// ── C3: apply + anchor advance ───────────────────────────────────────────

describe('subtree fetch: apply and anchor advance', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('prepends the page above kept children; scroll re-fires at the advanced anchor', () => {
    const chat = bootSubtree()
    const first = reqOf('agent1')
    sendSubtreePage({ nonce: first.nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })

    const container = containerOf(0)
    const kids = Array.from(container.children)
    expect(kids.length).toBe(60)
    expect(kids[0].textContent).toContain('agent1-older0')
    expect(kids[29].textContent).toContain('agent1-older29')
    expect(kids[30].textContent).toContain('agent1-orig0')
    expect(kids[59].textContent).toContain('agent1-orig29')

    mockNearTop(container)
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const second = reqOf('agent1')
    expect(second.anchor).toBe('s:940:5')
    expect(second.nonce).not.toBe(first.nonce)
  })
})

// ── C4: dedupe ───────────────────────────────────────────────────────────

describe('subtree fetch: dedupe', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('drops response tools whose id already exists in the mirror', () => {
    // Original children: one tool with id dup-tool among the texts.
    const agent = agentTool('agent1', 30, 970, 's:970:5')
    ;(agent.children as Record<string, unknown>[])[0] = { kind: 'tool', id: 'dup-tool', name: 'Bash', state: 'running', children: [] }
    bootSubtree([agent])

    sendSubtreePage({
      nonce: reqOf('agent1').nonce,
      children: [...olderText('agent1', 29), { kind: 'tool', id: 'dup-tool', name: 'Bash', state: 'running', children: [] }],
      nextAnchor: 's:940:5',
      hasMore: true,
      remaining: 940,
    })

    const container = containerOf(0)
    // 29 older + 30 original (dup-tool appears once).
    expect(container.childElementCount).toBe(59)
    expect(container.querySelectorAll('[data-tool-root]').length).toBe(1)
  })
})

// ── C5: nonce reconciliation ─────────────────────────────────────────────

describe('subtree fetch: nonce reconciliation', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('unknown-nonce frame dropped whole; the right nonce still applies', () => {
    const chat = bootSubtree()
    const req = reqOf('agent1')

    sendSubtreePage({ nonce: req.nonce + 1, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(occurrences('agent1-older0')).toBe(0)

    // The in-flight entry survives the drop, so the scroll hook stays gated.
    mockNearTop(containerOf(0))
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(subtreeRequests().filter((r) => r.blockID === 'agent1').length).toBe(1)

    sendSubtreePage({ nonce: req.nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(occurrences('agent1-older0')).toBe(1)

    // Model advanced: the gate re-opens at the new anchor.
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    const second = reqOf('agent1')
    expect(second.anchor).toBe('s:940:5')
    expect(second.nonce).not.toBe(req.nonce)
  })
})

// ── C6: stale terminal ───────────────────────────────────────────────────

describe('subtree fetch: stale', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('stale frame stops all refetching; truncated stays for the tool_end fold', () => {
    const chat = bootSubtree()
    sendSubtreePage({ blockID: 'agent0', nonce: reqOf('agent0').nonce, stale: true })

    expect(occurrences('agent1-older0')).toBe(0)
    expect(occurrences('agent1-orig0')).toBe(1)

    // agent0's entry was consumed by the stale frame and its cursor cleared:
    // even at the viewport top nothing re-fires.
    mockNearTop(containerOf(0))
    mockNearTop(containerOf(1))
    const before = subtreeRequests().length
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(subtreeRequests().length - before).toBe(0)

    // truncated untouched by the stale terminal: the kept=0 block still folds.
    dispatch({ type: 'event', event: { type: 'tool_end', tool_result: { tool_use_id: 'agent0', output: 'agent0-out' } }, seq: 101 })
    const container0 = containerOf(1)
    expect(container0.classList.contains('hidden')).toBe(true)
    expect(container0.childElementCount).toBe(0)
  })
})

// ── C7: concurrent siblings ──────────────────────────────────────────────

describe('subtree fetch: concurrent siblings', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('reverse-order responses apply to their own containers and advance anchors', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5'), agentTool('agent2', 30, 200, 's:200:6')])
    const c1 = reqOf('agent1')
    const c2 = reqOf('agent2')
    expect(c1.nonce).not.toBe(c2.nonce)

    // Responses arrive in reverse order.
    sendSubtreePage({ blockID: 'agent2', nonce: c2.nonce, children: olderText('agent2', 30), nextAnchor: 's:170:6', hasMore: true, remaining: 170 })
    sendSubtreePage({ nonce: c1.nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })

    const container1 = containerOf(0)
    const container2 = containerOf(1)
    expect(container1.childElementCount).toBe(60)
    expect(container1.children[0].textContent).toContain('agent1-older0')
    expect(container1.children[30].textContent).toContain('agent1-orig0')
    expect(container2.childElementCount).toBe(60)
    expect(container2.children[0].textContent).toContain('agent2-older0')
    expect(container2.children[30].textContent).toContain('agent2-orig0')

    mockNearTop(container1)
    mockNearTop(container2)
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(reqOf('agent1').anchor).toBe('s:940:5')
    expect(reqOf('agent2').anchor).toBe('s:170:6')
  })
})

// ── C8: tail page converges ──────────────────────────────────────────────

describe('subtree fetch: tail page', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('hasMore:false applies content and stops all refetching', () => {
    const chat = bootSubtree()
    sendSubtreePage({ nonce: reqOf('agent1').nonce, children: olderText('agent1', 30), nextAnchor: '', hasMore: false, remaining: 0 })

    expect(occurrences('agent1-older0')).toBe(1)

    mockNearTop(containerOf(0))
    const before = subtreeRequests().filter((r) => r.blockID === 'agent1').length
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(subtreeRequests().filter((r) => r.blockID === 'agent1').length - before).toBe(0)
  })
})

// ── C9: live interleave ──────────────────────────────────────────────────

describe('subtree fetch: live interleave', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('live sub-agent output lands below the fetched content; mirror unshift keeps deltas routed', () => {
    bootSubtree()
    sendSubtreePage({ nonce: reqOf('agent1').nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    const container = containerOf(0)
    expect(container.childElementCount).toBe(60)

    dispatch({
      type: 'event',
      event: { type: 'tool_start', tool_use: { id: 'newtool', name: 'Bash' }, agent: { parent_tool_use_id: 'agent1', agent_type: 'X', depth: 1 } },
      seq: 101,
    })
    expect(container.childElementCount).toBe(61)
    const last = container.lastElementChild as HTMLElement
    expect(last.dataset.toolName).toBe('Bash')

    dispatch({
      type: 'event',
      event: { type: 'tool_param_delta', partial_input: { id: 'newtool', name: 'Bash', delta: '', summary: 'sum-42' } },
      seq: 102,
    })
    expect(last.textContent).toContain('sum-42')
  })
})

// ── C10: tool_end fold (kept=0) and query_end teardown ───────────────────

describe('subtree fetch: teardown', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('kept=0 block folds on tool_end; a late frame for it lands nowhere', () => {
    bootSubtree()
    const nonce = reqOf('agent0').nonce
    dispatch({
      type: 'event',
      event: { type: 'tool_end', tool_result: { tool_use_id: 'agent0', output: 'agent0-out' } },
      seq: 101,
    })
    const container0 = containerOf(1)
    expect(container0.classList.contains('hidden')).toBe(true)
    expect(container0.childElementCount).toBe(0)

    sendSubtreePage({ blockID: 'agent0', nonce, children: olderText('agent0', 30), nextAnchor: '', hasMore: false, remaining: 0 })
    expect(occurrences('agent0-older0')).toBe(0)
  })

  it('query_end: a late frame reconciles against nothing; fetched and kept content survive', () => {
    bootSubtree()
    sendSubtreePage({ nonce: reqOf('agent1').nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    // agent0's render-time request is still unanswered at query_end.
    const lateNonce = reqOf('agent0').nonce

    dispatch({ type: 'event', event: { type: 'query_end' }, seq: 103 })

    sendSubtreePage({ blockID: 'agent0', nonce: lateNonce, children: olderText('agent0', 30), nextAnchor: '', hasMore: false, remaining: 0 })
    expect(occurrences('agent0-older0')).toBe(0)
    expect(occurrences('agent1-older0')).toBe(1)
    expect(occurrences('agent1-orig0')).toBe(1)
  })
})

// ── C11: text seam fallback ──────────────────────────────────────────────

describe('subtree fetch: text seam', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('identical text at the seam keeps only one copy', () => {
    bootSubtree()
    sendSubtreePage({
      nonce: reqOf('agent1').nonce,
      children: [...olderText('agent1', 29), { kind: 'text', id: '', text: 'agent1-orig0' }],
      nextAnchor: 's:940:5',
      hasMore: true,
      remaining: 940,
    })
    const container = containerOf(0)
    expect(container.childElementCount).toBe(59) // 29 older + 30 orig, seam text once
    expect(occurrences('agent1-orig0')).toBe(1)
  })
})

// ── C12: reconnect reset ─────────────────────────────────────────────────

describe('subtree fetch: reconnect reset', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('pending entry cleared by connect_status; re-boot re-arms the auto fetch', () => {
    bootSubtree()
    const lateNonce = reqOf('agent0').nonce

    dispatch({ type: 'connect_status', connected: true, sessionID: 'fresh' })

    sendSubtreePage({ blockID: 'agent0', nonce: lateNonce, children: olderText('agent0', 30), nextAnchor: '', hasMore: false, remaining: 0 })
    expect(occurrences('agent0-older0')).toBe(0)

    // Re-boot (a fresh takeover page0 render) re-arms every truncated block.
    sendMetadata({ busy: true })
    const n = bootRequestNonce()
    sendPage({ busy: true, stream: bootStream(), nextCursor: '', hasMore: false, seq: 200 }, n)
    expect(subtreeRequests().filter((r) => r.blockID === 'agent1').length).toBe(2)
    expect(reqOf('agent1').anchor).toBe('s:970:5')
    expect(reqOf('agent0').anchor).toBe('s:500:5')
  })
})

// ── A1-A2: scroll proximity ──────────────────────────────────────────────

describe('subtree fetch: scroll proximity', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('A1: in-flight requests are not re-fired by scroll', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    expect(subtreeRequests().length).toBe(1) // render-time fetch
    mockNearTop(containerOf(0))

    chat.scrollEl.dispatchEvent(new Event('scroll'))
    chat.scrollEl.dispatchEvent(new Event('scroll'))

    expect(subtreeRequests().length).toBe(1)
  })

  it('A2: a seam far from the viewport does not fire on scroll', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    expect(subtreeRequests().length).toBe(1) // render-time fetch
    const rect = vi.spyOn(containerOf(0), 'getBoundingClientRect')

    rect.mockReturnValue({ top: 5000 } as DOMRect)
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    rect.mockReturnValue({ top: -5000 } as DOMRect)
    chat.scrollEl.dispatchEvent(new Event('scroll'))

    expect(subtreeRequests().length).toBe(1)
  })
})

// ── N1: kept=0 empty-shell block ─────────────────────────────────────────

describe('subtree fetch: kept=0 shell', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('the auto fetch fills the visible empty shell immediately', () => {
    bootSubtree([agentTool('agent0', 0, 500, 's:500:5')])
    expect(subtreeRequests().length).toBe(1)

    sendSubtreePage({ blockID: 'agent0', nonce: reqOf('agent0').nonce, children: olderText('agent0', 30), nextAnchor: '', hasMore: false, remaining: 0 })

    const container = containerOf(0)
    expect(container.childElementCount).toBe(30)
    expect(container.classList.contains('hidden')).toBe(false)
    expect(container.firstElementChild?.textContent).toContain('agent0-older0')
  })
})

// ── N2: nested truncation ────────────────────────────────────────────────

describe('subtree fetch: nested truncation', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('a truncated child inside a fetched page auto-fetches its own first page', () => {
    bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    const nested = {
      kind: 'tool', id: 'nested', name: 'Agent', state: 'running', startedAt: 1000,
      children: [{ kind: 'text', id: '', text: 'nested-orig0' }], truncated: 5, truncatedCursor: 's:5:6',
    }
    sendSubtreePage({
      nonce: reqOf('agent1').nonce,
      children: [...olderText('agent1', 29), nested],
      nextAnchor: 's:940:5',
      hasMore: true,
      remaining: 940,
    })

    expect(reqOf('nested')).toMatchObject({ type: 'subtree_request', blockID: 'nested', anchor: 's:5:6', limit: 30 })
    expect(occurrences('nested-orig0')).toBe(1)
  })
})
