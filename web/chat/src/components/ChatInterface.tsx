import { useCallback, useEffect, useRef, useState } from 'react'
import { useWebSocket } from '../websocket'
import type { ServerMessage, QueryEvent, HistoryChatMsg } from '../types'
import { newAssistantMessage, type ChatMessage, type Block } from '../model'
import MessageComponent from './MessageComponent'
import StreamingMessage from './StreamingMessage'
import InputBar, { type InputBarHandle } from './InputBar'
import Ask from './Ask'
import Header from './Header'
import {
	type ToolDomHandles,
	type ProgressDomHandles,
	appendTextBlock,
	appendThinkingBlock,
	appendUserBlock,
	appendToolBlock,
	appendToolChildrenContainer,
	appendProgressBar,
	setToolSummary,
	setToolOutput,
	setProgressBarUsage,
	refreshProgressBar,
	refreshToolDuration,
	refreshThinkingLabel,
	writeThinkingText,
	finishThinking,
	finishTool,
	expandToolChildrenForRunning,
	collapseToolChildrenOnDone,
} from './streamDom'

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

interface ToolEntry {
	handles: ToolDomHandles
	startedAt: number
	parentID: string | null
	pendingBlock: ToolBlock
}

interface ThinkingEntry {
	p: HTMLParagraphElement
	labelEl: HTMLSpanElement
	startedAt: number
	pendingBlock: Extract<Block, { kind: 'thinking' }>
}

