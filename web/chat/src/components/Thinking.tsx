import { useEffect, useState } from 'react'
import type { ThinkingEntry } from '../model'
import { formatDurationNs } from '../utils'

export default function Thinking({
  entry,
}: {
  entry: ThinkingEntry
}) {
  const [expanded, setExpanded] = useState(false)
  const [, setTick] = useState(0)
  useEffect(() => {
    if (!entry.active) return
    const id = setInterval(() => setTick((t) => t + 1), 200)
    return () => clearInterval(id)
  }, [entry.active])

  const seconds = entry.active
    ? (Date.now() - entry.startedAt) / 1000
    : entry.durationNs / 1e9
  // TUI format: ✦ Thought for Xs (done) / ✦ Thinking... (active)
  const label = entry.active
    ? `Thinking (${formatDurationNs(seconds * 1e9)})`
    : `Thought for ${formatDurationNs(entry.durationNs)}`

  return (
    <div>
      <span
        role="button"
        tabIndex={0}
        onClick={() => setExpanded((v) => !v)}
        onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') setExpanded((v) => !v) }}
        className="inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle"
      >
        <span className="text-amber text-sm leading-none align-middle">✦</span>
        <svg
          className={'inline-block align-middle text-t3 transition-transform ' + (expanded ? 'rotate-90' : '')}
          width="12"
          height="12"
          viewBox="0 0 12 12"
          fill="none"
          stroke="currentColor"
          strokeWidth="1.5"
        >
          <path d="M4.5 3L7.5 6L4.5 9" />
        </svg>
        <span className="text-amber text-sm align-middle">{label}</span>
      </span>
      {expanded && entry.text && (
        <p className="ml-5 text-t2 text-sm italic whitespace-pre-wrap">
          {entry.text}
        </p>
      )}
    </div>
  )
}
