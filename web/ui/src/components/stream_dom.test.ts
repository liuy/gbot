import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  appendTextBlock,
  appendToolBlock,
  appendUserBlock,
  appendThinkingBlock,
  setToolSummary,
  setToolOutput,
  finishTool,
  appendToolChildrenContainer,
  toggleToolExpanded,
  appendProgressBar,
  setProgressBarUsage,
  refreshProgressBar,
  finalizeProgressBar,
  writeThinkingText,
  writeThinkingStream,
  THINKING_RENDER_LIMIT,
  appendQueuedMessageBlock,
  refreshToolDuration,
  expandToolChildrenForRunning,
  collapseToolChildrenOnDone,
  finishThinking,
} from './stream_dom'
import { stopStatusSweep } from './status_sweep'

function newParent(): HTMLElement {
  return document.createElement('div')
}

describe('appendTextBlock', () => {
  it('returns a div with md-body className whose textContent is writable', () => {
    const parent = newParent()
    const div = appendTextBlock(parent)
    expect(div.className).toContain('md-body')
    div.textContent = 'hello'
    expect(div.textContent).toBe('hello')
    expect(parent.querySelector('.md-body')).toBe(div)
  })

  it('text content persists across subsequent tool_block appends in same parent', () => {
    const parent = newParent()
    const div = appendTextBlock(parent)
    div.textContent = 'keep me'
    appendToolBlock(parent, 'Bash')
    expect(div.textContent).toBe('keep me')
  })

  it('inserts before the given anchor when provided', () => {
    const parent = newParent()
    const anchor = document.createElement('div')
    anchor.className = 'anchor'
    parent.appendChild(anchor)
    const text = appendTextBlock(parent, anchor)
    expect(parent.firstChild).toBe(text)
    expect(parent.lastChild).toBe(anchor)
  })
})

describe('appendUserBlock', () => {
  it('writes the text into a styled div', () => {
    const parent = newParent()
    const div = appendUserBlock(parent, 'queued message')
    expect(div.textContent).toBe('queued message')
    expect(div.className).toContain('italic')
  })

  it('preserves newlines in multiline text', () => {
    const parent = newParent()
    const div = appendUserBlock(parent, 'line1\nline2\nline3')
    expect(div.textContent).toBe('line1\nline2\nline3')
    // Wrap classes live on the inner span (createUserTextSpan), not the div.
    const span = div.querySelector('span')
    expect(span).not.toBeNull()
    expect(span!.className).toContain('whitespace-pre-wrap')
  })

  it('has break-words to prevent overflow on long strings', () => {
    const parent = newParent()
    const div = appendUserBlock(parent, 'https://example.com/very/long/path/that/should/overflow')
    const span = div.querySelector('span')
    expect(span).not.toBeNull()
    expect(span!.className).toContain('break-words')
  })

  it('stamps data-block-kind=user for share-source resolution (chat.ts getSource)', () => {
    const parent = newParent()
    const div = appendUserBlock(parent, 'share me')
    // chat.ts matches [data-block-kind="user"] by VALUE — an empty value
    // silently drops the block from long-press share selection.
    expect(div.dataset.blockKind).toBe('user')
  })
})

describe('appendQueuedMessageBlock', () => {
  it('writes the muted gray echo with queued text span styling', () => {
    const parent = newParent()
    const div = appendQueuedMessageBlock(parent, 'reminder text')
    expect(div.textContent).toBe('reminder text')
    expect(div.className).toContain('text-t3')
    const span = div.querySelector('span')!
    expect(span.className).toContain('whitespace-pre-wrap')
    expect(span.className).toContain('italic')
  })

  it('stamps data-block-kind=user on both the div and the inner span', () => {
    const parent = newParent()
    const div = appendQueuedMessageBlock(parent, 'queued')
    expect(div.dataset.blockKind).toBe('user')
    const span = div.querySelector('span')!
    expect(span.dataset.blockKind).toBe('user')
  })
})

