import { describe, it, expect, afterEach, vi } from 'vitest'
import { readFileSync } from 'fs'
import { resolve } from 'path'
import {
  parseDurationFromOutput,
  timeDividerLabel,
  classifyTool,
  isCollapsibleToolName,
  isCollapsibleBlock,
  summarize,
  bindLongPress,
  createPopupHost,
  unwrapQueuedReminder,
  unwrapEnvelopeDisplay,
  isInjectedReminderEnvelope,
  isJobNotificationEnvelope,
  isLegacyJobReminderEnvelope,
  classifyReminderMessage,
  countStreamBlocks,
  streamTextChars,
  VOICE_MSG_MARKER,
  stripVoiceMarker,
  formatDuration,
  formatDurationNs,
  formatTokenCount,
  nounFor,
  pluralize,
  positionAnchoredPopup,
  stripAnsi,
  createOutsideClick,
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
			isTask: false,
		})
	})

	it('classifies Grep and Glob as isSearch-only', () => {
		expect(classifyTool('Grep')).toEqual({
			isSearch: true,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			isTask: false,
		})
		expect(classifyTool('Glob')).toEqual({
			isSearch: true,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			isTask: false,
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
			isTask: false,
		})
	})

	it('classifies Web as isWeb-only', () => {
		expect(classifyTool('Web')).toEqual({
			isWeb: true,
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isTask: false,
		})
	})

	it('returns all-false for unknown names and never sets isList by name', () => {
		expect(classifyTool('Bash')).toEqual({
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			isTask: false,
		})
		expect(classifyTool('Unknown')).toEqual({
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			isTask: false,
		})
		// isList is backend-only (Bash ls/tree/du). No name maps to it.
		expect(classifyTool('Bash').isList).toBe(false)
	})

	it('classifies Task as isTask-only', () => {
		expect(classifyTool('Task')).toEqual({
			isTask: true,
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
		})
	})
})

describe('isCollapsibleToolName', () => {
	it('returns false for Bash', () => {
		expect(isCollapsibleToolName('Bash')).toBe(false)
	})

	it('returns true for Read, Grep, Glob, Lsp, Web, Task', () => {
		expect(isCollapsibleToolName('Read')).toBe(true)
		expect(isCollapsibleToolName('Grep')).toBe(true)
		expect(isCollapsibleToolName('Glob')).toBe(true)
		expect(isCollapsibleToolName('Lsp')).toBe(true)
		expect(isCollapsibleToolName('Web')).toBe(true)
		expect(isCollapsibleToolName('Task')).toBe(true)
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
			isTask: false,
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
			isTask: false,
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
			isTask: false,
			state: 'done',
			timingNs: 0,
			displayOutput: '',
			startedAt: 0,
			children: [],
		}
		expect(isCollapsibleBlock(b)).toBe(true)
	})

	it('returns true for Task block with all flags false (name fallback)', () => {
		const b: Block = {
			kind: 'tool',
			id: '',
			name: 'Task',
			summary: '',
			isSearch: false,
			isRead: false,
			isList: false,
			isLsp: false,
			isWeb: false,
			isTask: false,
			state: 'done',
			timingNs: 0,
			displayOutput: '',
			startedAt: 0,
			children: [],
		}
		expect(isCollapsibleBlock(b)).toBe(true)
	})
})

