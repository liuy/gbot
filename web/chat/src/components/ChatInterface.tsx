import { useCallback, useEffect, useRef, useState } from 'react'
import { useWebSocket } from '../websocket'
import type { ServerMessage, QueryEvent, HistoryChatMsg } from '../types'
import { newAssistantMessage, type ChatMessage, type Block } from '../model'
import MessageComponent from './MessageComponent'
import StreamingMessage from './StreamingMessage'
import InputBar, { type InputBarHandle } from './InputBar'
import Ask from './Ask'
import Header from './Header'

type ToolBlock = Extract<Block, { kind: 'tool' }>

type AskData = {
	id: string
	kind: 'permission' | 'input'
	tool_name: string
	input?: unknown
	message?: string
	rule_detail?: string
	prompt?: string
	masked?: boolean
	agent_type?: string
}

let msgIdCounter = 0
function nextId(prefix: string): string {
	msgIdCounter += 1
	return `${prefix}-${msgIdCounter}`
}

// Mirrors TUI findToolViewInBlocks (pkg/tui/repl.go:174) but returns a NEW
// blocks array (immutable update) instead of a live reference. Searches the
// whole tree depth-first: top-level first, then children. Returns the same
// array reference when nothing changed (so React .map short-circuits cleanly).
function mapToolBlockByID(
	blocks: Block[],
	id: string,
	fn: (t: ToolBlock) => ToolBlock,
): Block[] {
	let changed = false
	const out = blocks.map((b) => {
		if (b.kind !== 'tool') return b
		if (b.id === id) {
			changed = true
			return fn(b)
		}
		if (b.children.length > 0) {
			const newChildren = mapToolBlockByID(b.children, id, fn)
			if (newChildren !== b.children) {
				changed = true
				return { ...b, children: newChildren }
			}
		}
		return b
	})
	return changed ? out : blocks
}

// Mirrors TUI classifyToolName (pkg/tui/app.go:783). History messages don't
// carry is_search/is_read streaming flags, so we infer from tool name.
function classifyToolName(name: string): { isSearch: boolean; isRead: boolean; isList: boolean; isLsp: boolean; isWeb: boolean } {
	switch (name) {
		case 'Read': return { isRead: true, isSearch: false, isList: false, isLsp: false, isWeb: false }
		case 'Grep': case 'Glob': return { isSearch: true, isRead: false, isList: false, isLsp: false, isWeb: false }
		case 'Lsp': return { isLsp: true, isSearch: false, isRead: false, isList: false, isWeb: false }
		case 'Web': return { isWeb: true, isSearch: false, isRead: false, isList: false, isLsp: false }
		default: return { isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false }
	}
}

