import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  parseDurationFromOutput,
  timeDividerLabel,
  classifyTool,
  isCollapsibleToolName,
  isCollapsibleBlock,
  bindLongPress,
  createPopupHost,
} from './utils'
import type { Block } from './model'

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

	it('parses array-form content with text block carrying the prefix', () => {
		// Engine emits: json.Marshal([{type:"text",text:"[Tool spent Xs]..."}])
		const wire = JSON.stringify([{ type: 'text', text: '[Tool spent 1.5s]result' }])
		expect(parseDurationFromOutput(wire)).toBe(1.5 * 1e9)
	})

	it('returns 0 for array-form content with no duration prefix', () => {
		const wire = JSON.stringify([{ type: 'text', text: 'no duration prefix' }])
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

	it('returns date+time label for exactly 15 min gap (inclusive boundary)', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 12, 30).getTime()
		vi.setSystemTime(now)
		const prev = now - 15 * 60 * 1000 // exactly 15 min
		const label = timeDividerLabel(prev, now, 'en-US')
		expect(label).not.toBe(null)
		expect(label).toMatch(/^Today\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
	})

	it('returns date+time label for 2-hour same-day gap in en-US', () => {
		vi.useFakeTimers()
		const now = new Date(2026, 6, 18, 14, 30).getTime()
		vi.setSystemTime(now)
		const prev = now - 2 * 3600 * 1000
		const label = timeDividerLabel(prev, now, 'en-US')
		expect(label).toMatch(/^Today\b/)
		expect(label).toMatch(/\d{1,2}:\d{2}/)
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

	it('includes year when curr is in a prior calendar year', () => {
		vi.useFakeTimers()
		vi.setSystemTime(new Date(2026, 0, 5, 10, 0).getTime()) // Jan 5, 2026
		const curr = new Date(2025, 11, 25, 10, 0).getTime() // Dec 25, 2025
		const prev = curr - 24 * 3600 * 1000
		const label = timeDividerLabel(prev, curr, 'en-US')
		// Must contain the year token — same-year would only show month+day.
		expect(label).toMatch(/2025/)
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

	it('returns label when curr is OLDER than prev (loadHistory backward walk)', () => {
		// loadHistory prepends older pages. The caller may pass prev=newer
		// (lastDivAt from a forward walk) and curr=older (page-internal
		// message). The gap rule must be direction-agnostic: any wall-clock
		// delta >= 15min fires a divider.
		vi.useFakeTimers()
		vi.setSystemTime(new Date(2026, 6, 19, 12, 0).getTime())
		const newer = new Date(2026, 6, 19, 11, 50).getTime()
		const older = newer - 20 * 60 * 1000 // 20min before newer
		const label = timeDividerLabel(newer, older, 'en-US')
		expect(label).not.toBe(null)
	})
})

describe('classifyTool', () => {
	it('classifies Read as isRead-only', () => {
		expect(classifyTool('Read')).toEqual({
			isRead: true,
			isSearch: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
	})

	it('classifies Grep and Glob as isSearch-only', () => {
		expect(classifyTool('Grep')).toEqual({
			isSearch: true,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
		expect(classifyTool('Glob')).toEqual({
			isSearch: true,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
	})

	it('classifies Lsp as isLsp-only', () => {
		expect(classifyTool('Lsp').isLsp).toBe(true)
		expect(classifyTool('Lsp')).toEqual({
			isLsp: true,
			isSearch: false,
			isRead: false,
			isList: false,
			isWeb: false,
		})
	})

	it('classifies Web as isWeb-only', () => {
		expect(classifyTool('Web')).toEqual({
			isWeb: true,
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
		})
	})

	it('returns all-false for unknown names and never sets isList by name', () => {
		expect(classifyTool('Bash')).toEqual({
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
		expect(classifyTool('Unknown')).toEqual({
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
		// isList is backend-only (Bash ls/tree/du). No name maps to it.
		expect(classifyTool('Bash').isList).toBe(false)
	})
})

describe('isCollapsibleToolName', () => {
	it('returns false for Bash', () => {
		expect(isCollapsibleToolName('Bash')).toBe(false)
	})

	it('returns true for Read, Grep, Glob, Lsp, Web', () => {
		expect(isCollapsibleToolName('Read')).toBe(true)
		expect(isCollapsibleToolName('Grep')).toBe(true)
		expect(isCollapsibleToolName('Glob')).toBe(true)
		expect(isCollapsibleToolName('Lsp')).toBe(true)
		expect(isCollapsibleToolName('Web')).toBe(true)
	})
})

describe('isCollapsibleBlock', () => {
	it('returns false for non-tool blocks', () => {
		expect(isCollapsibleBlock({ kind: 'text', id: '', text: 'hi' } as Block)).toBe(false)
	})

	it('returns true when backend isList flag is set, even for unclassified name', () => {
		const b: Block = {
			kind: 'tool',
			id: '',
			name: 'Bash',
			summary: '',
			isSearch: false,
			isRead: false,
			isList: true,
			isLsp: false,
			isWeb: false,
			state: 'done',
			timingNs: 0,
			displayOutput: '',
			startedAt: 0,
			children: [],
		}
		expect(isCollapsibleBlock(b)).toBe(true)
	})

	it('returns false for tool with no flags and unclassified name', () => {
		const b: Block = {
			kind: 'tool',
			id: '',
			name: 'Bash',
			summary: '',
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			state: 'done',
			timingNs: 0,
			displayOutput: '',
			startedAt: 0,
			children: [],
		}
		expect(isCollapsibleBlock(b)).toBe(false)
	})

	it('returns true for tool with classified name even when flags are false', () => {
		const b: Block = {
			kind: 'tool',
			id: '',
			name: 'Read',
			summary: '',
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			state: 'done',
			timingNs: 0,
			displayOutput: '',
			startedAt: 0,
			children: [],
		}
		expect(isCollapsibleBlock(b)).toBe(true)
	})
})

describe('bindLongPress', () => {
	afterEach(() => {
		vi.useRealTimers()
	})

	it('fires onTrigger once after default 500ms touchstart hold', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(499)
		expect(onTrigger).not.toHaveBeenCalled()
		vi.advanceTimersByTime(1)
		expect(onTrigger).toHaveBeenCalledTimes(1)
	})

	it('cancel via touchend prevents onTrigger from firing', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(300)
		el.dispatchEvent(new TouchEvent('touchend', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).not.toHaveBeenCalled()
	})

	it('cancel via touchmove prevents onTrigger from firing', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(200)
		el.dispatchEvent(new TouchEvent('touchmove', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).not.toHaveBeenCalled()
	})

	it('cancel via pointercancel prevents onTrigger from firing (defensive)', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(200)
		el.dispatchEvent(new Event('pointercancel', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).not.toHaveBeenCalled()
	})

	it('useMouse binds mousedown path; default useMouse=false does not', () => {
		vi.useFakeTimers()
		const elMouse = document.createElement('div')
		const onTriggerMouse = vi.fn()
		bindLongPress(elMouse, onTriggerMouse, { useMouse: true })
		elMouse.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTriggerMouse).toHaveBeenCalledTimes(1)

		const elTouch = document.createElement('div')
		const onTriggerTouch = vi.fn()
		bindLongPress(elTouch, onTriggerTouch)
		elTouch.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTriggerTouch).not.toHaveBeenCalled()
	})

	it('mouseup cancels mouse-path timer before 500ms', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger, { useMouse: true })
		el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		vi.advanceTimersByTime(300)
		el.dispatchEvent(new MouseEvent('mouseup', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).not.toHaveBeenCalled()
	})

	it('touchstart+mousedown dedup: onTrigger fires exactly once', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger, { useMouse: true })
		// Browser synthesizes mousedown after touchstart on touch devices.
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).toHaveBeenCalledTimes(1)
	})

	it('consumeTrigger returns true once after fire, then false until next fire', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		const lp = bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(lp.consumeTrigger()).toBe(true)
		// Second read without a new fire returns false.
		expect(lp.consumeTrigger()).toBe(false)
		// Subsequent clicks can call consumeTrigger — still false.
		expect(lp.consumeTrigger()).toBe(false)
	})

	it('second long-press fires after the first one ends', () => {
		// Trigger flag must not latch across gestures.
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).toHaveBeenCalledTimes(1)
		el.dispatchEvent(new TouchEvent('touchend', { bubbles: true }))
		// Second long-press on a fresh gesture must fire again.
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(500)
		expect(onTrigger).toHaveBeenCalledTimes(2)
	})

	it('cancel() clears the pending timer', () => {
		vi.useFakeTimers()
		const el = document.createElement('div')
		const onTrigger = vi.fn()
		const lp = bindLongPress(el, onTrigger)
		el.dispatchEvent(new TouchEvent('touchstart', { bubbles: true }))
		vi.advanceTimersByTime(200)
		lp.cancel()
		vi.advanceTimersByTime(500)
		expect(onTrigger).not.toHaveBeenCalled()
	})
})

describe('createPopupHost', () => {
	function setup() {
		document.body.innerHTML = ''
		const trigger = document.createElement('button')
		const panel = document.createElement('div')
		panel.classList.add('hidden')
		document.body.appendChild(trigger)
		return { trigger, panel }
	}

	it('open lazily appends panel and removes hidden; isOpen reflects state', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		expect(host.isOpen()).toBe(false)
		host.open()
		expect(host.isOpen()).toBe(true)
		expect(panel.classList.contains('hidden')).toBe(false)
		expect(panel.parentElement).toBe(document.body)
	})

	it('open is idempotent (no duplicate append)', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		host.open()
		host.open()
		expect(document.body.querySelectorAll('div').length).toBeGreaterThanOrEqual(1)
		// panel is still a single direct child of body.
		let count = 0
		for (const child of document.body.children) {
			if (child === panel) count++
		}
		expect(count).toBe(1)
	})

	it('close re-adds hidden and reports closed; does NOT remove from DOM', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		host.open()
		host.close()
		expect(host.isOpen()).toBe(false)
		expect(panel.classList.contains('hidden')).toBe(true)
		// Critical contract: close keeps the panel parented so reopen is cheap.
		expect(panel.parentElement).toBe(document.body)
	})

	it('close is idempotent', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		host.open()
		host.close()
		host.close()
		expect(host.isOpen()).toBe(false)
	})

	it('toggle cycles open→close→open', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		expect(host.isOpen()).toBe(false)
		host.toggle()
		expect(host.isOpen()).toBe(true)
		host.toggle()
		expect(host.isOpen()).toBe(false)
		host.toggle()
		expect(host.isOpen()).toBe(true)
	})

	it('outside-click closes after open', () => {
		const { trigger, panel } = setup()
		const onClose = vi.fn()
		const host = createPopupHost({ trigger, panel, onClose })
		host.open()
		document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		expect(host.isOpen()).toBe(false)
		expect(onClose).toHaveBeenCalledTimes(1)
	})

	it('outside-click is removed after close (no double onClose)', () => {
		const { trigger, panel } = setup()
		const onClose = vi.fn()
		const host = createPopupHost({ trigger, panel, onClose })
		host.open()
		host.close()
		// host.close() already fired onClose once.
		expect(onClose).toHaveBeenCalledTimes(1)
		document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		// The mousedown after close must NOT fire onClose again.
		expect(onClose).toHaveBeenCalledTimes(1)
	})

	it('mousedown on trigger does NOT close (trigger excluded)', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		host.open()
		trigger.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		expect(host.isOpen()).toBe(true)
	})

	it('mousedown inside panel does NOT close', () => {
		const { trigger, panel } = setup()
		const host = createPopupHost({ trigger, panel })
		host.open()
		panel.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		expect(host.isOpen()).toBe(true)
	})

	it('onOpen fires after panel shown but before outside-click armed', () => {
		const { trigger, panel } = setup()
		// If outside-click were armed BEFORE onOpen returns, dispatching
		// mousedown inside onOpen would close the panel and onClose would
		// fire. We assert neither happens.
		let openOrder = ''
		const onOpen = vi.fn(() => {
			openOrder += 'onOpen'
			document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
		})
		const onClose = vi.fn(() => {
			openOrder += 'onClose'
		})
		const host = createPopupHost({ trigger, panel, onOpen, onClose })
		host.open()
		expect(onOpen).toHaveBeenCalledTimes(1)
		// onOpen ran first; the mousedown it dispatched must NOT close.
		expect(host.isOpen()).toBe(true)
		expect(openOrder).toBe('onOpen')
	})
})
