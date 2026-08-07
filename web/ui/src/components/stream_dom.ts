// Pure DOM mutation module for the streaming container. ChatInterface owns
// all writes here during streaming; React never touches the container's
// children until query_end swaps StreamingMessage out for MessageComponent.
//
// Every append function takes an optional `before` anchor so ChatInterface
// can pin the progress bar to the bottom of the streaming container by
// passing `progressHandles.current?.root ?? null` as the anchor.

import { formatDurationNs, formatTokenCount, summarize } from '../utils'
import { renderToolOutput } from '../tool_render'
import { renderMarkdown } from '../markdown'
import { morphHtml } from '../morph'
import hljs from 'highlight.js'
import {
  toolHeaderBtn,
  toolPrefix,
  toolHeaderContent,
  runningDot,
  chevron,
  thinkingGlyph,
  thinkingLabel,
  textBlock,
  userEchoBlock,
  userTextSpan,
  thinkingText,
  toolName,
  toolSummary,
  toolDuration,
  toolBody,
  toolChildren,
  groupSummary,
  groupDuration,
  groupToolsContainer,
  progressBar,
} from '../styles/recipes'
import { createElement, createNode } from '../dom'

export function createUserTextSpan(text: string): HTMLSpanElement {
  return createNode('span', { className: userTextSpan(), text })
}

interface ToolHeaderHandles {
  header: HTMLSpanElement
  dot: HTMLSpanElement
  chevron: HTMLSpanElement
  content: HTMLSpanElement
}

function createToolHeader(): ToolHeaderHandles {
  const header = createNode('span', {
    className: toolHeaderBtn(),
    attrs: { role: 'button' },
    props: { tabIndex: 0 },
  })

  const prefix = createElement('span', toolPrefix())

  const dot = createElement('span', runningDot({ color: 'white' }))
  dot.textContent = '●'
  prefix.appendChild(dot)

  const chevronEl = createElement('span')
  chevronEl.innerHTML = `<svg class="${chevron({ expanded: false })}" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>`
  prefix.appendChild(chevronEl)
  header.appendChild(prefix)

  const content = createElement('span', toolHeaderContent())
  header.appendChild(content)

  return { header, dot, chevron: chevronEl, content }
}

function shouldAutoExpand(toolName: string): boolean {
  return toolName === 'Edit' || toolName === 'Write'
}

export interface ToolDomHandles {
  root: HTMLDivElement
  header: HTMLSpanElement
  dot: HTMLSpanElement
  summaryEl: HTMLSpanElement
  durEl: HTMLSpanElement
  body: HTMLDivElement
  childrenContainer: HTMLDivElement
}

export interface ProgressDomHandles {
  root: HTMLDivElement
  elapsedEl: HTMLSpanElement
  inEl: HTMLSpanElement
  outEl: HTMLSpanElement
  rateEl: HTMLSpanElement
  toolCountEl: HTMLSpanElement
  dotEl: HTMLSpanElement
  cacheEl: HTMLSpanElement
  thinkingEl: HTMLSpanElement
  tokensSuffix: HTMLSpanElement
}

function insertBefore(parent: HTMLElement, child: Node, before: Node | null) {
  if (before) parent.insertBefore(child, before)
  else parent.appendChild(child)
}

// Walk backward through siblings, skipping thinking blocks (wrap divs with
// data-thinking). Matches renderGrouped in MessageComponent where thinking
// does NOT break a group of consecutive collapsible tools.
function findPrevToolSibling(before: Node | null, parent: HTMLElement): HTMLElement | null {
  let el: HTMLElement | null = before
    ? ((before as HTMLElement).previousElementSibling as HTMLElement | null)
    : (parent.lastElementChild as HTMLElement | null)
  while (el && el.dataset.thinking) {
    el = el.previousElementSibling as HTMLElement | null
  }
  return el
}

// Collects consecutive data-thinking element siblings immediately preceding
// `el`, walking backward. Returns oldest-first (document order) so callers
// can appendChild in array order to reproduce the visual sequence inside a
// group. Read-only; does not detach nodes.
function collectLeadingThinking(el: HTMLElement | null): HTMLElement[] {
  if (!el) return []
  const out: HTMLElement[] = []
  let node = el.previousElementSibling as HTMLElement | null
  while (node && node.dataset.thinking) {
    out.push(node)
    node = node.previousElementSibling as HTMLElement | null
  }
  return out.reverse()
}

