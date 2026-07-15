import { describe, it, expect, beforeEach } from 'vitest'
import { createHeader } from './header'

describe('Header context display', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function getContextText(): string {
    // Find the context span (last child of inner, not hidden)
    const spans = header.root.querySelectorAll('span.mono')
    for (const s of spans) {
      if (s.textContent && s.textContent.includes('/')) return s.textContent
    }
    return ''
  }

  function getContextClass(): string {
    const spans = header.root.querySelectorAll('span.mono')
    for (const s of spans) {
      if (s.textContent && s.textContent.includes('/')) return s.className
    }
    return ''
  }

  it('formatTokenCount: raw number under 1K', () => {
    header.setContext(500, 200000)
    expect(getContextText()).toContain('500/')
  })

  it('formatTokenCount: k suffix under 1M', () => {
    header.setContext(28300, 200000)
    expect(getContextText()).toContain('27.6k/')
    expect(getContextText()).toContain('195.3k')
  })

  it('formatTokenCount: M suffix over 1M', () => {
    header.setContext(1048576, 2097152)
    expect(getContextText()).toContain('1.0M/')
    expect(getContextText()).toContain('2.0M')
  })

  it('hides when total is 0', () => {
    header.setContext(100, 0)
    expect(getContextText()).toBe('')
  })

  it('hides when total is negative', () => {
    header.setContext(100, -1)
    expect(getContextText()).toBe('')
  })

  it('normal color under 80%', () => {
    header.setContext(100000, 200000) // 50%
    expect(getContextClass()).toContain('text-t2')
    expect(getContextClass()).not.toContain('text-amber')
    expect(getContextClass()).not.toContain('text-red')
  })

  it('amber color at 80%', () => {
    header.setContext(160000, 200000) // 80%
    expect(getContextClass()).toContain('text-amber-500')
  })

  it('red color at 90%', () => {
    header.setContext(180000, 200000) // 90%
    expect(getContextClass()).toContain('text-red-500')
  })

  it('updates on repeated calls', () => {
    header.setContext(10000, 200000)
    expect(getContextText()).toContain('9.8k/')
    header.setContext(50000, 200000)
    expect(getContextText()).toContain('48.8k/')
  })
})
