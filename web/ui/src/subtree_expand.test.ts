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

// Boots a busy page0 whose in-flight window holds agent1 (30 kept children,
// truncated 970) and agent0 (kept=0, truncated 500 — the button-reachable
// budget-exhausted shape).
function bootSubtree(stream: Record<string, unknown>[] = [agentTool('agent1', 30, 970, 's:970:5'), agentTool('agent0', 0, 500, 's:500:5')]) {
  const chat = mount()
  sendMetadata({ busy: true })
  const n = bootRequestNonce()
  sendPage({ busy: true, stream, nextCursor: '', hasMore: false, seq: 100 }, n)
  return chat
}

function expandButtons(): HTMLButtonElement[] {
  return Array.from(document.querySelectorAll<HTMLButtonElement>('[data-subtree-expand]'))
}

function findButton(labelPart: string): HTMLButtonElement {
  const btn = expandButtons().find((b) => (b.textContent ?? '').includes(labelPart))
  if (!btn) throw new Error(`no expand button with label containing ${labelPart}`)
  return btn
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

function clickExpand(labelPart: string): { btn: HTMLButtonElement; nonce: number } {
  const btn = findButton(labelPart)
  btn.click()
  const reqs = subtreeRequests()
  if (reqs.length === 0) throw new Error('click sent no subtree_request')
  return { btn, nonce: reqs[reqs.length - 1].nonce }
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

// ── C1: button rendering, including kept=0 and sink-less renders ────────

describe('subtree expand: button rendering', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('renders the expand button above kept children and for kept=0 blocks', () => {
    bootSubtree()
    const btns = expandButtons()
    expect(btns.length).toBe(2)
    expect(btns.map((b) => b.textContent).some((l) => l?.includes('970'))).toBe(true)
    expect(btns.map((b) => b.textContent).some((l) => l?.includes('500'))).toBe(true)

    const btn1 = findButton('970')
    const container1 = btn1.closest('[data-tool-children]') as HTMLElement
    expect(container1.childElementCount).toBe(31)
    expect(container1.firstElementChild).toBe(btn1)
    expect(container1.children[1].textContent).toContain('agent1-orig0')

    // kept=0: the button is the container's only child and the container is
    // visible (expand condition must fire on truncated alone).
    const btn0 = findButton('500')
    const container0 = btn0.closest('[data-tool-children]') as HTMLElement
    expect(container0.childElementCount).toBe(1)
    expect(container0.classList.contains('hidden')).toBe(false)
  })

  it('renders no button without a sink (job feeds)', () => {
    const parent = document.createElement('div')
    renderStreamBlock(parent, agentTool('agentX', 2, 30, 's:30:5') as unknown as Block, null, null)
    expect(parent.querySelector('[data-subtree-expand]')).toBeNull()
    // Children still render sink-less.
    expect(parent.querySelectorAll('[data-tool-children]').length).toBe(1)
    expect(parent.textContent).toContain('agentX-orig0')
  })
})

// ── C2: click wires the request ──────────────────────────────────────────

describe('subtree expand: request', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('click sends subtree_request with the frozen anchor and disables the button', () => {
    bootSubtree()
    clickExpand('970')
    const reqs = subtreeRequests()
    expect(reqs.length).toBe(1)
    expect(reqs[0]).toMatchObject({ type: 'subtree_request', blockID: 'agent1', anchor: 's:970:5', limit: 30 })
    expect(typeof reqs[0].nonce).toBe('number')
    expect(reqs[0].nonce).toBeGreaterThanOrEqual(1)
    expect(findButton('970').disabled).toBe(true)
    expect(findButton('500').disabled).toBe(false)
  })
})

// ── C3: apply + anchor advance ───────────────────────────────────────────

describe('subtree expand: apply and anchor advance', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('prepends the page below the button, advances label/anchor, re-enables', () => {
    bootSubtree()
    const { btn, nonce } = clickExpand('970')
    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })

    const container = btn.closest('[data-tool-children]') as HTMLElement
    const kids = Array.from(container.children)
    expect(kids.length).toBe(61)
    expect(kids[0]).toBe(btn)
    expect(kids[1].textContent).toContain('agent1-older0')
    expect(kids[30].textContent).toContain('agent1-older29')
    expect(kids[31].textContent).toContain('agent1-orig0')
    expect(kids[60].textContent).toContain('agent1-orig29')
    expect(btn.textContent).toContain('940')
    expect(btn.disabled).toBe(false)

    // Second click re-sends with the ADVANCED anchor (mirror fields written).
    btn.click()
    const reqs = subtreeRequests()
    expect(reqs.length).toBe(2)
    expect(reqs[1].anchor).toBe('s:940:5')
    expect(reqs[1].nonce).not.toBe(reqs[0].nonce)
  })
})

