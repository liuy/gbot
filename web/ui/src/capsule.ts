import { createNode } from './dom'
import { renderIcon } from './icons'

export interface CapsuleOpts {
  // Auto-dismiss window; omitted = sticky (e.g. an in-flight upgrade must
  // stay visible until the handover resolves it).
  durationMs?: number
}

export interface CapsuleHandle {
  root: HTMLElement          // data-status-capsule, starts opacity-0
  active: () => boolean
  show: (text: string, opts?: CapsuleOpts) => void
  hide: () => void
}

// The status capsule is the app's transient one-line notice channel under
// the header. Transport only: it receives final text and timing — i18n and
// semantics live at call sites. hide() never rewrites the text: the fade
// must show the last notice verbatim, not a default label (2026-09-22
// flash bug: a refusal visibly morphed into "Upgrading…" mid-fade).
export function createCapsule(): CapsuleHandle {
  const textSpan = createNode('span', {})
  const root = createNode(
    'div',
    {
      className: 'fixed left-1/2 -translate-x-1/2 top-[calc(2.75rem+env(safe-area-inset-top,0px)+0.75rem)] z-[70] pointer-events-none rounded-full bg-ink3 border border-hairline px-4 py-1.5 flex items-center gap-2 text-[12px] text-t2 shadow-lg opacity-0 transition-opacity duration-300',
      attrs: { 'data-status-capsule': '' },
    },
    renderIcon('refresh', { size: 12, className: 'spin' }),
    textSpan,
  )

  let shown = false
  let dismissTimer: ReturnType<typeof setTimeout> | null = null

  const hide: () => void = () => {
    if (dismissTimer) { clearTimeout(dismissTimer); dismissTimer = null }
    shown = false
    root.classList.add('opacity-0')
    root.classList.remove('capsule-show')
  }

  return {
    root,
    active: () => shown,
    show: (text, opts) => {
      if (dismissTimer) { clearTimeout(dismissTimer); dismissTimer = null }
      textSpan.textContent = text
      shown = true
      root.classList.remove('opacity-0')
      root.classList.add('capsule-show')
      if (opts?.durationMs !== undefined) {
        dismissTimer = setTimeout(hide, opts.durationMs)
      }
    },
    hide,
  }
}

// Module registry so any module can raise a notice without holding the
// handle (the capsule is mounted by the chat shell). Unregistered calls
// drop silently — same degradation as a pre-mount notice.
let registered: CapsuleHandle | null = null

export function registerCapsule(h: CapsuleHandle | null): void {
  registered = h
}

export function notifyCapsule(text: string, opts?: CapsuleOpts): void {
  registered?.show(text, opts)
}
