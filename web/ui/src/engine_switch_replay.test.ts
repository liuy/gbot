import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'

class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

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

const agentMeta = { parent_tool_use_id: 'task-1', agent_type: 'Executor', depth: 1 }

function metadata() {
  dispatch({
    type: 'metadata',
    connect: { connected: true, engineID: 'e3' },
    config: { models: [], current: { provider: 'p', model: 'm' } },
    engines: { engines: [], activeID: 'e3' },
    stats: { usage: { input_tokens: 0, output_tokens: 0 } },
  })
}

const FROZEN_HEAD = 'Existing tests are controller-level fakes'
const REPLAY_TAIL = ' — no direct processor tests, so the pure-core seam stays.'

function taskStream(children: unknown[]): unknown[] {
  return [
    {
      kind: 'tool',
      id: 'task-1',
      name: 'Task',
      summary: '',
      state: 'running',
      timingNs: 0,
      displayOutput: '',
      startedAt: Date.now(),
      isSearch: false,
      isRead: false,
      isList: false,
      isLsp: false,
      isWeb: false,
      isTask: true,
      children,
    },
  ]
}

function taskFeed(): HTMLElement {
  return document.querySelector(
    '[data-tool-root][data-tool-name="Task"] [data-tool-children]',
  ) as HTMLElement
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('engine switch replay', () => {
  it('replay resumes the frozen sub-agent text div instead of duplicating it', () => {
    mount()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'task-1', name: 'Task' } },
      { type: 'thinking_start', agent: agentMeta },
      { type: 'thinking_delta', agent: agentMeta, thinking: { text: 'Analyzing the drain path' } },
      { type: 'thinking_end', agent: agentMeta, thinking: { duration: 159_023_000_000 } },
      { type: 'text_delta', agent: agentMeta, text: FROZEN_HEAD },
    ])
    const feedBefore = taskFeed()
    expect(feedBefore.querySelectorAll('[data-block-kind="text"]').length).toBe(1)

    // Switch: metadata resets all state, later deltas buffer in pendingEvents.
    metadata()
    ;[
      { type: 'text_delta', agent: agentMeta, text: REPLAY_TAIL },
      { type: 'tool_start', agent: agentMeta, tool_use: { id: 'g1', name: 'Grep' } },
    ].forEach((e, i) => dispatch({ type: 'event', seq: 101 + i, event: e }))
    // Page frozen BEFORE the buffered deltas, like the incident (seq 100 < 101).
    dispatch({
      type: 'history',
      busy: true,
      seq: 100,
      nextCursor: '',
      hasMore: false,
      stream: taskStream([
        {
          kind: 'thinking',
          id: '',
          text: 'Analyzing the drain path',
          durationNs: 159_023_000_000,
          active: false,
          startedAt: Date.now(),
        },
        { kind: 'text', id: '', text: FROZEN_HEAD },
      ]),
    })

    const feed = taskFeed()
    const textDivs = feed.querySelectorAll('[data-block-kind="text"]')
    expect(textDivs.length).toBe(1)
    expect((feed.textContent.match(new RegExp(FROZEN_HEAD, 'g')) ?? []).length).toBe(1)
    const div = textDivs[0] as HTMLElement
    expect(div.textContent).toContain(FROZEN_HEAD)
    expect(div.textContent).toContain('no direct processor tests')
    expect(feed.querySelectorAll('[data-tool-root]').length).toBe(1)
    expect(feed.querySelectorAll('[data-thinking]').length).toBe(1)
  })

  it('replay resumes the mainline text block instead of splitting the segment', () => {
    mount()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'text_delta', text: 'partial segment before switch' },
    ])

    metadata()
    dispatch({ type: 'event', seq: 101, event: { type: 'text_delta', text: ' continued after switch' } })
    dispatch({
      type: 'history',
      busy: true,
      seq: 100,
      nextCursor: '',
      hasMore: false,
      stream: [{ kind: 'text', id: '', text: 'partial segment before switch' }],
    })

    const stream = document.querySelector('.space-y-3') as HTMLElement
    const textDivs = stream.querySelectorAll('[data-block-kind="text"]')
    expect(textDivs.length).toBe(1)
    // .trim(): renderMarkdown leaves a trailing newline text node after the <p>.
    expect(textDivs[0].textContent?.trim()).toBe('partial segment before switch continued after switch')
  })

  it('replayed thinking_delta continues the snapshot-rendered active thinking block', () => {
    mount()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'task-1', name: 'Task' } },
    ])

    metadata()
    dispatch({
      type: 'event',
      seq: 101,
      event: { type: 'thinking_delta', agent: agentMeta, thinking: { text: ' more' } },
    })
    dispatch({
      type: 'history',
      busy: true,
      seq: 100,
      nextCursor: '',
      hasMore: false,
      stream: taskStream([
        {
          kind: 'thinking',
          id: '',
          text: 'Thinking hard',
          durationNs: 0,
          active: true,
          startedAt: Date.now(),
        },
      ]),
    })

    const feed = taskFeed()
    const thinking = feed.querySelectorAll('[data-thinking]')
    expect(thinking.length).toBe(1)
    expect(thinking[0].textContent).toContain('Thinking hard more')
  })

  it('replay with a frozen tool tail appends one text div holding only the delta', () => {
    mount()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'task-1', name: 'Task' } },
    ])

    metadata()
    dispatch({
      type: 'event',
      seq: 101,
      event: { type: 'text_delta', agent: agentMeta, text: 'delta after freeze' },
    })
    dispatch({
      type: 'history',
      busy: true,
      seq: 100,
      nextCursor: '',
      hasMore: false,
      stream: taskStream([
        {
          kind: 'tool',
          id: 'g0',
          name: 'Bash',
          summary: '',
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          isTask: false,
          children: [],
        },
      ]),
    })

    const feed = taskFeed()
    const textDivs = feed.querySelectorAll('[data-block-kind="text"]')
    expect(textDivs.length).toBe(1)
    expect(textDivs[0].textContent?.trim()).toBe('delta after freeze')
    expect(feed.querySelectorAll('[data-tool-root]').length).toBe(1)
  })

  it('replay onto a frozen empty text child yields exactly one text div', () => {
    mount()
    events([
      { type: 'query_start' },
      { type: 'turn_start' },
      { type: 'tool_start', tool_use: { id: 'task-1', name: 'Task' } },
    ])

    metadata()
    dispatch({
      type: 'event',
      seq: 101,
      event: { type: 'text_delta', agent: agentMeta, text: 'delta onto empty block' },
    })
    dispatch({
      type: 'history',
      busy: true,
      seq: 100,
      nextCursor: '',
      hasMore: false,
      stream: taskStream([{ kind: 'text', id: '', text: '' }]),
    })

    const feed = taskFeed()
    const textDivs = feed.querySelectorAll('[data-block-kind="text"]')
    expect(textDivs.length).toBe(1)
    expect(textDivs[0].textContent?.trim()).toBe('delta onto empty block')
  })
})
