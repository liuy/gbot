import { describe, it, expect, beforeEach, vi } from 'vitest'
import {
  appendTextBlock,
  appendUserBlock,
  appendThinkingBlock,
  appendToolBlock,
  appendProgressBar,
  createUserTextSpan,
  finishTool,
} from '../components/stream_dom'
import { createPopupPanel, createAnchoredPopup } from '../utils'
import { createChat } from '../chat'
import { avatarBase, avatarG, avatarU } from './recipes'

type Listener = (msg: unknown) => void
const listeners: Set<Listener> = new Set()
vi.mock('../ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    subscribeBinary: () => () => {},
    send: () => {},
    connected: true,
  }),
}))

function dispatch(msg: unknown): void {
  listeners.forEach((fn) => fn(msg))
}

function mount(): ReturnType<typeof createChat> {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  return chat
}

beforeEach(() => {
  listeners.clear()
  document.body.innerHTML = ''
})

describe('stream_dom className snapshot guards', () => {
  describe('appendTextBlock', () => {
    it('div.className matches current literal', () => {
      const parent = document.createElement('div')
      const div = appendTextBlock(parent)
      expect(div.className).toBe('md-body md-text text-t1 text-[15px] break-words')
    })
  })

  describe('appendUserBlock', () => {
    it('div and inner span className match current literals', () => {
      const parent = document.createElement('div')
      const div = appendUserBlock(parent, 'hi')
      const span = div.querySelector('span')!
      expect(div.className).toBe('text-[13px] text-t2 italic ml-2 my-1')
      expect(span.className).toBe('whitespace-pre-wrap break-words')
    })
  })

  describe('createUserTextSpan', () => {
    it('span.className matches current literal', () => {
      const span = createUserTextSpan('hi')
      expect(span.className).toBe('whitespace-pre-wrap break-words')
    })
  })

  describe('appendThinkingBlock', () => {
    it('header / prefix / glyph / label / p / chevron svg class match current literals', () => {
      const parent = document.createElement('div')
      const { p, labelEl } = appendThinkingBlock(parent, Date.now())
      const header = labelEl.parentElement as HTMLElement
      const prefix = header.firstElementChild as HTMLElement
      const glyph = prefix.firstElementChild as HTMLElement
      const chevronSpan = prefix.children[1] as HTMLElement
      const svg = chevronSpan.querySelector('svg') as SVGElement

      expect(header.className).toBe('flex items-baseline cursor-pointer bg-transparent border-0 p-0 text-left')
      expect(prefix.className).toBe('shrink-0 w-6')
      expect(glyph.className).toBe('text-amber text-sm inline-block w-3 text-center heartbeat')
      expect(labelEl.className).toBe('text-amber text-sm')
      expect(p.className).toBe('md-body md-text ml-6 text-t2 text-sm break-words')
      expect(svg.getAttribute('class')).toBe('inline-block align-middle text-t3 transition-transform rotate-90')
    })
  })

  describe('appendToolBlock', () => {
    it('header / prefix / dot / content / chevron / name / summary / dur / body / children match', () => {
      const parent = document.createElement('div')
      const h = appendToolBlock(parent, 'Bash')
      const prefix = h.dot.parentElement as HTMLElement
      const chevronSpan = prefix.children[1] as HTMLElement
      const svg = chevronSpan.querySelector('svg') as SVGElement
      const content = h.header.children[1] as HTMLElement
      const nameEl = content.children[0] as HTMLElement
      const summaryEl = content.children[1] as HTMLElement

      expect(h.header.className).toBe('flex items-baseline cursor-pointer bg-transparent border-0 p-0 text-left')
      expect(prefix.className).toBe('shrink-0 w-6')
      expect(h.dot.className).toBe('text-[10px] leading-none align-middle inline-block w-3 text-center text-white heartbeat')
      expect(svg.getAttribute('class')).toBe('inline-block align-middle text-t3 transition-transform')
      expect(content.className).toBe('flex-1 min-w-0')
      expect(nameEl.className).toBe('font-mono text-sm text-blue')
      expect(summaryEl.className).toBe('text-sm text-t2 font-light break-all whitespace-pre-wrap')
      expect(h.durEl.className).toBe('font-mono text-xs text-blue')
      expect(h.body.className).toBe('md-body ml-6 font-mono text-sm leading-relaxed text-t2 overflow-x-auto break-words hidden')
      expect(h.childrenContainer.className).toBe('ml-6 mt-1 space-y-1 border-l border-t3/30 pl-2 hidden')
    })
  })

  describe('finishTool durEl', () => {
    it('success: className ends with text-t3', () => {
      const parent = document.createElement('div')
      const h = appendToolBlock(parent, 'Bash')
      finishTool(h, { isError: false, durationNs: 0, output: '' })
      expect(h.durEl.className).toBe('font-mono text-xs text-t3')
    })

    it('error: className ends with text-red', () => {
      const parent = document.createElement('div')
      const h = appendToolBlock(parent, 'Bash')
      finishTool(h, { isError: true, durationNs: 0, output: '' })
      expect(h.durEl.className).toBe('font-mono text-xs text-red')
    })
  })

  describe('appendProgressBar', () => {
    it('root and dotEl className match current literals', () => {
      const parent = document.createElement('div')
      const h = appendProgressBar(parent)
      expect(h.root.className).toBe('mt-2 flex items-center gap-1 overflow-x-auto overflow-y-hidden whitespace-nowrap text-xs text-t3')
      expect(h.dotEl.className).toBe('text-[10px] leading-none align-middle inline-block w-3 text-center text-blue heartbeat')
    })
  })

  describe('createGroupContainer (via two collapsible appendToolBlock)', () => {
    it('group summary / duration / toolsContainer className match', () => {
      const parent = document.createElement('div')
      appendToolBlock(parent, 'Web', null, true)
      appendToolBlock(parent, 'Web', null, true)
      const group = parent.querySelector('[data-tool-group]') as HTMLElement
      const summary = group.querySelector('[data-group-summary]') as HTMLElement
      const duration = group.querySelector('[data-group-duration]') as HTMLElement
      const toolsContainer = group.querySelector('[data-group-tools]') as HTMLElement

      expect(summary.className).toBe('font-mono text-sm text-blue')
      expect(duration.className).toBe('font-mono text-xs text-t3')
      expect(toolsContainer.className).toBe('ml-6 hidden')
    })
  })
})

