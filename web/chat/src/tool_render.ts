import hljs from 'highlight.js'
import DOMPurify from 'dompurify'
import { stripAnsi } from './utils'
import { renderMarkdown, renderMarkdownNoHighlight } from './markdown'

const DIFF_LINE_RE = /^\s*\d+\s([+-])/m

export function isDiffOutput(output: string): boolean {
  return DIFF_LINE_RE.test(output)
}

export function renderToolOutput(output: string, skipHighlight = false): string {
  if (!output) return ''
  const clean = stripAnsi(output)
  if (isDiffOutput(clean)) {
    return renderDiff(clean, skipHighlight)
  }
  return skipHighlight ? renderMarkdownNoHighlight(clean) : renderMarkdown(clean)
}

function renderDiff(output: string, skipHighlight = false): string {
  const lines = output.split('\n')
  const html = lines.map((line) => {
    const m = line.match(DIFF_LINE_RE)
    const cls = m
      ? m[1] === '+'
        ? 'diff-add text-green/90'
        : 'diff-del text-red/90'
      : 'text-t2'
    const content = skipHighlight ? escapeHtml(line) : highlightLine(line)
    return `<div class="${cls} whitespace-pre-wrap break-all font-mono text-[11px] leading-relaxed">${content}</div>`
  })
  return DOMPurify.sanitize(html.join(''), { USE_PROFILES: { html: true } })
}

function highlightLine(line: string): string {
  try {
    return hljs.highlightAuto(line).value
  } catch {
    return escapeHtml(line)
  }
}

function escapeHtml(s: string): string {
  return s
    .replace(/&/g, '&amp;')
    .replace(/</g, '&lt;')
    .replace(/>/g, '&gt;')
    .replace(/"/g, '&quot;')
    .replace(/'/g, '&#39;')
}
