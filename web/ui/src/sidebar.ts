import type { ArtifactListItem, SessionListItem } from './types'
import { HLJS_THEMES, getSavedHljsTheme, saveHljsTheme, applyHljsTheme } from './hljs_themes'
import { createOutsideClick, bindLongPress } from './utils'
import { createElement, createNode } from './dom'
import { renderIcon } from './icons'
import { createIconButton } from './buttons'

export interface SidebarHandles {
  root: HTMLElement
  overlay: HTMLElement
  open: () => void
  close: () => void
  closeImmediate: () => void
  toggle: () => void
  setStreaming: (s: boolean) => void
  setSessions: (sessions: SessionListItem[], currentID: string) => void
  onSessionClick: (handler: (id: string) => void) => void
  onNewSession: (handler: () => void) => void
  onRename: (handler: (id: string, title: string) => void) => void
  setArtifacts: (items: ArtifactListItem[]) => void
  onArtifactClick: (handler: (name: string) => void) => void
  onOpen: (handler: () => void) => void
}

function formatRelativeTime(unixMs: number): string {
  const diffMs = Date.now() - unixMs
  if (diffMs < 0) return 'now'
  const sec = Math.floor(diffMs / 1000)
  if (sec < 60) return sec + 's'
  const min = Math.floor(sec / 60)
  if (min < 60) return min + 'm'
  const hr = Math.floor(min / 60)
  if (hr < 24) return hr + 'h'
  const day = Math.floor(hr / 24)
  if (day < 30) return day + 'd'
  const mon = Math.floor(day / 30)
  if (mon < 12) return mon + 'mo'
  return Math.floor(mon / 12) + 'y'
}

// Builtin games render as a persistent group above Artifacts: they launch the
// embedded game pages, so their availability has nothing to do with the
// artifacts directory listing.
// Sidebar icon is the board's red general piece scaled 0.5 (24 viewBox vs
// the 48 cell): same radii, stroke widths and text baseline ratios as the
// piece drawn in games/chess.html renderBoard.
const XHQ_PIECE = `<svg viewBox="0 0 24 24" width="16" height="16" aria-hidden="true"><circle cx="12" cy="12" r="10.5" fill="#D54F4F" stroke="#A03030" stroke-width="0.75"/><circle cx="12" cy="12" r="8.75" fill="none" stroke="#FFFFFF" stroke-width="0.5" opacity="0.3"/><text x="12" y="15.5" text-anchor="middle" font-size="10.5" font-weight="700" fill="#FFF5F2" font-family='"Kaiti SC","STKaiti","KaiTi",serif'>帅</text></svg>`

export const BUILTIN_GAMES = [{ id: 'chess', label: 'Chinese Chess', icon: XHQ_PIECE }] as const

