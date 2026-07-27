import { describe, it, expect, vi } from 'vitest'
import {
  iconButtonRecipe,
  textButtonRecipe,
  toggleGroupRecipe,
  comboButtonRecipe,
  createIconButton,
  createTextButton,
  createToggleGroup,
  createComboButton,
  createFloatButton,
} from './buttons'

// ── iconButtonRecipe ─────────────────────────────────────────

describe('iconButtonRecipe', () => {
  it('variant=default size=sm emits base + default + sm classes', () => {
    expect(iconButtonRecipe({ variant: 'default', size: 'sm' })).toBe(
      'flex items-center justify-center transition-colors text-blue hover:text-white w-7 h-7 rounded-full',
    )
  })

  it('variant=solid size=xs emits solid bg + xs size', () => {
    const out = iconButtonRecipe({ variant: 'solid', size: 'xs' })
    expect(out).toContain('bg-blue text-white hover:bg-blue/80')
    expect(out).toContain('w-4 h-4 rounded-full')
  })

  it('variant=subtle size=lg emits hover-bg + lg size', () => {
    const out = iconButtonRecipe({ variant: 'subtle', size: 'lg' })
    expect(out).toContain('text-blue hover:text-white hover:bg-blue/10')
    expect(out).toContain('w-9 h-9 rounded-lg')
  })

  it('variant=ghost size=md emits ghost + md (no rounded)', () => {
    const out = iconButtonRecipe({ variant: 'ghost', size: 'md' })
    expect(out).toContain('text-t2 hover:text-t1')
    expect(out).toContain('w-10 h-10')
    expect(out).not.toContain('rounded')
  })

  it('variant=floating emits shadow + solid bg', () => {
    const out = iconButtonRecipe({ variant: 'floating' })
    expect(out).toContain('bg-blue text-white shadow-lg hover:bg-blue/80')
  })

  it('class option appends after recipe output', () => {
    const out = iconButtonRecipe({ class: 'absolute foo' })
    expect(out.endsWith('absolute foo')).toBe(true)
    expect(out).toContain('flex items-center justify-center transition-colors')
  })
})

// ── textButtonRecipe ─────────────────────────────────────────

describe('textButtonRecipe', () => {
  it('variant=primary size=sm emits tinted blue + sm padding', () => {
    expect(textButtonRecipe({ variant: 'primary', size: 'sm' })).toBe(
      'transition-colors rounded-lg bg-blue/20 text-blue hover:bg-blue/30 px-3 py-1.5 text-sm',
    )
  })

  it('variant=danger size=sm emits red tint + sm padding', () => {
    const out = textButtonRecipe({ variant: 'danger', size: 'sm' })
    expect(out).toContain('bg-red/10 text-red hover:bg-red/20')
    expect(out).toContain('px-3 py-1.5 text-sm')
  })

  it('variant=link emits transparent bg + text-t2 hover:t1 WITHOUT rounded-lg', () => {
    const out = textButtonRecipe({ variant: 'link' })
    expect(out).toContain('bg-transparent text-t2 hover:text-t1')
    expect(out).not.toContain('rounded-lg')
  })

  it('variant=ghost emits rounded-lg + transparent bg + ink3 hover', () => {
    const out = textButtonRecipe({ variant: 'ghost' })
    expect(out).toContain('rounded-lg bg-transparent text-t2 hover:bg-ink3/50')
  })

  it('size=md emits md padding', () => {
    const out = textButtonRecipe({ variant: 'primary', size: 'md' })
    expect(out).toContain('px-4 py-2 text-base')
  })
})

// ── toggleGroupRecipe & comboButtonRecipe ────────────────────

describe('toggleGroupRecipe', () => {
  it('emits inline-flex + gap', () => {
    expect(toggleGroupRecipe()).toBe('inline-flex items-center gap-1')
  })
})

describe('comboButtonRecipe', () => {
  it('emits flex + gap + transition + cursor-pointer', () => {
    expect(comboButtonRecipe()).toBe('flex items-center gap-1 transition-colors cursor-pointer')
  })
})

// ── createIconButton ─────────────────────────────────────────

