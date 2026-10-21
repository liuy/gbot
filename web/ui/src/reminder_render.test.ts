import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createChat } from './chat'

// jsdom lacks IntersectionObserver — stub it (no longer used for prefetch).
class MockIntersectionObserver {
  observe() {}
  unobserve() {}
  disconnect() {}
  takeRecords() { return [] }
}
vi.stubGlobal('IntersectionObserver', MockIntersectionObserver as unknown as typeof IntersectionObserver)

type Listener = (msg: unknown) => void

const listeners: Set<Listener> = new Set()
const sent: unknown[] = []

vi.mock('./ws', () => ({
  getConnection: () => ({
    subscribe: (fn: Listener) => {
      listeners.add(fn)
      return () => listeners.delete(fn)
    },
    send: (p: unknown) => {
      sent.push(p)
    },
    connected: true,
  }),
}))

function dispatch(msg: unknown) {
  listeners.forEach((fn) => fn(msg))
}

// Engine event frames ride the {type:'event', event} wire shape.
function dispatchEvent(event: unknown) {
  dispatch({ type: 'event', event })
}

function mount() {
  document.body.innerHTML = ''
  const chat = createChat({ connected: true })
  document.body.appendChild(chat.root)
  return chat
}

// A single user history message carrying text.
function userHistory(id: string, text: string) {
  return {
    id, role: 'user', text,
    thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
    error: '', status: 'done', startedAt: 1760424000000,
  }
}

function mountHistory(text: string) {
  mount()
  dispatch({ type: 'connect_status', connected: true })
  dispatch({
    type: 'history',
    messages: [userHistory('u1', text)],
    nextCursor: '', hasMore: false,
  })
}

// The marker rides the tool root itself, so a row found here IS the tool row
// built by the shared tool-block factory — history and live alike.
function reminderRows(): HTMLElement[] {
  return Array.from(document.querySelectorAll('[data-reminder-row]'))
}

// User-bubble text spans (plain and gray queued-message variants both carry
// whitespace-pre-wrap; a reminder row's body is a div, never one of these).
function bubbleSpans(): HTMLSpanElement[] {
  return Array.from(document.querySelectorAll('span')).filter(
    s => s.className.includes('whitespace-pre-wrap'),
  )
}

function summaryTextOfRow(row: HTMLElement): string {
  // toolSummary is the only span carrying the font-light utility.
  const span = row.querySelector('span.font-light')
  return span ? span.textContent ?? '' : ''
}

// Row bodies are folded with render-on-visible — expanding the header renders
// the deferred body before assertions can see it.
function clickRowHeader(row: HTMLElement) {
  ;(row.querySelector('[role="button"]') as HTMLElement).click()
}

const BASH_ENVELOPE =
  '<system-reminder>\nA background command completed:\n<job-notification>\n<job-id>bg-1</job-id>\n' +
  '<status>completed</status>\n<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n' +
  '</job-notification>\n</system-reminder>'

const AGENT_ENVELOPE =
  '<system-reminder>\nA background agent completed a job:\n<job-notification>\n<job-id>agent-1</job-id>\n' +
  '<tool-use-id>toolu_1</tool-use-id>\n<status>completed</status>\n<agent-type>fork</agent-type>\n' +
  '<duration-ms>1200</duration-ms>\n<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n' +
  '<summary>Background agent &quot;refactor auth&quot; completed</summary>\n<result>\ndone\n</result>\n' +
  '</job-notification>\n</system-reminder>'

const QUEUED_ENVELOPE =
  '<system-reminder>\nThe user sent a new message while you were working:\ncheck on that' +
  '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

beforeEach(() => {
  listeners.clear()
  sent.length = 0
  document.body.innerHTML = ''
})

afterEach(() => {
  document.body.innerHTML = ''
})

