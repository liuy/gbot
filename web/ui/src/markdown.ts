import MarkdownIt from 'markdown-it'
import highlightjs from 'markdown-it-highlightjs'
import DOMPurify from 'dompurify'

const mdHighlighted: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(highlightjs)
const mdPlain: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true })

for (const md of [mdHighlighted, mdPlain]) {
  md.renderer.rules.table_open = () => '<div class="table-wrap"><table>'
  md.renderer.rules.table_close = () => '</table></div>'
}

// escapeHtml avoids pulling in a dep for the 4 chars we need; fence info
// strings are user-controlled and must not leak into HTML unescaped.
function escapeHtml(s: string): string {
  return s.replace(/[<>&"]/g, (c) => (
    c === '<' ? '&lt;' : c === '>' ? '&gt;' : c === '&' ? '&amp;' : '&quot;'
  ))
}

// Wrap fenced code blocks with a header showing the language. The copy
// button is wired by chat.ts post-render via createIconButton — this
// renderer only produces the structural HTML (wrapper > header > code).
function wrapFenceWithHeader(md: MarkdownIt) {
  const defaultFence = md.renderer.rules.fence?.bind(md.renderer.rules)
  md.renderer.rules.fence = (tokens, idx, options, env, self) => {
    const rendered = defaultFence
      ? defaultFence(tokens, idx, options, env, self)
      : self.renderToken(tokens, idx, options)
    const lang = tokens[idx].info.trim().split(/\s+/)[0]
    const langBadge = lang
      ? `<span class="code-lang">${escapeHtml(lang)}</span>`
      : '<span class="code-lang-placeholder"></span>'
    return (
      '<div class="code-block-wrapper" data-lang="' + escapeHtml(lang) + '">' +
      '<div class="code-header">' + langBadge + '</div>' +
      rendered +
      '</div>'
    )
  }
}
wrapFenceWithHeader(mdHighlighted)
wrapFenceWithHeader(mdPlain)

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