describe('createIconButton', () => {
  it('returns HTMLButtonElement', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn).toBeInstanceOf(HTMLButtonElement)
    expect(btn.tagName).toBe('BUTTON')
  })

  it('type=button (form-safe: default would be submit)', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn.type).toBe('button')
  })

  it('aria-label is set from label', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn.getAttribute('aria-label')).toBe('Plus')
  })

  it('icon is rendered as the only svg child', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn.children.length).toBe(1)
    expect(btn.children[0].tagName.toLowerCase()).toBe('svg')
  })

  it('size=sm adds w-7 h-7 rounded-full', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', size: 'sm' })
    expect(btn.className).toContain('w-7 h-7')
    expect(btn.className).toContain('rounded-full')
  })

  it('size=md adds w-10 h-10 without rounded', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', size: 'md' })
    expect(btn.className).toContain('w-10 h-10')
    expect(btn.className).not.toContain('rounded')
  })

  it('size=lg adds w-9 h-9 rounded-lg', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', size: 'lg' })
    expect(btn.className).toContain('w-9 h-9')
    expect(btn.className).toContain('rounded-lg')
    expect(btn.className).not.toContain('rounded-full')
  })

  it('size=xs adds w-4 h-4 rounded-full', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', size: 'xs' })
    expect(btn.className).toContain('w-4 h-4')
    expect(btn.className).toContain('rounded-full')
  })

  it('variant=default emits text-blue hover:text-white', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', variant: 'default' })
    expect(btn.className).toContain('text-blue')
    expect(btn.className).toContain('hover:text-white')
  })

  it('variant=ghost emits text-t2 hover:text-t1', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', variant: 'ghost' })
    expect(btn.className).toContain('text-t2')
    expect(btn.className).toContain('hover:text-t1')
  })

  it('variant=solid emits bg-blue text-white hover:bg-blue/80', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', variant: 'solid' })
    expect(btn.className).toContain('bg-blue')
    expect(btn.className).toContain('text-white')
    expect(btn.className).toContain('hover:bg-blue/80')
  })

  it('variant=subtle emits hover:bg-blue/10', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', variant: 'subtle' })
    expect(btn.className).toContain('hover:bg-blue/10')
  })

  it('variant defaults to default when omitted', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn.className).toContain('text-blue hover:text-white')
  })

  it('size defaults to sm when omitted', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus' })
    expect(btn.className).toContain('w-7 h-7')
  })

  it('iconSize override propagates to svg width/height', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', iconSize: 24 })
    const svg = btn.children[0] as SVGElement
    expect(svg.getAttribute('width')).toBe('24')
    expect(svg.getAttribute('height')).toBe('24')
  })

  it('iconSize defaults: xs=9, sm=18, md=22, lg=20', () => {
    const xs = createIconButton({ icon: 'plus', label: 'Plus', size: 'xs' })
    const sm = createIconButton({ icon: 'plus', label: 'Plus', size: 'sm' })
    const md = createIconButton({ icon: 'plus', label: 'Plus', size: 'md' })
    const lg = createIconButton({ icon: 'plus', label: 'Plus', size: 'lg' })
    expect((xs.children[0] as SVGElement).getAttribute('width')).toBe('9')
    expect((sm.children[0] as SVGElement).getAttribute('width')).toBe('18')
    expect((md.children[0] as SVGElement).getAttribute('width')).toBe('22')
    expect((lg.children[0] as SVGElement).getAttribute('width')).toBe('20')
  })

  it('strokeWidth override propagates to svg stroke-width', () => {
    const btn = createIconButton({ icon: 'refresh', label: 'Retry', iconSize: 9, strokeWidth: 2.5 })
    const svg = btn.children[0] as SVGElement
    expect(svg.getAttribute('stroke-width')).toBe('2.5')
  })

  it('onClick fires on click', () => {
    const spy = vi.fn()
    const btn = createIconButton({ icon: 'plus', label: 'Plus', onClick: spy })
    btn.click()
    expect(spy).toHaveBeenCalledOnce()
  })

  it('onDblClick fires on dblclick', () => {
    const spy = vi.fn()
    const btn = createIconButton({ icon: 'plus', label: 'Plus', onDblClick: spy })
    btn.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    expect(spy).toHaveBeenCalledOnce()
  })

  it('onLongPress fires after touchstart + 500ms', () => {
    vi.useFakeTimers()
    const spy = vi.fn()
    const btn = createIconButton({ icon: 'plus', label: 'Plus', onLongPress: spy })
    btn.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    expect(spy).not.toHaveBeenCalled()
    vi.advanceTimersByTime(500)
    expect(spy).toHaveBeenCalledOnce()
    vi.useRealTimers()
  })

  it('onLongPress swallows the synthesized click so onClick does not double-fire', () => {
    vi.useFakeTimers()
    const longSpy = vi.fn()
    const clickSpy = vi.fn()
    const btn = createIconButton({
      icon: 'plus',
      label: 'Plus',
      onLongPress: longSpy,
      onClick: clickSpy,
    })
    btn.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(500)
    expect(longSpy).toHaveBeenCalledOnce()
    // Browser synthesizes a click after the long-press gesture.
    btn.click()
    expect(clickSpy).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('plain click (no touchstart) fires onClick, not onLongPress', () => {
    vi.useFakeTimers()
    const longSpy = vi.fn()
    const clickSpy = vi.fn()
    const btn = createIconButton({
      icon: 'plus',
      label: 'Plus',
      onLongPress: longSpy,
      onClick: clickSpy,
    })
    btn.click()
    vi.advanceTimersByTime(500)
    expect(clickSpy).toHaveBeenCalledOnce()
    expect(longSpy).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('className is appended after recipe output', () => {
    const btn = createIconButton({ icon: 'plus', label: 'Plus', className: 'absolute foo' })
    expect(btn.className).toContain('absolute')
    expect(btn.className).toContain('foo')
    expect(btn.className).toContain('flex items-center justify-center transition-colors')
  })

  it('onClick + onDblClick + onLongPress all wired independently', () => {
    vi.useFakeTimers()
    const c = vi.fn(), d = vi.fn(), l = vi.fn()
    const btn = createIconButton({
      icon: 'plus', label: 'Plus',
      onClick: c, onDblClick: d, onLongPress: l,
    })
    btn.click()
    expect(c).toHaveBeenCalledOnce()
    btn.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    expect(d).toHaveBeenCalledOnce()
    btn.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(500)
    expect(l).toHaveBeenCalledOnce()
    vi.useRealTimers()
  })
})

// ── createTextButton ─────────────────────────────────────────

describe('createTextButton', () => {
  it('returns HTMLButtonElement', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary' })
    expect(btn).toBeInstanceOf(HTMLButtonElement)
    expect(btn.tagName).toBe('BUTTON')
  })

  it('type=button (form-safe)', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary' })
    expect(btn.type).toBe('button')
  })

  it('textContent matches text option', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary' })
    expect(btn.textContent).toBe('Submit')
  })

  it('empty text is honored (no fallback placeholder)', () => {
    const btn = createTextButton({ text: '', variant: 'link' })
    expect(btn.textContent).toBe('')
    expect(btn.children.length).toBe(0)
  })

  it('aria-label mirrors text', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary' })
    expect(btn.getAttribute('aria-label')).toBe('Submit')
  })

  it('no icon → button has no child elements (just textContent)', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary' })
    expect(btn.children.length).toBe(0)
  })

  it('icon precedes label span when provided', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary', icon: 'plus' })
    expect(btn.children.length).toBe(2)
    expect(btn.children[0].tagName.toLowerCase()).toBe('svg')
    expect(btn.children[1].tagName.toLowerCase()).toBe('span')
    expect(btn.children[1].textContent).toBe('Submit')
  })

  it('iconSize default is 14', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary', icon: 'plus' })
    const svg = btn.children[0] as SVGElement
    expect(svg.getAttribute('width')).toBe('14')
  })

  it('iconSize override propagates', () => {
    const btn = createTextButton({ text: 'Submit', variant: 'primary', icon: 'plus', iconSize: 16 })
    const svg = btn.children[0] as SVGElement
    expect(svg.getAttribute('width')).toBe('16')
  })

  it('variant=primary emits tinted blue + rounded-lg', () => {
    const btn = createTextButton({ text: 'X', variant: 'primary' })
    expect(btn.className).toContain('bg-blue/20 text-blue hover:bg-blue/30')
    expect(btn.className).toContain('rounded-lg')
  })

  it('variant=danger emits red tint', () => {
    const btn = createTextButton({ text: 'X', variant: 'danger' })
    expect(btn.className).toContain('bg-red/10 text-red hover:bg-red/20')
  })

  it('variant=ghost emits rounded-lg + transparent bg + ink3 hover', () => {
    const btn = createTextButton({ text: 'X', variant: 'ghost' })
    expect(btn.className).toContain('rounded-lg bg-transparent text-t2 hover:bg-ink3/50')
  })

  it('variant=link emits transparent bg WITHOUT rounded-lg', () => {
    const btn = createTextButton({ text: 'X', variant: 'link' })
    expect(btn.className).toContain('bg-transparent text-t2 hover:text-t1')
    expect(btn.className).not.toContain('rounded-lg')
  })

  it('size=sm / md emit distinct padding', () => {
    const sm = createTextButton({ text: 'X', variant: 'primary', size: 'sm' })
    const md = createTextButton({ text: 'X', variant: 'primary', size: 'md' })
    expect(sm.className).toContain('px-3 py-1.5 text-sm')
    expect(md.className).toContain('px-4 py-2 text-base')
  })

  it('className is appended after recipe output', () => {
    const btn = createTextButton({ text: 'X', variant: 'link', className: 'mono text-[14px]' })
    expect(btn.className).toContain('mono')
    expect(btn.className).toContain('text-[14px]')
    expect(btn.className).toContain('bg-transparent text-t2 hover:text-t1')
  })

  it('onClick fires on click', () => {
    const spy = vi.fn()
    const btn = createTextButton({ text: 'X', variant: 'primary', onClick: spy })
    btn.click()
    expect(spy).toHaveBeenCalledOnce()
  })

  it('onDblClick fires on dblclick', () => {
    const spy = vi.fn()
    const btn = createTextButton({ text: 'X', variant: 'primary', onDblClick: spy })
    btn.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))
    expect(spy).toHaveBeenCalledOnce()
  })

  it('onLongPress fires after touchstart + 500ms', () => {
    vi.useFakeTimers()
    const spy = vi.fn()
    const btn = createTextButton({ text: 'X', variant: 'primary', onLongPress: spy })
    btn.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(500)
    expect(spy).toHaveBeenCalledOnce()
    vi.useRealTimers()
  })

  it('onLongPress swallows synthesized click', () => {
    vi.useFakeTimers()
    const longSpy = vi.fn()
    const clickSpy = vi.fn()
    const btn = createTextButton({
      text: 'X', variant: 'primary',
      onLongPress: longSpy, onClick: clickSpy,
    })
    btn.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
    vi.advanceTimersByTime(500)
    btn.click()
    expect(longSpy).toHaveBeenCalledOnce()
    expect(clickSpy).not.toHaveBeenCalled()
    vi.useRealTimers()
  })

  it('multi-byte text is set verbatim', () => {
    const btn = createTextButton({ text: '你好', variant: 'primary' })
    expect(btn.textContent).toBe('你好')
  })
})

