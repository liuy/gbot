import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  extractShareText,
  initShareMenu,
  LONG_PRESS_MS,
  CANCEL_MOVE_PX,
  type ShareMenuHandle,
  type ShareSource,
} from './share_menu'
import { shareMessage } from './share_card'
import { copyText } from './utils/copy_button'
import { t } from './i18n'
import type { Block } from './model'

// Partial mock: keep the real getShareBridge (the entry probe reads
// window.GBotShare through it) and only intercept shareMessage.
vi.mock('./share_card', async (importOriginal) => {
  const actual = await importOriginal<typeof import('./share_card')>()
  return { ...actual, shareMessage: vi.fn() }
})

vi.mock('./utils/copy_button', () => ({ copyText: vi.fn() }))

function block(kind: Block['kind'], text = 'x'): Block {
  switch (kind) {
    case 'text':
      return { kind: 'text', id: '', text }
    case 'user':
      return { kind: 'user', id: '', text }
    case 'thinking':
      return { kind: 'thinking', id: '', text, durationNs: 0, active: false, startedAt: 0 }
    case 'image':
      return { kind: 'image', id: '', src: 'data:image/png;base64,QUJD' }
    case 'document':
      return { kind: 'document', id: '', name: 'f.pdf' }
    default:
      return {
        kind: 'tool',
        id: '',
        name: 'Bash',
        summary: '',
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
      }
  }
}

describe('extractShareText', () => {
  it('returns only the last text/user block — the final reply, not the working-out-loud', () => {
    const blocks: Block[] = [
      block('thinking'),
      block('text', 'interim narration'),
      block('tool'),
      block('text', 'a'),
      block('image'),
      block('user', 'c'),
    ]
    expect(extractShareText(blocks)).toBe('c')
  })

  it('returns empty string when no shareable block exists', () => {
    expect(extractShareText([block('thinking'), block('tool')])).toBe('')
  })
})

// --- gesture / menu suites ---

function domRect(init: Partial<DOMRect>): DOMRect {
  return {
    top: 0,
    left: 0,
    bottom: 0,
    right: 0,
    width: 0,
    height: 0,
    x: 0,
    y: 0,
    toJSON: () => ({}),
    ...init,
  } as DOMRect
}

let host: HTMLElement
let container: HTMLElement
let row: HTMLElement
let source: ShareSource
let handle: ShareMenuHandle | null
const bridgeShare = vi.fn()

function pointer(type: string, clientY = 0): MouseEvent {
  return new MouseEvent(type, { bubbles: true, cancelable: true, clientY })
}

function press(clientY = 0): void {
  row.dispatchEvent(pointer('pointerdown', clientY))
}

function setup(getSource?: (r: HTMLElement) => ShareSource | null): void {
  document.body.innerHTML = ''
  host = document.createElement('div')
  container = document.createElement('div')
  row = document.createElement('div')
  container.appendChild(row)
  host.appendChild(container)
  document.body.appendChild(host)
  source = {
    anchor: row,
    role: 'assistant',
    startedAt: new Date(2026, 9, 15, 10, 50).getTime(),
    text: 'share me',
  }
  handle = initShareMenu({
    host,
    container,
    getSource: getSource ?? (() => source),
  })
}

beforeEach(() => {
  vi.useFakeTimers()
  bridgeShare.mockClear()
  ;(window as unknown as Record<string, unknown>).GBotShare = { shareImage: bridgeShare }
  vi.mocked(shareMessage).mockReset()
  vi.mocked(copyText).mockReset()
})

afterEach(() => {
  handle?.destroy()
  handle = null
  delete (window as unknown as Record<string, unknown>).GBotShare
  document.body.innerHTML = ''
  vi.useRealTimers()
})

