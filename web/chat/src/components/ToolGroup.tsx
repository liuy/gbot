import { memo, useState } from 'react'
import type { Block } from '../model'
import ToolRenderer from './ToolRenderer'
import { formatDurationNs } from '../utils'

type ToolBlock = Extract<Block, { kind: 'tool' }>

// Groups consecutive tools into a two-level collapse. Level 1: a header with
// chevron + summary ("2 Searches, 1 Read, 1 LSP") + total duration. Level 2:
// each child tool, indented and default-collapsed.
export default memo(function ToolGroup({
	tools,
}: {
	tools: ToolBlock[]
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
	  <div>
	    <button
	      type="button"
	      onClick={() => setExpanded((v) => !v)}
	      className="inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle"
	    >
	      <span className="text-[10px] leading-none align-middle inline-block w-4 text-center align-middle">
	        {anyRunning
	          ? <span className="text-white heartbeat">●</span>
	          : <span className="text-green">●</span>}
	      </span>
	      <Chevron expanded={expanded} />
	      <span className="font-mono text-sm text-blue align-middle">{summary}</span>
	      {!anyRunning && totalNs > 0 && (
	        <span className="font-mono text-xs align-middle"> {formatDurationNs(totalNs)}</span>
	      )}
	    </button>
	    {expanded && (
	      <div className="ml-[20px]">
	        {tools.map((t) => (
	          <ToolRenderer key={t.id} tool={t} />
	        ))}
	      </div>
	    )}
	  </div>
	)
})

// Noun short form: "2 Searches, 1 Read, 1 LSP". Pluralize with -s when >1.
function summarize(tools: ToolBlock[]): string {
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

function nounFor(t: ToolBlock): string {
	if (t.isWeb) return 'Web'
	if (t.isSearch) return 'Search'
	if (t.isRead) return 'Read'
	if (t.isList) return 'List'
	if (t.isLsp) return 'LSP'
	return t.name
}

function Chevron({ expanded }: { expanded: boolean }) {
	return (
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
	)
}
