import type { ServerMessage, QueryEvent, HistoryChatMsg } from './types'
import {
  type Block,
  type ChatMessage,
  newAssistantMessage,
  newUserMessage,
} from './model'
import { classifyTool, isCollapsibleBlock, timeDividerLabel } from './utils'
import { createCopyButton } from './utils/copy_button'
import { renderMarkdown } from './markdown'
import { morphHtml } from './morph'

// wireCopyButtons must be called after every innerHTML assignment that may
// contain rendered markdown — innerHTML wipes prior DOM, so the injected
// buttons do not survive a re-render.
function wireCopyButtons(container: HTMLElement) {
  const wrappers = container.querySelectorAll<HTMLElement>('.code-block-wrapper')
  for (const wrapper of wrappers) {
    const header = wrapper.querySelector('.code-header')
    const code = wrapper.querySelector('code')
    if (!header || !code) continue
    if (header.querySelector('.copy-btn')) continue
    const btn = createCopyButton(() => code.textContent ?? '')
    btn.classList.add('ml-auto')
    header.appendChild(btn)
  }
}
import {
  type ToolDomHandles,
  type ProgressDomHandles,
  createUserTextSpan,
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
import { createSidebar, BUILTIN_GAMES } from './sidebar'
import { createInputBar, type InputBarHandles, type AttachmentRef } from './input_bar'
import { createTaskPanel } from './task_panel'
import { createAsk } from './ask'
import { createFloatButton } from './buttons'
import { collectArtifactWrites, createArtifactCard, createArtifactSheet, fetchArtifactList } from './artifact'
import { createSettingsPage } from './settings'
import { getConnection } from './ws'
import { TokenRate } from './token_rate'
import { History } from './history'
import { initTheme } from './theme'
import { sendAttachmentViaWS, attachmentMeta, newAttachmentID } from './upload'
import {
  errorBox,
  compactDividerContainer,
  dividerHairline,
  dividerLabel,
  timeDividerContainer,
  contentArea,
  shellOuter,
  shellGrid,
  shellCenter,
  avatarBase,
  avatarG,
  avatarU,
  disconnectBannerClass,
  disconnectText,
} from './styles/recipes'
import { createElement, createNode, createFragment } from './dom'
import { renderIcon } from './icons'

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

// Lightweight lightbox: clicking any user-message thumbnail overlays a
// full-screen, scrollable, click-to-close view of the original image. No
// zoom/pan gestures — keeps the implementation under 30 lines and avoids
// pulling in a library. The overlay is created lazily on first click and
// reused across clicks.
let lightboxOverlay: HTMLDivElement | null = null
function showImageLightbox(src: string): void {
  if (!lightboxOverlay) {
    const overlay = createElement(
      'div',
      'fixed inset-0 z-50 bg-black/80 flex items-center justify-center cursor-zoom-out p-4',
    )
    overlay.addEventListener('click', () => {
      overlay.style.display = 'none'
    })
    const img = createElement('img', 'max-w-full max-h-full object-contain rounded-lg')
    overlay.appendChild(img)
    document.body.appendChild(overlay)
    lightboxOverlay = overlay
  }
  const img = lightboxOverlay.querySelector('img')!
  img.src = src
  lightboxOverlay.style.display = 'flex'
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
        } else if (b.kind === 'image') {
          // data URL inlined by backend history replay — no /file endpoint needed.
          m.blocks.push({ kind: 'image', id: '', src: b.src })
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
          const srk = classifyTool(t.name)
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

const avatarGStyle = 'background: linear-gradient(to bottom right, #00B4FF, #9D5CFF);color:#FFFFFF'
// User avatar uses literal #FFFFFF instead of text-white: index.css overrides
// --color-white to #333333 in light theme, so Tailwind's text-white would
// render the avatar nearly invisible on light backgrounds.
const renderUserAvatar = (): SVGElement =>
  renderIcon('user', { size: 13, className: 'text-[#FFFFFF]' })

// Shared layout for user and assistant messages. The grid (avatar | content |
// avatar) is identical for both roles — only the avatar position and content
// alignment differ. This ensures both sides stay vertically aligned without
// having to synchronise margin/line-height changes across two code paths.
function buildShell(
  role: 'user' | 'assistant',
): { outer: HTMLElement; content: HTMLDivElement } {
  const outer = createElement('div', shellOuter())
  const grid = createElement('div', shellGrid())

  const leftCol = role === 'assistant'
    ? createNode('div', {
        className: `${avatarBase()} ${avatarG()}`,
        attrs: { style: avatarGStyle },
        text: 'G',
      })
    : createElement('div')
  const centerCol = createElement('div', shellCenter())
  const rightCol = role === 'user'
    ? createElement('div', `${avatarBase()} ${avatarU()}`)
    : createElement('div')

  if (role === 'user') {
    rightCol.replaceChildren(renderUserAvatar())
  }

  const content = createElement('div', contentArea({ role }))

  centerCol.appendChild(content)
  grid.appendChild(leftCol)
  grid.appendChild(centerCol)
  grid.appendChild(rightCol)
  outer.appendChild(grid)
  return { outer, content }
}

// Build committed message DOM by replaying blocks through streamDom appenders.
// Produces the same visual structure streaming builds, so loadHistory output
// is indistinguishable from a message that just finished streaming.
// appendArtifactCards lets history replay derive artifact cards through the
// exact same helper the live query_end path uses.
function renderCommittedMessageDOM(
  m: ChatMessage,
  appendArtifactCards: (blocks: Block[], content: HTMLElement) => void,
): { outer: HTMLElement; content: HTMLDivElement; runningTools: { id: string; handles: ToolDomHandles; block: ToolBlock }[] } {
  const runningTools: { id: string; handles: ToolDomHandles; block: ToolBlock }[] = []
  if (m.role === 'user') {
    const { outer, content } = buildShell('user')
    // Render blocks in order. Image blocks land as <img>; text blocks are
    // parsed for the [Document: ...] prefix emitted by the backend when a
    // document attachment was sent — the prefix becomes a [filename.ext]
    // chip and the rest of the text becomes the text span.
    for (const b of m.blocks) {
      if (b.kind === 'image') {
        const img = createElement('img', 'block max-w-[200px] max-h-[200px] rounded-lg my-1 cursor-zoom-in')
        img.src = b.src
        img.addEventListener('click', () => showImageLightbox(b.src))
        content.appendChild(img)
      } else if (b.kind === 'text' || b.kind === 'user') {
        const text = (b as { text: string }).text
        const docMatch = text.match(/^\[Document: (.+?) saved at .+?\]\n?/)
        if (docMatch) {
          const chip = createElement('span', 'font-mono text-[12px] bg-ink2 text-t2 rounded-md px-2 py-1 mr-1')
          chip.textContent = `[${docMatch[1]}]`
          content.appendChild(chip)
          const rest = text.slice(docMatch[0].length)
          if (rest) {
            content.appendChild(createUserTextSpan(rest))
          }
        } else {
          content.appendChild(createUserTextSpan(text))
        }
      }
    }
    if (m.error) {
      content.appendChild(createNode('div', { className: errorBox(), text: m.error }))
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
      const handles = appendToolBlock(content, b.name, undefined, isCollapsibleBlock(b))
      if (b.summary) setToolSummary(handles, b.summary, b.name)
      if (b.state === 'running') {
        runningTools.push({ id: b.id, handles, block: b })
      } else {
        finishTool(handles, {
          isError: b.state === 'error',
          durationNs: b.timingNs,
          output: b.displayOutput,
        })
      }
    } else if (b.kind === 'text') {
      if (!b.text) continue
      const div = appendTextBlock(content)
      div.innerHTML = renderMarkdown(b.text)
      wireCopyButtons(div)
    } else if (b.kind === 'image') {
      // Assistant image blocks should not occur in normal flow (assistant is
      // text-only), but render defensively in case a future tool emits one.
      const img = createElement('img', 'block max-w-[400px] max-h-[400px] rounded-lg my-1')
      img.src = b.src
      content.appendChild(img)
    } else if (b.kind === 'user') {
      if (!b.text) continue
      appendUserBlock(content, b.text)
    }
  }

  if (m.error) {
    content.appendChild(createNode('div', { className: errorBox(), text: m.error }))
  }
  appendArtifactCards(m.blocks, content)
  return { outer, content, runningTools }
}

// buildCompactDivider returns the "compact" rule that visually separates
// pre-compact (older) and post-compact (newer) messages. Container uses
// flex with two flex-1 hairlines and a centered label so it scales to the
// available width. Class names mirror design tokens already used elsewhere
// in chat.ts (border-hairline, text-t3).
function buildCompactDivider(): HTMLElement {
  const container = createElement('div', compactDividerContainer())
  const left = createElement('div', dividerHairline())
  const label = createNode('span', { className: dividerLabel(), text: 'Compact' })
  const right = createElement('div', dividerHairline())
  container.append(left, label, right)
  return container
}

// buildTimeDivider renders a bare centered time label — no hairlines — so
// it stays visually quieter than buildCompactDivider (which marks a major
// session break). The label text is the sole content; tests distinguish
// time dividers from compact dividers by textContent ("Compact" vs not).
function buildTimeDivider(label: string): HTMLElement {
  const container = createElement('div', timeDividerContainer())
  const labelEl = createNode('span', { className: dividerLabel(), text: label })
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
  const root = createElement('div', 'relative flex flex-col h-dvh')

  const mainContent = createElement(
    'div',
    'relative overflow-hidden transition-transform duration-300 ease-out h-full',
  )

  const scroll = createElement(
    'div',
    'flex-1 min-h-0 overflow-y-auto overflow-x-hidden pb-20 h-full',
  )

  // Theme engine FIRST: page-load side effects (data-theme, hljs, the
  // __gbotApplySystemTheme hook the Android host calls immediately) must
  // be live before anything renders.
  initTheme()
  const sidebar = createSidebar({ mainContent })

  const header = createHeader({
    onModelSelect: (provider, model) => conn.send({ type: 'model_switch', provider, model }),
    onEngineSwitch: (engineID) => conn.send({ type: 'engine_switch', engineID }),
    onEngineNew: () => conn.send({ type: 'engine_new' }),
    onContextRequest: () => conn.send({ type: 'context_request' }),
    onContextCompact: () => {
      header.hideContextBreakdown()
      conn.send({ type: 'compact_request' })
    },
    onRequestQuota: () => conn.send({ type: 'quota_request' }),
  })
  header.setStatus(initial.connected)
  header.onHamburgerClick(() => sidebar.toggle())
  scroll.appendChild(header.root)

  // ── Disconnect banner: slides in below header when WS is down.
  const disconnectBanner = createElement('div', disconnectBannerClass())

  const dcText = createElement('span', disconnectText())
  disconnectBanner.appendChild(dcText)
  mainContent.appendChild(disconnectBanner)

  const wrapper = createElement('div', 'mx-auto max-w-2xl py-4')

  const topSentinel = createElement('div', 'h-px')
  const messagesContainer = createElement('div', 'space-y-7')
  ;(messagesContainer.style as unknown as Record<string, string>).overflowAnchor = 'none'

  const bottomSentinel = createElement('div', 'h-px')
  ;(bottomSentinel.style as unknown as Record<string, string>).overflowAnchor = 'auto'

  wrapper.appendChild(topSentinel)
  wrapper.appendChild(messagesContainer)
  wrapper.appendChild(bottomSentinel)
  scroll.appendChild(wrapper)
  mainContent.appendChild(scroll)

  const inputBar = createInputBar({ connected: initial.connected })
  inputBar.root.className = 'px-5 pb-3 pt-1'
  inputBar.onThinkingSelect((effort) => conn.send({ type: 'thinking_switch', effort }))

  const taskPanel = createTaskPanel()

  const inputWrapper = createElement('div', 'absolute bottom-0 inset-x-0 z-10')
  inputWrapper.appendChild(inputBar.bubbles)
  inputWrapper.appendChild(inputBar.root)
  mainContent.appendChild(inputWrapper)
  mainContent.appendChild(taskPanel.root)

  const setStreaming = (s: boolean) => {
    streaming = s
    header.setStreaming(s)
    inputBar.setStreaming(s)
    sidebar.setStreaming(s)
  }

  root.appendChild(mainContent)

  root.appendChild(sidebar.root)
  root.appendChild(sidebar.overlay)

  // Persistent top sheet: survives resetAllState by design (closing it is a
  // purely manual action via its handle).
  const artifactSheet = createArtifactSheet()
  root.appendChild(artifactSheet.root)

  // Settings page: full-screen overlay above everything (z-60). Closing the
  // sidebar first keeps the gear's tap from leaving both layers open.
  const settingsPage = createSettingsPage()
  root.appendChild(settingsPage.root)
  sidebar.onOpenSettings(() => {
    sidebar.closeImmediate()
    settingsPage.open()
  })

  // Live path and history replay share this derivation — replay has no
  // separate artifact logic.
  const appendArtifactCards = (blocks: Block[], content: HTMLElement) => {
    for (const w of collectArtifactWrites(blocks)) {
      content.appendChild(createArtifactCard(w, (name) => artifactSheet.open(name)))
    }
  }

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

  // No global click delegation: copy buttons are wired per-wrapper via
  // wireCopyButtons() after each renderMarkdown call.

  let isNearBottom = true
  let lastScrollHeight = 0

  // Scroll-to-bottom floating button — blue glow + circular progress ring.
  const scrollBtnHandle = createFloatButton({
    position: 'center',
    progressRing: { progressClassName: 'scroll-progress', backgroundClassName: 'scroll-ring' },
    innerIcon: 'scroll-to-bottom',
    onClick: () => {
      isNearBottom = true
      lastScrollHeight = 0
      scroll.scrollTo({ top: scroll.scrollHeight, behavior: 'smooth' })
      updateScrollBtn()
    },
  })
  const scrollBtn = scrollBtnHandle.root
  mainContent.appendChild(scrollBtn)

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
      scrollBtnHandle.setProgress(progress)
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
    setStreaming(false)
    console.debug('[chat] resetAllState')
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
    const temp = createElement('div')
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
      wireCopyButtons(currentTextDiv)
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
      wireCopyButtons(currentTextDiv)
    } else if (currentTextDiv && currentPendingText) {
      morphHtml(currentTextDiv, renderMarkdown(currentPendingText.block.text))
      wireCopyButtons(currentTextDiv)
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
      last.contentDiv.appendChild(createNode('div', { className: errorBox(), text }))
    } else {
      const { outer, content } = buildShell('assistant')
      content.appendChild(createNode('div', { className: errorBox(), text }))
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
    setStreaming(false)
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
    const frag = createFragment()
    // loadHistory prepends OLDER messages. The single cursor lastUserAt
    // already tracks the previous user message regardless of direction —
    // loadHistory and streaming share it via abs-time-delta rule.

    let batch: HistoryChatMsg[] = []
    const flushBatch = () => {
      if (batch.length === 0) return
      const chats = mapHistoryToChatMessages(batch)
      for (const chat of chats) {
        const { outer, content, runningTools } = renderCommittedMessageDOM(chat, appendArtifactCards)
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
          setStreaming(true)
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

    for (const h of msg.messages ?? []) {
      if (h.compactBoundary === true) {
        flushBatch()
        frag.appendChild(buildCompactDivider())
        continue
      }
      batch.push(h)
    }
    flushBatch()

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
    setStreaming(true)
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
    if (block.kind === 'user') {
      if (!block.text) return
      appendUserBlock(parent, block.text, before)
    } else if (block.kind === 'text') {
      if (!block.text) return
      const div = appendTextBlock(parent, before)
      div.innerHTML = renderMarkdown(block.text)
      wireCopyButtons(div)
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
      const handles = appendToolBlock(parent, block.name, before, isCollapsibleBlock(block))
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
              appendArtifactCards(last.blocks, last.contentDiv)
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
            appendArtifactCards(last.blocks, last.contentDiv)
          }
        }

        // Open sheet reloads on every query end — no per-file tracking, the
        // serve route is no-store so the re-fetch always yields fresh bytes.
        // Games are exempt: their page is stateful and a reload would abort
        // an in-flight turn POST mid-think.
        if (artifactSheet.isOpen() && !BUILTIN_GAMES.some((g) => g.id === artifactSheet.current())) {
          artifactSheet.reload()
        }

        setStreaming(false)
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
              wireCopyButtons(div)
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
            morphHtml(div, renderMarkdown((last as { text: string }).text))
            wireCopyButtons(div)
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
            morphHtml(currentTextDiv, renderMarkdown(currentPendingText.block.text))
      wireCopyButtons(currentTextDiv)
          } else if (streamContainer) {
            // Late delta: sink not yet mounted. Mount inline now.
            const anchor = progressHandles?.root ?? null
            currentTextDiv = appendTextBlock(streamContainer, anchor)
            currentTextDiv.innerHTML = renderMarkdown(currentPendingText.block.text)
      wireCopyButtons(currentTextDiv)
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
            const handles = appendToolBlock(
              domContainer,
              tu.name,
              undefined,
              isCollapsibleBlock(block),
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
          const handles = appendToolBlock(
            streamContainer,
            tu.name,
            progressAnchor(),
            isCollapsibleBlock(block),
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
        if (isCollapsibleBlock(ruEntry.pendingBlock)) {
          markToolCollapsible(ruEntry.handles.root)
        }
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
          content.appendChild(createUserTextSpan(text))
          const m: MessageState = {
            ...newUserMessage(text),
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

  // renderUserMessage builds a user message DOM node carrying text + any
  // uploaded attachments (image thumbnails via blob URL or document chips)
  // and appends it to the messages container. Mirrors the pre-attachment
  // send path so users see their message immediately, before the WS round-trip.
  // uploaded is narrowed to non-paste kinds: paste refs are filtered out of
  // `files` in onSend and inlined into `text`, so they never reach here.
  const renderUserMessage = (
    text: string,
    uploaded: Exclude<AttachmentRef, { kind: 'paste' }>[],
  ) => {
    const { outer, content } = buildShell('user')
    // Populate the blocks array so abort-rewind / replay can recover the
    // text without parsing DOM. Image refs become image blocks (the DOM
    // <img> src stays the blob URL — same data URL after backend history
    // replay, but for live send we use the blob URL until page unload).
    const blocks: Block[] = []
    if (text) {
      content.appendChild(createUserTextSpan(text))
      blocks.push({ kind: 'text', id: '', text })
    }
    for (const ref of uploaded) {
      if (ref.kind === 'image') {
        // Blob URL is still alive (clearAttachments used keepSentBlobURLs).
        // The rendered DOM owns it now; browser GCs on page unload.
        const img = createElement('img', 'block max-w-[200px] max-h-[200px] rounded-lg my-1 cursor-zoom-in')
        img.src = ref.previewURL
        img.alt = ref.file.name
        img.addEventListener('click', () => showImageLightbox(ref.previewURL))
        content.appendChild(img)
        blocks.push({ kind: 'image', id: '', src: ref.previewURL })
      } else {
        const span = createNode('span', {
          className: 'font-mono text-[12px] bg-ink2 text-t2 rounded-md px-2 py-1 mr-1',
          text: `[${ref.file.name}]`,
        })
        content.appendChild(span)
      }
    }
    const m: MessageState = {
      ...newUserMessage('', blocks),
      lastActivityAt: Date.now(),
      domRoot: outer,
      contentDiv: content,
    }
    appendMsgWithDivider(messagesContainer, m.role, m.startedAt, outer)
    messages.push(m)
  }

  const onSend = async (text: string) => {
    const all = inputBar.getAttachments()
    const pastes = all.filter((r): r is Extract<AttachmentRef, { kind: 'paste' }> => r.kind === 'paste')
    const files = all.filter((r) => r.kind !== 'paste')

    // Inline paste text into the message body. Each paste is separated from
    // the preceding content by a blank line so the LLM can tell where the
    // user's typed message ends and the pasted block begins.
    let fullText = text
    for (const p of pastes) {
      fullText = fullText === '' ? p.text : fullText + '\n\n' + p.text
    }

    if (files.length === 0) {
      if (fullText.trim() === '' && pastes.length === 0) return
      inputHistory.add(fullText)
      if (streaming) {
        queuedMsgs = [...queuedMsgs, { uuid: '', text: fullText }]
        inputBar.setQueuedMsgs(queuedMsgs)
        conn.send({ type: 'message', text: fullText })
        inputBar.removeAttachments(all)
        return
      }
      renderUserMessage(fullText, [])
      conn.send({ type: 'message', text: fullText })
      inputBar.removeAttachments(all)
      return
    }

    // Two-phase commit path — upload files only (paste already inlined).
    // Paste MUST be filtered out before this loop: paste refs have no `file`,
    // so sendAttachmentViaWS would crash dereferencing ref.file.
    inputBar.setUploading(true)
    let anyFailed = false
    for (const ref of files) {
      if (ref.uploadedID) continue // bytes already staged server-side (retry path)
      try {
        const id = newAttachmentID()
        await sendAttachmentViaWS(ref.file, id, (frac) =>
          inputBar.setAttachmentProgress(ref, frac))
        ref.uploadedID = id
      } catch {
        anyFailed = true
        inputBar.markAttachmentFailures([ref])
        break // stop on first failure — server only accepts one upload at a time
      }
    }
    inputBar.setUploading(false)
    if (anyFailed) {
      // Restore the user's TYPED text (not fullText) — paste chips stay in
      // the strip so a retry re-inlines them. Matches existing restore semantics.
      inputBar.setInputText(text)
      return
    }
    // inputHistory.add AFTER upload succeeded but BEFORE commit send — that
    // way a failed upload does NOT pollute history with a duplicate entry
    // when the user retries.
    inputHistory.add(fullText)
    conn.send({
      type: 'message',
      text: fullText,
      attachments: files.map((ref) =>
        attachmentMeta(ref.file, ref.uploadedID!)),
    })
    inputBar.removeAttachments(all)
    if (streaming) {
      queuedMsgs = [...queuedMsgs, { uuid: '', text: fullText }]
      inputBar.setQueuedMsgs(queuedMsgs)
      return
    }
    renderUserMessage(fullText, files)
  }

  const onStop = () => {
    const t0 = performance.now()
    console.debug('[chat] onStop esc pressed at', t0)
    conn.send({ type: 'stop' })
    console.debug('[chat] onStop stop sent, ws_buffered=' + (performance.now() - t0) + 'ms')
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
  // onRetryAttachment re-fires sendAttachmentViaWS for a failed chip with a
  // fresh id. On success the ref's uploadedID is set so a subsequent onSend
  // skips re-uploading it (only the failed chip needed bytes on the wire).
  // Paste refs never reach here at runtime: the retry button only renders
  // on `ref.failed === true` chips, and paste is filtered out of the upload
  // loop in onSend before any failure can be marked.
  inputBar.onRetryAttachment(async (ref) => {
    if (ref.kind === 'paste') return false
    const id = newAttachmentID()
    try {
      await sendAttachmentViaWS(ref.file, id, (frac) =>
        inputBar.setAttachmentProgress(ref, frac))
      ref.uploadedID = id
      return true
    } catch {
      return false
    }
  })
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
  sidebar.onArtifactClick((name) => {
    artifactSheet.open(name)
  })
  // The artifacts directory is the source of truth — refresh the list each
  // time the sidebar opens instead of tracking writes as state.
  sidebar.onOpen(() => {
    fetchArtifactList()
      .then((items) => sidebar.setArtifacts(items))
      .catch(() => {})
  })

  let askEls: HTMLElement[] = []

  // Single pending-file slot for the chunked binary receive path. Set by
  // file_start, accumulated by binary frames, finalized by file_end. Sound
  // only because the SERVER serializes concurrent SendFile calls
  // (sendFileMu) so frame sequences never interleave on the wire.
  let pendingFile: { name: string; mime: string; chunks: ArrayBuffer[] } | null = null

  const unsubscribeBinary = conn.subscribeBinary((data: ArrayBuffer) => {
    if (pendingFile) pendingFile.chunks.push(data)
  })

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
        inputBar.setThinking(msg.config.thinking ?? 'auto')
        if (msg.tasks) taskPanel.setTasks(msg.tasks.tasks)
        loadHistory({ type: 'history', ...msg.history })

        if (msg.snapshot && msg.snapshot.blocks.length > 0) {
          if (!streamContainer) {
            initStreaming('snapshot')
          }
          renderStreamStateBlocks(msg.snapshot.blocks)
        }

        if (msg.queuedMsgs && msg.queuedMsgs.length > 0) {
          queuedMsgs = msg.queuedMsgs.map((m: { uuid: string; text: string }) => ({ uuid: m.uuid, text: m.text }))
          inputBar.setQueuedMsgs(queuedMsgs)
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
        inputBar.setThinking(msg.thinking ?? 'auto')
        return
      case 'model_switched':
        contextTotal = msg.contextTotal
        header.setContext(msg.contextUsed, msg.contextTotal)
        inputBar.setThinking(msg.thinking ?? 'auto')
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
        const a = createAsk(msg, (payload) => {
          conn.send({ type: 'ask_response', id: msg.id, ...payload })
          a.close()
          askEls = askEls.filter((el) => el !== a.root)
        })
        messagesContainer.appendChild(a.root)
        askEls.push(a.root)
        return
      }
      case 'file_start': {
        // Begin chunked binary receive. Binary frames accumulate into
        // pendingFile until file_end reassembles the Blob.
        pendingFile = { name: msg.name, mime: msg.mime, chunks: [] }
        return
      }
      case 'file_end': {
        if (!pendingFile) return
        const blob = new Blob(pendingFile.chunks, { type: pendingFile.mime })

        // Auto-download via transient anchor (same as the old base64 path).
        const dlUrl = URL.createObjectURL(blob)
        const dl = document.createElement('a')
        dl.href = dlUrl
        dl.download = pendingFile.name
        dl.style.display = 'none'
        document.body.appendChild(dl)
        dl.click()
        setTimeout(() => { dl.remove(); URL.revokeObjectURL(dlUrl) }, 60_000)

        const { outer, content } = buildShell('assistant')
        if (pendingFile.mime.startsWith('image/')) {
          // Image: inline thumbnail. A SEPARATE object URL from the same
          // Blob — NOT revoked — so the 60s revoke of dlUrl above cannot
          // blank the preview. Reclaimed by the browser on page unload.
          const imgUrl = URL.createObjectURL(blob)
          const img = createElement('img', 'block max-w-full rounded-lg my-1')
          img.src = imgUrl
          img.alt = pendingFile.name
          content.appendChild(img)
        } else {
          // Non-image: file icon + name (file is already auto-downloaded).
          const row = createElement('div', 'inline-flex items-center gap-1.5 text-t2 my-1')
          const icon = renderIcon('file', { size: 16 })
          if (icon) row.appendChild(icon)
          const name = createElement('span', 'break-all')
          name.textContent = pendingFile.name
          row.appendChild(name)
          content.appendChild(row)
        }
        appendMsgWithDivider(messagesContainer, 'assistant', Date.now(), outer)
        pendingFile = null
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
    unsubscribeBinary()
    if (refreshInterval !== null) clearInterval(refreshInterval)
  }

  return { root, scrollEl: scroll, inputBar, cleanup }
}
