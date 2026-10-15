import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
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
function pointerId(type: string, clientY: number, id: number): MouseEvent {
  const e = new MouseEvent(type, { bubbles: true, cancelable: true, clientY })
  Object.defineProperty(e, 'pointerId', { value: id })
  return e
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
  // Selections outlive the DOM they pointed at; a leaked range from one
  // test would steer the next test's anchoring.
  window.getSelection()?.removeAllRanges()
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
    row.dispatchEvent(pointer('pointerup'))
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
  it('fires the menu after 450ms with lift/holding state', () => {
    setup()
    press()
    expect(host.querySelector('.share-menu')).toBeNull()
    vi.advanceTimersByTime(LONG_PRESS_MS - 1)
    expect(host.querySelector('.share-menu')).toBeNull()
    vi.advanceTimersByTime(1)
    // The expiry only marks the hold; the lift opens it (non-text fallback
    // path — text rows open earlier, at contextmenu).
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu')
    expect(menu).not.toBeNull()
    expect(host.querySelectorAll('button.share-menu-item')).toHaveLength(2)
    expect(host.querySelectorAll('.share-vsep')).toHaveLength(1)
    expect(container.classList.contains('share-holding')).toBe(true)
    expect(row.classList.contains('share-lifted')).toBe(true)
  })

  it(`cancels when the pointer moves more than ${CANCEL_MOVE_PX}px, fires within`, () => {
    setup()
    press(100)
    row.dispatchEvent(pointer('pointermove', 100 + CANCEL_MOVE_PX + 2))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()

    press(100)
    row.dispatchEvent(pointer('pointermove', 100 + 5))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
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

  it('contextmenu opens the menu immediately and consumes the hold', () => {
    setup()
    press(300)
    const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 300 })
    row.dispatchEvent(e)
    expect(e.defaultPrevented).toBe(true)
    expect(host.querySelector('.share-menu')).not.toBeNull()
    // The lift that follows contextmenu must not duplicate the menu.
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelectorAll('.share-menu')).toHaveLength(1)
  })

  it('secondary pointers never retake the hold', () => {
    setup()
    row.dispatchEvent(pointerId('pointerdown', 0, 1))
    row.dispatchEvent(pointerId('pointerdown', 0, 2))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    // Finger B lifts first: its lift must not kill finger A's hold.
    row.dispatchEvent(pointerId('pointerup', 0, 2))
    expect(host.querySelector('.share-menu')).toBeNull()
    row.dispatchEvent(pointerId('pointerup', 0, 1))
    expect(host.querySelector('.share-menu')).not.toBeNull()
  })

  it('a move or cancel from a different pointer leaves the hold alone', () => {
    setup()
    row.dispatchEvent(pointerId('pointerdown', 0, 1))
    vi.advanceTimersByTime(200)
    // A thumb's drag and cancel (pinch-zoom) must not cancel finger A's
    // hold — only finger A's own movement beyond the threshold may.
    row.dispatchEvent(pointerId('pointermove', 300, 2))
    vi.advanceTimersByTime(250)
    row.dispatchEvent(pointerId('pointercancel', 0, 2))
    row.dispatchEvent(pointerId('pointerup', 0, 1))
    expect(host.querySelector('.share-menu')).not.toBeNull()
  })

  it('a lost pointerup does not lock out future long-presses', () => {
    setup()
    row.dispatchEvent(pointerId('pointerdown', 0, 1))
    // A rewind detaches the row mid-press; the browser retargets the lift
    // outside the container, so our pointerup never fires and the hold goes
    // stale. The next press must take over, not be refused forever.
    row.remove()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const fresh = document.createElement('div')
    container.appendChild(fresh)
    fresh.dispatchEvent(pointerId('pointerdown', 0, 2))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    fresh.dispatchEvent(pointerId('pointerup', 0, 2))
    expect(host.querySelector('.share-menu')).not.toBeNull()
  })

  it('a lift whose hold row was detached mid-press opens nothing', () => {
    setup()
    press()
    row.remove()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    // The browser retargets the lift by hit test; it lands on the container.
    container.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('a contextmenu whose hold row was detached opens nothing', () => {
    setup()
    press()
    row.remove()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    const e = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    container.dispatchEvent(e)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('a drag after the hold expired cancels the lift', () => {
    setup()
    press(100)
    vi.advanceTimersByTime(LONG_PRESS_MS + 50)
    // Expired ≠ uncancellable: dragging 300px then lifting must not open
    // the menu at the stale start point.
    row.dispatchEvent(pointer('pointermove', 400))
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('a stray pointermove after the menu opened leaves it in place', () => {
    setup()
    press(300)
    row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 300 }))
    row.dispatchEvent(pointer('pointermove', 100))
    expect(host.querySelectorAll('.share-menu')).toHaveLength(1)
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(false)
  })

  it('cancels on horizontal movement beyond the threshold', () => {
    setup()
    press(100)
    row.dispatchEvent(new MouseEvent('pointermove', { bubbles: true, clientY: 100, clientX: 20 }))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('pointercancel during the hold opens nothing', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointercancel'))
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('pointercancel after the menu opened leaves it in place', () => {
    setup()
    press(300)
    row.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, cancelable: true, clientY: 300 }))
    row.dispatchEvent(pointer('pointercancel'))
    expect(host.querySelector('.share-menu')).not.toBeNull()
  })

  it('does not reopen while the menu is already open', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // mw=94/mh=48 demo fallbacks (jsdom layout is 0); touch 300-48-8=244
    expect(menu.style.top).toBe('244px')
    expect(menu.style.left).toBe('153px')
    expect(menu.classList.contains('share-below')).toBe(false)
  })

  it('anchors above the live selection, not the touch point', () => {
    setup()
    row.textContent = 'hello partial world'
    const range = document.createRange()
    range.setStart(row.firstChild!, 6)
    range.setEnd(row.firstChild!, 13)
    window.getSelection()?.addRange(range)
    // jsdom Range has no layout; stub the selection's bounding box.
    Object.defineProperty(range, 'getBoundingClientRect', {
      value: () => domRect({ top: 120, left: 40, width: 100, height: 20, right: 140, bottom: 140 }),
    })
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 100, width: 200, height: 50, right: 300, bottom: 250 }),
    )
    press(300)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // Selection top 120 wins over touch 300: 120-48-8=64
    expect(menu.style.top).toBe('64px')
  })

  it('converts the selection rect through a non-zero host offset', () => {
    setup()
    row.textContent = 'hello partial world'
    const range = document.createRange()
    range.setStart(row.firstChild!, 6)
    range.setEnd(row.firstChild!, 13)
    window.getSelection()?.addRange(range)
    Object.defineProperty(range, 'getBoundingClientRect', {
      value: () => domRect({ top: 300, left: 40, width: 100, height: 20, right: 140, bottom: 320 }),
    })
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 56, left: 0, width: 390, height: 744, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 280, left: 100, width: 200, height: 50, right: 300, bottom: 330 }),
    )
    press(400)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // client 300 → host 300-56=244 → 244-48-8=188. The pre-fix formula
    // (+hostRect.top then -hostRect.top cancels) gives 244 here.
    expect(menu.style.top).toBe('188px')
  })

  it('ignores a selection outside the long-pressed row', () => {
    setup()
    const range = document.createRange()
    range.setStart(host, 0)
    range.setEnd(host, 1)
    window.getSelection()?.addRange(range)
    Object.defineProperty(range, 'getBoundingClientRect', {
      value: () => domRect({ top: 300, left: 40, width: 100, height: 20, right: 140, bottom: 320 }),
    })
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 0, width: 390, height: 800, right: 390, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 100, width: 200, height: 50, right: 300, bottom: 250 }),
    )
    press(400)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // Selection (host of the row, not inside it) ignored → touch 400 wins:
    // 400-48-8=344. Without the containment guard the stale rect anchors at
    // 300-48-8=244.
    expect(menu.style.top).toBe('344px')
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
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // jsdom's zero row rect → 0 + 0 - 47 = -47 → clamped to 12.

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
    row.dispatchEvent(pointer('pointerup'))
    const menu2 = host.querySelector('.share-menu') as HTMLElement
    expect(menu.style.left).toBe('12px')
    expect(menu2.style.left).toBe('284px')
  })

  it('converts the row rect through a non-zero host left offset', () => {
    setup()
    vi.spyOn(host, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 0, left: 40, width: 390, height: 800, right: 430, bottom: 800 }),
    )
    vi.spyOn(row, 'getBoundingClientRect').mockReturnValue(
      domRect({ top: 200, left: 100, width: 200, height: 50, right: 300, bottom: 250 }),
    )
    press(300)
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    // (100-40) + 100 - 47 = 113. Skipping the host offset gives 153.
    expect(menu.style.left).toBe('113px')
  })
})