describe('summarize', () => {
	it('summarizes task-only group', () => {
		expect(summarize([{ name: 'Task' }, { name: 'Task' }, { name: 'Task' }])).toBe('3 Tasks')
	})

	it('summarizes mixed Grep + Task group', () => {
		expect(summarize([{ name: 'Grep' }, { name: 'Task' }, { name: 'Task' }, { name: 'Task' }])).toBe('1 Search, 3 Tasks')
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

describe('unwrapQueuedReminder', () => {
  const raw = 'wait, also run the tests'
  const envelope =
    '<system-reminder>\nThe user sent a new message while you were working:\n' + raw +
    '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

  it('unwraps the queued-message envelope', () => {
    expect(unwrapQueuedReminder(envelope)).toBe(raw)
  })

  it('rejects non-envelope text', () => {
    expect(unwrapQueuedReminder('')).toBeNull()
    expect(unwrapQueuedReminder('plain text')).toBeNull()
    expect(unwrapQueuedReminder(
      '<system-reminder>\nA background agent completed a job:\ndone\n</system-reminder>',
    )).toBeNull()
    expect(unwrapQueuedReminder(
      '[2026-10-05 11:00:00 CST] ' + envelope,
    )).toBeNull()
  })
})

// T4 — the TS half of the one-sniff-spec contract: the case lists here mirror
// pkg/utils/queued_message_test.go (T1) and envelope_display_test.go (T2)
// case-for-case, so a divergence between the Go and TS predicates fails in
// whichever implementation drifted. Envelope literals mirror real producer
// output (bash escapeXML named entities; agent xml.Escape numeric entities).
const BASH_DONE_XML =
  '<job-notification>\n<job-id>bg-1</job-id>\n<tool-use-id>toolu_9</tool-use-id>\n' +
  '<output-file>/tmp/bg-1.out</output-file>\n<status>completed</status>\n' +
  '<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n</job-notification>'

const BASH_STALL_XML =
  '<job-notification>\n<job-id>bg-2</job-id>\n' +
  '<summary>Background command &quot;read -p&quot; appears stalled</summary>\n</job-notification>' +
  '\nLast output:\nProceed? [y/n] <system-reminder>\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.'

const AGENT_XML =
  '<job-notification>\n<job-id>agent-1</job-id>\n<tool-use-id>toolu_1</tool-use-id>\n' +
  '<status>completed</status>\n<agent-type>fork</agent-type>\n<duration-ms>1200</duration-ms>\n' +
  '<tokens>42</tokens>\n<tool-uses>1</tool-uses>\n<summary>Background agent "refactor auth" completed</summary>\n' +
  '<result>\nall green &#34;ok&#34;&#xA;second line\n</result>\n</job-notification>'

const BASH_JOB_ENVELOPE =
  '<system-reminder>\nA background command completed:\n' +
  '<job-notification>\n<job-id>bg-1</job-id>\n<status>completed</status>\n' +
  '<summary>Background command &quot;make check&quot; completed (exit code 0)</summary>\n</job-notification>' +
  '\n</system-reminder>'

const AGENT_JOB_ENVELOPE =
  '<system-reminder>\nA background agent completed a job:\n' + AGENT_XML + '\n</system-reminder>'

const CHAHUA_ENVELOPE =
  '<system-reminder>\nThe user sent a new message while you were working:\nhi' +
  '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

// Unknown reminder-shaped envelopes must classify as null (render as a
// bubble) — the voice-face markers are retired, nothing else matches a bare
// system-reminder wrap.
describe('classifyReminderMessage: unknown envelopes stay null', () => {
  it('a user message that merely mentions retired phrases, or an unmatched envelope, must not classify', () => {
    expect(classifyReminderMessage('The user has switched to the voice interface, said nobody')).toBe(null)
    expect(classifyReminderMessage('<system-reminder>\nplant talk\n</system-reminder>')).toBe(null)
  })
})

describe('isInjectedReminderEnvelope', () => {
  it.each<[string, string, boolean]>([
    ['bash job envelope', BASH_JOB_ENVELOPE, true],
    ['agent job envelope', AGENT_JOB_ENVELOPE, true],
    ['bare bash job notification', BASH_DONE_XML, true],
    ['bare bash stall notification', BASH_STALL_XML, true],
    ['bare agent job notification', AGENT_XML, true],
    ['timestamped assembled form must not match', '[2026-10-13 13:43:05 CST] ' + BASH_JOB_ENVELOPE, false],
    ['job envelope missing suffix', '<system-reminder>\nA background command completed:\n<job-notification>\n</job-notification>', false],
    ['user queued envelope is not a job reminder', CHAHUA_ENVELOPE, false],
    ['message delivery is not a reminder envelope', '<message from="main">\nhi\n</message>', false],
    ['empty text', '', false],
  ])('%s', (_name, text, want) => {
    expect(isInjectedReminderEnvelope(text)).toBe(want)
  })
})

describe('isJobNotificationEnvelope', () => {
  it.each<[string, string, boolean]>([
    ['bash completion', BASH_DONE_XML, true],
    ['bash stall with joint and reminder-bearing tail', BASH_STALL_XML, true],
    ['agent shape', AGENT_XML, true],
    ['stall joint immediately after the head', '<job-notification>\n</job-notification>\nLast output: still waiting', true],
    ['timestamped prefix violates the index-0 rule', '[2026-10-14 09:00:00 CST] ' + BASH_DONE_XML, false],
    ['missing head newline', '<job-notification><job-id>bg-1</job-id>\n</job-notification>', false],
    ['missing terminator', '<job-notification>\n<job-id>bg-1</job-id>', false],
    ['unescaped system-reminder inside the field segment', '<job-notification>\n<summary>s</summary>\n<system-reminder>x</system-reminder>\n</job-notification>', false],
    ['system-reminder inside fields before a stall joint', '<job-notification>\n<system-reminder>x</system-reminder></job-notification>\nLast output: tail', false],
    ['prose before head', 'job done — ' + BASH_DONE_XML, false],
    ['插话 envelope', CHAHUA_ENVELOPE, false],
    ['message envelope', '<message from="main">\nhi\n</message>', false],
    ['legacy wrapped envelope', BASH_JOB_ENVELOPE, false],
    ['empty text', '', false],
  ])('%s', (_name, text, want) => {
    expect(isJobNotificationEnvelope(text)).toBe(want)
  })
})

describe('isLegacyJobReminderEnvelope', () => {
  it.each<[string, string, boolean]>([
    ['bash job envelope', BASH_JOB_ENVELOPE, true],
    ['agent job envelope', AGENT_JOB_ENVELOPE, true],
    ['bare bash form', BASH_DONE_XML, false],
    ['bare agent form', AGENT_XML, false],
    ['bare stall', BASH_STALL_XML, false],
    ['timestamped assembled form', '[2026-10-13 13:43:05 CST] ' + BASH_JOB_ENVELOPE, false],
    ['插话 envelope', CHAHUA_ENVELOPE, false],
    ['message envelope', '<message from="main">\nhi\n</message>', false],
    ['empty text', '', false],
  ])('%s', (_name, text, want) => {
    expect(isLegacyJobReminderEnvelope(text)).toBe(want)
  })
})

describe('unwrapEnvelopeDisplay', () => {
  it.each<[string, string, string | null]>([
    // Arm 1 — message delivery: decoded body only, no from line.
    [
      'message delivery with full escape alphabet and CJK',
      '<message from="e2">\nq&#34;q a&#39;l l&lt;t g&gt;t 配置 tab[&#x9;] nl[&#xA;] cr[&#xD;] amp &amp;\n</message>',
      'q"q a\'l l<t g>t 配置 tab[\t] nl[\n] cr[\r] amp &',
    ],
    [
      'literal entity text single-decodes',
      '<message from="main">\nsay &amp;quot;hi&amp;quot;\n</message>',
      'say &quot;hi&quot;',
    ],
    // Arm 2 — bare job notification: summary + result, machine fields hidden.
    ['agent notification with escaped result', AGENT_XML, 'Background agent "refactor auth" completed\n\nall green "ok"\nsecond line'],
    [
      'agent notification with empty result keeps summary only',
      '<job-notification>\n<job-id>agent-2</job-id>\n<status>completed</status>\n<agent-type>fork</agent-type>\n<summary>Background agent "cleanup" completed</summary>\n<result>\n\n</result>\n</job-notification>',
      'Background agent "cleanup" completed',
    ],
    ['bash completion is summary line only', BASH_DONE_XML, 'Background command "make check" completed (exit code 0)'],
    [
      'bash stall renders joint tail with boilerplate',
      '<job-notification>\n<job-id>bg-2</job-id>\n<summary>Background command &quot;read -p&quot; appears stalled</summary>\n</job-notification>\nLast output:\nProceed? [y/n]\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.',
      'Background command "read -p" appears stalled\n\nProceed? [y/n]\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.',
    ],
    [
      'bash stall tail stays verbatim, summary decodes',
      '<job-notification>\n<job-id>bg-3</job-id>\n<summary>Background command &quot;can\'t wait&quot; appears stalled</summary>\n</job-notification>\nLast output:\ngot &amp; input\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.',
      'Background command "can\'t wait" appears stalled\n\ngot &amp; input\n\nThe command is likely blocked on an interactive prompt. Kill this job and re-run with piped input (e.g., `echo y | command`) or a non-interactive flag if one exists.',
    ],
    ['bare notification without a summary tag falls back to inner segment', '<job-notification>\n<job-id>bg-9</job-id>\n</job-notification>', '<job-id>bg-9</job-id>'],
    ['unclosed summary tag falls back to inner segment', '<job-notification>\n<summary>partial\n</job-notification>', '<summary>partial'],
    // Arm 3 — single-layer system-reminder: body only.
    ['generic system-reminder body', '<system-reminder>\njust a note\n</system-reminder>', 'just a note'],
    ['插话 envelope matches arm 3 with boilerplate body', CHAHUA_ENVELOPE, 'The user sent a new message while you were working:\nhi\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.'],
    // Negatives.
    ['legacy wrapped job envelope', BASH_JOB_ENVELOPE, null],
    ['plain text', 'hello world', null],
    ['bare job-notification without head newline', '<job-notification><job-id>bg-1</job-id>\n</job-notification>', null],
    ['system-reminder head without tail', '<system-reminder>\njust a note', null],
    ['system-reminder without head newline', '<system-reminder>just a note</system-reminder>', null],
  ])('%s', (_name, text, want) => {
    expect(unwrapEnvelopeDisplay(text)).toBe(want)
  })
})

describe('countStreamBlocks', () => {
  it('counts an empty array as 0', () => {
    expect(countStreamBlocks([])).toBe(0)
  })
  it('counts top-level blocks of every kind', () => {
    const blocks: Block[] = [
      { kind: 'text', id: '', text: 'a' },
      { kind: 'user', id: '', text: 'b' },
      { kind: 'image', id: '', src: 'data:image/png;base64,xxx' },
      {
        kind: 'thinking', id: '', text: 'c', durationNs: 0, active: false, startedAt: 0,
      },
    ]
    expect(countStreamBlocks(blocks)).toBe(4)
  })
  it('counts nested tool children recursively', () => {
    const blocks: Block[] = [
      { kind: 'text', id: '', text: 'a' },
      {
        kind: 'tool', id: 't1', name: 'Agent', summary: '', isSearch: false,
        isRead: false, isList: false, isLsp: false, isWeb: false, isTask: false,
        state: 'running', timingNs: 0, displayOutput: '', startedAt: 0,
        children: [
          { kind: 'text', id: '', text: 'sub' },
          {
            kind: 'tool', id: 't2', name: 'Read', summary: '', isSearch: false,
            isRead: true, isList: false, isLsp: false, isWeb: false, isTask: false,
            state: 'done', timingNs: 0, displayOutput: 'out', startedAt: 0,
            children: [
              { kind: 'text', id: '', text: 'deep' },
            ],
          },
        ],
      },
    ]
    expect(countStreamBlocks(blocks)).toBe(5)
  })
})

describe('streamTextChars', () => {
  it('counts an empty array as 0', () => {
    expect(streamTextChars([])).toBe(0)
  })
  it('sums text/user/thinking text and tool displayOutput, images contribute 0', () => {
    const blocks: Block[] = [
      { kind: 'text', id: '', text: 'abc' },
      { kind: 'user', id: '', text: 'de' },
      { kind: 'image', id: '', src: 'data:image/png;base64,xxxxxx' },
      { kind: 'document', id: '', name: 'f.pdf', size: 10 },
      { kind: 'thinking', id: '', text: 'ghi', durationNs: 0, active: false, startedAt: 0 },
      {
        kind: 'tool', id: 't1', name: 'Bash', summary: '', isSearch: false,
        isRead: false, isList: false, isLsp: false, isWeb: false, isTask: false,
        state: 'done', timingNs: 0, displayOutput: 'jkl', startedAt: 0,
        children: [
          { kind: 'thinking', id: '', text: 'mn', durationNs: 0, active: false, startedAt: 0 },
          {
            kind: 'tool', id: 't2', name: 'Read', summary: '', isSearch: false,
            isRead: true, isList: false, isLsp: false, isWeb: false, isTask: false,
            state: 'done', timingNs: 0, displayOutput: 'opq', startedAt: 0, children: [],
          },
        ],
      },
    ]
    expect(streamTextChars(blocks)).toBe(3 + 2 + 3 + 3 + 2 + 3)
  })
  it('treats omitted wire fields (JSON omitempty) as empty, never throws', () => {
    const sparse = [
      { kind: 'thinking', id: '', durationNs: 0, active: false, startedAt: 0 },
      { kind: 'tool', id: 't1', name: 'Agent', state: 'running', children: undefined },
    ] as unknown as Block[]
    expect(streamTextChars(sparse)).toBe(0)
    expect(countStreamBlocks(sparse)).toBe(2)
  })
})

// The marker prefix lives in the Go connector (the server is what stores it);
// the TS mirror only renders and strips it. If the two literals drift, live
// rows and their replayed rows disagree and abort-rewind's text match misses.
describe('VOICE_MSG_MARKER mirror', () => {
  it('equals the Go const VoiceMsgMarker literal', () => {
    const goSrc = readFileSync(resolve(__dirname, '../../../pkg/context/voice_prompt.go'), 'utf-8')
    const m = goSrc.match(/^const VoiceMsgMarker = "((?:[^"\\]|\\.)*)"/m)
    if (!m) throw new Error('const VoiceMsgMarker not found in pkg/context/voice_prompt.go')
    // Go source literals are already JS-escapable for this constant.
    expect(JSON.parse(`"${m[1]}"`)).toBe(VOICE_MSG_MARKER)
  })
})

describe('stripVoiceMarker', () => {
  it('strips a leading marker', () => {
    expect(stripVoiceMarker('[voice] 你好。')).toBe('你好。')
  })
  it('leaves marker-less text untouched', () => {
    expect(stripVoiceMarker('你好')).toBe('你好')
  })
  it('strips the leading marker only', () => {
    expect(stripVoiceMarker('[voice] mid [voice] tail')).toBe('mid [voice] tail')
  })
  it('marker alone empties the text', () => {
    expect(stripVoiceMarker('[voice] ')).toBe('')
  })
  it('empty string stays empty', () => {
    expect(stripVoiceMarker('')).toBe('')
  })
})

describe('formatDuration', () => {
  it.each([
    [0.34, '0.3s'],
    [5, '5s'],
    [59, '59s'],
    [75, '1m 15s'],
    [3600, '1h 0m 0s'],
    [3661, '1h 1m 1s'],
  ])('%fs formats to %s', (seconds, want) => {
    expect(formatDuration(seconds)).toBe(want)
  })
})

describe('formatDurationNs', () => {
  it.each([
    [0, '0s'],
    [-5e9, '0s'],
    [0.3e9, '0.3s'],
    [1.2e9, '1s'],
    [90e9, '1m 30s'],
  ])('%sns formats to %s', (nanos, want) => {
    expect(formatDurationNs(nanos)).toBe(want)
  })
})

describe('formatTokenCount', () => {
  it.each([
    [0, '0'],
    [999, '999'],
    [1000, '1.0k'],
    [1024 * 1024, '1.0M'],
    [1024 * 1024 * 1024, '1.0G'],
    [1.5 * 1024 * 1024 * 1024, '1.5G'],
  ])('%d formats to %s', (n, want) => {
    expect(formatTokenCount(n)).toBe(want)
  })
})

describe('parseDurationFromOutput mutation kills', () => {
  it('JSON number payload decodes to a non-string and yields 0', () => {
    // decoded=42 is not a string and has no prefix: both arms must return 0
    // without the string-only guard crashing on startsWith.
    expect(parseDurationFromOutput('42')).toBe(0)
  })

  it('no-prefix string with a later bracket still yields 0', () => {
    expect(parseDurationFromOutput('aaaaaaaaaaaa 7]')).toBe(0)
  })

  it('inner "s" before the end (no trailing s) yields 0', () => {
    expect(parseDurationFromOutput('[Tool spent s1]')).toBe(0)
  })
})

describe('nounFor', () => {
  it('maps each tool noun exactly', () => {
    expect(nounFor('Bash', true)).toBe('List')
    expect(nounFor('Grep')).toBe('Search')
    expect(nounFor('Glob')).toBe('Search')
    expect(nounFor('Read')).toBe('Read')
    expect(nounFor('Lsp')).toBe('LSP')
    expect(nounFor('Web')).toBe('Web')
    expect(nounFor('Bash')).toBe('Bash')
  })
})

describe('pluralize', () => {
  it('s-suffix nouns take -es, not just ch-nouns', () => {
    expect(pluralize('Status', 2)).toBe('2 Statuses')
    expect(pluralize('Search', 2)).toBe('2 Searches')
    expect(pluralize('Read', 2)).toBe('2 Reads')
    expect(pluralize('Status', 1)).toBe('1 Status')
  })
})

describe('positionAnchoredPopup', () => {
  it('anchors left to the trigger and bottoms gap-above its top', () => {
    vi.stubGlobal('innerHeight', 800)
    try {
      const trigger = document.createElement('button')
      trigger.getBoundingClientRect = () =>
        ({ left: 50, top: 100, right: 150, bottom: 140, width: 100, height: 40, x: 50, y: 100, toJSON: () => ({}) }) as DOMRect
      const popup = document.createElement('div')
      positionAnchoredPopup(popup, trigger)
      expect(popup.style.left).toBe('50px')
      expect(popup.style.bottom).toBe('706px')
    } finally {
      vi.unstubAllGlobals()
    }
  })
})

describe('bindLongPress mouseleave', () => {
  it('mouseleave cancels the mouse-path timer before 500ms', () => {
    vi.useFakeTimers()
    try {
      const el = document.createElement('div')
      const onTrigger = vi.fn()
      bindLongPress(el, onTrigger, { useMouse: true })
      el.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
      vi.advanceTimersByTime(300)
      el.dispatchEvent(new MouseEvent('mouseleave', { bubbles: true }))
      vi.advanceTimersByTime(500)
      expect(onTrigger).not.toHaveBeenCalled()
    } finally {
      vi.useRealTimers()
    }
  })
})

describe('createPopupHost lazy append guard', () => {
  it('reopen does not move the panel behind elements appended after it', () => {
    document.body.innerHTML = ''
    const trigger = document.createElement('button')
    const panel = document.createElement('div')
    document.body.append(trigger, panel)
    const host = createPopupHost({ trigger, panel })
    host.open()
    const marker = document.createElement('div')
    document.body.appendChild(marker)
    host.close()
    host.open()
    const kids = [...document.body.children]
    expect(kids.indexOf(panel)).toBeLessThan(kids.indexOf(marker))
  })

  it('a second open() on an open host does not re-fire onOpen', () => {
    document.body.innerHTML = ''
    const trigger = document.createElement('button')
    const panel = document.createElement('div')
    const onOpen = vi.fn()
    const host = createPopupHost({ trigger, panel, onOpen })
    host.open()
    host.open()
    expect(onOpen).toHaveBeenCalledTimes(1)
  })

  it('a second close() on a closed host does not re-fire onClose', () => {
    document.body.innerHTML = ''
    const trigger = document.createElement('button')
    const panel = document.createElement('div')
    const onClose = vi.fn()
    const host = createPopupHost({ trigger, panel, onClose })
    host.open()
    host.close()
    host.close()
    expect(onClose).toHaveBeenCalledTimes(1)
  })
})

describe('timeDividerLabel boundary kills', () => {
  afterEach(() => {
    vi.useRealTimers()
  })

  it('exactly 6 days ago is still a weekday label', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 10, 0).getTime()
    vi.setSystemTime(now)
    const curr = now - 6 * 24 * 3600 * 1000
    const weekday = new Intl.DateTimeFormat('en-US', { weekday: 'long' }).format(curr)
    const label = timeDividerLabel(null, curr, 'en-US')
    expect(label.startsWith(weekday + ' ')).toBe(true)
  })

  it('same-year 10-day label omits the year; prior-year includes it (exact)', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 10, 0).getTime()
    vi.setSystemTime(now)
    const sameYear = now - 10 * 24 * 3600 * 1000
    expect(timeDividerLabel(null, sameYear, 'en-US')).toBe(
      new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric' }).format(sameYear) +
        ' ' +
        new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' }).format(sameYear),
    )
    vi.setSystemTime(new Date(2026, 0, 5, 10, 0).getTime())
    const priorYear = new Date(2025, 11, 25, 10, 0).getTime()
    expect(timeDividerLabel(null, priorYear, 'en-US')).toBe(
      new Intl.DateTimeFormat('en-US', { month: 'short', day: 'numeric', year: 'numeric' }).format(priorYear) +
        ' ' +
        new Intl.DateTimeFormat('en-US', { hour: 'numeric', minute: '2-digit' }).format(priorYear),
    )
  })

  it('omitted locale falls back to navigator.language', () => {
    vi.useFakeTimers()
    const now = new Date(2026, 6, 18, 10, 0).getTime()
    vi.setSystemTime(now)
    expect(timeDividerLabel(null, now)).toMatch(/Today|今天/)
  })

  it('with navigator absent the en-US fallback still formats', () => {
    vi.useFakeTimers()
    vi.stubGlobal('navigator', undefined)
    try {
      const now = new Date(2026, 6, 18, 10, 0).getTime()
      vi.setSystemTime(now)
      expect(timeDividerLabel(null, now)).toMatch(/^Today\b/)
    } finally {
      vi.unstubAllGlobals()
      vi.useRealTimers()
    }
  })
})

