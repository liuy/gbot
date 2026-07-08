import type { SessionListItem } from './types'

export interface SidebarHandles {
  root: HTMLElement
  overlay: HTMLElement
  open: () => void
  close: () => void
  closeImmediate: () => void
  toggle: () => void
  setSessions: (sessions: SessionListItem[], currentID: string) => void
  onSessionClick: (handler: (id: string) => void) => void
  onNewSession: (handler: () => void) => void
  onRename: (handler: (id: string, title: string) => void) => void
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

export function createSidebar(opts: { mainContent: HTMLElement }): SidebarHandles {
  const { mainContent } = opts

  const root = document.createElement('div')
  root.className =
    'fixed top-0 left-0 h-full w-72 z-50 glass-solid border-r border-hairline transition-transform duration-300 ease-out'
  root.style.transform = 'translateX(-100%)'

  const listContainer = document.createElement('div')
  listContainer.className = 'px-2 pt-4 overflow-y-auto'
  listContainer.style.maxHeight = 'calc(100dvh - 80px)'
  root.appendChild(listContainer)

  const fab = document.createElement('button')
  fab.className =
    'absolute bottom-5 right-5 w-10 h-10 rounded-full bg-blue/15 border border-blue/20 flex items-center justify-center hover:bg-blue/25 transition-colors'
  fab.innerHTML =
    '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="#00B4FF" stroke-width="2.5" stroke-linecap="round"><path d="M12 5v14M5 12h14"/></svg>'
  root.appendChild(fab)

  const overlay = document.createElement('div')
  overlay.className = 'fixed inset-0 z-40 bg-black/30 transition-opacity duration-300'
  overlay.style.display = 'none'

  const handlers = {
    sessionClick: (_id: string) => {},
    newSession: () => {},
    rename: (_id: string, _title: string) => {},
  }

  const isOpen = () => root.style.transform === 'translateX(0px)'

  const openFn = () => {
    root.style.transform = 'translateX(0px)'
    mainContent.style.transform = 'translateX(288px)'
    overlay.style.display = ''
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

  fab.addEventListener('click', () => {
    handlers.newSession()
    closeImmediate()
  })

  const setSessions = (sessions: SessionListItem[], currentID: string) => {
    listContainer.innerHTML = ''
    for (const s of sessions) {
      const isCurrent = s.id === currentID
      const row = document.createElement('div')
      row.className =
        'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer ' +
        (isCurrent
          ? 'bg-blue/15 border border-blue/20 text-blue font-medium'
          : 'hover:bg-ink3/30 text-t2')

      const titleSpan = document.createElement('span')
      titleSpan.className = 'text-[13px] truncate flex-1'
      titleSpan.textContent = s.title || s.id.slice(0, 8)

      const timeSpan = document.createElement('span')
      timeSpan.className = 'text-[10px] text-t3 shrink-0'
      timeSpan.textContent = formatRelativeTime(s.updatedAt)

      row.appendChild(titleSpan)
      row.appendChild(timeSpan)

      row.addEventListener('click', () => {
        handlers.sessionClick(s.id)
        closeImmediate()
      })

      // Long-press rename
      let pressTimer: ReturnType<typeof setTimeout> | null = null
      let pressed = false
      row.addEventListener('touchstart', () => {
        pressTimer = setTimeout(() => {
          pressed = true
          startRename(row, titleSpan, s)
        }, 500)
      })
      row.addEventListener('touchend', () => {
        if (pressTimer) clearTimeout(pressTimer)
      })
      row.addEventListener('touchmove', () => {
        if (pressTimer) clearTimeout(pressTimer)
      })

      listContainer.appendChild(row)
    }
  }

  const startRename = (
    row: HTMLElement,
    titleSpan: HTMLElement,
    session: SessionListItem,
  ) => {
    const originalText = titleSpan.textContent || ''
    const input = document.createElement('input')
    input.type = 'text'
    input.value = originalText
    input.className =
      'text-[13px] flex-1 bg-ink2 border border-blue/30 rounded px-1 py-0.5 text-t1 outline-none'
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
    setSessions,
    onSessionClick: (handler) => {
      handlers.sessionClick = handler
    },
    onNewSession: (handler) => {
      handlers.newSession = handler
    },
    onRename: (handler) => {
      handlers.rename = handler
    },
  }
}
