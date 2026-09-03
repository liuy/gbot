import { describe, it, expect, vi } from 'vitest'
import { createSidebar } from './sidebar'
import type { ArtifactListItem, SessionListItem } from './types'

function setup() {
  const mainContent = document.createElement('div')
  const sidebar = createSidebar({ mainContent })
  return { mainContent, sidebar }
}

describe('createSidebar', () => {
  it('returns handles with root, overlay, open, close, toggle, setSessions', () => {
    const { sidebar } = setup()
    expect(sidebar.root).toBeInstanceOf(HTMLElement)
    expect(sidebar.overlay).toBeInstanceOf(HTMLElement)
    expect(typeof sidebar.open).toBe('function')
    expect(typeof sidebar.close).toBe('function')
    expect(typeof sidebar.toggle).toBe('function')
    expect(typeof sidebar.setSessions).toBe('function')
  })

  it('root starts with translateX(-100%)', () => {
    const { sidebar } = setup()
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
  })

  it('root pads below the status-bar safe area (mobile edge-to-edge)', () => {
    const { sidebar } = setup()
    // .sidebar-safe-top carries padding-top: env(safe-area-inset-top) in
    // index.css — class asserted instead of the style property because jsdom
    // drops env() values it cannot parse. Same pattern as the main header.
    expect(sidebar.root.classList.contains('sidebar-safe-top')).toBe(true)
  })

  it('list maxHeight subtracts the safe-area inset', () => {
    const { sidebar } = setup()
    const list = sidebar.root.querySelector('.overflow-y-auto') as HTMLElement
    // .sidebar-list-height carries the calc() cap; without it the last
    // sessions/games rows scroll under the status bar on mobile.
    expect(list.classList.contains('sidebar-list-height')).toBe(true)
  })

  it('overlay starts hidden (display none)', () => {
    const { sidebar } = setup()
    expect(sidebar.overlay.style.display).toBe('none')
  })

  it('open sets root to translateX(0px) and mainContent to translateX(288px)', () => {
    const { mainContent, sidebar } = setup()
    sidebar.open()
    expect(sidebar.root.style.transform).toBe('translateX(0px)')
    expect(mainContent.style.transform).toBe('translateX(288px)')
    expect(sidebar.overlay.style.display).not.toBe('none')
  })

  it('close reverts transforms and hides overlay', () => {
    const { mainContent, sidebar } = setup()
    sidebar.open()
    sidebar.close()
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
    expect(sidebar.overlay.style.display).toBe('none')
  })

  it('toggle opens when closed and closes when open', () => {
    const { sidebar } = setup()
    sidebar.toggle()
    expect(sidebar.root.style.transform).toBe('translateX(0px)')
    sidebar.toggle()
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
  })

  it('setSessions renders rows with correct highlight for current session', () => {
    const { sidebar } = setup()
    const sessions: SessionListItem[] = [
      { id: 's1', title: 'First', updatedAt: Date.now() - 3600000 },
      { id: 's2', title: 'Second', updatedAt: Date.now() },
    ]
    sidebar.setSessions(sessions, 's1')
    // Games group rows (Step 5) are always present; session tests exclude them.
    const rows = sidebar.root.querySelectorAll('[class*="cursor-pointer"]:not([data-game-row])')
    expect(rows.length).toBe(2)
    expect(rows[0].className).toContain('bg-blue/15')
    expect(rows[1].className).not.toContain('bg-blue/15')
  })

  it('setStreaming(true) grays session rows and survives setSessions rebuild', () => {
    const { sidebar } = setup()
    const sessions: SessionListItem[] = [
      { id: 's1', title: 'First', updatedAt: Date.now() },
    ]
    sidebar.setStreaming(true)
    sidebar.setSessions(sessions, '')
    const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
    expect(row.className).toContain('pointer-events-none')
    expect(row.className).toContain('opacity-40')
  })
  it('setStreaming(false) restores session rows', () => {
    const { sidebar } = setup()
    const sessions: SessionListItem[] = [
      { id: 's1', title: 'First', updatedAt: Date.now() },
    ]
    sidebar.setStreaming(true)
    sidebar.setStreaming(false)
    sidebar.setSessions(sessions, '')
    const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
    expect(row.className).not.toContain('pointer-events-none')
  })
  it('sessions header row shows title with a new-session button and no FAB', () => {
    const { sidebar } = setup()
    const header = sidebar.root.querySelector('[data-sessions-header]') as HTMLElement
    expect(header).not.toBeNull()
    expect(header.textContent).toContain('Sessions')
    const btn = header.querySelector('[data-new-session]') as HTMLElement
    expect(btn).not.toBeNull()
    // The floating FAB is gone — no absolute button may cover list rows.
    expect(sidebar.root.querySelector('button.absolute.bottom-5.right-5')).toBeNull()
  })
  it('new-session button click during streaming does not call onNewSession', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    sidebar.setStreaming(true)
    const btn = sidebar.root.querySelector('[data-new-session]') as HTMLElement
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(handler).not.toHaveBeenCalled()
  })
  it('new-session button click when idle calls onNewSession', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    const btn = sidebar.root.querySelector('[data-new-session]') as HTMLElement
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(handler).toHaveBeenCalledTimes(1)
  })
  it('clicking a session row calls onSessionClick and closes', () => {
    const { mainContent, sidebar } = setup()
    const handler = vi.fn()
    sidebar.onSessionClick(handler)
    sidebar.open()
    const sessions: SessionListItem[] = [
      { id: 'abc', title: 'Test', updatedAt: Date.now() },
    ]
    sidebar.setSessions(sessions, 'abc')

    const row = sidebar.root.querySelector('[class*="cursor-pointer"]') as HTMLElement
    expect(row).not.toBeNull()
    row.click()
    expect(handler).toHaveBeenCalledWith('abc')
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
  })

  it('clicking new-session calls onNewSession and closes', () => {
    const { mainContent, sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    sidebar.open()

    const btn = sidebar.root.querySelector('[data-new-session]') as HTMLElement
    expect(btn).not.toBeNull()
    btn.click()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
  })

  it('setSessions with empty list renders no rows', () => {
    const { sidebar } = setup()
    sidebar.setSessions([], '')
    const rows = sidebar.root.querySelectorAll('[class*="cursor-pointer"]:not([data-game-row])')
    expect(rows.length).toBe(0)
  })

  it('onRename fires with id and new title', () => {
    const { mainContent } = { mainContent: document.createElement('div') }
    const sidebar = createSidebar({ mainContent })
    const handler = vi.fn()
    sidebar.onRename(handler)
    const sessions: SessionListItem[] = [
      { id: 'x1', title: 'Original', updatedAt: Date.now() },
    ]
    sidebar.setSessions(sessions, 'x1')

    const row = sidebar.root.querySelector('[class*="cursor-pointer"]') as HTMLElement
    const titleSpan = row.querySelector('span') as HTMLElement
    expect(titleSpan.textContent).toBe('Original')

    // Simulate long-press rename by calling touchstart then waiting
    vi.useFakeTimers()
    row.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(600)

    // After long-press, an input should appear
    const input = row.querySelector('input') as HTMLInputElement
    expect(input).not.toBeNull()
    expect(input.value).toBe('Original')

    input.value = 'Renamed'
    input.dispatchEvent(new Event('blur'))

    vi.useRealTimers()

    expect(handler).toHaveBeenCalledWith('x1', 'Renamed')
    const restoredSpan = row.querySelector('span') as HTMLElement
    expect(restoredSpan.textContent).toBe('Renamed')
  })

  it('long-press theme toggle opens hljs popover and does not cycle theme', () => {
    // consumeTrigger lets the click handler bail out when a long-press
    // just fired, so the synthesized post-touch click doesn't cycle theme.
    const { sidebar } = setup()
    const themeToggle = sidebar.root.querySelector('button.absolute.bottom-5.left-5') as HTMLElement
    expect(themeToggle).not.toBeNull()

    const themeBefore = document.documentElement.dataset.theme
    expect(themeBefore).toBe('dark')

    vi.useFakeTimers()
    themeToggle.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(600)
    vi.useRealTimers()

    // Popover opened.
    const popover = document.getElementById('hljs-popover')
    expect(popover).not.toBeNull()
    expect(popover?.textContent).toContain('Highlight Theme')

    // Now dispatch the click that the browser would synthesize after touch.
    themeToggle.dispatchEvent(new MouseEvent('click', { bubbles: true }))

    // Theme must NOT have cycled.
    expect(document.documentElement.dataset.theme).toBe('dark')
  })

  describe('artifacts section', () => {
    const artifacts: ArtifactListItem[] = [
      { name: 'game.html', size: 100, mtime: Date.now() - 60_000 },
      { name: 'demos/report.pdf', size: 2048, mtime: Date.now() - 3_600_000 },
    ]

    it('setArtifacts renders rows with name and relative time', () => {
      const { sidebar } = setup()
      sidebar.setArtifacts(artifacts)
      const section = sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement
      expect(section).not.toBeNull()
      const rows = section.querySelectorAll('.artifact-row')
      expect(rows.length).toBe(2)
      expect(rows[0].textContent).toContain('game.html')
      expect(rows[0].textContent).toContain('1m')
      expect(rows[1].textContent).toContain('demos/report.pdf')
      expect(rows[1].textContent).toContain('1h')
    })

    it('clicking an artifact row calls onArtifactClick with the full name and closes', () => {
      const { mainContent, sidebar } = setup()
      const handler = vi.fn()
      sidebar.onArtifactClick(handler)
      sidebar.open()
      sidebar.setArtifacts(artifacts)

      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(2)
      ;(rows[1] as HTMLElement).click()
      expect(handler).toHaveBeenCalledTimes(1)
      expect(handler).toHaveBeenCalledWith('demos/report.pdf')
      expect(sidebar.root.style.transform).toBe('translateX(-100%)')
      expect(mainContent.style.transform).toBe('translateX(0px)')
    })

    it('setArtifacts with empty list shows the empty state', () => {
      const { sidebar } = setup()
      sidebar.setArtifacts([])
      const section = sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement
      expect(section?.querySelectorAll('.artifact-row').length).toBe(0)
      expect(section?.textContent).toContain('No artifacts yet')
    })

    it('setSessions does not clear the artifacts section', () => {
      const { sidebar } = setup()
      sidebar.setArtifacts(artifacts)
      const sessions: SessionListItem[] = [
        { id: 's1', title: 'First', updatedAt: Date.now() },
      ]
      sidebar.setSessions(sessions, 's1')
      const section = sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement
      expect(section?.querySelectorAll('.artifact-row').length).toBe(2)
      // Session rows still render alongside.
      expect(sidebar.root.querySelectorAll('[class*="cursor-pointer"]:not([data-game-row])').length).toBe(3)
    })
    it('setSessions rebuild does not remove the sessions header row', () => {
      // The header is a sibling of sessionsList, not a child — the rebuild
      // wipes only sessionsList.innerHTML.
      const { sidebar } = setup()
      sidebar.setSessions([{ id: 's1', title: 'First', updatedAt: Date.now() }], 's1')
      sidebar.setSessions([{ id: 's2', title: 'Second', updatedAt: Date.now() }], 's2')
      expect(sidebar.root.querySelector('[data-sessions-header]')).not.toBeNull()
      expect(sidebar.root.querySelector('[data-new-session]')).not.toBeNull()
    })

    it('open fires the onOpen handler', () => {
      const { sidebar } = setup()
      const handler = vi.fn()
      sidebar.onOpen(handler)
      sidebar.open()
      expect(handler).toHaveBeenCalledTimes(1)
    })
  })

  describe('__gbotApplySystemTheme (Android WebView bridge)', () => {
    const getHook = (): ((isLight: boolean) => void) | undefined =>
      (window as Record<string, unknown>).__gbotApplySystemTheme as
        ((isLight: boolean) => void) | undefined

    it('HookDefined_AfterCreate', () => {
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      // Kotlin onConfigurationChanged calls this by name — must exist.
      expect(typeof getHook()).toBe('function')
    })

    it('SystemLight_SetsLightTheme', () => {
      localStorage.setItem('gbot-theme', 'system')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      getHook()!(true)
      expect(document.documentElement.dataset.theme).toBe('light')
    })

    it('SystemDark_SetsDarkTheme', () => {
      localStorage.setItem('gbot-theme', 'system')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      getHook()!(false)
      expect(document.documentElement.dataset.theme).toBe('dark')
    })

    it('ExplicitPref_NotOverridden', () => {
      localStorage.setItem('gbot-theme', 'dark')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      getHook()!(true)
      // User chose dark explicitly — system switch must not override.
      expect(document.documentElement.dataset.theme).toBe('dark')
    })

    it('SystemFollowsLastNativePush_NotLyingMatchMedia', () => {
      // Android WebView's matchMedia snapshot is unreliable (ours reports
      // light while the system is dark). The native push is the truth: a
      // later cycle to 'system' must resolve from the pushed value, not
      // from matchMedia.
      const origMatchMedia = window.matchMedia
      try {
        window.matchMedia = (q: string) =>
          ({ matches: true, media: q, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false }) as MediaQueryList
        localStorage.setItem('gbot-theme', 'dark')
        const { sidebar } = setup()
        document.body.appendChild(sidebar.root)
        getHook()!(false) // native pushes: real system is dark
        // User cycles dark → light → system: landing on 'system' must resolve
        // from the pushed value (dark), not from the lying matchMedia (light).
        const toggle = sidebar.root.querySelector('button.absolute.bottom-5.left-5') as HTMLElement
        toggle.dispatchEvent(new MouseEvent('click', { bubbles: true })) // dark → light
        toggle.dispatchEvent(new MouseEvent('click', { bubbles: true })) // light → system
        expect(document.documentElement.dataset.theme).toBe('dark')
      } finally {
        window.matchMedia = origMatchMedia // restore — later tests need the setup stub
      }
    })
  })

  describe('GBotNative.onThemeChanged (status-bar icon bridge)', () => {
    // The Android host registers a GBotNative JS interface; the WUI must
    // report every effective-theme change so the status-bar icon color can
    // match the header background (dark theme → light icons, and vice versa).
    let calls: boolean[]

    beforeEach(() => {
      calls = []
      ;(window as Record<string, unknown>).GBotNative = {
        onThemeChanged: (isDark: boolean) => calls.push(isDark),
      }
    })

    afterEach(() => {
      delete (window as Record<string, unknown>).GBotNative
    })

    it('NotifiesNative_OnInitialResolve', () => {
      localStorage.setItem('gbot-theme', 'light')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      expect(calls).toEqual([false])
    })

    it('NotifiesNative_WhenThemeCycles', () => {
      localStorage.setItem('gbot-theme', 'dark')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      const toggle = sidebar.root.querySelector('button.absolute.bottom-5.left-5') as HTMLElement
      toggle.dispatchEvent(new MouseEvent('click', { bubbles: true })) // dark → light
      expect(calls.length).toBeGreaterThanOrEqual(2)
      expect(calls[calls.length - 1]).toBe(false)
    })

    it('NotifiesNative_WhenSystemFlipsAndPrefIsSystem', () => {
      localStorage.setItem('gbot-theme', 'system')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      const hook = (window as Record<string, unknown>).__gbotApplySystemTheme as
        (isLight: boolean) => void
      // jsdom matchMedia stub always reports dark, so hook(true) proves the
      // notification carries the hook's value, not a stale matchMedia read.
      hook(true) // system went light
      expect(calls.length).toBeGreaterThanOrEqual(2)
      expect(calls[calls.length - 1]).toBe(false)
    })

    it('ExplicitPref_SystemFlipDoesNotNotify', () => {
      // Companion invariant of ExplicitPref_NotOverridden: with an explicit
      // user pref the apply() hook must not touch the theme or notify native
      // (the last web-set value already matches the pref).
      localStorage.setItem('gbot-theme', 'dark')
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      expect(calls).toEqual([true]) // initial resolve only
      const hook = (window as Record<string, unknown>).__gbotApplySystemTheme as
        (isLight: boolean) => void
      hook(true)
      expect(calls).toEqual([true]) // no additional notify
    })
  })
})