// Collects consecutive data-thinking element siblings immediately following
// `el`, walking forward. Returns oldest-first (document order). Used to
// absorb inter-tool thinking that sits between a group and the next tool.
// The progress bar (when present) is not a thinking node, so it terminates
// the walk naturally.
function collectTrailingThinking(el: HTMLElement | null): HTMLElement[] {
  if (!el) return []
  const out: HTMLElement[] = []
  let node = el.nextElementSibling as HTMLElement | null
  while (node && node.dataset.thinking) {
    out.push(node)
    node = node.nextElementSibling as HTMLElement | null
  }
  return out
}

export function appendTextBlock(parent: HTMLElement, before?: Node | null): HTMLDivElement {
  const div = createElement('div', textBlock())
  insertBefore(parent, div, before ?? null)
  return div
}

export function appendUserBlock(parent: HTMLElement, text: string, before?: Node | null): HTMLDivElement {
  // Streaming echo visual (small italic indented) lives on the div; the wrap
  // classes (whitespace-pre-wrap break-words) live on the inner span via
  // createUserTextSpan so all user-text paths share the same source of truth.
  const div = createElement('div', userEchoBlock())
  div.appendChild(createUserTextSpan(text))
  insertBefore(parent, div, before ?? null)
  return div
}

export function appendThinkingBlock(
  parent: HTMLElement,
  startedAt: number,
  before?: Node | null,
): { p: HTMLParagraphElement; labelEl: HTMLSpanElement } {
  const wrap = createElement('div')
  wrap.dataset.thinking = '1'

  const header = createNode('span', {
    className: toolHeaderBtn(),
    attrs: { role: 'button' },
    props: { tabIndex: 0 },
  })

  const prefix = createElement('span', toolPrefix())

  const glyph = createElement('span', thinkingGlyph())
  glyph.textContent = '✦'
  prefix.appendChild(glyph)

  const chevronEl = createElement('span')
  chevronEl.innerHTML = `<svg class="${chevron({ expanded: true })}" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>`
  prefix.appendChild(chevronEl)
  header.appendChild(prefix)

  const labelEl = createElement('span', thinkingLabel())
  labelEl.textContent = `Thinking (${formatDurationNs(0)})`
  header.appendChild(labelEl)

  wrap.appendChild(header)

  // <p> always mounted (CSS hidden when collapsed) so the sink stays live
  // for streaming writes even when collapsed.
  const p = createElement('p', thinkingText())
  wrap.appendChild(p)

  insertBefore(parent, wrap, before ?? null)

  // Auto-expand on creation (matches Thinking.tsx active → expanded).
  const toggleThinking = () => {
    const collapsed = p.classList.toggle('hidden')
    const svg = chevronEl.querySelector('svg')
    if (svg) svg.setAttribute('class', chevron({ expanded: !collapsed }))
  }
  header.addEventListener('click', toggleThinking)
  p.addEventListener('click', toggleThinking)

  return { p, labelEl }
}

export function writeThinkingText(p: HTMLParagraphElement, text: string): void {
  const html = renderMarkdown(text)
  if (p.children.length > 0) {
    morphHtml(p, html)
  } else {
    p.innerHTML = html
  }
}

export function refreshThinkingLabel(labelEl: HTMLSpanElement, startedAt: number): void {
  const seconds = (Date.now() - startedAt) / 1000
  labelEl.textContent = `Thinking (${formatDurationNs(seconds * 1e9)})`
}

export function finishThinking(
  p: HTMLParagraphElement,
  labelEl: HTMLSpanElement,
  durationNs: number,
): void {
  labelEl.textContent = `Thought for ${formatDurationNs(durationNs)}`
  // Only the glyph carries the heartbeat in a thinking block; the dot is
  // a separate concern handled by finishTool.
  const glyph = labelEl.parentElement?.querySelector('.heartbeat')
  if (glyph) glyph.classList.remove('heartbeat')
  // Auto-collapse on finish (matches Thinking.tsx active→inactive → setExpanded(false)).
  p.classList.add('hidden')
  // Sync chevron: collapsed = no rotation, expanded = rotate-90.
  const chevronNode = labelEl.parentElement?.querySelector('svg')
  if (chevronNode) chevronNode.setAttribute('class', chevron({ expanded: false }))
}

