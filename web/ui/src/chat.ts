import type { ServerMessage, QueryEvent, HistoryChatMsg } from './types'
import {
  type Block,
  type ChatMessage,
  newAssistantMessage,
} from './model'
import { isCollapsibleToolName, timeDividerLabel } from './utils'
import { renderMarkdown, renderMarkdownNoHighlight } from './markdown'
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
  finalizeProgressBar,
  refreshToolDuration,
  refreshThinkingLabel,
  writeThinkingText,
  finishThinking,
  finishTool,
  expandToolChildrenForRunning,
  collapseToolChildrenOnDone,
  markToolCollapsible,
} from './components/stream_dom'
import { createHeader } from './header'
import { createSidebar } from './sidebar'
import { createInputBar, type InputBarHandles } from './input_bar'
import { createTaskPanel } from './task_panel'
import { createAsk } from './ask'
import { getConnection } from './ws'
import { TokenRate } from './token_rate'
import { History } from './history'

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
  // Client-only: when the last activity on this message happened. For user
  // messages this equals startedAt (send time). For assistant messages it
  // starts at initStreaming and is updated to Date.now() on each completion
  // (query_end / appendError first-branch) so the next user-send gap is
  // measured from when the assistant finished, not when it started.
  lastActivityAt: number
  domRoot: HTMLElement
  contentDiv: HTMLDivElement
}

export interface ChatHandles {
  root: HTMLElement
  scrollEl: HTMLElement
  inputBar: InputBarHandles
  cleanup: () => void
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

export function mapHistoryToChatMessages(histMsgs: HistoryChatMsg[]): ChatMessage[] {
  const merged: HistoryChatMsg[] = []
  for (const h of histMsgs) {
    if (h.role === 'user' && (!h.text || h.text.trim() === '')) continue
    if (h.role === 'system') continue  // markers intercepted in loadHistory; defense-in-depth
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
    if (h.role === 'system') continue  // narrows h.role to 'user' | 'assistant' for the assignment below
    const m: ChatMessage = {
      id: h.id || '',
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
          m.blocks.push({ kind: 'text', id: '', text: b.text })
        } else if (b.kind === 'thinking') {
          const th = b.thinking!
          m.blocks.push({
            kind: 'thinking',
            id: '',
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
            isSearch: srk.isSearch || !!t.is_search,
            isRead: srk.isRead || !!t.is_read,
            isList: srk.isList || !!t.is_list,
            isLsp: srk.isLsp || !!t.is_lsp,
            isWeb: srk.isWeb,
            state: (t.isError ? 'error' : t.isRunning ? 'running' : 'done') as 'error' | 'running' | 'done',
            timingNs: t.durationNs ?? 0,
            displayOutput: t.displayOutput ?? '',
            startedAt: 0,
            children: [],
          })
        }
      }
    } else {
      if (h.text) {
        m.blocks.push({ kind: 'text', id: '', text: h.text })
      }
    }
    result.push(m)
  }
  return result
}

const avatarSizeClass = 'flex h-5 w-5 shrink-0 items-center justify-center rounded-md'
const avatarGExtra = 'text-[11px] font-bold text-white'
const avatarGStyle = 'background: linear-gradient(to bottom right, #00B4FF, #9D5CFF);'
const avatarUExtra = 'bg-gradient-to-br from-t2 to-t3'
const userAvatarSVG =
  '<svg width="13" height="13" viewBox="0 0 24 24" fill="none" stroke="white" stroke-width="2"><circle cx="12" cy="8" r="4" /><path d="M4 21v-1a8 8 0 0116 0v1" /></svg>'

// Shared layout for user and assistant messages. The grid (avatar | content |
// avatar) is identical for both roles — only the avatar position and content
// alignment differ. This ensures both sides stay vertically aligned without
// having to synchronise margin/line-height changes across two code paths.
const shellGridClass = 'grid grid-cols-[1.25rem_1fr_1.25rem] items-start gap-x-1.5'

