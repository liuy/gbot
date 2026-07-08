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

  it('shows summary counts and progress ring with correct offset', () => {
    const panel = mount()
    const tasks: TaskWireItem[] = [
      { id: '1', subject: 'A', status: 'completed' },
      { id: '2', subject: 'B', status: 'in_progress' },
      { id: '3', subject: 'C', status: 'pending' },
    ]
    panel.setTasks(tasks)

    expect(panel.root.style.display).toBe('')
    const text = panel.root.textContent ?? ''
    expect(text).toContain('1/3 Done')
    expect(text).toContain('1 Running')
    expect(text).toContain('1 Pending')

    const ring = panel.root.querySelector('.task-ring') as SVGCircleElement
    expect(ring).toBeTruthy()
    const circumference = 2 * Math.PI * 6
    const expectedOffset = circumference * (1 - 1 / 3)
    const actual = parseFloat(ring.getAttribute('stroke-dashoffset') ?? 'NaN')
    expect(Math.abs(actual - expectedOffset)).toBeLessThan(0.01)
  })

  it('hides panel when all tasks are completed', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Only', status: 'completed' }])
    expect(panel.root.style.display).toBe('none')
  })

  it('expand/collapse toggles list visibility and chevron rotation', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Task', status: 'pending' }])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    const list = panel.root.children[1] as HTMLElement

    expect(list.classList.contains('hidden')).toBe(true)
    header.click()
    expect(list.classList.contains('hidden')).toBe(false)
    header.click()
    expect(list.classList.contains('hidden')).toBe(true)
  })

  it('expanded state persists across setTasks', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'A', status: 'pending' }])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    const list = panel.root.children[1] as HTMLElement
    header.click()
    expect(list.classList.contains('hidden')).toBe(false)

    panel.setTasks([
      { id: '1', subject: 'A', status: 'completed' },
      { id: '2', subject: 'B', status: 'pending' },
    ])
    expect(list.classList.contains('hidden')).toBe(false)
    expect(panel.root.textContent).toContain('2')
  })

  it('renders completed checkmark with line-through', () => {
    const panel = mount()
    panel.setTasks([
      { id: '1', subject: 'Done thing', status: 'completed' },
      { id: '2', subject: 'Pending thing', status: 'pending' },
    ])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    header.click()
    const list = panel.root.children[1] as HTMLElement
    const text = list.textContent ?? ''
    expect(text).toContain('Done thing')
    const lineThrough = list.querySelector('.line-through')
    expect(lineThrough).toBeTruthy()
    expect(lineThrough?.textContent).toBe('Done thing')
    const checkSvg = list.querySelector('svg path')
    expect(checkSvg).toBeTruthy()
    expect(checkSvg?.getAttribute('d')).toContain('M20 6L9 17l-5-5')
  })

  it('renders in_progress spinner and running label', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Active task', status: 'in_progress' }])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    header.click()
    const list = panel.root.children[1] as HTMLElement
    const text = list.textContent ?? ''
    expect(text).toContain('Running')
    expect(text).toContain('Active task')

    const spinner = list.querySelector('.spin')
    expect(spinner).not.toBeNull()
    expect(spinner?.tagName.toLowerCase()).toBe('svg')
    expect(spinner?.outerHTML).toContain('M21 12a9 9 0 11-6.219-8.56')
  })

  it('renders pending with blockedBy subjects', () => {
    const panel = mount()
    panel.setTasks([
      { id: '1', subject: 'Blocker task', status: 'in_progress' },
      { id: '2', subject: 'Waiter', status: 'pending', blockedBy: ['Blocker task'] },
    ])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    header.click()
    const list = panel.root.children[1] as HTMLElement
    const text = list.textContent ?? ''
    expect(text).toContain('Blocked by Blocker task')
    expect(text).toContain('Waiter')
  })

  it('renders pending without blockedBy element when none provided', () => {
    const panel = mount()
    panel.setTasks([{ id: '1', subject: 'Free task', status: 'pending' }])
    const header = panel.root.querySelector('button') as HTMLButtonElement
    header.click()
    const text = panel.root.textContent ?? ''
    expect(text).not.toContain('Blocked by')
  })
})
