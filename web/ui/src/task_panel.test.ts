import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createTaskPanel } from './task_panel'
import type { TaskWireItem, JobWireItem } from './types'

function mount() {
  document.body.innerHTML = ''
  const panel = createTaskPanel()
  document.body.appendChild(panel.root)
  return panel
}

describe('taskPanel', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('hides root when tasks array is empty', () => {
    const panel = mount()
    panel.setTasks([])
    expect(panel.root.style.display).toBe('none')
  })

  it('hides root initially before any setTasks', () => {
    const panel = mount()
    expect(panel.root.style.display).toBe('none')
  })

  it('shows ring with correct label and progress offset', () => {
    const panel = mount()
    const tasks: TaskWireItem[] = [
      { id: '1', subject: 'A', status: 'completed' },
      { id: '2', subject: 'B', status: 'in_progress' },
      { id: '3', subject: 'C', status: 'pending' },
    ]
    panel.setTasks(tasks)

    expect(panel.root.style.display).toBe('')

    const label = panel.root.querySelector('.task-label') as SVGTextElement
    expect(label.textContent).toBe('1/3')

    const ring = panel.root.querySelector('.task-ring') as SVGCircleElement
    const circumference = 2 * Math.PI * 18
    const expectedOffset = circumference * (1 - 1 / 3)
    const actual = parseFloat(ring.getAttribute('stroke-dashoffset') ?? 'NaN')
    expect(Math.abs(actual - expectedOffset)).toBeLessThan(0.01)
    // No pulse on the ring even with running tasks.
    expect(panel.root.classList.contains('pulse')).toBe(false)
  })

  it('hides panel when all tasks are completed', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Only', status: 'completed' }])
    expect(panel.root.style.display).toBe('none')
  })

  it('popover shows full summary on click', () => {
    const panel = mount()
    panel.setTasks([
      { id: '1', subject: 'A', status: 'completed' },
      { id: '2', subject: 'B', status: 'in_progress' },
      { id: '3', subject: 'C', status: 'pending' },
    ])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover).toBeTruthy()
    const text = popover.textContent ?? ''
    expect(text).toContain('1/3 Done')
    expect(text).toContain('1 Running')
    expect(text).toContain('1 Pending')
  })

  it('clicking again closes the popover', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Task', status: 'pending' }])
    ;(panel.root as HTMLButtonElement).click()
    expect(document.getElementById('task-popover')).toBeTruthy()

    ;(panel.root as HTMLButtonElement).click()
    expect(document.getElementById('task-popover')).toBeNull()
  })

  it('setTasks closes any open popover', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'A', status: 'pending' }])
    ;(panel.root as HTMLButtonElement).click()
    expect(document.getElementById('task-popover')).toBeTruthy()

    panel.setTasks([
      { id: '1', subject: 'A', status: 'completed' },
      { id: '2', subject: 'B', status: 'pending' },
    ])
    expect(document.getElementById('task-popover')).toBeNull()
  })

  it('popover renders completed checkmark with line-through', () => {
    const panel = mount()
    panel.setTasks([
      { id: '1', subject: 'Done thing', status: 'completed' },
      { id: '2', subject: 'Pending thing', status: 'pending' },
    ])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    const lineThrough = popover.querySelector('.line-through')
    expect(lineThrough).toBeTruthy()
    expect(lineThrough?.textContent).toBe('Done thing')
    const checkSvg = popover.querySelector('svg path')
    expect(checkSvg?.getAttribute('d')).toContain('M20 6L9 17l-5-5')
  })

  it('popover renders in_progress spinner and running label', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Active task', status: 'in_progress' }])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    const text = popover.textContent ?? ''
    expect(text).toContain('Running')
    expect(text).toContain('Active task')

    const spinner = popover.querySelector('.spin')
    expect(spinner?.outerHTML).toContain('M21 12a9 9 0 11-6.219-8.56')
  })

  it('popover renders pending with blockedBy subjects', () => {
    const panel = mount()
    panel.setTasks([
      { id: '1', subject: 'Blocker task', status: 'in_progress' },
      { id: '2', subject: 'Waiter', status: 'pending', blockedBy: ['Blocker task'] },
    ])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    const text = popover.textContent ?? ''
    expect(text).toContain('Blocked by Blocker task')
    expect(text).toContain('Waiter')
  })

  it('popover renders pending without blockedBy element when none provided', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Free task', status: 'pending' }])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    expect(popover.textContent).not.toContain('Blocked by')
  })

  it('open-close-open does not accumulate children', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Pending task', status: 'pending' }])
    ;(panel.root as HTMLButtonElement).click()

    const popover = document.getElementById('task-popover') as HTMLElement
    // First open: exactly 1 title row + 1 list = 2 direct children.
    expect(popover.children.length).toBe(2)

    // Outside click closes (popover is detached but instance persists).
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(document.getElementById('task-popover')).toBeNull()

    // Reopen — onOpen must rebuild, not append.
    ;(panel.root as HTMLButtonElement).click()
    const reopened = document.getElementById('task-popover') as HTMLElement
    expect(reopened.children.length).toBe(2)
    // Title row should appear exactly once.
    const titleRows = reopened.querySelectorAll('.px-3.pt-2\\.5')
    expect(titleRows.length).toBe(1)
  })
})