describe('injected reminder history rendering', () => {
  it('R1: command job envelope renders a settled Reminder row with parsed summary', () => {
    mountHistory(BASH_ENVELOPE)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    const row = rows[0]
    expect(row.dataset.toolName).toBe('Reminder')
    expect(summaryTextOfRow(row)).toBe(' (command make check · exit 0)')
    // The row sits inside the message shell grid (avatar column + content
    // indent) — a bare top-level element would sit flush left, off-grid from
    // every tool row.
    const grid = row.closest('.grid') as HTMLElement
    expect(grid?.className).toContain('grid-cols-[1.25rem_1fr_1.25rem]')
    // Dot settled: green, no pulsing. The classList check also fails when the
    // dot is missing entirely (optional chain yields undefined).
    const dot = row.querySelector('.text-green')
    expect(dot?.classList.contains('text-green')).toBe(true)
    expect(document.querySelectorAll('.heartbeat').length).toBe(0)
    // Body starts folded; the raw envelope renders on expand.
    const body = row.querySelector('.md-body') as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
    clickRowHeader(row)
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('</job-notification>')
    // No user bubble carries the raw envelope.
    expect(bubbleSpans().some(s => s.textContent?.includes('<system-reminder>'))).toBe(false)
  })

  it('R2: agent job envelope renders an agent summary', () => {
    mountHistory(AGENT_ENVELOPE)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (agent fork · completed)')
    expect(document.querySelectorAll('.heartbeat').length).toBe(0)
  })

  it('R3: message delivery renders a Reminder row with the sender', () => {
    mountHistory('<message from="main">\n报数：e3→main 第一条\n</message>')
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(rows[0].dataset.toolName).toBe('Reminder')
    expect(summaryTextOfRow(rows[0])).toBe(' (got a message from main)')
    clickRowHeader(rows[0])
    expect(document.body.textContent).toContain('报数：e3→main 第一条')
  })

  it('R4: clicking the row header expands the raw envelope', () => {
    mountHistory(BASH_ENVELOPE)
    const row = reminderRows()[0]
    const body = row.querySelector('.md-body') as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
    const header = row.querySelector('[role="button"]') as HTMLElement
    header.click()
    expect(body.classList.contains('hidden')).toBe(false)
    expect(body.textContent).toContain('<system-reminder>')
    expect(body.textContent).toContain('</job-notification>')
  })

  it('R5 negatives: plain and near-miss texts render as ordinary bubbles, never rows', () => {
    const negatives: Array<[string, string]> = [
      ['plain user message', 'plain hello'],
      ['space in sender', '<message from="a b">hi</message>'],
      ['empty sender', '<message from="">hi</message>'],
      ['non-ascii sender', '<message from="用户">hi</message>'],
      ['prose after closing tag', '<message from="main">\nhi\n</message> and then some prose'],
      ['unescaped reminder inside body', '<message from="main">\n<system-reminder>x</system-reminder>\n</message>'],
      ['missing closing tag', '<message from="x">hi'],
      ['quoted id without the envelope prefix', 'nope"main">\n</message>'],
    ]
    for (const [name, text] of negatives) {
      mountHistory(text)
      expect(reminderRows().length, name).toBe(0)
      expect(bubbleSpans().some(s => s.textContent === text), name).toBe(true)
    }
  })

  it('R5 negative: the 插话 envelope still renders unwrapped gray text', () => {
    mountHistory(QUEUED_ENVELOPE)
    expect(reminderRows().length).toBe(0)
    const spans = Array.from(document.querySelectorAll('span')).filter(
      s => s.textContent === 'check on that',
    )
    expect(spans.length).toBe(1)
    expect(spans[0].className).toContain('text-t3')
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })

  it('R5 positives: every real sender shape renders a Reminder row', () => {
    for (const sender of ['main', 'e2', 'agent-3', 'fork-2', 'azAZ09._-']) {
      mountHistory(`<message from="${sender}">\nhi\n</message>`)
      const rows = reminderRows()
      expect(rows.length, sender).toBe(1)
      expect(rows[0].dataset.toolName, sender).toBe('Reminder')
      expect(summaryTextOfRow(rows[0]), sender).toBe(` (got a message from ${sender})`)
    }
  })
})

