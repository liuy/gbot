// Mirrors pkg/utils/duration.go:10. Input is SECONDS (caller converts ns).
// <1s: "0.3s", 1-59s: "Xs", 60s-59m: "Xm Ys", >=1h: "Xh Ym Zs".
import { popupPanel, anchoredPopup } from './styles/recipes'
import type { Block } from './model'
import { createElement } from './dom'

export interface ToolClassification {
  isSearch: boolean
  isRead: boolean
  isList: boolean
  isLsp: boolean
  isWeb: boolean
  isTask: boolean
}

// Pure name→flags lookup. isList is never set here — list-ness only comes
// from the backend `is_list` flag (Bash ls/tree/du), so callers OR it in
// at the merge sites (history replay / tool_start / tool_param_delta /
// tool_end). Backend always sends 'Lsp' (pkg/tool/lsp/lsp.go Name_).
export function classifyTool(name: string): ToolClassification {
  switch (name) {
    case 'Read':
      return { isRead: true, isSearch: false, isList: false, isLsp: false, isWeb: false, isTask: false }
    case 'Grep':
    case 'Glob':
      return { isSearch: true, isRead: false, isList: false, isLsp: false, isWeb: false, isTask: false }
    case 'Lsp':
      return { isLsp: true, isSearch: false, isRead: false, isList: false, isWeb: false, isTask: false }
    case 'Web':
      return { isWeb: true, isSearch: false, isRead: false, isList: false, isLsp: false, isTask: false }
    case 'Task':
      return { isTask: true, isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false }
    default:
      return { isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false, isTask: false }
  }
}

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

// Mirrors pkg/utils/token.go FormatTokenCount. Base 1024.
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
// no prefix is present. Mirrors renderToolOutput in pkg/wui/connector.go.
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
// Delegates to classifyTool so the name list has one source of truth.
export function isCollapsibleToolName(name: string): boolean {
  const c = classifyTool(name)
  return c.isSearch || c.isRead || c.isList || c.isLsp || c.isWeb || c.isTask
}

// Whether a block should group with adjacent collapsible tool blocks.
// Kept separate from isCollapsibleToolName because tool_start's buildToolBlock
// doesn't call classifyTool (backend may not have sent flags yet at creation),
// so the OR-with-name fallback covers tools marked collapsible retroactively
// via markToolCollapsible.
export function isCollapsibleBlock(b: Block): boolean {
  if (b.kind !== 'tool') return false
  return (
    b.isSearch || b.isRead || b.isList || b.isLsp || b.isWeb || b.isTask || isCollapsibleToolName(b.name)
  )
}

// Noun for tool group summary: "Search" for Grep/Glob, "Read" for Read, etc.
// isList flag: Bash tools with is_list:true (ls/tree/du) display as "List".
export function nounFor(name: string, isList = false): string {
	if (isList) return 'List'
	if (name === 'Grep' || name === 'Glob') return 'Search'
	if (name === 'Read') return 'Read'
	if (name === 'Lsp') return 'LSP'
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
  return createElement('div', popupPanel({ position: opts?.bottom ? 'bottom' : 'top', class: opts?.className }))
}

