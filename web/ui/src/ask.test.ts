import { describe, it, expect, vi } from 'vitest'
import { createAsk } from './ask'

describe('createAsk', () => {
  it('renders title Approve · <tool_name>', () => {
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      () => {},
    )
    expect(a.root.textContent).toContain('Approve · Bash')
  })

  it('uses message as label when provided', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'Bash',
        message: 'run ls?',
      },
      () => {},
    )
    expect(a.root.textContent).toContain('run ls?')
  })

  it('formatCommand: {command} renders command in pre', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'Bash',
        input: { command: 'ls -la' },
      },
      () => {},
    )
    const pre = a.root.querySelector('pre')
    expect(pre?.textContent).toBe('ls -la')
  })

  it('formatCommand: {path} renders path', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'Read',
        input: { path: '/x' },
      },
      () => {},
    )
    expect(a.root.querySelector('pre')?.textContent).toBe('/x')
  })

  it('formatCommand: {file_path} renders file_path', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'Read',
        input: { file_path: '/y' },
      },
      () => {},
    )
    expect(a.root.querySelector('pre')?.textContent).toBe('/y')
  })

  it('formatCommand: {pattern} renders pattern', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'Grep',
        input: { pattern: 'z' },
      },
      () => {},
    )
    expect(a.root.querySelector('pre')?.textContent).toBe('z')
  })

  it('formatCommand: unknown shape renders compact JSON', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'X',
        input: { foo: 1 },
      },
      () => {},
    )
    expect(a.root.querySelector('pre')?.textContent).toContain('"foo": 1')
  })

  it('formatCommand: string input renders verbatim', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'permission',
        tool_name: 'X',
        input: 'plain string',
      },
      () => {},
    )
    expect(a.root.querySelector('pre')?.textContent).toBe('plain string')
  })

  it('formatCommand: null input renders no pre', () => {
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'X' },
      () => {},
    )
    expect(a.root.querySelector('pre')).toBeNull()
  })

  it('Allow Once button calls respond("allow")', () => {
    const spy = vi.fn()
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      spy,
    )
    const btns = a.root.querySelectorAll('button')
    const allow = Array.from(btns).find((b) => b.textContent === 'Allow Once')!
    allow.click()
    expect(spy).toHaveBeenCalledWith({ decision: 'allow' })
  })

  it('Allow This Session button calls respond("allow_always")', () => {
    const spy = vi.fn()
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      spy,
    )
    const btns = a.root.querySelectorAll('button')
    const allow =
      Array.from(btns).find((b) => b.textContent === 'Allow This Session')!
    allow.click()
    expect(spy).toHaveBeenCalledWith({ decision: 'allow_always' })
  })

  it('Deny button calls respond("deny") and has red classes', () => {
    const spy = vi.fn()
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      spy,
    )
    const deny = Array.from(a.root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Deny',
    )!
    expect(deny.className).toContain('text-red')
    deny.click()
    expect(spy).toHaveBeenCalledWith({ decision: 'deny' })
  })

  it('renders exactly three action buttons', () => {
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      () => {},
    )
    const actions = a.root.querySelector('.flex.gap-2')!
    expect(actions.children.length).toBe(3)
  })

  it('close() removes root from parent', () => {
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      () => {},
    )
    const wrap = document.createElement('div')
    wrap.appendChild(a.root)
    expect(wrap.children.length).toBe(1)
    a.close()
    expect(wrap.children.length).toBe(0)
  })

  it('input ask renders prompt and input field (not permission buttons)', () => {
    const a = createAsk(
      {
        id: '1',
        kind: 'input',
        tool_name: 'Bash',
        prompt: '[sudo] password for yliu:',
        masked: true,
      },
      () => {},
    )
    expect(a.root.textContent).toContain('[sudo] password for yliu:')
    // No permission buttons.
    expect(a.root.textContent).not.toContain('Allow Once')
    expect(a.root.textContent).not.toContain('Deny')
    // Has input field (password type for masked).
    const input = a.root.querySelector('input')
    expect(input).toBeInstanceOf(HTMLInputElement)
    expect((input as HTMLInputElement).type).toBe('password')
    // Has Submit button.
    const submit = Array.from(a.root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Submit',
    )
    expect(submit).toBeInstanceOf(HTMLButtonElement)
  })

  it('input ask uses plain text input when masked=false', () => {
    const a = createAsk(
      { id: '1', kind: 'input', tool_name: 'Bash', prompt: 'name?' },
      () => {},
    )
    expect((a.root.querySelector('input') as HTMLInputElement).type).toBe('text')
  })

  it('input ask submit sends {text, aborted:false}', () => {
    const spy = vi.fn()
    const a = createAsk(
      {
        id: '1',
        kind: 'input',
        tool_name: 'Bash',
        prompt: 'pw?',
        masked: true,
      },
      spy,
    )
    const input = a.root.querySelector('input') as HTMLInputElement
    input.value = 'hunter2'
    const submit = Array.from(a.root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Submit',
    )!
    submit.click()
    expect(spy).toHaveBeenCalledWith({ text: 'hunter2', aborted: false })
  })

  it('input ask Enter key submits text', () => {
    const spy = vi.fn()
    const a = createAsk(
      { id: '1', kind: 'input', tool_name: 'Bash', prompt: 'pw?' },
      spy,
    )
    const input = a.root.querySelector('input') as HTMLInputElement
    input.value = 'secret'
    input.dispatchEvent(new KeyboardEvent('keydown', { key: 'Enter' }))
    expect(spy).toHaveBeenCalledWith({ text: 'secret', aborted: false })
  })

  it('input ask Cancel button sends {aborted:true, timeout:false}', () => {
    const spy = vi.fn()
    const a = createAsk(
      { id: '1', kind: 'input', tool_name: 'Bash', prompt: 'pw?' },
      spy,
    )
    const cancel = Array.from(a.root.querySelectorAll('button')).find(
      (b) => b.textContent === 'Cancel',
    ) as HTMLButtonElement
    cancel.click()
    expect(spy).toHaveBeenCalledWith({ text: '', aborted: true, timeout: false })
  })

  it('input ask deadline countdown auto-aborts at zero', () => {
    vi.useFakeTimers()
    const spy = vi.fn()
    const pastDeadline = Math.floor(Date.now() / 1000) - 5 // 5s ago
    createAsk(
      {
        id: '1',
        kind: 'input',
        tool_name: 'Bash',
        prompt: 'pw?',
        deadline_unix: pastDeadline,
      },
      spy,
    )
    vi.advanceTimersByTime(1100)
    expect(spy).toHaveBeenCalledWith({ text: '', aborted: true, timeout: true })
    vi.useRealTimers()
  })

  it('input ask without deadline shows no countdown text', () => {
    const a = createAsk(
      { id: '1', kind: 'input', tool_name: 'Bash', prompt: 'pw?' },
      () => {},
    )
    const countdown = a.root.querySelector('span.text-t3')
    expect(countdown?.textContent).toBe('')
  })

  it('input ask with future deadline shows timeout in Ns', () => {
    const future = Math.floor(Date.now() / 1000) + 30
    const a = createAsk(
      {
        id: '1',
        kind: 'input',
        tool_name: 'Bash',
        prompt: 'pw?',
        deadline_unix: future,
      },
      () => {},
    )
    const countdown = a.root.querySelector('span.text-t3')
    expect(countdown?.textContent).toMatch(/^timeout in \d+s$/)
    a.close()
  })
})
