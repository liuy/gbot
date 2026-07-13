import { describe, it, expect, vi } from 'vitest'
import { createAsk } from './ask'

describe('createAsk', () => {
  it('renders title approve · <tool_name>', () => {
    const a = createAsk(
      { id: '1', kind: 'permission', tool_name: 'Bash' },
      () => {},
    )
    expect(a.root.textContent).toContain('approve · Bash')
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
    expect(spy).toHaveBeenCalledWith('allow')
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
    expect(spy).toHaveBeenCalledWith('allow_always')
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
    expect(spy).toHaveBeenCalledWith('deny')
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
})
