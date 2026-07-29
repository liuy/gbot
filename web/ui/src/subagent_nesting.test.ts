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
    subscribeBinary: () => () => {},
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

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('sub-agent nesting', () => {
  it('sub-agent tool + text nest under parent Agent tool, not top-level', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'text_delta',
        text: 'top',
      },
      {
        type: 'tool_start',
        tool_use: { id: 'agent-1', name: 'Agent' },
      },
      {
        type: 'tool_start',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
        tool_use: { id: 'grep-1', name: 'Grep' },
      },
      {
        type: 'text_delta',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
        text: 'nested text',
      },
    ])
    // The sub-agent grep tool must be inside agent-1's children container,
    // not a sibling at the top-level streaming container.
    const agentRoot = document.querySelector(
      '[data-tool-root][data-tool-name="Agent"]',
    ) as HTMLElement
    expect(agentRoot).toBeTruthy()
    const nested = agentRoot.querySelector(
      '[data-tool-children] [data-tool-root][data-tool-name="Grep"]',
    )
    expect(nested).not.toBeNull()
    expect(nested!.getAttribute('data-tool-name')).toBe('Grep')
  })

  it('top-level events stay flat (no nesting)', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 'bash-1', name: 'Bash' },
      },
    ])
    const stream = document.querySelector('.space-y-3')!
    const top = stream.querySelectorAll(':scope > [data-tool-root]')
    expect(top.length).toBe(1)
  })

  it('turn_start/query_end with agent metadata do not finish stream', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 'agent-1', name: 'Agent' },
      },
      {
        type: 'turn_start',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
      },
      {
        type: 'query_end',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
      },
    ])
    // Still streaming (no top-level query_end): STOP visible.
    expect(document.body.textContent).toContain('STOP')
  })

  it('text_delta with unknown parent_tool_use_id dropped', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'text_delta',
        agent: { parent_tool_use_id: 'nope', agent_type: 'X', depth: 1 },
        text: 'orphan',
      },
    ])
    expect(document.body.textContent).not.toContain('orphan')
  })

  it('depth >= 2 nesting (agent-2 nests under agent-1)', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 'agent-1', name: 'Agent' },
      },
      {
        type: 'tool_start',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'Explore', depth: 1 },
        tool_use: { id: 'agent-2', name: 'Agent' },
      },
      {
        type: 'tool_start',
        agent: { parent_tool_use_id: 'agent-2', agent_type: 'Explore', depth: 2 },
        tool_use: { id: 'read-1', name: 'Read' },
      },
    ])
    const a1 = document.querySelector(
      '[data-tool-root][data-tool-name="Agent"]',
    ) as HTMLElement
    const a2 = a1.querySelector(
      '[data-tool-children] [data-tool-root][data-tool-name="Agent"]',
    ) as HTMLElement
    expect(a2).toBeTruthy()
    const r1 = a2.querySelector(
      '[data-tool-children] [data-tool-root][data-tool-name="Read"]',
    )
    expect(r1).not.toBeNull()
    expect(r1!.getAttribute('data-tool-name')).toBe('Read')
  })

  it('auto-expand on running, auto-collapse on done', () => {
    mount()
    events([
      { type: 'query_start' },
      {
        type: 'tool_start',
        tool_use: { id: 'agent-1', name: 'Agent' },
      },
      {
        type: 'tool_start',
        agent: { parent_tool_use_id: 'agent-1', agent_type: 'X', depth: 1 },
        tool_use: { id: 'grep-1', name: 'Grep' },
      },
    ])
    const a1 = document.querySelector(
      '[data-tool-root][data-tool-name="Agent"]',
    ) as HTMLElement
    const children = a1.querySelector('[data-tool-children]') as HTMLElement
    // Running parent → auto-expanded.
    expect(children.classList.contains('hidden')).toBe(false)
    // Finish agent-1: tool_end should auto-collapse AND clear children.
    dispatch({
      type: 'event',
      event: {
        type: 'tool_end',
        tool_result: { tool_use_id: 'agent-1' },
      },
    })
    expect(children.classList.contains('hidden')).toBe(true)
    // Children content must be cleared (not just hidden) so re-expanding
    // doesn't show stale streaming output from the sub-agent.
    expect(children.children.length).toBe(0)
  })
})