// ── createToggleGroup ────────────────────────────────────────

describe('createToggleGroup', () => {
  it('returns element + setSelected function', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    expect(handle.element).toBeInstanceOf(HTMLElement)
    expect(typeof handle.setSelected).toBe('function')
  })

  it('element has role=group', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    expect(handle.element.getAttribute('role')).toBe('group')
  })

  it('each item is a button', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    expect(handle.element.querySelectorAll('button').length).toBe(2)
  })

  it('selected item has aria-pressed=true; others false', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    const buttons = handle.element.querySelectorAll('button')
    expect(buttons[0].getAttribute('aria-pressed')).toBe('true')
    expect(buttons[1].getAttribute('aria-pressed')).toBe('false')
  })

  it('click fires onSelect with the item id', () => {
    const spy = vi.fn()
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
      selectedId: 'a',
      onSelect: spy,
    })
    const buttons = handle.element.querySelectorAll('button')
    buttons[1].click()
    expect(spy).toHaveBeenCalledWith('b')
  })

  it('click flips aria-pressed (old=false, new=true)', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    const buttons = handle.element.querySelectorAll('button')
    buttons[1].click()
    expect(buttons[0].getAttribute('aria-pressed')).toBe('false')
    expect(buttons[1].getAttribute('aria-pressed')).toBe('true')
  })

  it('setSelected programmatically flips aria-pressed', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }, { id: 'b', label: 'B' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    handle.setSelected('b')
    const buttons = handle.element.querySelectorAll('button')
    expect(buttons[0].getAttribute('aria-pressed')).toBe('false')
    expect(buttons[1].getAttribute('aria-pressed')).toBe('true')
  })

  it('icon item renders svg inside button (createIconButton path)', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A', icon: 'plus' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    const btn = handle.element.querySelector('button')!
    expect(btn.querySelector('svg')).not.toBeNull()
  })

  it('no-icon item uses textContent (createTextButton path)', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'TextOnly' }],
      selectedId: 'a',
      onSelect: () => {},
    })
    const btn = handle.element.querySelector('button')!
    expect(btn.textContent).toBe('TextOnly')
    expect(btn.querySelector('svg')).toBeNull()
  })

  it('className is applied to the wrap (not individual buttons)', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }],
      selectedId: 'a',
      onSelect: () => {},
      className: 'extra-wrap-class',
    })
    expect(handle.element.className).toContain('extra-wrap-class')
  })

  it('items=[] yields zero buttons (no crash)', () => {
    const handle = createToggleGroup({
      items: [],
      selectedId: 'x',
      onSelect: () => {},
    })
    expect(handle.element.querySelectorAll('button').length).toBe(0)
  })

  it('selectedId not in items leaves all aria-pressed=false', () => {
    const handle = createToggleGroup({
      items: [{ id: 'a', label: 'A' }],
      selectedId: 'missing',
      onSelect: () => {},
    })
    const btn = handle.element.querySelector('button')!
    expect(btn.getAttribute('aria-pressed')).toBe('false')
  })
})