describe('appendThinkingBlock', () => {
  it('creates header with label and a mounted <p>', () => {
    const parent = newParent()
    const { p, labelEl } = appendThinkingBlock(parent, Date.now())
    expect(p.tagName).toBe('P')
    expect(labelEl.textContent).toContain('Thinking')
    expect(parent.querySelector('p')).toBe(p)
  })

  it('auto-expanded by default; clicking header collapses the <p>', () => {
    const parent = newParent()
    const { p, labelEl } = appendThinkingBlock(parent, Date.now())
    // Default expanded: <p> visible
    expect(p.classList.contains('hidden')).toBe(false)
    labelEl.parentElement?.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(p.classList.contains('hidden')).toBe(true)
  })

  it('clicking the <p> itself collapses it and syncs the chevron', () => {
    const parent = newParent()
    const { p, labelEl } = appendThinkingBlock(parent, Date.now())
    p.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(p.classList.contains('hidden')).toBe(true)
    const svg = labelEl.parentElement!.querySelector('svg')
    expect(svg?.getAttribute('class')).not.toContain('rotate-90')
  })

  it('uses w-6 prefix to align dot+chevron with tool blocks', () => {
    const parent = newParent()
    const { labelEl } = appendThinkingBlock(parent, Date.now())
    const header = labelEl.parentElement!
    const prefix = header.querySelector(':scope > span.shrink-0')
    expect(prefix).not.toBeNull()
    expect(prefix?.className).toContain('w-6')
  })

  it('glyph is ✦ with w-3 and heartbeat class', () => {
    const parent = newParent()
    const { labelEl } = appendThinkingBlock(parent, Date.now())
    const header = labelEl.parentElement!
    const dot = header.querySelector('span.heartbeat')
    expect(dot).not.toBeNull()
    expect(dot?.className).toContain('w-3')
    expect(dot?.textContent).toBe('✦')
  })

  it('p has md-body scope and break-words for markdown rendering', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    expect(p.className).toContain('md-body')
    expect(p.className).toContain('break-words')
  })

  it('header is keyboard-focusable: role=button with tabIndex=0', () => {
    const parent = newParent()
    const { labelEl } = appendThinkingBlock(parent, Date.now())
    const header = labelEl.parentElement!
    expect(header.getAttribute('role')).toBe('button')
    expect(header.tabIndex).toBe(0)
  })

  it('stamps data-thinking=1 on the wrap for grouping walkers', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    // The grouping walkers branch on dataset.thinking truthiness — an empty
    // stamp silently ungroups thinking absorption.
    expect((p.parentElement as HTMLElement).dataset.thinking).toBe('1')
  })
})

describe('finishThinking', () => {
  it('collapses the <p>, drops the heartbeat, and flattens the chevron', () => {
    const parent = newParent()
    const { p, labelEl } = appendThinkingBlock(parent, Date.now())
    finishThinking(p, labelEl, 0)
    expect(p.classList.contains('hidden')).toBe(true)
    expect(labelEl.parentElement!.querySelector('.heartbeat')).toBeNull()
    expect(labelEl.parentElement!.querySelector('svg')?.getAttribute('class')).not.toContain('rotate-90')
  })
})

describe('appendToolBlock', () => {
  it('returns handles with dot in heartbeat state (running, no color class)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expect(h.dot.classList.contains('heartbeat')).toBe(true)
    expect(h.dot.classList.contains('text-white')).toBe(true)
    expect(h.dot.classList.contains('text-green')).toBe(false)
    expect(h.dot.classList.contains('text-red')).toBe(false)
    // name span has the tool name
    const nameEl = h.header.querySelector('.font-mono.text-blue')
    expect(nameEl?.textContent).toBe('Bash')
  })

  it('header splits prefix (dot+chevron) from content block so summary wraps aligned to tool name', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    // header is flex, no wrap — prefix and content side by side
    expect(h.header.className).toContain('flex')
    expect(h.header.className).not.toContain('flex-wrap')
    // prefix (dot+chevron) is shrink-0 so it never compresses
    const prefix = h.header.querySelector(':scope > span.shrink-0')
    expect(prefix).not.toBeNull()
    expect(prefix?.contains(h.dot)).toBe(true)
    // content (name+summary+dur) is a flex-1 min-w-0 block — summary wraps
    // within it, continuation lines align to name position (same as body)
    const content = h.header.querySelector(':scope > span.flex-1')
    expect(content).not.toBeNull()
    const nameEl = content?.querySelector('.font-mono.text-blue')
    expect(nameEl?.textContent).toBe('Bash')
  })

  it('prefix width matches body/children margin for vertical alignment', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    // prefix width must equal body/children left margin so that summary
    // continuation lines, body output, and children all align vertically
    // to the tool name position.
    const prefix = h.header.querySelector(':scope > span.shrink-0') as HTMLElement
    const prefixW = prefix.className.match(/w-(\d+)/)?.[1]
    const bodyMl = h.body.className.match(/ml-(\d+)/)?.[1]
    const childrenMl = h.childrenContainer.className.match(/ml-(\d+)/)?.[1]
    expect(prefixW).toBeTruthy()
    expect(bodyMl).toBe(prefixW)
    expect(childrenMl).toBe(prefixW)
  })

  it('stamps data-tool-root on root and data-tool-children on children container', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expect(h.root.dataset.toolRoot).toBe('1')
    expect(h.childrenContainer.dataset.toolChildren).toBe('1')
  })

  it('header is keyboard-focusable: role=button with tabIndex=0', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expect(h.header.getAttribute('role')).toBe('button')
    expect(h.header.tabIndex).toBe(0)
  })

  it('body and children container default to hidden', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expect(h.body.classList.contains('hidden')).toBe(true)
    expect(h.childrenContainer.classList.contains('hidden')).toBe(true)
  })

  it('appendToolChildrenContainer returns the same element on subsequent calls', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    const first = appendToolChildrenContainer(h)
    const second = appendToolChildrenContainer(h)
    expect(first).toBe(second)
    expect(first).toBe(h.childrenContainer)
  })

  it('defaults to non-collapsible: two plain tools never group', () => {
    const parent = newParent()
    appendToolBlock(parent, 'Bash')
    appendToolBlock(parent, 'Bash')
    expect(parent.querySelector('[data-tool-group]')).toBeNull()
    expect(parent.children.length).toBe(2)
    expect((parent.children[0] as HTMLElement).dataset.toolName).toBe('Bash')
    expect((parent.children[1] as HTMLElement).dataset.toolName).toBe('Bash')
  })
})

