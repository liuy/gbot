// Two-state share surface, one gating entry: initShareMenu no-ops without
// the Android shell bridge (window.GBotShare) and desktop stays
// indistinguishable from the feature not existing.
//
// State A — the armed long-press capsule: text/user/error blocks keep the
// full ritual (selection handles, lift/hold dimming, the three-icon Liquid
// Glass capsule opening AT contextmenu, the browser's own long-press
// moment). State B — the mini copy capsule: any other selection path
// (handle drag, double-tap word select, system selection) that leaves a
// non-empty selection inside the container floats a one-icon capsule that
// follows the selection rect live through selectionchange/scroll rAFs.
// The two states are mutually exclusive; any successfully armed long-press
// gesture replaces the mini, and open()'s leading close() takes a stray
// mini down as a second line of defense.
//
// Contextmenu adjudication: inside the container a LIVE selection owns the
// content area — preventDefault kills the system toolbar and our UI (main
// capsule or mini) serves the copy intent; with no selection the gesture
// passes to the WebView untouched. Outside the container it is never ours.
import { createNode } from './dom'
import { t } from './i18n'
import { renderIcon } from './icons'
import { copyText } from './utils/copy_button'
import { getShareBridge, shareMessage, shareMarkdown } from './share_card'

export const LONG_PRESS_MS = 450
export const CANCEL_MOVE_PX = 8

