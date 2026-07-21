import MarkdownIt from 'markdown-it'
import highlightjs from 'markdown-it-highlightjs'
import DOMPurify from 'dompurify'
import { copyButtonHTML } from './utils/copy_button'

const mdHighlighted: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(highlightjs)
const mdPlain: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true })

for (const md of [mdHighlighted, mdPlain]) {
  md.renderer.rules.table_open = () => '<div class="table-wrap"><table>'
  md.renderer.rules.table_close = () => '</table></div>'
}

// Wrap fenced code blocks with a copy button. The button itself is inert;
// chat.ts wires click handlers via delegation on the messages container.
function wrapFenceWithCopy(md: MarkdownIt) {
  const defaultFence = md.renderer.rules.fence?.bind(md.renderer.rules)
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const rendered = defaultFence
      ? defaultFence(tokens, idx, options, env, self)
      : self.renderToken(tokens, idx, options)
    return `<div class="code-block-wrapper">${rendered}${copyButtonHTML()}</div>`
  }
}
wrapFenceWithCopy(mdHighlighted)
wrapFenceWithCopy(mdPlain)

export function ensureTableBlankLine(src: string): string {
  return src.replace(/([^\n|])\n(\|[^\n]+\n\|[-| ]+\|)/g, '$1\n\n$2')
}

export function renderMarkdown(input: string): string {
  return DOMPurify.sanitize(mdHighlighted.render(ensureTableBlankLine(input)), {
    USE_PROFILES: { html: true, svg: true },
  })
}

export function renderMarkdownNoHighlight(input: string): string {
  return DOMPurify.sanitize(mdPlain.render(ensureTableBlankLine(input)), {
    USE_PROFILES: { html: true, svg: true },
  })
}
