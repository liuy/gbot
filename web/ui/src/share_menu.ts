// Long-press message menu: Liquid Glass two-item popup (share as image /
// copy text), interaction ported from the approved share-menu-demo.html with
// two deliberate deviations: the menu/scrim/toast mount on mainContent with
// absolute coordinates (a lingering sidebar translateX would otherwise trap
// fixed elements), and items await their action so the success/failure toast
// is truthful. Entry is gated on the Android shell bridge — without
// window.GBotShare initShareMenu no-ops and desktop never arms a listener.
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

/** text and user blocks joined with a blank line; thinking/tool/image/
 *  document are chrome, not shareable content. Empty string when none. */
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
  /** mainContent: absolute positioning host for menu/scrim/toast. */
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

  const scrim = createNode('div', { className: 'share-scrim' })
  host.appendChild(scrim)

  let menu: HTMLElement | null = null
  let held: HTMLElement | null = null
  let currentSource: ShareSource | null = null
  let timer: ReturnType<typeof setTimeout> | null = null
  let startY = 0

  function clearTimer(): void {
    if (timer !== null) {
      clearTimeout(timer)
      timer = null
    }
  }

  function close(): void {
    if (!menu) return
    const m = menu
    menu = null
    currentSource = null
    m.classList.add('share-closing')
    setTimeout(() => m.remove(), 120)
    scrim.classList.remove('share-open')
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

  function open(row: HTMLElement, src: ShareSource, touchY: number): void {
    close()
    held = row
    currentSource = src
    row.classList.add('share-lifted')
    container.classList.add('share-holding')
    scrim.classList.add('share-open')

    menu = createNode('div', { className: 'share-menu' })
    const shareItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuImage') },
    })
    shareItem.appendChild(renderIcon('share'))
    const copyItem = createNode('button', {
      className: 'share-menu-item',
      attrs: { type: 'button', 'aria-label': t('shareMenuCopy') },
    })
    copyItem.appendChild(renderIcon('copy'))
    menu.append(shareItem, createNode('span', { className: 'share-vsep' }), copyItem)
    host.appendChild(menu)

    // Anchor at the long-press point, not the row's geometry: wui rows can be
    // thousands of pixels tall, and row-rect anchoring pushes the menu to the
    // row's far edge (clamped to the screen bottom — reads as detached). The
    // touch point always sits inside the viewport, so a single flip covers it.
    const hostRect = host.getBoundingClientRect()
    // offsetWidth/Height fall back to the demo values when layout has not
    // run (jsdom) — real measurements win in the browser.
    const mw = menu.offsetWidth || 94
    const mh = menu.offsetHeight || 48
    const touch = touchY - hostRect.top
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
    // left stays in client space on purpose: mainContent never carries a
    // horizontal offset when the menu can open (the sidebar's fullscreen
    // scrim swallows presses while it covers the rows), so hostRect.left
    // is 0 and the subtraction would be a no-op.
    let left = r.left + r.width / 2 - mw / 2
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
    copyItem.addEventListener('click', () => {
      const src = currentSource
      close()
      if (!src) return
      void copyText(src.text).then(
        () => {
          toast(t('shareCopied'))
        },
        () => {
          toast(t('shareCopyFailed'))
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
    if (target.closest(AVOID_SELECTOR)) return
    const row = rowFromTarget(target)
    if (!row) return
    const src = getSource(row)
    if (!src) {
      return
    }
    startY = e.clientY
    timer = setTimeout(() => {
      timer = null
      open(row, src, startY)
    }, LONG_PRESS_MS)
  }
  const onPointerMove = (e: PointerEvent): void => {
    if (timer !== null && Math.abs((e.clientY || 0) - startY) > CANCEL_MOVE_PX) clearTimer()
  }
  const onPointerEnd = (): void => clearTimer()
  const onContextMenu = (e: MouseEvent): void => {
    // Suppressing the system menu also stops Chrome from synthesizing a
    // click after the long-press — the scrim would close instantly without
    // this (demo relies on the same detail).
    if (container.contains(e.target as Node)) e.preventDefault()
  }
  const onScrimClick = (): void => close()
  const onDocClick = (e: MouseEvent): void => {
    if (menu && !menu.contains(e.target as Node)) close()
  }

  container.addEventListener('pointerdown', onPointerDown)
  container.addEventListener('pointermove', onPointerMove)
  container.addEventListener('pointerup', onPointerEnd)
  container.addEventListener('pointercancel', onPointerEnd)
  container.addEventListener('contextmenu', onContextMenu)
  scrim.addEventListener('click', onScrimClick)
  document.addEventListener('click', onDocClick)

  return {
    close,
    destroy: () => {
      clearTimer()
      if (menu) {
        menu.remove()
        menu = null
      }
      currentSource = null
      scrim.remove()
      if (held) {
        held.classList.remove('share-lifted')
        held = null
      }
      container.classList.remove('share-root', 'share-holding')
      container.removeEventListener('pointerdown', onPointerDown)
      container.removeEventListener('pointermove', onPointerMove)
      container.removeEventListener('pointerup', onPointerEnd)
      container.removeEventListener('pointercancel', onPointerEnd)
      container.removeEventListener('contextmenu', onContextMenu)
      document.removeEventListener('click', onDocClick)
    },
  }
}