// Presses landing on interactive content never start the hold timer — code
// copy buttons, thinking/tool/group collapse headers, lightbox images,
// artifact cards.
const AVOID_SELECTOR =
  'button, a, input, textarea, select, img, [role="button"], .artifact-card'

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
  /** target is the ORIGINAL press target (e.target of pointerdown) so the
   *  wiring can resolve the block under the finger, not just the row. */
  getSource: (row: HTMLElement, target: HTMLElement) => ShareSource | null
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
  // Mini capsule state. `dead` exists for one race: after destroy() all
  // four capsule variables are null, indistinguishable from "idle + live
  // selection", so an in-flight mini rAF would happily re-append. The flag
  // is the only signal that teardown happened.
  let mini: HTMLElement | null = null
  let miniRafPending = false
  let dead = false
  // jsdom layout is 0: the mini's fallback footprint = 40px icon + 36px
  // item height + 2*3px capsule padding + 2*1px border.
  const MINI_W = 48
  const MINI_H = 44

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  function close(): void {
    disarmClickSwallow()
    // Before the `menu` early-return: open() starts with close() to enforce
    // two-state mutual exclusion, and that call arrives with `menu` null —
    // a hideMini below the guard would never run exactly when it must.
    hideMini()
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

  // The live selection, but only when it starts inside `root`: a stale
  // selection from elsewhere must not steer anchoring or copy content. The
  // container-level call (mini state) passes the whole container; a
  // selection that starts inside and bleeds across rows is kept whole —
  // that bleed is the user's own gesture.
  const selectionWithin = (root: HTMLElement): { text: string; range: Range } | null => {
    const sel = window.getSelection()
    const range = sel && sel.rangeCount ? sel.getRangeAt(0) : null
    if (!sel || !range || !root.contains(range.startContainer)) return null
    return { text: sel.toString(), range }
  }

  // jsdom Range has no getBoundingClientRect — the typeof guard is not
  // defensive theater, it keeps the test paths alive. A collapsed or
  // zero-area rect is as useless as none: the capsule would have no anchor.
  function usableRect(range: Range | null): DOMRect | null {
    if (!range || typeof range.getBoundingClientRect !== 'function') return null
    const rect = range.getBoundingClientRect()
    return rect.width || rect.height ? rect : null
  }

  // Shared flip/clamp math for both capsules. `anchorClientY` and
  // `centerClientX` are client space (the selection rect's domain); the
  // single conversion below (touch = anchorY - hostRect.top) covers both
  // the rect and the touch-point paths. hostRect.left is 0 today (the
  // sidebar's fullscreen scrim swallows presses while it covers the rows)
  // but the subtraction is free insurance against layout drift — the same
  // trap the absolute positioning exists for.
  function placeCapsule(
    el: HTMLElement,
    anchorClientY: number,
    centerClientX: number,
    mw: number,
    mh: number,
  ): void {
    const hostRect = host.getBoundingClientRect()
    const touch = anchorClientY - hostRect.top
    let top = touch - mh - 8
    let below = false
    if (top < 8) {
      top = touch + 8
      below = true
      if (top + mh > hostRect.height - 8) {
        top = Math.max(8, hostRect.height - mh - 8)
      }
    }
    let left = centerClientX - hostRect.left - mw / 2
    left = Math.max(12, Math.min(left, hostRect.width - mw - 12))
    el.style.top = `${top}px`
    el.style.left = `${left}px`
    el.classList.toggle('share-below', below)
  }

  let dismissingMini: HTMLElement | null = null
  let miniHideTimer: number | null = null

  function hideMini(): void {
    if (!mini) return
    const m = mini
    mini = null
    // Same fade-out as the main capsule; a hide+show burst must NOT
    // recreate — the dismissing element is kept revivable (timer +
    // reference) so showOrMoveMini can cancel the removal in flight.
    m.classList.add('share-closing')
    if (miniHideTimer !== null) clearTimeout(miniHideTimer)
    dismissingMini = m
    miniHideTimer = setTimeout(() => {
      m.remove()
      if (dismissingMini === m) dismissingMini = null
      miniHideTimer = null
    }, 120)
  }

  function positionMini(rect: DOMRect): void {
    if (!mini) return
    placeCapsule(mini, rect.top, rect.left + rect.width / 2, mini.offsetWidth || MINI_W, mini.offsetHeight || MINI_H)
  }

  // Synchronous, idempotent router: show, reposition, or hide the mini
  // based on the CURRENT selection. Every gate failing means the mini must
  // not exist (state transition, dead selection, unusable anchor).
  function showOrMoveMini(): void {
    const sel = selectionWithin(container)
    if (!menu && !held && !holdRow && sel && sel.text.trim()) {
      const rect = usableRect(sel.range)
      if (rect) {
        if (mini) {
          positionMini(rect)
          return
        }
        // Revive the dismissing element instead of creating a second one:
        // a hide+show burst (contextmenu runs close() then this) would
        // otherwise stack a fading capsule under a fresh one at the same
        // coordinates — visible as a one-frame flash on real devices.
        if (dismissingMini) {
          if (miniHideTimer !== null) {
            clearTimeout(miniHideTimer)
            miniHideTimer = null
          }
          dismissingMini.classList.remove('share-closing')
          mini = dismissingMini
          dismissingMini = null
          positionMini(rect)
          return
        }
        mini = createNode('div', { className: 'share-mini' })
        // Inline, like the main capsule: an unselectable surface means the
        // copy tap does not collapse the live selection it is about to
        // read — and jsdom computed styles never see the stylesheet.
        mini.style.userSelect = 'none'
        const copyItem = createNode('button', {
          className: 'share-menu-item',
          attrs: { type: 'button', 'aria-label': t('shareMenuCopy') },
        })
        copyItem.appendChild(renderIcon('copy'))
        copyItem.addEventListener('click', () => {
          // Read BEFORE hideMini(): the selection is the payload. A
          // selection swapped to whitespace between show and tap (no
          // selectionchange fired) collapses to a silent dismissal — no
          // clipboard write, no toast.
          const text = selectionWithin(container)?.text.trim() ?? ''
          hideMini()
          if (!text) return
          void copyText(text).then(
            () => {
              toast(t('shareCopied'))
            },
            () => {
              toast(t('shareCopyFailed'))
            },
          )
        })
        mini.appendChild(copyItem)
        host.appendChild(mini)
        positionMini(rect)
        return
      }
    }
    hideMini()
  }

  // selectionchange can burst per frame; one rAF per burst, and the frame
  // re-runs showOrMoveMini so an intra-frame state change (a long-press
  // opening the main capsule) cannot resurrect the mini after the fact.
  function scheduleMiniUpdate(): void {
    if (miniRafPending) return
    miniRafPending = true
    requestAnimationFrame(() => {
      miniRafPending = false
      if (dead) return
      showOrMoveMini()
    })
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
    // Copy = the partial selection when handles were used, else the source
    // text (the pressed block's text, per-block semantics). The system menu
    // stays suppressed — handles alone are enough for our copy item to read
    // the selection.
    const copyItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuCopy') },
    })
    copyItem.appendChild(renderIcon('copy'))
    // Export-as-file group (image, markdown) then copy. Old APK shells lack
    // bridge.shareText — probing at open time hides the MD item rather than
    // shipping a dead button; the capsule then keeps its two-item shape.
    const bridge = getShareBridge()
    const imageItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuImage') },
    })
    imageItem.appendChild(renderIcon('imageUp'))
    imageItem.addEventListener('click', () => {
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
    if (bridge && typeof bridge.shareText === 'function') {
      const mdItem = createNode('button', {
        className: 'share-menu-item',
        attrs: { type: 'button', 'aria-label': t('shareMenuMarkdown') },
      })
      mdItem.appendChild(renderIcon('markdownUp'))
      mdItem.addEventListener('click', () => {
        const src = currentSource
        close()
        if (!src) return
        const res = shareMarkdown(src)
        toast(res === 'opened' ? t('shareImageReady') : t('shareImageFailed'))
      })
      menu.append(imageItem, mdItem, createNode('span', { className: 'share-vsep' }), copyItem)
    } else {
      menu.append(imageItem, createNode('span', { className: 'share-vsep' }), copyItem)
    }
    host.appendChild(menu)
    copyItem.addEventListener('click', () => {
      const src = currentSource
      // Read the selection BEFORE close(): handles-dragged partial wins;
      // no selection (or one belonging to another row) falls back to the
      // source text.
      const selected = selectionWithin(row)?.text ?? ''
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
    positionMenu(row, touchY)
  }

  // Vertical anchor precedence: the selection's bounding box (system
  // toolbar style) → the touch point. Never the row's rect: wui rows can
  // be thousands of pixels tall and row-rect anchoring reads as detached.
  // The row rect is used for `left` only. Extracted from open() so scroll /
  // selectionchange can re-run the exact same math as the selection moves.
  function positionMenu(row: HTMLElement, touchY: number): void {
    if (!menu) return
    const rect = usableRect(selectionWithin(row)?.range ?? null)
    const r = row.getBoundingClientRect()
    // mw/mh fall back to the demo values when layout has not run (jsdom) —
    // real measurements win in the browser.
    placeCapsule(menu, rect ? rect.top : touchY, r.left + r.width / 2, menu.offsetWidth || 94, menu.offsetHeight || 48)
  }

  // A menu anchored to a live selection FOLLOWS it through scrolls and
  // handle drags instead of closing — gluing to the selection IS the
  // behavior, and when it scrolls fully out of the host the menu follows it
  // out and is clipped by the host's overflow:hidden (no clamping; the
  // native handles share the same fate). One rAF per frame; a reposition
  // scheduled before a close/destroy must never resurrect the menu.
  let rafPending = false
  const scheduleReposition = (): void => {
    if (!menu || !held) return
    if (rafPending) return
    rafPending = true
    requestAnimationFrame(() => {
      rafPending = false
      if (!menu || !held) return
      if (!held.isConnected || !selectionWithin(held)) {
        close()
        return
      }
      positionMenu(held, startY)
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
    const src = getSource(row, target)
    if (!src) {
      return
    }
    startY = e.clientY
    startX = e.clientX
    // Any successfully armed gesture ends in a capsule (or nothing) — the
    // old mini loses its right to exist the moment the hold arms.
    hideMini()
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
    if (!container.contains(e.target as Node)) return
    // This event IS the long-press moment: the browser has just started the
    // native selection (handles coming up) and is about to show its own
    // menu. For an armed float hold we open our capsule HERE so handles and
    // menu coexist; preventDefault kills the system menu only. Opening on
    // pointerup instead would race the selection takeover — the browser ends
    // the pointer stream at contextmenu (pointerout/pointercancel follow),
    // so waiting for lift made handles and menu mutually exclusive.
    if (holdRow && holdSrc) {
      e.preventDefault()
      const row = holdRow
      const src = holdSrc
      clearTimer()
      holdRow = null
      holdSrc = null
      holdPointerId = null
      if (row.isConnected) open(row, src, startY)
      return
    }
    // A live in-container selection owns the content area: the system
    // toolbar would collide with the mini that already follows this exact
    // selection, so the gesture becomes ours — capsule down, mini up.
    if (selectionWithin(container)?.text.trim()) {
      e.preventDefault()
      close()
      showOrMoveMini()
      return
    }
    // No hold, no selection (chrome, empty tool block): nothing to anchor
    // a capsule to — hand the gesture to the WebView untouched.
    if (menu) close()
  }
  const onDocClick = (e: MouseEvent): void => {
    const t = e.target as Node
    if (menu && !menu.contains(t)) close()
    // Desktop taps on chrome don't collapse the selection (no
    // selectionchange fires), so an explicit click is the only dismissal
    // signal the mini gets from outside itself.
    if (mini && !mini.contains(t)) hideMini()
  }
  // The menu is absolutely positioned on the host while the rows scroll.
  // Scroll events don't bubble; the capture phase is how they reach the host.
  // A menu anchored to a live selection follows it; every other open menu
  // (no selection, held row detached) still closes — a capsule floating over
  // content that moved with no anchor reads as broken. The riderless mini
  // follows the same way through its own rAF.
  const onScroll = (): void => {
    if (menu) {
      if (held && held.isConnected && selectionWithin(held)) scheduleReposition()
      else close()
      return
    }
    if (mini) scheduleMiniUpdate()
  }
  // Handle drags change the selection rect live; ride the same rAF path so
  // the menu tracks the handles. Detached row (rewind mid-menu) → close.
  const onSelectionChange = (): void => {
    if (menu || held) {
      // Set together in open(); the not-both shape keeps the old no-op.
      if (!menu || !held) return
      if (!held.isConnected) {
        close()
        return
      }
      if (selectionWithin(held)) scheduleReposition()
      return
    }
    if (holdRow) {
      // An armed long-press is mid-flight: the native takeover makes the
      // selection jitter, and a mini flashing for one frame before the
      // main capsule opens would read as a glitch. Swallow, never show.
      hideMini()
      return
    }
    scheduleMiniUpdate()
  }

  container.addEventListener('pointerdown', onPointerDown)
  container.addEventListener('pointermove', onPointerMove)
  container.addEventListener('pointerup', onPointerUp)
  container.addEventListener('pointercancel', onPointerCancel)
  container.addEventListener('contextmenu', onContextMenu)
  document.addEventListener('click', onDocClick)
  document.addEventListener('selectionchange', onSelectionChange)
  host.addEventListener('scroll', onScroll, true)

  return {
    close,
    destroy: () => {
      // Must precede everything else: the pending mini rAF checks this
      // flag, and a mini removed via hideMini() would linger for the
      // 120ms fade (and its timer with it) instead of dying here.
      dead = true
      clearTimer()
      holdRow = null
      holdSrc = null
      holdPointerId = null
      disarmClickSwallow()
      if (menu) {
        menu.remove()
        menu = null
      }
      mini?.remove()
      mini = null
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
      document.removeEventListener('selectionchange', onSelectionChange)
      host.removeEventListener('scroll', onScroll, true)
    },
  }
}
