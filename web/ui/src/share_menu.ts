// Long-press message menu: two-item Liquid Glass popup (share the full reply
// as image / copy the selection or the full reply), interaction ported from
// the approved share-menu-demo.html with two deliberate deviations: the
// menu/toast mount on mainContent with absolute coordinates (a lingering
// sidebar translateX would otherwise trap fixed elements), and the item awaits
// its action so the success/failure toast is truthful. Entry is gated on the
// Android shell bridge — without window.GBotShare initShareMenu no-ops and
// desktop never arms a listener. Long-press doubles as the native selection
// start: contextmenu is suppressed only to drop the system menu (handles live
// on), and the menu opens AT contextmenu — the moment the browser hands over
// selection — so handles and menu coexist. Opening on lift made them
// mutually exclusive.
import { createNode } from './dom'
import { t } from './i18n'
import { renderIcon } from './icons'
import { copyText } from './utils/copy_button'
import { getShareBridge, shareMessage } from './share_card'
import type { Block } from './model'

export const LONG_PRESS_MS = 450
export const CANCEL_MOVE_PX = 8

// Presses landing on interactive content never start the hold timer — code
// copy buttons, tool-row collapse headers (span[role=button]), lightbox
// images, artifact cards.
const AVOID_SELECTOR =
  'button, a, input, textarea, select, img, [role="button"], .artifact-card'

/** The last text/user block — the final reply, not the working-out-loud.
 *  thinking/tool/image/document are chrome, not shareable content.
 *  Empty string when none. */
export function extractShareText(blocks: Block[]): string {
  // The final reply is the last text block; everything before it (interim
  // narration, tool narration) is working-out-loud the reader didn't ask for.
  const lastText = [...blocks].reverse().find((b) => b.kind === 'text' || b.kind === 'user')
  return lastText && 'text' in lastText ? lastText.text : ''
}

export interface ShareSource {
  anchor: HTMLElement
  role: 'user' | 'assistant'
  startedAt: number
  text: string
}

export interface ShareMenuOptions {
  /** mainContent: absolute positioning host for menu/toast. */
  host: HTMLElement
  /** messagesContainer: gesture target, gains the runtime share-root class. */
  container: HTMLElement
  getSource: (row: HTMLElement) => ShareSource | null
}

export interface ShareMenuHandle {
  close: () => void
  destroy: () => void
}

/** Bridge missing → null with zero side effects (no class, no listeners):
 *  the desktop path must be indistinguishable from the feature not existing. */
