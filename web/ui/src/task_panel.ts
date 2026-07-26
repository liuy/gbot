import type { TaskWireItem } from './types'
import { createPopupPanel, createOutsideClick } from './utils'
import { floatingButton } from './styles/recipes'

export interface TaskPanelHandles {
  root: HTMLElement
  setTasks: (tasks: TaskWireItem[]) => void
}

const RING_R = 18
const RING_CIRC = 2 * Math.PI * RING_R

export function createTaskPanel(): TaskPanelHandles {
  const root = document.createElement('button')
  root.type = 'button'
  // Match scrollBtn style exactly: transparent bg, same size/positioning.
  root.className = floatingButton({ position: 'right' })
  root.style.display = 'none'

  root.innerHTML =
    '<svg width="44" height="44" viewBox="0 0 44 44">' +
    `<circle cx="22" cy="22" r="${RING_R}" fill="none" stroke="currentColor" stroke-width="2" opacity="0.2"/>` +
    `<circle class="task-ring" cx="22" cy="22" r="${RING_R}" fill="none" stroke="currentColor" stroke-width="2" ` +
    `stroke-linecap="round" stroke-dasharray="${RING_CIRC.toFixed(2)}" stroke-dashoffset="${RING_CIRC.toFixed(2)}" ` +
    'transform="rotate(-90 22 22)" style="transition:stroke-dashoffset 0.3s ease"/>' +
    '<text class="task-label" x="22" y="22" text-anchor="middle" dominant-baseline="central" ' +
    'fill="currentColor" style="font-size:11px;font-weight:600;font-family:ui-monospace,monospace"/>' +
    '</svg>'

  const ring = root.querySelector('.task-ring') as SVGCircleElement
  const label = root.querySelector('.task-label') as SVGTextElement

  let popover: HTMLDivElement | null = null
  let popoverClick: ReturnType<typeof createOutsideClick> | null = null
  let currentTasks: TaskWireItem[] = []

  function closePopover() {
    if (popover) {
      popover.remove()
      popover = null
    }
    if (popoverClick) {
      popoverClick.remove()
      popoverClick = null
    }
  }

  root.addEventListener('click', () => {
    if (popover) { closePopover(); return }

    popover = createPopupPanel({ bottom: true, className: 'right-5 left-auto translate-x-0' })
    popover.id = 'task-popover'
    popover.classList.remove('hidden')

    const done = currentTasks.filter(t => t.status === 'completed').length
    const running = currentTasks.filter(t => t.status === 'in_progress').length
    const pending = currentTasks.filter(t => t.status === 'pending').length
    const parts: string[] = [`${done}/${currentTasks.length} Done`]
    if (running > 0) parts.push(`${running} Running`)
    if (pending > 0) parts.push(`${pending} Pending`)

    const title = document.createElement('div')
    title.className = 'px-3 pt-2.5 pb-1 text-[11px] text-t3 font-medium'
    title.textContent = parts.join(' · ')
    popover.appendChild(title)

    const list = document.createElement('div')
    list.className = 'px-2 pb-2 space-y-0.5 max-h-[200px] overflow-y-auto'
    for (const t of currentTasks) {
      list.appendChild(renderRow(t))
    }
    popover.appendChild(list)

    document.body.appendChild(popover)
    popoverClick = createOutsideClick(root, popover, closePopover)
    popoverClick.add()
  })

  function renderRow(t: TaskWireItem): HTMLElement {
    const row = document.createElement('div')
    row.className = 'flex items-center gap-2 px-2 py-1.5 rounded-lg text-[13px]'

    const icon = document.createElement('span')
    icon.className = 'flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center'

    const subject = document.createElement('span')
    subject.className = 'flex-1'

    if (t.status === 'completed') {
      icon.className += ' bg-green/20 text-green'
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M20 6L9 17l-5-5"/></svg>'
      subject.className += ' text-t3 line-through'
      subject.textContent = t.subject
      row.append(icon, subject)
    } else if (t.status === 'in_progress') {
      icon.className += ' bg-blue/20 text-blue'
      icon.innerHTML = '<svg class="spin" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5"><path d="M21 12a9 9 0 11-6.219-8.56"/></svg>'
      subject.className += ' text-t1 font-medium'
      subject.textContent = t.subject
      const run = document.createElement('span')
      run.className = 'mono text-[10px] text-blue pulse'
      run.textContent = 'Running'
      row.append(icon, subject, run)
    } else {
      icon.className += ' border border-t3/40'
      subject.className += ' text-t2/70'
      subject.textContent = t.subject
      if (t.blockedBy && t.blockedBy.length > 0) {
        const bl = document.createElement('span')
        bl.className = 'mono text-[9px] text-t3'
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
    closePopover()

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
    ring.setAttribute('stroke-dashoffset', String(RING_CIRC * (1 - ratio)))
    const totalStr = tasks.length > 99 ? '99+' : String(tasks.length)
    const doneStr = done > 99 ? '99+' : String(done)
    label.textContent = `${doneStr}/${totalStr}`
  }

  // Cleanup popover if root is removed from DOM.
  new MutationObserver((_mutations, observer) => {
    if (!root.isConnected) {
      closePopover()
      observer.disconnect()
    }
  }).observe(root.parentElement ?? document.body, { childList: true })

  return { root, setTasks }
}
