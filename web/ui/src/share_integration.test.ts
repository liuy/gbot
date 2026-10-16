import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (chat.ts header/prefetch paths).
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
    subscribeBinary: () => () => {},
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
    onStateChange: () => {},
    reconnect: () => {},
  }),
}))

// Full-chain integration: only the rasterizer and the clipboard are mocked —
// the markdown pipeline, the offscreen card, the bridge probe and the menu
// all run real.
const domToPngMock = vi.hoisted(() => vi.fn())
vi.mock('modern-screenshot', () => ({ domToPng: domToPngMock }))
const copyTextMock = vi.hoisted(() => vi.fn())
vi.mock('./utils/copy_button', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./utils/copy_button')>()
  return { ...actual, copyText: copyTextMock }
})

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

// Query lifecycle events ride the ws 'event' envelope; history and
// connect_status are top-level.
function events(es: unknown[]): void {
  for (const e of es) dispatch({ type: 'event', event: e })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

function historyMsg(overrides: Record<string, unknown> = {}) {
  return {
    id: 'm-1',
    role: 'assistant',
    text: 'hello **world**',
    thinking: [],
    tools: [],
    usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '',
    status: 'done',
    startedAt: new Date(2026, 9, 15, 10, 50).getTime(),
    ...overrides,
  }
}

function messageRow(chat: ReturnType<typeof mount>): HTMLElement | null {
  const container = chat.root.querySelector('.space-y-7')
  if (!container) return null
  return (Array.from(container.children).find((c) => c.querySelector('.md-body')) ??
    null) as HTMLElement | null
}

function rowWith(chat: ReturnType<typeof mount>, selector: string): HTMLElement | null {
  const container = chat.root.querySelector('.space-y-7')
  if (!container) return null
  return (Array.from(container.children).find((c) => c.querySelector(selector)) ??
    null) as HTMLElement | null
}

// Tool block in the wire history shape (HistoryBlockTool): a finished Bash
// call with rendered output text.
function toolHistoryBlock(displayOutput = 'total 0\nfile-a\nfile-b') {
  return {
    kind: 'tool',
    tool: { id: 't-1', name: 'Bash', summary: 'ls', displayOutput, durationNs: 5_000_000_000 },
  }
}

const bridgeShare = vi.fn()

beforeEach(() => {
  vi.useFakeTimers()
  listeners.clear()
  sent.length = 0
  domToPngMock.mockReset()
  domToPngMock.mockResolvedValue('data:image/png;base64,QUJD')
  copyTextMock.mockReset()
  copyTextMock.mockResolvedValue()
  bridgeShare.mockClear()
  ;(window as unknown as Record<string, unknown>).GBotShare = { shareImage: bridgeShare }
})

afterEach(() => {
  delete (window as unknown as Record<string, unknown>).GBotShare
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('share integration: entry → menu → capture → bridge', () => {
  it('long-press a done assistant message, share item hands a PNG to the bridge', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg()],
      nextCursor: '',
      hasMore: false,
    })
    const row = messageRow(chat)
    expect(row?.textContent).toContain('hello')

    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    // The browser's long-press moment: contextmenu is the primary open path.
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const menu = chat.root.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    const items = chat.root.querySelectorAll('button.share-menu-item')
    expect(items).toHaveLength(2)

    // rAF is faked with the timers: advance two frames so the offscreen
    // capture's double-rAF gate opens, then let microtasks settle.
    items[0].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.advanceTimersByTimeAsync(16)
    await vi.advanceTimersByTimeAsync(16)
    await Promise.resolve()
    await Promise.resolve()

    expect(bridgeShare).toHaveBeenCalledTimes(1)
    expect(bridgeShare.mock.calls[0][0]).toBe('data:image/png;base64,QUJD')
    expect(bridgeShare.mock.calls[0][1]).toMatch(/^gbot-share-\d{4}-\d{2}-\d{2}-\d{6}\.png$/)
    expect(domToPngMock).toHaveBeenCalledTimes(1)
    chat.cleanup()
  })

  it('lift after expiry opens the menu (non-text fallback path)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [historyMsg()], nextCursor: '', hasMore: false })
    const row = messageRow(chat)
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    chat.cleanup()
  })

  it('streaming messages cannot be long-pressed into a menu', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg({ status: 'streaming' })],
      nextCursor: '',
      hasMore: false,
    })
    const row = messageRow(chat)
    expect(row).not.toBeNull()
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    row!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })
})