function jw(partial: Partial<JobWireItem> & Pick<JobWireItem, 'id' | 'kind' | 'status'>): JobWireItem {
  return { startedMs: 1000, ...partial }
}

describe('taskPanel — background jobs section', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  function openPopover(panel: ReturnType<typeof createTaskPanel>) {
    ;(panel.root as HTMLButtonElement).click()
    return document.getElementById('task-popover') as HTMLElement
  }

  it('renders no jobs section when there are no jobs', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setTasks([{ id: '1', subject: 'A', status: 'in_progress' }])
    const popover = openPopover(panel)
    expect(popover.querySelector('.jobs-section')).toBeNull()
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(0)
    expect(popover.textContent).not.toContain('Background')
  })

  it('renders the section and one row per job', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setTasks([{ id: '1', subject: 'A', status: 'in_progress' }])
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' }),
      jw({ id: 'agent-1', kind: 'agent', status: 'completed', agentType: 'executor', description: '修 wui 分页 bug' }),
    ])
    const popover = openPopover(panel)
    expect(popover.querySelector('.jobs-section')).not.toBeNull()
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(2)
  })

  it('shows root with running count label when only jobs exist (no tasks)', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'completed', command: 'go build' }),
    ])
    expect(panel.root.style.display).toBe('')
    const label = panel.root.querySelector('.task-label') as SVGTextElement
    expect(label.textContent).toBe('1')
    // Ring progress is task-only: no tasks means an empty ring.
    const ring = panel.root.querySelector('.task-ring') as SVGCircleElement
    const circumference = 2 * Math.PI * 18
    const offset = parseFloat(ring.getAttribute('stroke-dashoffset') ?? 'NaN')
    expect(Math.abs(offset - circumference)).toBeLessThan(0.01)
  })

  it('label falls back to total job count when none are running', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'go build' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'failed', command: 'make lint', exitCode: 2 }),
    ])
    const label = panel.root.querySelector('.task-label') as SVGTextElement
    expect(label.textContent).toBe('2')
  })

  it('rim dot is on while a job runs and off when all jobs are terminal', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'x' })])
    expect(panel.root.querySelector('.rim-dot.on')).not.toBeNull()
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'x' })])
    expect(panel.root.querySelector('.rim-dot.on')).toBeNull()
    expect(panel.root.querySelector('.rim-dot')).not.toBeNull()
  })

  it('bash row shows the command in mono, agent row shows type · description', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' }),
      jw({ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: '修 wui 分页 bug', parentToolUseID: 'p1' }),
    ])
    const popover = openPopover(panel)
    const rows = popover.querySelectorAll('.jobs-row')
    const bashName = rows[0].querySelector('.mono')
    expect(bashName?.textContent).toBe('make check')
    expect(rows[1].textContent).toContain('Executor · 修 wui 分页 bug')
    // Status chip is English mono (existing task panel language).
    expect(rows[0].querySelector('.mono.text-blue')?.textContent).toBe('Running')
  })

  it('fork agent row uses the fork kind label', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'fork', description: 'long task', parentToolUseID: 'p1' }),
    ])
    const popover = openPopover(panel)
    expect(popover.querySelector('.jobs-row')?.textContent).toContain('Fork · long task')
  })

  it('terminal chips: Done / Exit code / Stopped', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'failed', command: 'b', exitCode: 130 }),
      jw({ id: 'agent-1', kind: 'agent', status: 'killed', agentType: 'executor', description: 'c', parentToolUseID: 'p1' }),
    ])
    const popover = openPopover(panel)
    const chips = Array.from(popover.querySelectorAll('.jobs-row')).map((r) => {
      const chip = Array.from(r.querySelectorAll('span')).find((s) => s.className.includes('text-[10px]'))
      return chip?.textContent
    })
    expect(chips).toEqual(['Done', 'Exit 130', 'Stopped'])
  })

  it('terminal rows carry the dimmed opacity class, running rows do not', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'completed', command: 'b' }),
    ])
    const popover = openPopover(panel)
    const rows = popover.querySelectorAll('.jobs-row')
    expect(rows[0].classList.contains('opacity-60')).toBe(false)
    expect(rows[1].classList.contains('opacity-60')).toBe(true)
  })

  it('setJobs keeps an open popover open and refreshes row content', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' })])
    const popover = openPopover(panel)
    expect(popover.querySelector('.jobs-row')?.textContent).toContain('Running')

    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'make check' })])
    // A job finishing must not yank the popover shut.
    const popoverAfter = document.getElementById('task-popover')
    expect(popoverAfter).not.toBeNull()
    expect(popoverAfter?.querySelector('.jobs-row')?.textContent).toContain('Done')
  })

  it('setTasks still unconditionally closes an open popover (jobs present)', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'x' })])
    openPopover(panel)
    panel.setTasks([{ id: '1', subject: 'A', status: 'in_progress' }])
    expect(document.getElementById('task-popover')).toBeNull()
  })

  it('clear-done removes terminal jobs, hides root when nothing remains, and notifies the caller', () => {
    const onClearDone = vi.fn()
    const onStopJob = vi.fn()
    const panel = createTaskPanel({ onClearDone, onStopJob })
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'running', command: 'b' }),
    ])
    const popover = openPopover(panel)
    const clr = popover.querySelector('.clear-done-jobs') as HTMLButtonElement
    clr.click()
    expect(onClearDone).toHaveBeenCalledTimes(1)
    // Terminal job gone, running job stays: root still visible.
    expect(panel.root.style.display).toBe('')
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(1)
    expect(panel.jobsStore.jobs().map((j) => j.id)).toEqual(['bg-2'])

    // All-terminal now: clearing hides the root entirely.
    panel.setJobs([jw({ id: 'bg-2', kind: 'bash', status: 'completed', command: 'b' })])
    // Popover is still open from the first clear (setJobs must not close
    // it) — toggle it closed and reopen to exercise the fresh build.
    ;(panel.root as HTMLButtonElement).click()
    const popover2 = openPopover(panel)
    ;(popover2.querySelector('.clear-done-jobs') as HTMLButtonElement).click()
    expect(panel.root.style.display).toBe('none')
    expect(document.getElementById('task-popover')).toBeNull()
  })

  it('popover title aggregates background jobs', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'running', command: 'b' }),
    ])
    const popover = openPopover(panel)
    const title = popover.querySelector('.px-3.pt-2\\.5') as HTMLElement
    expect(title.textContent).toBe('Background 2 Running')
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'completed', command: 'b' }),
    ])
    expect((popover.querySelector('.px-3.pt-2\\.5') as HTMLElement).textContent).toBe('Background idle')
  })
})