describe('setToolSummary', () => {
  it('prefixes non-empty summary with a space', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolSummary(h, 'ls -la')
    expect(h.summaryEl.textContent).toBe(' (ls -la)')
  })

  it('leaves summary span empty for empty input', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolSummary(h, '')
    expect(h.summaryEl.textContent).toBe('')
  })

  it('preserves newlines in multi-line Bash command', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolSummary(h, 'echo hello\necho world')
    // innerHTML should contain a <br> or preserved newline, not collapsed
    expect(h.summaryEl.innerHTML).toContain('\n')
  })

  it('highlights Repl summary as JavaScript', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Repl')
    setToolSummary(h, 'const x = 1', 'Repl')
    // hljs wraps tokens in spans — innerHTML should contain markup
    expect(h.summaryEl.innerHTML).toContain('<')
  })

  it('highlights Bash summary with the bash grammar', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolSummary(h, 'ls -la /tmp', 'Bash')
    expect(h.summaryEl.innerHTML).toContain('<')
  })

  it('Repl summary tokens carry the javascript grammar classes', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Repl')
    setToolSummary(h, 'const x = 1', 'Repl')
    // 'const' is a JS keyword; the bash grammar leaves it as plain text.
    expect(h.summaryEl.innerHTML).toContain('hljs-keyword')
  })

  it('summaries for other tool names stay plain text', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Grep')
    setToolSummary(h, 'const x = 1', 'Grep')
    expect(h.summaryEl.innerHTML).toBe(' (const x = 1)')
  })
})

describe('setToolOutput', () => {
  it('renders output as markdown HTML', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h) // visible body — setToolOutput defers folded bodies
    setToolOutput(h, 'clean output')
    expect(h.body.innerHTML).toContain('clean output')
  })

  it('updates content across successive calls preserving element identity (morph path)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h) // visible body — setToolOutput defers folded bodies
    setToolOutput(h, 'step 1')
    const pBefore = h.body.querySelector('p')
    setToolOutput(h, 'step 1\nstep 2')
    expect(h.body.textContent).toContain('step 2')
    expect(h.body.querySelector('p')).toBe(pBefore)
  })

  it('clears rendered output when output becomes empty', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h) // visible body — setToolOutput defers folded bodies
    setToolOutput(h, 'temporary')
    setToolOutput(h, '')
    expect(h.body.children.length).toBe(0)
  })

  it('defers rendering while the body is folded: stores pending, touches no DOM', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h) // expand so the first frame renders
    setToolOutput(h, 'visible frame')
    toggleToolExpanded(h) // user folds
    const frozen = h.body.innerHTML
    setToolOutput(h, 'hidden frame 1')
    setToolOutput(h, 'hidden frame 2', true)
    expect(h.body.innerHTML).toBe(frozen)
    expect(h.pendingOutput).toBe('hidden frame 2')
    expect(h.pendingSkipHighlight).toBe(true)
  })

  it('pending tracks only the latest frame while folded', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    // Body starts folded (factory default) — a never-expanded running block.
    setToolOutput(h, 'frame 1')
    setToolOutput(h, 'frame 2')
    expect(h.pendingOutput).toBe('frame 2')
    expect(h.body.children.length).toBe(0)
  })

  it('visible render clears pending', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolOutput(h, 'frame 1') // folded → pending
    toggleToolExpanded(h) // expand → flush pending
    setToolOutput(h, 'frame 2') // visible → renders, nothing pending
    expect(h.pendingOutput).toBeNull()
    expect(h.body.textContent).toContain('frame 2')
  })

  it('code blocks in tool output are highlighted by default', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h)
    setToolOutput(h, '```js\nconst x = 1\n```')
    expect(h.body.innerHTML).toContain('hljs-keyword')
  })
})

