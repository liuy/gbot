import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'
import { renderStreamBlock } from './components/stream_blocks'
import { userEchoBlock } from './styles/recipes'
import type { Block } from './types'

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

function setTextarea(value: string) {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.value = value
  ta.dispatchEvent(new Event('input', { bubbles: true }))
}
function pressEnter() {
  const ta = document.querySelector('textarea') as HTMLTextAreaElement
  ta.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter', bubbles: true }))
}

function attachmentEvent(opts: {
  content?: unknown[]
  prompt?: string
}) {
  return {
    type: 'event',
    event: {
      type: 'attachment',
      message: {
        role: 'user',
        content: opts.content ?? [],
        attachment: { prompt: opts.prompt ?? '', source_uuid: '' },
      },
    },
  }
}

const IMAGE_BLOCK = {
  type: 'image',
  source: { type: 'base64', media_type: 'image/png', data: 'aWNl' },
}
const DOC_BLOCK = { type: 'document', name: 'notes.md', mime: 'text/markdown', size: 2048 }

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

describe('attachment streaming', () => {
  it('text_delta after attachment renders', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'see this', source_uuid: '' } },
      },
    })
    events([
      { type: 'text_start' },
      { type: 'text_delta', text: 'response' },
    ])
    expect(document.body.textContent).toContain('see this')
    const mdBody = document.querySelector('.md-body') as HTMLElement
    expect(mdBody.textContent).toContain('response')
  })

  it('attachment event renders user msg with prompt text', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'look here', source_uuid: '' } },
      },
    })
    expect(document.body.textContent).toContain('look here')
  })

  it('attachment after query_end renders as user msg then text_delta in new turn', () => {
    mount()
    setTextarea('q1')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first' },
      { type: 'query_end' },
    ])
    // Attachment between queries: rendered as standalone user msg.
    dispatch({
      type: 'event',
      event: {
        type: 'attachment',
        message: { attachment: { prompt: 'second prompt', source_uuid: '' } },
      },
    })
    expect(document.body.textContent).toContain('second prompt')
    // New turn streams text.
    events([
      { type: 'turn_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'second response' },
    ])
    expect(document.body.textContent).toContain('second response')
  })

  it('streaming attachment renders text, image and document chip from content blocks', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch(attachmentEvent({
      content: [
        { type: 'text', text: 'see this file' },
        IMAGE_BLOCK,
        DOC_BLOCK,
      ],
      prompt: 'stale prompt',
    }))
    expect(document.body.textContent).toContain('see this file')
    expect(document.body.textContent).not.toContain('stale prompt')
    const srcs = [...document.querySelectorAll('img')].map((i) => i.getAttribute('src'))
    expect(srcs).toContain('data:image/png;base64,aWNl')
    expect(document.body.textContent).toContain('[notes.md 2.0 KB]')
  })

  it('document chip wrappers stay block-shaped while image wrappers go inline', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch(attachmentEvent({
      content: [IMAGE_BLOCK, DOC_BLOCK],
      prompt: '',
    }))
    const img = document.querySelector('img') as HTMLImageElement
    const chip = [...document.querySelectorAll('span.font-mono')][0] as HTMLElement
    expect(img.parentElement!.className).toContain('inline-block')
    expect(chip.parentElement!.className).not.toContain('inline-block')
    // The image element itself carries the inline flow class through the
    // streaming path, same as the committed render.
    expect(img.className).toContain('inline-block')
  })
  it('multiple streaming images sit in inline wrappers so they flow side by side', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch(attachmentEvent({
      content: [IMAGE_BLOCK, IMAGE_BLOCK],
      prompt: '',
    }))
    const wraps = [...document.querySelectorAll('img')].map((i) => i.parentElement as HTMLElement)
    expect(wraps).toHaveLength(2)
    for (const w of wraps) {
      // The echo wrapper keeps the block-boundary shape but lays the images
      // out inline — matching the committed-history render.
      expect(w.className).toContain('inline-block')
    }
  })

  it('non-streaming attachment renders image and document chip from content blocks', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([
      { type: 'query_start' },
      { type: 'text_start' },
      { type: 'text_delta', text: 'first' },
      { type: 'query_end' },
    ])
    dispatch(attachmentEvent({
      content: [
        { type: 'text', text: 'look here' },
        IMAGE_BLOCK,
        DOC_BLOCK,
      ],
    }))
    expect(document.body.textContent).toContain('look here')
    const srcs = [...document.querySelectorAll('img')].map((i) => i.getAttribute('src'))
    expect(srcs).toContain('data:image/png;base64,aWNl')
    expect(document.body.textContent).toContain('[notes.md 2.0 KB]')
  })

  it('attachment with attachments only and no text still renders', () => {
    mount()
    setTextarea('q')
    pressEnter()
    events([{ type: 'query_start' }])
    dispatch(attachmentEvent({
      content: [IMAGE_BLOCK, DOC_BLOCK],
      prompt: '',
    }))
    const srcs = [...document.querySelectorAll('img')].map((i) => i.getAttribute('src'))
    expect(srcs).toContain('data:image/png;base64,aWNl')
    expect(document.body.textContent).toContain('[notes.md 2.0 KB]')
  })
})

describe('attachment block rebuild (rewind/takeover path)', () => {
  it('image blocks rebuild with inline wrappers, document chips with block wrappers', () => {
    const parent = document.createElement('div')
    document.body.appendChild(parent)
    renderStreamBlock(parent, { kind: 'image', id: '', src: 'data:image/png;base64,Z' } as unknown as Block, null, null)
    renderStreamBlock(parent, { kind: 'document', id: '', name: 'n.md', size: 2, mime: 'text/markdown' } as unknown as Block, null, null)
    const img = parent.querySelector('img') as HTMLImageElement
    const chip = parent.querySelector('span.font-mono') as HTMLElement
    expect(img.parentElement!.className).toContain('inline-block')
    expect(chip.parentElement!.className).not.toContain('inline-block')
    // Exact pin: the block wrapper is the plain userEchoBlock recipe — no
    // appended sentinel can hide in the else arm.
    expect(chip.parentElement!.className).toBe(userEchoBlock())
  })
})
