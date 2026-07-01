import { useEffect, useState } from 'react'
import type { ToolEntry } from '../model'
import { formatDurationNs, stripAnsi } from '../utils'

function isDiffLine(line: string): '+' | '-' | null {
  const m = line.match(/^\s*\d+\s([+\-])/)
  return m ? (m[1] as '+' | '-') : null
}

function DiffOutput({ text }: { text: string }) {
  const plain = stripAnsi(text)
  const lines = plain.split('\n')
  return (
    <div className="ml-[20px] font-mono text-sm leading-relaxed">
      {lines.map((line, i) => {
        const marker = isDiffLine(line)
        if (marker === '+') return <div key={i} className="bg-green/15 text-green/90 whitespace-pre-wrap break-all">{line}</div>
        if (marker === '-') return <div key={i} className="bg-red/15 text-red/90 whitespace-pre-wrap break-all">{line}</div>
        return <div key={i} className="text-t2 whitespace-pre-wrap break-all">{line}</div>
      })}
    </div>
  )
}

export default function ToolRenderer({ tool }: { tool: ToolEntry }) {
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
          ? <span className="text-[10px] align-middle text-white animate-pulse">{dot}</span>
          : <span className={'text-[10px] align-middle ' + dotColor}>{dot}</span>}
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
        <pre className="ml-[20px] font-mono text-sm leading-relaxed text-t2 whitespace-pre-wrap break-all">
          {stripAnsi(tool.displayOutput)}
        </pre>
      )}
    </div>
  )
}