function createGroupContainer(): HTMLElement {
  const group = createElement('div')
  group.dataset.toolGroup = '1'

  const { header, dot, chevron: chevronEl, content } = createToolHeader()
  header.dataset.groupHeader = '1'
  dot.dataset.groupDot = '1'
  chevronEl.dataset.groupChevron = '1'

  const summary = createElement('span', groupSummary())
  summary.dataset.groupSummary = '1'
  content.appendChild(summary)

  const duration = createElement('span', groupDuration())
  duration.dataset.groupDuration = '1'
  content.appendChild(duration)

  group.appendChild(header)

  const toolsContainer = createElement('div', groupToolsContainer())
  toolsContainer.dataset.groupTools = '1'
  group.appendChild(toolsContainer)

  header.addEventListener('click', () => {
    const visible = !toolsContainer.classList.contains('hidden')
    toolsContainer.classList.toggle('hidden', visible)
    const svg = chevronEl.querySelector('svg')
    if (svg) svg.setAttribute('class', chevron({ expanded: !visible }))
  })

  return group
}

// Requires group created by createGroupContainer() (needs [data-group-tools]).
function updateGroupSummary(group: HTMLElement): void {
  const toolsContainer = group.querySelector('[data-group-tools]')
  if (!toolsContainer) return
  const tools = toolsContainer.querySelectorAll('[data-tool-root]')
  const summary = group.querySelector('[data-group-summary]')
  const durationEl = group.querySelector('[data-group-duration]') as HTMLElement
  const dot = group.querySelector('[data-group-dot]') as HTMLElement
  if (!summary) return

  const names = Array.from(tools).map(t => {
    return (t as HTMLElement).dataset.toolName || ''
  })
  summary.textContent = summarize(names.map(n => ({ name: n })))

  // Dot: white+heartbeat if any tool still running, green when all done
  const runningTools = toolsContainer.querySelectorAll('[data-tool-root] .heartbeat').length
  if (dot) {
    if (runningTools > 0) {
      dot.classList.add('text-white', 'heartbeat')
      dot.classList.remove('text-green')
    } else {
      dot.classList.remove('text-white', 'heartbeat')
      dot.classList.add('text-green')
    }
  }

  // Duration: only when all done and timing available
  if (durationEl) {
    if (runningTools === 0) {
      const totalNs = Array.from(tools).reduce((sum, t) => {
        return sum + (parseInt((t as HTMLElement).dataset.toolTimingNs || '0', 10) || 0)
      }, 0)
      durationEl.textContent = totalNs > 0 ? ` ${formatDurationNs(totalNs)}` : ''
    } else {
      durationEl.textContent = ''
    }
  }
}

