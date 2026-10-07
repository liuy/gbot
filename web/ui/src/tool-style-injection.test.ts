import { describe, expect, it } from 'vitest'
import { renderToolOutput } from './tool_render'
import { renderMarkdown } from './markdown'

// 2026-10-07 blood lesson: agent-5's tool output carried a scraped Reddit
// page's own stylesheet. markdown-it (html:true) passed the tag through and
// DOMPurify's html profile keeps <style> elements in real browsers — so
// `body{width:600px;margin:0 auto}` applied document-wide and every phone
// layout clipped. The contract: raw HTML in message/tool content surfaces as
// ESCAPED TEXT (visible, inert) — never as markup, never silently dropped.
// Asserting the escaped form is what makes this a true red: element-count
// assertions go false-green under jsdom's DOMPurify, which strips what real
// browsers keep.
const REDDIT_PAYLOAD = [
  '<style>',
  '      body {',
  '          font: small verdana, arial, helvetica, sans-serif;',
  '          width: 600px;',
  '          margin: 0 auto;',
  '      }',
  '      h1 { background: transparent url(//www.redditstatic.com/logo.png); }',
  '</style>',
  'Reddit 对数据中心出口 IP 硬封锁（UA 无用、新的拿不到）',
].join('\n')

function renderToDiv(html: string): HTMLDivElement {
  const div = document.createElement('div')
  div.innerHTML = html
  return div
}

describe('2026-10-07 case: raw HTML in tool/message content stays escaped text', () => {
  it('renderToolOutput: style block surfaces as escaped text, inert', () => {
    const div = renderToDiv(renderToolOutput(REDDIT_PAYLOAD))
    expect(div.querySelectorAll('style').length).toBe(0)
    expect(div.innerHTML).toContain('&lt;style&gt;')
    expect(div.textContent).toContain('width: 600px')
    expect(div.textContent).toContain('Reddit 对数据中心出口 IP 硬封锁')
  })

  it('renderToolOutput: script block surfaces as escaped text, inert', () => {
    const div = renderToDiv(renderToolOutput(REDDIT_PAYLOAD + '\n<script>alert(1)</script>'))
    expect(div.querySelectorAll('script').length).toBe(0)
    expect(div.innerHTML).toContain('&lt;script&gt;')
  })

  it('renderMarkdown: same contract for assistant messages', () => {
    const div = renderToDiv(renderMarkdown(REDDIT_PAYLOAD))
    expect(div.querySelectorAll('style').length).toBe(0)
    expect(div.innerHTML).toContain('&lt;style&gt;')
    expect(div.textContent).toContain('width: 600px')
  })

  it('renderToolOutput skipHighlight path (mdPlain) obeys the same contract', () => {
    const div = renderToDiv(renderToolOutput(REDDIT_PAYLOAD, true))
    expect(div.querySelectorAll('style').length).toBe(0)
    expect(div.innerHTML).toContain('&lt;style&gt;')
    expect(div.textContent).toContain('width: 600px')
  })
})