// ── C4: dedupe ───────────────────────────────────────────────────────────

describe('subtree expand: dedupe', () => {
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

    const { btn, nonce } = clickExpand('970')
    sendSubtreePage({
      nonce,
      children: [...olderText('agent1', 29), { kind: 'tool', id: 'dup-tool', name: 'Bash', state: 'running', children: [] }],
      nextAnchor: 's:940:5',
      hasMore: true,
      remaining: 940,
    })

    const container = btn.closest('[data-tool-children]') as HTMLElement
    // btn + 29 older + 30 original (dup-tool appears once).
    expect(container.childElementCount).toBe(60)
    expect(container.querySelectorAll('[data-tool-root]').length).toBe(1)
  })
})

// ── C5: nonce reconciliation ─────────────────────────────────────────────

describe('subtree expand: nonce reconciliation', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('unknown-nonce frame dropped whole; the right nonce still applies', () => {
    bootSubtree()
    const { nonce } = clickExpand('970')

    sendSubtreePage({ nonce: nonce + 1, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(occurrences('agent1-older0')).toBe(0)
    expect(findButton('970').disabled).toBe(true)

    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(occurrences('agent1-older0')).toBe(1)
    expect(findButton('940').disabled).toBe(false)
  })
})

// ── C6: stale terminal ───────────────────────────────────────────────────

describe('subtree expand: stale', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('stale frame removes the button and touches nothing else', () => {
    bootSubtree()
    const { nonce } = clickExpand('970')
    sendSubtreePage({ nonce, stale: true })

    expect(expandButtons().map((b) => b.textContent).some((l) => l?.includes('970'))).toBe(false)
    expect(expandButtons().length).toBe(1) // agent0's button survives
    expect(occurrences('agent1-orig0')).toBe(1)
  })
})

// ── C7: concurrent siblings ──────────────────────────────────────────────

describe('subtree expand: concurrent siblings', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('reverse-order responses apply to their own containers', () => {
    bootSubtree([agentTool('agent1', 30, 970, 's:970:5'), agentTool('agent2', 30, 200, 's:200:6')])
    const c1 = clickExpand('970')
    const c2 = clickExpand('200')

    // Responses arrive in reverse order.
    sendSubtreePage({ blockID: 'agent2', nonce: c2.nonce, children: olderText('agent2', 30), nextAnchor: 's:170:6', hasMore: true, remaining: 170 })
    sendSubtreePage({ blockID: 'agent1', nonce: c1.nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })

    const container1 = c1.btn.closest('[data-tool-children]') as HTMLElement
    const container2 = c2.btn.closest('[data-tool-children]') as HTMLElement
    expect(container1.childElementCount).toBe(61)
    expect(container1.children[1].textContent).toContain('agent1-older0')
    expect(container2.childElementCount).toBe(61)
    expect(container2.children[1].textContent).toContain('agent2-older0')
    expect(c1.btn.textContent).toContain('940')
    expect(c2.btn.textContent).toContain('170')
  })
})

// ── C8: tail page removes the button ─────────────────────────────────────

describe('subtree expand: tail page', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('hasMore:false applies content and removes the button', () => {
    bootSubtree()
    const { nonce } = clickExpand('970')
    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: '', hasMore: false, remaining: 0 })

    expect(occurrences('agent1-older0')).toBe(1)
    expect(expandButtons().map((b) => b.textContent).some((l) => l?.includes('970'))).toBe(false)
    expect(expandButtons().length).toBe(1)
  })
})

// ── C9: live interleave ──────────────────────────────────────────────────

describe('subtree expand: live interleave', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('live sub-agent output lands below the expanded content; mirror unshift keeps deltas routed', () => {
    bootSubtree()
    const { btn, nonce } = clickExpand('970')
    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    const container = btn.closest('[data-tool-children]') as HTMLElement
    expect(container.childElementCount).toBe(61)

    dispatch({
      type: 'event',
      event: { type: 'tool_start', tool_use: { id: 'newtool', name: 'Bash' }, agent: { parent_tool_use_id: 'agent1', agent_type: 'X', depth: 1 } },
      seq: 101,
    })
    expect(container.childElementCount).toBe(62)
    const last = container.lastElementChild as HTMLElement
    expect(last.dataset.toolName).toBe('Bash')
    expect(btn.textContent).toContain('940')

    dispatch({
      type: 'event',
      event: { type: 'tool_param_delta', partial_input: { id: 'newtool', name: 'Bash', delta: '', summary: 'sum-42' } },
      seq: 102,
    })
    expect(last.textContent).toContain('sum-42')
  })
})

