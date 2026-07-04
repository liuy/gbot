import type { AskDecision } from './types'

export interface AskData {
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

export interface AskHandles {
  root: HTMLElement
  close: () => void
}

function formatCommand(input: unknown): string {
  if (input == null) return ''
  if (typeof input === 'string') return input
  try {
    const obj = input as Record<string, unknown>
    if (typeof obj.command === 'string') return obj.command
    if (typeof obj.path === 'string') return obj.path
    if (typeof obj.file_path === 'string') return obj.file_path
    if (typeof obj.pattern === 'string') return obj.pattern
    return JSON.stringify(obj, null, 2)
  } catch {
    return String(input)
  }
}

export function createAsk(
  ask: AskData,
  respond: (decision: AskDecision) => void,
): AskHandles {
  const root = document.createElement('div')
  root.className =
    'mx-5 my-3 rounded-lg border border-amber/50 bg-amber/5 px-4 py-3'

  const title = document.createElement('div')
  title.className = 'mb-2 text-sm text-amber'
  title.textContent = `approve · ${ask.tool_name}`
  root.appendChild(title)

  const label = ask.message ?? ask.prompt ?? `approve · ${ask.tool_name}`
  if (label) {
    const lbl = document.createElement('div')
    lbl.className = 'mb-1 text-t2 text-sm'
    lbl.textContent = label
    root.appendChild(lbl)
  }

  const command = formatCommand(ask.input)
  if (command) {
    const pre = document.createElement('pre')
    pre.className =
      'mb-3 overflow-x-auto whitespace-pre-wrap rounded bg-black/30 px-3 py-2 font-mono text-sm text-t1'
    pre.textContent = command
    root.appendChild(pre)
  }

  const actions = document.createElement('div')
  actions.className = 'flex gap-2'

  const mkBtn = (
    label: string,
    onClick: () => void,
    danger?: boolean,
  ): HTMLButtonElement => {
    const b = document.createElement('button')
    b.type = 'button'
    b.textContent = label
    b.className =
      'rounded-lg px-3 py-1.5 text-sm transition-colors ' +
      (danger
        ? 'bg-red/10 text-red hover:bg-red/20'
        : 'bg-blue/10 text-blue hover:bg-blue/20')
    b.addEventListener('click', onClick)
    return b
  }

  actions.appendChild(
    mkBtn('Allow Once', () => respond('allow')),
  )
  actions.appendChild(
    mkBtn('Allow This Session', () => respond('allow_always')),
  )
  actions.appendChild(
    mkBtn('Deny', () => respond('deny'), true),
  )
  root.appendChild(actions)

  return {
    root,
    close: () => root.remove(),
  }
}
