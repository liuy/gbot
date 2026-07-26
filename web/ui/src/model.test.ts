import { describe, it, expect } from 'vitest'
import { newUserMessage } from './model'

describe('newUserMessage', () => {
  it('builds a done user message with a text block for non-empty text', () => {
    const before = Date.now()
    const m = newUserMessage('hi')
    const after = Date.now()
    expect(m.id).toBe('')
    expect(m.role).toBe('user')
    expect(m.blocks).toEqual([{ kind: 'text', id: '', text: 'hi' }])
    expect(m.usage).toEqual({
      inputTokens: 0,
      outputTokens: 0,
      cacheRead: 0,
      cacheCreation: 0,
    })
    expect(m.error).toBe('')
    expect(m.status).toBe('done')
    expect(typeof m.startedAt).toBe('number')
    expect(m.startedAt).toBeGreaterThanOrEqual(before)
    expect(m.startedAt).toBeLessThanOrEqual(after)
  })

  it('returns empty blocks when text is empty and no blocks are passed', () => {
    const m = newUserMessage('')
    expect(m.blocks).toEqual([])
  })

  it('returns caller-supplied blocks unchanged, ignoring text default', () => {
    const blocks = [{ kind: 'image' as const, id: '', src: 'x' }]
    const m = newUserMessage('', blocks)
    expect(m.blocks).toBe(blocks)
    expect(m.blocks).toEqual([{ kind: 'image', id: '', src: 'x' }])
  })

  it('ignores text when blocks are explicitly provided', () => {
    const blocks = [{ kind: 'image' as const, id: '', src: 'y' }]
    const m = newUserMessage('ignored', blocks)
    expect(m.blocks).toEqual([{ kind: 'image', id: '', src: 'y' }])
  })
})
