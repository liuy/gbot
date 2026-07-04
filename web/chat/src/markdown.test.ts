import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import { renderMarkdown, ensureTableBlankLine } from './markdown'

const css = readFileSync(resolve(__dirname, 'index.css'), 'utf-8')

function container(html: string): HTMLElement {
  const el = document.createElement('div')
  el.innerHTML = html
  return el
}

describe('ensureTableBlankLine', () => {
  it('keeps header and separator adjacent (table at start)', () => {
    const input = '| H1 | H2 |\n|---|---|\n| a | b |'
    const out = ensureTableBlankLine(input)
    expect(out).toContain('| H1 | H2 |\n|---|---|')
  })

  it('inserts blank line before table when preceded by text', () => {
    const input = 'Some text\n| H1 | H2 |\n|---|---|\n| a | b |'
    const out = ensureTableBlankLine(input)
    expect(out).toContain('Some text\n\n| H1 | H2 |')
    expect(out).toContain('| H1 | H2 |\n|---|---|')
  })

  it('does not add extra blank line when one already exists', () => {
    const input = 'Some text\n\n| H1 | H2 |\n|---|---|\n| a | b |'
    const out = ensureTableBlankLine(input)
    expect(out).not.toContain('\n\n\n|')
    expect(out).toContain('| H1 | H2 |\n|---|---|')
  })

  it('does not break code blocks containing pipe characters', () => {
    const input = '```\necho "| a | b |"\n```\nText after.'
    const out = ensureTableBlankLine(input)
    expect(out).toBe(input)
  })
})

describe('renderMarkdown', () => {
  it('renders **bold** as <strong>', () => {
    const el = container(renderMarkdown('**bold**'))
    expect(el.querySelector('strong')?.textContent).toBe('bold')
  })

  it('renders inline code as <code> not inside <pre>', () => {
    const el = container(renderMarkdown('text with `inline` code'))
    const inlineCodes = el.querySelectorAll(':not(pre) > code')
    expect(inlineCodes.length).toBe(1)
    expect(inlineCodes[0].textContent).toBe('inline')
  })

  it('renders unordered list', () => {
    const el = container(renderMarkdown('- item 1\n- item 2'))
    const ul = el.querySelector('ul')
    expect(ul).toBeTruthy()
    expect(ul!.children.length).toBe(2)
  })

  it('renders ordered list', () => {
    const el = container(renderMarkdown('1. first\n2. second'))
    const ol = el.querySelector('ol')
    expect(ol).toBeTruthy()
    expect(ol!.children.length).toBe(2)
  })

  it('renders table with th and td', () => {
    const el = container(renderMarkdown('| H1 | H2 |\n|---|---|\n| a | b |'))
    const table = el.querySelector('table')
    expect(table).toBeTruthy()
    expect(table!.querySelectorAll('th').length).toBe(2)
    expect(table!.querySelectorAll('td').length).toBe(2)
  })

  it('renders code block with hljs class on <code>', () => {
    const el = container(renderMarkdown('```python\nprint("hi")\n```'))
    const pre = el.querySelector('pre')
    expect(pre).toBeTruthy()
    const code = pre!.querySelector('code')
    expect(code?.className).toContain('hljs')
  })

  it('renders blockquote', () => {
    const el = container(renderMarkdown('> quoted text'))
    const bq = el.querySelector('blockquote')
    expect(bq).toBeTruthy()
    expect(bq!.textContent).toContain('quoted text')
  })

  it('DOMPurify strips XSS event handlers', () => {
    const out = renderMarkdown('<img src=x onerror=alert(1)>')
    expect(out).not.toContain('onerror')
  })

  it('empty input yields empty/whitespace-only result', () => {
    const out = renderMarkdown('')
    expect(out.trim()).toBe('')
  })

  it('long input completes without truncation', () => {
    const long = 'word '.repeat(10000)
    const out = renderMarkdown(long)
    expect(out.length).toBeGreaterThan(1000)
  })
})

describe('Markdown CSS rules', () => {
  it('has .md-body table border-collapse rule', () => {
    expect(css).toContain('.md-body table')
    expect(css).toContain('border-collapse')
  })

  it('has .md-body ul list-style-type disc', () => {
    expect(css).toContain('.md-body ul')
    expect(css).toContain('list-style-type: disc')
  })

  it('has .md-body ol list-style-type decimal', () => {
    expect(css).toContain('.md-body ol')
    expect(css).toContain('list-style-type: decimal')
  })

  it('has .md-body inline code style (:not(pre) > code)', () => {
    expect(css).toContain(':not(pre) > code')
  })

  it('has .md-body blockquote border-left', () => {
    expect(css).toContain('.md-body blockquote')
    expect(css).toContain('border-left')
  })

  it('has .md-body pre overflow-x auto', () => {
    expect(css).toContain('.md-body pre')
    expect(css).toContain('overflow-x: auto')
    expect(css).toContain('max-width: 100%')
  })

  it('has .md-body td white-space nowrap for horizontal scroll', () => {
    expect(css).toContain('white-space: nowrap')
  })

  it('table does not have width 100% (should shrink to content)', () => {
    const tableIdx = css.indexOf('.md-body table')
    expect(tableIdx).toBeGreaterThanOrEqual(0)
    const tableRule = css.slice(tableIdx, tableIdx + 200)
    expect(tableRule).not.toContain('width: 100%')
  })
})
