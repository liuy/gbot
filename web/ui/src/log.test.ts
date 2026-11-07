import { describe, it, expect, vi, afterEach } from 'vitest'
import { getDebugLogs, pushDebugLog, } from './log'

// log.ts wraps the console methods at module scope; this file imports nothing
// else, so the buffer starts empty per vitest worker (per-file isolation).
// The cap test deliberately runs LAST: it floods the buffer with 1000 lines
// that would drown the marker lookups of the earlier tests.

afterEach(() => {
  vi.useRealTimers()
})

describe('formatArgs', () => {
  it('renders objects as JSON', () => {
    pushDebugLog({ x: 1 })
    expect(getDebugLogs().at(-1)).toMatch(/ \{"x":1\}$/)
  })

  it('renders nested arrays as JSON, not comma-joined String form', () => {
    pushDebugLog([1, 2])
    // String([1,2]) === '1,2' — JSON keeps the brackets.
    expect(getDebugLogs().at(-1)).toMatch(/ \[1,2\]$/)
  })

  it('renders null via String (JSON.stringify(null) and String(null) agree)', () => {
    pushDebugLog(null)
    expect(getDebugLogs().at(-1)).toMatch(/ null$/)
  })

  it('renders a circular object through the String fallback, never undefined', () => {
    const a: Record<string, unknown> = {}
    a.self = a
    pushDebugLog(a)
    // JSON.stringify throws on the cycle → catch arm → '[object Object]'.
    expect(getDebugLogs().at(-1)).toMatch(/ \[object Object\]$/)
  })

  it('joins multiple args with single spaces', () => {
    pushDebugLog('a', 1, true)
    expect(getDebugLogs().at(-1)).toMatch(/ a 1 true$/)
  })
})

describe('capture timestamp', () => {
  it('stamps HH:MM:SS.mmm with zero padding on every field', () => {
    vi.useFakeTimers()
    // 01:04:05.006 UTC is single-digit in both a UTC runner and a UTC+8
    // runner (09:04:05.006), so the padding asserts hold in any timezone;
    // minute-granular offsets keep :05.006 intact as well.
    vi.setSystemTime(new Date('2026-01-01T01:04:05.006Z'))
    pushDebugLog('ts-marker')
    expect(getDebugLogs().at(-1)).toMatch(/^0?1:04:05\.006 ts-marker$|^09:04:05\.006 ts-marker$/)
    // The strict shape: exactly two digit columns for hour and minute.
    expect(getDebugLogs().at(-1)).toMatch(/^\d{2}:\d{2}:05\.006 /)
  })
})

describe('getDebugLogs', () => {
  it('returns a defensive copy: caller mutation cannot corrupt the buffer', () => {
    pushDebugLog('copy-marker')
    const snapshot = getDebugLogs()
    snapshot.push('injected-junk')
    snapshot[0] = 'overwritten-junk'
    const fresh = getDebugLogs()
    expect(fresh).not.toContain('injected-junk')
    expect(fresh).not.toContain('overwritten-junk')
    expect(fresh.find((l) => /copy-marker$/.test(l))).toBeTruthy()
  })
})

describe('console wiring', () => {
  it('the wrapped console.log feeds the buffer and still calls through', () => {
    const spy = vi.spyOn(console, 'log')
    console.log('wired-marker', 42)
    expect(spy).toHaveBeenCalled()
    expect(getDebugLogs().find((l) => / wired-marker 42$/.test(l))).toBeTruthy()
    spy.mockRestore()
  })

  it('the wrapped console.error feeds the buffer', () => {
    const spy = vi.spyOn(console, 'error')
    console.error('err-marker')
    expect(getDebugLogs().find((l) => / err-marker$/.test(l))).toBeTruthy()
    spy.mockRestore()
  })
})

describe('buffer cap', () => {
  it('keeps the newest MAX_LOGS lines, dropping from the head', () => {
    for (let i = 0; i < 1002; i++) pushDebugLog(`cap-${i}`)
    const logs = getDebugLogs()
    expect(logs.length).toBe(1000)
    expect(logs[0]).toMatch(/ cap-2$/)
    expect(logs.at(-1)).toMatch(/ cap-1001$/)
  })

  it('does not drop at exactly MAX_LOGS entries', () => {
    // A fresh worker module would be ideal; the previous test already filled
    // the buffer, so push to the next exact boundary instead: after cap-1001
    // the buffer sits at 1000; two more pushes make 1002 → cap back to 1000,
    // first line cap-4. The exact-boundary drop (> vs >=) shifts the head
    // marker by one line, which the assertion pins.
    for (let i = 1002; i < 1004; i++) pushDebugLog(`cap-${i}`)
    const logs = getDebugLogs()
    expect(logs.length).toBe(1000)
    expect(logs[0]).toMatch(/ cap-4$/)
    expect(logs.at(-1)).toMatch(/ cap-1003$/)
  })
})
