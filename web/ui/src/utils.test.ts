import { describe, it, expect, afterEach, vi } from 'vitest'
import { parseDurationFromOutput, timeDividerLabel } from './utils'

describe('parseDurationFromOutput', () => {
	it('parses JSON-encoded string with prefix', () => {
		// Engine produces: json.Marshal("[Tool spent 1.2s]" + body)
		const wire = JSON.stringify('[Tool spent 1.2s]{"output":"done"}')
		expect(parseDurationFromOutput(wire)).toBe(1.2 * 1e9)
	})

	it('parses integer-second prefix', () => {
		const wire = JSON.stringify('[Tool spent 3s]ok')
		expect(parseDurationFromOutput(wire)).toBe(3 * 1e9)
	})

	it('parses already-decoded string (no JSON wrapping)', () => {
		expect(parseDurationFromOutput('[Tool spent 0.5s]result')).toBe(0.5 * 1e9)
	})

	it('returns 0 when no prefix present', () => {
		const wire = JSON.stringify('plain output')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})

	it('returns 0 for non-string input (objects from ToolWithWireFormat tools)', () => {
		expect(parseDurationFromOutput({ output: 'x' })).toBe(0)
		expect(parseDurationFromOutput(undefined)).toBe(0)
		expect(parseDurationFromOutput(null)).toBe(0)
	})

	it('returns 0 for malformed seconds value', () => {
		const wire = JSON.stringify('[Tool spent ABCs]result')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})

	it('returns 0 when close bracket missing', () => {
		const wire = JSON.stringify('[Tool spent 1.2s result without bracket')
		expect(parseDurationFromOutput(wire)).toBe(0)
	})
})

describe('timeDividerLabel', () => {
	afterEach(() => {
		vi.useRealTimers()
	})

	it('returns null for same-day sub-30-min gap', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 12, 0).getTime()
		vi.setSystemTime(now)
		const prev = new Date(2026, 6, 18, 11, 55).getTime() // 5 min before
		expect(timeDividerLabel(prev, now)).toBe(null)
	})

	it('returns time label for exactly 15 min gap (inclusive boundary)', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 12, 30).getTime()
		vi.setSystemTime(now)
		const prev = now - 15 * 60 * 1000 // exactly 15 min
		const label = timeDividerLabel(prev, now, 'en-US')
		expect(label).not.toBe(null)
		expect(label).toMatch(/^\d{1,2}:\d{2}\s*(AM|PM)?$/)
	})

	it('returns time label for 2-hour same-day gap in en-US', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 14, 30).getTime()
		vi.setSystemTime(now)
		const prev = now - 2 * 3600 * 1000
		const label = timeDividerLabel(prev, now, 'en-US')
		expect(label).toMatch(/^\d{1,2}:\d{2}\s*(AM|PM)?$/)
	})

	it('returns "Today <time>" for cross-day gap to today', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 10, 0).getTime()
		vi.setSystemTime(now)
		const prev = new Date(2026, 6, 17, 10, 0).getTime() // yesterday
		const label = timeDividerLabel(prev, now, 'en-US')
		expect(label).toMatch(/^Today\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns "Yesterday <time>" when curr is yesterday', () => {
		vi.useFakeTimers()
		vi.setSystemTime(new Date(2026, 6, 18, 10, 0).getTime())
		const curr = new Date(2026, 6, 17, 10, 0).getTime() // yesterday
		const prev = new Date(2026, 6, 16, 10, 0).getTime() // 2 days ago (prev < curr)
		const label = timeDividerLabel(prev, curr, 'en-US')
		expect(label).toMatch(/^Yesterday\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns weekday + time when curr is 3 days ago', () => {
		vi.useFakeTimers()
		vi.setSystemTime(new Date(2026, 6, 18, 10, 0).getTime())
		const curr = new Date(2026, 6, 15, 10, 0).getTime() // 3 days before today
		const prev = curr - 24 * 3600 * 1000 // 4 days ago
		const label = timeDividerLabel(prev, curr, 'en-US')
		const expected = new Intl.DateTimeFormat('en-US', { weekday: 'long' }).format(curr)
		expect(label.startsWith(expected)).toBe(true)
		expect(label).toMatch(/^(Saturday|Sunday|Monday|Tuesday|Wednesday|Thursday|Friday)\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns month-day + time when curr is 10 days ago', () => {
		vi.useFakeTimers()
		vi.setSystemTime(new Date(2026, 6, 18, 10, 0).getTime())
		const curr = new Date(2026, 6, 8, 10, 0).getTime() // 10 days before today
		const prev = curr - 24 * 3600 * 1000
		const label = timeDividerLabel(prev, curr, 'en-US')
		expect(label).toMatch(/^[A-Z][a-z]{2} \d{1,2}\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns date+time label when prev=null and curr is today (first-message anchor)', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 10, 0).getTime()
		vi.setSystemTime(now)
		const label = timeDividerLabel(null, now, 'en-US')
		expect(label).toMatch(/^Today\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns weekday+time when prev=null and curr is 5 days ago', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 10, 0).getTime()
		vi.setSystemTime(now)
		const curr = now - 5 * 24 * 3600 * 1000
		const label = timeDividerLabel(null, curr, 'en-US')
		expect(label).toMatch(/^(Saturday|Sunday|Monday|Tuesday|Wednesday|Thursday|Friday)\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('localizes labels to zh-CN', () => {
		vi.useFakeTimers()
		const curr = new Date(2026, 6, 18, 14, 30).getTime()
		vi.setSystemTime(curr)
		const prev2h = curr - 2 * 3600 * 1000
		// Time label: minute part always 30, tolerates both "14:30" and "下午2:30".
		expect(timeDividerLabel(prev2h, curr, 'zh-CN')).toMatch(/30/)
		// Weekday (3-day-old): contains CJK weekday char.
		const weekdayLabel = timeDividerLabel(null, curr - 3 * 24 * 3600 * 1000, 'zh-CN')
		expect(weekdayLabel).toMatch(/[星期周天]/)
		// Month-day (10-day-old): contains digit + 月 + 日.
		const monthDayLabel = timeDividerLabel(null, curr - 10 * 24 * 3600 * 1000, 'zh-CN')
		expect(monthDayLabel).toMatch(/\d/)
		expect(monthDayLabel).toMatch(/月/)
		expect(monthDayLabel).toMatch(/日/)
		// Today / Yesterday in zh-CN (now includes time suffix).
		expect(timeDividerLabel(null, curr, 'zh-CN')).toMatch(/^今天/)
		// curr=yesterday → label "昨天 <time>".
		expect(timeDividerLabel(curr - 48 * 3600 * 1000, curr - 24 * 3600 * 1000, 'zh-CN')).toMatch(/^昨天/)
	})

	it('returns date label (not time label) across midnight boundary', () => {
		// prev=23:59, curr=00:01 next day. dayDiff=1 → date label, not time label.
		const prev = new Date(2026, 6, 18, 23, 59).getTime()
		const curr = new Date(2026, 6, 19, 0, 1).getTime()
		vi.useFakeTimers()
		vi.setSystemTime(curr)
		const label = timeDividerLabel(prev, curr, 'en-US')
		expect(label).not.toBe(null)
		// Must NOT be a time-only label.
		expect(label).not.toMatch(/^\d{1,2}:\d{2}\s*(AM|PM)?$/)
	})
})