// Bare single-layer envelopes (the post-wire-change persisted form) render
// the same Reminder rows, but the row BODY is the shared display text —
// machine fields and raw XML never appear.
describe('bare envelope rendering', () => {
  const BARE_BASH_XML =
    '<job-notification>\n<job-id>bg-1</job-id>\n<status>completed</status>\n' +
    '<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n</job-notification>'
  const BARE_AGENT_XML =
    '<job-notification>\n<job-id>agent-1</job-id>\n<tool-use-id>toolu_1</tool-use-id>\n' +
    '<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>1200</duration-ms>\n' +
    '<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n<summary>Background agent &quot;refactor auth&quot; completed</summary>\n' +
    '<result>\ndone &amp; green\n</result>\n</job-notification>'
  const BARE_STALL_XML =
    '<job-notification>\n<job-id>bg-2</job-id>\n' +
    '<summary>Background command &quot;read -p&quot; appears stalled</summary>\n</job-notification>' +
    '\nLast output:\nProceed? [y/n]\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.'

  function expandedBody(row: HTMLElement): HTMLElement {
    const body = row.querySelector('.md-body') as HTMLElement
    const header = row.querySelector('[role="button"]') as HTMLElement
    header.click()
    return body
  }

  it('bare command envelope: display-text body, no raw XML', () => {
    mountHistory(BARE_BASH_XML)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (command make check · exit 0)')
    const body = expandedBody(rows[0])
    expect(body.textContent).toContain('Background command "make check" completed (exit code 0)')
    expect(body.textContent).not.toContain('<job-notification>')
    expect(body.textContent).not.toContain('<job-id>')
    expect(body.textContent).not.toContain('<system-reminder>')
  })

  it('bare agent envelope: summary line + result body, machine fields hidden', () => {
    mountHistory(BARE_AGENT_XML)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (agent fork · completed)')
    const body = expandedBody(rows[0])
    expect(body.textContent).toContain('Background agent "refactor auth" completed')
    expect(body.textContent).toContain('done & green')
    for (const hidden of ['<job-id>', '<tool-use-id>', '<agent-type>', '<duration-ms>', '<tokens>', '<tool-uses>', '<status>']) {
      expect(body.textContent, hidden).not.toContain(hidden)
    }
  })

  it('bare stall envelope: body carries the raw tail and the boilerplate', () => {
    mountHistory(BARE_STALL_XML)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (command read -p)')
    const body = expandedBody(rows[0])
    expect(body.textContent).toContain('Proceed? [y/n]')
    expect(body.textContent).toContain('The command is likely blocked on an interactive prompt.')
    expect(body.textContent).not.toContain('<job-notification>')
  })

  it('message row body becomes the decoded delivered text', () => {
    mountHistory('<message from="main">\nsay &#34;hi&#34;\n</message>')
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (got a message from main)')
    const body = expandedBody(rows[0])
    expect(body.textContent).toContain('say "hi"')
    expect(body.textContent).not.toContain('&#34;')
    expect(body.textContent).not.toContain('<message from=')
  })

  it('live: a drained notification (mode notification) inserts the same display row', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvent({ type: 'query_start' })
    dispatchEvent({
      type: 'attachment',
      message: {
        role: 'user',
        timestamp: '2026-10-14T09:30:00Z',
        content: [{ type: 'text', text: BARE_BASH_XML }],
        attachment: { prompt: BARE_BASH_XML, source_uuid: 'notif-bg-1', mode: 'notification' },
      },
    })
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(summaryTextOfRow(rows[0])).toBe(' (command make check · exit 0)')
    const body = expandedBody(rows[0])
    expect(body.textContent).toContain('Background command "make check" completed (exit code 0)')
    expect(body.textContent).not.toContain('<job-notification>')
  })
})

