import { describe, it, expect, beforeEach, vi } from 'vitest'
import { renderStreamBlock, type LiveStreamSink } from './stream_blocks'
import type { Block } from '../model'

function toolBlock(id: string, name: string, over: Partial<Extract<Block, { kind: 'tool' }>> = {}): Block {
  return {
    kind: 'tool',
    id,
    name,
    summary: `${name} summary`,
    isSearch: false,
    isRead: false,
    isList: false,
    isLsp: false,
    isWeb: false,
    isTask: false,
    state: 'done',
    timingNs: 0,
    displayOutput: '',
    startedAt: 0,
    children: [],
    ...over,
  }
}

function mount(): HTMLElement {
  document.body.innerHTML = ''
  const parent = document.createElement('div')
  document.body.appendChild(parent)
  return parent
}

describe('renderStreamBlock — pure rendering (no sink)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('renders a text block as a markdown md-body', () => {
    const parent = mount()
    renderStreamBlock(parent, { kind: 'text', id: '', text: 'plain **bold**' }, null, null)
    const divs = parent.querySelectorAll('.md-body')
    expect(divs).toHaveLength(1)
    expect(divs[0].innerHTML).toContain('<strong>bold</strong>')
  })

  it('renders a done thinking block labeled with its duration and a collapsed paragraph', () => {
    const parent = mount()
    renderStreamBlock(
      parent,
      { kind: 'thinking', id: '', text: '推理内容', durationNs: 0, active: false, startedAt: 0 },
      null,
      null,
    )
    const wrap = parent.querySelector('[data-thinking]') as HTMLElement
    expect(wrap.textContent).toContain('Thought for 0s')
    expect(wrap.textContent).toContain('推理内容')
    expect((wrap.querySelector('p') as HTMLElement).classList.contains('hidden')).toBe(true)
  })

  it('renders an active thinking block with markdown streamed in (within-threshold snapshot)', () => {
    const parent = mount()
    renderStreamBlock(
      parent,
      { kind: 'thinking', id: '', text: 'plan **hi**', durationNs: 0, active: true, startedAt: 0 },
      null,
      null,
    )
    const p = parent.querySelector('[data-thinking] p') as HTMLElement
    expect(p.querySelector('strong')?.textContent).toBe('hi')
    expect(p.querySelector('[data-think-tail]')).toBeNull()
  })

  it('renders a done thinking block with markdown content', () => {
    const parent = mount()
    renderStreamBlock(
      parent,
      { kind: 'thinking', id: '', text: 'plan **hi**', durationNs: 0, active: false, startedAt: 0 },
      null,
      null,
    )
    const wrap = parent.querySelector('[data-thinking]') as HTMLElement
    expect((wrap.querySelector('p') as HTMLElement).querySelector('strong')?.textContent).toBe('hi')
    expect(wrap.textContent).toContain('Thought for')
  })

  it('renders a done tool in its finished state: green dot, empty output stays collapsed', () => {
    const parent = mount()
    renderStreamBlock(parent, toolBlock('t-1', 'Grep'), null, null)
    const root = parent.querySelector('[data-tool-root]') as HTMLElement
    expect(root.dataset.toolName).toBe('Grep')
    const dot = root.querySelector('.text-green') as HTMLElement
    expect(dot).not.toBeNull()
    // finishTool only expands the body when there is output to show.
    const body = root.querySelector('.md-body') as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
  })

  it('renders a running tool with a heartbeat dot', () => {
    const parent = mount()
    renderStreamBlock(parent, toolBlock('t-1', 'Bash', { state: 'running' }), null, null)
    const root = parent.querySelector('[data-tool-root]') as HTMLElement
    expect(root.querySelector('.heartbeat')).not.toBeNull()
  })

  it('nests children in [data-tool-children]: expanded while the parent runs, hidden when done', () => {
    const runningParent = mount()
    renderStreamBlock(
      runningParent,
      toolBlock('t-1', 'Agent', { state: 'running', children: [{ kind: 'text', id: '', text: 'sub text' }] }),
      null,
      null,
    )
    const rc = runningParent.querySelector('[data-tool-children]') as HTMLElement
    expect(rc.classList.contains('hidden')).toBe(false)
    expect(rc.textContent).toContain('sub text')

    const doneParent = mount()
    renderStreamBlock(
      doneParent,
      toolBlock('t-2', 'Agent', { children: [{ kind: 'text', id: '', text: 'sub text' }] }),
      null,
      null,
    )
    const dc = doneParent.querySelector('[data-tool-children]') as HTMLElement
    // Collapsed on done, but present in the DOM — chat nesting semantics.
    expect(dc.classList.contains('hidden')).toBe(true)
    expect(dc.textContent).toContain('sub text')
  })

  it('renders a user block as the gray queued-message echo', () => {
    const parent = mount()
    renderStreamBlock(parent, { kind: 'user', id: '', text: '排队消息' }, null, null)
    const spans = Array.from(parent.querySelectorAll('span')).filter(
      (s) => s.textContent === '排队消息',
    )
    expect(spans).toHaveLength(1)
    expect(spans[0].className).toContain('text-t3')
  })

  it('skips empty text and user blocks without producing nodes', () => {
    const parent = mount()
    renderStreamBlock(parent, { kind: 'text', id: '', text: '' }, null, null)
    renderStreamBlock(parent, { kind: 'user', id: '', text: '' }, null, null)
    expect(parent.childNodes).toHaveLength(0)
  })
})

