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
    const tr = new TokenRate()
    tr.add('hello world')
    expect(tr.rate()).toBeGreaterThan(0)
    tr.reset()
    expect(tr.rate()).toBe(0)
    expect(tr.streamDurationMs()).toBe(0)
  })
})
