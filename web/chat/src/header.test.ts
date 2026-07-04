import { describe, it, expect } from 'vitest'
import { createHeader } from './header'

describe('createHeader', () => {
  it('builds sticky header with card-bg', () => {
    const { root } = createHeader()
    expect(root.tagName).toBe('HEADER')
    expect(root.className).toContain('sticky')
    expect(root.className).toContain('top-0')
    expect(root.className).toContain('card-bg')
  })

  it('renders GBot wordmark', () => {
    const { root } = createHeader()
    const span = root.querySelector('span')
    expect(span?.textContent).toBe('GBot')
  })

  it('setStatus(true) applies pulse + text-blue', () => {
    const h = createHeader()
    h.setStatus(true)
    const span = h.root.querySelector('span')!
    expect(span.classList.contains('pulse')).toBe(true)
    expect(span.className).toContain('text-blue')
  })

  it('setStatus(false) applies text-t3 and no pulse', () => {
    const h = createHeader()
    h.setStatus(false)
    const span = h.root.querySelector('span')!
    expect(span.classList.contains('pulse')).toBe(false)
    expect(span.className).toContain('text-t3')
  })

  it('clicking a dropdown trigger toggles its panel visibility', () => {
    const { root } = createHeader()
    const triggers = root.querySelectorAll('button')
    // Find a dropdown trigger (the first three .relative > button).
    const dd = root.querySelectorAll('.relative')
    expect(dd.length).toBe(3)
    const trigger = dd[0].querySelector('button')!
    const panel = dd[0].querySelector('div')!
    expect(panel.classList.contains('hidden')).toBe(true)
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(false)
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
  })
})
