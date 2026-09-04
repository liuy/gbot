// App-wide theme engine, extracted from the sidebar (whose toggle button
// is gone — theme now lives in Settings). One initTheme() at startup wires
// everything: resolved theme application, the desktop matchMedia listener,
// and the __gbotApplySystemTheme hook the Android host calls on every page
// load and system flip.

import { applyHljsTheme, getSavedHljsTheme } from './hljs_themes'

export type ThemePref = 'dark' | 'light' | 'system'

// The Android WebView's prefers-color-scheme query is unreliable — it
// reports light while the system is dark, and never updates on system
// flips. The Kotlin host pushes the REAL system theme through
// __gbotApplySystemTheme; remember it and resolve 'system' from that,
// falling back to matchMedia only where no push ever arrived (desktop).
let lastNativeIsLight: boolean | null = null

export const getThemePref = (): ThemePref =>
  (localStorage.getItem('gbot-theme') || 'dark') as ThemePref

const resolveTheme = (pref: ThemePref): 'dark' | 'light' => {
  if (pref === 'system') {
    if (lastNativeIsLight !== null) return lastNativeIsLight ? 'light' : 'dark'
    return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
  }
  return pref
}

export const getResolvedTheme = (): 'dark' | 'light' => resolveTheme(getThemePref())

// Report the effective theme to the Android host (GBotNative JS interface
// registered by ChatFragment) so the status-bar icon color matches the
// header background. Optional-chained: no-op on desktop / server WUI.
const notifyNativeTheme = (resolved: 'dark' | 'light') => {
  const native = (window as unknown as {
    GBotNative?: { onThemeChanged?: (isDark: boolean) => void }
  }).GBotNative
  native?.onThemeChanged?.(resolved === 'dark')
}

const applyResolved = (resolved: 'dark' | 'light') => {
  document.documentElement.dataset.theme = resolved
  applyHljsTheme(getSavedHljsTheme(), resolved === 'dark')
  notifyNativeTheme(resolved)
}

export const setThemePref = (pref: ThemePref) => {
  localStorage.setItem('gbot-theme', pref)
  applyResolved(resolveTheme(pref))
}

export const initTheme = () => {
  applyResolved(resolveTheme(getThemePref()))
  // Desktop path: the browser fires this on system flips. The WebView
  // does not — that's what the native hook below is for.
  window
    .matchMedia('(prefers-color-scheme: light)')
    .addEventListener('change', () => {
      if (getThemePref() === 'system') applyResolved(resolveTheme('system'))
    })
  // The pushed value is ALWAYS remembered (even with an explicit pref) so
  // a later switch to 'system' resolves from the native truth.
  Object.assign(window, {
    __gbotApplySystemTheme: (isLight: boolean) => {
      lastNativeIsLight = isLight
      if (getThemePref() !== 'system') return
      applyResolved(isLight ? 'light' : 'dark')
    },
  })
}
