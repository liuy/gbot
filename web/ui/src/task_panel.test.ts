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

  it('keeps the exact fraction at 99 tasks and degrades at 100', () => {
    const make = (n: number, done: number): TaskWireItem[] =>
      Array.from({ length: n }, (_, i) => ({
        id: String(i),
        subject: `T${i}`,
        status: i < done ? 'completed' : 'pending',
      }))
    // tasksActive requires a not-yet-done task; all-complete lists take the
    // hidden branch and never reach the label.
    const at99 = mount()
    at99.setTasks(make(99, 98))
    expect((at99.root.querySelector('.task-label') as SVGTextElement).textContent).toBe('98/99')
    const at100 = mount()
    at100.setTasks(make(100, 99))
    expect((at100.root.querySelector('.task-label') as SVGTextElement).textContent).toBe('99+')
  })

  it('degrades to 99+ past a hundred tasks while the ring keeps the exact ratio', () => {
    const panel = mount()
    const tasks: TaskWireItem[] = Array.from({ length: 120 }, (_, i) => ({
      id: String(i),
      subject: `T${i}`,
      status: i < 60 ? 'completed' : 'pending',
    }))
    panel.setTasks(tasks)
    const label = panel.root.querySelector('.task-label') as SVGTextElement
    expect(label.textContent).toBe('99+')
    const ring = panel.root.querySelector('.task-ring') as SVGCircleElement
    const circumference = 2 * Math.PI * 18
    const offset = parseFloat(ring.getAttribute('stroke-dashoffset') ?? 'NaN')
    expect(Math.abs(offset - circumference * 0.5)).toBeLessThan(0.01)
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

  it('hides root when only terminal jobs remain (no tasks)', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'go build' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'failed', command: 'make lint', exitCode: 2 }),
    ])
    expect(panel.root.style.display).toBe('none')
  })

  it('running job keeps the root; all-terminal hides it', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'x' })])
    expect(panel.root.style.display).toBe('')
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'x' })])
    // Terminal jobs no longer keep the trigger alive while the panel is shut.
    expect(panel.root.style.display).toBe('none')
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

  it('terminal chips: Running / Done / Exit code / Stopped', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([
      jw({ id: 'bg-0', kind: 'bash', status: 'running', command: 'z' }),
      jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'a' }),
      jw({ id: 'bg-2', kind: 'bash', status: 'failed', command: 'b', exitCode: 130 }),
      jw({ id: 'agent-1', kind: 'agent', status: 'killed', agentType: 'executor', description: 'c', parentToolUseID: 'p1' }),
    ])
    const popover = openPopover(panel)
    const chips = Array.from(popover.querySelectorAll('.jobs-row')).map((r) => {
      const chip = Array.from(r.querySelectorAll('span')).find((s) => s.className.includes('text-[10px]'))
      return chip?.textContent
    })
    expect(chips).toEqual(['Running', 'Done', 'Exit 130', 'Stopped'])
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

  it('all jobs terminal keeps an open panel readable; closing packs up the trigger', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'x' })])
    const popover = openPopover(panel)

    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'x' })])
    // Retention: rows must not be yanked while the user reads them, and the
    // popover's anchor (the trigger) stays put.
    expect(document.getElementById('task-popover')).toBe(popover)
    expect(panel.root.style.display).toBe('')
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(1)
    expect(popover.querySelector('.jobs-section')).not.toBeNull()

    // Close ends retention: nothing left to show hides the trigger.
    ;(panel.root as HTMLButtonElement).click()
    expect(document.getElementById('task-popover')).toBeNull()
    expect(panel.root.style.display).toBe('none')

    // The sticky latch must have reset with the close: reopening (via a
    // pending task, the only remaining trigger) shows no jobs section —
    // terminal rows packed up with the previous session of the panel.
    panel.setTasks([{ id: '1', subject: 'A', status: 'in_progress' }])
    ;(panel.root as HTMLButtonElement).click()
    const reopened = document.getElementById('task-popover')
    expect(reopened).not.toBeNull()
    expect(reopened?.querySelectorAll('.jobs-row')).toHaveLength(0)
    expect(reopened?.querySelector('.jobs-section')).toBeNull()
    ;(panel.root as HTMLButtonElement).click()

    // A new running job revives the trigger from the hidden state.
    panel.setJobs([jw({ id: 'bg-2', kind: 'bash', status: 'running', command: 'y' })])
    expect(panel.root.style.display).toBe('')
  })

  it('a fresh open with only terminal jobs renders no jobs section', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setTasks([{ id: '1', subject: 'A', status: 'in_progress' }])
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'go build' })])
    const popover = openPopover(panel)
    expect(popover.querySelector('.jobs-section')).toBeNull()
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(0)
    expect(popover.textContent).not.toContain('Background')
  })

  it('popover title aggregates background jobs only while they run', () => {
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
      jw({ id: 'bg-2', kind: 'bash', status: 'failed', command: 'b' }),
    ])
    // Zero running: the aggregate goes fully silent — no idle line, no title
    // row. The rows themselves stay until the panel closes (retention).
    expect(popover.querySelector('.px-3.pt-2\\.5')).toBeNull()
    expect(popover.querySelectorAll('.jobs-row')).toHaveLength(2)
  })
})