describe('unwrapQueuedReminder partial envelope', () => {
  it('prefix without suffix yields null', () => {
    expect(
      unwrapQueuedReminder(
        '<system-reminder>\nThe user sent a new message while you were working:\nhi',
      ),
    ).toBeNull()
  })
})

describe('message-delivery envelope negatives', () => {
  it('no head but an id-shaped span and suffix is not a delivery', () => {
    expect(unwrapEnvelopeDisplay('xxxxxxxxxxxxxxxxxxmain" tail </message>')).toBeNull()
  })

  it('head with no closing quote and no suffix is not a delivery', () => {
    expect(unwrapEnvelopeDisplay('<message from="abc')).toBeNull()
  })
})

describe('classifyReminderMessage command rows', () => {
  const wrap = (xml: string) =>
    '<system-reminder>\nA background command completed:\n' + xml + '\n</system-reminder>'

  it('no summary falls back to job-id in the summary text', () => {
    const row = classifyReminderMessage(
      wrap('<job-notification>\n<job-id>bg-7</job-id>\n</job-notification>'),
    )
    expect(row).toEqual({ name: 'Reminder', summary: 'command bg-7', body: wrap('<job-notification>\n<job-id>bg-7</job-id>\n</job-notification>') })
  })

  it('no quoted span and no job-id renders the bare "command" summary', () => {
    const row = classifyReminderMessage(
      wrap('<job-notification>\n<summary>stalled</summary>\n</job-notification>'),
    )
    expect(row?.name).toBe('Reminder')
    expect(row?.summary).toBe('command')
  })

  it('status without an exit code renders desc · status', () => {
    const row = classifyReminderMessage(
      wrap(
        '<job-notification>\n<job-id>bg-4</job-id>\n<status>running</status>\n' +
          '<summary>Background command &quot;read -p&quot; appears stalled</summary>\n</job-notification>',
      ),
    )
    expect(row?.name).toBe('Reminder')
    expect(row?.summary).toBe('command read -p · running')
  })

  it('multi-digit exit codes keep every digit', () => {
    const row = classifyReminderMessage(
      wrap(
        '<job-notification>\n<job-id>bg-8</job-id>\n' +
          '<summary>Background command &quot;big&quot; completed (exit code 10)</summary>\n</job-notification>',
      ),
    )
    expect(row?.summary).toBe('command big · exit 10')
  })
})

