import { useState } from 'react'
import type { ToolEntry } from '../model'
import ToolRenderer from './ToolRenderer'
import { formatDurationNs } from '../utils'

// Groups consecutive tools into a two-level collapse. Level 1: a header with
// chevron + summary ("2 Searches, 1 Read, 1 LSP") + total duration. Level 2:
// each child tool, indented and default-collapsed.
export default function ToolGroup({
  tools,
}: {
  tools: ToolEntry[]
}) {
  const [expanded, setExpanded] = useState(false)

  if (tools.length === 0) return null

  // Single tool: render bare, no grouping header.
  if (tools.length === 1) {
    return <ToolRenderer tool={tools[0]} />
  }

  const summary = summarize(tools)
  const totalNs = tools.reduce((sum, t) => sum + t.timingNs, 0)
  const anyRunning = tools.some((t) => t.state === 'running')

  return (
    <div className="my-1">
      <button
        type="button"
        onClick={() => setExpanded((v) => !v)}
        className="flex items-center gap-2 text-left"
      >
        <Chevron expanded={expanded} />
        <span
          className={
            'inline-block h-2 w-2 rounded-full ' +
            (anyRunning ? 'bg-blue pulse-blue' : 'bg-t3')
          }
        />
        <span className="text-t2 text-sm">{summary}</span>
        {!anyRunning && totalNs > 0 && (
          <span className="text-t3 text-xs">· {formatDurationNs(totalNs)}</span>
        )}
      </button>
      {expanded && (
        <div className="ml-4 border-l border-hairline pl-3">
          {tools.map((t) => (
            <ToolRenderer key={t.id} tool={t} />
          ))}
        </div>
      )}
    </div>
  )
}

// Noun short form: "2 Searches, 1 Read, 1 LSP". Pluralize with -s when >1.
function summarize(tools: ToolEntry[]): string {
  const counts = new Map<string, number>()
  for (const t of tools) {
    const noun = nounFor(t)
    counts.set(noun, (counts.get(noun) ?? 0) + 1)
  }
  const parts: string[] = []
  for (const [noun, count] of counts) {
    parts.push(count > 1 ? `${count} ${noun}s` : `${count} ${noun}`)
  }
  return parts.join(', ')
}

function nounFor(t: ToolEntry): string {
  if (t.isSearch) return 'Search'
  if (t.isRead) return 'Read'
  if (t.isList) return 'List'
  if (t.isLsp) return 'LSP'
  return t.name
}

function Chevron({ expanded }: { expanded: boolean }) {
  return (
    <svg
      className={'h-3 w-3 text-t3 transition-transform ' + (expanded ? 'rotate-90' : '')}
      viewBox="0 0 12 12"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.5"
    >
      <path d="M4.5 3L7.5 6L4.5 9" />
    </svg>
  )
}
