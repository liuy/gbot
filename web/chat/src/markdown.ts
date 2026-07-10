import MarkdownIt from 'markdown-it'
import highlightjs from 'markdown-it-highlightjs'
import DOMPurify from 'dompurify'

const mdHighlighted: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(highlightjs)
const mdPlain: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true })

for (const md of [mdHighlighted, mdPlain]) {
  md.renderer.rules.table_open = () => '<div class="table-wrap"><table>'
  md.renderer.rules.table_close = () => '</table></div>'
}

export function ensureTableBlankLine(src: string): string {
  return src.replace(/([^\n|])\n(\|[^\n]+\n\|[-| ]+\|)/g, '$1\n\n$2')
}

export function renderMarkdown(input: string): string {
  return DOMPurify.sanitize(mdHighlighted.render(ensureTableBlankLine(input)), {
    USE_PROFILES: { html: true },
  })
}

export function renderMarkdownNoHighlight(input: string): string {
  return DOMPurify.sanitize(mdPlain.render(ensureTableBlankLine(input)), {
    USE_PROFILES: { html: true },
  })
}