export function appendToolBlock(parent: HTMLElement, name: string, before?: Node | null, collapsible = false): ToolDomHandles {
  const root = createElement('div')
  root.dataset.toolRoot = '1'
  root.dataset.toolName = name
  if (collapsible) root.dataset.collapsible = '1'

  const { header, dot, content } = createToolHeader()

  const nameEl = createElement('span', toolName())
  nameEl.textContent = name
  content.appendChild(nameEl)

  const summaryEl = createElement('span', toolSummary())
  content.appendChild(summaryEl)

  const durEl = createElement('span', toolDuration({ state: 'running' }))
  durEl.textContent = ' 0s'
  content.appendChild(durEl)

  root.appendChild(header)

  const body = createElement('div', toolBody())
  root.appendChild(body)

  const childrenContainer = createElement('div', toolChildren())
  childrenContainer.dataset.toolChildren = '1'
  root.appendChild(childrenContainer)

  // Collapsible tool grouping: if previous sibling is a group, append.
  // If previous sibling is also a standalone collapsible tool, create group.
  if (collapsible) {
    const sibling = findPrevToolSibling(before ?? null, parent)
    if (sibling?.dataset.toolGroup) {
      const toolsContainer = sibling.querySelector('[data-group-tools]') as HTMLElement
      // Absorb inter-tool thinking sitting between the group and the new tool.
      // Must run before appendChild(root) so thinking lands above the new tool.
      if (toolsContainer) {
        for (const th of collectTrailingThinking(sibling as HTMLElement)) {
          toolsContainer.appendChild(th)
        }
        toolsContainer.appendChild(root)
      } else {
        sibling.appendChild(root)
      }
      updateGroupSummary(sibling as HTMLElement)
      const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }
      header.addEventListener('click', () => toggleToolExpanded(handles))
      body.addEventListener('click', () => toggleToolExpanded(handles))
      return handles
    }
    if (sibling?.dataset.collapsible === '1') {
      // Collect pre-group thinking BEFORE replaceChild detaches sibling; after
      // detach, sibling.previousElementSibling would return null.
      const preThinking = collectLeadingThinking(sibling)
      // Also collect inter-tool thinking BEFORE replaceChild — the thinking
      // blocks between sibling and the new tool; after detach, sibling's
      // nextElementSibling is null.
      const postThinking = collectTrailingThinking(sibling as HTMLElement)
      const group = createGroupContainer()
      parent.replaceChild(group, sibling)
      const toolsContainer = group.querySelector('[data-group-tools]') as HTMLElement
      for (const th of preThinking) toolsContainer.appendChild(th)
      toolsContainer.appendChild(sibling)
      for (const th of postThinking) toolsContainer.appendChild(th)
      toolsContainer.appendChild(root)
      updateGroupSummary(group)
      const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }
      header.addEventListener('click', () => toggleToolExpanded(handles))
      body.addEventListener('click', () => toggleToolExpanded(handles))
      return handles
    }
  }

  insertBefore(parent, root, before ?? null)

  const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }

  header.addEventListener('click', () => toggleToolExpanded(handles))
  body.addEventListener('click', () => toggleToolExpanded(handles))

  return handles
}

// markToolCollapsible retroactively marks an already-rendered tool as
// collapsible and attempts to group it with the previous tool sibling.
// Used when is_search arrives via tool_param_delta after tool_start.
export function markToolCollapsible(root: HTMLElement): void {
  if (root.dataset.collapsible) return
  root.dataset.collapsible = '1'

  const parent = root.parentElement
  if (!parent) return
  const sibling = findPrevToolSibling(root, parent)
  if (!sibling) return

  // Previous sibling is a group — move this tool into it.
  if (sibling.dataset.toolGroup) {
    const toolsContainer = sibling.querySelector('[data-group-tools]') as HTMLElement
    if (toolsContainer) {
      // Absorb inter-tool thinking.
      for (const th of collectTrailingThinking(sibling as HTMLElement)) {
        toolsContainer.appendChild(th)
      }
      toolsContainer.appendChild(root)
      updateGroupSummary(sibling as HTMLElement)
    }
    return
  }

  // Previous sibling is also collapsible — create a new group.
  if (sibling.dataset.collapsible === '1') {
    const preThinking = collectLeadingThinking(sibling)
    const postThinking = collectTrailingThinking(sibling as HTMLElement)
    const group = createGroupContainer()
    parent.replaceChild(group, sibling)
    const toolsContainer = group.querySelector('[data-group-tools]') as HTMLElement
    for (const th of preThinking) toolsContainer.appendChild(th)
    toolsContainer.appendChild(sibling)
    for (const th of postThinking) toolsContainer.appendChild(th)
    toolsContainer.appendChild(root)
    updateGroupSummary(group)
  }
}

export function appendToolChildrenContainer(handles: ToolDomHandles): HTMLDivElement {
  return handles.childrenContainer
}

export function setToolSummary(handles: ToolDomHandles, summary: string, toolName?: string): void {
  if (!summary) {
    handles.summaryEl.textContent = ''
    return
  }
  if (toolName === 'Bash') {
    const highlighted = hljs.highlight(summary, { language: 'bash' }).value
    handles.summaryEl.innerHTML = ` (${highlighted})`
  } else if (toolName === 'Repl') {
    const highlighted = hljs.highlight(summary, { language: 'javascript' }).value
    handles.summaryEl.innerHTML = ` (${highlighted})`
  } else {
    handles.summaryEl.textContent = ` (${summary})`
  }
}