// Extract the history→ChatMessage mapping so it's testable independently.
function mapHistoryToChatMessages(histMsgs: HistoryChatMsg[]): ChatMessage[] {
	// Merge consecutive assistant messages into one (like TUI engineMessagesToViews).
	const merged: HistoryChatMsg[] = []
	for (const h of histMsgs) {
		// Skip user messages that are pure tool_result carriers (no visible text).
		if (h.role === 'user' && (!h.text || h.text.trim() === '')) continue
		const last = merged[merged.length - 1]
		if (last && last.role === 'assistant' && h.role === 'assistant') {
			// Merge into last assistant
			last.text += h.text ?? ''
			last.blocks = [...(last.blocks ?? []), ...(h.blocks ?? [])]
			if (h.usage) {
				last.usage = {
					inputTokens: (last.usage?.inputTokens ?? 0) + h.usage.inputTokens,
					outputTokens: (last.usage?.outputTokens ?? 0) + h.usage.outputTokens,
					cacheRead: (last.usage?.cacheRead ?? 0) + h.usage.cacheRead,
					cacheCreation: (last.usage?.cacheCreation ?? 0) + h.usage.cacheCreation,
				}
			}
		} else {
			merged.push({ ...h })
		}
	}
	const result: ChatMessage[] = []
	for (const h of merged) {
		const m: ChatMessage = {
			id: h.id || nextId(h.role === 'user' ? 'u' : 'a'),
			role: h.role,
			blocks: [],
			usage: {
				inputTokens: h.usage?.inputTokens ?? 0,
				outputTokens: h.usage?.outputTokens ?? 0,
				cacheRead: h.usage?.cacheRead ?? 0,
				cacheCreation: h.usage?.cacheCreation ?? 0,
			},
			error: h.error ?? '',
			status: h.status ?? 'done',
			startedAt: h.startedAt ?? Date.now(),
		}
		if (h.blocks && h.blocks.length > 0) {
			for (const b of h.blocks) {
				if (b.kind === 'text') {
					m.blocks.push({ kind: 'text', id: nextId('txt'), text: b.text })
				} else if (b.kind === 'thinking') {
					const th = b.thinking!
					m.blocks.push({
						kind: 'thinking',
						id: nextId('th'),
						text: th.text,
						durationNs: th.durationNs ?? 0,
						active: false,
						startedAt: 0,
					})
				} else if (b.kind === 'tool') {
					const t = b.tool!
					const srk = classifyToolName(t.name)
					m.blocks.push({
						kind: 'tool',
						id: t.id,
						name: t.name,
						summary: t.summary ?? '',
						isSearch: srk.isSearch,
						isRead: srk.isRead,
						isList: srk.isList,
						isLsp: srk.isLsp,
						isWeb: srk.isWeb,
						state: (t.isError ? 'error' : 'done') as 'error' | 'done',
						timingNs: t.durationNs ?? 0,
						displayOutput: t.displayOutput ?? '',
						startedAt: 0,
						children: [],
					})
				}
			}
		} else {
			// User messages carry text; assistant legacy messages may have text
			// without blocks. Push a single text block so they render.
			if (h.text) {
				m.blocks.push({ kind: 'text', id: nextId('txt'), text: h.text })
			}
		}
		result.push(m)
	}
	return result
}

// Module-level so messages survive ChatInterface unmount on tab switch.
const persistedMessages: ChatMessage[] = []
let persistedNextCursor = ''
let persistedHasMore = false

