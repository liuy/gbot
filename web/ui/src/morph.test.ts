import { describe, it, expect } from 'vitest'
import { morphHtml } from './morph'

describe('morphHtml', () => {
  it('updates content with new HTML', () => {
    const el = document.createElement('div')
    el.innerHTML = '<p>old</p>'
    morphHtml(el, '<p>new</p>')
    expect(el.querySelector('p')?.textContent).toBe('new')
  })

  it('preserves existing code-header across morph', () => {
    const el = document.createElement('div')
    el.innerHTML = '<div class="code-block-wrapper"><div class="code-header"><span class="code-lang">go</span></div><pre><code>hello</code></pre></div>'
    const headerBefore = el.querySelector('.code-header')
    morphHtml(el, '<div class="code-block-wrapper"><div class="code-header"><span class="code-lang">go</span></div><pre><code>hello</code></pre></div><p>appended</p>')
    const headerAfter = el.querySelector('.code-header')
    expect(headerAfter).toBe(headerBefore)
  })

  it('preserves injected copy-btn across morph', () => {
    const el = document.createElement('div')
    el.innerHTML = '<div class="code-block-wrapper"><div class="code-header"><span class="code-lang">go</span><button class="copy-btn">copy</button></div><pre><code>hello</code></pre></div>'
    const btnBefore = el.querySelector('.copy-btn')
    expect(btnBefore).not.toBeNull()
    morphHtml(el, '<div class="code-block-wrapper"><div class="code-header"><span class="code-lang">go</span></div><pre><code>hello</code></pre></div><p>new text</p>')
    const btnAfter = el.querySelector('.copy-btn')
    expect(btnAfter).toBe(btnBefore)
    expect(btnAfter?.textContent).toBe('copy')
  })

  it('handles empty HTML by clearing content', () => {
    const el = document.createElement('div')
    el.innerHTML = '<p>content</p>'
    morphHtml(el, '')
    expect(el.children.length).toBe(0)
  })

  it('updates only changed text node, preserving unchanged siblings', () => {
    const el = document.createElement('div')
    el.innerHTML = '<p>unchanged</p><p>old text</p>'
    const p1Before = el.children[0]
    morphHtml(el, '<p>unchanged</p><p>new text</p>')
    expect(el.children[0]).toBe(p1Before)
    expect(el.children[1].textContent).toBe('new text')
  })
})