export function createSidebar(opts: { mainContent: HTMLElement }): SidebarHandles {
  const { mainContent } = opts

  const root = createNode('div', {
    className:
      'sidebar-safe-top fixed top-0 left-0 h-full w-72 z-50 glass-solid border-r border-hairline transition-transform duration-300 ease-out',
    style: { transform: 'translateX(-100%)' },
  })

  const listContainer = createNode('div', {
    className: 'sidebar-list-height px-2 pt-4 overflow-y-auto',
  })
  root.appendChild(listContainer)
  // "Sessions" header row: the section title Games/Artifacts already have,
  // with the new-session action at its right end — the floating FAB used to
  // sit here and covered list rows on long session lists.
  const sessionsHeader = createNode('div', {
    className: 'flex items-center justify-between pl-3 pr-2 pt-4 pb-1',
  })
  sessionsHeader.setAttribute('data-sessions-header', '')
  const sessionsTitle = createElement('span', 'text-[11px] text-t3 font-medium')
  sessionsTitle.textContent = 'Sessions'
  const newSessionBtn = createIconButton({
    icon: 'plus',
    label: 'New session',
    variant: 'ghost',
    size: 'sm',
    iconSize: 16,
  })
  newSessionBtn.setAttribute('data-new-session', '')
  sessionsHeader.append(sessionsTitle, newSessionBtn)
  listContainer.appendChild(sessionsHeader)
  const sessionsList = createElement('div', '')
  listContainer.appendChild(sessionsList)
  const gamesSection = createElement('div', 'sidebar-games')
  const gamesHeader = createElement('div', 'text-[11px] text-t3 px-3 pt-4 pb-1 font-medium')
  gamesHeader.setAttribute('data-games-header', '')
  gamesHeader.textContent = 'Games'
  const gamesList = createElement('div', '')
  for (const g of BUILTIN_GAMES) {
    const row = createElement(
      'div',
      'artifact-row flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
    )
    row.setAttribute('data-game-row', g.id)
    const iconSpan = createElement('span', 'text-[16px] shrink-0 leading-none flex items-center')
    if (g.icon.startsWith('<svg')) iconSpan.innerHTML = g.icon
    else iconSpan.textContent = g.icon
    const nameSpan = createElement('span', 'text-[13px] block truncate')
    nameSpan.textContent = g.label
    row.append(iconSpan, nameSpan)
    row.addEventListener('click', () => {
      handlers.artifactClick(g.id)
      closeImmediate()
    })
    gamesList.appendChild(row)
  }
  gamesSection.append(gamesHeader, gamesList)
  listContainer.appendChild(gamesSection)
  // Artifacts is a read-only view of the projectspace artifacts directory —
  // lifecycle (create/delete) stays with the conversation, so rows only open.
  const artifactsSection = createElement('div', 'sidebar-artifacts')
  const artifactsHeader = createElement('div', 'text-[11px] text-t3 px-3 pt-4 pb-1 font-medium')
  artifactsHeader.textContent = 'Artifacts'
  const artifactsList = createElement('div', '')
  artifactsSection.append(artifactsHeader, artifactsList)
  listContainer.appendChild(artifactsSection)

  const THEME_CYCLE = ['dark', 'light', 'system'] as const
  type Theme = typeof THEME_CYCLE[number]

  // The Android WebView's prefers-color-scheme query is unreliable — it
  // reports light while the system is dark, and never updates on system
  // theme switches. The Kotlin host pushes the REAL system theme through
  // __gbotApplySystemTheme; remember it and resolve 'system' from that,
  // falling back to matchMedia only where no push ever arrived (desktop).
  let lastNativeIsLight: boolean | null = null

  const resolveTheme = (pref: Theme): 'dark' | 'light' => {
    if (pref === 'system') {
      if (lastNativeIsLight !== null) return lastNativeIsLight ? 'light' : 'dark'
      return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    }
    return pref
  }

  // Report the effective theme to the Android host (GBotNative JS interface
  // registered by ChatFragment) so the status-bar icon color matches the
  // header background. Optional-chained: no-op on desktop / server WUI.
  const notifyNativeTheme = (resolved: 'dark' | 'light') => {
    const native = (window as unknown as {
      GBotNative?: { onThemeChanged?: (isDark: boolean) => void }
    }).GBotNative
    native?.onThemeChanged?.(resolved === 'dark')
  }

  const savedPref = (localStorage.getItem('gbot-theme') || 'dark') as Theme
  const effectiveTheme = resolveTheme(savedPref)
  document.documentElement.dataset.theme = effectiveTheme
  notifyNativeTheme(effectiveTheme)

  const renderThemeIcon = (pref: Theme): SVGElement =>
    renderIcon(pref === 'dark' ? 'moon' : pref === 'light' ? 'sun' : 'tai-chi', { size: 18 })

  // themeToggle: variant=ghost (text-t2 hover:text-t1) layers hover + transition
  // on top of the previous static look — slight UX upgrade accepted in D5.
  // Long-press → openHljsPopover, single click → cycleTheme: the factory's
  // internal consumeTrigger swallows the synthesized post-long-press click,
  // so we no longer need a sidebar-side themeLP binding.
  // cycleTheme must be declared before themeToggle's createIconButton call,
  // but it references themeToggle via closure — fine because cycleTheme only
  // runs after createIconButton returns.
  const cycleTheme = () => {
    const current = (localStorage.getItem('gbot-theme') || 'dark') as Theme
    const idx = THEME_CYCLE.indexOf(current)
    const next = THEME_CYCLE[(idx + 1) % THEME_CYCLE.length]
    localStorage.setItem('gbot-theme', next)
    const resolved = resolveTheme(next)
    document.documentElement.dataset.theme = resolved
    themeToggle.replaceChildren(renderThemeIcon(next))
    applyHljsTheme(getSavedHljsTheme(), resolved === 'dark')
    notifyNativeTheme(resolved)
  }
  const themeToggle = createIconButton({
    icon: savedPref === 'dark' ? 'moon' : savedPref === 'light' ? 'sun' : 'tai-chi',
    label: 'Theme',
    variant: 'ghost',
    size: 'md',
    iconSize: 18,
    className: 'absolute bottom-5 left-5',
    onClick: cycleTheme,
    onLongPress: openHljsPopover,
  })

  const mediaQuery = window.matchMedia('(prefers-color-scheme: light)')
  const onSystemChange = () => {
    const pref = localStorage.getItem('gbot-theme') as Theme || 'dark'
    if (pref === 'system') {
      const resolved = resolveTheme('system')
      document.documentElement.dataset.theme = resolved
      applyHljsTheme(getSavedHljsTheme(), resolved === 'dark')
      notifyNativeTheme(resolved)
    }
  }
  mediaQuery.addEventListener('change', onSystemChange)

  // Android WebView does not fire matchMedia change events when the system
  // theme switches. Expose a hook so the Kotlin side can call it from
  // onConfigurationChanged via evaluateJavascript. The pushed value is
  // ALWAYS remembered (even when the current pref is explicit dark/light)
  // so a later cycle to 'system' resolves from the native truth.
  const apply = (isLight: boolean) => {
    lastNativeIsLight = isLight
    const pref = (localStorage.getItem('gbot-theme') as Theme) || 'dark'
    if (pref !== 'system') return
    const resolved = isLight ? 'light' : 'dark'
    document.documentElement.dataset.theme = resolved
    applyHljsTheme(getSavedHljsTheme(), !isLight)
    notifyNativeTheme(resolved)
  }
  Object.assign(window, { __gbotApplySystemTheme: apply })

  // Apply saved hljs theme on load
  applyHljsTheme(getSavedHljsTheme(), effectiveTheme === 'dark')

  function openHljsPopover() {
    // Remove existing popover if open
    const existing = document.getElementById('hljs-popover')
    if (existing) { existing.remove(); return }

    const currentHljs = getSavedHljsTheme()
    const currentTheme = (localStorage.getItem('gbot-theme') || 'dark') as Theme
    const isDark = resolveTheme(currentTheme) === 'dark'

    const popover = createNode('div', {
      className: 'fixed z-50 glass-solid border border-hairline rounded-xl p-2 shadow-2xl modal-enter',
      props: { id: 'hljs-popover' },
      style: { bottom: '60px', left: '20px', minWidth: '180px' },
    })

    const title = createElement('div', 'text-[11px] text-t3 px-2 py-1 font-medium')
    title.textContent = 'Highlight Theme'
    popover.appendChild(title)

    for (const theme of HLJS_THEMES) {
      const isSelected = theme.key === currentHljs
      const row = createElement(
        'div',
        'flex items-center gap-2 px-2 py-1.5 rounded-lg cursor-pointer text-[13px] ' +
          (isSelected
            ? 'bg-blue/15 border border-blue/20 text-blue font-medium'
            : 'hover:bg-ink3/50 text-t2'),
      )
      const label = createElement('span', 'flex-1')
      label.textContent = theme.label
      row.appendChild(label)
      if (isSelected) {
        const check = createElement('span', 'text-blue text-[13px]')
        check.textContent = '✓'
        row.appendChild(check)
      }
      row.addEventListener('click', () => {
        saveHljsTheme(theme.key)
        applyHljsTheme(theme.key, isDark)
        popover.remove()
      })
      popover.appendChild(row)
    }

    document.body.appendChild(popover)
    const oc = createOutsideClick(themeToggle, popover, () => {
      popover.remove()
      oc.remove()
    })
    oc.add()
  }
  root.appendChild(themeToggle)

  const overlay = createNode('div', {
    className: 'fixed inset-0 z-40 bg-black/30 transition-opacity duration-300',
    style: { display: 'none' },
  })

  const handlers = {
    sessionClick: (_id: string) => {},
    newSession: () => {},
    rename: (_id: string, _title: string) => {},
    artifactClick: (_name: string) => {},
    open: () => {},
  }

  const isOpen = () => root.style.transform === 'translateX(0px)'

  const openFn = () => {
    root.style.transform = 'translateX(0px)'
    mainContent.style.transform = 'translateX(288px)'
    overlay.style.display = ''
    handlers.open()
  }
  const closeFn = () => {
    root.style.transform = 'translateX(-100%)'
    mainContent.style.transform = 'translateX(0px)'
    overlay.style.display = 'none'
  }
  const closeImmediate = () => {
    root.style.transition = 'none'
    mainContent.style.transition = 'none'
    closeFn()
    requestAnimationFrame(() => {
      root.style.transition = ''
      mainContent.style.transition = ''
    })
  }
  const toggleFn = () => {
    if (isOpen()) closeFn()
    else openFn()
  }

  overlay.addEventListener('click', closeFn)

  // Busy state disables session switching: the backend rejects it anyway
  // (swapping sessions mid-query loses the running query's output), so
  // graying the controls keeps the UI honest about that.
  let busy = false
  const setStreaming = (s: boolean) => {
    busy = s
    newSessionBtn.classList.toggle('opacity-40', s)
    newSessionBtn.classList.toggle('pointer-events-none', s)
    for (const row of sessionsList.querySelectorAll('[data-session-row]')) {
      row.classList.toggle('opacity-40', s)
      row.classList.toggle('pointer-events-none', s)
    }
  }

  newSessionBtn.addEventListener('click', () => {
    if (busy) return
    handlers.newSession()
    closeImmediate()
  })

  const setSessions = (sessions: SessionListItem[], currentID: string) => {
    sessionsList.innerHTML = ''
    for (const s of sessions) {
      const isCurrent = s.id === currentID
      const row = createElement(
        'div',
        'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer ' +
          (isCurrent
            ? 'bg-blue/15 border border-blue/20 text-blue font-medium'
            : 'hover:bg-ink3/30 text-t2'),
      )
      row.setAttribute('data-session-row', '')

      const titleSpan = createElement('span', 'text-[13px] truncate flex-1')
      titleSpan.textContent = s.title || s.id.slice(0, 8)

      const timeSpan = createElement('span', 'text-[10px] text-t3 shrink-0')
      timeSpan.textContent = formatRelativeTime(s.updatedAt)

      row.appendChild(titleSpan)
      row.appendChild(timeSpan)

      // Long-press rename. Touch-only: session rows have no mouse hover affordance.
      const rowLP = bindLongPress(row, () => startRename(row, titleSpan, s))
      row.addEventListener('click', () => {
        if (rowLP.consumeTrigger()) return
        handlers.sessionClick(s.id)
        closeImmediate()
      })

      sessionsList.appendChild(row)
    }
    // Row rebuild drops the busy classes — reapply so a rename-triggered
    // list refresh mid-query can't re-enable clickable-looking rows.
    if (busy) setStreaming(true)
  }

  const setArtifacts = (items: ArtifactListItem[]) => {
    artifactsList.innerHTML = ''
    if (items.length === 0) {
      const empty = createElement('div', 'px-3 py-2 text-[12px] text-t3')
      empty.textContent = 'No artifacts yet'
      artifactsList.appendChild(empty)
      return
    }
    for (const a of items) {
      const row = createElement(
        'div',
        'artifact-row flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer hover:bg-ink3/30 text-t2',
      )
      const nameSpan = createElement('span', 'text-[13px] truncate flex-1')
      nameSpan.textContent = a.name
      const timeSpan = createElement('span', 'text-[10px] text-t3 shrink-0')
      timeSpan.textContent = formatRelativeTime(a.mtime)
      row.append(nameSpan, timeSpan)
      row.addEventListener('click', () => {
        handlers.artifactClick(a.name)
        closeImmediate()
      })
      artifactsList.appendChild(row)
    }
  }

  const startRename = (
    row: HTMLElement,
    titleSpan: HTMLElement,
    session: SessionListItem,
  ) => {
    const originalText = titleSpan.textContent || ''
    const input = createNode('input', {
      className:
        'text-[13px] flex-1 bg-ink2 border border-blue/30 rounded px-1 py-0.5 text-t1 outline-none',
      props: { type: 'text', value: originalText },
    })
    titleSpan.replaceWith(input)
    input.focus()
    input.select()

    const commit = () => {
      const newTitle = input.value.trim()
      if (newTitle && newTitle !== originalText) {
        titleSpan.textContent = newTitle
        handlers.rename(session.id, newTitle)
      } else {
        titleSpan.textContent = originalText
      }
      input.replaceWith(titleSpan)
    }

    input.addEventListener('blur', commit)
    input.addEventListener('keydown', (e) => {
      if (e.key === 'Enter') {
        e.preventDefault()
        commit()
      } else if (e.key === 'Escape') {
        titleSpan.textContent = originalText
        input.replaceWith(titleSpan)
      }
    })
  }

  return {
    root,
    overlay,
    open: openFn,
    close: closeFn,
    closeImmediate,
    toggle: toggleFn,
    setStreaming,
    setSessions,
    setArtifacts,
    onSessionClick: (handler) => {
      handlers.sessionClick = handler
    },
    onNewSession: (handler) => {
      handlers.newSession = handler
    },
    onRename: (handler) => {
      handlers.rename = handler
    },
    onArtifactClick: (handler) => {
      handlers.artifactClick = handler
    },
    onOpen: (handler) => {
      handlers.open = handler
    },
  }
}
