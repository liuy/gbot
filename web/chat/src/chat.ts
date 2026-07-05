import type { ServerMessage, QueryEvent, HistoryChatMsg } from './types'
import {
  type Block,
  type ChatMessage,
  newAssistantMessage,
} from './model'
import { isCollapsibleToolName } from './utils'
import { renderMarkdown } from './markdown'
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
} from './components/streamDom'
import { createHeader } from './header'
import {
  createInputBar,
  type InputBarHandles,
} from './inputBar'
import { createAsk } from './ask'
import { getConnection } from './ws'

type ToolBlock = Extract<Block, { kind: 'tool' }>

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

interface MessageState {
  id: string
  role: 'user' | 'assistant'
  blocks: Block[]
  usage: {
    inputTokens: number
    outputTokens: number
    cacheRead: number
    cacheCreation: number
  }
  error: string
  status: 'streaming' | 'done'
  startedAt: number
  domRoot: HTMLElement
  contentDiv: HTMLDivElement
}

export interface ChatHandles {
  root: HTMLElement
  scrollEl: HTMLElement
  inputBar: InputBarHandles
  cleanup: () => void
}

let msgIdCounter = 0
function nextId(prefix: string): string {
  msgIdCounter += 1
  return `${prefix}-${msgIdCounter}`
}

function classifyToolName(name: string): {
  isSearch: boolean
  isRead: boolean
  isList: boolean
  isLsp: boolean
  isWeb: boolean
} {
  switch (name) {
    case 'Read':
      return { isRead: true, isSearch: false, isList: false, isLsp: false, isWeb: false }
    case 'Grep':
    case 'Glob':
      return { isSearch: true, isRead: false, isList: false, isLsp: false, isWeb: false }
    case 'Lsp':
      return { isLsp: true, isSearch: false, isRead: false, isList: false, isWeb: false }
    case 'Web':
      return { isWeb: true, isSearch: false, isRead: false, isList: false, isLsp: false }
    default:
      return { isSearch: false, isRead: false, isList: false, isLsp: false, isWeb: false }
  }
}

function mapHistoryToChatMessages(histMsgs: HistoryChatMsg[]): ChatMessage[] {
  const merged: HistoryChatMsg[] = []
  for (const h of histMsgs) {
    if (h.role === 'user' && (!h.text || h.text.trim() === '')) continue
    const last = merged[merged.length - 1]
    if (last && last.role === 'assistant' && h.role === 'assistant') {
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
      if (h.text) {
        m.blocks.push({ kind: 'text', id: nextId('txt'), text: h.text })
      }
    }
    result.push(m)
  }
  return result
}

const avatarGClass =
  'flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-blue to-violet text-[11px] font-bold text-white'
const avatarUClass =
  'flex h-5 w-5 shrink-0 items-center justify-center rounded-md bg-gradient-to-br from-t2 to-t3'

function buildShell(
  role: 'user' | 'assistant',
): { outer: HTMLElement; content: HTMLDivElement } {
  const outer = document.createElement('div')
  outer.className = 'px-1.5'
  const grid = document.createElement('div')
  grid.className =
    'grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5'

  const leftCol = document.createElement('div')
  const centerCol = document.createElement('div')
  centerCol.className = 'min-w-0'
  const rightCol = document.createElement('div')

  if (role === 'assistant') {
    leftCol.className = avatarGClass
    leftCol.textContent = 'G'
  } else {
    rightCol.className = avatarUClass
    rightCol.innerHTML =
      '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2"><circle cx="12" cy="8" r="4" /><path d="M4 21v-1a8 8 0 0116 0v1" /></svg>'
  }

  const content = document.createElement('div')
  if (role === 'assistant') {
    content.className = 'space-y-3'
  } else {
    content.className = 'ml-auto w-fit text-left text-t1 text-[15px]'
  }

  centerCol.appendChild(content)
  grid.appendChild(leftCol)
  grid.appendChild(centerCol)
  grid.appendChild(rightCol)
  outer.appendChild(grid)
  return { outer, content }
}

function isCollapsibleToolBlock(b: Block): boolean {
  return (
    b.kind === 'tool' &&
    (b.isSearch ||
      b.isRead ||
      b.isList ||
      b.isLsp ||
      b.isWeb ||
      isCollapsibleToolName(b.name))
  )
}

