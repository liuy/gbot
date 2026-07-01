import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { PreWithCopy } from './PreWithCopy'
import { TableWrap } from './TableWrap'

export function ensureTableBlankLine(md: string): string {
  // Add blank line BEFORE a GFM table header row when it follows non-table content.
  // Match: (non-| char)(newline)(| header | ...)(newline)(|---|---|)
  // The header and separator must stay adjacent — we only insert a blank line before the header.
  return md.replace(/([^\n|])\n(\|[^\n]+\n\|[-| ]+\|)/g, '$1\n\n$2')
}

export default function Markdown({ children }: { children: string }) {
  return (
    <div className="md-body text-t1 text-[15px] leading-relaxed">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeHighlight]}
        components={{ pre: PreWithCopy as any, table: TableWrap as any }}
      >
        {ensureTableBlankLine(children)}
      </ReactMarkdown>
    </div>
  )
}