export function initShareMenu(opts: ShareMenuOptions): ShareMenuHandle | null {
  if (!getShareBridge()) {
    return null
  }
  const { host, container, getSource } = opts
  container.classList.add('share-root')

  let menu: HTMLElement | null = null
  let held: HTMLElement | null = null
  let currentSource: ShareSource | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let startY = 0
  let startX = 0
  let holdRow: HTMLElement | null = null
  let holdSrc: ShareSource | null = null
  let holdPointerId: number | null = null

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  function close(): void {
    disarmClickSwallow()
    if (!menu) return
    const m = menu
    menu = null
    currentSource = null
    m.classList.add('share-closing')
    setTimeout(() => m.remove(), 120)
    if (held) {
      held.classList.remove('share-lifted')
      held = null
    }
    container.classList.remove('share-holding')
  }

  function toast(text: string): void {
    const el = createNode('div', { className: 'share-toast', text })
    host.appendChild(el)
    setTimeout(() => {
      el.style.opacity = '0'
    }, 1400)
    setTimeout(() => el.remove(), 1800)
  }

  // Some WebViews synthesize a click on the row right after the long-press
  // gesture; that click would bubble to the doc-click closer and kill the
  // menu instantly (jsdom never synthesizes it, so tests alone can't catch
  // it). The swallow is bounded (350ms) but restarts at every gesture end
  // while the menu is open — dismissing by tapping the long-pressed row
  // needs a >350ms pause; a finger that stays down while reading the menu
  // would otherwise let the window expire before the lift fires.
  // Row-targeted only: menu-item and outside clicks behave normally.
  let clickEat: ((ev: Event) => void) | null = null
  let clickEatTimer: ReturnType<typeof setTimeout> | null = null
  const armClickSwallow = (): void => {
    // Local invariant: never armed twice (open() closes first, but keep the
    // guarantee here so a future caller can't orphan a capture listener).
    disarmClickSwallow()
    clickEat = (ev: Event): void => {
      if (!held || !held.contains(ev.target as Node)) return
      ev.stopPropagation()
      ev.preventDefault()
      disarmClickSwallow()
    }
    document.addEventListener('click', clickEat, true)
    clickEatTimer = setTimeout(disarmClickSwallow, 350)
  }
  const disarmClickSwallow = (): void => {
    if (clickEatTimer !== null) {
      clearTimeout(clickEatTimer)
      clickEatTimer = null
    }
    if (!clickEat) return
    document.removeEventListener('click', clickEat, true)
    clickEat = null
  }
  // Restart the swallow window at the gesture's end (see the arm block).
  const rearmClickSwallow = (): void => {
    if (!clickEat) return
    if (clickEatTimer !== null) clearTimeout(clickEatTimer)
    clickEatTimer = setTimeout(disarmClickSwallow, 350)
  }

  // The live selection, but only when it starts inside `row`: a stale
  // selection from a previously long-pressed row must not steer this row's
  // anchoring or copy content. A selection that starts here and bleeds into
  // the next row (end handle dragged downward) is kept whole — that bleed
  // is the user's own gesture.
  const selectionInRow = (row: HTMLElement): { text: string; range: Range } | null => {
    const sel = window.getSelection()
    const range = sel && sel.rangeCount ? sel.getRangeAt(0) : null
    if (!sel || !range || !row.contains(range.startContainer)) return null
    return { text: sel.toString(), range }
  }

  function open(row: HTMLElement, src: ShareSource, touchY: number): void {
    close()
    armClickSwallow()
    held = row
    currentSource = src
    row.classList.add('share-lifted')
    container.classList.add('share-holding')

    menu = createNode('div', { className: 'share-menu' })
    // Load-bearing: an unselectable menu means tapping Copy does not collapse
    // the live selection — the handler reads it after this tap.
    menu.style.userSelect = 'none'
    const shareItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuImage') },
    })
    shareItem.appendChild(renderIcon('share'))
    // Two icons, both reading the same long-press: share = full reply as
    // image; copy = the partial selection when handles were used, else the
    // full reply text. The system menu stays suppressed — handles alone are
    // enough for our copy item to read the selection.
    const copyItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuCopy') },
    })
    copyItem.appendChild(renderIcon('copy'))
    menu.append(shareItem, createNode('span', { className: 'share-vsep' }), copyItem)
    host.appendChild(menu)
    copyItem.addEventListener('click', () => {
      const src = currentSource
      // Read the selection BEFORE close(): handles-dragged partial wins;
      // no selection (or one belonging to another row) falls back to the
      // full reply text.
      const selected = selectionInRow(row)?.text ?? ''
      close()
      if (!src) return
      // Trim: handle drags routinely leave a stray leading space or
      // trailing newline that nobody wants on the clipboard.
      const trimmed = selected.trim()
      void copyText(trimmed ? trimmed : src.text).then(
        () => {
          toast(t('shareCopied'))
        },
        () => {
          toast(t('shareCopyFailed'))
        },
      )
    })

    // Vertical anchor precedence: the selection's bounding box (system
    // toolbar style) → the touch point. Never the row's rect: wui rows can
    // be thousands of pixels tall and row-rect anchoring reads as detached.
    // The row rect is used for `left` only.
    const hostRect = host.getBoundingClientRect()
    let anchorY = touchY
    // jsdom Range has no getBoundingClientRect — the typeof guard is not
    // defensive theater, it keeps the open() path alive under test.
    const range = selectionInRow(row)?.range ?? null
    const rect = range && typeof range.getBoundingClientRect === 'function' ? range.getBoundingClientRect() : null
    // Client space, same domain as touchY — the single conversion below
    // (touch = anchorY - hostRect.top) covers both paths.
    if (rect && (rect.width || rect.height)) anchorY = rect.top
    // offsetWidth/Height fall back to the demo values when layout has not
    // run (jsdom) — real measurements win in the browser.
    const mw = menu.offsetWidth || 94
    const mh = menu.offsetHeight || 48
    const touch = anchorY - hostRect.top
    let top = touch - mh - 8
    let below = false
    if (top < 8) {
      top = touch + 8
      below = true
      if (top + mh > hostRect.height - 8) {
        top = Math.max(8, hostRect.height - mh - 8)
      }
    }
    const r = row.getBoundingClientRect()
    // Same client→host conversion as the vertical path. hostRect.left is 0
    // today (the sidebar's fullscreen scrim swallows presses while it covers
    // the rows) but the subtraction is free insurance against layout drift —
    // the same trap the absolute positioning exists for.
    let left = r.left - hostRect.left + r.width / 2 - mw / 2
    left = Math.max(12, Math.min(left, hostRect.width - mw - 12))
    menu.style.top = `${top}px`
    menu.style.left = `${left}px`
    if (below) menu.classList.add('share-below')

    shareItem.addEventListener('click', () => {
      const src = currentSource
      close()
      if (!src) return
      void shareMessage(src).then(
        (res) => {
          // 'no-bridge' resolving here would be a false success toast — it
          // is a defensive path, report it as a failure.
          toast(res === 'opened' ? t('shareImageReady') : t('shareImageFailed'))
        },
        () => {
          toast(t('shareImageFailed'))
        },
      )
    })
  }

  function rowFromTarget(target: EventTarget | null): HTMLElement | null {
    let el = target as HTMLElement | null
    while (el && el.parentElement !== container) el = el.parentElement
    return el
  }

  const onPointerDown = (e: PointerEvent): void => {
    const target = e.target as HTMLElement | null
    if (!target || menu) return
    // Secondary fingers (pinch-zoom, a resting thumb) must not retake a
    // LIVE hold: overwriting the timer leaks the first handle and a
    // sub-450ms second press then looks "expired" to the lift path. A stale
    // hold (timer already fired) is fair game — its pointerup may never
    // reach the container (the row was detached mid-press, the browser
    // retargets the lift), and refusing every later press would kill the
    // feature for the session. Accepted side effect: a thumb pressing after
    // the first finger's timer fired takes the hold over and the first
    // finger's completed long-press is lost — the price of never locking
    // out. Do not "fix" this back into a blanket refusal.
    if (holdPointerId !== null && timer !== null) return
    if (target.closest(AVOID_SELECTOR)) return
    const row = rowFromTarget(target)
    if (!row) return
    const src = getSource(row)
    if (!src) {
      return
    }
    startY = e.clientY
    startX = e.clientX
    // The hold timer only MARKS a valid long-press. Text rows open at
    // contextmenu (the browser's own long-press moment, handles included);
    // the lift path in onPointerUp is the fallback for non-text targets
    // where the browser never fires contextmenu.
    holdRow = row
    holdSrc = src
    holdPointerId = e.pointerId
    timer = setTimeout(() => {
      timer = null
    }, LONG_PRESS_MS)
  }
  const onPointerMove = (e: PointerEvent): void => {
    // No `timer === null` early return: an expired hold must stay
    // cancellable too — hold 500ms then drag 300px and the lift would
    // otherwise open the menu at the stale start point.
    if (e.pointerId !== holdPointerId) return
    const dx = Math.abs(e.clientX - startX)
    const dy = Math.abs(e.clientY - startY)
    if (Math.max(dx, dy) > CANCEL_MOVE_PX) {
      clearTimer()
      // Clear the hold state too: timer===null alone means "expired", and
      // the lift path cannot tell that apart from "cancelled by movement" —
      // a stale holdRow would open the menu on lift anyway.
      holdRow = null
      holdSrc = null
      holdPointerId = null
    }
  }
  const onPointerUp = (e: PointerEvent): void => {
    // Fallback path for long-presses on non-text targets where the browser
    // never fires contextmenu (no selection takeover): open on lift. For
    // text rows the menu already opened at contextmenu — the hold state was
    // consumed there, so this lift returns at the pointer-id guard below.
    // A lift from a pointer that never armed the hold (secondary finger)
    // must leave the hold state alone.
    rearmClickSwallow()
    if (e.pointerId !== holdPointerId) return
    if (timer !== null || !holdRow) {
      clearTimer()
      holdRow = null
      holdSrc = null
      holdPointerId = null
      return
    }
    const row = holdRow
    const src = holdSrc
    holdRow = null
    holdSrc = null
    holdPointerId = null
    if (row.isConnected && src) open(row, src, startY)
  }
  const onPointerCancel = (e: PointerEvent): void => {
    rearmClickSwallow()
    if (e.pointerId !== holdPointerId) return
    clearTimer()
    holdRow = null
    holdSrc = null
    holdPointerId = null
  }
  const onContextMenu = (e: MouseEvent): void => {
    // This event IS the long-press moment: the browser has just started the
    // native selection (handles coming up) and is about to show its own
    // menu. Open ours HERE so handles and menu coexist; preventDefault
    // kills the system menu only. Opening on pointerup instead would race
    // the selection takeover — the browser ends the pointer stream at
    // contextmenu (pointerout/pointercancel follow), so waiting for lift
    // made handles and menu mutually exclusive.
    if (container.contains(e.target as Node)) {
      e.preventDefault()
      if (holdRow && holdSrc) {
        const row = holdRow
        const src = holdSrc
        clearTimer()
        holdRow = null
        holdSrc = null
        holdPointerId = null
        if (row.isConnected) open(row, src, startY)
      }
    }
  }
  const onDocClick = (e: MouseEvent): void => {
    if (menu && !menu.contains(e.target as Node)) close()
  }
  // The menu is absolutely positioned on the host while the rows scroll —
  // a capsule floating over content that moved reads as broken. Scroll
  // events don't bubble; the capture phase is how they reach the host.
  const onScroll = (): void => {
    if (menu) close()
  }

  container.addEventListener('pointerdown', onPointerDown)
  container.addEventListener('pointermove', onPointerMove)
  container.addEventListener('pointerup', onPointerUp)
  container.addEventListener('pointercancel', onPointerCancel)
  container.addEventListener('contextmenu', onContextMenu)
  document.addEventListener('click', onDocClick)
  host.addEventListener('scroll', onScroll, true)

  return {
    close,
    destroy: () => {
      clearTimer()
      holdRow = null
      holdSrc = null
      holdPointerId = null
      disarmClickSwallow()
      if (menu) {
        menu.remove()
        menu = null
      }
      currentSource = null
      if (held) {
        held.classList.remove('share-lifted')
        held = null
      }
      container.classList.remove('share-root', 'share-holding')
      container.removeEventListener('pointerdown', onPointerDown)
      container.removeEventListener('pointermove', onPointerMove)
      container.removeEventListener('pointerup', onPointerUp)
      container.removeEventListener('pointercancel', onPointerCancel)
      container.removeEventListener('contextmenu', onContextMenu)
      document.removeEventListener('click', onDocClick)
      host.removeEventListener('scroll', onScroll, true)
    },
  }
}