// createAnchoredPopup creates a popup panel anchored to a trigger element.
// Position is set by positionAnchoredPopup at open time (left-aligned to
// the trigger's left edge, `gap` pixels above). Built separately from
// createPopupPanel to avoid the centering transform and animation keyframes
// that conflict with anchor positioning.
export function createAnchoredPopup(className?: string): HTMLDivElement {
  return createElement('div', anchoredPopup({ class: className }))
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

// ── Long-press ─────────────────────────────────────────────────
//
// Touch + optional mouse long-press binding. If both touchstart and a
// synthesized mousedown fire on a touch device, the second start clears
// the first's pending timer so only one fires.
//
// consumeTrigger lets the caller's click handler suppress the click that
// would otherwise follow a long-press (e.g., to avoid cycling theme when
// the user long-pressed to open the hljs popover).
export interface LongPressOptions {
  durationMs?: number
  useMouse?: boolean
}

export interface LongPressController {
  cancel: () => void
  consumeTrigger: () => boolean
}

export function bindLongPress(
  el: HTMLElement,
  onTrigger: () => void,
  opts?: LongPressOptions,
): LongPressController {
  const durationMs = opts?.durationMs ?? 500
  const useMouse = opts?.useMouse ?? false
  let timer: ReturnType<typeof setTimeout> | null = null
  let triggered = false

  const start = () => {
    if (timer) clearTimeout(timer)
    timer = setTimeout(() => {
      triggered = true
      onTrigger()
    }, durationMs)
  }
  const clear = () => {
    if (timer) {
      clearTimeout(timer)
      timer = null
    }
  }

  el.addEventListener('touchstart', start)
  el.addEventListener('touchend', clear)
  el.addEventListener('touchmove', clear)
  el.addEventListener('pointercancel', clear)
  if (useMouse) {
    el.addEventListener('mousedown', start)
    el.addEventListener('mouseup', clear)
    el.addEventListener('mouseleave', clear)
  }

  return {
    cancel: clear,
    consumeTrigger: () => {
      const was = triggered
      triggered = false
      return was
    },
  }
}

// ── PopupHost ──────────────────────────────────────────────────
//
// Standard popup skeleton shared by modelPicker, enginePicker,
// taskPopover, attachPanel, histPanel: caller-built panel + a trigger
// (for outside-click exclusion) + lazy body append + hidden toggle +
// outside-click arm/disarm. Caller still owns the trigger's event
// handler — host exposes open/close/toggle/isOpen so non-standard
// triggers (histPanel's sendBtn triple-duty, taskPopover's toggle)
// stay at the call site. Special cases (contextPopover three-state,
// hljsPopover long-press, editPopup mousedown)
// stay on createOutsideClick directly.
export interface PopupHostOptions {
  trigger: HTMLElement
  panel: HTMLElement
  onOpen?: () => void
  onClose?: () => void
}

export interface PopupHost {
  open: () => void
  close: () => void
  toggle: () => void
  isOpen: () => boolean
}

export function createPopupHost(opts: PopupHostOptions): PopupHost {
  let open = false
  const outside = createOutsideClick(opts.trigger, opts.panel, () => closeImpl())

  const openImpl = () => {
    if (open) return
    if (!opts.panel.parentElement) document.body.appendChild(opts.panel)
    opts.panel.classList.remove('hidden')
    open = true
    opts.onOpen?.()
    outside.add()
  }
  const closeImpl = () => {
    if (!open) return
    opts.panel.classList.add('hidden')
    open = false
    opts.onClose?.()
    outside.remove()
  }
  return {
    open: openImpl,
    close: closeImpl,
    toggle: () => (open ? closeImpl() : openImpl()),
    isOpen: () => open,
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

// Queued-message reminder envelope — mirrors pkg/utils/queued_message.go. The
// engine persists mid-turn user queued messages in this exact wire form; the UI
// unwraps it for display and styles the original text gray (the model sees it
// as a special reminder forever, so the UI marks it forever too).
const QUEUED_REMINDER_PREFIX =
  '<system-reminder>\nThe user sent a new message while you were working:\n'
const QUEUED_REMINDER_SUFFIX =
  '\n\nIMPORTANT: After completing your current task, you MUST address the user\'s message above. Do not ignore it.\n</system-reminder>'

// Returns the original user text when text is exactly the queued message
// envelope, null otherwise (other system-reminder content never matches).
export function unwrapQueuedReminder(text: string): string | null {
  if (!text.startsWith(QUEUED_REMINDER_PREFIX)) return null
  if (!text.endsWith(QUEUED_REMINDER_SUFFIX)) return null
  return text.slice(
    QUEUED_REMINDER_PREFIX.length,
    text.length - QUEUED_REMINDER_SUFFIX.length,
  )
}

// Injected job/message reminder envelopes — byte-mirrors of
// pkg/utils/queued_message.go. The Go predicates decide TUI replay and
// rewind-selectability with the same rules, so a divergence renders a row the
// TUI hides (or the reverse) for the same stored message.
const JOB_COMMAND_REMINDER_PREFIX = '<system-reminder>\nA background command completed:\n'
const JOB_AGENT_REMINDER_PREFIX = '<system-reminder>\nA background agent completed a job:\n'
const REMINDER_SUFFIX = '\n</system-reminder>'
const MESSAGE_DELIVERY_PREFIX = '<message from="'
const MESSAGE_DELIVERY_SUFFIX = '</message>'

// Bare <job-notification> envelope: what both producers emit once the outer
// system-reminder wrap is gone. Mirrors background.go FormatXML's format
// strings byte-for-byte.
const JOB_NOTIFICATION_HEAD = '<job-notification>\n'
const JOB_NOTIFICATION_SUFFIX = '</job-notification>'
const JOB_STALL_JOINT = '</job-notification>\nLast output:'

// Field segment between the head and the terminator — the final closing-tag
// suffix, or the stall joint when the raw stall tail follows the closing tag.
function jobNotificationSegment(text: string): string | null {
  if (!text.startsWith(JOB_NOTIFICATION_HEAD)) return null
  const inner = text.slice(JOB_NOTIFICATION_HEAD.length)
  if (inner.endsWith(JOB_NOTIFICATION_SUFFIX)) {
    return inner.slice(0, inner.length - JOB_NOTIFICATION_SUFFIX.length)
  }
  const joint = inner.indexOf(JOB_STALL_JOINT)
  if (joint >= 0) return inner.slice(0, joint)
  return null
}

// Bare half of the sniff spec (D2 N1-N3): head newline at index 0, terminator
// at the closing tag or the stall joint, no unescaped system-reminder tag in
// the field segment (the stall tail is command output and may contain
// anything, so it is deliberately outside the check).
export function isJobNotificationEnvelope(text: string): boolean {
  const segment = jobNotificationSegment(text)
  return segment !== null && !segment.includes('<system-reminder>')
}

// Pre-single-layer job predicate: one of the two <system-reminder> job heads
// plus the reminder suffix — the double-wrapped form older sessions carry.
// Its rows keep the raw-body rendering; no single-layer display form exists.
export function isLegacyJobReminderEnvelope(text: string): boolean {
  if (!text.endsWith(REMINDER_SUFFIX)) return false
  return text.startsWith(JOB_COMMAND_REMINDER_PREFIX) || text.startsWith(JOB_AGENT_REMINDER_PREFIX)
}

// Total job predicate: legacy double-wrapped OR bare single-layer.
export function isInjectedReminderEnvelope(text: string): boolean {
  return isLegacyJobReminderEnvelope(text) || isJobNotificationEnvelope(text)
}

// D7 ①-④: the sender must be an id-shaped non-empty string (real senders are
// main / e2 / agent-3 / fork-2 — buildEnvelope XML-escapes from, so real
// senders never carry excluded bytes), the text must end at </message>, and
// it must contain no unescaped system-reminder tag (a real delivery's body is
// escaped).
function isMessageDeliveryEnvelope(text: string): boolean {
  if (!text.startsWith(MESSAGE_DELIVERY_PREFIX)) return false
  const quote = text.indexOf('"', MESSAGE_DELIVERY_PREFIX.length)
  if (quote === -1) return false
  const from = text.slice(MESSAGE_DELIVERY_PREFIX.length, quote)
  if (from === '' || !/^[A-Za-z0-9._-]+$/.test(from)) return false
  if (!text.endsWith(MESSAGE_DELIVERY_SUFFIX)) return false
  return !text.includes('<system-reminder>')
}

// A collapsed job/message row derived from a stored envelope. No fallback for
// a missing/empty from: D7 ② makes that state unreachable (the delivery
// never classifies), and a fallback would be dead code.
// body is the row's expandable content: display text for bare single-layer
// envelopes and deliveries, the raw envelope for legacy wrapped rows (their
// rendering is unchanged).
export type ReminderRow = { name: string; summary: string; body: string }

// The union of both producers' escape alphabets — bash escapeXML emits the
// named five, the agent/message xe (xml.Escape) emits the numeric forms and
// escapes newline/tab/CR. ONE replace pass with &amp; last in the alternation
// so &amp;quot; cannot double-decode (a chained replaceAll would).
const XML_ENTITY_RE = /&#34;|&#39;|&quot;|&apos;|&lt;|&gt;|&#xA;|&#x9;|&#xD;|&amp;/g
const XML_ENTITY_MAP: Record<string, string> = {
  '&#34;': '"',
  '&#39;': "'",
  '&quot;': '"',
  '&apos;': "'",
  '&lt;': '<',
  '&gt;': '>',
  '&#xA;': '\n',
  '&#x9;': '\t',
  '&#xD;': '\r',
  '&amp;': '&',
}

function decodeEntities(s: string): string {
  return s.replace(XML_ENTITY_RE, (entity) => XML_ENTITY_MAP[entity])
}

// Each field gets its OWN tag lookup, never a forward scan from a previously
// matched tag: the bash shape (background.go FormatXML) has no <agent-type>
// and can lack <status> entirely, while the agent shape (fork.go) puts
// <status> BEFORE <agent-type> and <summary> seven tags further down.
function extractTag(text: string, tag: string): string | null {
  const m = text.match(new RegExp(`<${tag}>([\\s\\S]*?)</${tag}>`))
  return m ? m[1] : null
}

function classifyCommandRow(inner: string, body: string): ReminderRow {
  // Field values are XML-escaped by both producers; unescape BEFORE the
  // quoted-span search and the exit-code probe — searching for a literal '"'
  // in escaped text finds nothing and the row degrades to command <job-id>.
  const summaryTag = decodeEntities(extractTag(inner, 'summary') ?? '')
  const jobId = decodeEntities(extractTag(inner, 'job-id') ?? '')
  const status = extractTag(inner, 'status')
  const quoted = summaryTag.match(/"([^"]*)"/)
  const desc = quoted ? quoted[1] : jobId
  if (!desc) return { name: 'Reminder', summary: 'command', body }
  const exit = summaryTag.match(/exit code (\d+)/)
  if (exit) return { name: 'Reminder', summary: `command ${desc} · exit ${exit[1]}`, body }
  if (status) return { name: 'Reminder', summary: `command ${desc} · ${status}`, body }
  return { name: 'Reminder', summary: `command ${desc}`, body }
}

function classifyAgentRow(inner: string, body: string): ReminderRow {
  const agentType = decodeEntities(extractTag(inner, 'agent-type') ?? '')
  const status = extractTag(inner, 'status')
  const jobId = decodeEntities(extractTag(inner, 'job-id') ?? '')
  if (agentType) {
    return { name: 'Reminder', summary: status ? `agent ${agentType} · ${status}` : `agent ${agentType}`, body }
  }
  if (jobId) return { name: 'Reminder', summary: `agent ${jobId}`, body }
  return { name: 'Reminder', summary: 'agent', body }
}

// classifyReminderMessage decides from content alone (no flag, no metadata):
// a history user message whose single text block sniffs as one of the
// injected envelopes renders as a collapsed row instead of a user bubble.
// Legacy wrapped rows keep their head-based inner slice and raw body; bare
// single-layer rows dispatch on the agent-type tag (agent producers always
// emit it, bash never does) with the FULL text as inner — extractTag is
// wrapping-agnostic — and the display text as body.
export function classifyReminderMessage(text: string): ReminderRow | null {
  if (isLegacyJobReminderEnvelope(text)) {
    const isCommand = text.startsWith(JOB_COMMAND_REMINDER_PREFIX)
    const prefixLen = isCommand ? JOB_COMMAND_REMINDER_PREFIX.length : JOB_AGENT_REMINDER_PREFIX.length
    const inner = text.slice(prefixLen, text.length - REMINDER_SUFFIX.length)
    return isCommand ? classifyCommandRow(inner, text) : classifyAgentRow(inner, text)
  }
  if (isJobNotificationEnvelope(text)) {
    const body = unwrapEnvelopeDisplay(text) ?? text
    return extractTag(text, 'agent-type') !== null
      ? classifyAgentRow(text, body)
      : classifyCommandRow(text, body)
  }
  if (isMessageDeliveryEnvelope(text)) {
    const quote = text.indexOf('"', MESSAGE_DELIVERY_PREFIX.length)
    const from = text.slice(MESSAGE_DELIVERY_PREFIX.length, quote)
    return { name: 'Reminder', summary: `got a message from ${from}`, body: unwrapEnvelopeDisplay(text) ?? text }
  }
  return null
}

// Byte-mirror of pkg/utils EnvelopeDisplayText: message → decoded body only;
// job-notification → summary line + result body (machine fields hidden);
// system-reminder → body only. The legacy double-wrapped job form returns
// null (no recursion by design). Caller order is load-bearing: arm 3 also
// matches the 插话 envelope and returns its full body including the IMPORTANT
// boilerplate — callers wanting the original must try unwrapQueuedReminder
// FIRST.
export function unwrapEnvelopeDisplay(text: string): string | null {
  if (isMessageDeliveryEnvelope(text)) return messageDeliveryDisplay(text)
  if (isJobNotificationEnvelope(text)) return jobNotificationDisplay(text)
  if (!isLegacyJobReminderEnvelope(text)) {
    if (text.startsWith('<system-reminder>\n') && text.endsWith(REMINDER_SUFFIX)) {
      return text.slice('<system-reminder>\n'.length, text.length - REMINDER_SUFFIX.length)
    }
  }
  return null
}

// Decoded delivery body: between the `<message from="X">` head and the
// closing tag with the bounding newlines dropped (the producer always emits
// `\nbody\n`; a single-bound trim keeps the newline-less paste total).
function messageDeliveryDisplay(text: string): string {
  const quote = text.indexOf('"', MESSAGE_DELIVERY_PREFIX.length)
  let body = text.slice(quote + 2, text.length - MESSAGE_DELIVERY_SUFFIX.length)
  if (body.startsWith('\n')) body = body.slice(1)
  if (body.endsWith('\n')) body = body.slice(0, -1)
  return decodeEntities(body)
}

// Summary line + result body; machine fields (job-id, tool-use-id, status,
// agent-type, output-file, duration-ms, tokens, tool-uses) are hidden —
// duration/tokens are dropped, not compacted. The stall tail is interpolated
// raw by the producer, so it is appended verbatim and NEVER decoded: decoding
// would corrupt legitimate entity-shaped output (`echo '&amp;'`).
function jobNotificationDisplay(text: string): string {
  const summaryTag = extractTag(text, 'summary')
  let out =
    summaryTag !== null
      ? decodeEntities(summaryTag)
      : (jobNotificationSegment(text) ?? '').trim()
  const resultTag = extractTag(text, 'result')
  if (resultTag !== null) {
    let body = decodeEntities(resultTag)
    if (body.startsWith('\n')) body = body.slice(1)
    if (body.endsWith('\n')) body = body.slice(0, -1)
    if (body.trim() !== '') out += '\n\n' + body
  }
  const joint = text.indexOf(JOB_STALL_JOINT)
  if (joint >= 0) {
    out += '\n\n' + text.slice(joint + JOB_STALL_JOINT.length).trim()
  }
  return out
}

// Snapshot hydration cost estimate: recursive block count and parseable
// character volume. Image src excluded — base64 data URLs are huge but
// never markdown-parsed.
export function countStreamBlocks(blocks: Block[]): number {
  let n = 0
  for (const b of blocks) {
    n++
    if (b.kind === 'tool' && b.children && b.children.length > 0) {
      n += countStreamBlocks(b.children)
    }
  }
  return n
}

export function streamTextChars(blocks: Block[]): number {
  let n = 0
  for (const b of blocks) {
    if (b.kind === 'text' || b.kind === 'user') n += (b.text || '').length
    else if (b.kind === 'thinking') n += (b.text || '').length
    else if (b.kind === 'tool') {
      n += (b.displayOutput || '').length
      if (b.children && b.children.length > 0) n += streamTextChars(b.children)
    }
  }
  return n
}
