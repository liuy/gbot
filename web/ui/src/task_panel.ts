import type { TaskWireItem } from './types'
import { createPopupPanel, createPopupHost } from './utils'
import { floatingButton } from './styles/recipes'
import { progressRingCircles, progressRingDashOffset } from './components/progress_ring'
import { createElement, createNode } from './dom'
import { renderIcon } from './icons'

export interface TaskPanelHandles {
  root: HTMLElement
  setTasks: (tasks: TaskWireItem[]) => void
}

export function createTaskPanel(): TaskPanelHandles {
  // Match scrollBtn style exactly: transparent bg, same size/positioning.
  const root = createNode('button', {
    className: floatingButton({ position: 'right' }),
    props: { type: 'button' },
    style: { display: 'none' },
  })

  root.innerHTML =
    '<svg width="44" height="44" viewBox="0 0 44 44">' +
    progressRingCircles({
      progressClassName: 'task-ring',
      backgroundOpacity: 0.2,
      transitionMs: 300,
      transitionEasing: 'ease',
    }) +
    '<text class="task-label" x="22" y="22" text-anchor="middle" dominant-baseline="central" ' +
    'fill="currentColor" style="font-size:11px;font-weight:600;font-family:ui-monospace,monospace"/>' +
    '</svg>'

  const ring = root.querySelector('.task-ring') as SVGCircleElement
  const label = root.querySelector('.task-label') as SVGTextElement

  // Panel is built once; onOpen clears and rebuilds content so each open
  // reflects currentTasks at click time. onClose detaches so the hidden
  // panel doesn't linger in the DOM.
  const popover = createPopupPanel({ bottom: true, className: 'right-5 left-auto translate-x-0' })
  popover.id = 'task-popover'
  let currentTasks: TaskWireItem[] = []

  const host = createPopupHost({
    trigger: root,
    panel: popover,
    onOpen: () => {
      // onOpen clears and rebuilds content so each open reflects currentTasks
      // at click time. Without this, reopen after close would stack a second
      // title+list on top of the stale ones (instance is reused).
      popover.replaceChildren()
      const done = currentTasks.filter(t => t.status === 'completed').length
      const running = currentTasks.filter(t => t.status === 'in_progress').length
      const pending = currentTasks.filter(t => t.status === 'pending').length
      const parts: string[] = [`${done}/${currentTasks.length} Done`]
      if (running > 0) parts.push(`${running} Running`)
      if (pending > 0) parts.push(`${pending} Pending`)

      const title = createElement('div', 'px-3 pt-2.5 pb-1 text-[11px] text-t3 font-medium')
      title.textContent = parts.join(' · ')
      popover.appendChild(title)

      const list = createElement('div', 'px-2 pb-2 space-y-0.5 max-h-[200px] overflow-y-auto')
      for (const t of currentTasks) {
        list.appendChild(renderRow(t))
      }
      popover.appendChild(list)
    },
    onClose: () => { popover.remove() },
  })

  root.addEventListener('click', () => host.toggle())

  function renderRow(t: TaskWireItem): HTMLElement {
    const row = createElement('div', 'flex items-center gap-2 px-2 py-1.5 rounded-lg text-[13px]')

    const icon = createElement('span', 'flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center')

    const subject = createElement('span', 'flex-1')

    if (t.status === 'completed') {
      icon.className += ' bg-green/20 text-green'
      // 'check' is not in IconName; inline SVG preserved so task_panel.test.ts
      // path-substring assertion stays byte-stable.
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M20 6L9 17l-5-5"/></svg>'
      subject.className += ' text-t3 line-through'
      subject.textContent = t.subject
      row.append(icon, subject)
    } else if (t.status === 'in_progress') {
      icon.className += ' bg-blue/20 text-blue'
      icon.replaceChildren(renderIcon('refresh', { size: 10, strokeWidth: 2.5, className: 'spin' }))
      subject.className += ' text-t1 font-medium'
      subject.textContent = t.subject
      const run = createElement('span', 'mono text-[10px] text-blue pulse')
      run.textContent = 'Running'
      row.append(icon, subject, run)
    } else {
      icon.className += ' border border-t3/40'
      subject.className += ' text-t2/70'
      subject.textContent = t.subject
      if (t.blockedBy && t.blockedBy.length > 0) {
        const bl = createElement('span', 'mono text-[9px] text-t3')
        bl.textContent = 'Blocked by ' + t.blockedBy.join(', ')
        row.append(icon, subject, bl)
      } else {
        row.append(icon, subject)
      }
    }
    return row
  }

  const setTasks = (tasks: TaskWireItem[]) => {
    currentTasks = tasks
    host.close()

    if (tasks.length === 0 || tasks.every(t => t.status === 'completed')) {
      root.style.display = 'none'
      return
    }

    root.style.display = ''
    requestAnimationFrame(() => {
      root.classList.remove('opacity-0', 'pointer-events-none')
    })

    const done = tasks.filter(t => t.status === 'completed').length
    const ratio = done / tasks.length
    ring.setAttribute('stroke-dashoffset', String(progressRingDashOffset(ratio)))
    const totalStr = tasks.length > 99 ? '99+' : String(tasks.length)
    const doneStr = done > 99 ? '99+' : String(done)
    label.textContent = `${doneStr}/${totalStr}`
  }

  // Cleanup popover if root is removed from DOM.
  new MutationObserver((_mutations, observer) => {
    if (!root.isConnected) {
      // jsdom teardown fires observer after document is gone; guard.
      if (typeof document !== 'undefined') host.close()
      observer.disconnect()
    }
  }).observe(root.parentElement ?? document.body, { childList: true })

  return { root, setTasks }
}
