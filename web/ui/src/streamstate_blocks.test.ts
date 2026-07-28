import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
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

function dispatchEvents(events: unknown[]) {
  for (const e of events) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('streamState block tree rendering', () => {
  it('empty blocks array creates no streaming container', () => {
    mount()
    dispatch({ type: 'streamState', blocks: [] })
    // No assistant shell should have been created.
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(0)
    // No progress bar.
    const progressBars = document.querySelectorAll('[data-progress]')
    expect(progressBars.length).toBe(0)
  })

  it('text block renders text in DOM', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [{ kind: 'text', id: '', text: 'hello streamState' }],
    })
    const mdBodies = document.querySelectorAll('.md-body')
    expect(mdBodies.length).toBe(1)
    expect(mdBodies[0].textContent!.trim()).toBe('hello streamState')
  })

  it('running tool is registered in toolEntries and shows as running', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'tool',
          id: 'tool-1',
          name: 'Bash',
          summary: 'ls -la',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
          children: [],
        },
      ],
    })

    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot.getAttribute('data-tool-name')).toBe('Bash')

    // Running tool dot has heartbeat class (white dot).
    const heartbeat = toolRoot.querySelector('.heartbeat')
    expect(heartbeat).not.toBeNull()

    // Summary text should be rendered.
    expect(toolRoot.textContent).toContain('ls -la')
  })

  it('done tool finalizes immediately and shows output', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'tool',
          id: 'tool-1',
          name: 'Read',
          summary: 'file.txt',
          isSearch: false,
          isRead: true,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'done',
          timingNs: 1_500_000_000,
          displayOutput: 'line1\nline2',
          startedAt: 0,
          children: [],
        },
      ],
    })

    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement

    // Finalized (done) tool: dot should be green, no heartbeat.
    expect(toolRoot.querySelector('.text-green')).not.toBeNull()
    expect(toolRoot.querySelector('.heartbeat')).toBeNull()

    // Duration should be displayed (formatDurationNs floors to 1s for 1.5e9 ns).
    const durEl = toolRoot.querySelector('.font-mono.text-xs') as HTMLElement
    expect(durEl.textContent!.trim()).toBe('1s')

    // Output should be rendered in the body div (children[1] of tool root).
    const bodyEl = toolRoot.children[1] as HTMLElement
    expect(bodyEl.classList.contains('hidden')).toBe(true)
    expect(bodyEl.textContent).toContain('line1')
    expect(bodyEl.textContent).toContain('line2')
  })

  it('error tool shows red dot', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'tool',
          id: 'tool-err',
          name: 'Bash',
          summary: 'exit 1',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'error',
          timingNs: 500_000_000,
          displayOutput: 'command failed',
          startedAt: 0,
          children: [],
        },
      ],
    })

    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot.querySelector('.text-red')).not.toBeNull()
  })

  it('nested children render inside parent tool children container', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'tool',
          id: 'agent-1',
          name: 'Agent',
          summary: 'explore code',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
          children: [
            {
              kind: 'text',
              id: '',
              text: 'sub-agent text here',
            },
            {
              kind: 'tool',
              id: 'sub-read-1',
              name: 'Read',
              summary: 'sub.go',
              isSearch: false,
              isRead: true,
              isList: false,
              isLsp: false,
              isWeb: false,
              state: 'done',
              timingNs: 100_000_000,
              displayOutput: 'package main',
              startedAt: 0,
              children: [],
            },
          ],
        },
      ],
    })

    const agentRoot = document.querySelector(
      '[data-tool-root][data-tool-name="Agent"]',
    ) as HTMLElement

    // The children container is inside the agent tool.
    const childrenContainer = agentRoot.querySelector('[data-tool-children]') as HTMLElement

    // The sub-agent text should be inside the children container, not top-level.
    const nestedMdBody = childrenContainer.querySelector('.md-body')
    expect(nestedMdBody!.textContent!.trim()).toBe('sub-agent text here')

    // The sub-agent Read tool should be inside the children container.
    const nestedTool = childrenContainer.querySelector(
      '[data-tool-root][data-tool-name="Read"]',
    )
    expect(nestedTool).not.toBeNull()

    // Verify nested Read tool shows output.
    expect(nestedTool!.textContent).toContain('package main')
  })

  it('active thinking block is tracked and shows thinking text', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'thinking',
          id: 'think-1',
          text: 'reasoning about the problem',
          durationNs: 0,
          active: true,
          startedAt: Date.now(),
        },
      ],
    })

    // Thinking text should be visible in DOM.
    expect(document.body.textContent).toContain('reasoning about the problem')

    // Active thinking shows expanded (no 'hidden' on the <p>).
    const thinkWrap = document.querySelector('[data-thinking]') as HTMLElement
    const thinkP = thinkWrap.querySelector('p') as HTMLElement
    expect(thinkP.classList.contains('hidden')).toBe(false)

    // Label shows "Thinking" (active), not "Thought for" (finished).
    // querySelectorAll('.text-amber') returns [glyph, label]; label is index 1.
    const amberEls = thinkWrap.querySelectorAll('.text-amber')
    expect(amberEls.length).toBe(2)
    const labelEl = amberEls[1] as HTMLElement
    expect(labelEl.textContent).toContain('Thinking')

    // Heartbeat animation active on the glyph (index 0).
    expect(amberEls[0].classList.contains('heartbeat')).toBe(true)
  })

  it('finished thinking block shows collapsed with duration', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'thinking',
          id: 'think-done',
          text: 'completed reasoning',
          durationNs: 3_000_000_000,
          active: false,
          startedAt: 0,
        },
      ],
    })

    expect(document.body.textContent).toContain('completed reasoning')

    const thinkWrap = document.querySelector('[data-thinking]') as HTMLElement
    const thinkP = thinkWrap.querySelector('p') as HTMLElement
    // Finished thinking is auto-collapsed.
    expect(thinkP.classList.contains('hidden')).toBe(true)

    // querySelectorAll('.text-amber') returns [glyph, label]; label is index 1.
    const amberEls = thinkWrap.querySelectorAll('.text-amber')
    expect(amberEls.length).toBe(2)
    const labelEl = amberEls[1] as HTMLElement
    expect(labelEl.textContent).toContain('Thought for')
    expect(labelEl.textContent).toContain('3s')

    // No heartbeat on finished (glyph at index 0 has heartbeat removed).
    expect(amberEls[0].classList.contains('heartbeat')).toBe(false)
  })

  it('live tool_end after streamState finalizes the running tool', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'tool',
          id: 'live-tool-1',
          name: 'Bash',
          summary: 'echo hi',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'running',
          timingNs: 0,
          displayOutput: '',
          startedAt: Date.now(),
          children: [],
        },
      ],
    })

    // Verify it is running initially.
    const toolRoot = document.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot.querySelector('.heartbeat')).not.toBeNull()

    // Now send a live tool_end event for the same tool ID.
    dispatchEvents([
      {
        type: 'tool_end',
        tool_result: {
          tool_use_id: 'live-tool-1',
          display_output: 'hi',
          is_error: false,
        },
      },
    ])

    // The tool should now be finalized (green dot, no heartbeat).
    expect(toolRoot.querySelector('.text-green')).not.toBeNull()
    expect(toolRoot.querySelector('.heartbeat')).toBeNull()

    // Output should be rendered in the body div (children[1] of tool root).
    const bodyEl = toolRoot.children[1] as HTMLElement
    expect(bodyEl.textContent).toContain('hi')
  })

  it('live thinking_delta after streamState appends to active thinking', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        {
          kind: 'thinking',
          id: 'think-live',
          text: 'initial thought',
          durationNs: 0,
          active: true,
          startedAt: Date.now(),
        },
      ],
    })

    // Dispatch a live thinking_delta — should append to the active thinking block.
    dispatchEvents([
      { type: 'thinking_delta', thinking: { text: ' more reasoning' } },
    ])

    expect(document.body.textContent).toContain('initial thought')
    expect(document.body.textContent).toContain('more reasoning')
  })

  it('multiple blocks in order preserves DOM order', () => {
    mount()
    dispatch({
      type: 'streamState',
      blocks: [
        { kind: 'text', id: '', text: 'first block' },
        {
          kind: 'tool',
          id: 'mid-tool',
          name: 'Bash',
          summary: 'cmd',
          isSearch: false,
          isRead: false,
          isList: false,
          isLsp: false,
          isWeb: false,
          state: 'done',
          timingNs: 1_000_000_000,
          displayOutput: 'result',
          startedAt: 0,
          children: [],
        },
        { kind: 'text', id: '', text: 'second block' },
      ],
    })

    // Collect all top-level streaming elements in DOM order.
    const streamingContent = document.querySelector('.space-y-3') as HTMLElement

    // The md-body elements and tool roots should be interleaved in order.
    const allText = streamingContent.textContent ?? ''
    const firstIdx = allText.indexOf('first block')
    const cmdIdx = allText.indexOf('cmd')
    const secondIdx = allText.indexOf('second block')

    expect(firstIdx).toBeGreaterThanOrEqual(0)
    expect(cmdIdx).toBeGreaterThan(firstIdx)
    expect(secondIdx).toBeGreaterThan(cmdIdx)
  })

  it('streamState after metadata with running tool reuses existing container, does not create new message', () => {
    mount()
    // metadata with history: assistant response with Agent tool (running)
    dispatch({
      type: 'metadata',
      connect: { connected: true, model: 'test' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      history: {
        messages: [{
          id: 'a1', role: 'assistant', startedAt: 1000, text: '', thinking: [], tools: [],
          blocks: [
            { kind: 'text', text: 'Let me review' },
            { kind: 'tool', tool: { id: 'agent1', name: 'Agent', summary: 'Reviewer', isRunning: true } },
          ],
          usage: { inputTokens: 0, outputTokens: 0 },
        }],
        nextCursor: '', hasMore: false,
      },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
    })

    // history loaded — should have 1 assistant message with running Agent tool
    const msgsBefore = document.querySelectorAll('.space-y-3')
    expect(msgsBefore.length).toBe(1)

    // streamState arrives with sub-agent data (text + tools nested in Agent)
    dispatch({
      type: 'streamState',
      blocks: [{
        kind: 'tool', id: 'agent1', name: 'Agent', state: 'running',
        children: [
          { kind: 'text', id: 't1', text: 'sub-agent text' },
          { kind: 'tool', id: 'sub1', name: 'Grep', state: 'done', displayOutput: 'found 3 matches' },
        ],
      }],
    })

    // Should NOT create a new message — streamState should reuse the
    // existing message (the one with the running Agent tool from history)
    const msgsAfter = document.querySelectorAll('.space-y-3')
    expect(msgsAfter.length).toBe(1)

    // The sub-agent text should be visible inside the Agent tool
    const allText = document.body.textContent ?? ''
    expect(allText).toContain('sub-agent text')
    expect(allText).toContain('found 3 matches')
  })

  it('query_end after streamState with undefined children does not throw', () => {
    mount()
    // streamState with a tool block that has no children (JSON omitempty may omit it)
    dispatch({
      type: 'streamState',
      blocks: [{
        kind: 'tool', id: 'agent1', name: 'Agent', state: 'running',
        summary: 'review', children: undefined,
      }],
    })

    // Now dispatch query_end (aborted) — should NOT throw
    expect(() => {
      dispatch({
        type: 'event',
        event: { type: 'query_end', aborted: true },
      })
    }).not.toThrow()
  })

  it('tool_start with summary renders it immediately (compact virtual tool)', () => {
    mount()
    dispatch({
      type: 'event',
      event: {
        type: 'tool_start',
        tool_use: { id: 'compact1', name: 'Compact', summary: 'Compacting conversation...' },
      },
    })

    // The tool header should show the summary from tool_start
    const summaryEls = document.querySelectorAll('.text-t2')
    let found = false
    summaryEls.forEach(el => {
      if (el.textContent && el.textContent.includes('Compacting')) found = true
    })
    expect(found).toBe(true)
  })

  it('Agent tool header shows agent type from sub-agent turn_start event', () => {
    mount()
    dispatch({
      type: 'event',
      event: {
        type: 'tool_start',
        tool_use: { id: 'agent1', name: 'Agent', summary: '' },
      },
    })

    const toolNames = document.querySelectorAll('[data-tool-name]')
    expect(toolNames.length).toBe(1)
    expect(toolNames[0].getAttribute('data-tool-name')).toBe('Agent')

    dispatch({
      type: 'event',
      event: {
        type: 'turn_start',
        agent: { parent_tool_use_id: 'agent1', agent_type: 'Reviewer' },
      },
    })

    const nameEl = document.querySelector('.font-mono.text-blue') as HTMLElement
    expect(nameEl).toBeTruthy()
    expect(nameEl.textContent).toBe('Agent Reviewer')

    // Second turn_start should be a no-op (no double append)
    dispatch({
      type: 'event',
      event: {
        type: 'turn_start',
        agent: { parent_tool_use_id: 'agent1', agent_type: 'Reviewer' },
      },
    })
    expect(nameEl.textContent).toBe('Agent Reviewer')
  })

  it('Agent tool header ignores fork agent type', () => {
    mount()
    dispatch({
      type: 'event',
      event: {
        type: 'tool_start',
        tool_use: { id: 'agent2', name: 'Agent', summary: '' },
      },
    })
    dispatch({
      type: 'event',
      event: {
        type: 'turn_start',
        agent: { parent_tool_use_id: 'agent2', agent_type: 'fork' },
      },
    })
    const nameEl = document.querySelector('.font-mono.text-blue') as HTMLElement
    expect(nameEl).toBeTruthy()
    expect(nameEl.textContent).toBe('Agent')
  })

  // Red test: switching engines back to a main engine whose query is in
  // progress but has not yet emitted any stream events (LLM still warming
  // up, blocks empty). The server sends queryStartMs > 0 in stats to signal
  // "query is active". Without checking queryStartMs, the metadata handler
  // only restores streaming when blocks are non-empty — leaving the UI
  // stuck: no STOP button, Esc ignored.
  it('metadata with queryStartMs > 0 but empty snapshot restores streaming state', () => {
    mount()
    // First metadata: query_start happened on server, but LLM hasn't
    // returned any events yet — snapshot.blocks is empty/absent.
    // queryStartMs > 0 signals the query is active.
    dispatch({
      type: 'metadata',
      connect: { connected: true, model: 'test' },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      history: { messages: [], nextCursor: '', hasMore: false },
      stats: {
        usage: { input_tokens: 0, output_tokens: 0 },
        queryStartMs: Date.now() - 2000,
      },
      // snapshot intentionally omitted / blocks empty
    })

    // Expectation: UI enters streaming state (STOP button visible)
    expect(document.body.textContent).toContain('STOP')
  })
})
