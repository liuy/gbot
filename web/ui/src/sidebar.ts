import type { SessionListItem } from './types'
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

  const root = createNode('div', {
    className:
      'fixed top-0 left-0 h-full w-72 z-50 glass-solid border-r border-hairline transition-transform duration-300 ease-out',
    style: { transform: 'translateX(-100%)' },
  })

  const listContainer = createNode('div', {
    className: 'px-2 pt-4 overflow-y-auto',
    style: { maxHeight: 'calc(100dvh - 80px)' },
  })
  root.appendChild(listContainer)

  // FAB: variant=default (text-blue hover:text-white) layers a transition on
  // top of the previous static look — slight UX upgrade accepted in D5.
  // absolute positioning classes go through className so sidebar.test.ts
  // `button.absolute` selector still finds the FAB.
  const fab = createIconButton({
    icon: 'plus',
    label: 'New session',
    variant: 'default',
    size: 'md',
    iconSize: 22,
    className: 'absolute bottom-5 right-5',
  })
  root.appendChild(fab)

  const THEME_CYCLE = ['dark', 'light', 'system'] as const
  type Theme = typeof THEME_CYCLE[number]

  const resolveTheme = (pref: Theme): 'dark' | 'light' => {
    if (pref === 'system') return window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark'
    return pref
  }

  const savedPref = (localStorage.getItem('gbot-theme') || 'dark') as Theme
  const effectiveTheme = resolveTheme(savedPref)
  document.documentElement.dataset.theme = effectiveTheme

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
    }
  }
  mediaQuery.addEventListener('change', onSystemChange)

  // Android WebView does not fire matchMedia change events when the system
  // theme switches. Expose a hook so the Kotlin side can call it from
  // onConfigurationChanged via evaluateJavascript.
  ;(window as any).__gbotApplySystemTheme = (isLight: boolean) => {
    const pref = (localStorage.getItem('gbot-theme') as Theme) || 'dark'
    if (pref !== 'system') return
    document.documentElement.dataset.theme = isLight ? 'light' : 'dark'
    applyHljsTheme(getSavedHljsTheme(), !isLight)
  }

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
      const row = createElement(
        'div',
        'flex items-center gap-2 px-3 py-2 rounded-lg cursor-pointer ' +
          (isCurrent
            ? 'bg-blue/15 border border-blue/20 text-blue font-medium'
            : 'hover:bg-ink3/30 text-t2'),
      )

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

      listContainer.appendChild(row)
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
