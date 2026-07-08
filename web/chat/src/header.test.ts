import { describe, it, expect } from 'vitest'
import { createHeader } from './header'

function findHamburger(root: HTMLElement): HTMLElement | null {
  const svgs = root.querySelectorAll('svg')
  for (const svg of Array.from(svgs)) {
    const rects = svg.querySelectorAll('rect')
    if (rects.length === 2) return svg.closest('button')
  }
  return null
}

describe('createHeader', () => {
  it('builds sticky header with glass', () => {
    const { root } = createHeader()
    expect(root.tagName).toBe('HEADER')
    expect(root.className).toContain('sticky')
    expect(root.className).toContain('top-0')
    expect(root.className).toContain('card-bg')
  })

  it('renders GBot wordmark span', () => {
    const { root } = createHeader()
    const spans = root.querySelectorAll('span')
    let found = false
    for (const s of Array.from(spans)) {
      if (s.textContent === 'GBot') found = true
    }
    expect(found).toBe(true)
  })

  it('contains hamburger button with two rects', () => {
    const { root } = createHeader()
    const hamburger = findHamburger(root)
    expect(hamburger).not.toBeNull()
    const rects = hamburger!.querySelectorAll('rect')
    expect(rects.length).toBe(2)
  })

  it('contains agent span with text-t2 class (empty until setBreadcrumb)', () => {
    const { root } = createHeader()
    const spans = root.querySelectorAll('span.text-\\[12px\\].text-t2')
    expect(spans.length).toBe(1)
    expect(spans[0].textContent).toBe('')
  })

  it('contains separator chevron', () => {
    const { root } = createHeader()
    const spans = root.querySelectorAll('span')
    let found = false
    for (const s of Array.from(spans)) {
      if (s.textContent === '\u203a') found = true
    }
    expect(found).toBe(true)
  })

  it('contains model button with mono class', () => {
    const { root } = createHeader()
    const buttons = root.querySelectorAll('button')
    let modelButton: HTMLElement | null = null
    for (const b of Array.from(buttons)) {
      if (b.className.includes('mono')) {
        modelButton = b
      }
    }
    expect(modelButton).not.toBeNull()
  })

  it('has no session dropdown (no .relative group with session text)', () => {
    const { root } = createHeader()
    const dropdowns = root.querySelectorAll('.relative.group')
    expect(dropdowns.length).toBe(0)
  })

  it('setBreadcrumb sets agent and model text', () => {
    const h = createHeader()
    h.setBreadcrumb('coder', 'glm-4.6')
    const spans = h.root.querySelectorAll('span')
    let agentText = ''
    for (const s of Array.from(spans)) {
      if (s.textContent === 'coder') agentText = s.textContent
    }
    expect(agentText).toBe('coder')

    const buttons = h.root.querySelectorAll('button')
    for (const b of Array.from(buttons)) {
      if (b.className.includes('mono')) {
        expect(b.textContent).toBe('glm-4.6')
      }
    }
  })

  it('setStatus(true) applies pulse + text-blue to GBot wordmark', () => {
    const h = createHeader()
    h.setStatus(true)
    const spans = h.root.querySelectorAll('span')
    let gbotSpan: HTMLElement | null = null
    for (const s of Array.from(spans)) {
      if (s.textContent === 'GBot') gbotSpan = s
    }
    expect(gbotSpan).not.toBeNull()
    expect(gbotSpan!.classList.contains('pulse')).toBe(true)
    expect(gbotSpan!.className).toContain('text-blue')
  })

  it('setStatus(false) applies text-t3 and no pulse', () => {
    const h = createHeader()
    h.setStatus(false)
    const spans = h.root.querySelectorAll('span')
    let gbotSpan: HTMLElement | null = null
    for (const s of Array.from(spans)) {
      if (s.textContent === 'GBot') gbotSpan = s
    }
    expect(gbotSpan).not.toBeNull()
    expect(gbotSpan!.classList.contains('pulse')).toBe(false)
    expect(gbotSpan!.className).toContain('text-t3')
  })

  it('onHamburgerClick registers handler called on click', () => {
    const h = createHeader()
    let clicked = 0
    h.onHamburgerClick(() => { clicked++ })
    const hamburger = findHamburger(h.root)
    expect(hamburger).not.toBeNull()
    hamburger!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(clicked).toBe(1)
  })

  it('clicking model button opens dropdown panel', () => {
    const { root } = createHeader()
    const buttons = root.querySelectorAll('button')
    let modelButton: HTMLElement | null = null
    for (const b of Array.from(buttons)) {
      if (b.className.includes('mono')) {
        modelButton = b
      }
    }
    expect(modelButton).not.toBeNull()

    // Panel is lazily appended to body on first open
    modelButton!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = document.body.querySelector('div.fixed') as HTMLElement
    expect(panel).toBeTruthy()
    expect(panel.classList.contains('hidden')).toBe(false)

    modelButton!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
  })

})
