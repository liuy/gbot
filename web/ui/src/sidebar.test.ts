import { describe, it, expect, vi } from 'vitest'
import { createSidebar, artifactIconFor } from './sidebar'
import { renderIcon, type IconName } from './icons'
import type { ArtifactListItem, SessionListItem } from './types'
import type { RemoteDevice } from './vnc'

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

    it('setArtifacts renders tree rows: file, dir with newest time, nested short name', () => {
      const { sidebar } = setup()
      sidebar.setArtifacts(artifacts)
      const section = sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement
      expect(section).not.toBeNull()
      const rows = section.querySelectorAll('.artifact-row')
      // game.html (file) + demos/ (dir) + report.pdf (nested file)
      expect(rows.length).toBe(3)
      expect(rows[0].textContent).toContain('game.html')
      expect(rows[0].textContent).toContain('1m')
      expect(rows[1].getAttribute('data-artifact-dir')).toBe('demos')
      expect(rows[1].textContent).toContain('demos/')
      // Dir time = newest child's mtime (report.pdf's 1h).
      expect(rows[1].textContent).toContain('1h')
      expect(rows[2].textContent).toContain('report.pdf')
      expect(rows[2].textContent).not.toContain('demos/report.pdf')
      // Exact text catches a prefixLen off-by-one (a leading "/" would
      // still satisfy the toContain pair above).
      expect(rows[2].querySelector('.truncate')!.textContent).toBe('report.pdf')
    })

    it('clicking a nested file row calls onArtifactClick with the full path and closes', () => {
      const { mainContent, sidebar } = setup()
      const handler = vi.fn()
      sidebar.onArtifactClick(handler)
      sidebar.open()
      sidebar.setArtifacts(artifacts)

      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(3)
      ;(rows[1] as HTMLElement).click()
      ;(rows[2] as HTMLElement).click()
      expect(handler).toHaveBeenCalledTimes(1)
      expect(handler).toHaveBeenCalledWith('demos/report.pdf')
      expect(sidebar.root.style.transform).toBe('translateX(-100%)')
      expect(mainContent.style.transform).toBe('translateX(0px)')
    })

    it('nested path video/refs/x.png renders dir video → dir refs → short-named file', () => {
      const { sidebar } = setup()
      sidebar.setArtifacts([
        { name: 'video/refs/x.png', size: 10, mtime: Date.now() - 60_000 },
      ])
      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(3)
      expect(rows[0].getAttribute('data-artifact-dir')).toBe('video')
      expect(rows[0].textContent).toContain('video/')
      expect(rows[1].getAttribute('data-artifact-dir')).toBe('refs')
      expect(rows[1].textContent).toContain('refs/')
      expect(rows[2].hasAttribute('data-artifact-dir')).toBe(false)
      expect(rows[2].textContent).toContain('x.png')
      expect(rows[2].querySelector('.truncate')!.textContent).toBe('x.png')
      expect(rows[2].textContent).not.toContain('video/refs/x.png')
    })

    it('dir row time is the newest child\'s, not the oldest\'s', () => {
      const { sidebar } = setup()
      const now = Date.now()
      sidebar.setArtifacts([
        { name: 'video/new.mp4', size: 1, mtime: now - 30_000 },
        { name: 'video/old.png', size: 1, mtime: now - 3 * 24 * 3_600_000 },
      ])
      const dirRow = sidebar.root.querySelector(
        '.sidebar-artifacts [data-artifact-dir="video"]',
      ) as HTMLElement
      expect(dirRow.textContent).toContain('30s')
      expect(dirRow.textContent).not.toContain('3d')
    })

    it('dir rows interleave with top-level files at their newest child\'s position', () => {
      const { sidebar } = setup()
      const now = Date.now()
      sidebar.setArtifacts([
        { name: 'top.md', size: 1, mtime: now - 10_000 },
        { name: 'video/a.mp4', size: 1, mtime: now - 60_000 },
      ])
      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(3)
      expect(rows[0].textContent).toContain('top.md')
      expect(rows[0].hasAttribute('data-artifact-dir')).toBe(false)
      expect(rows[1].getAttribute('data-artifact-dir')).toBe('video')

      // Reversed mtimes: the dir row leads because its newest child is newer
      // (its children then sit between the dir row and the next top-level
      // file — expansion is in place).
      sidebar.setArtifacts([
        { name: 'video/a.mp4', size: 1, mtime: now - 10_000 },
        { name: 'top.md', size: 1, mtime: now - 60_000 },
      ])
      const rerendered = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rerendered.length).toBe(3)
      expect(rerendered[0].getAttribute('data-artifact-dir')).toBe('video')
      expect(rerendered[1].textContent).toContain('a.mp4')
      expect(rerendered[2].textContent).toContain('top.md')
    })

    it('file rows map extensions to type icons', () => {
      const { sidebar } = setup()
      const now = Date.now()
      sidebar.setArtifacts([
        { name: 'clip.mp4', size: 1, mtime: now },
        { name: 'clip.mov', size: 1, mtime: now - 1000 },
        { name: 'page.html', size: 1, mtime: now - 2000 },
        { name: 'page.htm', size: 1, mtime: now - 3000 },
        { name: 'app.apk', size: 1, mtime: now - 4000 },
        { name: 'app.zip', size: 1, mtime: now - 5000 },
        { name: 'pic.png', size: 1, mtime: now - 6000 },
        { name: 'notes.txt', size: 1, mtime: now - 7000 },
      ])
      const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
      expect(rows.length).toBe(8)
      const expected: IconName[] = ['film', 'film', 'globe', 'globe', 'box', 'box', 'image', 'file']
      // outerHTML (not innerHTML): the size and className land on the svg
      // element itself, so innerHTML comparisons cannot see them.
      for (let i = 0; i < expected.length; i++) {
        const svg = rows[i].querySelector('svg') as SVGSVGElement
        expect(svg.outerHTML, `row ${i} (${(rows[i].textContent || '').trim()})`).toBe(
          renderIcon(expected[i], { size: 14, className: 'shrink-0 text-t3' }).outerHTML,
        )
      }
    })

    it('clicking a dir row toggles expansion and chevron without firing open', () => {
      const { sidebar } = setup()
      const handler = vi.fn()
      sidebar.onArtifactClick(handler)
      sidebar.setArtifacts([
        { name: 'video/a.mp4', size: 1, mtime: Date.now() },
        { name: 'video/refs/b.png', size: 1, mtime: Date.now() - 60_000 },
      ])
      const dirRow = sidebar.root.querySelector(
        '.sidebar-artifacts [data-artifact-dir="video"]',
      ) as HTMLElement
      const children = dirRow.nextElementSibling as HTMLElement
      const chev = dirRow.querySelector('svg') as SVGElement
      expect(children.classList.contains('hidden')).toBe(true)
      expect(chev.classList.contains('rotate-90')).toBe(false)

      dirRow.click()
      expect(children.classList.contains('hidden')).toBe(false)
      expect(chev.classList.contains('rotate-90')).toBe(true)
      expect(handler).not.toHaveBeenCalled()

      dirRow.click()
      expect(children.classList.contains('hidden')).toBe(true)
      expect(chev.classList.contains('rotate-90')).toBe(false)
      expect(handler).not.toHaveBeenCalled()
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
      // game.html + demos/ dir row + nested report.pdf
      expect(section?.querySelectorAll('.artifact-row').length).toBe(3)
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
      expect(rows.length).toBe(3)
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

describe('remote desktop section', () => {
  const DEVICES: RemoteDevice[] = [
    { name: 'win11', addr: 'ws://127.0.0.1:8006', pass: '' },
    { name: 'servere5', addr: 'http://127.0.0.1:5901', pass: 'pw' },
  ]

  it('hides the section when no desktops are configured and shows it again once one exists', () => {
    const { sidebar } = setup()
    const section = sidebar.root.querySelector('.sidebar-devices') as HTMLElement
    sidebar.setRemoteDevices([])
    expect(section.classList.contains('hidden')).toBe(true)
    sidebar.setRemoteDevices([DEVICES[0]])
    expect(section.classList.contains('hidden')).toBe(false)
    expect(sidebar.root.querySelectorAll('[data-device-row]').length).toBe(1)
  })

  it('setRemoteDevices renders one row per device, between Games and Artifacts', () => {
    const { sidebar } = setup()
    sidebar.setRemoteDevices(DEVICES)
    const rows = sidebar.root.querySelectorAll('[data-device-row]')
    expect(rows.length).toBe(2)
    expect((rows[0].lastElementChild as HTMLElement).textContent).toBe('win11')
    expect((rows[1].lastElementChild as HTMLElement).textContent).toBe('servere5')
    // Section order: Games < Remote Desktop < Artifacts inside the list
    // container (listContainer children, not document-wide queries).
    const list = sidebar.root.querySelector('.overflow-y-auto') as HTMLElement
    const kids = Array.from(list.children)
    const pos = (el: Element) => kids.indexOf(el)
    expect(pos(sidebar.root.querySelector('.sidebar-games') as Element)).toBeLessThan(
      pos(sidebar.root.querySelector('.sidebar-devices') as Element),
    )
    expect(pos(sidebar.root.querySelector('.sidebar-devices') as Element)).toBeLessThan(
      pos(sidebar.root.querySelector('.sidebar-artifacts') as Element),
    )
  })

  it('row click fires onDeviceClick with the exact device object and closes', () => {
    const { mainContent, sidebar } = setup()
    const handler = vi.fn()
    sidebar.onDeviceClick(handler)
    sidebar.open()
    sidebar.setRemoteDevices(DEVICES)
    ;(sidebar.root.querySelector('[data-device-row="servere5"]') as HTMLElement).click()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(DEVICES[1])
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
  })

  it('zero devices render the header with no rows', () => {
    const { sidebar } = setup()
    sidebar.setRemoteDevices([])
    expect(sidebar.root.querySelector('[data-devices-header]')).not.toBeNull()
    expect(sidebar.root.querySelectorAll('[data-device-row]').length).toBe(0)
  })
})

describe('sidebar plugins section', () => {
  const CARDS = [
    { name: 'browser', ui: '/plugins/browser/ui', running: true },
    { name: 'camera', ui: '/plugins/camera/ui', running: false },
  ]

  it('empty card list hides the whole section', () => {
    const { sidebar } = setup()
    sidebar.setPlugins([])
    expect(sidebar.root.querySelector('.sidebar-plugins')!.classList.contains('hidden')).toBe(true)
  })

  it('online rows are clickable; standby rows are grayed and inert', () => {
    const { mainContent, sidebar } = setup()
    const handler = vi.fn()
    sidebar.onPluginClick(handler)
    sidebar.open()
    sidebar.setPlugins(CARDS)
    const rows = sidebar.root.querySelectorAll('[data-plugin-row]')
    expect(rows).toHaveLength(2)
    const online = rows[0] as HTMLElement
    const standby = rows[1] as HTMLElement
    expect(online.hasAttribute('data-plugin-standby')).toBe(false)
    expect(standby.hasAttribute('data-plugin-standby')).toBe(true)
    // The standby row must not carry the pointer affordance of live rows.
    expect(online.className).toContain('cursor-pointer')
    expect(standby.className).not.toContain('cursor-pointer')
    standby.click()
    expect(handler).not.toHaveBeenCalled()
    online.click()
    expect(handler).toHaveBeenCalledTimes(1)
    expect(handler).toHaveBeenCalledWith(CARDS[0])
    expect(mainContent.style.transform).toBe('translateX(0px)')
  })

  it('re-setting the list replaces rows (running flips re-enable a row)', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onPluginClick(handler)
    sidebar.setPlugins(CARDS)
    sidebar.setPlugins([{ name: 'camera', ui: '/plugins/camera/ui', running: true }])
    const rows = sidebar.root.querySelectorAll('[data-plugin-row]')
    expect(rows).toHaveLength(1)
    expect((rows[0] as HTMLElement).hasAttribute('data-plugin-standby')).toBe(false)
    ;(rows[0] as HTMLElement).click()
    expect(handler).toHaveBeenCalledTimes(1)
  })
})

describe('artifactIconFor anchors', () => {
  it.each([
    ['clip.mp4', 'film'],
    ['a.mp4x.md', 'file'],
    ['pic.png', 'image'],
    ['a.png.txt', 'file'],
    ['photo.jpg', 'image'],
    ['pack.zip', 'box'],
    ['a.zip.txt', 'file'],
    ['page.html', 'globe'],
    ['a.htmlx.txt', 'file'],
  ])('%s maps to %s', (name, want) => {
    expect(artifactIconFor(name)).toBe(want)
  })
})

describe('formatRelativeTime boundaries (via session rows)', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it.each([
    ['future 5s', -5000, 'now'],
    ['exactly now', 0, '0s'],
    ['2h', 2 * 3600_000, '2h'],
    ['exactly 24h', 24 * 3600_000, '1d'],
    ['25h', 25 * 3600_000, '1d'],
    ['29d', 29 * 24 * 3600_000, '29d'],
    ['exactly 30d', 30 * 24 * 3600_000, '1mo'],
    ['40d', 40 * 24 * 3600_000, '1mo'],
    ['330d', 330 * 24 * 3600_000, '11mo'],
    ['exactly 360d', 360 * 24 * 3600_000, '1y'],
    ['400d', 400 * 24 * 3600_000, '1y'],
  ])('%s renders %s', (_label, agoMs, want) => {
    vi.useFakeTimers()
    const now = new Date(2026, 10, 4, 12, 0).getTime()
    vi.setSystemTime(now)
    const { sidebar } = setup()
    sidebar.setSessions([{ id: 's1', title: 'T', updatedAt: now - agoMs }], '')
    const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
    expect(row.children[1].textContent).toBe(want)
  })

  it('row spans carry their exact classes and the time span is appended', () => {
    const { sidebar } = setup()
    sidebar.setSessions(
      [
        { id: 's1', title: 'First', updatedAt: Date.now() - 3600_000 },
        { id: 's2', title: 'Second', updatedAt: Date.now() - 60_000 },
      ],
      's1',
    )
    const rows = sidebar.root.querySelectorAll('[data-session-row]')
    expect((rows[0].children[0] as HTMLElement).className).toBe('text-[13px] truncate flex-1')
    expect((rows[0].children[1] as HTMLElement).className).toBe('text-[10px] text-t3 shrink-0')
    expect(rows[0].getAttribute('data-session-row')).toBe('')
    expect(rows[1].className).toBe(
      'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
    )
  })

  it('empty-title sessions fall back to the id prefix', () => {
    const { sidebar } = setup()
    sidebar.setSessions([{ id: 'abcdefghijklmnop', title: '', updatedAt: Date.now() }], '')
    const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
    expect((row.children[0] as HTMLElement).textContent).toBe('abcdefgh')
  })

  it('a rebuild leaves no stale text node behind', () => {
    const { sidebar } = setup()
    const list = sidebar.root.querySelector('[data-sessions-header]')!.nextElementSibling as HTMLElement
    sidebar.setSessions([{ id: 's1', title: 'A', updatedAt: Date.now() }], '')
    sidebar.setSessions([{ id: 's2', title: 'B', updatedAt: Date.now() }], '')
    expect(list.childNodes.length).toBe(1)
  })

  it('a long-press trigger suppresses the following click', () => {
    vi.useFakeTimers()
    try {
      const { sidebar } = setup()
      const handler = vi.fn()
      sidebar.onSessionClick(handler)
      sidebar.setSessions([{ id: 's1', title: 'T', updatedAt: Date.now() }], '')
      const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
      row.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
      vi.advanceTimersByTime(600)
      row.click()
      expect(handler).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('artifact tree structure pins', () => {
  it('nested rows indent one step per depth with the pinned pixel ladder', () => {
    const { sidebar } = setup()
    sidebar.setArtifacts([{ name: 'video/refs/x.png', size: 10, mtime: Date.now() }])
    const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
    expect((rows[0] as HTMLElement).style.paddingLeft).toBe('')
    expect((rows[1] as HTMLElement).style.paddingLeft).toBe('26px')
    expect((rows[2] as HTMLElement).style.paddingLeft).toBe('44px')
    expect(rows[0].className).toBe(
      'artifact-row flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t1',
    )
    expect(rows[2].className).toBe(
      'artifact-row flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
    )
  })

  it('a depth-2 directory row indents at the 44px step', () => {
    const { sidebar } = setup()
    sidebar.setArtifacts([{ name: 'a/b/c/d.png', size: 1, mtime: Date.now() }])
    const rows = sidebar.root.querySelectorAll('.sidebar-artifacts .artifact-row')
    expect(rows[0].getAttribute('data-artifact-dir')).toBe('a')
    expect((rows[1] as HTMLElement).style.paddingLeft).toBe('26px')
    expect(rows[1].getAttribute('data-artifact-dir')).toBe('b')
    expect((rows[2] as HTMLElement).style.paddingLeft).toBe('44px')
    expect(rows[2].getAttribute('data-artifact-dir')).toBe('c')
  })

  it('dir rows carry the chevron icon and span classes exactly', () => {
    const { sidebar } = setup()
    sidebar.setArtifacts([{ name: 'video/a.mp4', size: 1, mtime: Date.now() }])
    const dirRow = sidebar.root.querySelector('[data-artifact-dir="video"]') as HTMLElement
    expect(dirRow.querySelector('svg')!.outerHTML).toBe(
      renderIcon('chevron-right', {
        size: 14,
        className: 'shrink-0 text-t3 transition-transform duration-200',
      }).outerHTML,
    )
    expect((dirRow.children[1] as HTMLElement).className).toBe('text-[13px] truncate flex-1')
    expect((dirRow.children[2] as HTMLElement).className).toBe('text-[10px] text-t3 shrink-0')
    expect(dirRow.parentElement!.className).toBe('')
  })

  it('file row spans carry their exact classes', () => {
    const { sidebar } = setup()
    sidebar.setArtifacts([{ name: 'game.html', size: 1, mtime: Date.now() }])
    const fileRow = sidebar.root.querySelector('.sidebar-artifacts .artifact-row') as HTMLElement
    expect((fileRow.children[1] as HTMLElement).className).toBe('text-[13px] truncate flex-1')
    expect((fileRow.children[2] as HTMLElement).className).toBe('text-[10px] text-t3 shrink-0')
    // Top-level file rows carry no indentation.
    expect(fileRow.style.paddingLeft).toBe('')
  })

  it('a rebuild leaves no stale text node and the empty state keeps its class', () => {
    const { sidebar } = setup()
    sidebar.setArtifacts([{ name: 'game.html', size: 1, mtime: Date.now() }])
    sidebar.setArtifacts([{ name: 'a.html', size: 1, mtime: Date.now() }])
    const list = sidebar.root.querySelector('.sidebar-artifacts')!.lastElementChild as HTMLElement
    expect(list.childNodes.length).toBe(1)
    sidebar.setArtifacts([])
    expect((list.children[0] as HTMLElement).className).toBe('px-3 py-2 text-[12px] text-t3')
  })
})

describe('clear-artifacts confirm branch', () => {
  it('confirm resets armed state: a third tap re-arms instead of re-firing', async () => {
    const fetchMock = vi.fn(async () => ({ ok: true }))
    vi.stubGlobal('fetch', fetchMock)
    try {
      const { sidebar } = setup()
      const btn = sidebar.root.querySelector('[data-clear-artifacts]') as HTMLElement
      btn.click()
      btn.click()
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(btn.style.color).toBe('')
      expect(btn.getAttribute('aria-pressed')).toBe('false')
      btn.click()
      expect(fetchMock).toHaveBeenCalledTimes(1)
      expect(btn.style.color).toBe('rgb(248, 81, 73)')
      expect(btn.getAttribute('aria-pressed')).toBe('true')
    } finally {
      vi.unstubAllGlobals()
    }
  })

  it('a confirm clears the arm timer so a re-arm survives the old deadline', () => {
    vi.useFakeTimers()
    try {
      const fetchMock = vi.fn(async () => ({ ok: true }))
      vi.stubGlobal('fetch', fetchMock)
      const { sidebar } = setup()
      const btn = sidebar.root.querySelector('[data-clear-artifacts]') as HTMLElement
      btn.click()
      vi.advanceTimersByTime(2000)
      btn.click()
      btn.click()
      // t=5000 is the FIRST arm's deadline: the confirm at t=2000 must have
      // cleared it, so the re-arm (deadline t=7000) is still armed here.
      vi.advanceTimersByTime(3000)
      expect(btn.style.color).toBe('rgb(248, 81, 73)')
      expect(btn.getAttribute('aria-pressed')).toBe('true')
    } finally {
      vi.useRealTimers()
      vi.unstubAllGlobals()
    }
  })
})

describe('device and plugin row structure pins', () => {
  it('device rows keep their class and name span, and rebuilds leave no stale node', () => {
    const { sidebar } = setup()
    const devices: RemoteDevice[] = [{ name: 'win11', addr: 'ws://127.0.0.1:8006', pass: '' }]
    sidebar.setRemoteDevices(devices)
    sidebar.setRemoteDevices(devices)
    const list = sidebar.root.querySelector('.sidebar-devices')!.lastElementChild as HTMLElement
    expect(list.childNodes.length).toBe(1)
    const row = list.children[0] as HTMLElement
    expect(row.className).toBe(
      'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
    )
    expect((row.children[0] as HTMLElement).className).toBe('text-[13px] truncate flex-1')
  })

  it('non-empty plugin list shows the section and rows carry name text and classes', () => {
    const { sidebar } = setup()
    const cards = [
      { name: 'browser', ui: '/plugins/browser/ui', running: true },
      { name: 'camera', ui: '/plugins/camera/ui', running: false },
    ]
    sidebar.setPlugins(cards)
    sidebar.setPlugins(cards)
    const section = sidebar.root.querySelector('.sidebar-plugins') as HTMLElement
    expect(section.classList.contains('hidden')).toBe(false)
    const list = section.lastElementChild as HTMLElement
    expect(list.childNodes.length).toBe(2)
    const online = list.children[0] as HTMLElement
    const standby = list.children[1] as HTMLElement
    expect(online.textContent).toBe('browser')
    expect(standby.textContent).toBe('camera')
    expect(online.className).toBe(
      'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
    )
    expect(standby.className).toBe('flex items-center gap-2 px-3 py-2 rounded-lg text-t3 opacity-50')
    expect(standby.getAttribute('data-plugin-standby')).toBe('')
    expect((online.children[0] as HTMLElement).className).toBe('text-[13px] truncate flex-1')
  })
})

describe('rename flow structure pins', () => {
  function startRename(sidebar: ReturnType<typeof createSidebar>) {
    document.body.appendChild(sidebar.root)
    sidebar.setSessions([{ id: 'x1', title: 'Original', updatedAt: Date.now() }], '')
    const row = sidebar.root.querySelector('[data-session-row]') as HTMLElement
    vi.useFakeTimers()
    row.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(600)
    return row.querySelector('input') as HTMLInputElement
  }

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
  })

  it('the edit input carries its class, type, focus and full selection', () => {
    const { sidebar } = setup()
    const input = startRename(sidebar)
    expect(input.className).toBe(
      'text-[13px] flex-1 bg-ink2 border border-blue/30 rounded px-1 py-0.5 text-t1 outline-none',
    )
    expect(input.getAttribute('type')).toBe('text')
    expect(document.activeElement).toBe(input)
    expect(input.selectionStart).toBe(0)
    expect(input.selectionEnd).toBe('Original'.length)
    vi.useRealTimers()
  })

  it('commit trims the typed title before handing it to the handler', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onRename(handler)
    const input = startRename(sidebar)
    input.value = '  Renamed  '
    input.dispatchEvent(new Event('blur'))
    vi.useRealTimers()
    expect(handler).toHaveBeenCalledWith('x1', 'Renamed')
  })

  it('empty and unchanged commits restore the original without calling the handler', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onRename(handler)
    const input = startRename(sidebar)
    input.value = ''
    input.dispatchEvent(new Event('blur'))
    const input2 = startRename(sidebar)
    input2.value = 'Original'
    input2.dispatchEvent(new Event('blur'))
    vi.useRealTimers()
    expect(handler).not.toHaveBeenCalled()
    const span = sidebar.root.querySelector('[data-session-row] span') as HTMLElement
    expect(span.textContent).toBe('Original')
  })

  it('non-Enter keys do not commit; Enter is default-prevented', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onRename(handler)
    const input = startRename(sidebar)
    input.value = 'Typed'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'x', bubbles: true, cancelable: true }))
    expect(sidebar.root.querySelector('[data-session-row] input')).not.toBeNull()
    const ev = new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true })
    input.dispatchEvent(ev)
    expect(ev.defaultPrevented).toBe(true)
    expect(handler).toHaveBeenCalledWith('x1', 'Typed')
    vi.useRealTimers()
  })

  it('Escape restores the original text and removes the input without committing', () => {
    const { sidebar } = setup()
    const handler = vi.fn()
    sidebar.onRename(handler)
    const input = startRename(sidebar)
    input.value = 'Discard me'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true }))
    expect(sidebar.root.querySelector('[data-session-row] input')).toBeNull()
    expect(handler).not.toHaveBeenCalled()
    const span = sidebar.root.querySelector('[data-session-row] span') as HTMLElement
    expect(span.textContent).toBe('Original')
    vi.useRealTimers()
  })
})

