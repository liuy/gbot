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
    const rows = sidebar.root.querySelectorAll('[class*="cursor-pointer"]')
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
  it('FAB click during streaming does not call onNewSession', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    sidebar.setStreaming(true)
    const fab = sidebar.root.querySelector('button.absolute') as HTMLElement
    fab.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(handler).not.toHaveBeenCalled()
  })
  it('FAB click when idle calls onNewSession', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    const fab = sidebar.root.querySelector('button.absolute') as HTMLElement
    fab.dispatchEvent(new MouseEvent('click', { bubbles: true }))
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

  it('clicking FAB calls onNewSession and closes', () => {
    const { mainContent, sidebar } = setup()
    const handler = vi.fn()
    sidebar.onNewSession(handler)
    sidebar.open()

    const fab = sidebar.root.querySelector('button.absolute') as HTMLElement
    expect(fab).not.toBeNull()
    fab.click()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
  })

  it('setSessions with empty list renders no rows', () => {
    const { sidebar } = setup()
    sidebar.setSessions([], '')
    const rows = sidebar.root.querySelectorAll('[class*="cursor-pointer"]')
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
      expect(sidebar.root.querySelectorAll('[class*="cursor-pointer"]').length).toBe(3)
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
  })
})
