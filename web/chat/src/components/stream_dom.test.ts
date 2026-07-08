import { describe, it, expect } from 'vitest'
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
} from './stream_dom'

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

  it('stamps data-tool-root on root and data-tool-children on children container', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    expect(h.root.dataset.toolRoot).toBe('1')
    expect(h.childrenContainer.dataset.toolChildren).toBe('1')
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
})

describe('setToolOutput', () => {
  it('writes stripped ANSI output as textContent', () => {
    const parent = newParent()
    const h = appendToolBlock(parent, 'Bash')
    setToolOutput(h, '\x1b[32mclean\x1b[0m output')
    expect(h.body.textContent).toBe('clean output')
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
    expect(h.body.textContent).toBe('done')
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
})

describe('appendProgressBar', () => {
  it('creates a progress bar with all spans', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    expect(h.dotEl.classList.contains('heartbeat')).toBe(true)
    expect(h.elapsedEl.textContent).toBe('0s')
    expect(h.inEl.textContent).toBe('↑0')
    expect(h.outEl.textContent).toBe('↓0')
    expect(h.rateEl.textContent).toBe('0.0 t/s')
  })

  it('setProgressBarUsage updates token counts', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 1234, outputTokens: 5678, cacheRead: 0, cacheCreation: 0 })
    expect(h.inEl.textContent).toBe('↑1.2k')
    expect(h.outEl.textContent).toBe('↓5.5k')
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
    expect(sep?.style.display).toBe('none')
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

  it('finalizeProgressBar stops heartbeat and shows final stats', () => {
    const parent = newParent()
    const h = appendProgressBar(parent)
    setProgressBarUsage(h, { inputTokens: 16311, outputTokens: 1854, cacheRead: 4096, cacheCreation: 0 })
    refreshProgressBar(h, Date.now() - 12000, 7, 1854)
    finalizeProgressBar(h, { inputTokens: 16311, outputTokens: 1854, cacheRead: 4096, cacheCreation: 0 }, 12000, 7)
    // Heartbeat stopped
    expect(h.dotEl.classList.contains('heartbeat')).toBe(false)
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
      if (el.style && el.style.display === 'none') return
      const t = (el.textContent ?? '').trim()
      if (t) parts.push(t)
    })
    const line = parts.join(' ')
    // Format: ● ↑180.6k ↓96 · 1.5 t/s · 99% cached · 8 tools · 1m 3s
    const dot = '\\u00B7'  // ·
    expect(line).toMatch(RegExp('^\\u25CF \\u2191[\\d.]+[kM] \\u2193\\d+ ' + dot + ' [\\d.]+ t/s ' + dot + ' [\\d.]+% cached ' + dot + ' 8 tools ' + dot + ' 1m 3s$'))
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
      if (el.style && el.style.display === 'none') return
      const t = (el.textContent ?? '').trim()
      if (t) parts.push(t)
    })
    const line = parts.join(' ')
    // Format: ● ↑180.3k ↓227 · 8.8 t/s · 8 tools · 56s
    const dot = '\\u00B7'
    expect(line).toMatch(RegExp('^\\u25CF \\u2191[\\d.]+[kM] \\u2193\\d+ ' + dot + ' [\\d.]+ t/s ' + dot + ' 8 tools ' + dot + ' \\d+s$'))
    expect(line).not.toContain('cached')
    expect(line).not.toContain('warmed')
    expect(line).not.toMatch(/ · · /)
  })
})
