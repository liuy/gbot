import { useWebSocket } from '../websocket'
import type { AskDecision } from '../types'

type AskData = {
  id: string
  kind: 'permission' | 'input'
  tool_name: string
  input?: unknown
  message?: string
  rule_detail?: string
  prompt?: string
  masked?: boolean
  agent_type?: string
}

export default function Ask({ ask }: { ask: AskData }) {
  const { send } = useWebSocket()

  const respond = (decision: AskDecision) => {
    send({ type: 'ask_response', id: ask.id, decision })
  }

  const command = formatCommand(ask.input)
  const label = ask.message ?? ask.prompt ?? `approve · ${ask.tool_name}`

  return (
    <div className="mx-5 my-3 rounded-lg border border-amber/50 bg-amber/5 px-4 py-3">
      <div className="mb-2 text-sm text-amber">
        approve · {ask.tool_name}
      </div>
      {label && <div className="mb-1 text-t2 text-sm">{label}</div>}
      {command && (
        <pre className="mb-3 overflow-x-auto whitespace-pre-wrap rounded bg-black/30 px-3 py-2 font-mono text-[12px] text-t1">
          {command}
        </pre>
      )}
      <div className="flex gap-2">
        <AskButton label="Allow Once" onClick={() => respond('allow')} />
        <AskButton
          label="Allow This Session"
          onClick={() => respond('allow_always')}
        />
        <AskButton label="Deny" onClick={() => respond('deny')} danger />
      </div>
    </div>
  )
}

function AskButton({
  label,
  onClick,
  danger,
}: {
  label: string
  onClick: () => void
  danger?: boolean
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        'rounded-lg px-3 py-1.5 text-sm transition-colors ' +
        (danger
          ? 'bg-red/10 text-red hover:bg-red/20'
          : 'bg-blue/10 text-blue hover:bg-blue/20')
      }
    >
      {label}
    </button>
  )
}

function formatCommand(input: unknown): string {
  if (input == null) return ''
  if (typeof input === 'string') return input
  try {
    const obj = input as Record<string, unknown>
    // Common tool input shapes: prefer "command" (Bash), then "path"/"file_path",
    // then fall back to a compact JSON dump.
    if (typeof obj.command === 'string') return obj.command
    if (typeof obj.path === 'string') return obj.path
    if (typeof obj.file_path === 'string') return obj.file_path
    if (typeof obj.pattern === 'string') return obj.pattern
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(input)
  }
}