describe('classifyReminderMessage agent rows', () => {
  const wrapAgent = (xml: string) =>
    '<system-reminder>\nA background agent completed a job:\n' + xml + '\n</system-reminder>'

  it('agent-type plus status renders "agent <type> · <status>"', () => {
    const row = classifyReminderMessage(
      wrapAgent(
        '<job-notification>\n<job-id>agent-1</job-id>\n<status>completed</status>\n' +
          '<agent-type>fork</agent-type>\n<summary>Background agent &quot;r&quot; completed</summary>\n</job-notification>',
      ),
    )
    expect(row?.name).toBe('Reminder')
    expect(row?.summary).toBe('agent fork · completed')
  })

  it('missing agent-type falls back to the job-id noun', () => {
    const row = classifyReminderMessage(
      wrapAgent('<job-notification>\n<job-id>e1</job-id>\n</job-notification>'),
    )
    expect(row?.name).toBe('Reminder')
    expect(row?.summary).toBe('agent e1')
  })

  it('neither agent-type nor job-id renders the bare "agent" summary', () => {
    const row = classifyReminderMessage(
      wrapAgent('<job-notification>\n<summary>s</summary>\n</job-notification>'),
    )
    expect(row?.name).toBe('Reminder')
    expect(row?.summary).toBe('agent')
  })

  it('agent-type without a status omits the status half', () => {
    const row = classifyReminderMessage(
      wrapAgent(
        '<job-notification>\n<job-id>agent-2</job-id>\n<agent-type>fork</agent-type>\n</job-notification>',
      ),
    )
    expect(row?.summary).toBe('agent fork')
  })
})

