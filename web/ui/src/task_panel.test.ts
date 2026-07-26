import { describe, it, expect, beforeEach } from 'vitest'
import { createTaskPanel } from './task_panel'
import type { TaskWireItem } from './types'

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
