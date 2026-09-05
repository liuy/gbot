import type { ArtifactListItem, SessionListItem } from './types'
import { bindLongPress } from './utils'
import { createElement, createNode } from './dom'
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
  onClearArtifacts: (handler: () => void) => void
  onOpenSettings: (handler: () => void) => void
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
  // Artifacts lists the projectspace artifacts directory. Rows open the
  // artifact; the header trash button clears everything (two-tap confirm —
  // artifacts are regenerable by the agent, so bulk delete is recoverable).
  const artifactsSection = createElement('div', 'sidebar-artifacts')
  // Header row mirrors the Sessions header: title left, action button right.
  const artifactsHeader = createNode('div', {
    className: 'flex items-center justify-between pl-3 pr-2 pt-4 pb-1',
  })
  const artifactsTitle = createElement('span', 'text-[11px] text-t3 font-medium')
  artifactsTitle.textContent = 'Artifacts'
  const clearArtifactsBtn = createIconButton({
    icon: 'trash',
    label: 'Clear all artifacts',
    variant: 'ghost',
    size: 'auto',
    iconSize: 12,
    className: 'p-1.5 -m-1.5',
  })
  clearArtifactsBtn.setAttribute('data-clear-artifacts', '')
  // Two-tap confirm, no dialog: first tap arms (red) and a 2s timer disarms;
  // a second tap inside the window executes the clear-all.
  let clearArmed = false
  let clearArmTimer: ReturnType<typeof setTimeout> | undefined
  clearArtifactsBtn.addEventListener('click', () => {
    if (!clearArmed) {
      clearArmed = true
      clearArtifactsBtn.setAttribute('aria-pressed', 'true')
      // Inline style, not a utility class: conflicting text-color utilities
      // resolve by stylesheet order (text-t2 would win), inline always wins.
      clearArtifactsBtn.style.color = '#f85149'
      clearArmTimer = setTimeout(() => {
        clearArmed = false
        clearArtifactsBtn.style.color = ''
        clearArtifactsBtn.setAttribute('aria-pressed', 'false')
      }, 5000)
      return
    }
    clearTimeout(clearArmTimer)
    clearArmed = false
    clearArtifactsBtn.style.color = ''
    clearArtifactsBtn.setAttribute('aria-pressed', 'false')
    fetch('/api/artifacts', { method: 'DELETE' })
      .catch(() => {})
      .finally(() => handlers.clearArtifacts())
  })
  artifactsHeader.append(artifactsTitle, clearArtifactsBtn)
  const artifactsList = createElement('div', '')
  artifactsSection.append(artifactsHeader, artifactsList)
  listContainer.appendChild(artifactsSection)


  // Settings gear occupies the sidebar's bottom-left slot (the old theme
  // toggle's home — theming moved into the Settings page).
  const settingsBtn = createIconButton({
    icon: 'settings',
    label: 'Settings',
    variant: 'ghost',
    size: 'auto',
    iconSize: 18,
    className: 'absolute bottom-5 left-5',
  })
  settingsBtn.setAttribute('data-settings-btn', '')
  root.appendChild(settingsBtn)
  settingsBtn.addEventListener('click', () => {
    handlers.openSettings()
  })

  const overlay = createNode('div', {
    className: 'fixed inset-0 z-40 bg-black/30 transition-opacity duration-300',
    style: { display: 'none' },
  })

  const handlers = {
    sessionClick: (_id: string) => {},
    newSession: () => {},
    rename: (_id: string, _title: string) => {},
    artifactClick: (_name: string) => {},
    clearArtifacts: () => {},
    openSettings: () => {},
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
    onClearArtifacts: (handler) => {
      handlers.clearArtifacts = handler
    },
    onOpenSettings: (handler) => {
      handlers.openSettings = handler
    },
    onOpen: (handler) => {
      handlers.open = handler
    },
  }
}
