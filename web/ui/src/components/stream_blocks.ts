// Shared single-block stream renderer: a Block tree → DOM. Extracted from
// chat's createChat closure so job feeds render with the exact same DOM the
// chat streaming container produces. Chat-specific incremental-delta concerns
// (running-tool registry, active-thinking tracking) are parameterized through
// the optional LiveStreamSink — chat passes its own sink, job feeds render
// sink-less and never touch chat's registries.

import { renderMarkdown } from '../markdown'
import { createCopyButton } from '../utils/copy_button'
import { createElement } from '../dom'
import { classifyReminderMessage, isCollapsibleBlock, type ReminderRow } from '../utils'
import { formatArtifactSize } from '../artifact'
import { userEchoBlock } from '../styles/recipes'
import type { Block } from '../model'
import {
  type ToolDomHandles,
  appendTextBlock,
  appendThinkingBlock,
  appendQueuedMessageBlock,
  appendToolBlock,
  appendToolChildrenContainer,
  setToolSummary,
  writeThinkingText,
  finishThinking,
  finishTool,
  expandToolChildrenForRunning,
} from './stream_dom'

type ToolBlockData = Extract<Block, { kind: 'tool' }>

export interface ThinkingEntryData {
  p: HTMLParagraphElement
  labelEl: HTMLSpanElement
  startedAt: number
  pendingBlock: Extract<Block, { kind: 'thinking' }>
}

// Registration surface for live (streaming) consumers. Without a sink the
// renderer is pure: snapshot-style renders (job feeds) rebuild the same DOM
// from block state alone.
export interface LiveStreamSink {
  onToolRunning(handles: ToolDomHandles, block: ToolBlockData, parentID: string | null): void
  onThinkingActive(entry: ThinkingEntryData, parentID: string | null): void
}

// wireCopyButtons must be called after every innerHTML assignment that may
// contain rendered markdown — innerHTML wipes prior DOM, so the injected
// buttons do not survive a re-render.
export function wireCopyButtons(container: HTMLElement) {
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

// Lightweight lightbox: clicking any user-message thumbnail overlays a
// full-screen, scrollable, click-to-close view of the original image. No
// zoom/pan gestures — keeps the implementation under 30 lines and avoids
// pulling in a library. The overlay is created lazily on first click and
// reused across clicks.
let lightboxOverlay: HTMLDivElement | null = null
export function showImageLightbox(src: string): void {
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

// buildDocumentChip renders a display-only chip for a document reference
// block: `[name size]` mono chip — byte-identical to the input bar's
// pending upload chip, so upload and history render the same (the legacy
// regex chip shares the shape minus size, which old text blocks never
// carried). Deliberately NO click behavior (user decision) and never the
// parsed markdown.
export function buildDocumentChip(name: string, size: number | undefined): HTMLElement {
  const chip = createElement(
    'span',
    'font-mono text-[12px] bg-ink2 text-t2 rounded-md px-2 py-1 mr-1',
  )
  let label = `[${name}`
  if (size !== undefined && size > 0) {
    label += ` ${formatArtifactSize(size)}`
  }
  chip.textContent = label + ']'
  return chip
}

// createAttachmentImage mirrors the committed-history image visual exactly
// (class + lightbox click), so a drained queued attachment looks the same as
// the same image replayed from history.
export function createAttachmentImage(src: string): HTMLImageElement {
  const img = createElement('img', 'block max-w-[200px] max-h-[200px] rounded-lg my-1 cursor-zoom-in')
  img.src = src
  img.addEventListener('click', () => showImageLightbox(src))
  return img
}

// Attachment echo nodes get their own userEchoBlock container as siblings of
// the queued-text echo, matching what renderStreamBlock rebuilds from
// pendingBlocks on rewind/takeover — same block, same DOM shape.
export function appendUserAttachment(parent: HTMLElement, node: HTMLElement, before: Node | null): void {
  const wrap = createElement('div', userEchoBlock())
  wrap.appendChild(node)
  parent.insertBefore(wrap, before)
}

function wallClockHHMMSS(ts: number): string {
  const d = new Date(ts)
  const p = (n: number) => String(n).padStart(2, '0')
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`
}

// A reminder row is built entirely by the tool-block factory — appendToolBlock
// header (dot, chevron, name, summary, duration) + finishTool settle — so a
// reminder is anatomically a finished tool row. The one bespoke input is the
// wall clock override in the duration slot: the message's own timestamp, not a
// fake tool duration.
export function appendReminderRow(parent: HTMLElement, row: ReminderRow, startedAt: number, before: Node | null = null): void {
  const handles = appendToolBlock(parent, row.name, before)
  handles.root.dataset.reminderRow = '1'
  setToolSummary(handles, row.summary, row.name)
  finishTool(handles, { isError: false, durationNs: 0, output: row.raw, skipHighlight: true })
  handles.durEl.textContent = ' ' + wallClockHHMMSS(startedAt)
}

export function renderStreamBlock(
  parent: HTMLElement,
  block: Block,
  before: Node | null,
  parentID: string | null,
  live?: LiveStreamSink,
): void {
  if (block.kind === 'user') {
    if (!block.text) return
    // A block whose text sniffs as an injected reminder envelope becomes a
    // Reminder row through the shared tool-block path instead of gray queued
    // echo text. One sniff point here covers chat (live event insertion,
    // takeover snapshot, streamState backfill) and job feeds alike, so every
    // surface renders the same row. Mid-turn queued message echoes (the
    // engine persists them immediately in reminder form; text is already
    // unwrapped) never classify and stay gray.
    const reminder = classifyReminderMessage(block.text)
    if (reminder) {
      appendReminderRow(parent, reminder, block.startedAt ?? Date.now(), before)
      return
    }
    appendQueuedMessageBlock(parent, block.text, before)
  } else if (block.kind === 'image') {
    appendUserAttachment(parent, createAttachmentImage(block.src), before)
  } else if (block.kind === 'document') {
    appendUserAttachment(parent, buildDocumentChip(block.name, block.size), before)
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
      const entry: ThinkingEntryData = { p, labelEl, startedAt, pendingBlock: block }
      live?.onThinkingActive(entry, parentID)
    } else {
      finishThinking(p, labelEl, block.durationNs)
    }
  } else if (block.kind === 'tool') {
    const handles = appendToolBlock(parent, block.name, before, isCollapsibleBlock(block))
    if (block.summary) setToolSummary(handles, block.summary, block.name)
    if (block.state === 'running') {
      live?.onToolRunning(handles, block, parentID)
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
        renderStreamBlock(childContainer, child, null, block.id, live)
      }
    }
  }
}
