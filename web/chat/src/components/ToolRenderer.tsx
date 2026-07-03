import { memo, useEffect, useState, type ReactNode } from 'react'
import type { Block } from '../model'
import { formatDurationNs, stripAnsi } from '../utils'
import Markdown from './Markdown'
import Thinking from './Thinking'
import ToolGroup from './ToolGroup'
import { isCollapsibleTool } from './MessageComponent'

type ToolBlock = Extract<Block, { kind: 'tool' }>

function isDiffLine(line: string): '+' | '-' | null {
	const m = line.match(/^\s*\d+\s([+\-])/)
	return m ? (m[1] as '+' | '-') : null
}

function DiffOutput({ text }: { text: string }) {
	const plain = stripAnsi(text)
	const lines = plain.split('\n')
	return (
	  <div className="ml-[20px] font-mono text-sm leading-relaxed overflow-x-auto">
	    <div className="inline-block min-w-full">
	      {lines.map((line, i) => {
	        const marker = isDiffLine(line)
	        if (marker === '+') return <div key={i} className="bg-green/15 text-green/90 whitespace-pre">{line}</div>
	        if (marker === '-') return <div key={i} className="bg-red/15 text-red/90 whitespace-pre">{line}</div>
	        return <div key={i} className="text-t2 whitespace-pre">{line}</div>
	      })}
	    </div>
	  </div>
	)
}

function ToolRendererBase({ tool }: { tool: ToolBlock }) {
	const [expanded, setExpanded] = useState(false)
	const [, setTick] = useState(0)
	useEffect(() => {
	  if (tool.state !== 'running') return
	  const id = setInterval(() => setTick((t) => t + 1), 200)
	  return () => clearInterval(id)
	}, [tool.state])

	const seconds = tool.state === 'running'
	  ? (Date.now() - tool.startedAt) / 1000
	  : tool.timingNs / 1e9
	const dur = formatDurationNs(seconds * 1e9)
	const running = tool.state === 'running'
	const isError = tool.state === 'error'

	const hasDiff =
	  (tool.name === 'Edit' || tool.name === 'Write') &&
	  !!tool.displayOutput &&
	  /^\s*\d+\s[+\-]/m.test(stripAnsi(tool.displayOutput))

	const dot = '●'
	const dotColor = isError ? 'text-red' : 'text-green'
	const summary = tool.summary ? `(${tool.summary})` : ''
	const durStr = isError ? `FAIL · ${dur}` : dur
	const durColor = isError ? 'text-red' : running ? 'text-blue' : 'text-t3'

	return (
	  <div>
	    <span
	      role="button"
	      tabIndex={0}
	      onClick={() => setExpanded((v) => !v)}
	      onKeyDown={(e) => { if (e.key === 'Enter' || e.key === ' ') setExpanded((v) => !v) }}
	      className="inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle"
	    >
	      {running
	        ? <span className="text-[10px] leading-none align-middle inline-block w-4 text-center text-white animate-pulse">{dot}</span>
	        : <span className={'text-[10px] leading-none align-middle inline-block w-4 text-center ' + dotColor}>{dot}</span>}
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
	      <span className="font-mono text-sm text-blue align-middle">{tool.name}</span>
	      {summary && <span className="text-sm text-t2 font-light break-all align-middle"> {summary}</span>}
	      <span className={'font-mono text-xs align-middle ' + durColor}> {durStr}</span>
	    </span>
	    {expanded && tool.displayOutput && hasDiff && <DiffOutput text={tool.displayOutput} />}
    {expanded && tool.displayOutput && !hasDiff && (
      <pre className="ml-[20px] font-mono text-sm leading-relaxed text-t2 whitespace-pre overflow-x-auto">
        {stripAnsi(tool.displayOutput)}
      </pre>
    )}
    {expanded && tool.children.length > 0 && (
      <div className="ml-[20px] mt-1 space-y-1 border-l border-t3/30 pl-2">
        {renderChildBlocks(tool.children)}
      </div>
    )}
  </div>
  )
}

// Mirrors MessageComponent.renderGrouped but for nested children. Reuses
// the same grouping logic (consecutive collapsible tools collapse into
// ToolGroup; non-collapsible tools, text, thinking render standalone).
function renderChildBlocks(blocks: Block[]): ReactNode[] {
  const out: ReactNode[] = []
  let buffer: ToolBlock[] = []
  let key = 0
  const flush = () => {
    if (buffer.length === 0) return
    if (buffer.length >= 2) {
      out.push(<ToolGroup key={`cgrp-${key++}`} tools={buffer} />)
    } else {
      out.push(<ToolRenderer key={buffer[0].id} tool={buffer[0]} />)
    }
    buffer = []
  }
  for (const b of blocks) {
    switch (b.kind) {
      case 'thinking':
        out.push(<Thinking key={b.id} entry={b} />)
        break
      case 'tool':
        if (isCollapsibleTool(b)) {
          buffer.push(b)
        } else {
          flush()
          out.push(<ToolRenderer key={b.id} tool={b} />)
        }
        break
      case 'text':
        if (!b.text) break
        flush()
        out.push(<Markdown key={`ctxt-${key++}`}>{b.text}</Markdown>)
        break
      case 'user':
        if (!b.text) break
        flush()
        out.push(<div key={`cusr-${key++}`} className="text-[13px] text-t2 italic ml-2 my-1">{b.text}</div>)
        break
    }
  }
  flush()
  return out
}

const ToolRenderer = memo(ToolRendererBase)
export default ToolRenderer
