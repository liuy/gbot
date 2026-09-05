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
      // Session rows still render alongside. (Counted by row markers, not
      // the cursor-pointer class — artifact rows carry a ✕ delete span that
      // is cursor-pointer too.)
      expect(sidebar.root.querySelectorAll('[data-session-row]').length).toBe(1)
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

    it('trash button two-tap fires DELETE /api/artifacts and refreshes', async () => {
      const fetchMock = vi.fn(async () => ({ ok: true }))
      vi.stubGlobal('fetch', fetchMock)
      const { sidebar } = setup()
      const refresh = vi.fn()
      sidebar.onClearArtifacts(refresh)
      const btn = sidebar.root.querySelector('[data-clear-artifacts]') as HTMLElement
      expect(btn).not.toBeNull()

      // First tap only arms: red highlight, no request, no refresh.
      // Arming uses an inline color (a text-color utility would lose to
      // the button's text-t2 by stylesheet order).
      btn.click()
      expect(btn.style.color).toBe('rgb(248, 81, 73)')
      expect(btn.getAttribute('aria-pressed')).toBe('true')
      expect(fetchMock).not.toHaveBeenCalled()
      expect(refresh).not.toHaveBeenCalled()

      // Second tap inside the window executes the clear-all.
      btn.click()
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(fetchMock).toHaveBeenCalledWith('/api/artifacts', { method: 'DELETE' })
      await vi.waitFor(() => expect(refresh).toHaveBeenCalledTimes(1))
    })

    it('trash button disarms after 5s so a late tap does not clear', () => {
      vi.useFakeTimers()
      try {
        const fetchMock = vi.fn(async () => ({ ok: true }))
        vi.stubGlobal('fetch', fetchMock)
        const { sidebar } = setup()
        const btn = sidebar.root.querySelector('[data-clear-artifacts]') as HTMLElement
        btn.click()
        vi.advanceTimersByTime(5000)
        expect(btn.style.color).toBe('')
        expect(btn.getAttribute('aria-pressed')).toBe('false')
        // Disarmed: the next tap is a fresh arm, not a confirm.
        btn.click()
        expect(btn.style.color).toBe('rgb(248, 81, 73)')
        expect(fetchMock).not.toHaveBeenCalled()
      } finally {
        vi.useRealTimers()
        vi.unstubAllGlobals()
      }
    })

    it('artifact rows have no per-row delete control (clear-all only)', async () => {
      const { sidebar } = setup()
      sidebar.open()
      sidebar.setArtifacts(artifacts)
      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(2)
      for (const row of rows) {
        expect(row.textContent).not.toContain('✕')
      }
    })
  })

  describe('settings entry', () => {
    it('renders a settings icon button in the bottom row beside the theme toggle', () => {
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      const btn = sidebar.root.querySelector('[data-settings-btn]') as HTMLElement
      expect(btn).toBeTruthy()
      // Bottom-row placement: same bottom offset as the theme toggle
      // (absolute bottom-5 left-5); the gear sits 12px further right.
      expect(btn.className).toContain('bottom-5')
      // createIconButton surfaces its label as the button's aria-label.
      expect(sidebar.root.querySelector('button[aria-label="Settings"]')).toBe(btn)
    })
    it('clicking the settings button fires the onOpenSettings handler', () => {
      const { sidebar } = setup()
      document.body.appendChild(sidebar.root)
      const handler = vi.fn()
      sidebar.onOpenSettings(handler)
      ;(sidebar.root.querySelector('[data-settings-btn]') as HTMLElement).click()
      expect(handler).toHaveBeenCalledTimes(1)
    })
  })
})