function buildShell(
  role: 'user' | 'assistant',
): { outer: HTMLElement; content: HTMLDivElement } {
  const outer = document.createElement('div')
  outer.className = 'px-1.5'
  const grid = document.createElement('div')
  grid.className = shellGridClass

  const leftCol = document.createElement('div')
  const centerCol = document.createElement('div')
  centerCol.className = 'min-w-0'
  const rightCol = document.createElement('div')

  if (role === 'assistant') {
    leftCol.className = `${avatarSizeClass} ${avatarGExtra}`
    leftCol.setAttribute('style', avatarGStyle)
    leftCol.textContent = 'G'
  } else {
    rightCol.className = `${avatarSizeClass} ${avatarUExtra}`
    rightCol.innerHTML = userAvatarSVG
  }

  const content = document.createElement('div')
  content.className = role === 'assistant' ? 'space-y-3' : 'ml-auto w-fit text-left text-t1 text-[15px] whitespace-pre-wrap'

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
): { outer: HTMLElement; content: HTMLDivElement; runningTools: { id: string; handles: ToolDomHandles; block: ToolBlock }[] } {
  const runningTools: { id: string; handles: ToolDomHandles; block: ToolBlock }[] = []
  if (m.role === 'user') {
    const { outer, content } = buildShell('user')
    const text = m.blocks
      .filter((b) => b.kind === 'text' || b.kind === 'user')
      .map((b) => (b as { text: string }).text)
      .join('')
    const span = document.createElement('span')
    span.className = 'whitespace-pre-wrap'
    span.textContent = text
    content.appendChild(span)
    if (m.error) {
      const err = document.createElement('div')
      err.className =
        'rounded-lg border border-red/40 bg-red/5 px-3 py-2 text-sm text-red'
      err.textContent = m.error
      content.appendChild(err)
    }
    return { outer, content, runningTools }
  }

  const { outer, content } = buildShell('assistant')

  for (const b of m.blocks) {
    if (b.kind === 'thinking') {
      const { p, labelEl } = appendThinkingBlock(content, 0)
      if (b.text) writeThinkingText(p, b.text)
      finishThinking(p, labelEl, b.durationNs)
    } else if (b.kind === 'tool') {
      const collapsible = isCollapsibleToolName(b.name) || isCollapsibleToolBlock(b)
      const handles = appendToolBlock(content, b.name, undefined, collapsible)
      if (b.summary) setToolSummary(handles, b.summary, b.name)
      if (b.state === 'running') {
        runningTools.push({ id: b.id, handles, block: b })
      } else {
        finishTool(handles, {
          isError: b.state === 'error',
          durationNs: b.timingNs,
          output: b.displayOutput,
          skipHighlight: true,
        })
      }
    } else if (b.kind === 'text') {
      if (!b.text) continue
      const div = appendTextBlock(content)
      div.innerHTML = renderMarkdownNoHighlight(b.text)
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
  return { outer, content, runningTools }
}

// buildCompactDivider returns the "compact" rule that visually separates
// pre-compact (older) and post-compact (newer) messages. Container uses
// flex with two flex-1 hairlines and a centered label so it scales to the
// available width. Class names mirror design tokens already used elsewhere
// in chat.ts (border-hairline, text-t3).
function buildCompactDivider(): HTMLElement {
  const container = document.createElement('div')
  container.className = 'flex items-center gap-2 my-4 px-4'
  const left = document.createElement('div')
  left.className = 'flex-1 border-t border-hairline'
  const label = document.createElement('span')
  label.className = 'text-blue text-[10px] shrink-0'
  label.textContent = 'Compact'
  const right = document.createElement('div')
  right.className = 'flex-1 border-t border-hairline'
  container.append(left, label, right)
  return container
}

// buildTimeDivider renders a bare centered time label — no hairlines — so
// it stays visually quieter than buildCompactDivider (which marks a major
// session break). The label text is the sole content; tests distinguish
// time dividers from compact dividers by textContent ("Compact" vs not).
function buildTimeDivider(label: string): HTMLElement {
  const container = document.createElement('div')
  container.className = 'flex justify-center items-center my-4 px-4'
  const labelEl = document.createElement('span')
  labelEl.className = 'text-blue text-[10px] shrink-0'
  labelEl.textContent = label
  container.appendChild(labelEl)
  return container
}

export function createChat(initial: { connected: boolean }): ChatHandles {
  // ── Module-level state (persists across createChat calls in the same
  // session, mirroring persistedMessages in ChatInterface.tsx).
  const messages: MessageState[] = []
  const inputHistory = new History()
  let nextCursor = ''
  let hasMore = false
  let loadingMore = false

  // ── Streaming refs (cleared on query_end).
  let streamContainer: HTMLDivElement | null = null
  let streamStartedAt = 0
  let committedToolCount = 0  // restored from connect_status, tracks all known tool IDs
  const knownToolIDs = new Set<string>()
  let streaming = false
  const toolEntries = new Map<string, ToolEntry>()
  let currentTextDiv: HTMLDivElement | null = null
  let currentPendingText: { block: { kind: 'text'; id: string; text: string } } | null = null
  let currentThinking: ThinkingEntry | null = null
  let accumulatedThinkingMs = 0  // restored from connect_status, incremented by thinking_end
  let contextTotal = 0
  const tokenRate = new TokenRate()
  let progressHandles: ProgressDomHandles | null = null
  let progressUsage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
  let refreshInterval: number | null = null
  const pendingBlocks: Block[] = []
  const pendingToolByID = new Map<string, ToolBlock>()
  const currentSubAgentTextDiv = new Map<string, HTMLDivElement>()
  const currentSubAgentThinking = new Map<string, ThinkingEntry>()
  let pendingCancel: { uuid: string; text: string }[] | null = null
  let queuedMsgs: { uuid: string; text: string }[] = []

  // ── Shell DOM: relative root, sidebar + mainContent, scroll fills viewport.
  const root = document.createElement('div')
  root.className = 'relative flex flex-col h-dvh'

  const mainContent = document.createElement('div')
  mainContent.className = 'relative overflow-hidden transition-transform duration-300 ease-out h-full'

  const scroll = document.createElement('div')
  scroll.className = 'flex-1 min-h-0 overflow-y-auto overflow-x-hidden pb-20 h-full'

  const sidebar = createSidebar({ mainContent })

  const header = createHeader({
    onModelSelect: (provider, model) => conn.send({ type: 'model_switch', provider, model }),
    onEngineSwitch: (engineID) => conn.send({ type: 'engine_switch', engineID }),
    onEngineNew: () => conn.send({ type: 'engine_new' }),
    onContextRequest: () => conn.send({ type: 'context_request' }),
    onRequestQuota: () => conn.send({ type: 'quota_request' }),
  })
  header.setStatus(initial.connected)
  header.onHamburgerClick(() => sidebar.toggle())
  scroll.appendChild(header.root)

  // ── Disconnect banner: slides in below header when WS is down.
  const disconnectBanner = document.createElement('div')
  disconnectBanner.className =
    'absolute top-11 inset-x-0 z-50 ' +
    'card-bg border-b border-hairline ' +
    'px-4 py-1.5 flex items-center ' +
    'transition-all duration-300 overflow-hidden max-h-0 opacity-0'

  const dcText = document.createElement('span')
  dcText.className = 'text-[12px] text-red'
  disconnectBanner.appendChild(dcText)
  mainContent.appendChild(disconnectBanner)

  const wrapper = document.createElement('div')
  wrapper.className = 'mx-auto max-w-2xl py-4'

  const topSentinel = document.createElement('div')
  topSentinel.className = 'h-px'
  const messagesContainer = document.createElement('div')
  messagesContainer.className = 'space-y-7'
  ;(messagesContainer.style as unknown as Record<string, string>).overflowAnchor = 'none'

  const bottomSentinel = document.createElement('div')
  bottomSentinel.className = 'h-px'
  ;(bottomSentinel.style as unknown as Record<string, string>).overflowAnchor = 'auto'

  wrapper.appendChild(topSentinel)
  wrapper.appendChild(messagesContainer)
  wrapper.appendChild(bottomSentinel)
  scroll.appendChild(wrapper)
  mainContent.appendChild(scroll)

  const inputBar = createInputBar({ connected: initial.connected })
  inputBar.root.className = 'px-5 pb-3 pt-1'

  const taskPanel = createTaskPanel()

  const inputWrapper = document.createElement('div')
  inputWrapper.className = 'absolute bottom-0 inset-x-0 z-10'
  inputWrapper.appendChild(inputBar.bubbles)
  inputWrapper.appendChild(inputBar.root)
  mainContent.appendChild(inputWrapper)
  mainContent.appendChild(taskPanel.root)

  root.appendChild(mainContent)

  root.appendChild(sidebar.root)
  root.appendChild(sidebar.overlay)

  const conn = getConnection()

  let dcDotTimer: ReturnType<typeof setTimeout> | null = null
  const stopDots = () => {
    if (dcDotTimer) { clearTimeout(dcDotTimer); dcDotTimer = null }
  }
  const startDots = () => {
    let dots = 1
    dcText.textContent = 'Connection lost, reconnecting.'
    const tick = () => {
      dcDotTimer = null
      dots = (dots + 1) % 4
      dcText.textContent = 'Connection lost, reconnecting' + '.'.repeat(dots)
      dcDotTimer = setTimeout(tick, 1000)
    }
    dcDotTimer = setTimeout(tick, 1000)
  }
  conn.onStateChange?.((cs) => {
    stopDots()
    if (cs === 'connected') {
      disconnectBanner.style.maxHeight = '0px'
      disconnectBanner.style.opacity = '0'
      disconnectBanner.style.cursor = 'default'
    } else if (cs === 'reconnecting') {
      disconnectBanner.style.cursor = 'default'
      startDots()
      disconnectBanner.style.maxHeight = '40px'
      disconnectBanner.style.opacity = '1'
    } else {
      dcText.textContent = 'Reconnection failed. Tap to retry.'
      disconnectBanner.style.cursor = 'pointer'
      disconnectBanner.style.maxHeight = '40px'
      disconnectBanner.style.opacity = '1'
    }
  })

  disconnectBanner.addEventListener('click', () => {
    conn.reconnect?.()
  })

  let isNearBottom = true
  let lastScrollHeight = 0

  // Scroll-to-bottom floating button — blue glow + circular progress ring.
  const scrollBtn = document.createElement('button')
  scrollBtn.className =
    'absolute bottom-24 left-1/2 -translate-x-1/2 z-50 flex h-11 w-11 items-center justify-center rounded-full bg-transparent opacity-0 pointer-events-none transition-all duration-200 text-blue'
  // SVG: outer ring (progress) + inner arrow
  scrollBtn.innerHTML =
    '<svg width="44" height="44" viewBox="0 0 44 44">' +
    '<circle class="scroll-ring" cx="22" cy="22" r="18" fill="none" stroke="currentColor" stroke-width="2"/>' +
    '<circle class="scroll-progress" cx="22" cy="22" r="18" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-dasharray="113.1" stroke-dashoffset="113.1" transform="rotate(-90 22 22)" style="transition:stroke-dashoffset 0.15s ease-out"/>' +
    '<path d="M22 14v10M17 20l5 5 5-5" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"/>' +
    '</svg>'
  mainContent.appendChild(scrollBtn)
  scrollBtn.addEventListener('click', () => {
    isNearBottom = true
    lastScrollHeight = 0
    scroll.scrollTo({ top: scroll.scrollHeight, behavior: 'smooth' })
    updateScrollBtn()
  })

  const scrollArc = scrollBtn.querySelector('.scroll-progress') as SVGCircleElement
  const circumference = 2 * Math.PI * 18 // r=18

  const updateScrollBtn = () => {
    const maxScroll = scroll.scrollHeight - scroll.clientHeight
    const distFromBottom = maxScroll - scroll.scrollTop
    const near = distFromBottom < 120
    if (near !== isNearBottom) isNearBottom = near
    scrollBtn.classList.toggle('opacity-0', isNearBottom)
    scrollBtn.classList.toggle('pointer-events-none', isNearBottom)
    // Progress ring: 0 at bottom, full at top
    if (maxScroll > 0) {
      const progress = Math.min(distFromBottom / maxScroll, 1)
      scrollArc.setAttribute('stroke-dashoffset', String(circumference * (1 - progress)))
    }
    maybePrefetchHistory()
  }

  // Prefetch when fewer than REMAINING_THRESHOLD messages are above the viewport.
  const REMAINING_THRESHOLD = 10
  function maybePrefetchHistory() {
    if (!hasMore || loadingMore || !nextCursor) return
    const scrollTop = scroll.scrollTop
    // Count messages above the viewport top. In jsdom offsetTop/offsetHeight
    // are always 0, so aboveCount=0 — which correctly triggers prefetch when
    // there are few messages (scrollTop=0 means at top).
    let aboveCount = 0
    for (const child of messagesContainer.children) {
      const el = child as HTMLElement
      if (el.offsetTop + el.offsetHeight >= scrollTop) break
      aboveCount++
    }
    if (aboveCount < REMAINING_THRESHOLD) {
      loadingMore = true
      conn.send({ type: 'history_request', cursor: nextCursor, limit: 30 })
    }
  }

  scroll.addEventListener('scroll', updateScrollBtn, { passive: true })
  const scrollToBottom = () => {
    if (!isNearBottom) return
    const sh = scroll.scrollHeight
    if (sh === lastScrollHeight) return
    lastScrollHeight = sh
    scroll.scrollTop = sh
  }

  // Single observer: any DOM mutation in the messages container triggers
  // autoscroll and scroll-button update (progress ring follows streaming).
  const scrollObserver = new MutationObserver(() => {
    scrollToBottom()
    updateScrollBtn()
  })
  scrollObserver.observe(messagesContainer, { childList: true, subtree: true, characterData: true })

  const progressAnchor = (): Node | null => progressHandles?.root ?? null

  const cleanupStreamingRefs = () => {
    if (refreshInterval !== null) {
      clearInterval(refreshInterval)
      refreshInterval = null
    }
    streamContainer = null
    toolEntries.clear()
    currentTextDiv = null
    currentThinking = null
    tokenRate.reset()
    progressHandles = null
    pendingBlocks.length = 0
    pendingToolByID.clear()
    currentPendingText = null
    currentSubAgentTextDiv.clear()
    currentSubAgentThinking.clear()
  }

  const resetProgressUsage = () => {
    progressUsage = { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }
    committedToolCount = 0
    accumulatedThinkingMs = 0
    streamStartedAt = 0
  }

  const resetAllState = () => {
    cleanupStreamingRefs()
    resetProgressUsage()
    streaming = false
    console.debug('[chat] resetAllState')
    inputBar.setStreaming(false)
    queuedMsgs = []
    pendingCancel = null
    inputBar.setQueuedMsgs([])
    for (const m of messages) m.domRoot.remove()
    messages.length = 0
    // Dividers are not tracked in messages[] (they aren't message roots) —
    // replaceChildren after detaching tracked roots clears any stragglers.
    messagesContainer.replaceChildren()
    lastUserAt = null
    lastDivAt = null
    nextCursor = ''
    hasMore = false
    loadingMore = false
    isNearBottom = true
    lastScrollHeight = 0
    inputHistory.load([])
  }

  function finalizeRunningBlocks(blocks: Block[], aborted = false) {
    for (const b of blocks) {
      if (b.kind === 'tool') {
        if (b.state === 'running') {
          b.state = aborted ? 'error' : 'done'
          if (!b.timingNs) b.timingNs = (Date.now() - b.startedAt) * 1e6
          if (aborted && toolEntries.get(b.id)) {
            const entry = toolEntries.get(b.id)!
            finishTool(entry.handles, {
              isError: true,
              durationNs: b.timingNs,
              output: '',
            })
          }
        }
        if (b.children && b.children.length > 0) finalizeRunningBlocks(b.children, aborted)
      }
    }
  }

  function buildToolBlock(tu: {
    id: string
    name: string
    summary?: string
    is_search?: boolean
    is_read?: boolean
    is_list?: boolean
    is_lsp?: boolean
  }): ToolBlock {
    return {
      kind: 'tool',
      id: tu.id,
      name: tu.name,
      summary: tu.summary ?? '',
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

  function updateAgentToolName(parentID: string, agentType: string) {
    if (!agentType || agentType === 'fork') return
    const parent = pendingToolByID.get(parentID)
    if (!parent || parent.name.includes(agentType)) return
    parent.name = parent.name + ' ' + agentType
    const entry = toolEntries.get(parentID)
    if (entry) {
      const root = entry.handles.root as HTMLElement
      root.dataset.toolName = parent.name
      const nameEl = root.querySelector('.font-mono.text-blue')
      if (nameEl) nameEl.textContent = parent.name
    }
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
      id: '',
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
      id: '',
      text: '',
    }
    pendingBlocks.push(block)
    currentPendingText = { block }
    if (streamContainer) {
      currentTextDiv = appendTextBlock(streamContainer, progressAnchor())
      currentTextDiv.innerHTML = renderMarkdown('')
    }
  }

  function setupStreaming() {
    if (!streamContainer) return
    // Late text deltas (text_delta before streamContainer mounted) wrote
    // directly into currentPendingText.block.text. Mount the DOM sink and
    // re-derive innerHTML from block.text so nothing is lost.
    if (currentPendingText && !currentTextDiv) {
      currentTextDiv = appendTextBlock(streamContainer, progressAnchor())
      currentTextDiv.innerHTML = renderMarkdown(currentPendingText.block.text)
    } else if (currentTextDiv && currentPendingText) {
      currentTextDiv.innerHTML = renderMarkdown(currentPendingText.block.text)
    }
    // Late thinking deltas wrote into currentThinking.pendingBlock.text.
    // thinking_start only attaches the <p> when streamContainer is non-null;
    // if it arrived earlier the entry existed but its <p> was never mounted.
    // Mount it now so the reasoning text is visible.
    if (currentThinking) {
      const anchor = progressAnchor()
      if (anchor) streamContainer.insertBefore(currentThinking.p.parentElement!, anchor)
      else streamContainer.appendChild(currentThinking.p.parentElement!)
      writeThinkingText(currentThinking.p, currentThinking.pendingBlock.text)
    }
    if (!progressHandles) {
      progressHandles = appendProgressBar(streamContainer)
    }
    setProgressBarUsage(progressHandles, progressUsage)
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
        const r = tokenRate.rate()
        refreshProgressBar(
          progressHandles,
          streamStartedAt,
          committedToolCount,
          progressUsage.outputTokens,
        )
        progressHandles.rateEl.textContent = r > 0 ? r.toFixed(1) + ' t/s' : '0.0 t/s'
      }
    }, 200)
  }

  function appendError(text: string) {
    const last = messages[messages.length - 1]
    if (last && last.role === 'assistant' && last.status === 'streaming') {
      last.error = text
      last.status = 'done'
      last.lastActivityAt = Date.now()
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
        id: '',
        role: 'assistant',
        blocks: [],
        usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
        error: text,
        status: 'done',
        startedAt: Date.now(),
        lastActivityAt: Date.now(),
        domRoot: outer,
        contentDiv: content,
      }
      appendMsgWithDivider(messagesContainer, m.role, m.startedAt, outer)
      messages.push(m)
    }
    streaming = false
    inputBar.setStreaming(false)
    queuedMsgs = []
    inputBar.setQueuedMsgs([])
    cleanupStreamingRefs()
  }

  // Last user message's startedAt — the cursor for deciding whether to
  // insert a time divider before the NEXT user message. Only user turns
  // update this; assistant replies don't, so divider gaps measure
  // user-to-user (burst boundaries) instead of wall-clock adjacency.
  // Reset to null by resetAllState.
  // Two cursors, one timeline:
  //   lastUserAt  — previous user message (for first-message anchor check)
  //   lastDivAt   — previous divider (for the 15min wall-clock guarantee)
  // A divider fires before a user message when EITHER (a) lastUserAt is
  // null (first message anchor) OR (b) the wall-clock gap from lastDivAt
  // crosses 15min. Rule (b) guarantees a divider every 15min even when
  // user queries arrive closer together (e.g., three queries 8min apart
  // over 16min span → two dividers, not one). Assistant replies never
  // touch either cursor — they belong to the same burst.
  let lastUserAt: number | null = null
  let lastDivAt: number | null = null

  // appendMsgWithDivider inserts a time divider before `dom` when role is
  // 'user' AND (lastUserAt is null OR |startedAt - lastDivAt| >= 15min).
  // Absolute value so loadHistory (older) and streaming (newer) share the
  // same cursors. Assistant replies never trigger a divider.
  function appendMsgWithDivider(parent: Node, role: 'user' | 'assistant', startedAt: number, dom: HTMLElement) {
    if (role === 'user') {
      const shouldInsert = lastUserAt === null ||
        (lastDivAt !== null && Math.abs(startedAt - lastDivAt) >= 15 * 60 * 1000)
      if (shouldInsert) {
        // Label uses lastDivAt (not lastUserAt) so the cross-day / time-only
        // decision matches the 15min wall-clock rule that triggered the insert.
        const label = timeDividerLabel(lastDivAt, startedAt)
        if (label) {
          parent.appendChild(buildTimeDivider(label))
          lastDivAt = startedAt
        }
      }
      lastUserAt = startedAt
    }
    parent.appendChild(dom)
  }

  function loadHistory(msg: Extract<ServerMessage, { type: 'history' }>) {
    const prevScrollHeight = scroll.scrollHeight
    const prevScrollTop = scroll.scrollTop
    const before = messagesContainer.firstChild
    const wasEmpty = messages.length === 0
    const frag = document.createDocumentFragment()
    // loadHistory prepends OLDER messages. The single cursor lastUserAt
    // already tracks the previous user message regardless of direction —
    // loadHistory and streaming share it via abs-time-delta rule.

    let batch: HistoryChatMsg[] = []
    const flushBatch = () => {
      if (batch.length === 0) return
      const chats = mapHistoryToChatMessages(batch)
      for (const chat of chats) {
        const { outer, content, runningTools } = renderCommittedMessageDOM(chat)
        for (const rt of runningTools) {
          toolEntries.set(rt.id, {
            handles: rt.handles,
            startedAt: Date.now(),
            parentID: null,
            pendingBlock: rt.block,
          })
          pendingToolByID.set(rt.id, rt.block)
          knownToolIDs.add(rt.id)
        }
        if (runningTools.length > 0 && !streaming) {
          streaming = true
          inputBar.setStreaming(true)
          streamContainer = content
          if (streamStartedAt === 0) {
            streamStartedAt = Date.now()
          }
          setupStreaming()
          console.debug('[chat] initStreaming reason=loadHistory_runningTool streamContainer=' + !!streamContainer + ' progressHandles=' + !!progressHandles)
        }
        appendMsgWithDivider(frag, chat.role, chat.startedAt, outer)
        const m: MessageState = {
          id: chat.id,
          role: chat.role,
          blocks: chat.blocks,
          usage: chat.usage,
          error: chat.error,
          status: chat.status,
          startedAt: chat.startedAt,
          lastActivityAt: chat.startedAt,
          domRoot: outer,
          contentDiv: content,
        }
        messages.unshift(m)
      }
      batch = []
    }

    for (const h of msg.messages) {
      if (h.compactBoundary === true) {
        flushBatch()
        frag.appendChild(buildCompactDivider())
        continue
      }
      batch.push(h)
    }
    flushBatch()

    // Envelope-level compactBoundary (in-memory → precompact transition).
    // Rendered at END of frag so the divider sits between the in-memory
    // messages and whatever the client loads next.
    if (msg.compactBoundary === true) {
      frag.appendChild(buildCompactDivider())
    }

    messagesContainer.insertBefore(frag, before)
    // On first load, lastUserAt was advanced through the page and ended at
    // the newest user message (page is ASC). That's exactly what streaming
    // needs for the next user-send comparison — nothing to restore.
    nextCursor = msg.nextCursor
    hasMore = msg.hasMore
    loadingMore = false
    maybePrefetchHistory()
    if (wasEmpty) {
      scroll.scrollTop = scroll.scrollHeight
      isNearBottom = true
      lastScrollHeight = scroll.scrollHeight
      scrollBtn.classList.add('opacity-0', 'pointer-events-none')
    } else {
      requestAnimationFrame(() => {
        const delta = scroll.scrollHeight - prevScrollHeight
        scroll.scrollTop = prevScrollTop + delta
      })
    }
  }

  function initStreaming(reason: string) {
    const { outer, content } = buildShell('assistant')
    const m: MessageState = {
      ...newAssistantMessage(''),
      lastActivityAt: Date.now(),
      domRoot: outer,
      contentDiv: content,
    }
    appendMsgWithDivider(messagesContainer, m.role, m.startedAt, outer)
    messages.push(m)
    streamContainer = content
    if (streamStartedAt === 0) {
      streamStartedAt = Date.now()
    }
    streaming = true
    inputBar.setStreaming(true)
    setupStreaming()
    console.debug('[chat] initStreaming reason=' + reason + ' streamContainer=' + !!streamContainer + ' progressHandles=' + !!progressHandles)
  }

  function registerBlocksForStreamState(blocks: Block[], _parentID: string | null) {
    for (const b of blocks) {
      if (b.kind === 'tool') {
        pendingToolByID.set(b.id, b)
        knownToolIDs.add(b.id)
        if (b.children && b.children.length > 0) {
          registerBlocksForStreamState(b.children, b.id)
        }
      }
    }
  }

  function renderStreamBlock(
    parent: HTMLElement,
    block: Block,
    before: Node | null,
    parentID: string | null,
  ) {
    if (block.kind === 'text') {
      if (!block.text) return
      const div = appendTextBlock(parent, before)
      div.innerHTML = renderMarkdown(block.text)
    } else if (block.kind === 'thinking') {
      const startedAt = block.startedAt || Date.now()
      const { p, labelEl } = appendThinkingBlock(parent, startedAt, before)
      if (block.text) writeThinkingText(p, block.text)
      if (block.active) {
        const entry: ThinkingEntry = { p, labelEl, startedAt, pendingBlock: block }
        if (parentID) {
          currentSubAgentThinking.set(parentID, entry)
        } else {
          currentThinking = entry
        }
      } else {
        finishThinking(p, labelEl, block.durationNs)
      }
    } else if (block.kind === 'tool') {
      const collapsible = isCollapsibleToolName(block.name) || isCollapsibleToolBlock(block)
      const handles = appendToolBlock(parent, block.name, before, collapsible)
      if (block.summary) setToolSummary(handles, block.summary, block.name)
      if (block.state === 'running') {
        toolEntries.set(block.id, {
          handles,
          startedAt: block.startedAt || Date.now(),
          parentID,
          pendingBlock: block,
        })
        if (block.children && block.children.length > 0) {
          expandToolChildrenForRunning(handles)
        }
      } else {
        finishTool(handles, {
          isError: block.state === 'error',
          durationNs: block.timingNs,
          output: block.displayOutput,
          skipHighlight: true,
        })
      }
      if (block.children && block.children.length > 0) {
        const childContainer = appendToolChildrenContainer(handles)
        for (const child of block.children) {
          renderStreamBlock(childContainer, child, null, block.id)
        }
      }
    }
  }

  function renderStreamStateBlocks(blocks: Block[]) {
    if (!streamContainer) return
    pendingBlocks.length = 0
    pendingBlocks.push(...blocks)
    registerBlocksForStreamState(blocks, null)
    const anchor = progressAnchor()
    for (const b of blocks) {
      renderStreamBlock(streamContainer, b, anchor, null)
    }
  }

  function handleEvent(e: QueryEvent) {
    switch (e.type) {
      case 'query_start': {
        if (e.agent) return
        cleanupStreamingRefs()
        initStreaming('query_start')
        return
      }
      case 'turn_start': {
        if (e.agent) {
          updateAgentToolName(e.agent.parent_tool_use_id, e.agent.agent_type)
          return
        }
        if (streaming) return
        cleanupStreamingRefs()
        initStreaming('turn_start')
        return
      }
      case 'query_end': {
        if (e.agent) return
        const wasAborted = !!e.aborted
        console.debug('[chat] query_end aborted=' + wasAborted)
        finalizeRunningBlocks(pendingBlocks, wasAborted)

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
              last.lastActivityAt = Date.now()
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
            last.lastActivityAt = Date.now()
          }
        }

        streaming = false
        inputBar.setStreaming(false)
        queuedMsgs = []
        inputBar.setQueuedMsgs([])
        // Finalize progress bar to static stats before cleanup nulls its refs.
        if (progressHandles) {
          const finalUsage = e.usage_event
            ? {
                inputTokens: e.usage_event.input_tokens,
                outputTokens: e.usage_event.output_tokens,
                cacheRead: e.usage_event.cache_read_input_tokens ?? 0,
                cacheCreation: e.usage_event.cache_creation_input_tokens ?? 0,
              }
            : progressUsage
          const streamMs = tokenRate.streamDurationMs()
          finalizeProgressBar(progressHandles, finalUsage,
            streamMs > 0 ? streamMs : (Date.now() - streamStartedAt),
            committedToolCount, accumulatedThinkingMs)
        }
        cleanupStreamingRefs()
        resetProgressUsage()
        knownToolIDs.clear()
        if (isNearBottom) {
          scroll.scrollTop = scroll.scrollHeight
        }
        console.debug('[chat] query_end aborted=' + wasAborted)
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
            tokenRate.add(e.thinking.text)
            writeThinkingText(newEntry.p, newEntry.pendingBlock.text)
            domContainer.appendChild(newEntry.p.parentElement!)
            return
          }
          entry.pendingBlock.text += e.thinking.text
          tokenRate.add(e.thinking.text)
          writeThinkingText(entry.p, entry.pendingBlock.text)
          return
        }
        if (!currentThinking) return
        currentThinking.pendingBlock.text += e.thinking.text
        tokenRate.add(e.thinking.text)
        writeThinkingText(currentThinking.p, currentThinking.pendingBlock.text)
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
          accumulatedThinkingMs += Math.round(entry.pendingBlock.durationNs / 1e6)
          finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
          currentSubAgentThinking.delete(parentID)
          return
        }
        if (!currentThinking) return
        const entry = currentThinking
        entry.pendingBlock.active = false
        entry.pendingBlock.durationNs =
          e.thinking?.duration ?? entry.pendingBlock.durationNs
        accumulatedThinkingMs += Math.round(entry.pendingBlock.durationNs / 1e6)
        finishThinking(entry.p, entry.labelEl, entry.pendingBlock.durationNs)
        currentThinking = null
        return
      }
      case 'text_start': {
        if (e.agent) return
        startNewTextBlock()
        return
      }
      case 'text_delta': {
        if (!e.text) return
        if (e.agent) {
          tokenRate.add(e.text)
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
              id: '',
              text: e.text,
            }
            container.push(newBlock)
            if (domContainer) {
              const div = appendTextBlock(domContainer)
              div.innerHTML = renderMarkdown(e.text)
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
            div.innerHTML = renderMarkdown((last as { text: string }).text)
          }
          return
        }
        if (!currentPendingText) {
          startNewTextBlock()
        }
        if (currentPendingText) {
          currentPendingText.block.text += e.text
          tokenRate.add(e.text)
          if (currentTextDiv) {
            currentTextDiv.innerHTML = renderMarkdown(currentPendingText.block.text)
          } else if (streamContainer) {
            // Late delta: sink not yet mounted. Mount inline now.
            const anchor = progressHandles?.root ?? null
            currentTextDiv = appendTextBlock(streamContainer, anchor)
            currentTextDiv.innerHTML = renderMarkdown(currentPendingText.block.text)
          }
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
          committedToolCount++
          const domContainer = subAgentContainer(parentID)
          const block = buildToolBlock(tu)
          container.push(block)
          pendingToolByID.set(tu.id, block)
          if (domContainer) {
            const collapsible = isCollapsibleToolName(tu.name) || !!tu.is_search
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
        if (!streaming) {
          initStreaming('tool_start_fallback')
        }
        if (knownToolIDs.has(tu.id)) return
        knownToolIDs.add(tu.id)
        committedToolCount++
        currentPendingText = null
        currentTextDiv = null
        const block = buildToolBlock(tu)
        pendingBlocks.push(block)
        pendingToolByID.set(tu.id, block)
        if (streamContainer) {
          const collapsible = isCollapsibleToolName(tu.name) || !!tu.is_search
          const handles = appendToolBlock(
            streamContainer,
            tu.name,
            progressAnchor(),
            collapsible,
          )
          if (tu.summary) {
            setToolSummary(handles, tu.summary, tu.name)
          }
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
        if (e.partial_input?.delta) tokenRate.add(e.partial_input.delta)
        if (!e.partial_input || !e.partial_input.summary) return
        const targetId = e.partial_input.id
        const summary = e.partial_input.summary
        const entry = toolEntries.get(targetId)
        if (entry) setToolSummary(entry.handles, summary, entry.pendingBlock.name)
        const pending = pendingToolByID.get(targetId)
        if (pending) {
          pending.summary = summary
          if (e.partial_input.is_search !== undefined) pending.isSearch = e.partial_input.is_search
          if (e.partial_input.is_read !== undefined) pending.isRead = e.partial_input.is_read
          if (e.partial_input.is_list !== undefined) pending.isList = e.partial_input.is_list
          if (e.partial_input.is_lsp !== undefined) pending.isLsp = e.partial_input.is_lsp
        }
        return
      }
      case 'tool_run': {
        if (!e.tool_use) return
        const ruEntry = toolEntries.get(e.tool_use.id)
        if (!ruEntry) return
        // tool_start fires with empty input, so is_search is false.
        // Now that tool_param_delta has populated the block flags,
        // retroactively mark collapsible and trigger grouping.
        const srk = classifyToolName(e.tool_use.name)
        const shouldCollapse = srk.isSearch || ruEntry.pendingBlock.isSearch
        if (shouldCollapse) markToolCollapsible(ruEntry.handles.root)
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
          entry.pendingBlock.children && entry.pendingBlock.children.length > 0 &&
          entry.pendingBlock.state !== 'running'
        ) {
          collapseToolChildrenOnDone(entry.handles)
        }
        toolEntries.delete(tr.tool_use_id)
        return
      }
      case 'usage': {
        if (!e.usage_event) return
        const u = e.usage_event
        progressUsage.inputTokens += u.input_tokens
        progressUsage.outputTokens += u.output_tokens
        progressUsage.cacheRead += u.cache_read_input_tokens ?? 0
        progressUsage.cacheCreation += u.cache_creation_input_tokens ?? 0
        // contextUsed = this turn's total tokens in context (not cumulative)
        const ctxUsed = u.input_tokens + (u.cache_read_input_tokens ?? 0) + (u.cache_creation_input_tokens ?? 0) + u.output_tokens
        header.setContext(ctxUsed, contextTotal)
        if (progressHandles) {
          setProgressBarUsage(progressHandles, progressUsage)
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
          pendingBlocks.push({ kind: 'user', id: '', text })
          if (streamContainer) {
            appendUserBlock(streamContainer, text, progressAnchor())
          }
        } else {
          const { outer, content } = buildShell('user')
          const span = document.createElement('span')
          span.className = 'whitespace-pre-wrap'
          span.textContent = text
          content.appendChild(span)
          const m: MessageState = {
            id: '',
            role: 'user',
            blocks: [{ kind: 'text', id: '', text }],
            usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
            error: '',
            status: 'done',
            startedAt: Date.now(),
            lastActivityAt: Date.now(),
            domRoot: outer,
            contentDiv: content,
          }
          appendMsgWithDivider(messagesContainer, m.role, m.startedAt, outer)
          messages.push(m)
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
    inputHistory.add(text)
    if (streaming) {
      queuedMsgs = [...queuedMsgs, { uuid: '', text }]
      inputBar.setQueuedMsgs(queuedMsgs)
      conn.send({ type: 'message', text })
      return
    }
    const { outer, content } = buildShell('user')
    const span = document.createElement('span')
    span.className = 'whitespace-pre-wrap'
    span.textContent = text
    content.appendChild(span)
    const m: MessageState = {
      id: '',
      role: 'user',
      blocks: [{ kind: 'text', id: '', text }],
      usage: { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 },
      error: '',
      status: 'done',
      startedAt: Date.now(),
      lastActivityAt: Date.now(),
      domRoot: outer,
      contentDiv: content,
    }
    appendMsgWithDivider(messagesContainer, m.role, m.startedAt, outer)
    messages.push(m)
    conn.send({ type: 'message', text })
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
  inputBar.onHistoryUp((current) => {
    const r = inputHistory.up(current)
    return r.cursor === 'none' ? null : r.text
  })
  inputBar.onHistoryDown(() => {
    const r = inputHistory.down()
    return r.cursor === 'none' ? null : r.text
  })
  inputBar.onHistoryReset(() => {
    inputHistory.resetNav()
  })
  inputBar.onHistoryPicker(() => inputHistory.getAll())

  let currentSessionID = ''

  sidebar.onSessionClick((id) => {
    conn.send({ type: 'session_switch', sessionID: id })
  })
  sidebar.onNewSession(() => {
    conn.send({ type: 'session_new' })
  })
  sidebar.onRename((id, title) => {
    conn.send({ type: 'session_rename', sessionID: id, title })
  })

  let askEls: HTMLElement[] = []

  const unsubscribe = conn.subscribe((msg: ServerMessage) => {
    switch (msg.type) {
      case 'metadata': {
        const c = msg.connect
        sidebar.closeImmediate()
        header.setStatus(c.connected)
        header.setModel(c.model ?? '')
        header.hideContextBreakdown()
        inputBar.setConnected(c.connected)
        resetAllState()
        if (c.inputHistory) inputHistory.load(c.inputHistory)
        taskPanel.setTasks([])
        scrollBtn.classList.add('opacity-0', 'pointer-events-none')
        if (c.sessionID) currentSessionID = c.sessionID
        conn.send({ type: 'session_list_request' })

        header.setModels(msg.config.models, msg.config.current.provider, msg.config.current.model)
        header.setEngines(msg.engines.engines, msg.engines.activeID)
        if (msg.tasks) taskPanel.setTasks(msg.tasks.tasks)
        loadHistory({ type: 'history', ...msg.history })

        if (msg.snapshot && msg.snapshot.blocks.length > 0) {
          if (!streamContainer) {
            initStreaming('snapshot')
          }
          renderStreamStateBlocks(msg.snapshot.blocks)
        }

        const s = msg.stats
        progressUsage = {
          inputTokens: s.usage?.input_tokens ?? s.usage?.InputTokens ?? 0,
          outputTokens: s.usage?.output_tokens ?? s.usage?.OutputTokens ?? 0,
          cacheRead: s.usage?.cache_read_input_tokens ?? s.usage?.CacheReadInputTokens ?? 0,
          cacheCreation: s.usage?.cache_creation_input_tokens ?? s.usage?.CacheCreationInputTokens ?? 0,
        }
        if (s.queryStartMs) streamStartedAt = s.queryStartMs
        committedToolCount = s.toolCount ?? 0
        accumulatedThinkingMs = s.thinkingMs ?? 0
        header.setContext(s.contextUsed ?? 0, s.contextTotal ?? 0)
        contextTotal = s.contextTotal ?? 0
        if (progressHandles) {
          setProgressBarUsage(progressHandles, progressUsage)
        }
        return
      }
      case 'streamState': {
        if (msg.blocks.length > 0) {
          if (!streamContainer) {
            initStreaming('streamState')
          }
          renderStreamStateBlocks(msg.blocks)
        }
        return
      }
      case 'connect_status':
        sidebar.closeImmediate()
        header.setStatus(msg.connected)
        header.setModel(msg.model ?? '')
        header.hideContextBreakdown()
        inputBar.setConnected(msg.connected)
        resetAllState()
        if (msg.inputHistory) inputHistory.load(msg.inputHistory)
        taskPanel.setTasks([])
        scrollBtn.classList.add('opacity-0', 'pointer-events-none')
        if (msg.sessionID) currentSessionID = msg.sessionID
        conn.send({ type: 'session_list_request' })
        return
      case 'stats':
        progressUsage = {
          inputTokens: msg.usage?.input_tokens ?? msg.usage?.InputTokens ?? 0,
          outputTokens: msg.usage?.output_tokens ?? msg.usage?.OutputTokens ?? 0,
          cacheRead: msg.usage?.cache_read_input_tokens ?? msg.usage?.CacheReadInputTokens ?? 0,
          cacheCreation: msg.usage?.cache_creation_input_tokens ?? msg.usage?.CacheCreationInputTokens ?? 0,
        }
        if (msg.queryStartMs) {
          streamStartedAt = msg.queryStartMs
        }
        committedToolCount = msg.toolCount ?? 0
        accumulatedThinkingMs = msg.thinkingMs ?? 0
        header.setContext(msg.contextUsed ?? 0, msg.contextTotal ?? 0)
        contextTotal = msg.contextTotal ?? 0
        if (progressHandles) {
          setProgressBarUsage(progressHandles, progressUsage)
        }
        return
      case 'config':
        header.setModels(msg.models, msg.current.provider, msg.current.model)
        return
      case 'model_switched':
        contextTotal = msg.contextTotal
        header.setContext(msg.contextUsed, msg.contextTotal)
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
      case 'task_list':
        taskPanel.setTasks(msg.tasks)
        return
      case 'engine_list':
        header.setEngines(msg.engines, msg.activeID)
        return
      case 'session_list':
        sidebar.setSessions(msg.sessions, currentSessionID)
        return
      case 'context_breakdown':
        if (msg.totalTokens === 0) {
          header.setContextBreakdown(null)
        } else {
          header.setContextBreakdown(msg)
        }
        return
      case 'quota_result':
        for (const entry of msg.entries) {
          header.setQuota(entry.provider, entry.quota)
        }
        return
    }
  })

  const cleanup = () => {
    unsubscribe()
    if (refreshInterval !== null) clearInterval(refreshInterval)
  }

  return { root, scrollEl: scroll, inputBar, cleanup }
}
