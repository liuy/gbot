import MarkdownIt from 'markdown-it'
import highlightjs from 'markdown-it-highlightjs'
import DOMPurify from 'dompurify'

const md: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(highlightjs)

// Wrap <table> in a horizontal-scroll container so wide tables don't break
// the message column. Overrides the default table_open/table_close renderers.
md.renderer.rules.table_open = () => '<div class="table-wrap"><table>'
md.renderer.rules.table_close = () => '</table></div>'

export function ensureTableBlankLine(src: string): string {
  // Add blank line BEFORE a GFM table header row when it follows non-table content.
  return src.replace(/([^\n|])\n(\|[^\n]+\n\|[-| ]+\|)/g, '$1\n\n$2')
}

export function renderMarkdown(input: string): string {
  return DOMPurify.sanitize(md.render(ensureTableBlankLine(input)), {
    USE_PROFILES: { html: true },
  })
}
