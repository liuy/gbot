// Theme engine coverage, migrated from the sidebar suite when the sidebar
// toggle was removed (theming now lives in Settings; engine in theme.ts).
import { afterEach, describe, expect, it, vi } from 'vitest'
import { initTheme, setThemePref, getThemePref } from './theme'

const setup = (pref?: string) => {
  localStorage.clear()
  if (pref) localStorage.setItem('gbot-theme', pref)
  vi.stubGlobal('GBotNative', { onThemeChanged: vi.fn() })
  initTheme()
  const hook = (window as unknown as Record<string, unknown>).__gbotApplySystemTheme as
    (isLight: boolean) => void
  return { hook: hook!, calls: () => (window as unknown as { GBotNative: { onThemeChanged: (b: boolean) => void } }).GBotNative.onThemeChanged as unknown as ReturnType<typeof vi.fn> }
}

describe('theme engine', () => {
  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).__gbotApplySystemTheme
    vi.unstubAllGlobals()
  })

  it('HookDefined_AfterInit', () => {
    const { hook } = setup()
    expect(typeof hook).toBe('function')
  })

  it('SystemLight_SetsLightTheme', () => {
    const { hook } = setup('system')
    hook(true)
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('SystemDark_SetsDarkTheme', () => {
    const { hook } = setup('system')
    hook(false)
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('ExplicitPref_NotOverridden', () => {
    const { hook } = setup('dark')
    hook(true)
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  it('SystemFollowsLastNativePush_NotLyingMatchMedia', () => {
    const orig = window.matchMedia
    try {
      window.matchMedia = ((q: string) =>
        ({ matches: true, media: q, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false })) as unknown as typeof matchMedia
      const { hook } = setup('dark')
      hook(false) // native truth: system is dark
      setThemePref('system')
      expect(document.documentElement.dataset.theme).toBe('dark')
    } finally {
      window.matchMedia = orig
    }
  })

  it('setThemePref applies and notifies the host', () => {
    const { calls } = setup()
    setThemePref('light')
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(getThemePref()).toBe('light')
    expect(calls()).toHaveBeenCalledWith(false) // isDark=false
  })
})
