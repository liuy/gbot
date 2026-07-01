import { type ReactNode } from 'react'

type CodeProps = {
  inline?: boolean
  className?: string
  children?: ReactNode
}

export default function CodeBlock({
  inline,
  className,
  children,
}: CodeProps) {
  if (inline) {
    return (
      <code className="rounded bg-card px-1.5 py-0.5 font-mono text-sm text-blue">
        {children}
      </code>
    )
  }

  const text = String(children ?? '').replace(/\n$/, '')

  return (
    <pre className="overflow-x-auto px-3 py-1.5 font-mono text-sm leading-relaxed text-t2">
      <code>{text}</code>
    </pre>
  )
}