describe('entry probe', () => {
  it('no bridge: initShareMenu returns null with zero side effects', () => {
    delete (window as unknown as Record<string, unknown>).GBotShare
    setup()
    expect(handle).toBeNull()
    expect(container.classList.contains('share-root')).toBe(false)
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()
    expect(container.classList.contains('share-holding')).toBe(false)
  })

  it('bridge present: container carries share-root immediately', () => {
    setup()
    expect(handle).not.toBeNull()
    expect(container.classList.contains('share-root')).toBe(true)
  })
})

describe('long-press gesture', () => {
  it('fires the menu after 450ms with lift/holding/scrim state', () => {
    setup()
    press()
    expect(host.querySelector('.share-menu')).toBeNull()
    vi.advanceTimersByTime(LONG_PRESS_MS - 1)
    expect(host.querySelector('.share-menu')).toBeNull()
    vi.advanceTimersByTime(1)
    const menu = host.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    expect(host.querySelectorAll('button.share-menu-item')).toHaveLength(2)
    expect(host.querySelectorAll('.share-vsep')).toHaveLength(1)
    expect(container.classList.contains('share-holding')).toBe(true)
    expect(row.classList.contains('share-lifted')).toBe(true)
    expect(host.querySelector('.share-scrim')?.classList.contains('share-open')).toBe(true)
  })

  it(`cancels when the pointer moves more than ${CANCEL_MOVE_PX}px, fires within`, () => {
    setup()
    press(100)
    row.dispatchEvent(pointer('pointermove', 100 + CANCEL_MOVE_PX + 2))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()

    press(100)
    row.dispatchEvent(pointer('pointermove', 100 + 5))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).not.toBeNull()
  })

  it('cancels on pointerup before the timer fires', () => {
    setup()
    press()
    vi.advanceTimersByTime(200)
    row.dispatchEvent(pointer('pointerup'))
    vi.advanceTimersByTime(600)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('does not reopen while the menu is already open', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelectorAll('.share-menu')).toHaveLength(1)
  })
})

describe('menu positioning', () => {
  it('anchors above the touch point when there is room (host coordinates)', () => {
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 100, width: 200, height: 50, right: 300, bottom: 250 }),
    )
    press(300)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const menu = host.querySelector('.share-menu') as HTMLElement
    // mw=94/mh=48 demo fallbacks (jsdom layout is 0); touch 300-48-8=244
    expect(menu.style.top).toBe('244px')
    expect(menu.style.left).toBe('153px')
    expect(menu.classList.contains('share-below')).toBe(false)
  })

  it('flips below the touch point when the top edge would cross the host top', () => {
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 30, left: 100, width: 200, height: 50, right: 300, bottom: 80 }),
    )
    press(30)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const menu = host.querySelector('.share-menu') as HTMLElement
    expect(menu.classList.contains('share-below')).toBe(true)
    expect(menu.style.top).toBe('38px')
  })

  it('clamps the flipped position against the host bottom (short host)', () => {
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 60, right: 390, bottom: 60 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 30, left: 100, width: 200, height: 20, right: 300, bottom: 50 }),
    )
    press(30)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const menu = host.querySelector('.share-menu') as HTMLElement
    expect(menu.classList.contains('share-below')).toBe(true)
    // flip lands at 38; 38+48 overflows 60-8 → clamped to max(8, 60-48-8)
    expect(menu.style.top).toBe('8px')
  })

  it('clamps left into [12, hostWidth - mw - 12]', () => {
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const menu = host.querySelector('.share-menu') as HTMLElement

    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 0, width: 10, height: 50, right: 10, bottom: 250 }),
    )
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    handle!.destroy()
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 380, width: 10, height: 50, right: 390, bottom: 250 }),
    )
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const menu2 = host.querySelector('.share-menu') as HTMLElement
    expect(menu.style.left).toBe('12px')
    expect(menu2.style.left).toBe('284px')
  })
})