describe('sidebar section structure pins', () => {
  it('sessions header, list, and anchors carry their exact classes and values', () => {
    const { sidebar } = setup()
    const header = sidebar.root.querySelector('[data-sessions-header]') as HTMLElement
    expect(header.className).toBe('flex items-center justify-between pl-3 pr-2 pt-4 pb-1')
    expect(header.getAttribute('data-sessions-header')).toBe('')
    expect((header.querySelector('[data-new-session]') as HTMLElement).getAttribute('data-new-session')).toBe('')
    const list = header.nextElementSibling as HTMLElement
    expect(list.className).toBe('')
  })

  it('games and devices headers carry their exact classes, anchors, and lists', () => {
    const { sidebar } = setup()
    const gamesSection = sidebar.root.querySelector('.sidebar-games') as HTMLElement
    expect(gamesSection).not.toBeNull()
    expect(gamesSection.className).toBe('sidebar-games')
    const gamesHeader = sidebar.root.querySelector('[data-games-header]') as HTMLElement
    expect(gamesHeader.className).toBe('text-[11px] text-t3 px-3 pt-4 pb-1 font-medium')
    expect(gamesHeader.getAttribute('data-games-header')).toBe('')
    expect((gamesHeader.nextElementSibling as HTMLElement).className).toBe('')
    const devicesHeader = sidebar.root.querySelector('[data-devices-header]') as HTMLElement
    expect(devicesHeader.className).toBe('text-[11px] text-t3 px-3 pt-4 pb-1 font-medium')
    expect(devicesHeader.getAttribute('data-devices-header')).toBe('')
    expect((devicesHeader.nextElementSibling as HTMLElement).className).toBe('')
  })

  it('plugins and artifacts headers carry their exact classes, anchors, and lists', () => {
    const { sidebar } = setup()
    const pluginsHeader = sidebar.root.querySelector('[data-plugins-header]') as HTMLElement
    expect(pluginsHeader.className).toBe('text-[11px] text-t3 px-3 pt-4 pb-1 font-medium')
    expect(pluginsHeader.getAttribute('data-plugins-header')).toBe('')
    expect(pluginsHeader.getAttribute('data-i18n')).toBe('sidebarPlugins')
    expect((pluginsHeader.nextElementSibling as HTMLElement).className).toBe('')
    const artifactsTitle = sidebar.root.querySelector('.sidebar-artifacts > div > span') as HTMLElement
    expect(artifactsTitle.className).toBe('text-[11px] text-t3 font-medium')
    expect(artifactsTitle.textContent).toBe('Artifacts')
    const artifactsHeader = artifactsTitle.parentElement as HTMLElement
    expect(artifactsHeader.className).toBe('flex items-center justify-between pl-3 pr-2 pt-4 pb-1')
    const clearBtn = artifactsHeader.querySelector('[data-clear-artifacts]') as HTMLElement
    expect(clearBtn.className).toContain('p-1.5 -m-1.5')
    expect(clearBtn.getAttribute('data-clear-artifacts')).toBe('')
    expect((sidebar.root.querySelector('.sidebar-artifacts') as HTMLElement).lastElementChild!.className).toBe('')
  })

  it('settings button anchor value and overlay class are pinned', () => {
    const { sidebar } = setup()
    expect(
      (sidebar.root.querySelector('[data-settings-btn]') as HTMLElement).getAttribute('data-settings-btn'),
    ).toBe('')
    expect(sidebar.overlay.className).toBe('fixed inset-0 z-40 bg-black/30 transition-opacity duration-300')
  })

  it('overlay click closes the sidebar', () => {
    const { mainContent, sidebar } = setup()
    sidebar.open()
    sidebar.overlay.click()
    expect(sidebar.root.style.transform).toBe('translateX(-100%)')
    expect(mainContent.style.transform).toBe('translateX(0px)')
    expect(sidebar.overlay.style.display).toBe('none')
  })

  it('closeImmediate disables transitions for one frame, then restores them', () => {
    let raf: (() => void) | null = null
    const rafSpy = vi.fn((cb: () => void) => {
      raf = cb
      return 1
    })
    vi.stubGlobal('requestAnimationFrame', rafSpy)
    try {
      const { mainContent, sidebar } = setup()
      sidebar.open()
      sidebar.closeImmediate()
      expect(rafSpy).toHaveBeenCalledTimes(1)
      // The jump itself happens with transitions off…
      expect(sidebar.root.style.transition).toBe('none')
      expect(mainContent.style.transition).toBe('none')
      expect(sidebar.root.style.transform).toBe('translateX(-100%)')
      // …and the next frame re-enables them.
      raf!()
      expect(sidebar.root.style.transition).toBe('')
      expect(mainContent.style.transition).toBe('')
    } finally {
      vi.unstubAllGlobals()
    }
  })
})