describe('share integration: per-block press resolution', () => {
  function mountToolTextChat(): ReturnType<typeof mount> {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        historyMsg({
          text: '',
          blocks: [toolHistoryBlock(), { kind: 'text', text: 'final **reply**' }],
        }),
      ],
      nextCursor: '',
      hasMore: false,
    })
    return chat
  }

  it('long-press the tool block of a done message yields a copy-only menu', () => {
    const chat = mountToolTextChat()
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).not.toBeNull()
    toolRoot.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    toolRoot.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const menu = chat.root.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(1)
    expect(chat.root.querySelector('.share-vsep')).toBeNull()
    chat.cleanup()
  })

  it('long-press a tool GROUP header opens nothing', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    // Groups form only on the live path: two consecutive collapsible tools
    // (Grep) group under one header mid-turn, and the live DOM SURVIVES the
    // query_end commit (history reload renders flat — only this shape has
    // the group header).
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Grep' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-1', display_output: 'grep hit a' } }])
    events([{ type: 'tool_start', tool_use: { id: 't-2', name: 'Grep' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-2', display_output: 'grep hit b' } }])
    events([{ type: 'text_delta', text: 'final **reply**' }])
    events([{ type: 'query_end' }])
    const groupHeader = chat.root.querySelector('[data-group-header]') as HTMLElement
    expect(groupHeader).not.toBeNull()
    groupHeader.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    groupHeader.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    // A group header is a collapse toggle, not a copy surface — and never a
    // share trigger for the reply it happens to sit above.
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })

  it('long-press the text block of the same message yields share + copy', () => {
    const chat = mountToolTextChat()
    // The tool body also renders an .md-body (the tool output); the text
    // block is the marked one.
    const md = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    expect(md).not.toBeNull()
    md.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    md.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(2)
    expect(chat.root.querySelectorAll('.share-vsep')).toHaveLength(1)
    chat.cleanup()
  })

  it('long-press the tool block of a STREAMING message still yields copy-only', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    // Real mid-flight shape: query events, not a synthetic history frame —
    // the Go relay never emits streaming history with blocks (m.blocks is
    // [] until query_end; the blocks live in the streaming pending list).
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-1', display_output: 'partial output' } }])
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).not.toBeNull()
    toolRoot.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    toolRoot.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(1)
    chat.cleanup()
  })
  it('long-press the tool HEADER of a streaming message yields copy-only (device repro)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-1', display_output: 'partial output' } }])
    // The header span is the role=button collapse toggle wrapping the
    // command text — the exact area a user long-presses on device.
    const header = chat.root.querySelector('[data-tool-header]') as HTMLElement
    expect(header).not.toBeNull()
    header.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    header.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(1)
    chat.cleanup()
  })

  it('mid-flight tool copy hands over the committed displayOutput', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-1', display_output: 'partial output' } }])
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    toolRoot.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    toolRoot.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    ;(chat.root.querySelector('button.share-menu-item') as HTMLElement).dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await Promise.resolve()
    await Promise.resolve()
    expect(copyTextMock).toHaveBeenCalledWith('partial output')
    chat.cleanup()
  })

  it('long-press the text block of a STREAMING message yields nothing', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'text_delta', text: 'forming reply' }])
    const md = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    expect(md).not.toBeNull()
    md.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    md.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })

  it('long-press the chrome of a tool-only message opens nothing', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg({ text: '', blocks: [toolHistoryBlock()] })],
      nextCursor: '',
      hasMore: false,
    })
    const row = rowWith(chat, '[data-tool-root]')
    expect(row).not.toBeNull()
    // Pressing the row itself (avatar/padding chrome, no block element).
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    row!.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })

  it('long-press a reminder-row message yields a copy-only menu', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg({ role: 'user', text: '<message from="e2">\nhi there\n</message>' })],
      nextCursor: '',
      hasMore: false,
    })
    const row = rowWith(chat, '[data-reminder-row]')
    expect(row).not.toBeNull()
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const menu = chat.root.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(1)
    expect(chat.root.querySelector('.share-vsep')).toBeNull()
    chat.cleanup()
  })
})

