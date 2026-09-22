import { t } from './i18n'
import { createCapsule, notifyCapsule, registerCapsule } from './capsule'

// Stable refusal codes shared with the server (GET reason / POST 501 body).
// The frontend maps them to localized text; unknown codes fall back to the
// server's human message so a newer daemon's new refusal still reads.
export const REFUSAL_TUI_MODE = 'tui_mode'

const refusalText = (code: string, fallback: string): string => {
  if (code === REFUSAL_TUI_MODE) return t('refusalTuiMode')
  return t('upgradeRefusedPrefix') + fallback
}

export interface UpgradeCapsuleHandle {
  root: HTMLElement
  active: () => boolean
  enter: () => void          // sticky "Upgrading…" through the handover
  recovered: () => void      // "Recovered", auto-dismiss after 1000 ms
  refused: (code: string, fallback: string) => void  // 4000 ms notice
  dismiss: () => void
}

// Upgrade semantics over the generic status capsule. i18n resolves here,
// at the semantic layer — the capsule only ever receives final text.
// Creating the upgrade handle also registers the generic capsule for
// module-level notifyCapsule callers (settings, future surfaces).
export function createUpgradeCapsule(): UpgradeCapsuleHandle {
  const c = createCapsule()
  registerCapsule(c)
  return {
    root: c.root,
    active: c.active,
    enter: () => c.show(t('upgradeCapsuleText')),
    recovered: () => c.show(t('upgradeRecoveredText'), { durationMs: 1000 }),
    refused: (code, fallback) => c.show(refusalText(code, fallback), { durationMs: 4000 }),
    dismiss: c.hide,
  }
}

export function refuseUpgrade(code: string, fallback: string): void {
  notifyCapsule(refusalText(code, fallback), { durationMs: 4000 })
}

// Freeze: dim ONLY the element currently streaming (CSS class), never
// restructure or disable existing content.
export function setStreamFreeze(container: HTMLElement, on: boolean): void {
  if (on) {
    const last = container.lastElementChild
    if (last instanceof HTMLElement) last.classList.add('stream-dim')
  } else {
    container.querySelectorAll('.stream-dim').forEach(el => el.classList.remove('stream-dim'))
  }
}