describe('control avoidance', () => {
  it('ignores presses landing on interactive controls', () => {
    setup()
    const btn = document.createElement('button')
    row.appendChild(btn)
    btn.dispatchEvent(pointer('pointerdown'))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('ignores presses landing on artifact cards', () => {
    setup()
    const card = document.createElement('div')
    card.className = 'artifact-card'
    row.appendChild(card)
    card.dispatchEvent(pointer('pointerdown'))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('ignores rows whose getSource returns null (reminder rows)', () => {
    setup((r) => (r === row ? null : source))
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()
    expect(container.classList.contains('share-holding')).toBe(false)
  })
})

describe('copy item', () => {
  async function flush(): Promise<void> {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  }

  it('copies the source text, toasts success, closes the menu', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    vi.mocked(copyText).mockResolvedValue()
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(copyText).toHaveBeenCalledWith('share me')
    const toast = host.querySelector('.share-toast')
    expect(toast?.textContent).toBe(t('shareCopied'))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
    expect(container.classList.contains('share-holding')).toBe(false)
    expect(row.classList.contains('share-lifted')).toBe(false)
    vi.advanceTimersByTime(120)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('toasts failure when copyText rejects', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    vi.mocked(copyText).mockRejectedValue(new Error('denied'))
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(host.querySelector('.share-toast')?.textContent).toBe(t('shareCopyFailed'))
    expect(host.textContent).not.toContain(t('shareCopied'))
  })
})

describe('share item', () => {
  async function flush(): Promise<void> {
    await Promise.resolve()
    await Promise.resolve()
    await Promise.resolve()
  }

  function clickShare(): void {
    const items = host.querySelectorAll('button.share-menu-item')
    items[0].dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  it('calls shareMessage and toasts readiness on opened', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    vi.mocked(shareMessage).mockResolvedValue('opened')
    clickShare()
    await flush()
    expect(shareMessage).toHaveBeenCalledWith(
      expect.objectContaining({ text: 'share me', role: 'assistant' }),
    )
    expect(host.querySelector('.share-toast')?.textContent).toBe(t('shareImageReady'))
  })

  it('toasts failure when shareMessage rejects', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    vi.mocked(shareMessage).mockRejectedValue(new Error('boom'))
    clickShare()
    await flush()
    expect(host.querySelector('.share-toast')?.textContent).toBe(t('shareImageFailed'))
  })

  it('toasts failure on a defensive no-bridge resolve', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    vi.mocked(shareMessage).mockResolvedValue('no-bridge')
    clickShare()
    await flush()
    expect(host.querySelector('.share-toast')?.textContent).toBe(t('shareImageFailed'))
  })
})

describe('dismissal', () => {
  function openMenu(): void {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
  }

  it('scrim click closes and restores all state', () => {
    openMenu()
    const scrim = host.querySelector('.share-scrim') as HTMLElement
    scrim.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
    expect(scrim.classList.contains('share-open')).toBe(false)
    vi.advanceTimersByTime(120)
    expect(host.querySelector('.share-menu')).toBeNull()
    expect(container.classList.contains('share-holding')).toBe(false)
    expect(row.classList.contains('share-lifted')).toBe(false)
  })

  it('document click outside the menu closes it', () => {
    openMenu()
    container.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
  })

  it('click inside the menu does not close it', () => {
    openMenu()
    const menu = host.querySelector('.share-menu') as HTMLElement
    menu.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(menu.classList.contains('share-closing')).toBe(false)
    expect(menu.isConnected).toBe(true)
  })

  it('contextmenu inside a row is prevented', () => {
    setup()
    const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    row.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
  })
})

describe('destroy', () => {
  it('removes listeners, classes and an open menu immediately', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    handle!.destroy()
    expect(host.querySelector('.share-menu')).toBeNull()
    expect(container.classList.contains('share-root')).toBe(false)
    expect(container.classList.contains('share-holding')).toBe(false)
    expect(row.classList.contains('share-lifted')).toBe(false)
    // Listeners are gone: a later press must not open anything.
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    expect(host.querySelector('.share-menu')).toBeNull()
  })
})