export default function ChatInterface() {
	const { subscribe, send } = useWebSocket()
	const messagesRef = useRef<ChatMessage[]>(persistedMessages)
	const [, setTick] = useState(0)
	// Stabilized via useCallback so every useCallback below (and every
	// structural-event handler) closes over the SAME forceRender reference.
	// If this were a fresh function each render, those callbacks would
	// recapture a new ref and break the InputBar React.memo.
	const forceRender = useCallback(() => setTick((t) => (t + 1) & 0x7fffffff), [])

	// Streaming container ref: the inner <div className="space-y-3"> in
	// StreamingMessage. All streamDom appends target this node directly.
	const streamContainerRef = useRef<HTMLDivElement | null>(null)
	const streamStartedAt = useRef(0)

	// All the streaming bookkeeping refs. Cleared on query_end.
	const toolEntries = useRef<Map<string, ToolEntry>>(new Map())
	const streamAccum = useRef<{ text: string; thinking: string }>({ text: '', thinking: '' })
	const currentTextDiv = useRef<HTMLDivElement | null>(null)
	const currentThinking = useRef<ThinkingEntry | null>(null)
	const progressHandles = useRef<ProgressDomHandles | null>(null)
	const progressUsage = useRef<{ inputTokens: number; outputTokens: number }>({ inputTokens: 0, outputTokens: 0 })
	const refreshIntervalRef = useRef<number | null>(null)

	// pendingBlocks mirror: parallel Block tree kept in sync with streamDom
	// during streaming. query_end commits a slice of this array to the
	// assistant message, then MessageComponent renders the final state.
	const pendingBlocks = useRef<Block[]>([])
	const pendingToolByID = useRef<Map<string, ToolBlock>>(new Map())
	// Index of the last text block at top-level, so text_delta knows where
	// to mutate. Lazily created in text_delta when no text block exists yet.
	const currentPendingText = useRef<{ block: { kind: 'text'; id: string; text: string } } | null>(null)
	// Per-parent lazy sinks for sub-agent text/thinking during streaming.
	const currentSubAgentTextDiv = useRef<Map<string, HTMLDivElement>>(new Map())
	const currentSubAgentThinking = useRef<Map<string, ThinkingEntry>>(new Map())

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
		cleanupStreamingRefs()
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

	// Finalize any tools that never got a tool_end. Walks recursively so
	// sub-agent tools at any nesting depth are covered. Mutates state in
	// place. Called from query_end right before committing pendingBlocks.
	function finalizeRunningBlocks(blocks: Block[]) {
		for (const b of blocks) {
			if (b.kind === 'tool') {
				if (b.state === 'running') {
					b.state = 'done'
					if (!b.timingNs) b.timingNs = (Date.now() - b.startedAt) * 1e6
				}
				if (b.children.length > 0) finalizeRunningBlocks(b.children)
			}
		}
	}

	// Drop all streaming refs and tear down the refresh timer. Called at
	// query_end (both abort and normal) and on appendError.
	const cleanupStreamingRefs = () => {
		if (refreshIntervalRef.current !== null) {
			clearInterval(refreshIntervalRef.current)
			refreshIntervalRef.current = null
		}
		streamContainerRef.current = null
		toolEntries.current.clear()
		streamAccum.current = { text: '', thinking: '' }
		currentTextDiv.current = null
		currentThinking.current = null
		progressHandles.current = null
		progressUsage.current = { inputTokens: 0, outputTokens: 0 }
		pendingBlocks.current = []
		pendingToolByID.current.clear()
		currentPendingText.current = null
		currentSubAgentTextDiv.current.clear()
		currentSubAgentThinking.current.clear()
	}

	// DOM anchor for keeping the progress bar pinned to the bottom of the
	// streaming container. New blocks insertBefore this anchor.
	const progressAnchor = (): Node | null => progressHandles.current?.root ?? null

	// Build a new tool block with the same shape used in the old streaming
	// handlers and loadHistory. Pure construction — no side effects.
	function buildToolBlock(tu: { id: string; name: string; is_search?: boolean; is_read?: boolean; is_list?: boolean; is_lsp?: boolean }): ToolBlock {
		return {
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
		}
	}

	// Returns the `children` array to push sub-agent blocks into. Recurses
	// via pendingToolByID so depth ≥ 2 nests naturally (agent-2 → agent-1).
	function pendingChildrenFor(parentID: string): Block[] | null {
		const parent = pendingToolByID.current.get(parentID)
		if (!parent) return null
		return parent.children
	}

	// Returns the DOM container for sub-agent events: lazily creates the
	// children container if not yet present.
	function subAgentContainer(parentID: string): HTMLElement | null {
		const entry = toolEntries.current.get(parentID)
		if (!entry) return null
		return appendToolChildrenContainer(entry.handles)
	}

	// Auto-expand a parent tool when a sub-agent event first lands. Matches
	// ToolRenderer.tsx auto-expand-on-children behavior for running agent tools.
	function maybeAutoExpandParent(parentID: string) {
		const entry = toolEntries.current.get(parentID)
		if (!entry) return
		// Only expand running parents (collapsed-on-done wins otherwise).
		if (entry.pendingBlock.state === 'running') {
			expandToolChildrenForRunning(entry.handles)
		}
	}

	const handleEvent = (e: QueryEvent) => {
		const ctx = e.agent ? `agent:${e.agent.parent_tool_use_id}` : 'main'
		const tid = e.tool_use?.id ?? e.tool_result?.tool_use_id ?? ''
		console.log(`[webchat] ${e.type} ctx=${ctx}${tid ? ` tool=${tid}` : ''} streaming=${streamingRef.current}`)
		switch (e.type) {
			case 'query_start': {
				if (e.agent) return
				cleanupStreamingRefs()
				streamAccum.current = { text: '', thinking: '' }
				messagesRef.current.push(newAssistantMessage(nextId('a')))
				streamStartedAt.current = Date.now()
				streamingRef.current = true
				setStreaming(true)
				forceRender()
				return
			}
			case 'turn_start': {
				// Sub-engine turnStart: do NOT push a new assistant message.
				if (e.agent) return
				// processAttachments path emits turn_start without query_start.
				if (streamingRef.current) return
				cleanupStreamingRefs()
				streamAccum.current = { text: '', thinking: '' }
				messagesRef.current.push(newAssistantMessage(nextId('a')))
				streamStartedAt.current = Date.now()
				streamingRef.current = true
				setStreaming(true)
				forceRender()
				return
			}
			case 'query_end': {
				// Sub-agent queryEnd: do NOT finish the main query stream.
				if (e.agent) return
				const wasAborted = !!e.aborted

				// Finalize any tools that never received tool_end (e.g., stream
				// cut off mid-tool). Their state flips running→done so the
				// post-query_end MessageComponent render matches ToolRenderer's
				// collapsed-by-default UX for finished tools.
				finalizeRunningBlocks(pendingBlocks.current)

				if (wasAborted) {
					const list = messagesRef.current
					const last = list[list.length - 1]
					// pendingBlocks is the in-flight source of truth (last.blocks
					// stays empty until query_end in this architecture).
					const hasContent = !!last && last.role === 'assistant' &&
						pendingBlocks.current.some(b =>
							(b.kind === 'text' && b.text.trim()) ||
							b.kind === 'tool' ||
							b.kind === 'user',
						)

					if (hasContent) {
						// COMMIT path: interrupt marker text already landed in
						// pendingBlocks via text_delta before query_end.
						if (last && last.role === 'assistant') {
							last.blocks = pendingBlocks.current.slice()
							last.status = 'done'
						}
					} else {
						// REWIND path: no meaningful content. Pop empty assistant
						// message and restore the user's input text. Matches TUI
						// tryAutoRewind (input.SetValue).
						if (last && last.role === 'assistant') {
							list.pop()
						}
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
					// Normal completion: commit pendingBlocks to the assistant message.
					const list = messagesRef.current
					const last = list[list.length - 1]
					if (last && last.role === 'assistant') {
						last.blocks = pendingBlocks.current.slice()
						last.status = 'done'
					}
				}

				streamingRef.current = false
				setStreaming(false)
				setQueuedMsgs([])
				cleanupStreamingRefs()
				forceRender()
				return
			}
			case 'thinking_start': {
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const container = pendingChildrenFor(parentID)
					if (!container) return
					maybeAutoExpandParent(parentID)
					const domContainer = subAgentContainer(parentID)
					const entry: ThinkingEntry = createThinkingEntry()
					container.push(entry.pendingBlock)
					// Sub-agent thinking tracked per-parent.
					currentSubAgentThinking.current.set(parentID, entry)
					if (domContainer) {
						domContainer.appendChild(entry.p.parentElement!)
					}
					return
				}
				streamAccum.current.thinking = ''
				const entry = createThinkingEntry()
				pendingBlocks.current.push(entry.pendingBlock)
				currentThinking.current = entry
				if (streamContainerRef.current) {
					const anchor = progressAnchor()
					if (anchor) streamContainerRef.current.insertBefore(entry.p.parentElement!, anchor)
					else streamContainerRef.current.appendChild(entry.p.parentElement!)
				}
				return
			}
			case 'thinking_delta': {
				if (!e.thinking?.text) return
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const entry = currentSubAgentThinking.current.get(parentID)
					if (!entry) {
						// Lazily create the thinking block (text_start is a no-op for sub-agents).
						const container = pendingChildrenFor(parentID)
						const domContainer = subAgentContainer(parentID)
						if (!container || !domContainer) return
						maybeAutoExpandParent(parentID)
						const newEntry = createThinkingEntry()
						container.push(newEntry.pendingBlock)
						currentSubAgentThinking.current.set(parentID, newEntry)
						newEntry.pendingBlock.text += e.thinking.text
						writeThinkingText(newEntry.p, newEntry.pendingBlock.text)
						domContainer.appendChild(newEntry.p.parentElement!)
						return
					}
					entry.pendingBlock.text += e.thinking.text
					writeThinkingText(entry.p, entry.pendingBlock.text)
					return
				}
				if (!currentThinking.current) return
				streamAccum.current.thinking += e.thinking.text
				currentThinking.current.pendingBlock.text += e.thinking.text
				writeThinkingText(currentThinking.current.p, streamAccum.current.thinking)
				return
			}
			case 'thinking_end': {
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const entry = currentSubAgentThinking.current.get(parentID)
					if (!entry) return
					entry.pendingBlock.active = false
					entry.pendingBlock.durationNs = e.thinking?.duration ?? entry.pendingBlock.durationNs
					finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
					currentSubAgentThinking.current.delete(parentID)
					return
				}
				if (!currentThinking.current) return
				const entry = currentThinking.current
				entry.pendingBlock.active = false
				entry.pendingBlock.durationNs = e.thinking?.duration ?? entry.pendingBlock.durationNs
				finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
				currentThinking.current = null
				return
			}
			case 'text_start': {
				// Sub-agent text_start is a no-op: text_delta creates the block
				// lazily inside the parent tool. Matches TUI textStartMsg.
				if (e.agent) return
				streamAccum.current.text = ''
				// Always create a fresh text block on text_start (matches the
				// old streaming handler which pushed a new block per text_start).
				startNewTextBlock()
				return
			}
			case 'text_delta': {
				if (!e.text) return
				if (e.agent) {
					const parentID = e.agent.parent_tool_use_id
					const container = pendingChildrenFor(parentID)
					if (!container) return // unknown parent: silently dropped
					maybeAutoExpandParent(parentID)
					const domContainer = subAgentContainer(parentID)
					const last = container[container.length - 1]
					if (last && last.kind === 'text') {
						(last as any).text += e.text
					} else {
						const newBlock = { kind: 'text' as const, id: nextId('txt'), text: e.text }
						container.push(newBlock)
						if (domContainer) {
							const div = appendTextBlock(domContainer)
							div.textContent = e.text
							currentSubAgentTextDiv.current.set(parentID, div)
						}
						return
					}
					if (domContainer) {
						let div = currentSubAgentTextDiv.current.get(parentID)
						if (!div) {
							div = appendTextBlock(domContainer)
							currentSubAgentTextDiv.current.set(parentID, div)
						}
						div.textContent = (last as any).text
					}
					return
				}
				streamAccum.current.text += e.text
				if (!currentTextDiv.current || !currentPendingText.current) {
					startNewTextBlock()
				}
				if (currentTextDiv.current) {
					currentTextDiv.current.textContent = streamAccum.current.text
					if (currentPendingText.current) currentPendingText.current.block.text = streamAccum.current.text
					scrollToBottom()
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
					const container = pendingChildrenFor(parentID)
					if (!container) return // unknown parent: silently dropped
					maybeAutoExpandParent(parentID)
					const domContainer = subAgentContainer(parentID)
					const block = buildToolBlock(tu)
					container.push(block)
					pendingToolByID.current.set(tu.id, block)
					if (domContainer) {
						const srk = classifyToolName(tu.name)
					const collapsible = srk.isSearch || srk.isRead || srk.isList || srk.isLsp || srk.isWeb
					const handles = appendToolBlock(domContainer, tu.name, undefined, collapsible)
						toolEntries.current.set(tu.id, { handles, startedAt: block.startedAt, parentID, pendingBlock: block })
					}
					return
				}
				const block = buildToolBlock(tu)
				pendingBlocks.current.push(block)
				pendingToolByID.current.set(tu.id, block)
				if (streamContainerRef.current) {
					const srk = classifyToolName(tu.name)
				const collapsible = srk.isSearch || srk.isRead || srk.isList || srk.isLsp || srk.isWeb
				const handles = appendToolBlock(streamContainerRef.current, tu.name, progressAnchor(), collapsible)
					toolEntries.current.set(tu.id, { handles, startedAt: block.startedAt, parentID: null, pendingBlock: block })
				}
				return
			}
			case 'tool_param_delta': {
				if (!e.partial_input || !e.partial_input.summary) return
				const targetId = e.partial_input.id
				const summary = e.partial_input.summary
				const entry = toolEntries.current.get(targetId)
				if (entry) setToolSummary(entry.handles, summary)
				const pending = pendingToolByID.current.get(targetId)
				if (pending) pending.summary = summary
				return
			}
			case 'tool_run': {
				return // no-op
			}
			case 'tool_output_delta': {
				if (e.agent && e.tool_result) {
					const targetId = e.tool_result.tool_use_id
					const output = e.tool_result.display_output ?? ''
					const entry = toolEntries.current.get(targetId)
					if (entry) setToolOutput(entry.handles, output)
					const pending = pendingToolByID.current.get(targetId)
					if (pending) pending.displayOutput = output
				}
				return
			}
			case 'tool_end': {
				if (!e.tool_result) return
				const tr = e.tool_result
				const entry = toolEntries.current.get(tr.tool_use_id)
				const durationNs = entry ? (Date.now() - entry.startedAt) * 1e6 : 0
				const output = tr.display_output ?? ''
				const pending = pendingToolByID.current.get(tr.tool_use_id)
				if (entry) {
					finishTool(entry.handles, { isError: !!tr.is_error, durationNs, output })
				}
				if (pending) {
					pending.state = tr.is_error ? 'error' : 'done'
					pending.timingNs = durationNs
					pending.displayOutput = output
					if (tr.is_search !== undefined) pending.isSearch = tr.is_search
					if (tr.is_read !== undefined) pending.isRead = tr.is_read
					if (tr.is_list !== undefined) pending.isList = tr.is_list
					if (tr.is_lsp !== undefined) pending.isLsp = tr.is_lsp
				}
				// TUI parity: agent tools auto-collapse on done (state transition running→done).
				if (entry && entry.pendingBlock.children.length > 0 && entry.pendingBlock.state !== 'running') {
					collapseToolChildrenOnDone(entry.handles)
				}
				toolEntries.current.delete(tr.tool_use_id)
				return
			}
			case 'usage': {
				if (!e.usage_event) return
				// Sub-agent usage is reported via the parent Agent tool's
				// result, not merged into the top-level assistant's usage.
				if (e.agent) return
				const u = e.usage_event
				progressUsage.current = {
					inputTokens: u.input_tokens,
					outputTokens: u.output_tokens,
				}
				if (progressHandles.current) {
					setProgressBarUsage(progressHandles.current, {
						inputTokens: u.input_tokens,
						outputTokens: u.output_tokens,
						cacheRead: u.cache_read_input_tokens ?? 0,
						cacheCreation: u.cache_creation_input_tokens ?? 0,
					})
				}
				// Mirror into the in-flight assistant message's usage so the
				// post-query_end MessageComponent render shows the right tally.
				const list = messagesRef.current
				const last = list[list.length - 1]
				if (last && last.role === 'assistant' && last.status === 'streaming') {
					last.usage = {
						inputTokens: u.input_tokens,
						outputTokens: u.output_tokens,
						cacheRead: u.cache_read_input_tokens ?? 0,
						cacheCreation: u.cache_creation_input_tokens ?? 0,
					}
				}
				return
			}
			case 'retry_attempt':
				return
			case 'attachment': {
				// TUI parity: repl.go:1364 — mid-turn drain appends
				// BlockUser inside the current assistant message's blocks.
				const att = (e as any).message?.attachment
				if (!att) return
				const text: string = att.prompt ?? ''
				const sourceUUID: string = att.source_uuid ?? ''
				if (!text) return
				if (streamingRef.current) {
					// Mid-turn: append user block to streaming DOM + pendingBlocks.
					pendingBlocks.current.push({ kind: 'user', id: nextId('u'), text })
					if (streamContainerRef.current) {
						appendUserBlock(streamContainerRef.current, text, progressAnchor())
					}
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
					forceRender()
				}
				if (sourceUUID === '') return
				setQueuedMsgs((prev) => prev.filter((m) => m.uuid !== sourceUUID))
				return
			}
			default:
				return
		}
	}

	// Builds a ThinkingEntry (DOM + pendingBlock) but does NOT attach it to
	// the streaming container. The caller appends `entry.p.parentElement`
	// (the wrap div containing header + <p>) into the chosen container.
	function createThinkingEntry(): ThinkingEntry {
		const startedAt = Date.now()
		const temp = document.createElement('div')
		const { p, labelEl } = appendThinkingBlock(temp, startedAt)
		const wrap = temp.firstChild as HTMLElement
		// Detach wrap from temp so the caller chooses where to append it.
		// Event listeners on the header survive detachment.
		temp.removeChild(wrap)
		const pendingBlock: Extract<Block, { kind: 'thinking' }> = {
			kind: 'thinking',
			id: nextId('th'),
			text: '',
			durationNs: 0,
			active: true,
			startedAt,
		}
		return { p, labelEl, startedAt, pendingBlock }
	}

	// Lazily creates a new top-level text block (DOM + pending). Called from
	// text_start (always fresh) and from text_delta when no sink is mounted
	// (text_delta arrived without a preceding text_start — covered by
	// attachment_streaming.test.tsx and attachment_idle.test.tsx).
	function startNewTextBlock() {
		const block = { kind: 'text' as const, id: nextId('txt'), text: streamAccum.current.text }
		pendingBlocks.current.push(block)
		currentPendingText.current = { block }
		if (streamContainerRef.current) {
			currentTextDiv.current = appendTextBlock(streamContainerRef.current, progressAnchor())
			currentTextDiv.current.textContent = streamAccum.current.text
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

	// Mount-effect: when streaming flips on, StreamingMessage mounts and
	// streamContainerRef.current becomes non-null. Drain any deltas that
	// arrived before commit, attach the progress bar, and start the
	// 200ms refresh interval. Cleared on cleanupStreamingRefs at query_end.
	useEffect(() => {
		if (!streaming) return
		if (!streamContainerRef.current) return
		// Drain late deltas: if text arrived before this mount committed,
		// spin up the sink now and populate it.
		if (streamAccum.current.text && !currentTextDiv.current) {
			startNewTextBlock()
		} else if (currentTextDiv.current) {
			currentTextDiv.current.textContent = streamAccum.current.text
		}
		// Append the progress bar (last child by default — no anchor).
		if (!progressHandles.current) {
			progressHandles.current = appendProgressBar(streamContainerRef.current)
		}
		// 200ms refresh interval: live elapsed/rate/duration for tools + thinking.
		const id = window.setInterval(() => {
			toolEntries.current.forEach((entry) => {
				if (entry.pendingBlock.state === 'running') {
					refreshToolDuration(entry.handles, entry.startedAt)
				}
			})
			if (currentThinking.current) {
				refreshThinkingLabel(currentThinking.current.labelEl, currentThinking.current.startedAt)
			}
			currentSubAgentThinking.current.forEach((entry) => {
				if (entry.pendingBlock.active) {
					refreshThinkingLabel(entry.labelEl, entry.startedAt)
				}
			})
			if (progressHandles.current) {
				refreshProgressBar(
					progressHandles.current,
					streamStartedAt.current,
					pendingBlocks.current.filter(b => b.kind === 'tool').length,
					progressUsage.current.outputTokens,
				)
			}
		}, 200)
		refreshIntervalRef.current = id
		return () => {
			clearInterval(id)
			refreshIntervalRef.current = null
		}
	}, [streaming])

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
								ref={streamContainerRef}
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