describe('finishTool', () => {
  it('swaps dot to green and writes duration on success', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    finishTool(h, { isError: false, durationNs: 2_000_000_000, output: 'done' })
    expect(h.dot.classList.contains('heartbeat')).toBe(false)
    expect(h.dot.classList.contains('text-green')).toBe(true)
    expect(h.dot.classList.contains('text-red')).toBe(false)
    expect(h.durEl.textContent).toBe(' 2s')
    expect(h.durEl.classList.contains('text-t3')).toBe(true)
  })

  it('swaps dot to red and prefixes FAIL on error', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    finishTool(h, { isError: true, durationNs: 0, output: '' })
    expect(h.dot.classList.contains('text-red')).toBe(true)
    expect(h.durEl.textContent).toBe(' FAIL · 0s')
    expect(h.durEl.classList.contains('text-red')).toBe(true)
  })

  it('handles durationNs=0 as 0s', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    finishTool(h, { isError: false, durationNs: 0, output: '' })
    expect(h.durEl.textContent).toBe(' 0s')
  })

  it('folds a system-expanded body and defers final output to pending', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    // System auto-expand (first-delta path) unhides the body directly; no
    // user gesture, so userTouched stays false.
    h.body.classList.remove('hidden')
    finishTool(h, { isError: false, durationNs: 0, output: 'final' })
    expect(h.body.classList.contains('hidden')).toBe(true)
    expect(h.body.children.length).toBe(0)
    expect(h.pendingOutput).toBe('final')
    // Chevron must follow the fold.
    expect(h.header.querySelector('svg')?.getAttribute('class')).not.toContain('rotate-90')
  })

  it('leaves a user-touched expanded body expanded and renders into it', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h) // user gesture: userTouched = true, body visible
    finishTool(h, { isError: false, durationNs: 0, output: 'final' })
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.body.textContent).toContain('final')
    expect(h.pendingOutput).toBeNull()
  })

  it('Edit/Write auto-expand on finish renders directly and beats the settle fold', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Edit')
    finishTool(h, { isError: false, durationNs: 0, output: '+ added' })
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.body.textContent).toContain('added')
    expect(h.header.querySelector('svg')?.getAttribute('class')).toContain('rotate-90')
  })

  it('Write finish auto-expands and renders like Edit (deliverable diff)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Write')
    finishTool(h, { isError: false, durationNs: 0, output: 'written' })
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.body.textContent).toContain('written')
    expect(h.header.querySelector('svg')?.getAttribute('class')).toContain('rotate-90')
  })

  it('visible render retires pending: Edit finish after folded deltas leaves no stale flush', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Edit')
    toggleToolExpanded(h) // user opens
    toggleToolExpanded(h) // user folds — later deltas defer
    setToolOutput(h, 'streaming delta')
    expect(h.pendingOutput).toBe('streaming delta')
    // Edit finish forces the body visible and renders the final output; the
    // stale pending must be retired so a later fold+expand never flushes it.
    finishTool(h, { isError: false, durationNs: 0, output: 'final diff' })
    toggleToolExpanded(h) // fold
    toggleToolExpanded(h) // expand
    expect(h.body.textContent).toContain('final diff')
    expect(h.body.textContent).not.toContain('streaming delta')
  })

  it('already-folded body stays folded with output deferred (loadHistory path)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Read')
    finishTool(h, { isError: false, durationNs: 0, output: 'file contents' })
    expect(h.body.classList.contains('hidden')).toBe(true)
    expect(h.body.children.length).toBe(0)
    expect(h.pendingOutput).toBe('file contents')
    toggleToolExpanded(h)
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.body.textContent).toContain('file contents')
  })

  it('empty output on finish defers nothing: pending stays null', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    finishTool(h, { isError: false, durationNs: 0, output: '' })
    expect(h.pendingOutput).toBeNull()
    expect(h.body.children.length).toBe(0)
  })
})

