import { describe, it, expect, beforeEach, vi } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'
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

const css = readFileSync(resolve(__dirname, 'index.css'), 'utf-8')

type Listener = (msg: any) => void
const listeners: Set<Listener> = new Set()
const sent: any[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: any) => sent.push(p),
    connected: true,
  }),
}))

function dispatch(msg: any) {
  listeners.forEach((fn) => fn(msg))
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

function assistantWithBlocks(
  id: string,
  blocks: any[],
  error = '',
) {
  return {
    id,
    role: 'assistant',
    text: '',
    thinking: [],
    tools: [],
    blocks,
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error,
    status: 'done',
    startedAt: 0,
  }
}

function userMsg(id: string, text: string) {
  return {
    id,
    role: 'user',
    text,
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done',
    startedAt: 0,
  }
}

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('committed message layout (via loadHistory)', () => {
  it('assistant message uses 3-col grid with avatarG + min-w-0 content', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [{ kind: 'text', text: 'hi' }]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const messagesContainer = document.getElementsByClassName(
      'space-y-7',
    )[0] as HTMLElement
    const outer = messagesContainer.firstElementChild as HTMLElement
    expect(outer.className).toContain('px-1.5')
    const grid = outer.firstElementChild as HTMLElement
    expect(grid.className).toContain('grid-cols-[1.25rem_1fr_1.25rem]')
    const left = grid.firstElementChild as HTMLElement
    expect(left.textContent).toBe('G')
    const center = grid.children[1] as HTMLElement
    expect(center.className).toContain('min-w-0')
  })

  it('user message has right-aligned content with person avatar', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [userMsg('u-1', 'hello user')],
      nextCursor: '',
      hasMore: false,
    })
    const content = document.querySelector('.ml-auto') as HTMLElement
    expect(content).toBeTruthy()
    expect(content.textContent).toContain('hello user')
    const messagesContainer = document.getElementsByClassName(
      'space-y-7',
    )[0] as HTMLElement
    const outer = messagesContainer.firstElementChild as HTMLElement
    const grid = outer.firstElementChild as HTMLElement
    const rightCol = grid.children[2]
    expect(rightCol.querySelector('svg')).toBeTruthy()
  })

  it('assistant text block renders via markdown (.md-body with <p>)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [{ kind: 'text', text: '**bold**' }]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const mdBody = document.querySelector('.md-body') as HTMLElement
    expect(mdBody.querySelector('strong')?.textContent).toBe('bold')
  })

  it('assistant error block renders red-bordered div', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [], 'something broke'),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const red = document.querySelector('.text-red') as HTMLElement
    expect(red).toBeTruthy()
    expect(red.className).toContain('border-red/40')
    expect(red.textContent).toBe('something broke')
  })

  it('tool block renders with data-tool-root + finishTool green dot', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          {
            kind: 'tool',
            tool: {
              id: 't-1',
              name: 'Bash',
              summary: 'echo',
              displayOutput: 'done',
              isError: false,
              durationNs: 1_000_000_000,
            },
          },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const toolRoot = document.querySelector(
      '[data-tool-root]',
    ) as HTMLElement
    expect(toolRoot).toBeTruthy()
    expect(toolRoot.dataset.toolName).toBe('Bash')
    // Finished non-error tool → green dot.
    const dot = toolRoot.querySelector('.text-green')
    expect(dot).toBeTruthy()
  })

  it('thinking block renders collapsed after finishThinking', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          {
            kind: 'thinking',
            thinking: { text: 'pondered', durationNs: 5_000_000_000 },
          },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // Collapsed <p> has hidden class.
    const p = document.querySelector('[data-thinking] p') as HTMLElement
    expect(p.classList.contains('hidden')).toBe(true)
    expect(p.textContent).toContain('pondered')
  })

  it('two collapsible tools group into a summary header', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          {
            kind: 'tool',
            tool: { id: 't-1', name: 'Grep', durationNs: 0 },
          },
          {
            kind: 'tool',
            tool: { id: 't-2', name: 'Glob', durationNs: 0 },
          },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // streamDom grouping: when the 2nd collapsible appends, it builds a
    // [data-tool-group]. Summary should mention "Searches".
    const group = document.querySelector('[data-tool-group]')
    expect(group).toBeTruthy()
    expect(group?.textContent).toContain('Search')
  })

  it('single collapsible tool renders as bare tool (no group)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          {
            kind: 'tool',
            tool: { id: 't-1', name: 'Grep', durationNs: 0 },
          },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    expect(document.querySelector('[data-tool-group]')).toBeNull()
    expect(
      document.querySelector('[data-tool-root][data-tool-name="Grep"]'),
    ).toBeTruthy()
  })

  it('non-collapsible tool breaks a group (Bash between two Greps)', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          { kind: 'tool', tool: { id: 't-1', name: 'Grep', durationNs: 0 } },
          { kind: 'tool', tool: { id: 't-2', name: 'Bash', durationNs: 0 } },
          { kind: 'tool', tool: { id: 't-3', name: 'Glob', durationNs: 0 } },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // Grep is bare; Bash flushes; Glob is bare (prev sibling Bash is
    // non-collapsible). No group should form.
    const groups = document.querySelectorAll('[data-tool-group]')
    expect(groups.length).toBe(0)
    // Three bare tool roots.
    const roots = document.querySelectorAll('[data-tool-root]')
    expect(roots.length).toBe(3)
  })

  it('expanding a tool group reveals its children', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [
        assistantWithBlocks('a-1', [
          { kind: 'tool', tool: { id: 't-1', name: 'Grep', durationNs: 0 } },
          { kind: 'tool', tool: { id: 't-2', name: 'Glob', durationNs: 0 } },
        ]),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const group = document.querySelector('[data-tool-group]') as HTMLElement
    const toolsContainer = group.querySelector(
      '[data-group-tools]',
    ) as HTMLElement
    expect(toolsContainer.style.display).toBe('none')
    const header = group.querySelector('[data-group-header]') as HTMLElement
    header.click()
    expect(toolsContainer.style.display).not.toBe('none')
  })
})

describe('message CSS design tokens', () => {
  it('has card-bg utility', () => {
    expect(css).toContain('card-bg')
  })

  it('has pulse-blue animation utility', () => {
    expect(css).toContain('pulse-blue')
  })

  it('has glow-blue utility', () => {
    expect(css).toContain('glow-blue')
  })
})

describe('app layout (overflow)', () => {
  it('root is flex-col h-dvh, scroll child has overflow-y-auto', () => {
    mount()
    const root = document.body.firstElementChild as HTMLElement
    expect(root.className).toContain('flex')
    expect(root.className).toContain('h-dvh')
    const scroll = root.querySelector('.flex-1.min-h-0') as HTMLElement
    expect(scroll).toBeTruthy()
    expect(scroll.className).toContain('overflow-y-auto')
    expect(scroll.className).toContain('overflow-x-hidden')
  })
})
