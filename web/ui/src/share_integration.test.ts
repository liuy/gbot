import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat } from './chat'
import { t } from './i18n'

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
const bridgeShareMd = vi.fn()

// Full long-press gesture on a native row (tool output, thinking, chrome):
// returns the contextmenu event so tests can assert it passed through to
// the system selection toolbar.
function pressNative(el: HTMLElement): MouseEvent {
  el.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientY: 0 }))
  vi.advanceTimersByTime(450)
  const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 })
  el.dispatchEvent(e)
  return e
}

beforeEach(() => {
  vi.useFakeTimers()
  listeners.clear()
  sent.length = 0
  domToPngMock.mockReset()
  domToPngMock.mockResolvedValue('data:image/png;base64,QUJD')
  copyTextMock.mockReset()
  copyTextMock.mockResolvedValue()
  bridgeShare.mockClear()
  bridgeShareMd.mockClear()
  ;(window as unknown as Record<string, unknown>).GBotShare = {
    shareImage: bridgeShare,
    shareText: bridgeShareMd,
  }
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

    const text = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    text.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    // The browser's long-press moment: contextmenu is the primary open path.
    text.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const menu = chat.root.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    const items = chat.root.querySelectorAll('button.share-menu-item')
    expect(items).toHaveLength(3)

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

  it('long-press, markdown item hands the raw source text to bridge.shareText without rasterizing', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [historyMsg({ text: '# Title\n\n**bold** reply' })],
      nextCursor: '',
      hasMore: false,
    })
    const text = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    text.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    text.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    const mdItem = chat.root.querySelector(
      `button.share-menu-item[aria-label="${t('shareMenuMarkdown')}"]`,
    ) as HTMLElement
    expect(mdItem).not.toBeNull()
    mdItem.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    // Sync path, no rAF gate: two microtask turns settle the toast.
    await Promise.resolve()
    await Promise.resolve()
    expect(bridgeShareMd).toHaveBeenCalledTimes(1)
    expect(bridgeShareMd.mock.calls[0][0]).toBe('# Title\n\n**bold** reply')
    expect(bridgeShareMd.mock.calls[0][1]).toMatch(/^gbot-share-\d{4}-\d{2}-\d{2}-\d{6}\.md$/)
    // The .md share must never rasterize.
    expect(domToPngMock).toHaveBeenCalledTimes(0)
    expect(chat.root.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
    chat.cleanup()
  })

  it('lift after expiry opens the menu (non-text fallback path)', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({ type: 'history', messages: [historyMsg()], nextCursor: '', hasMore: false })
    // Chrome presses open nothing now — the text block carries the float
    // capsule, and the LIFT (not contextmenu) is the path under test.
    const text = chat.root.querySelector('[data-block-kind="text"]') as HTMLElement
    text.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    text.dispatchEvent(new MouseEvent('pointerup', { bubbles: true, clientY: 0 }))
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

  it('long-press the tool block of a done message stays native', () => {
    const chat = mountToolTextChat()
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    expect(toolRoot).not.toBeNull()
    const e = pressNative(toolRoot)
    expect(e.defaultPrevented).toBe(false)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-copy-inline')).toBeNull()
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
    expect(chat.root.querySelectorAll('button.share-menu-item')).toHaveLength(3)
    expect(chat.root.querySelectorAll('.share-vsep')).toHaveLength(1)
    chat.cleanup()
  })

  it('long-press the tool HEADER of a streaming message stays native', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Bash' } }])
    events([{ type: 'tool_end', tool_result: { tool_use_id: 't-1', display_output: 'partial output' } }])
    // The header span is the role=button collapse toggle wrapping the
    // command text — the exact area a user long-presses on device.
    const header = chat.root.querySelector('[data-tool-header]') as HTMLElement
    expect(header).not.toBeNull()
    const e = pressNative(header)
    expect(e.defaultPrevented).toBe(false)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-copy-inline')).toBeNull()
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

  it('long-press a reminder-row message stays native', () => {
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
    const e = pressNative(row!.querySelector('.font-mono') as HTMLElement)
    expect(e.defaultPrevented).toBe(false)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-copy-inline')).toBeNull()
    chat.cleanup()
  })
})

