import ReactMarkdown from 'react-markdown'
import remarkGfm from 'remark-gfm'
import rehypeHighlight from 'rehype-highlight'
import { PreWithCopy } from './PreWithCopy'

export default function Markdown({ children }: { children: string }) {
  return (
    <div className="md-body text-t1 text-[15px] leading-relaxed">
      <ReactMarkdown
        remarkPlugins={[remarkGfm]}
        rehypePlugins={[rehypeHighlight]}
        components={{ pre: PreWithCopy as any }}
      >
        {children}
      </ReactMarkdown>
    </div>
  )
}