export function setToolOutput(handles: ToolDomHandles, output: string, skipHighlight = false): void {
  handles.body.innerHTML = renderToolOutput(output, skipHighlight)
}

export function refreshToolDuration(handles: ToolDomHandles, startedAt: number): void {
  const seconds = (Date.now() - startedAt) / 1000
  handles.durEl.textContent = ' ' + formatDurationNs(seconds * 1e9)
}

export function finishTool(
  handles: ToolDomHandles,
  opts: { isError: boolean; durationNs: number; output: string; skipHighlight?: boolean },
): void {
  const { isError, durationNs, output, skipHighlight } = opts
  handles.dot.classList.remove('heartbeat', 'text-white')
  handles.dot.classList.add(isError ? 'text-red' : 'text-green')
  handles.root.dataset.toolTimingNs = String(durationNs)
  const dur = formatDurationNs(durationNs)
  handles.durEl.textContent = ' ' + (isError ? `FAIL · ${dur}` : dur)
  handles.durEl.className = toolDuration({ state: isError ? 'error' : 'done' })
  if (output) {
    setToolOutput(handles, output, skipHighlight)
    if (shouldAutoExpand(handles.root.dataset.toolName ?? '')) {
      handles.body.classList.remove('hidden')
    }
  }
  // If tool is inside a group, update group dot/summary/duration.
  const group = handles.root.closest('[data-tool-group]') as HTMLElement | null
  if (group) updateGroupSummary(group)
}

export function toggleToolExpanded(handles: ToolDomHandles): void {
  const collapsed = !handles.body.classList.contains('hidden')
  if (collapsed) {
    handles.body.classList.add('hidden')
    handles.childrenContainer.classList.add('hidden')
  } else {
    handles.body.classList.remove('hidden')
    handles.childrenContainer.classList.remove('hidden')
  }
  const svg = handles.header.querySelector('svg')
  if (svg) svg.setAttribute('class', chevron({ expanded: !collapsed }))
}

export function expandToolChildrenForRunning(handles: ToolDomHandles): void {
  handles.childrenContainer.classList.remove('hidden')
  const svg = handles.header.querySelector('svg')
  if (svg) svg.setAttribute('class', chevron({ expanded: true }))
}

export function collapseToolChildrenOnDone(handles: ToolDomHandles): void {
  handles.childrenContainer.replaceChildren()
  handles.childrenContainer.classList.add('hidden')
  const svg = handles.header.querySelector('svg')
  if (svg) svg.setAttribute('class', chevron({ expanded: false }))
}

export function appendProgressBar(parent: HTMLElement, before?: Node | null): ProgressDomHandles {
  const root = createElement('div', progressBar())

  const dotWrap = createElement('span', toolPrefix())
  const dotEl = createElement('span', runningDot({ color: 'blue' }))
  dotEl.textContent = '●'
  dotWrap.appendChild(dotEl)
  root.appendChild(dotWrap)

  const inEl = createElement('span')
  inEl.textContent = '↑' + formatTokenCount(0)
  root.appendChild(inEl)

  const outEl = createElement('span')
  outEl.textContent = '↓' + formatTokenCount(0)
  root.appendChild(outEl)

  const tokensSuffix = createElement('span', 'hidden')
  tokensSuffix.textContent = ''
  root.appendChild(tokensSuffix)

  const sep1 = createElement('span')
  sep1.textContent = '·'
  root.appendChild(sep1)

  const rateEl = createElement('span')
  rateEl.textContent = '0.0 t/s'
  root.appendChild(rateEl)

  const sep2 = createElement('span', 'sep-cache hidden')
  sep2.textContent = '·'
  root.appendChild(sep2)

  const cacheEl = createElement('span')
  root.appendChild(cacheEl)

  const sep3 = createElement('span', 'sep-tools hidden')
  sep3.textContent = '·'
  root.appendChild(sep3)

  const toolCountEl = createElement('span')
  toolCountEl.textContent = ''
  root.appendChild(toolCountEl)

  const sep4 = createElement('span', 'sep-elapsed')
  sep4.textContent = '·'
  root.appendChild(sep4)

  const elapsedEl = createElement('span')
  elapsedEl.textContent = '0s'
  root.appendChild(elapsedEl)

  const thinkingEl = createElement('span', 'hidden')
  root.appendChild(thinkingEl)

  insertBefore(parent, root, before ?? null)

  return { root, elapsedEl, inEl, outEl, rateEl, toolCountEl, dotEl, cacheEl, thinkingEl, tokensSuffix }
}

