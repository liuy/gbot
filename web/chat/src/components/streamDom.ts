// Pure DOM mutation module for the streaming container. ChatInterface owns
// all writes here during streaming; React never touches the container's
// children until query_end swaps StreamingMessage out for MessageComponent.
//
// Every append function takes an optional `before` anchor so ChatInterface
// can pin the progress bar to the bottom of the streaming container by
// passing `progressHandles.current?.root ?? null` as the anchor.

import { formatDurationNs, formatTokenCount, stripAnsi, summarize } from '../utils'

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

export function appendTextBlock(parent: HTMLElement, before?: Node | null): HTMLDivElement {
  const div = document.createElement('div')
  div.className = 'md-body md-text text-t1 text-[15px] leading-relaxed'
  insertBefore(parent, div, before ?? null)
  return div
}

export function appendUserBlock(parent: HTMLElement, text: string, before?: Node | null): HTMLDivElement {
  const div = document.createElement('div')
  div.className = 'text-[13px] text-t2 italic ml-2 my-1'
  div.textContent = text
  insertBefore(parent, div, before ?? null)
  return div
}

export function appendThinkingBlock(
  parent: HTMLElement,
  startedAt: number,
  before?: Node | null,
): { p: HTMLParagraphElement; labelEl: HTMLSpanElement } {
  const wrap = document.createElement('div')
  wrap.dataset.thinking = '1'

  const header = document.createElement('span')
  header.setAttribute('role', 'button')
  header.tabIndex = 0
  header.className = 'inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle'

  const glyph = document.createElement('span')
  glyph.className = 'text-amber text-sm leading-none align-middle inline-block w-4 text-center heartbeat'
  glyph.textContent = '✦'
  header.appendChild(glyph)

  const chevron = document.createElement('span')
  chevron.innerHTML = '<svg class="inline-block align-middle text-t3 transition-transform rotate-90" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>'
  header.appendChild(chevron)

  const labelEl = document.createElement('span')
  labelEl.className = 'text-amber text-sm align-middle'
  labelEl.textContent = `Thinking (${formatDurationNs(0)})`
  header.appendChild(labelEl)

  wrap.appendChild(header)

  // <p> always mounted (CSS hidden when collapsed) so the sink stays live
  // for streaming writes even when collapsed.
  const p = document.createElement('p')
  p.className = 'ml-5 text-t2 text-sm italic whitespace-pre-wrap'
  p.style.maxHeight = 'none'
  wrap.appendChild(p)

  insertBefore(parent, wrap, before ?? null)

  // Auto-expand on creation (matches Thinking.tsx active → expanded).
  header.addEventListener('click', () => {
    const collapsed = p.classList.toggle('hidden')
    const svg = chevron.querySelector('svg')
    if (svg) svg.setAttribute('class',
      'inline-block align-middle text-t3 transition-transform ' + (collapsed ? '' : 'rotate-90'))
  })

  return { p, labelEl }
}

export function writeThinkingText(p: HTMLParagraphElement, text: string): void {
  p.textContent = text
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
  // Stop the heartbeat animation on the glyph.
  const glyph = labelEl.parentElement?.querySelector('.heartbeat')
  if (glyph) glyph.classList.remove('heartbeat')
  // Auto-collapse on finish (matches Thinking.tsx active→inactive → setExpanded(false)).
  p.classList.add('hidden')
}