describe('chat.ts className snapshot guards (via createChat + dispatch)', () => {
  it('scrollBtn.className matches recipe output (floatingButton center)', () => {
    mount()
    const scrollBtn = Array.from(document.querySelectorAll('button'))
      .find((b) => b.className.includes('bottom-24') && b.className.includes('left-1/2')) as HTMLElement
    expect(scrollBtn.className).toBe('flex h-11 w-11 items-center justify-center rounded-full bg-transparent opacity-0 pointer-events-none transition-all duration-200 text-blue z-50 absolute bottom-24 left-1/2 -translate-x-1/2')
  })

  it('disconnectBanner and dcText className match current literals', () => {
    mount()
    const banner = Array.from(document.querySelectorAll('div'))
      .find((d) => d.className.includes('top-11') && d.className.includes('card-bg')) as HTMLElement
    expect(banner.className).toBe('absolute top-11 inset-x-0 z-50 card-bg border-b border-hairline px-4 py-1.5 flex items-center justify-center transition-all duration-300 overflow-hidden max-h-0 opacity-0')
    const dcText = banner.querySelector(':scope > span') as HTMLElement
    expect(dcText.className).toBe('text-[12px] text-red')
  })

  it('error box (assistant error from history) matches current literal', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [{
        id: 'a-1', role: 'assistant', text: '', thinking: [], tools: [], blocks: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: 'boom', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const err = Array.from(document.querySelectorAll('.text-red'))
      .find((el) => el.className.includes('border-red/40')) as HTMLElement
    expect(err.className).toBe('rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red break-all')
  })

  it('compact divider container / hairlines / label className match', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [{
        id: 'b1', role: 'system', compactBoundary: true, text: '', thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const container = Array.from(document.querySelectorAll('div'))
      .find((d) => d.className === 'flex items-center gap-2 my-4 px-4') as HTMLElement
    expect(container.className).toBe('flex items-center gap-2 my-4 px-4')
    const left = container.children[0] as HTMLElement
    const label = container.children[1] as HTMLElement
    const right = container.children[2] as HTMLElement
    expect(left.className).toBe('flex-1 border-t border-hairline')
    expect(label.className).toBe('text-blue text-[10px] shrink-0')
    expect(right.className).toBe('flex-1 border-t border-hairline')
  })

  it('time divider container / label className match', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [{
        id: 'u-1', role: 'user', text: 'hi', thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const container = Array.from(document.querySelectorAll('div'))
      .find((d) => d.className === 'flex justify-center items-center my-4 px-4') as HTMLElement
    expect(container.className).toBe('flex justify-center items-center my-4 px-4')
    const label = container.children[0] as HTMLElement
    expect(label.className).toBe('text-blue text-[10px] shrink-0')
  })

  it('assistant shell: outer / grid / centerCol / content className match', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [{
        id: 'a-1', role: 'assistant', text: 'hi', thinking: [], tools: [], blocks: [{ kind: 'text', text: 'hi' }],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const outer = Array.from(document.querySelectorAll('div'))
      .find((d) => d.className === 'px-1.5') as HTMLElement
    expect(outer.className).toBe('px-1.5')
    const grid = outer.firstElementChild as HTMLElement
    expect(grid.className).toBe('grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5')
    const centerCol = grid.children[1] as HTMLElement
    expect(centerCol.className).toBe('min-w-0')
    const content = centerCol.firstElementChild as HTMLElement
    expect(content.className).toBe('space-y-3')
  })

  it('user shell: content className matches contentArea user output', () => {
    mount()
    dispatch({
      type: 'history',
      messages: [{
        id: 'u-1', role: 'user', text: 'hello', thinking: [], tools: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: '', status: 'done', startedAt: 0,
      }],
      nextCursor: '', hasMore: false,
    })
    const outer = Array.from(document.querySelectorAll('div'))
      .find((d) => d.className === 'px-1.5') as HTMLElement
    const grid = outer.firstElementChild as HTMLElement
    const content = (grid.children[1] as HTMLElement).firstElementChild as HTMLElement
    expect(content.className).toBe('ml-auto max-w-fit text-left text-t1 text-[15px] whitespace-pre-wrap break-words')
  })
})

describe('avatar recipes (each tested independently, not as caller concatenation)', () => {
  it('avatarBase() output', () => {
    expect(avatarBase()).toBe('flex h-5 w-5 shrink-0 items-center justify-center rounded-md')
  })

  it('avatarG() output', () => {
    expect(avatarG()).toBe('text-[11px] font-bold avatar-g-bg')
  })

  it('avatarU() output', () => {
    expect(avatarU()).toBe('bg-gradient-to-br from-t2 to-t3')
  })
})

describe('task_panel.ts className snapshot guards', () => {
  it('taskPanel root className matches recipe output (floatingButton right)', () => {
    mount()
    dispatch({
      type: 'task_list',
      tasks: [{ id: 't1', subject: 'do thing', status: 'in_progress' }],
    })
    const taskPanel = Array.from(document.querySelectorAll('button'))
      .find((b) => b.className.includes('bottom-24') && b.className.includes('right-5')) as HTMLElement
    expect(taskPanel.className).toBe('flex h-11 w-11 items-center justify-center rounded-full bg-transparent opacity-0 pointer-events-none transition-all duration-200 text-blue z-50 absolute bottom-24 right-5')
  })
})

describe('utils.ts className snapshot guards', () => {
  it('createPopupPanel (top, no class) matches current literal', () => {
    const panel = createPopupPanel()
    expect(panel.className).toBe('bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl modal-enter z-40 hidden w-[90vw] max-w-sm fixed left-1/2 -translate-x-1/2 top-12')
  })

  it('createPopupPanel (bottom, no class) matches current literal', () => {
    const panel = createPopupPanel({ bottom: true })
    expect(panel.className).toBe('bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl modal-enter z-40 hidden w-[90vw] max-w-sm fixed left-1/2 -translate-x-1/2 bottom-20')
  })

  it('createPopupPanel (bottom, className) — tailwind-merge drops left-1/2 -translate-x-1/2', () => {
    const panel = createPopupPanel({ bottom: true, className: 'right-5 left-auto translate-x-0' })
    expect(panel.className).toBe('bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl modal-enter z-40 hidden w-[90vw] max-w-sm fixed bottom-20 right-5 left-auto translate-x-0')
  })

  it('createAnchoredPopup (no class) matches current literal', () => {
    const panel = createAnchoredPopup()
    expect(panel.className).toBe('fixed hidden z-40 bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl')
  })

  it('createAnchoredPopup (with class) matches current literal', () => {
    const panel = createAnchoredPopup('extra-class')
    expect(panel.className).toBe('fixed hidden z-40 bg-ink2/75 backdrop-blur-[20px] backdrop-saturate-[1.5] border border-hairline rounded-xl shadow-2xl extra-class')
  })
})