export default function ChatInterface() {
	const { subscribe, send } = useWebSocket()
	const messagesRef = useRef<ChatMessage[]>(persistedMessages)
	const [, setTick] = useState(0)
	// Stabilized via useCallback so every useCallback below (and every
	// structural-event handler) closes over the SAME forceRender reference.
	// If this were a fresh function each render, those callbacks would
	// recapture a new ref and break the InputBar React.memo.
	const forceRender = useCallback(() => setTick((t) => (t + 1) & 0x7fffffff), [])

	// DOM sink for the currently-streaming text block. Null when not streaming
	// or when streaming but no text block exists yet.
	const streamTextRef = useRef<HTMLDivElement | null>(null)
	// DOM sink for the currently-streaming thinking block body (the <p> element
	// inside Thinking.tsx). Type matches the JSX tag in Thinking.tsx (<p>),
	// which is HTMLParagraphElement — NOT HTMLDivElement.
	const streamThinkingRef = useRef<HTMLParagraphElement | null>(null)
	// Accumulators mirror what's in messagesRef but are the source of truth for
	// DOM writes during streaming. Cleared on query_start, drained on query_end.
	const streamTextAccum = useRef('')
	const streamThinkingAccum = useRef('')
	// Tracks whether a StreamingText sink is currently mounted, so we know when
	// to swap it in/out without relying on React state.
	const streamSinkMounted = useRef(false)

	const [ask, setAsk] = useState<AskData | null>(null)
	const [queuedMsgs, setQueuedMsgs] = useState<{ uuid: string; text: string }[]>([])
	const streamingRef = useRef(false)
	const [streaming, setStreaming] = useState(false)
	const pendingCancelRef = useRef<{ uuid: string; text: string }[] | null>(null)
	const [nextCursor, setNextCursor] = useState(persistedNextCursor)
	const [hasMore, setHasMore] = useState(persistedHasMore)
	const loadingMoreRef = useRef(false)
	const expectingInitialRef = useRef(true)

	const scrollRef = useRef<HTMLDivElement | null>(null)
	const topSentinelRef = useRef<HTMLDivElement | null>(null)
	const bottomRef = useRef<HTMLDivElement | null>(null)
	const inputRef = useRef<InputBarHandle>(null)

	const scrollToBottom = () => {
		const el = scrollRef.current
		if (!el) return
		const nearBottom =
			el.scrollHeight - el.scrollTop - el.clientHeight < 120
		if (nearBottom) {
			requestAnimationFrame(() => {
				bottomRef.current?.scrollIntoView({ behavior: 'auto' })
			})
		}
	}

	// Immutable update helper: replaces the last streaming assistant message
	// with a new object derived from the old one.
	const updateStreamingAssistant = (fn: (m: ChatMessage) => ChatMessage) => {
		const list = messagesRef.current
		const idx = list.length - 1
		const last = list[idx]
		if (!last || last.role !== 'assistant' || last.status !== 'streaming') return
		list[idx] = fn(last)
	}

	// Routes a sub-agent event into the parent tool block via mapToolBlockByID.
	// Sub-agent text/thinking/tool events update parent.children through React
	// state (forceRender), NOT the DOM-sink path — those sinks are top-level
	// streaming only.
	const updateParentToolBlock = (
		parentID: string,
		fn: (tool: ToolBlock) => ToolBlock,
	) => {
		updateStreamingAssistant((m) => ({
			...m,
			blocks: mapToolBlockByID(m.blocks, parentID, fn),
		}))
		forceRender()
	}

	const appendError = (text: string) => {
		const list = messagesRef.current
		const idx = list.length - 1
		const last = list[idx]
		if (last && last.role === 'assistant' && last.status === 'streaming') {
			list[idx] = { ...last, error: text, status: 'done' as const }
		} else {
			list.push({ ...newAssistantMessage(nextId('a')), error: text, status: 'done' as const })
		}
		streamingRef.current = false
		setStreaming(false)
		setQueuedMsgs([])
		streamSinkMounted.current = false
		streamTextRef.current = null
		streamThinkingRef.current = null
		forceRender()
	}

	const loadHistory = (msg: Extract<ServerMessage, { type: 'history' }>) => {
		const histMsgs = msg.messages
		const newMsgs = mapHistoryToChatMessages(histMsgs)

		// Initial page (cursor was empty) replaces all messages.
		// Use ref, not React state, because connect_status and history
		// arrive in the same synchronous batch — setNextCursor('') won't
		// have committed by the time loadHistory runs.
		const isInitial = expectingInitialRef.current

		if (isInitial) {
			expectingInitialRef.current = false
			messagesRef.current.splice(0, messagesRef.current.length, ...newMsgs)
		} else {
			// Deduplicate: skip messages already in the list (by id).
			const existingIds = new Set(messagesRef.current.map((m) => m.id))
			const deduped = newMsgs.filter((m) => !existingIds.has(m.id))
			if (deduped.length === 0) {
				loadingMoreRef.current = false
				return
			}
			// Pagination: prepend older messages.
			const el = scrollRef.current
			const prevScrollHeight = el?.scrollHeight ?? 0
			const prevScrollTop = el?.scrollTop ?? 0
			messagesRef.current.unshift(...deduped)
			forceRender()
			requestAnimationFrame(() => {
				if (el) {
					const delta = el.scrollHeight - prevScrollHeight
					el.scrollTop = prevScrollTop + delta
				}
			})
		}
		persistedNextCursor = msg.nextCursor
		persistedHasMore = msg.hasMore
		setNextCursor(msg.nextCursor)
		setHasMore(msg.hasMore)
		loadingMoreRef.current = false
		forceRender()
		if (isInitial) {
			requestAnimationFrame(() => {
				bottomRef.current?.scrollIntoView({ behavior: 'smooth' })
			})
			// Prefetch next page immediately so scroll-up is instant
			if (msg.hasMore && msg.nextCursor) {
				loadingMoreRef.current = true
				send({ type: 'history_request', cursor: msg.nextCursor, limit: 10 })
			}
		}
	}

	const handleEvent = (e: QueryEvent) => {
		switch (e.type) {
			case 'query_start': {
				streamTextAccum.current = ''
				streamThinkingAccum.current = ''
				streamSinkMounted.current = false
				streamTextRef.current = null
				streamThinkingRef.current = null
				messagesRef.current.push(newAssistantMessage(nextId('a')))
				streamingRef.current = true
				setStreaming(true)
				forceRender()
				return
			}
			case 'turn_start': {
				// Sub-engine turnStart (processAttachments → runTurns inside a
				// sub-agent): do NOT push a new assistant message. Sub-agent
				// text/thinking events route to the parent tool via agent metadata.
				if (e.agent) return
				// processAttachments path emits turn_start without query_start.
				// Push an assistant message if none exists yet.
				if (streamingRef.current) return
				streamTextAccum.current = ''
				streamThinkingAccum.current = ''
				streamSinkMounted.current = false
				streamTextRef.current = null
				streamThinkingRef.current = null
				messagesRef.current.push(newAssistantMessage(nextId('a')))
				streamingRef.current = true
				setStreaming(true)
				forceRender()
				return
			}
			case 'query_end': {
				// Sub-agent queryEnd: do NOT finish the main query stream.
				// Only the main engine's queryEnd (no agent metadata) marks
				// the top-level assistant done.
				if (e.agent) return
				const wasAborted = !!e.aborted

				if (wasAborted) {
					// Check if assistant produced meaningful content
					const list = messagesRef.current
					const last = list[list.length - 1]
					const hasContent = last && last.role === 'assistant' &&
						last.blocks.some(b => (b.kind === 'text' && b.text.trim()) || b.kind === 'tool')

					if (hasContent) {
						updateStreamingAssistant((m) => ({
							...m,
							status: 'done' as const,
						}))
					} else {
						// No meaningful response — rewind: remove empty assistant msg + restore input
						if (last && last.role === 'assistant') {
							list.pop()
						}
						// Find the user message text to restore
						const userMsg = [...list].reverse().find(m => m.role === 'user')
						if (userMsg) {
							const textBlock = userMsg.blocks.find(b => b.kind === 'text') as any
							if (textBlock?.text) {
								list.pop()
								inputRef.current?.setInputText(textBlock.text)
							}
						}
					}
				} else {
					updateStreamingAssistant((m) => ({ ...m, status: 'done' as const }))
				}
			streamingRef.current = false
			setStreaming(false)
			setQueuedMsgs([])
			streamSinkMounted.current = false
			streamTextRef.current = null
			streamThinkingRef.current = null
			forceRender()
			return
		}
			case 'thinking_start': {
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					updateParentToolBlock(parentID, (parent) => ({
						...parent,
						children: [...parent.children, {
							kind: 'thinking',
							id: nextId('th'),
							text: '',
							durationNs: 0,
							active: true,
							startedAt: Date.now(),
						}],
					}))
					return
				}
				streamThinkingAccum.current = ''
				updateStreamingAssistant((m) => ({
					...m,
					blocks: [...m.blocks, {
						kind: 'thinking',
						id: nextId('th'),
						text: '',
						durationNs: 0,
						active: true,
						startedAt: Date.now(),
					}],
				}))
				forceRender()
				return
			}
			case 'thinking_delta': {
				if (!e.thinking?.text) return
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const text = e.thinking.text
					updateParentToolBlock(parentID, (parent) => {
						const children = [...parent.children]
						for (let i = children.length - 1; i >= 0; i--) {
							const c = children[i]
							if (c.kind === 'thinking' && c.active) {
								children[i] = { ...c, text: c.text + text }
								break
							}
						}
						return { ...parent, children }
					})
					return
				}
				streamThinkingAccum.current += e.thinking.text
				updateStreamingAssistant((m) => {
					const blocks = [...m.blocks]
					for (let i = blocks.length - 1; i >= 0; i--) {
						if (blocks[i].kind === 'thinking') {
							blocks[i] = { ...blocks[i], text: (blocks[i] as any).text + e.thinking!.text } as Block
							break
						}
					}
					return { ...m, blocks }
				})
				if (streamThinkingRef.current) {
					streamThinkingRef.current.textContent = streamThinkingAccum.current
				}
				return
			}
			case 'thinking_end': {
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const duration = e.thinking?.duration
					updateParentToolBlock(parentID, (parent) => {
						const children = [...parent.children]
						for (let i = children.length - 1; i >= 0; i--) {
							const c = children[i]
							if (c.kind === 'thinking' && c.active) {
								children[i] = {
									...c,
									active: false,
									durationNs: duration ?? c.durationNs,
								}
								break
							}
						}
						return { ...parent, children }
					})
					return
				}
				updateStreamingAssistant((m) => {
					const blocks = [...m.blocks]
					for (let i = blocks.length - 1; i >= 0; i--) {
						if (blocks[i].kind === 'thinking') {
							blocks[i] = {
								...blocks[i],
								active: false,
								durationNs: e.thinking?.duration ?? (blocks[i] as any).durationNs,
							} as Block
							break
						}
					}
					return { ...m, blocks }
				})
				forceRender()
				return
			}
			case 'text_start': {
				// Sub-agent text_start is a no-op: text_delta creates the block
				// lazily inside the parent tool. Matches TUI textStartMsg.
				if (e.agent) return
				streamTextAccum.current = ''
				streamSinkMounted.current = true
				updateStreamingAssistant((m) => ({
					...m,
					blocks: [...m.blocks, { kind: 'text', id: nextId('txt'), text: '' }],
				}))
				forceRender()
				return
			}
			case 'text_delta': {
				if (!e.text) return
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const text = e.text
					updateParentToolBlock(parentID, (parent) => {
						const last = parent.children[parent.children.length - 1]
						if (last && last.kind === 'text') {
							const children = parent.children.slice()
							children[children.length - 1] = { ...last, text: last.text + text }
							return { ...parent, children }
						}
						return { ...parent, children: [...parent.children, { kind: 'text', id: nextId('txt'), text }] }
					})
					return
				}
				streamTextAccum.current += e.text
				updateStreamingAssistant((m) => {
					const blocks = [...m.blocks]
					let found = false
					for (let i = blocks.length - 1; i >= 0; i--) {
						if (blocks[i].kind === 'text') {
							blocks[i] = { ...blocks[i], text: (blocks[i] as any).text + e.text } as Block
							found = true
							break
						}
					}
					if (!found) {
						blocks.push({ kind: 'text', id: nextId('txt'), text: e.text! })
					}
					return { ...m, blocks }
				})
				if (streamTextRef.current) {
					streamTextRef.current.textContent = streamTextAccum.current
					scrollToBottom()
				} else {
					// Sink not yet mounted. Two arrival patterns reach here:
					//  (1) text_start fired and scheduled a forceRender, but React
					//      hasn't committed the sink yet (streamSinkMounted is true).
					//  (2) text_delta arrived with NO preceding text_start — real-world
					//      case covered by attachment_streaming.test.tsx and
					//      attachment_idle.test.tsx, where the engine emits text_delta
					//      directly after query_start / turn_start without a text_start.
					// Either way: flip the flag, force ONE render to mount StreamingText,
					// and let flushRef on mount drain streamTextAccum (including this
					// delta). Subsequent deltas find the ref live and write directly.
					streamSinkMounted.current = true
					forceRender()
				}
				return
			}
			case 'text_end': {
				return
			}
			case 'tool_start': {
				if (!e.tool_use) return
				const tu = e.tool_use
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					updateParentToolBlock(parentID, (parent) => ({
						...parent,
						children: [...parent.children, {
							kind: 'tool',
							id: tu.id,
							name: tu.name,
							summary: '',
							isSearch: !!tu.is_search,
							isRead: !!tu.is_read,
							isList: !!tu.is_list,
							isLsp: !!tu.is_lsp,
							isWeb: tu.name === 'Web',
							state: 'running',
							timingNs: 0,
							displayOutput: '',
							startedAt: Date.now(),
							children: [],
						}],
					}))
					return
				}
				updateStreamingAssistant((m) => ({
					...m,
					blocks: [...m.blocks, {
						kind: 'tool',
						id: tu.id,
						name: tu.name,
						summary: '',
						isSearch: !!tu.is_search,
						isRead: !!tu.is_read,
						isList: !!tu.is_list,
						isLsp: !!tu.is_lsp,
						isWeb: tu.name === 'Web',
						state: 'running',
						timingNs: 0,
						displayOutput: '',
						startedAt: Date.now(),
						children: [],
					}],
				}))
				forceRender()
				return
			}
			case 'tool_param_delta': {
				if (!e.partial_input || !e.partial_input.summary) return
				const targetId = e.partial_input.id
				const summary = e.partial_input.summary
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					updateParentToolBlock(parentID, (parent) => ({
						...parent,
						children: mapToolBlockByID(parent.children, targetId, (t) => ({ ...t, summary })),
					}))
					return
				}
				updateStreamingAssistant((m) => ({
					...m,
					blocks: m.blocks.map((b) =>
						b.id === targetId && b.kind === 'tool'
							? { ...b, summary }
							: b
					),
				}))
				forceRender()
				return
			}
			case 'tool_run': {
				forceRender()
				return
			}
			case 'tool_output_delta': {
				// TUI repl.go:1046-1057 — best-effort: update trailing tool's
				// output in parent.children. Most tools don't stream output
				// deltas, so this is rarely hit.
				if (e.agent && e.tool_result) {
					const parentID = e.agent.parent_tool_use_id
					const targetId = e.tool_result.tool_use_id
					const output = e.tool_result.display_output ?? ''
					updateParentToolBlock(parentID, (parent) => ({
						...parent,
						children: mapToolBlockByID(parent.children, targetId, (t) => ({ ...t, displayOutput: output })),
					}))
				}
				return
			}
			case 'tool_end': {
				if (!e.tool_result) return
				const tr = e.tool_result
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					updateParentToolBlock(parentID, (parent) => ({
						...parent,
						children: mapToolBlockByID(parent.children, tr.tool_use_id, (t) => ({
							...t,
							state: tr.is_error ? 'error' as const : 'done' as const,
							timingNs: (Date.now() - t.startedAt) * 1e6,
							displayOutput: tr.display_output ?? '',
							isSearch: tr.is_search ?? t.isSearch,
							isRead: tr.is_read ?? t.isRead,
							isList: tr.is_list ?? t.isList,
							isLsp: tr.is_lsp ?? t.isLsp,
						})),
					}))
					return
				}
				updateStreamingAssistant((m) => ({
					...m,
					blocks: m.blocks.map((b) => {
						if (b.id !== tr.tool_use_id || b.kind !== 'tool') return b
						return {
							...b,
							state: tr.is_error ? 'error' as const : 'done' as const,
							timingNs: (Date.now() - b.startedAt) * 1e6,
							displayOutput: tr.display_output ?? '',
							isSearch: tr.is_search ?? b.isSearch,
							isRead: tr.is_read ?? b.isRead,
							isList: tr.is_list ?? b.isList,
							isLsp: tr.is_lsp ?? b.isLsp,
						}
					}),
				}))
				forceRender()
				return
			}
			case 'usage': {
				if (!e.usage_event) return
				// Sub-agent usage is reported via the parent Agent tool's
				// result, not merged into the top-level assistant's usage.
				if (e.agent) return
				const u = e.usage_event
				updateStreamingAssistant((m) => ({
					...m,
					usage: {
						inputTokens: u.input_tokens,
						outputTokens: u.output_tokens,
						cacheRead: u.cache_read_input_tokens ?? 0,
						cacheCreation: u.cache_creation_input_tokens ?? 0,
					},
				}))
				forceRender()
				return
			}
			case 'retry_attempt':
				return
			case 'attachment': {
				// TUI parity: repl.go:1364 — mid-turn drain appends
				// BlockUser inside the current assistant message's blocks.
				// Streaming assistant must remain the last message so
				// text_delta/thinking_delta handlers can find it.
				const att = (e as any).message?.attachment
				if (!att) return
				const text: string = att.prompt ?? ''
				const sourceUUID: string = att.source_uuid ?? ''
				if (!text) return
				if (streamingRef.current) {
					// Mid-turn: append user block to current assistant
					updateStreamingAssistant((m) => ({
						...m,
						blocks: [...m.blocks, { kind: 'user', id: nextId('u'), text }],
					}))
				} else {
					// Between queries (idle): push as standalone user message
					messagesRef.current.push({
						id: nextId('u'),
						role: 'user',
						blocks: [{ kind: 'text', id: nextId('txt'), text }],
						usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
						error: '',
						status: 'done',
						startedAt: Date.now(),
					})
				}
				if (sourceUUID === '') return
				setQueuedMsgs((prev) => prev.filter((m) => m.uuid !== sourceUUID))
				forceRender()
				return
			}
			default:
				return
		}
	}

	useEffect(() => {
		const unsubscribe = subscribe((msg: ServerMessage) => {
			switch (msg.type) {
				case 'connect_status':
					expectingInitialRef.current = true
					persistedNextCursor = ''
					persistedHasMore = false
					setNextCursor('')
					setHasMore(false)
					loadingMoreRef.current = false
					return
			case 'queued': {
				// FIFO stamping: msgCh is a single ordered channel, so the Nth
				// 'queued' reply corresponds to the Nth unstamped entry in the
				// array. Find the first entry with uuid === '' and stamp it.
				const uuid = (msg as any).uuid as string
				setQueuedMsgs((prev) => {
					const next = [...prev]
					for (let i = 0; i < next.length; i++) {
						if (next[i].uuid === '') {
							next[i] = { ...next[i], uuid }
							return next
						}
					}
					return prev
				})
				return
			}
			case 'cancel_result': {
				const removed = new Set((msg as any).removed as string[])
				const snapshot = pendingCancelRef.current
				pendingCancelRef.current = null
				if (snapshot) {
					// TUI parity: only restore text for UUIDs that were successfully
					// removed. Drained items (not in removed) were already processed
					// by the engine and would duplicate if restored to input.
					const toRestore = snapshot.filter((m) => removed.has(m.uuid))
					if (toRestore.length > 0) {
						const joined = toRestore.map((m) => m.text).join('\n')
						inputRef.current?.appendQueuedText(joined)
					}
				}
				setQueuedMsgs([])
				return
			}
				case 'history':
					loadHistory(msg)
					return
				case 'error':
					appendError(msg.message)
					return
				case 'ask':
					setAsk({
						id: msg.id,
						kind: msg.kind,
						tool_name: msg.tool_name,
						input: msg.input,
						message: msg.message,
						rule_detail: msg.rule_detail,
						prompt: msg.prompt,
						masked: msg.masked,
						agent_type: msg.agent_type,
					})
					return
				case 'event':
					handleEvent(msg.event)
					return
			}
		})
		return unsubscribe
	}, [subscribe])

	useEffect(() => {
		scrollToBottom()
	})

	// IntersectionObserver: load more when scrolling to top.
	useEffect(() => {
		const el = topSentinelRef.current
		const root = scrollRef.current
		if (!el || !root) return
		const obs = new IntersectionObserver(
			(entries) => {
				if (entries[0].isIntersecting && hasMore && !loadingMoreRef.current && nextCursor) {
					loadingMoreRef.current = true
					send({ type: 'history_request', cursor: nextCursor, limit: 10 })
				}
			},
			{ root, rootMargin: '400px 0px 0px 0px', threshold: 0 }
		)
		obs.observe(el)
		return () => obs.disconnect()
	}, [hasMore, nextCursor, send])

	const onSend = useCallback((text: string) => {
		if (streamingRef.current) {
			setQueuedMsgs((prev) => [...prev, { uuid: '', text }])
			send({ type: 'message', text })
			return
		}
		messagesRef.current.push({
			id: nextId('u'),
			role: 'user',
			blocks: [{ kind: 'text', id: nextId('txt'), text }],
			usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
			error: '',
			status: 'done',
			startedAt: Date.now(),
		})
		send({ type: 'message', text })
		forceRender()
	}, [send, forceRender])

	const onStop = useCallback(() => {
		send({ type: 'stop' })
	}, [send])

	const onCancelQueued = useCallback(() => {
		if (queuedMsgs.length === 0) return
		const uuids = queuedMsgs.map((m) => m.uuid).filter((u) => u !== '')
		// Save snapshot — cancel_result arrives async, queuedMsgs may change
		pendingCancelRef.current = queuedMsgs
		if (uuids.length > 0) {
			send({ type: 'cancel_queued', uuids })
		} else {
			// No UUIDs stamped yet (all optimistic) — restore all text immediately
			const joined = queuedMsgs.map((m) => m.text).join('\n')
			inputRef.current?.appendQueuedText(joined)
			setQueuedMsgs([])
			pendingCancelRef.current = null
		}
	}, [queuedMsgs, send])

	// Drains streamTextAccum into the sink on mount. Needed because text_start
	// (or the text_delta else-branch) schedules a forceRender to mount the sink,
	// but between scheduling and React's commit, deltas may arrive and append to
	// streamTextAccum without a live DOM target. flushRef on mount drains them.
	const flushTextRef = useCallback(() => {
		if (streamTextRef.current) {
			streamTextRef.current.textContent = streamTextAccum.current
		}
	}, [])

	return (
		<div ref={scrollRef} className="overflow-y-auto overflow-x-hidden" style={{ height: '100dvh' }}>
			<Header />
			<div className="mx-auto max-w-2xl py-4">
				<div ref={topSentinelRef} style={{ height: 1 }} />
				<div className="space-y-7">
					{messagesRef.current.map((m, i) => {
						const isStreamingMsg =
							streamingRef.current &&
							i === messagesRef.current.length - 1 &&
							m.role === 'assistant' &&
							m.status === 'streaming'
						return isStreamingMsg ? (
							<StreamingMessage
								key={m.id}
								message={m}
								textRef={streamTextRef}
								thinkingRef={streamThinkingRef}
								flushTextRef={flushTextRef}
							/>
						) : (
							<MessageComponent key={m.id} message={m} />
						)
					})}
					{ask && <Ask ask={ask} />}
				</div>
				<div ref={bottomRef} />
			</div>
			<InputBar
				ref={inputRef}
				streaming={streaming}
				queuedMsgs={queuedMsgs}
				onSend={onSend}
				onStop={onStop}
				onCancelQueued={onCancelQueued}
			/>
		</div>
	)
}
