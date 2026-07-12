import { describe, it, expect } from 'vitest'
import { isDiffOutput, renderToolOutput } from './tool_render'

// ── A. isDiffOutput — diff detection ─────────────────────────────────

describe('isDiffOutput', () => {
  it('detects single-line diff', () => {
    expect(isDiffOutput(' 5 -const x')).toBe(true)
  })

  it('detects diff with summary prefix', () => {
    expect(isDiffOutput('Added 1 line\n 5 -const x')).toBe(true)
  })

  it('rejects plain text', () => {
    expect(isDiffOutput('clean output')).toBe(false)
  })
})

// ── B. renderToolOutput — full pipeline ───────────────────────────────

describe('renderToolOutput', () => {
  it('returns empty string for empty input', () => {
    expect(renderToolOutput('')).toBe('')
  })

  it('strips ANSI codes from diff before rendering', () => {
    const ansiDiff = '\x1b[1m 5 -const x\x1b[0m'
    const html = renderToolOutput(ansiDiff)
    expect(html).not.toContain('\x1b[')
    expect(html).toContain('diff-del')
  })

  it('renders plain text via markdown', () => {
    const html = renderToolOutput('hello world')
    expect(html).toContain('hello world')
  })

  it('renders web tool markdown with headings and code blocks', () => {
    const webOutput = `## Sources
3 sources
\`\`\`js
const x = 1;
\`\`\`
- [Link](https://example.com)`

    const html = renderToolOutput(webOutput)
    expect(html).toContain('<h2>')
    expect(html).toContain('<code')
    expect(html).toContain('<li>')
  })

  it('renders Bash output as-is', () => {
    const bashOutput = 'file1.txt\nfile2.txt\n3 directories'
    const html = renderToolOutput(bashOutput)
    expect(html).toContain('file1.txt')
    expect(html).toContain('file2.txt')
  })

  it('sanitizes XSS in output', () => {
    const xss = '<script>alert(1)</script>'
    const html = renderToolOutput(xss)
    expect(html).not.toContain('<script>')
  })
})

// ── C. renderDiff — line-level rendering ──────────────────────────────

describe('renderDiff output', () => {
  it('applies green background to added lines', () => {
    const html = renderToolOutput(' 5 +const x = 1;')
    expect(html).toContain('diff-add')
  })

  it('applies red background to removed lines', () => {
    const html = renderToolOutput(' 5 -const x = 1;')
    expect(html).toContain('diff-del')
  })

  it('uses text-t2 for context lines (no background)', () => {
    // Context lines only appear in diff when mixed with +/- lines.
    // A standalone context line without +/- is not a diff → goes to markdown.
    const diff = ` 3  import hljs
 5 -old line`
    const html = renderToolOutput(diff)
    expect(html).toContain('text-t2')
    expect(html).toContain('diff-del')
  })

  it('classifies mixed diff lines correctly', () => {
    const diff = `Added 1 line, removed 1 line
 3  import hljs
 4  import DOMPurify
 5 -const OLD = 1
 5 +const NEW = 2`
    const html = renderToolOutput(diff)
    // hljs wraps text in <span> tags, so check for raw text presence via regex
    expect(html).toMatch(/Added/)
    expect(html).toMatch(/removed/)
    // context lines → text-t2 (hljs wraps keywords in <span>)
    expect(html).toMatch(/import/)
    expect(html).toMatch(/DOMPurify/)
    // added line → green
    expect(html).toContain('diff-add')
    // removed line → red
    expect(html).toContain('diff-del')
  })
})

// ── D. Call chain integration ─────────────────────────────────────────

describe('renderToolOutput integration', () => {
  it('web markdown renders headings into DOM-ready HTML', () => {
    const webOutput = `## Sources
- [1] Title
  https://example.com`
    const html = renderToolOutput(webOutput)
    // Should produce DOM-ready HTML with proper markdown elements
    expect(html).toContain('<h2')
    expect(html).toContain('<li>')
    expect(html).toContain('https://example.com')
  })

  it('diff renders with line-level background classes', () => {
    const diff = ` 3  import hljs
 5 -old line
 5 +new line`
    const html = renderToolOutput(diff)
    // Each line in its own div with correct class
    expect(html).toContain('diff-del')
    expect(html).toContain('diff-add')
    expect(html).toContain('text-t2')
    // Lines are in separate divs
    const divCount = (html.match(/<div /g) || []).length
    expect(divCount).toBe(3)
  })
})
