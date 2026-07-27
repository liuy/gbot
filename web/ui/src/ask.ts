import type { AskResponsePayload } from './types'
import { createElement, createNode } from './dom'

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
  deadline_unix?: number
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
  respond: (payload: AskResponsePayload) => void,
): AskHandles {
  if (ask.kind === 'input') {
    return createInputAsk(ask, respond)
  }
  return createPermissionAsk(ask, respond)
}

function createPermissionAsk(
  ask: AskData,
  respond: (payload: AskResponsePayload) => void,
): AskHandles {
  const root = createElement(
    'div',
    'mx-5 my-3 rounded-lg border border-amber/50 bg-amber/5 px-4 py-3',
  )

  const title = createElement('div', 'mb-2 text-sm text-amber')
  title.textContent = `Approve · ${ask.tool_name}`
  root.appendChild(title)

  const label = ask.message ?? ask.prompt ?? `Approve · ${ask.tool_name}`
  if (label) {
    const lbl = createElement('div', 'mb-1 text-t2 text-sm')
    lbl.textContent = label
    root.appendChild(lbl)
  }

  const command = formatCommand(ask.input)
  if (command) {
    const pre = createElement(
      'pre',
      'mb-3 overflow-x-auto whitespace-pre-wrap rounded bg-black/30 px-3 py-2 font-mono text-sm text-t1',
    )
    pre.textContent = command
    root.appendChild(pre)
  }

  const actions = createElement('div', 'flex gap-2')

  const mkBtn = (
    label: string,
    onClick: () => void,
    danger?: boolean,
  ): HTMLButtonElement => {
    const b = createNode('button', {
      className:
        'rounded-lg px-3 py-1.5 text-sm transition-colors ' +
        (danger
          ? 'bg-red/10 text-red hover:bg-red/20'
          : 'bg-blue/10 text-blue hover:bg-blue/20'),
      props: { type: 'button' },
      text: label,
    })
    b.addEventListener('click', onClick)
    return b
  }

  actions.appendChild(
    mkBtn('Allow Once', () => respond({ decision: 'allow' })),
  )
  actions.appendChild(
    mkBtn('Allow This Session', () => respond({ decision: 'allow_always' })),
  )
  actions.appendChild(
    mkBtn('Deny', () => respond({ decision: 'deny' }), true),
  )
  root.appendChild(actions)

  return {
    root,
    close: () => root.remove(),
  }
}

// createInputAsk renders an interactive text input (masked or plain) with a
// countdown timer. Enter submits the text; the countdown auto-aborts on zero.
// Uses blue (info/interactive) to distinguish from permission ask (amber).
function createInputAsk(
  ask: AskData,
  respond: (payload: AskResponsePayload) => void,
): AskHandles {
  const root = createElement(
    'div',
    'mx-5 my-3 rounded-lg border border-blue/50 bg-blue/5 px-4 py-3',
  )

  const title = createElement('div', 'mb-2 text-sm text-blue')
    title.textContent = ask.tool_name ? `Input · ${ask.tool_name}` : 'Input'
  root.appendChild(title)

  const prompt = ask.prompt ?? ask.message ?? ''
  if (prompt) {
    const lbl = createElement('div', 'mb-2 text-t2 text-sm')
    lbl.textContent = prompt
    root.appendChild(lbl)
  }

  const input = createNode('input', {
    className:
      'mb-3 w-full rounded card-bg px-3 py-2 font-mono text-sm text-t1 outline-none border border-hairline focus:border-blue',
    props: { type: ask.masked ? 'password' : 'text', spellcheck: false },
    attrs: { autocomplete: 'off' },
  })
  root.appendChild(input)

  const metaRow = createElement('div', 'flex items-center justify-between gap-2')

  const countdown = createElement('span', 'text-xs text-t3')
  const submitBtn = createNode('button', {
    className:
      'rounded-lg bg-blue/20 px-3 py-1.5 text-sm text-blue transition-colors hover:bg-blue/30',
    props: { type: 'button' },
    text: 'Submit',
  })

  const cancelBtn = createNode('button', {
    className:
      'rounded-lg bg-red/10 px-3 py-1.5 text-sm text-red transition-colors hover:bg-red/20',
    props: { type: 'button' },
    text: 'Cancel',
  })

  // Button group on the right: Cancel left, Submit right (primary action rightmost).
  const btnGroup = createElement('div', 'flex gap-2')
  btnGroup.appendChild(cancelBtn)
  btnGroup.appendChild(submitBtn)

  metaRow.appendChild(countdown)
  metaRow.appendChild(btnGroup)
  root.appendChild(metaRow)

  let submitted = false
  const submit = () => {
    if (submitted) return
    submitted = true
    stopTimer()
    respond({ text: input.value, aborted: false })
  }
  // User clicked Cancel — aborted without timeout. PTY shows
  // "[Interaction cancelled by user]" (vs "[Interaction timed out]").
  const cancel = () => {
    if (submitted) return
    submitted = true
    stopTimer()
    respond({ text: '', aborted: true, timeout: false })
  }
  // Deadline countdown expired — aborted with timeout. PTY shows
  // "[Interaction timed out]".
  const abort = () => {
    if (submitted) return
    submitted = true
    stopTimer()
    respond({ text: '', aborted: true, timeout: true })
  }

  submitBtn.addEventListener('click', submit)
  cancelBtn.addEventListener('click', cancel)
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      submit()
    }
  })

  // Countdown timer — auto-abort at deadline.
  // Abort is deferred via setTimeout(0) so createAsk's caller (chat.ts)
  // finishes appendChild + askEls.push before the abort callback fires;
  // otherwise an already-expired deadline would synchronously close the
  // dialog mid-construction, leaving a dead node in the DOM.
  let timerId: number | undefined
  const stopTimer = () => {
    if (timerId !== undefined) {
      clearInterval(timerId)
      timerId = undefined
    }
  }
  const startTimer = () => {
    if (!ask.deadline_unix) {
      countdown.textContent = ''
      return
    }
    const update = () => {
      const remaining = Math.max(0, ask.deadline_unix! - Math.floor(Date.now() / 1000))
      if (remaining <= 0) {
        countdown.textContent = 'Timed out'
        setTimeout(abort, 0)
        stopTimer()
        return
      }
      countdown.textContent = `timeout in ${remaining}s`
    }
    update()
    timerId = window.setInterval(update, 1000)
  }
  startTimer()

  // Autofocus after attach.
  setTimeout(() => input.focus(), 0)

  return {
    root,
    close: () => {
      stopTimer()
      root.remove()
    },
  }
}