function createGroupContainer(): HTMLElement {
  const group = document.createElement('div')
  group.dataset.toolGroup = '1'

  // Header: button with dot + chevron + summary + duration (mirrors ToolGroup.tsx)
  const header = document.createElement('span')
  header.dataset.groupHeader = '1'
  header.setAttribute('role', 'button')
  header.tabIndex = 0
  header.className = 'inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle'

  // Dot: same class as individual tool dot (text-white heartbeat while running)
  const dot = document.createElement('span')
  dot.className = 'text-[10px] leading-none align-middle inline-block w-4 text-center text-white heartbeat'
  dot.dataset.groupDot = '1'
  dot.textContent = '●'
  header.appendChild(dot)

  // Chevron SVG (same as ToolGroup.tsx)
  const chevron = document.createElement('span')
  chevron.dataset.groupChevron = '1'
  chevron.innerHTML = '<svg class="inline-block align-middle text-t3" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>'
  header.appendChild(chevron)

  // Summary text: "2 Searches, 1 Read"
  const summary = document.createElement('span')
  summary.dataset.groupSummary = '1'
  summary.className = 'font-mono text-sm text-blue align-middle'
  header.appendChild(summary)

  // Duration: shown when all done
  const duration = document.createElement('span')
  duration.dataset.groupDuration = '1'
  duration.className = 'font-mono text-xs align-middle text-t3'
  header.appendChild(duration)

  group.appendChild(header)

  // Tools container: default collapsed
  const toolsContainer = document.createElement('div')
  toolsContainer.dataset.groupTools = '1'
  toolsContainer.className = 'ml-[20px]'
  toolsContainer.style.display = 'none'
  group.appendChild(toolsContainer)

  // Toggle expand/collapse on click
  header.addEventListener('click', () => {
    const visible = toolsContainer.style.display !== 'none'
    toolsContainer.style.display = visible ? 'none' : ''
    const svg = chevron.querySelector('svg')
    if (svg) svg.style.transform = visible ? '' : 'rotate(90deg)'
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
  const root = document.createElement('div')
  root.dataset.toolRoot = '1'
  root.dataset.toolName = name
  if (collapsible) root.dataset.collapsible = '1'

  const header = document.createElement('span')
  header.setAttribute('role', 'button')
  header.tabIndex = 0
  header.className = 'inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle'

  const dot = document.createElement('span')
  dot.className = 'text-[10px] leading-none align-middle inline-block w-4 text-center text-white heartbeat'
  dot.textContent = '●'
  header.appendChild(dot)

  const chevron = document.createElement('span')
  chevron.innerHTML = '<svg class="inline-block align-middle text-t3 transition-transform" width="12" height="12" viewBox="0 0 12 12" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M4.5 3L7.5 6L4.5 9"/></svg>'
  header.appendChild(chevron)

  const nameEl = document.createElement('span')
  nameEl.className = 'font-mono text-sm text-blue align-middle'
  nameEl.textContent = name
  header.appendChild(nameEl)

  const summaryEl = document.createElement('span')
  summaryEl.className = 'text-sm text-t2 font-light break-all align-middle'
  header.appendChild(summaryEl)

  const durEl = document.createElement('span')
  durEl.className = 'font-mono text-xs align-middle text-blue'
  durEl.textContent = ' 0s'
  header.appendChild(durEl)

  root.appendChild(header)

  const body = document.createElement('div')
  body.className = 'ml-[20px] font-mono text-sm leading-relaxed text-t2 whitespace-pre overflow-x-auto hidden'
  root.appendChild(body)

  const childrenContainer = document.createElement('div')
  childrenContainer.className = 'ml-[20px] mt-1 space-y-1 border-l border-t3/30 pl-2 hidden'
  childrenContainer.dataset.toolChildren = '1'
  root.appendChild(childrenContainer)

  // Collapsible tool grouping: if previous sibling is a group, append.
  // If previous sibling is also a standalone collapsible tool, create group.
  if (collapsible) {
    const sibling = findPrevToolSibling(before ?? null, parent)
    if (sibling?.dataset.toolGroup) {
      const toolsContainer = sibling.querySelector('[data-group-tools]') as HTMLElement
      if (toolsContainer) toolsContainer.appendChild(root)
      else sibling.appendChild(root)
      updateGroupSummary(sibling as HTMLElement)
      const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }
      header.addEventListener('click', () => toggleToolExpanded(handles))
      return handles
    }
    if (sibling?.dataset.collapsible === '1') {
      const group = createGroupContainer()
      parent.replaceChild(group, sibling)
      const toolsContainer = group.querySelector('[data-group-tools]') as HTMLElement
      toolsContainer.appendChild(sibling)
      toolsContainer.appendChild(root)
      updateGroupSummary(group)
      const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }
      header.addEventListener('click', () => toggleToolExpanded(handles))
      return handles
    }
  }

  insertBefore(parent, root, before ?? null)

  const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }

  header.addEventListener('click', () => toggleToolExpanded(handles))

  return handles
}

export function appendToolChildrenContainer(handles: ToolDomHandles): HTMLDivElement {
  return handles.childrenContainer
}

export function setToolSummary(handles: ToolDomHandles, summary: string): void {
  handles.summaryEl.textContent = summary ? ` (${summary})` : ''
}

export function setToolOutput(handles: ToolDomHandles, output: string): void {
  handles.body.textContent = stripAnsi(output)
}

export function refreshToolDuration(handles: ToolDomHandles, startedAt: number): void {
  const seconds = (Date.now() - startedAt) / 1000
  handles.durEl.textContent = ' ' + formatDurationNs(seconds * 1e9)
}

