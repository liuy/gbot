// Mirrors pkg/utils/duration.go:10. Input is SECONDS (caller converts ns).
// <1s: "0.3s", 1-59s: "Xs", 60s-59m: "Xm Ys", >=1h: "Xh Ym Zs".
import { popupPanel, anchoredPopup } from './styles/recipes'

export function formatDuration(seconds: number): string {
  const s = Math.floor(seconds)
  if (s < 1) {
    return seconds.toFixed(1) + 's'
  }
  if (s < 60) {
    return s + 's'
  }
  if (s < 3600) {
    const m = Math.floor(s / 60)
    const sec = s % 60
    return m + 'm ' + sec + 's'
  }
  const h = Math.floor(s / 3600)
  const m = Math.floor((s % 3600) / 60)
  const sec = s % 60
  return h + 'h ' + m + 'm ' + sec + 's'
}

// Mirrors pkg/types/text.go:84. Base 1024.
// <1000: raw, >=1k: "1.2k", >=1M: "2.3M", >=1G: "4.5G".
export function formatTokenCount(n: number): string {
  if (n < 1000) {
    return String(n)
  }
  if (n < 1024 * 1024) {
    return (n / 1024).toFixed(1) + 'k'
  }
  if (n < 1024 * 1024 * 1024) {
    return (n / (1024 * 1024)).toFixed(1) + 'M'
  }
  return (n / (1024 * 1024 * 1024)).toFixed(1) + 'G'
}

// Formats a duration given a nanosecond value (toolOutput prefix-parse, thinking.duration).
export function formatDurationNs(nanos: number): string {
  if (!nanos || nanos <= 0) return '0s'
  return formatDuration(nanos / 1e9)
}

