import { describe, it, expect, vi } from 'vitest'
import { TokenRate } from './token_rate'

describe('TokenRate', () => {
  it('tool_param_delta text extends streamDuration', () => {
    vi.useFakeTimers()
    vi.setSystemTime(10000)
    const tr = new TokenRate()

    // Text burst: t=10000ms and t=10100ms
    tr.add('hello world response text part one')
    vi.setSystemTime(10100)
    tr.add('continuing the response here')

    const durBefore = tr.streamDurationMs()
    expect(durBefore).toBe(100) // 10100 - 10000 = 100ms

    // Tool param delta extends the burst: t=10200ms and t=10300ms
    vi.setSystemTime(10200)
    tr.add('{"command":"ls -la /home"}')
    vi.setSystemTime(10300)
    tr.add('{"description":"check files"}')

    const durAfter = tr.streamDurationMs()
    expect(durAfter).toBe(300) // 10300 - 10000 = 300ms (single burst, no gap)

    vi.useRealTimers()
  })

  it('rate is 0 when nothing added', () => {
    const tr = new TokenRate()
    expect(tr.rate()).toBe(0)
    expect(tr.streamDurationMs()).toBe(0)
  })

  it('reset clears all state', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    vi.setSystemTime(10000)
    tr.add('hello world')
    // Rate needs a span: a second sample 500ms later opens it.
    vi.setSystemTime(10500)
    tr.add('second chunk arrives')
    expect(tr.rate()).toBeGreaterThan(0)
    tr.reset()
    expect(tr.rate()).toBe(0)
    expect(tr.streamDurationMs()).toBe(0)
    vi.useRealTimers()
  })
})

describe('TokenRate burst span handling', () => {
  it('same-ms burst returns 0; a later sample opens the span', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    vi.setSystemTime(10000)
    tr.add('abc def ghi') // 3 tokens
    tr.add('jkl mno pqr') // 3 tokens, same ms — span 0
    // Old bug: the zero span floored to 1ms → 6/0.001 = 6000 t/s.
    expect(tr.rate()).toBe(0)
    vi.setSystemTime(10500)
    tr.add('stu vwx yz!') // 3 more tokens, 500ms later
    expect(tr.rate()).toBe(18) // 9 tokens / 500ms
    vi.useRealTimers()
  })

  it('single sample has no span: rate is 0, not tokens*1000', () => {
    vi.useFakeTimers()
    const tr = new TokenRate()
    vi.setSystemTime(10000)
    tr.add('abc def ghi')
    expect(tr.rate()).toBe(0)
    vi.useRealTimers()
  })
})
