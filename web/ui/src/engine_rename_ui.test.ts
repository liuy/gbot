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

function engineTrigger(): HTMLButtonElement {
  return document.body.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
}

function openPanel(): HTMLElement {
  engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
  const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
  for (const p of panels) {
    if (!p.classList.contains('hidden')) return p as HTMLElement
  }
  throw new Error('engine picker panel not open')
}

function renameBtn(panel: HTMLElement): HTMLButtonElement {
  const btn = panel.querySelector('button[aria-label="Rename engine"]') as HTMLButtonElement
  if (!btn) throw new Error('rename button not found')
  return btn
}

function armRename(panel: HTMLElement): HTMLInputElement {
  renameBtn(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  const input = panel.querySelector('[data-engine-rename]') as HTMLInputElement
  if (!input) throw new Error('rename input not found')
  return input
}

function key(el: Element, k: string) {
  const ev = new KeyboardEvent('keydown', { key: k, bubbles: true, cancelable: true })
  el.dispatchEvent(ev)
  return ev
}

const renameSent = () => sent.filter((m) => (m as { type?: string }).type === 'engine_rename')

describe('engine rename chat wiring', () => {
  beforeEach(() => {
    listeners.clear()
    sent.length = 0
  })

  it('footer carries the three-tool toolbar (new, rename, delete-mode)', () => {
    mount()
    const panel = openPanel()
    for (const label of ['New engine', 'Rename engine', 'Delete engines']) {
      const btn = panel.querySelector(`button[aria-label="${label}"]`)
      expect(btn, `missing footer tool: ${label}`).not.toBeNull()
    }
    // Layout C: the three tools cluster at the right edge — footer uses
    // justify-end with no distribution spacers, tools adjacent (gap-0.5).
    const footer = renameBtn(panel).closest('div') as HTMLElement
    const spacers = [...footer.children].filter((c) => (c as HTMLElement).classList.contains('flex-1'))
    expect(spacers.length).toBe(0)
    expect(footer.className).toContain('justify-end')
    // Bare-tool styling: padding + hover wash, no full-width button chrome.
    expect(renameBtn(panel).className).toContain('p-1.5 rounded-md hover:bg-ink3/50')
    expect(renameBtn(panel).className).not.toContain('w-full')
    // Optical size: the pencil glyph fills its whole viewBox (coords 2..22)
    // while +/− span 5..19, so it renders one step smaller to match.
    expect(renameBtn(panel).querySelector('svg')!.getAttribute('width')).toBe('14')
  })

  it('pencil turns the active row name into a prefilled, selected input', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    expect(input.value).toBe('Main')
    expect(document.activeElement).toBe(input)
    expect(input.selectionStart).toBe(0)
    expect(input.selectionEnd).toBe('Main'.length)
    // Prototype styling contract for the edit affordance.
    expect(input.className).toBe(
      'text-[13px] bg-ink3/60 border border-hairline rounded-md px-1.5 py-0.5 text-t1 outline-none',
    )
    expect(input.style.width).toBe('120px')
    expect(input.spellcheck).toBe(false)
    expect(input.getAttribute('aria-label')).toBe('Rename engine')
    // The edited row is no longer a switch target.
    expect(panel.querySelector('[data-engine-id="main"]').tagName).toBe('DIV')
    expect(panel.querySelector('[data-engine-id="e2"]').tagName).toBe('BUTTON')
  })

  it('stray keys are inert: only Enter submits and only Escape cancels', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'Draft'
    key(input, 'x')
    expect(renameSent()).toEqual([])
    // The input survives a stray keystroke — an over-eager cancel branch
    // would have torn the edit down on any non-Enter key.
    expect(panel.querySelector('[data-engine-rename]')).not.toBeNull()
    const enter = key(input, 'Enter')
    expect(enter.defaultPrevented).toBe(true)
    expect(renameSent()).toEqual([{ type: 'engine_rename', engineID: 'main', name: 'Draft' }])
  })

  it('a second pencil tap preserves the half-typed text instead of resetting it', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'half'
    renameBtn(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const again = panel.querySelector('[data-engine-rename]') as HTMLInputElement
    expect(again).not.toBeNull()
    expect(again.value).toBe('half')
    expect(document.activeElement).toBe(again)
  })

  it('clicking the editing row neither switches engines nor closes the panel', () => {
    mount()
    const panel = openPanel()
    armRename(panel)
    const row = panel.querySelector('[data-engine-id="main"]') as HTMLElement
    row.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const switches = sent.filter((m) => (m as { type?: string }).type === 'engine_switch')
    expect(switches).toEqual([])
    expect(panel.classList.contains('hidden')).toBe(false)
    expect(panel.querySelector('[data-engine-rename]')).not.toBeNull()
  })

  it('Enter sends engine_rename for the active engine (trimmed)', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = '  Primary  '
    key(input, 'Enter')
    expect(renameSent()).toEqual([{ type: 'engine_rename', engineID: 'main', name: 'Primary' }])
    expect(panel.querySelector('[data-engine-rename]')).toBeNull()
  })

  it('Enter on a whitespace-only name cancels without sending', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = '   '
    key(input, 'Enter')
    expect(renameSent()).toEqual([])
    expect(panel.querySelector('[data-engine-rename]')).toBeNull()
  })

  it('Escape cancels without sending', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    key(input, 'Escape')
    expect(renameSent()).toEqual([])
    expect(panel.querySelector('[data-engine-rename]')).toBeNull()
  })

  it('blur submits the trimmed value once', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'Draft'
    input.dispatchEvent(new FocusEvent('blur'))
    // A second blur (jsdom does not fire one on remove; browsers may lag one)
    // must not double-send — the finalizer is one-shot.
    input.dispatchEvent(new FocusEvent('blur'))
    expect(renameSent()).toEqual([{ type: 'engine_rename', engineID: 'main', name: 'Draft' }])
    expect(panel.querySelector('[data-engine-rename]')).toBeNull()
  })

  it('closing the popup mid-edit cancels without submitting half-done text', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'half-typed'
    // Combo toggle: clicking the engine trigger again closes the popover.
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(renameSent()).toEqual([])
    // Reopening must not resurrect the editing state.
    const reopened = openPanel()
    expect(reopened.querySelector('[data-engine-rename]')).toBeNull()
  })

  it('a fresh engine_list re-renders the row and syncs the header combo label', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'Primary'
    key(input, 'Enter')
    dispatch({
      type: 'engine_list',
      engines: [
        { id: 'main', name: 'Primary', model: 'm1' },
        { id: 'e2', name: 'Second', model: 'm2' },
      ],
      activeID: 'main',
    })
    const row = panel.querySelector('[data-engine-id="main"]') as HTMLElement
    expect(row.textContent).toContain('Primary')
    expect(engineTrigger().textContent).toContain('Primary')
  })

  it('Enter during IME composition does not submit (isComposing)', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = '半截'
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Enter', bubbles: true, cancelable: true, isComposing: true }),
    )
    // The Enter that confirms a candidate belongs to the composition, not
    // the dialog: nothing is submitted and the edit stays armed.
    expect(renameSent()).toEqual([])
    expect(panel.querySelector('[data-engine-rename]')).not.toBeNull()
  })

  it('Escape during IME composition does not cancel (isComposing)', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = '半截'
    input.dispatchEvent(
      new KeyboardEvent('keydown', { key: 'Escape', bubbles: true, cancelable: true, isComposing: true }),
    )
    expect(panel.querySelector('[data-engine-rename]')).not.toBeNull()
  })

  it('an engine_list push mid-edit preserves the half-typed value, focus, and row order', () => {
    mount()
    const panel = openPanel()
    // Edit the SECOND engine so the rebuild must also re-insert rows both
    // above and below the preserved edit row.
    dispatch({ type: 'engine_list', engines: ENGINES, activeID: 'e2' })
    renameBtn(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const input = panel.querySelector('[data-engine-rename]') as HTMLInputElement
    input.value = 'Second draft'
    // e3 rides AFTER the edited engine: rows behind the edit must append,
    // not stack in front of it, and the footer toolbar must survive the
    // preserve-path render.
    dispatch({
      type: 'engine_list',
      engines: [
        { id: 'main', name: 'Main', model: 'm1' },
        { id: 'e2', name: 'Second', model: 'm2' },
        { id: 'e3', name: 'Third', model: 'm3' },
      ],
      activeID: 'e2',
    })
    const live = panel.querySelector('[data-engine-rename]') as HTMLInputElement
    // Same node — the armed edit row is re-positioned, not rebuilt from
    // entry.name (which would clobber 'Second draft' back to 'Second').
    expect(live).toBe(input)
    expect(live.value).toBe('Second draft')
    expect(document.activeElement).toBe(live)
    expect(renameSent()).toEqual([])
    const ids = [...panel.querySelectorAll('[data-engine-id]')].map(
      (el) => (el as HTMLElement).dataset.engineId,
    )
    expect(ids).toEqual(['main', 'e2', 'e3'])
    expect(panel.querySelector('button[aria-label="Rename engine"]')).not.toBeNull()
  })

  it('the editing row keeps plain-row chrome: classes, active dot, model echo, del-mode trash', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    const row = input.closest('[data-engine-id]') as HTMLElement
    expect(row.className).toBe(
      'w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50',
    )
    // The edited engine is the active one: blue dot, not the idle grey.
    const dot = row.children[0] as HTMLElement
    expect(dot.className).toBe('h-2 w-2 rounded-full shrink-0 bg-blue')
    const model = [...row.querySelectorAll('span')].find((s) => s.textContent === 'm1') as HTMLElement
    // A missing model echo fails on the deref, an altered one on the class.
    expect(model.className).toBe('text-[13px] text-t3 ml-2')
    // main is not deletable, so its edit row stays trash-free even with the
    // mode on.
    const delToggle = panel.querySelector('button[aria-label="Delete engines"]') as HTMLButtonElement
    delToggle.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(row.querySelector('[data-engine-delete="main"]')).toBeNull()
    // A deletable engine's edit row does carry the trash: arm the edit on
    // e2 (now active) while the mode is still on.
    key(input, 'Escape')
    dispatch({ type: 'engine_list', engines: ENGINES, activeID: 'e2' })
    renameBtn(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const e2row = panel.querySelector('[data-engine-id="e2"]') as HTMLElement
    expect(e2row.querySelector('[data-engine-delete="e2"]')).not.toBeNull()
  })

  it('an engine_list push that drops the edited engine disarms the edit', () => {
    mount()
    const panel = openPanel()
    dispatch({ type: 'engine_list', engines: ENGINES, activeID: 'e2' })
    renameBtn(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const input = panel.querySelector('[data-engine-rename]') as HTMLInputElement
    dispatch({
      type: 'engine_list',
      engines: [{ id: 'main', name: 'Main', model: 'm1' }],
      activeID: 'main',
    })
    expect(panel.querySelector('[data-engine-rename]')).toBeNull()
    // Disarmed: a late blur from the dropped input must not rename a dead
    // engine (the server would reject it with a toast).
    input.dispatchEvent(new FocusEvent('blur'))
    expect(renameSent()).toEqual([])
  })

  it('blur finishing the edit swaps only the edited row (pending clicks on siblings land)', () => {
    mount()
    const panel = openPanel()
    const input = armRename(panel)
    input.value = 'Draft'
    const e2row = panel.querySelector('[data-engine-id="e2"]') as HTMLElement
    input.dispatchEvent(new FocusEvent('blur'))
    expect(renameSent()).toEqual([{ type: 'engine_rename', engineID: 'main', name: 'Draft' }])
    // Mousedown on a sibling row blurs the input first; a whole-list
    // rebuild would yank the node under that pending click. Only the
    // edited row is swapped — the sibling keeps its DOM node.
    expect(panel.querySelector('[data-engine-id="e2"]')).toBe(e2row)
    const edited = panel.querySelector('[data-engine-id="main"]') as HTMLElement
    expect(edited.tagName).toBe('BUTTON')
    // Un-optimistic by design (chat.ts scoped-error contract): the row
    // shows the old name until the receipt's engine_list lands.
    expect(edited.textContent).toContain('Main')
  })

  it('a scoped engine_rename error toasts without touching the chat stream', () => {
    mount()
    dispatch({ type: 'error', message: 'engine name must not be empty', scope: 'engine_rename' })
    const capsule = document.querySelector('[data-status-capsule]') as HTMLElement
    expect(capsule.textContent).toContain('engine name must not be empty')
    expect(capsule.textContent).toContain('Rename failed')
  })
})
