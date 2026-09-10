import MarkdownIt from 'markdown-it'
import highlightjs from 'markdown-it-highlightjs'
import DOMPurify from 'dompurify'
import katexMath from '@vscode/markdown-it-katex'
// output:'mathml' renders through the browser's native MathML engine with
// system fonts — no KaTeX webfonts bundled, which keeps the single-file
// build ~1MB gz smaller. The top-level katex stays on the plugin's native
// ^0.16 line so its nested require dedupes to one copy with ours.
const mdHighlighted: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(highlightjs).use(katexMath, { throwOnError: false, output: 'mathml' })
const mdPlain: MarkdownIt = MarkdownIt({ html: true, linkify: true, breaks: true }).use(katexMath, { throwOnError: false, output: 'mathml' })

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

// KaTeX emits an embedded <math> accessibility subtree next to its HTML
// output; without the mathMl profile DOMPurify strips it. The key must be
// camelCase `mathMl` — lowercase `mathml` is a silent no-op because
// DOMPurify only recognizes camelCase keys in USE_PROFILES.
function sanitize(html: string): string {
  return DOMPurify.sanitize(html, {
    USE_PROFILES: { html: true, svg: true, mathMl: true },
    // KaTeX wraps the math in <semantics><annotation encoding=...>TeX source
    // </annotation></semantics>; DOMPurify strips the wrapper tags but hoists
    // the annotation text, so screen readers would read each formula twice.
    // Forbidding <annotation> (not <semantics>, whose subtree holds the
    // <mrow> we must keep) deletes the TeX source together with its element.
    // ADD_FORBID_CONTENTS merges into the default list; FORBID_CONTENTS would
    // replace it, dropping script/style content stripping.
    ADD_FORBID_CONTENTS: ['annotation'],
  })
}

export function renderMarkdown(input: string): string {
  return sanitize(mdHighlighted.render(ensureTableBlankLine(input)))
}

export function renderMarkdownNoHighlight(input: string): string {
  return sanitize(mdPlain.render(ensureTableBlankLine(input)))
}
