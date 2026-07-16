import type { SessionListItem } from './types'
import { HLJS_THEMES, getSavedHljsTheme, saveHljsTheme, applyHljsTheme } from './hljs_themes'

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
    'absolute bottom-5 right-5 w-10 h-10 flex items-center justify-center text-blue'
  fab.innerHTML =
    '<svg width="18" height="18" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M7 2v10M2 7h10"/></svg>'
  root.appendChild(fab)

  // Theme toggle — bottom-left corner
  const moonSvg = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79z"/></svg>'
  const sunSvg = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="5"/><path d="M12 1v2M12 21v2M4.22 4.22l1.42 1.42M18.36 18.36l1.42 1.42M1 12h2M21 12h2M4.22 19.78l1.42-1.42M18.36 5.64l1.42-1.42"/></svg>'
  const taiChiSvg = '<svg width="18" height="18" viewBox="0 0 24 24" fill="none"><path d="M17 3.34a10 10 0 1 1 -14.995 8.984l-.005 -.324l.005 -.324a10 10 0 0 1 14.995 -8.336zm-9 1.732a8 8 0 0 0 4 14.928l.2 -.005a4 4 0 0 0 0 -7.99l-.2 -.005a4 4 0 0 1 -.2 -7.995l.2 -.005a7.995 7.995 0 0 0 -4 1.072zm4 1.428a1.5 1.5 0 1 0 0 3a1.5 1.5 0 0 0 0 -3" fill="currentColor"/><circle cx="12" cy="15.5" r="1.5" fill="currentColor"/><circle cx="12" cy="8.5" r="1.5" fill="var(--color-ink2, white)"/></svg>'

  const THEME_CYCLE = ['dark', 'light', 'system'] as const
  type Theme = typeof THEME_CYCLE[number]

  const resolveTheme = (pref: Theme): 'dark' | 'light' => {
    if (pref === 'system') return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    return pref
  }

  const savedPref = (localStorage.getItem('gbot-theme') || 'dark') as Theme
  const effectiveTheme = resolveTheme(savedPref)
  document.documentElement.dataset.theme = effectiveTheme

  const themeIcon = (pref: Theme) => pref === 'dark' ? moonSvg : pref === 'light' ? sunSvg : taiChiSvg

  const themeToggle = document.createElement('button')
  themeToggle.className =
    'absolute bottom-5 left-5 w-10 h-10 flex items-center justify-center text-t2'
  themeToggle.innerHTML = themeIcon(savedPref)

  const mediaQuery = window.matchMedia('(prefers-color-scheme: light)')
  const onSystemChange = () => {
    const pref = localStorage.getItem('gbot-theme') as Theme || 'dark'
    if (pref === 'system') {
      const resolved = resolveTheme('system')
      document.documentElement.dataset.theme = resolved
      applyHljsTheme(getSavedHljsTheme(), resolved === 'dark')
    }
  }
  mediaQuery.addEventListener('change', onSystemChange)

  // Apply saved hljs theme on load
  applyHljsTheme(getSavedHljsTheme(), effectiveTheme === 'dark')

  themeToggle.addEventListener('click', () => {
    const current = (localStorage.getItem('gbot-theme') || 'dark') as Theme
    const idx = THEME_CYCLE.indexOf(current)
    const next = THEME_CYCLE[(idx + 1) % THEME_CYCLE.length]
    localStorage.setItem('gbot-theme', next)
    const resolved = resolveTheme(next)
    document.documentElement.dataset.theme = resolved
    themeToggle.innerHTML = themeIcon(next)
    applyHljsTheme(getSavedHljsTheme(), resolved === 'dark')
  })

  // Long-press: open highlight theme selector
  let themePressTimer: ReturnType<typeof setTimeout> | null = null
  let themePressed = false
  themeToggle.addEventListener('touchstart', () => {
    themePressTimer = setTimeout(() => {
      themePressed = true
      openHljsPopover()
    }, 500)
  })
  themeToggle.addEventListener('touchend', () => {
    if (themePressTimer) clearTimeout(themePressTimer)
  })
  themeToggle.addEventListener('touchmove', () => {
    if (themePressTimer) clearTimeout(themePressTimer)
  })
  // Mouse long-press for desktop
  let mousePressTimer: ReturnType<typeof setTimeout> | null = null
  themeToggle.addEventListener('mousedown', () => {
    mousePressTimer = setTimeout(() => {
      themePressed = true
      openHljsPopover()
    }, 500)
  })
  themeToggle.addEventListener('mouseup', () => {
    if (mousePressTimer) clearTimeout(mousePressTimer)
  })
  themeToggle.addEventListener('mouseleave', () => {
    if (mousePressTimer) clearTimeout(mousePressTimer)
  })
  // Prevent click from firing after long-press
  themeToggle.addEventListener('click', (e) => {
    if (themePressed) {
      themePressed = false
      e.preventDefault()
      e.stopPropagation()
    }
  })

  function openHljsPopover() {
    // Remove existing popover if open
    const existing = document.getElementById('hljs-popover')
    if (existing) { existing.remove(); return }

    const currentHljs = getSavedHljsTheme()
    const currentTheme = (localStorage.getItem('gbot-theme') || 'dark') as Theme
    const isDark = resolveTheme(currentTheme) === 'dark'

    const popover = document.createElement('div')
    popover.id = 'hljs-popover'
    popover.className = 'fixed z-50 glass-solid border border-hairline rounded-xl p-2 shadow-2xl modal-enter'
    popover.style.bottom = '60px'
    popover.style.left = '20px'
    popover.style.minWidth = '180px'

    const title = document.createElement('div')
    title.className = 'text-[11px] text-t3 px-2 py-1 font-medium'
    title.textContent = 'Highlight Theme'
    popover.appendChild(title)

    for (const theme of HLJS_THEMES) {
      const isSelected = theme.key === currentHljs
      const row = document.createElement('div')
      row.className =
        'flex items-center gap-2 px-2 py-1.5 rounded-lg cursor-pointer text-[13px] ' +
        (isSelected
          ? 'bg-blue/15 border border-blue/20 text-blue font-medium'
          : 'hover:bg-ink3/50 text-t2')
      const label = document.createElement('span')
      label.className = 'flex-1'
      label.textContent = theme.label
      row.appendChild(label)
      if (isSelected) {
        const check = document.createElement('span')
        check.className = 'text-blue text-[13px]'
        check.textContent = '✓'
        row.appendChild(check)
      }
      row.addEventListener('click', () => {
        saveHljsTheme(theme.key)
        applyHljsTheme(theme.key, isDark)
        closePopover()
      })
      popover.appendChild(row)
    }

    document.body.appendChild(popover)

    const closePopover = () => {
      popover.remove()
      document.removeEventListener('mousedown', onDown)
      document.removeEventListener('touchstart', onDown)
    }
    const onDown = (ev: MouseEvent | TouchEvent) => {
      if (!popover.contains(ev.target as Node)) closePopover()
    }
    document.addEventListener('mousedown', onDown)
    document.addEventListener('touchstart', onDown)
  }
  root.appendChild(themeToggle)

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
        if (pressed) {
          pressed = false
          return
        }
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