const nextFrame = () => new Promise<void>((r) => requestAnimationFrame(() => r()))

describe('taskPanel — job expansion (Phase 3)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  function openPopover(panel: ReturnType<typeof createTaskPanel>) {
    ;(panel.root as HTMLButtonElement).click()
    return document.getElementById('task-popover') as HTMLElement
  }

  function expandFirstRow(popover: HTMLElement) {
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()
  }

  it('expansion survives status-change rebuilds and hides the terminal stop button', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' })])
    const popover = openPopover(panel)
    expandFirstRow(popover)
    const feed1 = popover.querySelector('.jobs-feed') as HTMLElement
    expect(popover.querySelector('.jobs-stop')).not.toBeNull()

    // Running → completed flips the row but must not collapse the reading
    // nor recreate the feed (scroll position lives on that node). Retention
    // keeps the row until the panel closes.
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'completed', command: 'make check' })])
    expect(document.getElementById('task-popover')).toBe(popover)
    expect(popover.querySelector('.jobs-exp')).not.toBeNull()
    expect(popover.querySelector('.jobs-feed')).toBe(feed1)
    expect(popover.querySelector('.jobs-stop')).toBeNull()
  })

  it('expansion and the carried feed element persist across close/reopen while the job runs', () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' })])
    const popover = openPopover(panel)
    expandFirstRow(popover)
    const feed1 = popover.querySelector('.jobs-feed') as HTMLElement

    ;(panel.root as HTMLButtonElement).click()
    const popover2 = openPopover(panel)
    expect(popover2.querySelector('.jobs-exp')).not.toBeNull()
    expect(popover2.querySelector('.jobs-feed')).toBe(feed1)
  })

  it('tail-only updates refresh the feed in place without rebuilding rows', async () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'make check' })])
    const popover = openPopover(panel)
    expandFirstRow(popover)
    const row1 = popover.querySelector('.jobs-row') as HTMLElement
    const feed1 = popover.querySelector('.jobs-feed') as HTMLElement

    panel.jobsStore.applyOutput('bg-1', 'ok  pkg/engine  1.2s\n')
    await nextFrame()
    // Same row and feed nodes — a 2Hz tail frame must not swap the DOM the
    // user is looking at (hover/selection/scroll all live on these nodes).
    expect(popover.querySelector('.jobs-row')).toBe(row1)
    expect(popover.querySelector('.jobs-feed')).toBe(feed1)
    expect(popover.querySelector('.jobs-exp')).not.toBeNull()
    expect(feed1.textContent).toContain('ok  pkg/engine  1.2s')
  })

  it('stop button routes to the onStopJob option', () => {
    const onStopJob = vi.fn()
    const panel = createTaskPanel({ onStopJob })
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'bg-1', kind: 'bash', status: 'running', command: 'x' })])
    const popover = openPopover(panel)
    expandFirstRow(popover)
    ;(popover.querySelector('.jobs-stop') as HTMLButtonElement).click()
    expect(onStopJob).toHaveBeenCalledTimes(1)
    expect(onStopJob).toHaveBeenCalledWith('bg-1')
    // Stop is not a toggle: the row stays expanded.
    expect(popover.querySelector('.jobs-exp')).not.toBeNull()
  })
})

describe('taskPanel — job feed catch-up (DOM sink)', () => {
  beforeEach(() => {
    document.body.innerHTML = ''
  })

  function openPopover(panel: ReturnType<typeof createTaskPanel>) {
    ;(panel.root as HTMLButtonElement).click()
    return document.getElementById('task-popover') as HTMLElement
  }

  it('events streamed while the popover is closed appear when it reopens', async () => {
    const panel = createTaskPanel()
    document.body.appendChild(panel.root)
    panel.setJobs([jw({ id: 'agent-1', kind: 'agent', status: 'running', agentType: 'Executor', description: 'work', parentToolUseID: 'p1' })])
    const popover = openPopover(panel)
    ;(popover.querySelector('.jobs-row') as HTMLElement).click()
    const feed1 = popover.querySelector('.jobs-feed') as HTMLElement
    expect(feed1.children.length).toBe(0)

    // Close: the popover detaches; stream deltas land with no connected feed.
    ;(panel.root as HTMLButtonElement).click()
    panel.jobsStore.applyEvent({
      type: 'tool_start',
      tool_use: { id: 't-1', name: 'Read', input: {} },
      agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    })
    panel.jobsStore.applyEvent({
      type: 'text_delta',
      text: 'ran while closed',
      agent: { parent_tool_use_id: 'p1', agent_type: 'Executor', depth: 1 },
    })
    await nextFrame()

    const reopened = openPopover(panel)
    expect(reopened.querySelector('.jobs-exp')).not.toBeNull()
    const feed2 = reopened.querySelector('.jobs-feed') as HTMLElement
    expect(feed2.textContent).toContain('ran while closed')
    expect(feed2.querySelectorAll('[data-tool-root]')).toHaveLength(1)
  })
})
