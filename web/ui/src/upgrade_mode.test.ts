import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'
import { createUpgradeCapsule, refuseUpgrade, setStreamFreeze } from './upgrade_mode'
import { registerCapsule } from './capsule'
import { setLocale } from './i18n'
import { t } from './i18n'

describe('upgrade capsule', () => {
  beforeEach(() => {
    setLocale('en')
    vi.useFakeTimers()
    registerCapsule(null)
  })
  afterEach(() => {
    vi.useRealTimers()
  })

  it('starts hidden and enter() shows it with the localized upgrading text', () => {
    const capsule = createUpgradeCapsule()
    expect(capsule.root.classList.contains('opacity-0')).toBe(true)
    expect(capsule.root.classList.contains('capsule-show')).toBe(false)
    expect(capsule.active()).toBe(false)

    capsule.enter()
    expect(capsule.active()).toBe(true)
    expect(capsule.root.classList.contains('capsule-show')).toBe(true)
    expect(capsule.root.classList.contains('opacity-0')).toBe(false)
    expect(capsule.root.textContent).toContain(t('upgradeCapsuleText'))
  })

  it('recovered() swaps text and auto-dismisses after 1000ms; hide keeps the last label (no flash)', () => {
    const capsule = createUpgradeCapsule()
    capsule.enter()
    capsule.recovered()
    expect(capsule.root.textContent).toContain(t('upgradeRecoveredText'))

    vi.advanceTimersByTime(999)
    expect(capsule.root.classList.contains('capsule-show')).toBe(true)
    vi.advanceTimersByTime(1)
    expect(capsule.root.classList.contains('capsule-show')).toBe(false)
    expect(capsule.active()).toBe(false)
    // The fade shows the recovered label verbatim — hide() rewrites
    // nothing. The next enter() swaps back to the upgrading label.
    expect(capsule.root.textContent).toContain(t('upgradeRecoveredText'))
    capsule.enter()
    expect(capsule.root.textContent).toContain(t('upgradeCapsuleText'))
  })

  it('refused() maps the tui_mode code to localized text; unknown codes fall back to prefix + server message', () => {
    const capsule = createUpgradeCapsule()
    capsule.refused('tui_mode', 'server fallback sentence')
    expect(capsule.root.textContent).toContain(t('refusalTuiMode'))
    expect(capsule.root.textContent).not.toContain('server fallback sentence')
    expect(capsule.active()).toBe(true)
    vi.advanceTimersByTime(4000)
    expect(capsule.active()).toBe(false)
    // Hide never rewrites the label — no "Upgrading…" flash mid-fade.
    expect(capsule.root.textContent).toContain(t('refusalTuiMode'))

    capsule.refused('future_unknown_code', 'raw server message')
    expect(capsule.root.textContent).toContain(t('upgradeRefusedPrefix') + 'raw server message')
  })
  it('refuseUpgrade routes through the registered generic capsule', () => {
    const capsule = createUpgradeCapsule()
    refuseUpgrade('tui_mode', 'ignored fallback')
    expect(capsule.active()).toBe(true)
    expect(capsule.root.textContent).toContain(t('refusalTuiMode'))
    capsule.dismiss()
  })
  it('dismiss() hides immediately', () => {
    const capsule = createUpgradeCapsule()
    capsule.enter()
    capsule.dismiss()
    expect(capsule.active()).toBe(false)
    expect(capsule.root.classList.contains('capsule-show')).toBe(false)
    expect(capsule.root.classList.contains('opacity-0')).toBe(true)
  })

  it('re-enter during the recovered window re-shows the upgrading text', () => {
    const capsule = createUpgradeCapsule()
    capsule.enter()
    capsule.recovered()
    capsule.enter()
    expect(capsule.root.textContent).toContain(t('upgradeCapsuleText'))
    expect(capsule.root.classList.contains('capsule-show')).toBe(true)
    vi.advanceTimersByTime(2000)
    // The recovered() timer must have been cancelled — still shown.
    expect(capsule.root.classList.contains('capsule-show')).toBe(true)
  })

  it('renders under the zh locale too (i18n anchor)', () => {
    setLocale('zh')
    const capsule = createUpgradeCapsule()
    capsule.enter()
    expect(capsule.root.textContent).toContain('升级中…')
    capsule.recovered()
    expect(capsule.root.textContent).toContain('已恢复')
    setLocale('en')
  })
})

describe('stream freeze', () => {
  const buildContainer = () => {
    const c = document.createElement('div')
    for (let i = 0; i < 3; i++) {
      const child = document.createElement('div')
      child.innerHTML = `<p>message ${i}</p>`
      c.appendChild(child)
    }
    return c
  }

  it('dims ONLY the last child; other children byte-identical; no attributes added', () => {
    const c = buildContainer()
    const before = Array.from(c.children).map(el => el.outerHTML)

    setStreamFreeze(c, true)

    const children = Array.from(c.children)
    expect(children[2].classList.contains('stream-dim')).toBe(true)
    expect(children[0].classList.contains('stream-dim')).toBe(false)
    expect(children[1].classList.contains('stream-dim')).toBe(false)
    expect(children[0].outerHTML).toBe(before[0])
    expect(children[1].outerHTML).toBe(before[1])
    // The freeze must not restructure or disable anything.
    expect(c.querySelector('[disabled]')).toBeNull()
    expect(c.querySelector('[aria-disabled]')).toBeNull()
  })

  it('unfreeze removes every stream-dim class', () => {
    const c = buildContainer()
    setStreamFreeze(c, true)
    setStreamFreeze(c, false)
    expect(c.querySelectorAll('.stream-dim').length).toBe(0)
    expect(c.children[2].classList.contains('stream-dim')).toBe(false)
  })

  it('freeze on an empty container is a no-op', () => {
    const c = document.createElement('div')
    setStreamFreeze(c, true)
    expect(c.querySelectorAll('.stream-dim').length).toBe(0)
  })
})