// Build committed message DOM by replaying blocks through streamDom appenders.
// Produces the same visual structure streaming builds, so loadHistory output
// is indistinguishable from a message that just finished streaming.
function renderCommittedMessageDOM(
  m: ChatMessage,
): { outer: HTMLElement; content: HTMLDivElement } {
  if (m.role === 'user') {
    const { outer, content } = buildShell('user')
    const text = m.blocks
      .filter((b) => b.kind === 'text' || b.kind === 'user')
      .map((b) => (b as { text: string }).text)
      .join('')
    const span = document.createElement('span')
    span.textContent = text
    content.appendChild(span)
    if (m.error) {
      const err = document.createElement('div')
      err.className =
        'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red'
      err.textContent = m.error
      content.appendChild(err)
    }
    return { outer, content }
  }

  const { outer, content } = buildShell('assistant')

  // Sequential append — findPrevToolSibling in streamDom handles cross-thinking grouping.
  // Same code path as streaming, no buffer, no rebuild.
  for (const b of m.blocks) {
    if (b.kind === 'thinking') {
      const { p, labelEl } = appendThinkingBlock(content, 0)
      if (b.text) writeThinkingText(p, b.text)
      finishThinking(p, labelEl, b.durationNs)
    } else if (b.kind === 'tool') {
      const collapsible = isCollapsibleToolName(b.name) || isCollapsibleToolBlock(b)
      const handles = appendToolBlock(content, b.name, undefined, collapsible)
      if (b.summary) setToolSummary(handles, b.summary)
      finishTool(handles, {
        isError: b.state === 'error',
        durationNs: b.timingNs,
        output: b.displayOutput,
      })
    } else if (b.kind === 'text') {
      if (!b.text) continue
      const div = appendTextBlock(content)
      div.innerHTML = renderMarkdown(b.text)
    } else if (b.kind === 'user') {
      if (!b.text) continue
      appendUserBlock(content, b.text)
    }
  }

  if (m.error) {
    const err = document.createElement('div')
    err.className =
      'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red'
    err.textContent = m.error
    content.appendChild(err)
  }
  return { outer, content }
}

