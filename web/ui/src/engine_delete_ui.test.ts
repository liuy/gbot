import { describe, it, expect, beforeEach, vi } from 'vitest'
import { createChat } from './chat'
import { setLocale } from './i18n'

class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() {
    return []
  }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver)

type Listener = (msg: unknown) => void
const listeners: Set<Listener> = new Set()
const sent: unknown[] = []
vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => sent.push(p),
    connected: true,
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

const ENGINES = [
  { id: 'main', name: 'Main', model: 'm1' },
  { id: 'e2', name: 'Second', model: 'm2' },
]

function metadata() {
  dispatch({
    type: 'metadata',
    connect: { connected: true, engineID: 'main' },
    config: { models: [], current: { provider: 'p', model: 'm1' } },
    engines: { engines: ENGINES, activeID: 'main' },
    stats: { usage: { input_tokens: 0, output_tokens: 0 } },
  })
}

function mount() {
  document.body.innerHTML = ''
  setLocale('en')
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  dispatch({ type: 'connect_status', connected: true })
  metadata()
  return chat
}

function openPanel(): HTMLElement {
  const trigger = document.body.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
  trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
  for (const p of panels) {
    if (!p.classList.contains('hidden')) return p as HTMLElement
  }
  throw new Error('engine picker panel not open')
}

function enableDelMode(panel: HTMLElement): void {
  const btn = panel.querySelector('button[aria-label="Delete engines"]') as HTMLButtonElement
  if (!btn) throw new Error('delete-mode toggle not found')
  btn.dispatchEvent(new MouseEvent('click', { bubbles: true }))
}

describe('engine delete chat wiring', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
  })

  it('no delete affordances until the − toggle arms delete mode', () => {
    mount()
    const panel = openPanel()
    expect(panel.querySelector('[data-engine-delete]')).toBeNull()
    enableDelMode(panel)
    expect(panel.querySelector('[data-engine-delete="e2"]')).not.toBeNull()
    // The main engine never carries a trash, even in delete mode.
    expect(panel.querySelector('[data-engine-delete="main"]')).toBeNull()
    const toggle = panel.querySelector('button[aria-label="Delete engines"]') as HTMLButtonElement
    toggle.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(panel.querySelector('[data-engine-delete]')).toBeNull()
  })

  it('two-tap delete sends engine_delete for the row engine', () => {
    vi.useFakeTimers()
    try {
      mount()
      const panel = openPanel()
      enableDelMode(panel)
      const trash = panel.querySelector('[data-engine-delete="e2"]') as HTMLElement
      expect(trash).not.toBeNull()
      trash.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(sent).not.toContainEqual({ type: 'engine_delete', engineID: 'e2' })
      trash.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(sent).toContainEqual({ type: 'engine_delete', engineID: 'e2' })
    } finally {
      vi.useRealTimers()
    }
  })

  it('a scoped engine_delete error toasts and restores the collapsed row', () => {
    vi.useFakeTimers()
    try {
      mount()
      const panel = openPanel()
      enableDelMode(panel)
      const trash = panel.querySelector('[data-engine-delete="e2"]') as HTMLElement
      trash.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      trash.dispatchEvent(new MouseEvent('click', { bubbles: true }))
      vi.advanceTimersByTime(400)
      // The optimistic collapse removed the row; the server rejected the
      // delete (scoped error), so the row must come back.
      expect(panel.querySelector('[data-engine-id="e2"]')).toBeNull()
      dispatch({ type: 'error', message: 'must keep at least one engine', scope: 'engine_delete' })
      expect(panel.querySelector('[data-engine-id="e2"]')).not.toBeNull()
      const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
      expect(capsule.textContent).toContain('must keep at least one engine')
      expect(capsule.textContent).toContain('Delete failed')
      // Auto-dismiss: the notice is transient, not sticky.
      vi.advanceTimersByTime(4100)
      expect(capsule.classList.contains('opacity-0')).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })
})
