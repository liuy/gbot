import type { TaskWireItem } from './types'

export interface TaskPanelHandles {
  root: HTMLElement
  setTasks: (tasks: TaskWireItem[]) => void
}

const RING_RADIUS = 6
const RING_CIRCUMFERENCE = 2 * Math.PI * RING_RADIUS

export function createTaskPanel(): TaskPanelHandles {
  let expanded = false

  const root = document.createElement('div')
  root.className = 'mb-2 glass border border-hairline rounded-xl overflow-hidden'
  root.style.display = 'none'

  // ── Header button (always visible when panel is shown).
  const header = document.createElement('button')
  header.type = 'button'
  header.className = 'w-full flex items-center gap-2 px-3 py-2 text-left'

  const chev = document.createElement('span')
  chev.innerHTML = '<svg class="inline-block align-middle text-t3 transition-transform" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>'

  const ringWrap = document.createElementNS('http://www.w3.org/2000/svg', 'svg')
  ringWrap.setAttribute('width', '16')
  ringWrap.setAttribute('height', '16')
  ringWrap.setAttribute('viewBox', '0 0 16 16')
  ringWrap.setAttribute('class', 'flex-shrink-0')
  ringWrap.innerHTML =
    `<circle cx="8" cy="8" r="${RING_RADIUS}" fill="none" stroke="rgba(120,180,255,0.12)" stroke-width="2"/>` +
    `<circle class="task-ring" cx="8" cy="8" r="${RING_RADIUS}" fill="none" stroke="#00B4FF" stroke-width="2" stroke-linecap="round" stroke-dasharray="${RING_CIRCUMFERENCE.toFixed(3)}" stroke-dashoffset="${RING_CIRCUMFERENCE.toFixed(3)}" transform="rotate(-90 8 8)" style="transition:stroke-dashoffset 0.3s ease"/>`

  const doneText = document.createElement('span')
  doneText.className = 'mono text-[11px] text-t2'

  const middot = document.createElement('span')
  middot.className = 'mono text-[10px] text-t3'

  const runningText = document.createElement('span')
  runningText.className = 'mono text-[11px] text-blue pulse'

  const spacer = document.createElement('div')
  spacer.className = 'flex-1'

  const pendingText = document.createElement('span')
  pendingText.className = 'mono text-[10px] text-t3'

  header.append(chev, ringWrap, doneText, middot, runningText, spacer, pendingText)
  root.appendChild(header)

  // ── Expanded list.
  const list = document.createElement('div')
  list.className = 'hidden px-3 pb-2 space-y-0.5'
  root.appendChild(list)

  header.addEventListener('click', () => {
    expanded = !expanded
    list.classList.toggle('hidden', !expanded)
    const svg = chev.querySelector('svg')
    if (svg) svg.setAttribute('class',
      'inline-block align-middle text-t3 transition-transform' + (expanded ? ' rotate-90' : ''))
  })

  const renderRow = (t: TaskWireItem): HTMLElement => {
    const row = document.createElement('div')
    row.className = 'flex items-center gap-2 py-1'

    const icon = document.createElement('span')
    icon.className = 'flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center'

    const subject = document.createElement('span')
    subject.className = 'text-[13px]'

    if (t.status === 'completed') {
      icon.className += ' bg-green/20'
      icon.innerHTML =
        '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="#3DD68C" stroke-width="3"><path d="M20 6L9 17l-5-5"/></svg>'
      subject.className += ' text-t3 line-through'
      subject.textContent = t.subject
      row.append(icon, subject)
    } else if (t.status === 'in_progress') {
      icon.className += ' bg-blue/20'
      icon.innerHTML =
        '<svg class="spin" width="10" height="10" viewBox="0 0 24 24" fill="none" stroke="#00B4FF" stroke-width="2.5"><path d="M21 12a9 9 0 11-6.219-8.56"/></svg>'
      subject.className += ' text-t1 font-medium'
      subject.textContent = t.subject
      const running = document.createElement('span')
      running.className = 'mono text-[10px] text-blue pulse ml-auto'
      running.textContent = 'Running'
      row.append(icon, subject, running)
    } else {
      icon.className += ' border border-t3/40'
      subject.className += ' text-t2/70'
      subject.textContent = t.subject
      if (t.blockedBy && t.blockedBy.length > 0) {
        const blocked = document.createElement('span')
        blocked.className = 'mono text-[9px] text-t3 ml-auto'
        blocked.textContent = 'Blocked by ' + t.blockedBy.join(', ')
        row.append(icon, subject, blocked)
      } else {
        row.append(icon, subject)
      }
    }
    return row
  }

  const setTasks = (tasks: TaskWireItem[]) => {
    if (tasks.length === 0) {
      root.style.display = 'none'
      return
    }
    if (tasks.every((t) => t.status === 'completed')) {
      root.style.display = 'none'
      return
    }
    root.style.display = ''

    const completed = tasks.filter((t) => t.status === 'completed').length
    const running = tasks.filter((t) => t.status === 'in_progress').length
    const pending = tasks.filter((t) => t.status === 'pending').length

    doneText.textContent = `${completed}/${tasks.length} Done`

    // The running/middot block renders together only when running > 0.
    if (running > 0) {
      middot.textContent = '·'
      runningText.textContent = `${running} Running`
      middot.style.display = ''
      runningText.style.display = ''
    } else {
      middot.style.display = 'none'
      runningText.style.display = 'none'
    }

    if (pending > 0) {
      pendingText.textContent = `${pending} Pending`
      pendingText.style.display = ''
    } else {
      pendingText.style.display = 'none'
    }

    const ring = ringWrap.querySelector('.task-ring') as SVGCircleElement | null
    if (ring) {
      const ratio = tasks.length > 0 ? completed / tasks.length : 0
      ring.setAttribute('stroke-dashoffset', String(RING_CIRCUMFERENCE * (1 - ratio)))
    }

    list.replaceChildren(...tasks.map(renderRow))
  }

  return { root, setTasks }
}