// ── createComboButton ────────────────────────────────────────

describe('createComboButton', () => {
  it('returns wrap + setLabel + open/close/toggle', () => {
    const h = createComboButton({
      label: 'Model',
      onOpen: () => {},
    })
    expect(h.wrap).toBeInstanceOf(HTMLElement)
    expect(typeof h.setLabel).toBe('function')
    expect(typeof h.open).toBe('function')
    expect(typeof h.close).toBe('function')
    expect(typeof h.toggle).toBe('function')
  })

  it('wrap is positioned relative (for popup anchoring)', () => {
    const h = createComboButton({ label: 'Model', onOpen: () => {} })
    expect(h.wrap.className).toContain('relative')
  })

  it('setLabel updates trigger text', () => {
    const h = createComboButton({ label: 'Old', onOpen: () => {} })
    h.setLabel('New')
    const trigger = h.wrap.querySelector('button')!
    expect(trigger.textContent).toBe('New')
  })

  it('className lands on trigger, NOT wrap (header.test selector requirement)', () => {
    const h = createComboButton({
      label: '',
      className: 'mono text-[14px]',
      onOpen: () => {},
    })
    const trigger = h.wrap.querySelector('button')!
    expect(trigger.className).toContain('mono')
    expect(trigger.className).toContain('text-[14px]')
    expect(h.wrap.className).not.toContain('mono')
    expect(h.wrap.className).not.toContain('text-[14px]')
  })

  it('click trigger opens panel and calls onOpen with the panel element', () => {
    document.body.innerHTML = ''
    const spy = vi.fn()
    const h = createComboButton({
      label: 'Model',
      onOpen: spy,
    })
    document.body.appendChild(h.wrap)
    h.wrap.querySelector('button')!.click()
    expect(spy).toHaveBeenCalledOnce()
    const panelArg = spy.mock.calls[0][0] as HTMLElement
    expect(panelArg).toBeInstanceOf(HTMLElement)
    // Panel is appended to document.body on open.
    expect(panelArg.parentElement).toBe(document.body)
    expect(panelArg.classList.contains('hidden')).toBe(false)
    document.body.innerHTML = ''
  })

  it('onClose fires on outside-click after open', () => {
    document.body.innerHTML = ''
    const closeSpy = vi.fn()
    const h = createComboButton({
      label: 'Model',
      onOpen: () => {},
      onClose: closeSpy,
    })
    document.body.appendChild(h.wrap)
    h.wrap.querySelector('button')!.click()
    expect(closeSpy).not.toHaveBeenCalled()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(closeSpy).toHaveBeenCalledOnce()
    document.body.innerHTML = ''
  })

  it('open() / close() / toggle() drive panel visibility', () => {
    document.body.innerHTML = ''
    const h = createComboButton({ label: 'Model', onOpen: () => {} })
    document.body.appendChild(h.wrap)
    h.open()
    // Panel is appended to body on open.
    const panel = document.body.querySelector('.modal-enter') as HTMLElement
    expect(panel).not.toBeNull()
    expect(panel.classList.contains('hidden')).toBe(false)
    h.close()
    expect(panel.classList.contains('hidden')).toBe(true)
    h.toggle()
    expect(panel.classList.contains('hidden')).toBe(false)
    h.toggle()
    expect(panel.classList.contains('hidden')).toBe(true)
    document.body.innerHTML = ''
  })

  it('onClick (when provided) replaces default toggle behavior', () => {
    document.body.innerHTML = ''
    const clickSpy = vi.fn()
    const h = createComboButton({
      label: 'Model',
      onOpen: () => {},
      onClick: clickSpy,
    })
    document.body.appendChild(h.wrap)
    h.wrap.querySelector('button')!.click()
    expect(clickSpy).toHaveBeenCalledOnce()
    // Panel should NOT auto-open because onClick replaced toggle.
    const panel = document.body.querySelector('.modal-enter') as HTMLElement
    expect(panel).toBeNull()
    document.body.innerHTML = ''
  })

  it('second trigger click toggles panel closed', () => {
    document.body.innerHTML = ''
    const h = createComboButton({ label: 'Model', onOpen: () => {} })
    document.body.appendChild(h.wrap)
    const trigger = h.wrap.querySelector('button')!
    trigger.click()
    const panel = document.body.querySelector('.modal-enter') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    trigger.click()
    expect(panel.classList.contains('hidden')).toBe(true)
    document.body.innerHTML = ''
  })

  it('onOpen can populate the panel (caller-side)', () => {
    document.body.innerHTML = ''
    const h = createComboButton({
      label: 'Model',
      onOpen: (panel) => {
        panel.textContent = 'content-here'
      },
    })
    document.body.appendChild(h.wrap)
    h.wrap.querySelector('button')!.click()
    const panel = document.body.querySelector('.modal-enter') as HTMLElement
    expect(panel.textContent).toBe('content-here')
    document.body.innerHTML = ''
  })
})

