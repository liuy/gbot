import { useEffect, useRef, useState } from 'react'
import { useWebSocket } from '../websocket'
import type { ServerMessage, QueryEvent, HistoryChatMsg } from '../types'
import { newAssistantMessage, type ChatMessage, type Block } from '../model'
import MessageComponent from './MessageComponent'
import InputBar from './InputBar'
import Ask from './Ask'
import Header from './Header'

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
	const forceRender = () => setTick((t) => (t + 1) & 0x7fffffff)

	const [ask, setAsk] = useState<AskData | null>(null)
	const [queuedText, setQueuedText] = useState<string | null>(null)
	const streamingRef = useRef(false)
	const [streaming, setStreaming] = useState(false)
	const [nextCursor, setNextCursor] = useState(persistedNextCursor)
	const [hasMore, setHasMore] = useState(persistedHasMore)
	const loadingMoreRef = useRef(false)

	const scrollRef = useRef<HTMLDivElement | null>(null)
	const topSentinelRef = useRef<HTMLDivElement | null>(null)
	const bottomRef = useRef<HTMLDivElement | null>(null)

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
		setQueuedText(null)
		forceRender()
	}

	const loadHistory = (msg: Extract<ServerMessage, { type: 'history' }>) => {
		const histMsgs = msg.messages
		const newMsgs = mapHistoryToChatMessages(histMsgs)

		// Initial page (cursor was empty) replaces all messages.
		// Pagination page (cursor non-empty) prepends older messages.
		const isInitial = !nextCursor && !loadingMoreRef.current

		if (isInitial) {
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
		}
	}

	const handleEvent = (e: QueryEvent) => {
		switch (e.type) {
			case 'query_start': {
				messagesRef.current.push(newAssistantMessage(nextId('a')))
				streamingRef.current = true
				setStreaming(true)
				forceRender()
				return
			}
			case 'query_end': {
				updateStreamingAssistant((m) => ({ ...m, status: 'done' as const }))
				streamingRef.current = false
				setStreaming(false)
				setQueuedText(null)
				forceRender()
				return
			}
			case 'thinking_start': {
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
				forceRender()
				return
			}
			case 'thinking_end': {
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
				updateStreamingAssistant((m) => ({
					...m,
					blocks: [...m.blocks, { kind: 'text', id: nextId('txt'), text: '' }],
				}))
				forceRender()
				return
			}
			case 'text_delta': {
				if (!e.text) return
				updateStreamingAssistant((m) => {
					const blocks = [...m.blocks]
					for (let i = blocks.length - 1; i >= 0; i--) {
						if (blocks[i].kind === 'text') {
							blocks[i] = { ...blocks[i], text: (blocks[i] as any).text + e.text } as Block
							break
						}
					}
					return { ...m, blocks }
				})
				forceRender()
				return
			}
			case 'text_end': {
				forceRender()
				return
			}
			case 'tool_start': {
				if (!e.tool_use) return
				const tu = e.tool_use
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
					}],
				}))
				forceRender()
				return
			}
			case 'tool_param_delta': {
				if (!e.partial_input || !e.partial_input.summary) return
				const targetId = e.partial_input.id
				const summary = e.partial_input.summary
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
				return
			}
			case 'tool_end': {
				if (!e.tool_result) return
				const tr = e.tool_result
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
			default:
				return
		}
	}

	useEffect(() => {
		const unsubscribe = subscribe((msg: ServerMessage) => {
			switch (msg.type) {
				case 'connect_status':
					return
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
			{ root, threshold: 0 }
		)
		obs.observe(el)
		return () => obs.disconnect()
	}, [hasMore, nextCursor, send])

	const onSend = (text: string) => {
		if (streamingRef.current) {
			setQueuedText(text)
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
	}

	const onStop = () => {
		send({ type: 'stop' })
	}

	const onCancelQueued = () => {
		setQueuedText(null)
	}

	return (
		<div ref={scrollRef} className="overflow-y-auto overflow-x-hidden" style={{ height: '100dvh' }}>
			<Header />
			<div className="mx-auto max-w-2xl py-4">
				<div ref={topSentinelRef} style={{ height: 1 }} />
				<div className="space-y-7">
					{messagesRef.current.map((m) => (
						<MessageComponent key={m.id} message={m} />
					))}
					{ask && <Ask ask={ask} />}
				</div>
				<div ref={bottomRef} />
			</div>
			<InputBar
				streaming={streaming}
				queuedText={queuedText}
				onSend={onSend}
				onStop={onStop}
				onCancelQueued={onCancelQueued}
			/>
		</div>
	)
}
