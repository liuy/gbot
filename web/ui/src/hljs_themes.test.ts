import { describe, it, expect, beforeEach } from 'vitest'
import { HLJS_THEMES, getSavedHljsTheme, saveHljsTheme, applyHljsTheme } from './hljs_themes'

// Note: ?raw CSS imports return empty in vitest's jsdom environment.
// Tests focus on logic (theme list, localStorage, style element management)
// rather than asserting specific CSS color values.

describe('hljs_themes', () => {
  beforeEach(() => {
    localStorage.clear()
    const existing = document.getElementById('hljs-theme-css')
    if (existing) existing.remove()
  })

  it('HLJS_THEMES has 13 entries sorted alphabetically by key', () => {
    expect(HLJS_THEMES).toHaveLength(13)
    const keys = HLJS_THEMES.map(t => t.key)
    const sorted = [...keys].sort()
    expect(keys).toEqual(sorted)
  })

  it('no label contains "Dark" or "Light"', () => {
    for (const t of HLJS_THEMES) {
      expect(t.label).not.toMatch(/Dark/i)
      expect(t.label).not.toMatch(/Light/i)
    }
  })

  it('each theme has darkCss and lightCss properties', () => {
    for (const t of HLJS_THEMES) {
      expect(typeof t.darkCss).toBe('string')
      expect(typeof t.lightCss).toBe('string')
    }
  })

  it('getSavedHljsTheme returns default "atom-one" when localStorage empty', () => {
    expect(getSavedHljsTheme()).toBe('atom-one')
  })

  it('saveHljsTheme/getSavedHljsTheme round-trip', () => {
    saveHljsTheme('atom-one')
    expect(getSavedHljsTheme()).toBe('atom-one')
  })

  it('applyHljsTheme creates a style element with id hljs-theme-css', () => {
    applyHljsTheme('github', true)
    const style = document.getElementById('hljs-theme-css') as HTMLStyleElement | null
    expect(style).not.toBeNull()
    expect(style!.tagName).toBe('STYLE')
  })

  it('applyHljsTheme reuses the same style element on subsequent calls', () => {
    applyHljsTheme('github', true)
    const style1 = document.getElementById('hljs-theme-css')

    applyHljsTheme('atom-one', false)
    const style2 = document.getElementById('hljs-theme-css')

    expect(style1).toBe(style2)
  })

  it('applyHljsTheme falls back to first theme for unknown key', () => {
    expect(() => applyHljsTheme('nonexistent', true)).not.toThrow()
    const style = document.getElementById('hljs-theme-css') as HTMLStyleElement | null
    expect(style).not.toBeNull()
  })
})
