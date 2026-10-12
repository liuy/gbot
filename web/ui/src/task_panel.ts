import type { TaskWireItem, JobWireItem } from './types'
import type { Block } from './model'
import { createPopupPanel, createPopupHost } from './utils'
import { createElement } from './dom'
import { renderIcon } from './icons'
import { createFloatButton } from './buttons'
import { createJobsStore, type JobsStoreHandle } from './jobs_store'
import { createJobsSection } from './jobs_render'
import { t } from './i18n'

export interface TaskPanelOptions {
  // Wired by chat.ts to conn.send; the stop button lands in Phase 3, the
  // interface is complete now so chat wiring does not churn twice.
  onStopJob: (jobID: string) => void
}

export interface TaskPanelHandles {
  root: HTMLElement
  setTasks: (tasks: TaskWireItem[]) => void
  setJobs: (jobs: JobWireItem[], hydrated?: Map<string, { blocks?: Block[]; tail?: string }>) => void
  jobsStore: JobsStoreHandle
}

export function createTaskPanel(opts?: Partial<TaskPanelOptions>): TaskPanelHandles {
  // Match scrollBtn style exactly: transparent bg, same size/positioning.
  const floatBtn = createFloatButton({
    position: 'right',
    progressRing: {
      progressClassName: 'task-ring',
      backgroundOpacity: 0.2,
      transitionMs: 300,
      transitionEasing: 'ease',
    },
    labelClassName: 'task-label',
  })
  const root = floatBtn.root
  root.style.display = 'none'

  const jobsStore = createJobsStore()
  const jobsSection = createJobsSection()
  // Expansion state outlives popover rebuilds: a 2Hz job frame must not
  // collapse a row the user is reading.
  const expanded = new Set<string>()
  // Rows only rebuild when the job list identity changes (new/removed/status
  // flip). Pure content updates (bash tail, agent blocks) refresh the feed
  // in place via jobsSection.scheduleRender — full row rebuilds at frame
  // rate would eat hover state and swap the stop button mid-click.
  let lastJobsSig = ''
  const jobsSig = () => jobsStore.jobs().map((j) => `${j.id}:${j.status}`).join('|')
  let currentTasks: TaskWireItem[] = []
  // Latches the first section render of an open popover session: a job going
  // terminal mid-read must not yank its row out from under the user, so the
  // section keeps rendering until the popover closes. A fresh open re-applies
  // the running-only gate (terminal residue alone renders nothing).
  let jobsSticky = false

  // Panel is built once; onOpen clears and rebuilds content so each open
  // reflects currentTasks at click time. onClose detaches so the hidden
  // panel doesn't linger in the DOM.
  const popover = createPopupPanel({ bottom: true, className: 'right-5 left-auto translate-x-0' })
  popover.id = 'task-popover'

  const rebuildPopover = () => {
    popover.replaceChildren()
    const done = currentTasks.filter(t => t.status === 'completed').length
    const running = currentTasks.filter(t => t.status === 'in_progress').length
    const pending = currentTasks.filter(t => t.status === 'pending').length
    const jobs = jobsStore.jobs()
    const runningJobs = jobs.filter(j => j.status === 'running').length
    const parts: string[] = []
    if (currentTasks.length > 0) parts.push(`${done}/${currentTasks.length} Done`)
    if (running > 0) parts.push(`${running} Running`)
    if (pending > 0) parts.push(`${pending} Pending`)
    // The background aggregate speaks only while work is live: zero running
    // jobs means no background mention at all (no idle line).
    if (runningJobs > 0) parts.push(`${t('jobsSectionTitle')} ${runningJobs} Running`)

    if (parts.length > 0) {
      const title = createElement('div', 'px-3 pt-2.5 pb-1 text-[11px] text-t3 font-medium')
      title.textContent = parts.join(' · ')
      popover.appendChild(title)
    }

    const list = createElement('div', 'px-2 pb-2 space-y-0.5 max-h-[200px] overflow-y-auto')
    for (const task of currentTasks) {
      list.appendChild(renderRow(task))
    }
    appendJobsSection(list, jobs)
    popover.appendChild(list)
    lastJobsSig = jobsSig()
  }

  const host = createPopupHost({
    trigger: root,
    panel: popover,
    onOpen: rebuildPopover,
    onClose: () => {
      popover.remove()
      // Retention ends with the close: re-evaluate so an all-terminal view
      // releases the trigger immediately.
      jobsSticky = false
      updateChrome()
    },
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

  function appendJobsSection(list: HTMLElement, jobs: JobWireItem[]) {
    // Render gate: live work only. jobsSticky covers the open-panel retention
    // window (running→terminal flips must not yank rows mid-read).
    if (jobs.length === 0 || (!jobsStore.hasRunning() && !jobsSticky)) return
    jobsSticky = true
    const sec = createElement('div', 'jobs-section px-2 pt-2 pb-1')
    const label = createElement('span', 'text-[10px] font-semibold tracking-wider uppercase text-t3')
    label.textContent = t('jobsSectionTitle')
    sec.append(label)
    list.appendChild(sec)
    // Rows (and expansion boxes) render through jobs_render; expansion
    // state and feed elements survive this rebuild by design.
    const rows = createElement('div')
    list.appendChild(rows)
    jobsSection.rebuild(rows, jobsStore, {
      expanded,
      onStop: (id) => opts?.onStopJob?.(id),
    })
  }

  const updateChrome = () => {
    const jobs = jobsStore.jobs()
    const tasksActive = currentTasks.length > 0 && !currentTasks.every(t => t.status === 'completed')
    // An open panel outranks hiding: it may be showing retained terminal
    // rows, and the popover is anchored to this trigger — hiding it would
    // strand an orphaned panel. onClose re-runs this the moment the panel
    // shuts, so retention still ends with the close.
    if (!tasksActive && !jobsStore.hasRunning() && !host.isOpen()) {
      root.style.display = 'none'
      return
    }
    root.style.display = ''
    requestAnimationFrame(() => {
      root.classList.remove('opacity-0', 'pointer-events-none')
    })

    // Ring and label stay task-only while tasks exist; jobs-only mode shows
    // the running count (terminal jobs never keep the trigger alive).
    if (currentTasks.length > 0) {
      const done = currentTasks.filter(t => t.status === 'completed').length
      floatBtn.setProgress(done / currentTasks.length)
      const totalStr = currentTasks.length > 99 ? '99+' : String(currentTasks.length)
      const doneStr = done > 99 ? '99+' : String(done)
      floatBtn.setLabel(`${doneStr}/${totalStr}`)
    } else {
      floatBtn.setProgress(0)
      floatBtn.setLabel(String(jobs.filter(j => j.status === 'running').length))
    }
  }

  const setTasks = (tasks: TaskWireItem[]) => {
    currentTasks = tasks
    // Unconditional close is the pre-existing behavior: task frames mean the
    // whole task list generation swapped. Deliberately NOT mirrored by
    // setJobs below.
    host.close()
    updateChrome()
  }

  // Job frames must never close the popover: a job completing while the user
  // is reading its row would yank the panel shut. Chrome updates and the
  // open popover's content rebuild via the store's onChange.
  const setJobs = (jobs: JobWireItem[], hydrated?: Map<string, { blocks?: Block[]; tail?: string }>) => {
    jobsStore.setJobs(jobs, hydrated)
  }

  jobsStore.onChange(() => {
    updateChrome()
    if (!host.isOpen()) return
    if (jobsSig() !== lastJobsSig) {
      rebuildPopover()
    } else {
      jobsSection.scheduleRender()
    }
  })

  // Cleanup popover if root is removed from DOM.
  new MutationObserver((_mutations, observer) => {
    if (!root.isConnected) {
      // jsdom teardown fires observer after document is gone; guard.
      if (typeof document !== 'undefined') host.close()
      observer.disconnect()
    }
  }).observe(root.parentElement ?? document.body, { childList: true })

  return { root, setTasks, setJobs, jobsStore }
}