// Mirrors pkg/tool/render.go StripANSI: removes ANSI escape sequences so the
// web frontend can re-style displayOutput independently of terminal colors.
export function stripAnsi(s: string): string {
  // eslint-disable-next-line no-control-regex
  return s.replace(/\x1b\[[0-9;]*[a-zA-Z]/g, '')
}

// Extracts the [Tool spent Xs] prefix that pkg/engine/runTools.go:prependDuration
// adds to tool_result Output JSON. Returns the duration in nanoseconds, or 0 if
// no prefix is present. Mirrors renderToolOutput in pkg/connector/wui/connector.go.
//
// Handles both string form (legacy: JSON-encoded string) and array form
// (current: [{type:"text",text:"[Tool spent Xs]..."}]). Legacy string-form
// branch stays for any persisted sessions that reach the frontend unchanged.
export function parseDurationFromOutput(output: unknown): number {
	if (typeof output !== 'string') return 0
	let s = output
	try {
		const decoded = JSON.parse(s)
		if (Array.isArray(decoded)) {
			// Array form: find first text block.
			const textBlock = decoded.find(
				(b: { type?: string; text?: string }) => b?.type === 'text' && typeof b.text === 'string'
			) as { text: string } | undefined
			if (!textBlock) return 0
			s = textBlock.text
		} else if (typeof decoded === 'string') {
			s = decoded
		}
	} catch {
		// Not JSON — try the raw string (defensive; should not normally happen).
	}
	const prefix = '[Tool spent '
	if (!s.startsWith(prefix)) return 0
	const closeBracket = s.indexOf(']', prefix.length)
	if (closeBracket < 0) return 0
	const inner = s.slice(prefix.length, closeBracket)
	const numPart = inner.replace(/s$/, '')
	const sec = parseFloat(numPart)
	if (isNaN(sec)) return 0
	return sec * 1e9
}

// Whether a tool is collapsible (groups with adjacent collapsible tools).
// Shared by streamDom.ts (streaming) and MessageComponent.tsx (committed).
// KEEP IN SYNC with the rules below — both call sites must agree.
export function isCollapsibleToolName(name: string): boolean {
  return name === 'Grep' || name === 'Glob' || name === 'Read' || name === 'Lsp' || name === 'LSP' || name === 'Web'
}

// Noun for tool group summary: "Search" for Grep/Glob, "Read" for Read, etc.
// isList flag: Bash tools with is_list:true (ls/tree/du) display as "List".
export function nounFor(name: string, isList = false): string {
	if (isList) return 'List'
	if (name === 'Grep' || name === 'Glob') return 'Search'
	if (name === 'Read') return 'Read'
	if (name === 'Lsp' || name === 'LSP') return 'LSP'
	if (name === 'Web') return 'Web'
	return name
}

// Pluralize: "Search"→"Searches", "Read"→"Reads", "LSP"→"LSPs".
// Shared by ToolGroup.tsx and streamDom.ts.
export function pluralize(noun: string, count: number): string {
	if (count <= 1) return `${count} ${noun}`
	if (noun.endsWith('ch') || noun.endsWith('s')) return `${count} ${noun}es`
	return `${count} ${noun}s`
}

// ── Popup panels ────────────────────────────────────────────────

export function createPopupPanel(opts?: { bottom?: boolean; className?: string }): HTMLDivElement {
  const panel = document.createElement('div')
  panel.className = popupPanel({ position: opts?.bottom ? 'bottom' : 'top', class: opts?.className })
  return panel
}

// createAnchoredPopup creates a popup panel anchored to a trigger element.
// Position is set by positionAnchoredPopup at open time (left-aligned to
// the trigger's left edge, `gap` pixels above). Built separately from
// createPopupPanel to avoid the centering transform and animation keyframes
// that conflict with anchor positioning.
export function createAnchoredPopup(className?: string): HTMLDivElement {
  const panel = document.createElement('div')
  panel.className = anchoredPopup({ class: className })
  return panel
}

// positionAnchoredPopup positions a popup above a trigger element.
// Call AFTER removing the `hidden` class so the popup appears already
// anchored (avoids a 1-frame flash of the unpositioned popup jumping
// to its final location).
export function positionAnchoredPopup(popup: HTMLElement, trigger: HTMLElement, gap = 6) {
  const rect = trigger.getBoundingClientRect()
  popup.style.left = `${rect.left}px`
  popup.style.bottom = `${window.innerHeight - rect.top + gap}px`
}

// ── Outside-click dismiss ───────────────────────────────────────
export function createOutsideClick(
  trigger: HTMLElement,
  panel: HTMLElement,
  onClose: () => void,
) {
  const handler = (e: Event) => {
    if (!trigger.contains(e.target as Node) && !panel.contains(e.target as Node)) {
      onClose()
    }
  }
  return {
    add() { document.addEventListener('mousedown', handler) },
    remove() { document.removeEventListener('mousedown', handler) },
  }
}

// Group summary: "2 Searches, 1 Read, 1 LSP". Shared by ToolGroup.tsx and streamDom.ts.
export function summarize(tools: { name: string; isList?: boolean }[]): string {
	const counts = new Map<string, number>()
	for (const t of tools) {
		const noun = nounFor(t.name, t.isList)
		counts.set(noun, (counts.get(noun) ?? 0) + 1)
	}
	const parts: string[] = []
	for (const [noun, count] of counts) {
		parts.push(pluralize(noun, count))
	}
	return parts.join(', ')
}

// ── Time divider label (iMessage-style) ────────────────────────

// Truncates a timestamp to local-calendar-day midnight. Using
// new Date(y, m, d) rather than UTC math so DST transitions don't
// shift the boundary (the local calendar day is what users perceive).
function localDay(ts: number): Date {
	const d = new Date(ts)
	return new Date(d.getFullYear(), d.getMonth(), d.getDate())
}

// Whole-day difference between two timestamps, accounting for DST drift
// via Math.round on the midnight-aligned diff.
function dayDiff(a: number, b: number): number {
	return Math.round((localDay(b).getTime() - localDay(a).getTime()) / 86400000)
}

// Picks a date label for `curr` based on its distance from today:
// 0=Today, 1=Yesterday, 2..6=weekday, 7+=month-day. Latin-script labels
// are capitalized; CJK labels are returned as-is (capitalize would corrupt them).
// Beyond a week, includes the year when the date is in a different calendar
// year than today — "Dec 25" for same year, "Dec 25, 2025" for prior year.
function dateLabel(curr: number, locale: string): string {
	const diff = dayDiff(curr, Date.now())
	if (diff <= 1) {
		const raw = new Intl.RelativeTimeFormat(locale, { numeric: 'auto' }).format(-diff, 'day')
		return raw.charAt(0).toUpperCase() + raw.slice(1)
	}
	if (diff <= 6) {
		return new Intl.DateTimeFormat(locale, { weekday: 'long' }).format(curr)
	}
	const now = new Date()
	const currDate = new Date(curr)
	if (currDate.getFullYear() !== now.getFullYear()) {
		return new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric', year: 'numeric' }).format(curr)
	}
	return new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric' }).format(curr)
}

function timeLabel(curr: number, locale: string): string {
	return new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit' }).format(curr)
}

// Date label + time, joined by a space. Used for anchor (first message)
// and cross-day dividers so the user sees the actual time of day, not
// just "Today" / "Yesterday" without context.
function dateTimeLabel(curr: number, locale: string): string {
	return dateLabel(curr, locale) + ' ' + timeLabel(curr, locale)
}

// Returns the label for an iMessage-style time divider between prev and curr,
// or null when no divider should be shown.
//
// - prev=null → date+time label for curr (first-message-of-session anchor).
// - |curr - prev| crosses a day boundary → date+time label for curr.
// - |curr - prev| >= 15 min (same day) → date+time label for curr.
// - Otherwise → null (no divider).
//
// Absolute value: direction-agnostic so the caller doesn't have to think
// about whether prev or curr is older. loadHistory walks backward in time
// (curr older than prev) while streaming walks forward (curr newer); both
// share the same rule.
//
// All dividers carry the date prefix (not just first-of-day) so historical
// times can't be mistaken for today — without this, "21:30" after a
// "Yesterday" divider looks like today's 21:30. Redundant but unambiguous.
//
// Locale follows navigator.language (iMessage behavior); tests use regex
// tolerance because zh-CN time strings vary across Node ICU builds
// ("14:30" vs "下午2:30") and forcing hour12 would lose locale fidelity.
export function timeDividerLabel(
	prev: number | null,
	curr: number,
	locale: string = typeof navigator !== 'undefined' ? navigator.language : 'en-US',
): string | null {
	if (prev === null) return dateTimeLabel(curr, locale)
	// Normalize: earlier timestamp first so dayDiff / subtraction produce
	// positive values. loadHistory (backward walk) passes prev=newer,
	// curr=older; streaming passes prev=older, curr=newer. Same rule.
	const earlier = Math.min(prev, curr)
	const later = Math.max(prev, curr)
	if (dayDiff(earlier, later) >= 1) return dateTimeLabel(curr, locale)
	if (later - earlier >= 15 * 60 * 1000) return dateTimeLabel(curr, locale)
	return null
}