describe('messageDeliveryDisplay newline trims', () => {
  it('unpadded body stays total; trailing-only newline is dropped', () => {
    expect(unwrapEnvelopeDisplay('<message from="m">ab</message>')).toBe('ab')
    expect(unwrapEnvelopeDisplay('<message from="m">ab\n</message>')).toBe('ab')
  })
})

describe('jobNotificationDisplay result padding', () => {
  const notif = (result: string) =>
    `<job-notification>\n<job-id>bg-9</job-id>\n<summary>S</summary>\n${result}\n</job-notification>`

  it('result without padding newlines keeps every character', () => {
    expect(unwrapEnvelopeDisplay(notif('<result>ab</result>'))).toBe('S\n\nab')
  })

  it('leading-only newline is trimmed once', () => {
    expect(unwrapEnvelopeDisplay(notif('<result>\nab</result>'))).toBe('S\n\nab')
  })

  it('whitespace-only result appends nothing', () => {
    expect(unwrapEnvelopeDisplay(notif('<result>\n  \n</result>'))).toBe('S')
  })
})

describe('unwrapEnvelopeDisplay arm-3 head', () => {
  it('prose before the bare suffix is not an arm-3 body', () => {
    expect(unwrapEnvelopeDisplay('blah\n</system-reminder>')).toBeNull()
  })
})

describe('streamTextChars empty text', () => {
  it('empty-string text contributes zero characters', () => {
    expect(streamTextChars([{ kind: 'text', id: '', text: '' }])).toBe(0)
  })
})

describe('formatDuration minute boundary', () => {
  it('exactly 60s formats as a minute pair', () => {
    expect(formatDuration(60)).toBe('1m 0s')
  })
})

describe('stripAnsi', () => {
  it('strips multi-parameter CSI sequences whole', () => {
    expect(stripAnsi('\x1b[31;42mdone\x1b[0m')).toBe('done')
  })
})

describe('createOutsideClick unit', () => {
  it('remove() disarms the handler so later outside mousedowns do nothing', () => {
    document.body.innerHTML = ''
    const trigger = document.createElement('button')
    const panel = document.createElement('div')
    document.body.append(trigger, panel)
    const onClose = vi.fn()
    const oc = createOutsideClick(trigger, panel, onClose)
    oc.add()
    oc.remove()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(onClose).not.toHaveBeenCalled()
  })
})