export function finishTool(
  handles: ToolDomHandles,
  opts: { isError: boolean; durationNs: number; output: string },
): void {
  const { isError, durationNs, output } = opts
  handles.dot.classList.remove('heartbeat', 'text-white')
  handles.dot.classList.add(isError ? 'text-red' : 'text-green')
  handles.root.dataset.toolTimingNs = String(durationNs)
  const dur = formatDurationNs(durationNs)
  handles.durEl.textContent = ' ' + (isError ? `FAIL · ${dur}` : dur)
  handles.durEl.className = 'font-mono text-xs align-middle ' + (isError ? 'text-red' : 'text-t3')
  if (output) setToolOutput(handles, output)
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
  if (svg) svg.setAttribute('class',
    'inline-block align-middle text-t3 transition-transform' + (collapsed ? '' : ' rotate-90'))
}

export function expandToolChildrenForRunning(handles: ToolDomHandles): void {
  handles.childrenContainer.classList.remove('hidden')
  const svg = handles.header.querySelector('svg')
  if (svg) svg.setAttribute('class', 'inline-block align-middle text-t3 transition-transform rotate-90')
}

export function collapseToolChildrenOnDone(handles: ToolDomHandles): void {
  handles.childrenContainer.classList.add('hidden')
  const svg = handles.header.querySelector('svg')
  if (svg) svg.setAttribute('class', 'inline-block align-middle text-t3 transition-transform')
}

export function appendProgressBar(parent: HTMLElement, before?: Node | null): ProgressDomHandles {
  const root = document.createElement('div')
  root.className = 'mt-2 flex items-center gap-2 overflow-x-auto overflow-y-hidden whitespace-nowrap text-xs text-t3'

  const dotEl = document.createElement('span')
  dotEl.className = 'inline-block overflow-hidden text-[12px] text-blue heartbeat'
  dotEl.textContent = '●'
  root.appendChild(dotEl)

  const inEl = document.createElement('span')
  inEl.textContent = '↑' + formatTokenCount(0)
  root.appendChild(inEl)

  const outEl = document.createElement('span')
  outEl.textContent = '↓' + formatTokenCount(0)
  root.appendChild(outEl)

  const tokensSuffix = document.createElement('span')
  tokensSuffix.textContent = 'tokens'
  root.appendChild(tokensSuffix)

  const sep1 = document.createElement('span')
  sep1.textContent = '·'
  root.appendChild(sep1)

  const rateEl = document.createElement('span')
  rateEl.textContent = '0.0 t/s'
  root.appendChild(rateEl)

  const sep2 = document.createElement('span')
  sep2.textContent = '·'
  sep2.className = 'sep-cache'
  sep2.style.display = 'none'
  root.appendChild(sep2)

  const cacheEl = document.createElement('span')
  root.appendChild(cacheEl)

  const sep3 = document.createElement('span')
  sep3.textContent = '·'
  sep3.className = 'sep-tools'
  sep3.style.display = 'none'
  root.appendChild(sep3)

  const toolCountEl = document.createElement('span')
  toolCountEl.textContent = ''
  root.appendChild(toolCountEl)

  const sep4 = document.createElement('span')
  sep4.textContent = '·'
  sep4.className = 'sep-elapsed'
  root.appendChild(sep4)

  const elapsedEl = document.createElement('span')
  elapsedEl.textContent = '0s'
  root.appendChild(elapsedEl)

  insertBefore(parent, root, before ?? null)

  return { root, elapsedEl, inEl, outEl, rateEl, toolCountEl, dotEl, cacheEl, thinkingEl: document.createElement('span'), tokensSuffix }
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
  if (sep) sep.style.display = 'none'
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
  if (sep) sep.style.display = toolCount > 0 ? '' : 'none'
}

export function finalizeProgressBar(
  h: ProgressDomHandles,
  usage: { inputTokens: number; outputTokens: number; cacheRead: number; cacheCreation: number },
  elapsedMs: number,
  toolCount: number,
  thinkingDurationMs?: number,
): void {
  h.dotEl.classList.remove('heartbeat')
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
  if (sepTools) sepTools.style.display = toolCount > 0 ? '' : 'none'
  // Cache line matching TUI AppendStatsLine.
  const sepCache = h.root.querySelector('.sep-cache') as HTMLElement | null
  if (usage.cacheRead > 0 || usage.cacheCreation > 0) {
    const total = totalInput
    if (total > 0 && usage.cacheRead > 0) {
      const pct = Math.round(usage.cacheRead * 100 / total)
      h.cacheEl.textContent = pct + '% cached'
    } else if (usage.cacheCreation > 0) {
      h.cacheEl.textContent = formatTokenCount(usage.cacheCreation) + ' warmed'
    }
  } else {
    h.cacheEl.textContent = 'cache missed'
  }
  if (thinkingDurationMs && thinkingDurationMs > 0) {
    h.thinkingEl.textContent = 'thought for ' + (thinkingDurationMs / 1000).toFixed(1) + 's'
  } else {
    h.thinkingEl.textContent = ''
  }
}