// ── createFloatButton ────────────────────────────────────────

describe('createFloatButton', () => {
  it('returns a button with floatingButton recipe classes', () => {
    const h = createFloatButton({ position: 'center' })
    expect(h.root.tagName).toBe('BUTTON')
    // floatingButton recipe emits position-specific classes; 'center' sets
    // a center transform that 'right' does not.
    expect(h.root.className).toMatch(/left-/)
  })

  it('position=right differs from position=center', () => {
    const center = createFloatButton({ position: 'center' }).root.className
    const right = createFloatButton({ position: 'right' }).root.className
    expect(center).not.toBe(right)
  })

  it('renders a progress circle with the caller-supplied progressClassName', () => {
    const h = createFloatButton({
      position: 'center',
      progressRing: { progressClassName: 'my-ring' },
    })
    const circle = h.root.querySelector('.my-ring') as SVGCircleElement | null
    expect(circle).not.toBeNull()
    expect(circle!.tagName).toBe('circle')
  })

  it('setProgress updates stroke-dashoffset', () => {
    const h = createFloatButton({
      position: 'center',
      progressRing: { progressClassName: 'ring' },
    })
    const circle = h.root.querySelector('.ring') as SVGCircleElement
    const before = circle.getAttribute('stroke-dashoffset')
    h.setProgress(0.5)
    const after = circle.getAttribute('stroke-dashoffset')
    expect(after).not.toBe(before)
    // Larger progress → smaller offset (ring fills more)
    expect(parseFloat(after!)).toBeLessThan(parseFloat(before!))
  })

  it('setProgress is a no-op when no progressRing configured', () => {
    const h = createFloatButton({ position: 'center' })
    expect(() => h.setProgress(0.5)).not.toThrow()
  })

  it('setLabel updates the <text> element content', () => {
    const h = createFloatButton({
      position: 'right',
      labelClassName: 'task-label',
      innerLabel: '0/0',
    })
    const text = h.root.querySelector('.task-label') as SVGTextElement
    expect(text.textContent).toBe('0/0')
    h.setLabel('3/5')
    expect(text.textContent).toBe('3/5')
  })

  it('creates an empty <text> target when innerLabel is omitted', () => {
    const h = createFloatButton({ position: 'right' })
    const text = h.root.querySelector('.float-label') as SVGTextElement
    expect(text).not.toBeNull()
    expect(text.textContent).toBe('')
    h.setLabel('hi')
    expect(text.textContent).toBe('hi')
  })

  it('labelClassName defaults to float-label', () => {
    const h = createFloatButton({ position: 'right', innerLabel: 'x' })
    expect(h.root.querySelector('.float-label')).not.toBeNull()
  })

  it('escapes XML-special characters in innerLabel', () => {
    const h = createFloatButton({
      position: 'right',
      innerLabel: '<script>',
    })
    const text = h.root.querySelector('.float-label') as SVGTextElement
    // innerHTML contains the escaped form; textContent is the unescaped value
    expect(text.textContent).toBe('<script>')
    expect(h.root.innerHTML).not.toContain('<script>')
  })

  it('renders innerIcon via renderIcon (single source of truth)', () => {
    const h = createFloatButton({
      position: 'center',
      innerIcon: 'scroll-to-bottom',
    })
    // innerIcon is embedded as a nested <svg> (renderIcon output), wrapped
    // in a <g transform="..."> for centering.
    const innerSvg = h.root.querySelector('svg svg') as SVGElement | null
    expect(innerSvg).not.toBeNull()
    expect(innerSvg!.getAttribute('width')).toBe('28')
  })

  it('onClick fires when root is clicked', () => {
    const spy = vi.fn()
    const h = createFloatButton({ position: 'center', onClick: spy })
    h.root.click()
    expect(spy).toHaveBeenCalledTimes(1)
  })
})