describe('live reminder insertion', () => {
  // The engine's drained-reminder EventAttachment carries the persisted
  // message: the envelope in content, the wall clock in timestamp.
  function reminderEvent(text: string) {
    dispatchEvent({
      type: 'attachment',
      message: {
        role: 'user',
        timestamp: '2026-10-13T13:43:05Z',
        content: [{ type: 'text', text }],
        attachment: { prompt: text, source_uuid: 'job-bg-1', mode: 'job' },
      },
    })
  }


  it('streaming: a drained job reminder inserts a Reminder row at the stream tail', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvent({ type: 'query_start' })
    reminderEvent(BASH_ENVELOPE)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    const row = rows[0]
    expect(row.dataset.toolName).toBe('Reminder')
    expect(summaryTextOfRow(row)).toBe(' (command make check · exit 0)')
    // Duration slot shows the shared zero-duration default, matching every
    // tool row's slot shape; arrival time rides the wire prefix, not the UI.
    const durEl = row.querySelector('span.font-mono.text-xs') as HTMLElement
    expect(durEl.textContent).toBe(' 0s')
    // Row anatomy matches the settled tool path: green settled dot (no
    // pulse), collapsed body holding the raw envelope. The streaming shell's
    // own progress pulse is outside the row.
    const dot = row.querySelector('.text-green')
    expect(dot?.classList.contains('text-green')).toBe(true)
    expect(dot?.classList.contains('heartbeat')).toBe(false)
    // Row anatomy matches the settled tool path: green settled dot (no
    // pulse), folded body that renders the raw envelope on expand. The
    // streaming shell's own progress pulse is outside the row.
    const body = row.querySelector('.md-body') as HTMLElement
    expect(body.classList.contains('hidden')).toBe(true)
    clickRowHeader(row)
    expect(body.textContent).toContain('</job-notification>')
    // The live row shares the shell grid with tool rows, and no gray
    // queued-echo bubble carries the envelope — the row replaces it.
    expect(row.closest('.grid')).not.toBeNull()
    expect(bubbleSpans().some(s => s.textContent?.includes('<system-reminder>'))).toBe(false)
  })

  it('streaming: a drained message delivery inserts a Reminder row with the sender', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvent({ type: 'query_start' })
    reminderEvent('<message from="main">\n报数\n</message>')
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(rows[0].dataset.toolName).toBe('Reminder')
    expect(summaryTextOfRow(rows[0])).toBe(' (got a message from main)')
    clickRowHeader(rows[0])
    expect(document.body.textContent).toContain('报数')
  })

  it('idle: a reminder drained while no query streams renders as a committed row', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    reminderEvent(BASH_ENVELOPE)
    const rows = reminderRows()
    expect(rows.length).toBe(1)
    expect(rows[0].dataset.toolName).toBe('Reminder')
    expect(summaryTextOfRow(rows[0])).toBe(' (command make check · exit 0)')
    expect(rows[0].closest('.grid')).not.toBeNull()
    // The follow-up query_start opens its own streaming shell below the row.
    dispatchEvent({ type: 'query_start' })
    expect(reminderRows().length).toBe(1)
  })

  it('metadata push during the idle auto-turn renders the reminder exactly once', () => {
    // The idle-drain contract: history owns the row (snapshotQueryStart
    // indexes after the drain append), so the streamState snapshot must
    // not carry an echo copy — a session switch / takeover during the
    // auto-turn would render the row twice (history row + snapshot row).
    // The frame below is the fixed wire: history carries the envelope, the
    // snapshot only the auto-turn's streamed assistant text.
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatch({
      type: 'metadata',
      connect: { connected: true },
      config: { models: [], current: { provider: 'p', model: 'm' } },
      engines: { engines: [], activeID: 'main' },
      history: {
        messages: [{
          id: 'u1', role: 'user', text: '<message from="main">\nhello\n</message>',
          thinking: [], tools: [], usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
          error: '', status: 'done', startedAt: 1760424000000,
        }],
        nextCursor: '', hasMore: false,
      },
      stats: { usage: { input_tokens: 0, output_tokens: 0 } },
      snapshot: {
        blocks: [
          { kind: 'text', id: '', text: 'noted, thanks' },
        ],
      },
    })
    // The history row is the single reminder row; the snapshot's streamed
    // text renders without adding a second copy of the envelope.
    expect(reminderRows()).toHaveLength(1)
    expect(document.body.textContent).toContain('noted, thanks')
  })

  it('streaming: an agent-tagged reminder renders nothing in the main stream', () => {
    mount()
    dispatch({ type: 'connect_status', connected: true })
    dispatchEvent({ type: 'query_start' })
    dispatchEvent({
      type: 'attachment',
      message: {
        role: 'user',
        timestamp: '2026-10-13T13:43:05Z',
        content: [{ type: 'text', text: BASH_ENVELOPE }],
        attachment: { prompt: BASH_ENVELOPE, source_uuid: 'job-bg-1', mode: 'job' },
      },
      agent: { parent_tool_use_id: 'toolu_1', agent_type: 'fork', depth: 1 },
    })
    // TUI parity: Agent-tagged reminders surface only in the jobs feed's
    // parent-tool view — no Reminder row, no echo, no envelope text here.
    expect(reminderRows()).toHaveLength(0)
    expect(bubbleSpans().some(s => s.textContent?.includes('<system-reminder>'))).toBe(false)
    expect(document.body.textContent).not.toContain('<system-reminder>')
  })
})

test('reminder row carries no time suffix in the duration slot', async () => {
  const { appendReminderRow } = await import('./components/stream_blocks')
  const { classifyReminderMessage } = await import('./utils')
  const host = document.createElement('div')
  const row = classifyReminderMessage('<message from="agent-2">hi</message>')
  if (!row) throw new Error('classify failed')
  appendReminderRow(host, row, new Date('2026-10-14T20:02:19').getTime())
  expect(host.textContent).not.toMatch(/\d{2}:\d{2}/)
  const durEl = host.querySelector('span.font-mono.text-xs') as HTMLElement
  expect(durEl.textContent).toContain('0s')
})