describe('share integration: live-streaming pairing', () => {
  // Real reminder + tools turn: a drained reminder fires mid-stream (an
  // attachment event carrying the persisted envelope), then two tools run
  // and query_end commits. This is the shape where the reminder row — built
  // through the tool-block path — used to shift the DOM↔model tool pairing
  // and copy the WRONG tool's output.
  function mountReminderToolsTurn(): ReturnType<typeof mount> {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([
      {
        type: 'attachment',
        message: {
          timestamp: new Date(2026, 9, 15, 10, 50).toISOString(),
          attachment: { prompt: '<message from="alice">\nhello from alice\n</message>' },
        },
      },
    ])
    events([{ type: 'tool_start', tool_use: { id: 'ta', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 'ta', display_output: 'output-A' } }])
    events([{ type: 'tool_start', tool_use: { id: 'tb', name: 'Read' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 'tb', display_output: 'output-B' } }])
    events([{ type: 'query_end' }])
    return chat
  }

  async function pressAndCopy(el: HTMLElement, chat: ReturnType<typeof mount>): Promise<void> {
    el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    el.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    ;(chat.root.querySelector('button.share-menu-item') as HTMLElement).dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await Promise.resolve()
    await Promise.resolve()
  }

  it('each tool copies its OWN output when a reminder row precedes them', async () => {
    const chat = mountReminderToolsTurn()
    // The reminder row is also a tool root (built through the shared tool
    // path) — exclude it so tools[0]/[1] are the real Bash/Read cards.
    const tools = chat.root.querySelectorAll('[data-tool-root]:not([data-reminder-row])')
    expect(tools).toHaveLength(2)
    // Pre-fix, counting the reminder row in the DOM tool list shifted every
    // index: pressing tool A copied 'output-B'.
    await pressAndCopy(tools[0] as HTMLElement, chat)
    expect(copyTextMock).toHaveBeenCalledWith('output-A')
    copyTextMock.mockClear()
    await pressAndCopy(tools[1] as HTMLElement, chat)
    expect(copyTextMock).toHaveBeenCalledWith('output-B')
    chat.cleanup()
  })

  it('pressing the mid-turn reminder row copies its decoded body', async () => {
    const chat = mountReminderToolsTurn()
    const reminder = chat.root.querySelector('[data-reminder-row]') as HTMLElement
    expect(reminder).not.toBeNull()
    await pressAndCopy(reminder, chat)
    expect(copyTextMock).toHaveBeenCalledWith('hello from alice')
    chat.cleanup()
  })
})

describe('share integration: markdown source fidelity', () => {
  it('copying a markdown text block hands over the RAW model text', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        historyMsg({
          text: '',
          blocks: [toolHistoryBlock(), { kind: 'text', text: 'final **reply**' }],
        }),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const md = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    md.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    md.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const items = chat.root.querySelectorAll('button.share-menu-item')
    // Copy is the second item when share is present.
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    // Syntax intact — a textContent fallback would hand over 'final reply'.
    expect(copyTextMock).toHaveBeenCalledWith('final **reply**')
    chat.cleanup()
  })

  it('an abandoned empty text_start does not shift the text-block pairing', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'text_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't1', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't1', display_output: 'done-out' } }])
    events([{ type: 'text_start' }])
    events([{ type: 'text_delta', text: '# Real **md**' }])
    events([{ type: 'query_end' }])
    const divs = chat.root.querySelectorAll('[data-block-kind="text"]')
    // The abandoned div is gone after commit; only the real one pairs.
    expect(divs).toHaveLength(1)
    divs[0].dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    divs[0].dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const items = chat.root.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    expect(copyTextMock).toHaveBeenCalledWith('# Real **md**')
    chat.cleanup()
  })

  it('a TRAILING abandoned text_start still lets chrome share the reply', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'text_start' }])
    events([{ type: 'text_delta', text: 'the reply' }])
    // Trailing bare text_start: never receives a delta before the turn ends.
    events([{ type: 'text_start' }])
    events([{ type: 'query_end' }])
    // The commit-side empty-block filter drops it from the model; without
    // that filter the empty block is the LAST text block and the chrome
    // fallback (extractShareText) finds nothing → no menu.
    const row = messageRow(chat)
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).not.toBeNull()
    chat.cleanup()
  })

  it('a tool with blank displayOutput falls back to the share capsule', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        historyMsg({
          text: '',
          blocks: [toolHistoryBlock(''), { kind: 'text', text: 'reply' }],
        }),
      ],
      nextCursor: '',
      hasMore: false,
    })
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).not.toBeNull()
    toolRoot.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    toolRoot.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    // Blank output → copy-only would be a lie; the reply fallback engages
    // instead (two items + separator).
    const items = chat.root.querySelectorAll('button.share-menu-item')
    expect(items).toHaveLength(2)
    expect(chat.root.querySelector('.share-vsep')).not.toBeNull()
    chat.cleanup()
  })
})