describe('toggleToolExpanded', () => {
  it('flips body and children container hidden state', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    // Default collapsed (body+children hidden for tool — tool body not output yet)
    toggleToolExpanded(h)
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.childrenContainer.classList.contains('hidden')).toBe(false)
    toggleToolExpanded(h)
    expect(h.body.classList.contains('hidden')).toBe(true)
    expect(h.childrenContainer.classList.contains('hidden')).toBe(true)
  })

  it('marks user intent on either direction (userTouched)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h)
    expect(h.userTouched).toBe(true)
    toggleToolExpanded(h)
    expect(h.userTouched).toBe(true)
  })

  it('chevron follows the toggle: rotate-90 expanded, flat when folded', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    toggleToolExpanded(h)
    expect(h.header.querySelector('svg')?.getAttribute('class')).toContain('rotate-90')
    toggleToolExpanded(h)
    expect(h.header.querySelector('svg')?.getAttribute('class')).not.toContain('rotate-90')
  })

  it('clicking the tool body toggles expansion (DOM listener)', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    h.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.userTouched).toBe(true)
    h.body.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(h.body.classList.contains('hidden')).toBe(true)
  })

  it('expanding flushes the pending output exactly once', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolOutput(h, 'streamed frame') // folded → pending
    expect(h.body.children.length).toBe(0)
    toggleToolExpanded(h)
    expect(h.body.classList.contains('hidden')).toBe(false)
    expect(h.body.textContent).toContain('streamed frame')
    expect(h.pendingOutput).toBeNull()
    // A second expand (fold → expand) must not re-render or duplicate: the
    // rendered DOM persists, pending stays empty.
    toggleToolExpanded(h)
    toggleToolExpanded(h)
    expect(h.body.textContent.match(/streamed frame/g)?.length).toBe(1)
  })
})

describe('writeThinkingStream — threshold freeze', () => {
  it('within threshold renders markdown per write', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    writeThinkingStream(p, '**b**')
    expect(p.querySelector('strong')?.textContent).toBe('b')
    expect(p.querySelector('[data-think-tail]')).toBeNull()
  })

  it('past threshold freezes the tree and appends a tail span; later writes touch only the span', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT))
    expect(p.children.length).toBe(1)
    const frozenChild = p.children[0]
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT) + '0123456789')
    expect(p.children.length).toBe(2)
    const span = p.children[1] as HTMLSpanElement
    expect(span.textContent).toBe('0123456789')
    expect(span.classList.contains('whitespace-pre-wrap')).toBe(true)
    expect(p.children[0]).toBe(frozenChild)
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT) + '0123456789XYZ')
    expect(p.children.length).toBe(2)
    expect((p.children[1] as HTMLElement).textContent).toBe('0123456789XYZ')
    expect(p.children[0]).toBe(frozenChild)
  })

  it('writeThinkingText finalize removes the tail and renders the full markdown in place', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    const prefix = 'A'.repeat(THINKING_RENDER_LIMIT)
    writeThinkingStream(p, prefix)
    writeThinkingStream(p, prefix + '0123456789')
    expect(p.querySelector('[data-think-tail]')).not.toBeNull()
    const frozenChild = p.children[0]
    writeThinkingText(p, prefix + '**tail**')
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    expect(p.querySelector('strong')?.textContent).toBe('tail')
    expect(p.children[0]).toBe(frozenChild)
  })

  it('exactly at the limit is not over it: no span until one more char', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT))
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT) + 'x')
    expect(p.querySelector('[data-think-tail]')?.textContent).toBe('x')
  })

  it('exact-limit write on an already-rendered p full-renders (strict >, children > 0)', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    // The fresh-p tests above never evaluate the threshold with children
    // already rendered; grow to LIMIT from below to pin the strict >.
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT - 1))
    expect(p.children.length).toBe(1)
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT))
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    expect(p.dataset.thinkLen).toBe(String(THINKING_RENDER_LIMIT))
    writeThinkingStream(p, 'A'.repeat(THINKING_RENDER_LIMIT) + 'x')
    expect(p.querySelector('[data-think-tail]')?.textContent).toBe('x')
  })

  it('over-limit text on a fresh p renders once in full, then freezes on the next write', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    const big = 'A'.repeat(20000)
    writeThinkingStream(p, big)
    expect(p.children.length).toBe(1)
    expect(p.querySelector('[data-think-tail]')).toBeNull()
    expect(p.dataset.thinkLen).toBe('20000')
    writeThinkingStream(p, big + '0123456789')
    expect(p.querySelector('[data-think-tail]')?.textContent).toBe('0123456789')
  })

  it('writeThinkingText with identical text is idempotent: same structure, same nodes', () => {
    const parent = newParent()
    const { p } = appendThinkingBlock(parent, Date.now())
    writeThinkingText(p, '**one** and `code`')
    const childrenBefore = Array.from(p.children)
    writeThinkingText(p, '**one** and `code`')
    // Identity, not structural equality: toEqual passes with a rebuilt tree
    // of identical shape.
    const childrenAfter = Array.from(p.children)
    expect(childrenAfter.length).toBe(childrenBefore.length)
    for (let i = 0; i < childrenAfter.length; i++) {
      expect(childrenAfter[i]).toBe(childrenBefore[i])
    }
    expect(p.dataset.thinkLen).toBe(String('**one** and `code`'.length))
  })
})