// ── createIconButton setIcon (2-state toggle) ────────────────

describe('createIconButton — setIcon callback', () => {
  it('setIcon swaps the rendered icon inside the button', () => {
    let triggerSetIcon: ((icon: 'copy' | 'check') => void) | null = null
    const btn = createIconButton({
      icon: 'copy',
      label: 'Copy',
      onClick: (_e, setIcon) => {
        triggerSetIcon = setIcon
      },
    })
    // Before click: svg contains the copy icon's <rect>
    expect(btn.querySelector('rect')).not.toBeNull()
    btn.click()
    expect(triggerSetIcon).not.toBeNull()
    triggerSetIcon!('check')
    // check icon is a polyline; rect from copy is gone
    expect(btn.querySelector('polyline')).not.toBeNull()
    expect(btn.querySelector('rect')).toBeNull()
  })

  it('setIcon reuses the original iconSize and strokeWidth', () => {
    let setIcon: (icon: 'copy' | 'check') => void = () => {}
    const btn = createIconButton({
      icon: 'copy',
      label: 'Copy',
      iconSize: 14,
      strokeWidth: 3,
      onClick: (_e, fn) => { setIcon = fn },
    })
    btn.click()
    setIcon('check')
    const svg = btn.querySelector('svg') as SVGElement
    expect(svg.getAttribute('width')).toBe('14')
    expect(svg.getAttribute('stroke-width')).toBe('3')
  })

  it('omitting the second argument is backward-compatible', () => {
    // Callers that predate setIcon pass onClick: (e) => void and must
    // continue to work without TypeScript or runtime errors.
    const spy = vi.fn()
    const btn = createIconButton({
      icon: 'plus',
      label: 'Plus',
      onClick: (e) => spy(e),
    })
    btn.click()
    expect(spy).toHaveBeenCalledTimes(1)
  })
})
