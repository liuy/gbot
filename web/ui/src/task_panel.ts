import type { TaskWireItem, JobWireItem } from './types'
import type { Block } from './model'
import { createPopupPanel, createPopupHost } from './utils'
import { createElement } from './dom'
import { renderIcon } from './icons'
import { createFloatButton } from './buttons'
import { createJobsStore, type JobsStoreHandle } from './jobs_store'
import { t } from './i18n'

export interface TaskPanelOptions {
  // Wired by chat.ts to conn.send; the stop button lands in Phase 3, the
  // interface is complete now so chat wiring does not churn twice.
  onStopJob: (jobID: string) => void
  onClearDone: () => void
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
    rimDot: true,
  })
  const root = floatBtn.root
  root.style.display = 'none'

  const jobsStore = createJobsStore()
  let currentTasks: TaskWireItem[] = []

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
    const parts: string[] = []
    if (currentTasks.length > 0) parts.push(`${done}/${currentTasks.length} Done`)
    if (running > 0) parts.push(`${running} Running`)
    if (pending > 0) parts.push(`${pending} Pending`)
    if (jobs.length > 0) {
      const runningJobs = jobs.filter(j => j.status === 'running').length
      parts.push(runningJobs > 0 ? `Background ${runningJobs} Running` : 'Background idle')
    }

    const title = createElement('div', 'px-3 pt-2.5 pb-1 text-[11px] text-t3 font-medium')
    title.textContent = parts.join(' · ')
    popover.appendChild(title)

    const list = createElement('div', 'px-2 pb-2 space-y-0.5 max-h-[200px] overflow-y-auto')
    for (const task of currentTasks) {
      list.appendChild(renderRow(task))
    }
    appendJobsSection(list, jobs)
    popover.appendChild(list)
  }

  const host = createPopupHost({
    trigger: root,
    panel: popover,
    onOpen: rebuildPopover,
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

  function appendJobsSection(list: HTMLElement, jobs: JobWireItem[]) {
    if (jobs.length === 0) return
    const sec = createElement('div', 'jobs-section flex items-center justify-between px-2 pt-2 pb-1')
    const label = createElement('span', 'text-[10px] font-semibold tracking-wider uppercase text-t3')
    label.textContent = t('jobsSectionTitle')
    const clr = createElement('button', 'clear-done-jobs text-[11px] text-t3 hover:text-t2 px-1.5 py-0.5 rounded-md') as HTMLButtonElement
    clr.type = 'button'
    clr.textContent = t('clearDoneJobs')
    clr.addEventListener('click', (e) => {
      e.stopPropagation()
      // Optimistic clear for instant feedback; the server answers with a
      // fresh job_list that re-confirms the same state.
      jobsStore.clearDone()
      opts?.onClearDone?.()
    })
    sec.append(label, clr)
    list.appendChild(sec)
    for (const j of jobs) list.appendChild(renderJobRow(j))
  }

  function renderJobRow(j: JobWireItem): HTMLElement {
    const row = createElement('div', 'jobs-row flex items-center gap-2 px-2 py-1.5 rounded-lg text-[13px]')
    if (j.status !== 'running') row.className += ' opacity-60 hover:opacity-100'

    const icon = createElement('span', 'flex-shrink-0 w-4 h-4 rounded-full flex items-center justify-center')
    if (j.status === 'running') {
      const ring = createElement('span', 'w-3.5 h-3.5 rounded-full border-2 border-blue/25 border-t-blue spin')
      icon.replaceChildren(ring)
    } else if (j.status === 'completed') {
      icon.className += ' bg-green/20 text-green'
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M20 6L9 17l-5-5"/></svg>'
    } else {
      icon.className += ' bg-red/20 text-red'
      icon.innerHTML = '<svg width="9" height="9" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="3"><path d="M18 6L6 18M6 6l12 12"/></svg>'
    }

    const name = createElement('span', 'flex-1 min-w-0 truncate')
    if (j.kind === 'bash') {
      name.className += ' mono text-[12px]'
      name.textContent = j.command ?? ''
    } else {
      const label = j.agentType === 'fork' ? t('jobKindFork') : (j.agentType ?? '')
      name.textContent = j.description ? `${label} · ${j.description}` : label
    }
    row.append(icon, name, renderJobChip(j))
    return row
  }

  // Chips are English mono by design (Running/Done — same language as the
  // existing task rows); the surrounding UI text is i18n'd.
  function renderJobChip(j: JobWireItem): HTMLElement {
    const chip = createElement('span', 'mono text-[10px] flex-shrink-0')
    if (j.status === 'running') {
      chip.className += ' text-blue pulse'
      chip.textContent = 'Running'
    } else if (j.status === 'completed') {
      chip.className += ' text-green'
      chip.textContent = 'Done'
    } else if (j.status === 'failed') {
      chip.className += ' text-red'
      chip.textContent = j.exitCode ? `Exit ${j.exitCode}` : 'Failed'
    } else {
      chip.className += ' text-red'
      chip.textContent = 'Stopped'
    }
    return chip
  }

  const updateChrome = () => {
    const jobs = jobsStore.jobs()
    const tasksActive = currentTasks.length > 0 && !currentTasks.every(t => t.status === 'completed')
    if (!tasksActive && jobs.length === 0) {
      root.style.display = 'none'
      floatBtn.setRunning(false)
      // The popover anchors to the button: hiding the trigger would strand
      // an orphaned panel in the DOM.
      host.close()
      return
    }
    root.style.display = ''
    requestAnimationFrame(() => {
      root.classList.remove('opacity-0', 'pointer-events-none')
    })

    // Ring and label stay task-only while tasks exist; jobs-only mode shows
    // the running count (or total when idle-but-kept).
    if (currentTasks.length > 0) {
      const done = currentTasks.filter(t => t.status === 'completed').length
      floatBtn.setProgress(done / currentTasks.length)
      const totalStr = currentTasks.length > 99 ? '99+' : String(currentTasks.length)
      const doneStr = done > 99 ? '99+' : String(done)
      floatBtn.setLabel(`${doneStr}/${totalStr}`)
    } else {
      floatBtn.setProgress(0)
      const runningJobs = jobs.filter(j => j.status === 'running').length
      floatBtn.setLabel(String(runningJobs || jobs.length))
    }
    floatBtn.setRunning(jobsStore.hasRunning())
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
    if (host.isOpen()) rebuildPopover()
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