describe('control avoidance', () => {
  it('ignores presses landing on interactive controls', () => {
    setup()
    const btn = document.createElement('button')
    row.appendChild(btn)
    btn.dispatchEvent(pointer('pointerdown'))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('ignores presses landing on artifact cards', () => {
    setup()
    const card = document.createElement('div')
    card.className = 'artifact-card'
    row.appendChild(card)
    card.dispatchEvent(pointer('pointerdown'))
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('ignores rows whose getSource returns null (reminder rows)', () => {
    setup((r) => (r === row ? null : source))
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
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

  it('copies the active partial selection instead of the full text', async () => {
    setup()
    row.textContent = 'hello partial world'
    const sel = window.getSelection()
    const range = document.createRange()
    range.setStart(row.firstChild!, 6)
    range.setEnd(row.firstChild!, 13)
    sel?.removeAllRanges()
    sel?.addRange(range)
    vi.mocked(copyText).mockResolvedValue()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(copyText).toHaveBeenCalledWith('partial')
  })

  it('trims a padded selection before the clipboard', async () => {
    setup()
    row.textContent = 'hello partial world'
    const sel = window.getSelection()
    const range = document.createRange()
    range.setStart(row.firstChild!, 5)
    range.setEnd(row.firstChild!, 14)
    sel?.removeAllRanges()
    sel?.addRange(range)
    vi.mocked(copyText).mockResolvedValue()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    // ' partial ' → 'partial'
    expect(copyText).toHaveBeenCalledWith('partial')
  })

  it('a whitespace-only selection falls back to the full text', async () => {
    setup()
    row.textContent = 'a   b'
    const sel = window.getSelection()
    const range = document.createRange()
    range.setStart(row.firstChild!, 1)
    range.setEnd(row.firstChild!, 4)
    sel?.removeAllRanges()
    sel?.addRange(range)
    vi.mocked(copyText).mockResolvedValue()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    expect(copyText).toHaveBeenCalledWith('share me')
  })

  it('a selection from a different row never reaches the clipboard', async () => {
    setup()
    const other = document.createElement('div')
    other.textContent = 'foreign selection'
    host.insertBefore(other, container)
    const range = document.createRange()
    range.setStart(other.firstChild!, 0)
    range.setEnd(other.firstChild!, 7)
    window.getSelection()?.addRange(range)
    vi.mocked(copyText).mockResolvedValue()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const items = host.querySelectorAll('button.share-menu-item')
    items[1].dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flush()
    // Row B's menu copies row B's text; row A's stale selection is ignored.
    expect(copyText).toHaveBeenCalledWith('share me')
  })

  it('menu keeps user-select none so tap-copy preserves the live selection', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    const menu = host.querySelector('.share-menu') as HTMLElement
    expect(getComputedStyle(menu).userSelect).toBe('none')
  })

  it('the stylesheet keeps selection alive inside the shell (source pin)', () => {
    // jsdom never applies index.css to computed styles, so the rule that
    // makes native handles possible at all is pinned as source text —
    // the pattern layout.test.ts / markdown.test.ts already use.
    const css = readFileSync(resolve(__dirname, 'index.css'), 'utf-8')
    expect(css).toMatch(/\.share-root\s*\{[^}]*user-select:\s*text/)
    expect(css).not.toMatch(/\.share-root\s+\*\s*\{[^}]*user-select:\s*none/)
  })

  it('copies the source text, toasts success, closes the menu', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
    vi.mocked(shareMessage).mockRejectedValue(new Error('boom'))
    clickShare()
    await flush()
    expect(host.querySelector('.share-toast')?.textContent).toBe(t('shareImageFailed'))
  })

  it('toasts failure on a defensive no-bridge resolve', async () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
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
    row.dispatchEvent(pointer('pointerup'))
  }

  it('outside tap on the host closes and restores all state', () => {
    openMenu()
    // Dismissal rides the doc-click path — there is no scrim element.
    host.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
    expect(container.classList.contains('share-holding')).toBe(false)
    expect(row.classList.contains('share-lifted')).toBe(false)
    vi.advanceTimersByTime(120)
    expect(host.querySelector('.share-menu')).toBeNull()
  })

  it('a deliberate tap on the row after the swallow window closes the menu', () => {
    openMenu()
    // The synthesized-click swallow is bounded (350ms); a later intentional
    // tap on the long-pressed row must reach the doc-click closer.
    vi.advanceTimersByTime(400)
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
  })

  it('a click on the row inside the swallow window is eaten', () => {
    openMenu()
    vi.advanceTimersByTime(100)
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(false)
  })

  it('the swallow window restarts at the gesture end', () => {
    openMenu()
    vi.advanceTimersByTime(300)
    // A second gesture end (the finger that stayed down finally lifts, or a
    // stray cancel) restarts the window: the synthesized click rides the
    // gesture's END, not the open.
    row.dispatchEvent(pointer('pointerdown'))
    row.dispatchEvent(pointer('pointerup'))
    vi.advanceTimersByTime(100)
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(false)
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

  it('scrolling closes the menu', () => {
    openMenu()
    // Scroll events don't bubble; the handler rides the capture phase on
    // the host, so a scroll on the scrolling descendant reaches it.
    container.dispatchEvent(new Event('scroll'))
    expect(host.querySelector('.share-menu')?.classList.contains('share-closing')).toBe(true)
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
    row.dispatchEvent(pointer('pointerup'))
    handle!.destroy()
    expect(host.querySelector('.share-menu')).toBeNull()
    expect(container.classList.contains('share-root')).toBe(false)
    expect(container.classList.contains('share-holding')).toBe(false)
    expect(row.classList.contains('share-lifted')).toBe(false)
    // Listeners are gone: a later press must not open anything.
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    expect(host.querySelector('.share-menu')).toBeNull()
    // ...and the contextmenu listener is gone too: no more preventDefault.
    const cm = new MouseEvent('contextmenu', { bubbles: true, cancelable: true })
    row.dispatchEvent(cm)
    expect(cm.defaultPrevented).toBe(false)
  })

  it('destroy tears down the click swallow', () => {
    setup()
    press()
    vi.advanceTimersByTime(LONG_PRESS_MS)
    row.dispatchEvent(pointer('pointerup'))
    handle!.destroy()
    // destroy() removes the menu synchronously; the only timer an open
    // menu leaves behind is the 350ms click swallow — a missing
    // disarmClickSwallow() shows up here as count 1.
    expect(vi.getTimerCount()).toBe(0)
  })
})