// ── C10: tool_end fold (kept=0) and query_end sweep ──────────────────────

describe('subtree expand: teardown', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('kept=0 block folds on tool_end: button and container collapse', () => {
    bootSubtree()
    const container0 = findButton('500').closest('[data-tool-children]') as HTMLElement
    dispatch({
      type: 'event',
      event: { type: 'tool_end', tool_result: { tool_use_id: 'agent0', output: 'agent0-out' } },
      seq: 101,
    })
    expect(expandButtons().length).toBe(1)
    expect(container0.classList.contains('hidden')).toBe(true)
    expect(container0.childElementCount).toBe(0)
  })

  it('query_end sweeps leftover expand buttons', () => {
    bootSubtree()
    const { nonce } = clickExpand('970')
    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(expandButtons().length).toBe(2)

    dispatch({ type: 'event', event: { type: 'query_end' }, seq: 103 })
    expect(expandButtons().length).toBe(0)
    // Content survives the sweep.
    expect(occurrences('agent1-older0')).toBe(1)
    expect(occurrences('agent1-orig0')).toBe(1)
  })
})

// ── C11: text seam fallback ──────────────────────────────────────────────

describe('subtree expand: text seam', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('identical text at the seam keeps only one copy', () => {
    bootSubtree()
    const { btn, nonce } = clickExpand('970')
    sendSubtreePage({
      nonce,
      children: [...olderText('agent1', 29), { kind: 'text', id: '', text: 'agent1-orig0' }],
      nextAnchor: 's:940:5',
      hasMore: true,
      remaining: 940,
    })
    const container = btn.closest('[data-tool-children]') as HTMLElement
    expect(container.childElementCount).toBe(60) // btn + 29 older + 30 orig
    expect(occurrences('agent1-orig0')).toBe(1)
  })
})

// ── C12: reconnect reset ─────────────────────────────────────────────────

describe('subtree expand: reconnect reset', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('pending entry cleared by connect_status; the late frame lands nowhere', () => {
    bootSubtree()
    const { nonce } = clickExpand('970')

    dispatch({ type: 'connect_status', connected: true, sessionID: 'fresh' })
    expect(expandButtons().length).toBe(0)

    sendSubtreePage({ nonce, children: olderText('agent1', 30), nextAnchor: 's:940:5', hasMore: true, remaining: 940 })
    expect(occurrences('agent1-older0')).toBe(0)
    expect(expandButtons().length).toBe(0)
  })
})

// ── A1-A3: scroll auto-trigger (§7) ──────────────────────────────────────

describe('subtree expand: scroll auto-trigger', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
    document.body.innerHTML = ''
  })

  it('A1: a sentinel within the threshold fires the click path on scroll', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    const btn = findButton('970')
    vi.spyOn(btn, 'getBoundingClientRect').mockReturnValue({ top: 0 } as DOMRect)

    chat.scrollEl.dispatchEvent(new Event('scroll'))

    const reqs = subtreeRequests()
    expect(reqs.length).toBe(1)
    expect(reqs[0]).toMatchObject({ type: 'subtree_request', blockID: 'agent1', anchor: 's:970:5', limit: 30 })
    expect(reqs[0].nonce).toBeGreaterThanOrEqual(1)
    expect(btn.disabled).toBe(true)
  })

  it('A2: an in-flight (disabled) button is not re-fired by scroll', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    const btn = findButton('970')
    clickExpand('970')
    expect(btn.disabled).toBe(true)
    vi.spyOn(btn, 'getBoundingClientRect').mockReturnValue({ top: 0 } as DOMRect)

    chat.scrollEl.dispatchEvent(new Event('scroll'))
    chat.scrollEl.dispatchEvent(new Event('scroll'))

    expect(subtreeRequests().length).toBe(1)
  })

  it('A3: a sentinel far from the viewport does not fire on scroll', () => {
    const chat = bootSubtree([agentTool('agent1', 30, 970, 's:970:5')])
    const btn = findButton('970')
    const rect = vi.spyOn(btn, 'getBoundingClientRect')

    rect.mockReturnValue({ top: 5000 } as DOMRect)
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(subtreeRequests().length).toBe(0)

    rect.mockReturnValue({ top: -5000 } as DOMRect)
    chat.scrollEl.dispatchEvent(new Event('scroll'))
    expect(subtreeRequests().length).toBe(0)
    expect(btn.disabled).toBe(false)
  })
})