describe('share integration: mini capsule over tool output', () => {
  // Same shape as the per-block suite's mountToolTextChat (local to that
  // describe): a done message with a Bash block and a trailing text block.
  function mountToolText(): ReturnType<typeof mount> {
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

  // Tool body renders on expand (render-on-visible fold), so open it before
  // any selection can live inside the output text.
  function expandedToolBody(chat: ReturnType<typeof mount>): HTMLElement {
    const toolRoot = chat.root.querySelector('[data-tool-root]') as HTMLElement
    ;(toolRoot.querySelector('[data-tool-header]') as HTMLElement).click()
    return toolRoot.querySelector('.md-body') as HTMLElement
  }

  // Live selection over the tool output's first text node, with the rect
  // stub jsdom never provides.
  function selectToolText(body: HTMLElement): void {
    const walker = document.createTreeWalker(body, NodeFilter.SHOW_TEXT)
    const node = walker.nextNode() as Text
    const range = document.createRange()
    range.setStart(node, 0)
    range.setEnd(node, node.data.length)
    window.getSelection()?.removeAllRanges()
    window.getSelection()?.addRange(range)
    Object.defineProperty(range, 'getBoundingClientRect', {
      value: () =>
        ({
          top: 200, left: 40, width: 100, height: 20, right: 140, bottom: 220,
          x: 40, y: 200, toJSON: () => ({}),
        }) as DOMRect,
    })
  }

  it('a tool-output selection floats the mini and copies through the real chat wiring', async () => {
    const chat = mountToolText()
    const body = expandedToolBody(chat)
    selectToolText(body)
    document.dispatchEvent(new Event('selectionchange'))
    vi.advanceTimersByTime(16)
    const mini = chat.root.querySelector('.share-mini') as HTMLElement
    expect(mini).not.toBeNull()
    expect(mini.querySelectorAll('button.share-menu-item')).toHaveLength(1)
    mini.querySelector('button')!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    expect(copyTextMock).toHaveBeenCalledWith('total 0')
    expect(chat.root.querySelector('.share-toast')?.textContent).toBe(t('shareCopied'))
    chat.cleanup()
  })

  it('unarmed long-press on a tool block with a live selection is prevented', () => {
    const chat = mountToolText()
    const body = expandedToolBody(chat)
    selectToolText(body)
    body.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, cancelable: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 })
    body.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-mini')).not.toBeNull()
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
    // Copy is the third item when share is present.
    items[2].dispatchEvent(new MouseEvent('click', { bubbles: true }))
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
    items[2].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await Promise.resolve()
    await Promise.resolve()
    expect(copyTextMock).toHaveBeenCalledWith('# Real **md**')
    chat.cleanup()
  })

  it('a TRAILING abandoned text_start leaves a chrome press opening nothing', async () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    events([{ type: 'query_start' }])
    events([{ type: 'text_start' }])
    events([{ type: 'text_delta', text: 'the reply' }])
    // Trailing bare text_start: never receives a delta before the turn ends.
    events([{ type: 'text_start' }])
    events([{ type: 'query_end' }])
    // The commit-side empty-block filter drops it from the model; the chrome
    // fallback is gone, so a chrome press must open nothing at all.
    const row = messageRow(chat)
    row!.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true, clientY: 0 }))
    vi.advanceTimersByTime(450)
    row!.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 0 }))
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    chat.cleanup()
  })
})

describe('share integration: thinking blocks', () => {
  it('long-press a thinking block of a done message stays native', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'history',
      messages: [
        historyMsg({
          text: '',
          blocks: [
            { kind: 'thinking', thinking: { text: '**why** not because', durationNs: 1_000_000_000 } },
            { kind: 'text', text: 'reply' },
          ],
        }),
      ],
      nextCursor: '',
      hasMore: false,
    })
    // The wrap's only role=button child is the collapse header — collapsed,
    // it is the only pressable area of a finished block.
    const header = chat.root.querySelector('[data-thinking] > [role="button"]') as HTMLElement
    expect(header).not.toBeNull()
    const e = pressNative(header)
    expect(e.defaultPrevented).toBe(false)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-copy-inline')).toBeNull()
    chat.cleanup()
  })

  it('sub-agent thinking stays native', () => {
    const chat = mount()
    dispatch({ type: 'connect_status', connected: true })
    // Press must land mid-flight: tool_end with children collapses (and
    // clears) the children DOM, destroying the sub-agent thinking wrap.
    events([{ type: 'query_start' }])
    events([{ type: 'tool_start', tool_use: { id: 't-1', name: 'Agent' } }])
    events([{ type: 'thinking_start', agent: { parent_tool_use_id: 't-1' } }])
    events([
      {
        type: 'thinking_delta',
        thinking: { text: 'child reasoning' },
        agent: { parent_tool_use_id: 't-1' },
      },
    ])
    events([{ type: 'tool_output_delta', tool_result: { tool_use_id: 't-1', display_output: 'parent output' } }])
    const childThinking = chat.root.querySelector('[data-tool-children] [data-thinking]') as HTMLElement
    expect(childThinking).not.toBeNull()
    const e = pressNative(childThinking)
    expect(e.defaultPrevented).toBe(false)
    expect(chat.root.querySelector('.share-menu')).toBeNull()
    expect(chat.root.querySelector('.share-copy-inline')).toBeNull()
    chat.cleanup()
  })
})