describe('refreshToolDuration', () => {
  it('writes " <elapsed>" into the duration span', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    refreshToolDuration(h, Date.now() - 5000)
    expect(h.durEl.textContent).toBe(' 5s')
  })
})

describe('expandToolChildrenForRunning / collapseToolChildrenOnDone', () => {
  it('running expands and shows rotate-90; done clears children and drops the rotation', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expandToolChildrenForRunning(h)
    const svg = h.header.querySelector('svg')!
    expect(h.childrenContainer.classList.contains('hidden')).toBe(false)
    expect(svg.getAttribute('class')).toContain('rotate-90')
    h.childrenContainer.appendChild(document.createElement('div'))
    collapseToolChildrenOnDone(h)
    expect(h.childrenContainer.children.length).toBe(0)
    expect(h.childrenContainer.classList.contains('hidden')).toBe(true)
    expect(svg.getAttribute('class')).not.toContain('rotate-90')
  })
})

describe('appendProgressBar', () => {
  // appendProgressBar starts the sweep singleton; kill it between tests so no
  // 110ms chain (fake or real) leaks into later tests.
  afterEach(() => {
    stopStatusSweep()
    vi.useRealTimers()
    document.body.replaceChildren()
  })

  it('creates a progress bar with all spans', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    expect(h.root.classList.contains('status-line')).toBe(true)
    expect(h.root.querySelector('.heartbeat')).toBeNull()
    expect(h.elapsedEl.textContent).toBe('0s')
    expect(h.inEl.textContent).toBe('↑0')
    expect(h.outEl.textContent).toBe('↓0')
    expect(h.rateEl.textContent).toBe('0.0 t/s')
    expect(h.toolCountEl.textContent).toBe('')
    // Segment text is span-per-char so the sweep cursor can address it.
    expect(h.inEl.children.length).toBe(2)
    expect(h.inEl.children[0].classList.contains('ch')).toBe(true)
    expect(h.inEl.children[1].classList.contains('ch')).toBe(true)
  })

  it('no heartbeat dot on the status line', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    expect(h.root.querySelector('.heartbeat')).toBeNull()
    expect(h.root.textContent.startsWith('●')).toBe(false)
  })

  it('setProgressBarUsage updates token counts', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 1234, outputTokens: 5678, cacheRead: 0, cacheCreation: 0 })
    expect(h.inEl.textContent).toBe('↑1.2k')
    expect(h.outEl.textContent).toBe('↓5.5k')
  })
  it('setProgressBarUsage folds cacheCreation into the streaming input count', () => {
    // totalInput = input + cacheRead + cacheCreation; streaming shows the
    // full sum so a mutation flipping any operand to '-' drops the count.
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 1000, outputTokens: 0, cacheRead: 0, cacheCreation: 2048 })
    expect(h.inEl.textContent).toBe('↑3.0k')
  })

  it('setProgressBarUsage hides cache during streaming', () => {
    // Streaming progress line should NOT show cache info — TUI only shows
    // cache in AppendStatsLine (finalized). Verify sep-cache is hidden.
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 1000, outputTokens: 500, cacheRead: 800, cacheCreation: 0 })
    // Even with cacheRead>0, cacheEl must be empty during streaming.
    expect(h.cacheEl.textContent).toBe('')
    const sep = h.root.querySelector('.sep-cache') as HTMLElement | null
    expect(sep?.classList.contains('hidden')).toBe(true)
  })

  it('refreshProgressBar updates elapsed, rate, tool count without NaN', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    const startedAt = Date.now() - 5000 // 5s ago
    setProgressBarUsage(h, { inputTokens: 100, outputTokens: 50, cacheRead: 0, cacheCreation: 0 })
    refreshProgressBar(h, startedAt, 3, 50)
    expect(h.elapsedEl.textContent).toBe('5s')
    expect(h.toolCountEl.textContent).toBe('3 tools')
    // rate ≈ 50/5 = 10.0
    expect(h.rateEl.textContent).toBe('10.0 t/s')
  })

  it('refreshProgressBar with all-zero tokens does not produce NaN', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    refreshProgressBar(h, Date.now(), 0, 0)
    expect(h.rateEl.textContent).toBe('0.0 t/s')
  })

  it('finalizeProgressBar stops the sweep and shows final stats', () => {
    vi.useFakeTimers()
    const parent = newParent()
    // Sweep self-stops on detached roots, so the bar must be connected for
    // the liveness assertions to mean anything.
    document.body.appendChild(parent)
    const h = appendProgressBar(parent)
    vi.advanceTimersByTime(110)
    expect(parent.querySelectorAll('.p1').length).toBe(1)
    setProgressBarUsage(h, { inputTokens: 16311, outputTokens: 1854, cacheRead: 4096, cacheCreation: 0 })
    refreshProgressBar(h, Date.now() - 12000, 7, 1854)
    finalizeProgressBar(h, { inputTokens: 16311, outputTokens: 1854, cacheRead: 4096, cacheCreation: 0 }, 12000, 7)
    expect(parent.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
    // totalInput = 16311+4096+0 = 20407 ≈ 19.9k, output = 1854 ≈ 1.8k
    expect(h.inEl.textContent).toContain('19')
    expect(h.outEl.textContent).toContain('1.8')
    expect(h.rateEl.textContent).toContain('t/s')
    expect(h.toolCountEl.textContent).toContain('7 tools')
    // cache: cacheRead=4096, total=20407 → 20.08% → truncated to 20.0%
    expect(h.cacheEl.textContent).toContain('20.0%')
  })

  it('finalizeProgressBar cache percent uses floor, not round', () => {
    // TUI uses integer division (floor). cacheRead=999/total=1000 → 99%, not 100%.
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 1, outputTokens: 0, cacheRead: 999, cacheCreation: 0 }, 1000, 0)
    expect(h.cacheEl.textContent).toBe('99.9% cached')
  })

  it('finalizeProgressBar produces correct stats line format', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 155996, outputTokens: 227, cacheRead: 28672, cacheCreation: 0 })
    finalizeProgressBar(h, { inputTokens: 293, outputTokens: 96, cacheRead: 184640, cacheCreation: 0 }, 63800, 8, 12000)
    const parts: string[] = []
    h.root.childNodes.forEach((n) => {
      const el = n as HTMLElement
      if (el.classList?.contains('hidden')) return
      const t = (el.textContent ?? '').trim()
      if (t) parts.push(t)
    })
    const line = parts.join(' ')
    // Format: ↑180.6k ↓96 · 1.5 t/s · 99.8% cached · 8 tools · 1m 3s
    // Thinking stats are hidden.
    const dot = '\\u00B7'  // ·
    expect(line).toMatch(RegExp('^\\u2191[\\d.]+[kM] \\u2193\\d+ ' + dot + ' [\\d.]+ t/s ' + dot + ' [\\d.]+% cached ' + dot + ' 8 tools ' + dot + ' 1m 3s$'))
    console.log('FINAL:', JSON.stringify(line))
    expect(line).not.toMatch(/^·|·$/)
    expect(line).not.toMatch(/ · · /)
  })

  it('streaming stats line has no cache info', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 155996, outputTokens: 227, cacheRead: 28672, cacheCreation: 0 })
    refreshProgressBar(h, Date.now() - 56000, 8, 494)
    const parts: string[] = []
    h.root.childNodes.forEach((n) => {
      const el = n as HTMLElement
      if (el.classList?.contains('hidden')) return
      const t = (el.textContent ?? '').trim()
      if (t) parts.push(t)
    })
    const line = parts.join(' ')
    // Format: ↑180.3k ↓227 · 8.8 t/s · 8 tools · 56s
    const dot = '\\u00B7'
    expect(line).toMatch(RegExp('^\\u2191[\\d.]+[kM] \\u2193\\d+ ' + dot + ' [\\d.]+ t/s ' + dot + ' 8 tools ' + dot + ' \\d+s$'))
    expect(line).not.toContain('cached')
    expect(line).not.toContain('warmed')
    expect(line).not.toMatch(/ · · /)
  })

  it('finalizeProgressBar computes rate as outputTokens per second', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 0, outputTokens: 100, cacheRead: 0, cacheCreation: 0 }, 10000, 0)
    expect(h.rateEl.textContent).toBe('10.0 t/s')
  })

  it('finalizeProgressBar singular tool count reads "1 tool"', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, 1000, 1)
    expect(h.toolCountEl.textContent).toBe('1 tool')
  })

  it('finalizeProgressBar with no cache activity reads "cache missed"', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 500, outputTokens: 10, cacheRead: 0, cacheCreation: 0 }, 1000, 0)
    expect(h.cacheEl.textContent).toBe('cache missed')
  })

  it('finalizeProgressBar with outputTokens=0 leaves the rate span empty', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 500, outputTokens: 0, cacheRead: 0, cacheCreation: 0 }, 1000, 1)
    expect(h.rateEl.textContent).toBe('')
  })

  it('finalizeProgressBar with only cacheCreation reads "<count> warmed"', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 0, outputTokens: 0, cacheRead: 0, cacheCreation: 2048 }, 1000, 0)
    expect(h.cacheEl.textContent).toBe('2.0k warmed')
  })

  it('finalizeProgressBar mounts hidden scaffolding spans for thinking stats and token suffix', () => {
    const parent = newParent()
    document.body.appendChild(parent)
    const h = appendProgressBar(parent)
    // No writer unhides these today, but they belong to the bar skeleton the
    // sweep walks; a missing or visible span shifts the visible-char stream.
    expect(h.root.contains(h.thinkingEl)).toBe(true)
    expect(h.thinkingEl.classList.contains('hidden')).toBe(true)
    expect(h.root.contains(h.tokensSuffix)).toBe(true)
    expect(h.tokensSuffix.classList.contains('hidden')).toBe(true)
  })

  it('finalizeProgressBar input total counts cacheCreation tokens (1024-based)', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    // total = 1000 + 0 + 2048 = 3048 → 3048/1024 = 2.976 → toFixed(1) '3.0'
    finalizeProgressBar(h, { inputTokens: 1000, outputTokens: 0, cacheRead: 0, cacheCreation: 2048 }, 1000, 0)
    expect(h.inEl.textContent).toBe('↑3.0k')
  })

  it('finalizeProgressBar stops the sweep for good: no highlight reappears on later ticks', () => {
    vi.useFakeTimers()
    const parent = newParent()
    document.body.appendChild(parent)
    const h = appendProgressBar(parent)
    vi.advanceTimersByTime(110)
    finalizeProgressBar(h, { inputTokens: 1, outputTokens: 1, cacheRead: 0, cacheCreation: 0 }, 1000, 1)
    vi.advanceTimersByTime(1200)
    expect(parent.querySelectorAll('.p1, .t1, .t2').length).toBe(0)
  })

  it('finalizeProgressBar with zero elapsed writes an empty rate, never Infinity', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 0, outputTokens: 100, cacheRead: 0, cacheCreation: 0 }, 0, 0)
    expect(h.rateEl.textContent).toBe('')
  })

  it('finalizeProgressBar with zero tools leaves the tool count span empty', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 1, outputTokens: 1, cacheRead: 0, cacheCreation: 0 }, 1000, 0)
    expect(h.toolCountEl.textContent).toBe('')
  })

  it('finalizeProgressBar with zero tools keeps the tools separator hidden', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    finalizeProgressBar(h, { inputTokens: 1, outputTokens: 1, cacheRead: 0, cacheCreation: 0 }, 1000, 0)
    expect(h.root.querySelector('.sep-tools')?.classList.contains('hidden')).toBe(true)
  })
})