export function createChat(initial: { connected: boolean }): ChatHandles {
  // ── Module-level state (persists across createChat calls in the same
  // session, mirroring persistedMessages in ChatInterface.tsx).
  const messages: MessageState[] = []
  let nextCursor = ''
  let hasMore = false
  let expectingInitial = true
  let loadingMore = false

  // ── Streaming refs (cleared on query_end).
  let streamContainer: HTMLDivElement | null = null
  let streamStartedAt = 0
  let streaming = false
  const toolEntries = new Map<string, ToolEntry>()
  let streamAccum = { text: '', thinking: '' }
  let currentTextDiv: HTMLDivElement | null = null
  let currentPendingText: { block: { kind: 'text'; id: string; text: string } } | null = null
  let currentThinking: ThinkingEntry | null = null
  let progressHandles: ProgressDomHandles | null = null
  let progressUsage = { inputTokens: 0, outputTokens: 0 }
  let refreshInterval: number | null = null
  const pendingBlocks: Block[] = []
  const pendingToolByID = new Map<string, ToolBlock>()
  const currentSubAgentTextDiv = new Map<string, HTMLDivElement>()
  const currentSubAgentThinking = new Map<string, ThinkingEntry>()
  let pendingCancel: { uuid: string; text: string }[] | null = null
  let queuedMsgs: { uuid: string; text: string }[] = []

  // ── Shell DOM.
  const root = document.createElement('div')
  root.className = 'overflow-y-auto overflow-x-hidden'
  root.style.height = '100dvh'

  const header = createHeader()
  header.setStatus(initial.connected)
  root.appendChild(header.root)

  const wrapper = document.createElement('div')
  wrapper.className = 'mx-auto max-w-2xl py-4'

  const topSentinel = document.createElement('div')
  topSentinel.style.height = '1px'
  const messagesContainer = document.createElement('div')
  messagesContainer.className = 'space-y-7'
  const bottomSentinel = document.createElement('div')

  wrapper.appendChild(topSentinel)
  wrapper.appendChild(messagesContainer)
  wrapper.appendChild(bottomSentinel)
  root.appendChild(wrapper)

  const inputBar = createInputBar({ connected: initial.connected })
  root.appendChild(inputBar.root)

  const conn = getConnection()

  let isNearBottom = true
  let lastScrollHeight = 0
  root.addEventListener('scroll', () => {
    const near = root.scrollHeight - root.scrollTop - root.clientHeight < 120
    if (near !== isNearBottom) isNearBottom = near
  }, { passive: true })
  const scrollToBottom = () => {
    if (!isNearBottom) return
    const sh = root.scrollHeight
    if (sh === lastScrollHeight) return
    lastScrollHeight = sh
    requestAnimationFrame(() => {
      if (!isNearBottom) return
      bottomSentinel.scrollIntoView({ behavior: 'auto' })
    })
  }

  const progressAnchor = (): Node | null => progressHandles?.root ?? null

  const cleanupStreamingRefs = () => {
    if (refreshInterval !== null) {
      clearInterval(refreshInterval)
      refreshInterval = null
    }
    streamContainer = null
    toolEntries.clear()
    streamAccum = { text: '', thinking: '' }
    currentTextDiv = null
    currentThinking = null
    progressHandles = null
    progressUsage = { inputTokens: 0, outputTokens: 0 }
    pendingBlocks.length = 0
    pendingToolByID.clear()
    currentPendingText = null
    currentSubAgentTextDiv.clear()
    currentSubAgentThinking.clear()
  }

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

  function buildToolBlock(tu: {
    id: string
    name: string
    is_search?: boolean
    is_read?: boolean
    is_list?: boolean
    is_lsp?: boolean
  }): ToolBlock {
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

  function pendingChildrenFor(parentID: string): Block[] | null {
    const parent = pendingToolByID.get(parentID)
    if (!parent) return null
    return parent.children
  }

  function subAgentContainer(parentID: string): HTMLElement | null {
    const entry = toolEntries.get(parentID)
    if (!entry) return null
    return appendToolChildrenContainer(entry.handles)
  }

  function maybeAutoExpandParent(parentID: string) {
    const entry = toolEntries.get(parentID)
    if (!entry) return
    if (entry.pendingBlock.state === 'running') {
      expandToolChildrenForRunning(entry.handles)
    }
  }

  function createThinkingEntry(): ThinkingEntry {
    const startedAt = Date.now()
    const temp = document.createElement('div')
    const { p, labelEl } = appendThinkingBlock(temp, startedAt)
    const wrap = temp.firstChild as HTMLElement
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

  function startNewTextBlock() {
    const block = {
      kind: 'text' as const,
      id: nextId('txt'),
      text: streamAccum.text,
    }
    pendingBlocks.push(block)
    currentPendingText = { block }
    if (streamContainer) {
      currentTextDiv = appendTextBlock(streamContainer, progressAnchor())
      currentTextDiv.innerHTML = renderMarkdown(streamAccum.text)
    }
  }

  function setupStreaming() {
    if (!streamContainer) return
    // Drain late deltas: text arrived before the sink was mounted.
    if (streamAccum.text && !currentTextDiv) {
      startNewTextBlock()
    } else if (currentTextDiv) {
      currentTextDiv.innerHTML = renderMarkdown(streamAccum.text)
    }
    if (!progressHandles) {
      progressHandles = appendProgressBar(streamContainer)
    }
    refreshInterval = window.setInterval(() => {
      toolEntries.forEach((entry) => {
        if (entry.pendingBlock.state === 'running') {
          refreshToolDuration(entry.handles, entry.startedAt)
        }
      })
      if (currentThinking) {
        refreshThinkingLabel(currentThinking.labelEl, currentThinking.startedAt)
      }
      currentSubAgentThinking.forEach((entry) => {
        if (entry.pendingBlock.active) {
          refreshThinkingLabel(entry.labelEl, entry.startedAt)
        }
      })
      if (progressHandles) {
        refreshProgressBar(
          progressHandles,
          streamStartedAt,
          pendingBlocks.filter((b) => b.kind === 'tool').length,
          progressUsage.outputTokens,
        )
      }
    }, 200)
  }

  function appendError(text: string) {
    const last = messages[messages.length - 1]
    if (last && last.role === 'assistant' && last.status === 'streaming') {
      last.error = text
      last.status = 'done'
      const err = document.createElement('div')
      err.className =
        'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red'
      err.textContent = text
      last.contentDiv.appendChild(err)
    } else {
      const { outer, content } = buildShell('assistant')
      const err = document.createElement('div')
      err.className =
        'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red'
      err.textContent = text
      content.appendChild(err)
      const m: MessageState = {
        id: nextId('a'),
        role: 'assistant',
        blocks: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: text,
        status: 'done',
        startedAt: Date.now(),
        domRoot: outer,
        contentDiv: content,
      }
      messages.push(m)
      messagesContainer.appendChild(outer)
    }
    streaming = false
    inputBar.setStreaming(false)
    queuedMsgs = []
    inputBar.setQueuedMsgs([])
    cleanupStreamingRefs()
  }

  function loadHistory(msg: Extract<ServerMessage, { type: 'history' }>) {
    const newMsgs = mapHistoryToChatMessages(msg.messages)
    const isInitial = expectingInitial

    if (isInitial) {
      expectingInitial = false
      // Replace DOM children + state list.
      for (const m of messages) m.domRoot.remove()
      messages.length = 0
      for (const chat of newMsgs) {
        const { outer, content } = renderCommittedMessageDOM(chat)
        const m: MessageState = {
          id: chat.id,
          role: chat.role,
          blocks: chat.blocks,
          usage: chat.usage,
          error: chat.error,
          status: chat.status,
          startedAt: chat.startedAt,
          domRoot: outer,
          contentDiv: content,
        }
        messages.push(m)
        messagesContainer.appendChild(outer)
      }
    } else {
      const existingIds = new Set(messages.map((m) => m.id))
      const deduped = newMsgs.filter((m) => !existingIds.has(m.id))
      if (deduped.length === 0) {
        loadingMore = false
        return
      }
      // Pagination: prepend older messages.
      const prevScrollHeight = root.scrollHeight
      const prevScrollTop = root.scrollTop
      const before = messagesContainer.firstChild
      for (const chat of deduped) {
        const { outer, content } = renderCommittedMessageDOM(chat)
        const m: MessageState = {
          id: chat.id,
          role: chat.role,
          blocks: chat.blocks,
          usage: chat.usage,
          error: chat.error,
          status: chat.status,
          startedAt: chat.startedAt,
          domRoot: outer,
          contentDiv: content,
        }
        messages.unshift(m)
        messagesContainer.insertBefore(outer, before)
      }
      requestAnimationFrame(() => {
        const delta = root.scrollHeight - prevScrollHeight
        root.scrollTop = prevScrollTop + delta
      })
    }
    nextCursor = msg.nextCursor
    hasMore = msg.hasMore
    loadingMore = false
    if (isInitial) {
      requestAnimationFrame(() => {
        bottomSentinel.scrollIntoView({ behavior: 'smooth' })
      })
      if (msg.hasMore && msg.nextCursor) {
        loadingMore = true
        conn.send({ type: 'history_request', cursor: msg.nextCursor, limit: 10 })
      }
    }
  }

  function handleEvent(e: QueryEvent) {
    switch (e.type) {
      case 'query_start': {
        if (e.agent) return
        cleanupStreamingRefs()
        streamAccum = { text: '', thinking: '' }
        const { outer, content } = buildShell('assistant')
        const m: MessageState = {
          ...newAssistantMessage(nextId('a')),
          domRoot: outer,
          contentDiv: content,
        }
        messages.push(m)
        messagesContainer.appendChild(outer)
        streamContainer = content
        streamStartedAt = Date.now()
        streaming = true
        inputBar.setStreaming(true)
        setupStreaming()
        return
      }
      case 'turn_start': {
        if (e.agent) return
        if (streaming) return
        cleanupStreamingRefs()
        streamAccum = { text: '', thinking: '' }
        const { outer, content } = buildShell('assistant')
        const m: MessageState = {
          ...newAssistantMessage(nextId('a')),
          domRoot: outer,
          contentDiv: content,
        }
        messages.push(m)
        messagesContainer.appendChild(outer)
        streamContainer = content
        streamStartedAt = Date.now()
        streaming = true
        inputBar.setStreaming(true)
        setupStreaming()
        return
      }
      case 'query_end': {
        if (e.agent) return
        const wasAborted = !!e.aborted
        finalizeRunningBlocks(pendingBlocks)

        if (wasAborted) {
          const last = messages[messages.length - 1]
          const hasContent =
            !!last &&
            last.role === 'assistant' &&
            pendingBlocks.some(
              (b) =>
                (b.kind === 'text' && b.text.trim()) ||
                b.kind === 'tool' ||
                b.kind === 'user',
            )

          if (hasContent) {
            // COMMIT path: DOM stays; record the snapshot for history/reconnect.
            if (last && last.role === 'assistant') {
              last.blocks = pendingBlocks.slice()
              last.status = 'done'
            }
          } else {
            // REWIND path: no content. Remove the empty assistant shell +
            // restore the user's input text. Matches TUI tryAutoRewind.
            if (last && last.role === 'assistant') {
              last.domRoot.remove()
              messages.pop()
            }
            const userMsg = [...messages].reverse().find((m) => m.role === 'user')
            if (userMsg) {
              const textBlock = userMsg.blocks.find(
                (b) => b.kind === 'text',
              ) as { text?: string } | undefined
              if (textBlock?.text) {
                userMsg.domRoot.remove()
                messages.pop()
                inputBar.setInputText(textBlock.text)
              }
            }
          }
        } else {
          // Normal completion: DOM stays as-is (markdown already rendered during streaming).
          const last = messages[messages.length - 1]
          if (last && last.role === 'assistant') {
            last.blocks = pendingBlocks.slice()
            last.status = 'done'
          }
        }

        streaming = false
        inputBar.setStreaming(false)
        queuedMsgs = []
        inputBar.setQueuedMsgs([])
        cleanupStreamingRefs()
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
          currentSubAgentThinking.set(parentID, entry)
          if (domContainer) {
            domContainer.appendChild(entry.p.parentElement!)
          }
          return
        }
        streamAccum.thinking = ''
        const entry = createThinkingEntry()
        pendingBlocks.push(entry.pendingBlock)
        currentThinking = entry
        if (streamContainer) {
          const anchor = progressAnchor()
          if (anchor)
            streamContainer.insertBefore(entry.p.parentElement!, anchor)
          else streamContainer.appendChild(entry.p.parentElement!)
        }
        return
      }
      case 'thinking_delta': {
        if (!e.thinking?.text) return
        if (e.agent) {
          const parentID = e.agent.parent_tool_use_id
          const entry = currentSubAgentThinking.get(parentID)
          if (!entry) {
            const container = pendingChildrenFor(parentID)
            const domContainer = subAgentContainer(parentID)
            if (!container || !domContainer) return
            maybeAutoExpandParent(parentID)
            const newEntry = createThinkingEntry()
            container.push(newEntry.pendingBlock)
            currentSubAgentThinking.set(parentID, newEntry)
            newEntry.pendingBlock.text += e.thinking.text
            writeThinkingText(newEntry.p, newEntry.pendingBlock.text)
            domContainer.appendChild(newEntry.p.parentElement!)
            return
          }
          entry.pendingBlock.text += e.thinking.text
          writeThinkingText(entry.p, entry.pendingBlock.text)
          return
        }
        if (!currentThinking) return
        streamAccum.thinking += e.thinking.text
        currentThinking.pendingBlock.text += e.thinking.text
        writeThinkingText(currentThinking.p, streamAccum.thinking)
        scrollToBottom()
        return
      }
      case 'thinking_end': {
        if (e.agent) {
          const parentID = e.agent.parent_tool_use_id
          const entry = currentSubAgentThinking.get(parentID)
          if (!entry) return
          entry.pendingBlock.active = false
          entry.pendingBlock.durationNs =
            e.thinking?.duration ?? entry.pendingBlock.durationNs
          finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
          currentSubAgentThinking.delete(parentID)
          return
        }
        if (!currentThinking) return
        const entry = currentThinking
        entry.pendingBlock.active = false
        entry.pendingBlock.durationNs =
          e.thinking?.duration ?? entry.pendingBlock.durationNs
        finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
        currentThinking = null
        return
      }
      case 'text_start': {
        if (e.agent) return
        streamAccum.text = ''
        startNewTextBlock()
        return
      }
      case 'text_delta': {
        if (!e.text) return
        if (e.agent) {
          const parentID = e.agent.parent_tool_use_id
          const container = pendingChildrenFor(parentID)
          if (!container) return
          maybeAutoExpandParent(parentID)
          const domContainer = subAgentContainer(parentID)
          const last = container[container.length - 1]
          if (last && last.kind === 'text') {
            ;(last as { text: string }).text += e.text
          } else {
            const newBlock = {
              kind: 'text' as const,
              id: nextId('txt'),
              text: e.text,
            }
            container.push(newBlock)
            if (domContainer) {
              const div = appendTextBlock(domContainer)
              div.textContent = e.text
              currentSubAgentTextDiv.set(parentID, div)
            }
            return
          }
          if (domContainer) {
            let div = currentSubAgentTextDiv.get(parentID)
            if (!div) {
              div = appendTextBlock(domContainer)
              currentSubAgentTextDiv.set(parentID, div)
            }
            div.textContent = (last as { text: string }).text
          }
          return
        }
        streamAccum.text += e.text
        if (!currentTextDiv || !currentPendingText) {
          startNewTextBlock()
        }
        if (currentTextDiv) {
          currentTextDiv.innerHTML = renderMarkdown(streamAccum.text)
          if (currentPendingText)
            currentPendingText.block.text = streamAccum.text
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
          if (!container) return
          maybeAutoExpandParent(parentID)
          const domContainer = subAgentContainer(parentID)
          const block = buildToolBlock(tu)
          container.push(block)
          pendingToolByID.set(tu.id, block)
          if (domContainer) {
            const collapsible = isCollapsibleToolName(tu.name)
            const handles = appendToolBlock(
              domContainer,
              tu.name,
              undefined,
              collapsible,
            )
            toolEntries.set(tu.id, {
              handles,
              startedAt: block.startedAt,
              parentID,
              pendingBlock: block,
            })
          }
          return
        }
        const block = buildToolBlock(tu)
        pendingBlocks.push(block)
        pendingToolByID.set(tu.id, block)
        if (streamContainer) {
          const collapsible = isCollapsibleToolName(tu.name)
          const handles = appendToolBlock(
            streamContainer,
            tu.name,
            progressAnchor(),
            collapsible,
          )
          toolEntries.set(tu.id, {
            handles,
            startedAt: block.startedAt,
            parentID: null,
            pendingBlock: block,
          })
        }
        return
      }
      case 'tool_param_delta': {
        if (!e.partial_input || !e.partial_input.summary) return
        const targetId = e.partial_input.id
        const summary = e.partial_input.summary
        const entry = toolEntries.get(targetId)
        if (entry) setToolSummary(entry.handles, summary)
        const pending = pendingToolByID.get(targetId)
        if (pending) pending.summary = summary
        return
      }
      case 'tool_run': {
        return
      }
      case 'tool_output_delta': {
        if (e.agent && e.tool_result) {
          const targetId = e.tool_result.tool_use_id
          const output = e.tool_result.display_output ?? ''
          const entry = toolEntries.get(targetId)
          if (entry) setToolOutput(entry.handles, output)
          const pending = pendingToolByID.get(targetId)
          if (pending) pending.displayOutput = output
        }
        return
      }
      case 'tool_end': {
        if (!e.tool_result) return
        const tr = e.tool_result
        const entry = toolEntries.get(tr.tool_use_id)
        const durationNs = entry ? (Date.now() - entry.startedAt) * 1e6 : 0
        const output = tr.display_output ?? ''
        const pending = pendingToolByID.get(tr.tool_use_id)
        if (entry) {
          finishTool(entry.handles, {
            isError: !!tr.is_error,
            durationNs,
            output,
          })
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
        if (
          entry &&
          entry.pendingBlock.children.length > 0 &&
          entry.pendingBlock.state !== 'running'
        ) {
          collapseToolChildrenOnDone(entry.handles)
        }
        toolEntries.delete(tr.tool_use_id)
        return
      }
      case 'usage': {
        if (!e.usage_event) return
        if (e.agent) return
        const u = e.usage_event
        progressUsage = {
          inputTokens: u.input_tokens,
          outputTokens: u.output_tokens,
        }
        if (progressHandles) {
          setProgressBarUsage(progressHandles, {
            inputTokens: u.input_tokens,
            outputTokens: u.output_tokens,
            cacheRead: u.cache_read_input_tokens ?? 0,
            cacheCreation: u.cache_creation_input_tokens ?? 0,
          })
        }
        const last = messages[messages.length - 1]
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
        const att = (e as { message?: { attachment?: { prompt?: string; source_uuid?: string } } }).message?.attachment
        if (!att) return
        const text: string = att.prompt ?? ''
        const sourceUUID: string = att.source_uuid ?? ''
        if (!text) return
        if (streaming) {
          pendingBlocks.push({ kind: 'user', id: nextId('u'), text })
          if (streamContainer) {
            appendUserBlock(streamContainer, text, progressAnchor())
          }
        } else {
          const { outer, content } = buildShell('user')
          const span = document.createElement('span')
          span.textContent = text
          content.appendChild(span)
          const m: MessageState = {
            id: nextId('u'),
            role: 'user',
            blocks: [{ kind: 'text', id: nextId('txt'), text }],
            usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
            error: '',
            status: 'done',
            startedAt: Date.now(),
            domRoot: outer,
            contentDiv: content,
          }
          messages.push(m)
          messagesContainer.appendChild(outer)
        }
        if (sourceUUID === '') return
        queuedMsgs = queuedMsgs.filter((m) => m.uuid !== sourceUUID)
        inputBar.setQueuedMsgs(queuedMsgs)
        return
      }
      default:
        return
    }
  }

  const onSend = (text: string) => {
    if (streaming) {
      queuedMsgs = [...queuedMsgs, { uuid: '', text }]
      inputBar.setQueuedMsgs(queuedMsgs)
      conn.send({ type: 'message', text })
      return
    }
    const { outer, content } = buildShell('user')
    const span = document.createElement('span')
    span.textContent = text
    content.appendChild(span)
    const m: MessageState = {
      id: nextId('u'),
      role: 'user',
      blocks: [{ kind: 'text', id: nextId('txt'), text }],
      usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
      error: '',
      status: 'done',
      startedAt: Date.now(),
      domRoot: outer,
      contentDiv: content,
    }
    messages.push(m)
    messagesContainer.appendChild(outer)
    conn.send({ type: 'message', text })
    scrollToBottom()
  }

  const onStop = () => {
    conn.send({ type: 'stop' })
  }

  const onCancelQueued = () => {
    if (queuedMsgs.length === 0) return
    const uuids = queuedMsgs.map((m) => m.uuid).filter((u) => u !== '')
    pendingCancel = queuedMsgs
    if (uuids.length > 0) {
      conn.send({ type: 'cancel_queued', uuids })
    } else {
      const joined = queuedMsgs.map((m) => m.text).join('\n')
      inputBar.appendQueuedText(joined)
      queuedMsgs = []
      inputBar.setQueuedMsgs([])
      pendingCancel = null
    }
  }

  inputBar.onSend(onSend)
  inputBar.onStop(onStop)
  inputBar.onCancelQueued(onCancelQueued)

  // IntersectionObserver: load more when scrolling to top.
  const obs = new IntersectionObserver(
    (entries) => {
      if (
        entries[0].isIntersecting &&
        hasMore &&
        !loadingMore &&
        nextCursor
      ) {
        loadingMore = true
        conn.send({ type: 'history_request', cursor: nextCursor, limit: 10 })
      }
    },
    { root, rootMargin: '400px 0px 0px 0px', threshold: 0 },
  )
  obs.observe(topSentinel)

  let askEls: HTMLElement[] = []

  const unsubscribe = conn.subscribe((msg: ServerMessage) => {
    switch (msg.type) {
      case 'connect_status':
        header.setStatus(msg.connected)
        inputBar.setConnected(msg.connected)
        expectingInitial = true
        nextCursor = ''
        hasMore = false
        loadingMore = false
        return
      case 'queued': {
        const uuid = msg.uuid
        for (let i = 0; i < queuedMsgs.length; i++) {
          if (queuedMsgs[i].uuid === '') {
            queuedMsgs[i] = { ...queuedMsgs[i], uuid }
            inputBar.setQueuedMsgs(queuedMsgs)
            return
          }
        }
        return
      }
      case 'cancel_result': {
        const removed = new Set(msg.removed)
        const snapshot = pendingCancel
        pendingCancel = null
        if (snapshot) {
          const toRestore = snapshot.filter((m) => removed.has(m.uuid))
          if (toRestore.length > 0) {
            const joined = toRestore.map((m) => m.text).join('\n')
            inputBar.appendQueuedText(joined)
          }
        }
        queuedMsgs = []
        inputBar.setQueuedMsgs([])
        return
      }
      case 'history':
        loadHistory(msg)
        return
      case 'error':
        appendError(msg.message)
        return
      case 'ask': {
        const a = createAsk(msg, (decision) => {
          conn.send({ type: 'ask_response', id: msg.id, decision })
          a.close()
          askEls = askEls.filter((el) => el !== a.root)
        })
        messagesContainer.appendChild(a.root)
        askEls.push(a.root)
        return
      }
      case 'event':
        handleEvent(msg.event)
        return
    }
  })

  const cleanup = () => {
    unsubscribe()
    if (refreshInterval !== null) clearInterval(refreshInterval)
    obs.disconnect()
  }

  return { root, scrollEl: root, inputBar, cleanup }
}