export function setProgressBarUsage(
  h: ProgressDomHandles,
  u: { inputTokens: number; outputTokens: number; cacheRead: number; cacheCreation: number },
): void {
  // Streaming line uses totalInput (matches TUI streaming tokensStr).
  const totalInput = u.inputTokens + u.cacheRead + u.cacheCreation
  h.inEl.textContent = '↑' + formatTokenCount(totalInput)
  h.outEl.textContent = '↓' + formatTokenCount(u.outputTokens)
  // Streaming does NOT show cache info — TUI streaming progress line has no
  // cacheStr (only AppendStatsLine does). Keep cacheEl/sep-cache hidden.
  h.cacheEl.textContent = ''
  const sep = h.root.querySelector('.sep-cache') as HTMLElement | null
  if (sep) sep.classList.add('hidden')
}

export function refreshProgressBar(
  h: ProgressDomHandles,
  startedAt: number,
  toolCount: number,
  outputTokens: number,
): void {
  const elapsedSec = (Date.now() - startedAt) / 1000
  h.elapsedEl.textContent = formatDurationNs(elapsedSec * 1e9)
  const rate = elapsedSec > 0 ? outputTokens / elapsedSec : 0
  h.rateEl.textContent = rate.toFixed(1) + ' t/s'
  h.toolCountEl.textContent = toolCount === 1 ? '1 tool' : toolCount + ' tools'
  const sep = h.root.querySelector('.sep-tools') as HTMLElement | null
  if (sep) sep.classList.toggle('hidden', !(toolCount > 0))
  h.toolCountEl.classList.toggle('hidden', !(toolCount > 0))
}

export function finalizeProgressBar(
  h: ProgressDomHandles,
  usage: { inputTokens: number; outputTokens: number; cacheRead: number; cacheCreation: number },
  elapsedMs: number,
  toolCount: number,
  _thinkingDurationMs?: number,
): void {
  h.dotEl.classList.remove('heartbeat')
  if (h.dotEl.parentElement) h.dotEl.parentElement.classList.add('hidden')
  h.root.dataset.progress = '1'
  const totalInput = usage.inputTokens + usage.cacheRead + usage.cacheCreation
  h.inEl.textContent = '↑' + formatTokenCount(totalInput)
  h.outEl.textContent = '↓' + formatTokenCount(usage.outputTokens)
  const elapsedSec = elapsedMs / 1000
  const rate = elapsedSec > 0 && usage.outputTokens > 0 ? usage.outputTokens / elapsedSec : 0
  h.rateEl.textContent = rate > 0 ? rate.toFixed(1) + ' t/s' : ''
  h.elapsedEl.textContent = formatDurationNs(elapsedMs * 1e6)
  h.toolCountEl.textContent = toolCount === 1 ? '1 tool' : toolCount > 0 ? toolCount + ' tools' : ''
  const sepTools = h.root.querySelector('.sep-tools') as HTMLElement | null
  if (sepTools) sepTools.classList.toggle('hidden', !(toolCount > 0))
  // Cache line matching TUI AppendStatsLine.
  const sepCache = h.root.querySelector('.sep-cache') as HTMLElement | null
  if (sepCache) sepCache.classList.remove('hidden')
  if (usage.cacheRead > 0 || usage.cacheCreation > 0) {
    const total = totalInput
    if (total > 0 && usage.cacheRead > 0) {
      const pct = usage.cacheRead * 100 / total
      // Truncate (not round) so 99.96% shows as 99.9%, not 100.0%.
      h.cacheEl.textContent = (Math.floor(pct * 10) / 10).toFixed(1) + '% cached'
    } else if (usage.cacheCreation > 0) {
      h.cacheEl.textContent = formatTokenCount(usage.cacheCreation) + ' warmed'
    }
  } else {
    h.cacheEl.textContent = 'cache missed'
  }
}