describe('renderStreamBlock — live sink', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  function sinkWith(): LiveStreamSink & {
    onToolRunning: ReturnType<typeof vi.fn>
    onThinkingActive: ReturnType<typeof vi.fn>
  } {
    const onToolRunning = vi.fn()
    const onThinkingActive = vi.fn()
    return { onToolRunning, onThinkingActive } as LiveStreamSink & {
      onToolRunning: ReturnType<typeof vi.fn>
      onThinkingActive: ReturnType<typeof vi.fn>
    }
  }

  it('reports a running root tool with connected handles, the block, and null parentID', () => {
    const parent = mount()
    const sink = sinkWith()
    const block = toolBlock('t-1', 'Bash', { state: 'running' })
    renderStreamBlock(parent, block, null, null, sink)
    expect(sink.onToolRunning).toHaveBeenCalledTimes(1)
    const [handles, gotBlock, parentID] = sink.onToolRunning.mock.calls[0]
    expect(handles.root.isConnected).toBe(true)
    expect(handles.root.dataset.toolName).toBe('Bash')
    expect(gotBlock).toBe(block)
    expect(parentID).toBeNull()
  })

  it('forwards live into nested children: a running child reports with the parent id as parentID', () => {
    const parent = mount()
    const sink = sinkWith()
    const child = toolBlock('t-child', 'Read', { state: 'running' })
    const parentBlock = toolBlock('t-parent', 'Agent', { children: [child] })
    renderStreamBlock(parent, parentBlock, null, null, sink)
    // The parent is done — only the nested running tool registers, and the
    // registration names the parent tool so later deltas route correctly.
    expect(sink.onToolRunning).toHaveBeenCalledTimes(1)
    const [, gotBlock, parentID] = sink.onToolRunning.mock.calls[0]
    expect(gotBlock).toBe(child)
    expect(parentID).toBe('t-parent')
  })

  it('reports active thinking and stays silent for finished blocks', () => {
    const parent = mount()
    const sink = sinkWith()
    const active = { kind: 'thinking', id: '', text: '', durationNs: 0, active: true, startedAt: 0 }
    renderStreamBlock(parent, active, null, null, sink)
    expect(sink.onThinkingActive).toHaveBeenCalledTimes(1)
    const [entry, parentID] = sink.onThinkingActive.mock.calls[0]
    expect(entry.p.isConnected).toBe(true)
    expect(entry.labelEl.textContent).toContain('Thinking')
    expect(parentID).toBeNull()

    // Terminal blocks must never register.
    renderStreamBlock(
      parent,
      { kind: 'thinking', id: '', text: 'done', durationNs: 5e9, active: false, startedAt: 0 },
      null,
      null,
      sink,
    )
    renderStreamBlock(parent, toolBlock('t-done', 'Grep'), null, null, sink)
    expect(sink.onThinkingActive).toHaveBeenCalledTimes(1)
    expect(sink.onToolRunning).toHaveBeenCalledTimes(0)
  })
})
