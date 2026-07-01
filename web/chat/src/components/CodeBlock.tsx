import { type ReactNode, useState } from 'react'

type CodeProps = {
  className?: string
  children?: ReactNode
}

export default function CodeBlock({ className, children }: CodeProps) {
  const [copied, setCopied] = useState(false)

  const isBlock = className?.startsWith('language-')
  if (!isBlock) {
    return <code>{children}</code>
  }

  const text = String(children ?? '').replace(/\n$/, '')

  const copy = () => {
    navigator.clipboard?.writeText(text)
    setCopied(true)
    setTimeout(() => setCopied(false), 1500)
  }

  return (
    <pre className="group relative overflow-x-auto">
      <button
        onClick={copy}
        className="absolute right-2 top-1 z-10 rounded px-1.5 py-0.5 text-xs text-t3 opacity-0 transition-opacity hover:text-t1 group-hover:opacity-100"
      >
        {copied ? '✓' : 'copy'}
      </button>
      <code className={className}>{children}</code>
    </pre>
  )
}
