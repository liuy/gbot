import { describe, it, expect } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import { renderMarkdown, renderMarkdownNoHighlight, ensureTableBlankLine } from './markdown'

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

  it('renders rust code block with language label in header', () => {
    const el = container(renderMarkdown('```rust\nfn main() {}\n```'))
    const wrapper = el.querySelector('.code-block-wrapper') as HTMLElement
    expect(wrapper).toBeTruthy()
    expect(wrapper.dataset.lang).toBe('rust')
    const langSpan = wrapper.querySelector('.code-lang')
    expect(langSpan?.textContent).toBe('rust')
    const code = wrapper.querySelector('code')
    expect(code?.className).toContain('hljs')
    expect(code?.className).toContain('language-rust')
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

describe('KaTeX math rendering', () => {
  it('renders inline $...$ as a single katex span with native MathML glyphs', () => {
    const el = container(renderMarkdown('$a \\cdot b = \\sum a_i b_i$'))
    expect(el.querySelectorAll('.katex').length).toBe(1)
    const math = el.querySelector('.katex > math')
    expect(math?.textContent).toContain('∑')
    expect(math?.textContent).toContain('⋅')
    // output:'mathml' must stay font-free — katex-html would drag ~1MB of
    // webfonts back into the single-file build.
    expect(el.querySelector('.katex-html')).toBeNull()
  })

  it('renders $$...$$ as a block-level <math display="block">', () => {
    const el = container(renderMarkdown('$$\nx = y\n$$'))
    const math = el.querySelector('math')
    expect(math?.getAttribute('display')).toBe('block')
    expect(math?.textContent).toContain('x=y')
  })

  it('does not typeset currency amounts ($20,000 and $30,000)', () => {
    const el = container(renderMarkdown('cost $20,000 and $30,000 total'))
    expect(el.textContent).toContain('$20,000 and $30,000 total')
    expect(el.querySelectorAll('.katex').length).toBe(0)
  })

  it('leaves unclosed $x + y as literal text (streaming safety)', () => {
    const el = container(renderMarkdown('pending $x + y more'))
    expect(el.textContent).toContain('$x + y')
    expect(el.querySelectorAll('.katex').length).toBe(0)
  })

  it('sanitizer keeps the <math> accessibility subtree after render', () => {
    const out = renderMarkdown('$a + b$')
    expect(out).toContain('<math')
    expect(out).toContain('<mi>a</mi>')
    expect(out).toContain('<mo>+</mo>')
    const el = container(out)
    const math = el.querySelector('math')
    expect(math?.namespaceURI).toBe('http://www.w3.org/1998/Math/MathML')
  })

  it('renderMarkdownNoHighlight typesets math through the plain instance', () => {
    const el = container(renderMarkdownNoHighlight('$a \\cdot b$'))
    expect(el.querySelectorAll('.katex').length).toBe(1)
    expect(el.querySelector('.katex > math')?.textContent).toContain('⋅')
  })

  it('renders invalid TeX as katex-error instead of throwing', () => {
    let out = ''
    expect(() => { out = renderMarkdown('$\\frac{$') }).not.toThrow()
    const err = container(out).querySelector('.katex-error')
    expect(err).toBeTruthy()
    expect(err?.getAttribute('title')).toContain('ParseError')
  })

  it('sanitizer drops annotation TeX source, keeping MathML element text', () => {
    const out = renderMarkdown('$x+y$')
    const math = container(out).querySelector('math')
    expect(math).toBeTruthy()
    // Hoisted annotation residue would be a bare text child of <math>;
    // legit reading text lives inside <mi>/<mo>/<mn> elements.
    const stray = Array.from(math!.childNodes).filter(
      (n) => n.nodeType === 3 && (n.textContent ?? '').trim() !== ''
    )
    expect(stray).toHaveLength(0)
    expect(math!.querySelector('mi')?.textContent).toBe('x')
    expect(math!.querySelector('mo')?.textContent).toBe('+')
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
