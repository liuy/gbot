// Pure DOM mutation module for the streaming container. ChatInterface owns
// all writes here during streaming; React never touches the container's
// children until query_end swaps StreamingMessage out for MessageComponent.
//
// Every append function takes an optional `before` anchor so ChatInterface
// can pin the progress bar to the bottom of the streaming container by
// passing `progressHandles.current?.root ?? null` as the anchor.

import { formatDurationNs, formatTokenCount, stripAnsi } from '../utils'

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
}

function insertBefore(parent: HTMLElement, child: Node, before: Node | null) {
  if (before) parent.insertBefore(child, before)
  else parent.appendChild(child)
}

export function appendTextBlock(parent: HTMLElement, before?: Node | null): HTMLDivElement {
  const div = document.createElement('div')
  div.className = 'md-body text-t1 text-[15px] leading-relaxed whitespace-pre-wrap'
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

  const header = document.createElement('span')
  header.role = 'button'
  header.tabIndex = 0
  header.className = 'inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle'

  const glyph = document.createElement('span')
  glyph.className = 'text-amber text-sm leading-none align-middle inline-block w-4 text-center'
  glyph.textContent = '✦'
  header.appendChild(glyph)

  const chevron = document.createElement('svg')
  chevron.setAttribute('class', 'inline-block align-middle text-t3 transition-transform rotate-90')
  chevron.setAttribute('width', '12')
  chevron.setAttribute('height', '12')
  chevron.setAttribute('viewBox', '0 0 12 12')
  chevron.setAttribute('fill', 'none')
  chevron.setAttribute('stroke', 'currentColor')
  chevron.setAttribute('stroke-width', '1.5')
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
  path.setAttribute('d', 'M4.5 3L7.5 6L4.5 9')
  chevron.appendChild(path)
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
    chevron.setAttribute(
      'class',
      'inline-block align-middle text-t3 transition-transform ' + (collapsed ? '' : 'rotate-90'),
    )
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
  // Auto-collapse on finish (matches Thinking.tsx active→inactive → setExpanded(false)).
  p.classList.add('hidden')
}

export function appendToolBlock(parent: HTMLElement, name: string, before?: Node | null): ToolDomHandles {
  const root = document.createElement('div')
  root.dataset.toolRoot = '1'

  const header = document.createElement('span')
  header.role = 'button'
  header.tabIndex = 0
  header.className = 'inline cursor-pointer bg-transparent border-0 p-0 text-left align-middle'

  const dot = document.createElement('span')
  dot.className = 'text-[10px] leading-none align-middle inline-block w-4 text-center text-white heartbeat'
  dot.textContent = '●'
  header.appendChild(dot)

  const chevron = document.createElement('svg')
  chevron.setAttribute('class', 'inline-block align-middle text-t3 transition-transform')
  chevron.setAttribute('width', '12')
  chevron.setAttribute('height', '12')
  chevron.setAttribute('viewBox', '0 0 12 12')
  chevron.setAttribute('fill', 'none')
  chevron.setAttribute('stroke', 'currentColor')
  chevron.setAttribute('stroke-width', '1.5')
  const path = document.createElementNS('http://www.w3.org/2000/svg', 'path')
  path.setAttribute('d', 'M4.5 3L7.5 6L4.5 9')
  chevron.appendChild(path)
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

  insertBefore(parent, root, before ?? null)

  const handles: ToolDomHandles = { root, header, dot, summaryEl, durEl, body, childrenContainer }

  header.addEventListener('click', () => toggleToolExpanded(handles))

  return handles
}

export function appendToolChildrenContainer(handles: ToolDomHandles): HTMLDivElement {
  return handles.childrenContainer
}

export function setToolSummary(handles: ToolDomHandles, summary: string): void {
  // Match ToolRenderer.tsx:99 conditional rendering — prefix with a space when non-empty.
  handles.summaryEl.textContent = summary ? ` ${summary}` : ''
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
  const dur = formatDurationNs(durationNs)
  handles.durEl.textContent = ' ' + (isError ? `FAIL · ${dur}` : dur)
  handles.durEl.className = 'font-mono text-xs align-middle ' + (isError ? 'text-red' : 'text-t3')
  if (output) setToolOutput(handles, output)
}

export function toggleToolExpanded(handles: ToolDomHandles): void {
  const collapsed = !handles.body.classList.contains('hidden')
  // Collapse = hide body + children; expand = show both.
  if (collapsed) {
    handles.body.classList.add('hidden')
    handles.childrenContainer.classList.add('hidden')
    handles.header.querySelector('svg')?.setAttribute(
      'class',
      'inline-block align-middle text-t3 transition-transform',
    )
  } else {
    handles.body.classList.remove('hidden')
    handles.childrenContainer.classList.remove('hidden')
    handles.header.querySelector('svg')?.setAttribute(
      'class',
      'inline-block align-middle text-t3 transition-transform rotate-90',
    )
  }
}

export function expandToolChildrenForRunning(handles: ToolDomHandles): void {
  // TUI parity: running agent tools auto-expand while children exist.
  handles.childrenContainer.classList.remove('hidden')
  handles.header.querySelector('svg')?.setAttribute(
    'class',
    'inline-block align-middle text-t3 transition-transform rotate-90',
  )
}

export function collapseToolChildrenOnDone(handles: ToolDomHandles): void {
  handles.childrenContainer.classList.add('hidden')
  handles.header.querySelector('svg')?.setAttribute(
    'class',
    'inline-block align-middle text-t3 transition-transform',
  )
}

export function appendProgressBar(parent: HTMLElement, before?: Node | null): ProgressDomHandles {
  const root = document.createElement('div')
  root.className = 'mt-2 flex items-center gap-2 overflow-x-auto overflow-y-hidden whitespace-nowrap text-xs text-t3'

  const dotEl = document.createElement('span')
  dotEl.className = 'inline-block overflow-hidden text-[12px] text-blue heartbeat'
  dotEl.textContent = '●'
  root.appendChild(dotEl)

  const elapsedEl = document.createElement('span')
  elapsedEl.textContent = '0s'
  root.appendChild(elapsedEl)

  const sep1 = document.createElement('span')
  sep1.textContent = '·'
  root.appendChild(sep1)

  const inEl = document.createElement('span')
  inEl.textContent = '↑' + formatTokenCount(0)
  root.appendChild(inEl)

  const outEl = document.createElement('span')
  outEl.textContent = '↓' + formatTokenCount(0)
  root.appendChild(outEl)

  const sep2 = document.createElement('span')
  sep2.textContent = '·'
  root.appendChild(sep2)

  const rateEl = document.createElement('span')
  rateEl.textContent = '0.0 t/s'
  root.appendChild(rateEl)

  const sep3 = document.createElement('span')
  sep3.textContent = '·'
  root.appendChild(sep3)

  const toolCountEl = document.createElement('span')
  toolCountEl.textContent = '0 tools'
  root.appendChild(toolCountEl)

  insertBefore(parent, root, before ?? null)

  return { root, elapsedEl, inEl, outEl, rateEl, toolCountEl, dotEl }
}

export function setProgressBarUsage(
  h: ProgressDomHandles,
  u: { inputTokens: number; outputTokens: number; cacheRead: number; cacheCreation: number },
): void {
  h.inEl.textContent = '↑' + formatTokenCount(u.inputTokens)
  h.outEl.textContent = '↓' + formatTokenCount(u.outputTokens)
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
  h.toolCountEl.textContent = toolCount + ' tools'
}
