import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { createHeader } from './header'
import { setLocale } from './i18n'
import type { ContextBreakdownData } from './types'

describe('Header context display', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function getContextTrigger(): HTMLButtonElement {
    return header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
  }

  function getContextText(): string {
    const el = getContextTrigger()
    return el?.textContent ?? ''
  }

  function getContextClass(): string {
    const el = getContextTrigger()
    return el?.className ?? ''
  }

  it('formatTokenCount: raw number under 1K', () => {
    header.setContext(500, 200000)
    expect(getContextText()).toContain('500/')
  })

  it('formatTokenCount: k suffix under 1M', () => {
    header.setContext(28300, 200000)
    expect(getContextText()).toContain('27.6k/')
    expect(getContextText()).toContain('195.3k')
  })

  it('formatTokenCount: M suffix over 1M', () => {
    header.setContext(1048576, 2097152)
    expect(getContextText()).toContain('1.0M/')
    expect(getContextText()).toContain('2.0M')
  })

  it('hides when total is 0', () => {
    header.setContext(100, 0)
    const el = getContextTrigger()
    expect(el.classList.contains('hidden')).toBe(true)
  })

  it('hides when total is negative', () => {
    header.setContext(100, -1)
    const el = getContextTrigger()
    expect(el.classList.contains('hidden')).toBe(true)
  })

  it('normal color under 80%', () => {
    header.setContext(100000, 200000)
    expect(getContextClass()).toContain('text-t2')
    expect(getContextClass()).not.toContain('text-amber')
    expect(getContextClass()).not.toContain('text-red')
  })

  it('amber color at 80%', () => {
    header.setContext(160000, 200000)
    expect(getContextClass()).toContain('text-amber-500')
  })

  it('red color at 90%', () => {
    header.setContext(180000, 200000)
    expect(getContextClass()).toContain('text-red-500')
  })

  it('updates on repeated calls', () => {
    header.setContext(10000, 200000)
    expect(getContextText()).toContain('9.8k/')
    header.setContext(50000, 200000)
    expect(getContextText()).toContain('48.8k/')
  })
})

function sampleBreakdown(overrides: Partial<ContextBreakdownData> = {}): ContextBreakdownData {
  return {
    model: 'test-model',
    contextWindow: 200000,
    totalTokens: 50000,
    percentage: 25.0,
    isAutoCompact: true,
    categories: [
      { name: 'System prompt', tokens: 5000, percentage: 2.5, color: '12', isFree: false, isReserved: false },
      { name: 'Messages', tokens: 30000, percentage: 15.0, color: '255', isFree: false, isReserved: false },
      { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
    ],
    mcpToolsLoaded: [],
    mcpToolsDeferred: [],
    deferredBuiltinTools: [],
    systemTools: [],
    agents: [],
    skills: [],
    messageBreakdown: null,
    ...overrides,
  }
}

describe('Header context popover', () => {
  let header: ReturnType<typeof createHeader>
  let requestCalled: boolean

  beforeEach(() => {
    document.body.innerHTML = ''
    requestCalled = false
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onContextRequest: () => { requestCalled = true },
    })
    document.body.appendChild(header.root)
  })

  function getTrigger(): HTMLButtonElement {
    return header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
  }

  function getPanel(): HTMLDivElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLDivElement
    }
    return null
  }

  function clickTrigger() {
    const el = getTrigger()
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  it('calls onContextRequest when trigger clicked', () => {
    header.setContext(500, 200000)
    clickTrigger()
    expect(requestCalled).toBe(true)
  })

  it('shows loading text when no data arrived yet', () => {
    header.setContext(500, 200000)
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()
    expect(panel!.textContent).toContain('Loading')
  })

  it('shows send-a-message hint when setContextBreakdown(null)', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(null)
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()
    expect(panel!.textContent).toContain('Send a message first')
  })

  it('renders breakdown content after setContextBreakdown + click', () => {
    header.setContext(500, 200000)
    const bd = sampleBreakdown()
    header.setContextBreakdown(bd)
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()
    expect(panel!.textContent).toContain('Context Usage')
    expect(panel!.textContent).toContain('System prompt')
    expect(panel!.textContent).toContain('Messages')
    expect(panel!.textContent).toContain('Free space')
  })

  it('segmented bar has segments with correct colors', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    const bar = panel!.querySelector('[data-testid="context-bar"]') as HTMLDivElement
    expect(bar).not.toBeNull()
    const segments = bar.querySelectorAll('[data-testid="context-segment"]')
    expect(segments.length).toBe(3)
    const seg0 = segments[0] as HTMLDivElement
    expect(seg0.style.backgroundColor).toBe('var(--color-blue)')
    const seg1 = segments[1] as HTMLDivElement
    expect(seg1.style.backgroundColor).toBe('var(--color-t1)')
    const seg2 = segments[2] as HTMLDivElement
    expect(seg2.style.backgroundColor).toBe('var(--color-t3)')
  })

  it('segment width is percentage based', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    const segments = panel!.querySelectorAll('[data-testid="context-segment"]')
    const seg0 = segments[0] as HTMLDivElement
    expect(seg0.style.width).toBe('2.5%')
    const seg1 = segments[1] as HTMLDivElement
    expect(seg1.style.width).toBe('15%')
  })

  it('segmented bar maps all ANSI color codes to CSS variables', () => {
    const codes: Record<string, string> = {
      '12': 'var(--color-blue)', '39': '#38BDF8', '33': '#22D3EE',
      '51': '#06B6D4', '201': 'var(--color-violet)', '220': 'var(--color-amber)',
      '45': '#14B8A6', '46': 'var(--color-green)', '22': '#15803D',
      '93': 'var(--color-violet)', '255': 'var(--color-t1)', '240': 'var(--color-t3)',
      '160': 'var(--color-red)',
    }
    const cats = Object.entries(codes).map(([code], i) => ({
      name: `Cat${i}`, tokens: 1000, percentage: 5.0, color: code,
      isFree: false, isReserved: false,
    }))
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({ categories: cats }))
    clickTrigger()
    const panel = getPanel()
    const segments = panel!.querySelectorAll('[data-testid="context-segment"]')
    for (let i = 0; i < cats.length; i++) {
      const seg = segments[i] as HTMLDivElement
      const expected = codes[cats[i].color]
      if (expected.startsWith('var(')) {
        expect(seg.style.backgroundColor).toBe(expected)
      } else if (expected.startsWith('#')) {
        // jsdom converts hex to rgb
        expect(seg.style.backgroundColor).toMatch(/rgb/i)
      }
    }
  })

  it('category list shows formatted token count and percentage', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    const text = panel!.textContent ?? ''
    expect(text).toContain('System prompt')
    expect(text).toContain('4.9k')
    expect(text).toContain('2.5%')
    expect(text).toContain('Messages')
    expect(text).toContain('29.3k')
    expect(text).toContain('Free space')
  })

  it('detail sections appear only when non-empty', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemTools: [{ name: 'grep', tokens: 200, text: 'grep\n\nP\n\n{}' }],
      agents: [],
      skills: [],
      mcpToolsLoaded: [],
    }))
    clickTrigger()
    const panel = getPanel()
    expect(panel!.textContent).toContain('System tools')
    // EAGER pre-render: the grep segment row exists inside the hidden
    // segments container, not in a visible detail section.
    const segs = panel!.querySelector('[data-testid="ctx-cat-segments"][data-cat="system_tools"]')
    expect(segs).not.toBeNull()
    expect(segs!.classList.contains('hidden')).toBe(true)
    expect(panel!.textContent).not.toContain('Agents')
    expect(panel!.textContent).not.toContain('Skills')
    expect(panel!.textContent).not.toContain('MCP tools loaded')
  })

  it('message breakdown renders token summary', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      messageBreakdown: {
        toolCallTokens: 5000,
        toolResultTokens: 10000,
        attachmentTokens: 0,
        assistantTextTokens: 2000,
        userTextTokens: 1000,
        toolCallsByType: [
          { name: 'Bash', callTokens: 3000, resultTokens: 6000 },
          { name: 'Read', callTokens: 2000, resultTokens: 4000 },
        ],
        attachmentsByType: [],
      },
    }))
    clickTrigger()
    const panel = getPanel()
    expect(panel!.textContent).toContain('Message breakdown')
    expect(panel!.textContent).toContain('Tool calls')
    expect(panel!.textContent).toContain('Tool results')
    expect(panel!.textContent).toContain('Top tools')
    expect(panel!.textContent).toContain('Bash')
    expect(panel!.textContent).toContain('Read')
  })

  it('closes on outside click', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()

    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('can open and close multiple times via outside click', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())

    // First open — simulate real click sequence
    clickTrigger()
    const panel = getPanel()
    expect(panel!.classList.contains('hidden')).toBe(false)

    // First close — mousedown
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)

    // Second open
    clickTrigger()
    expect(panel!.classList.contains('hidden')).toBe(false)

    // Second close — must still work
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('toggles closed on second trigger click', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()
    clickTrigger()
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('updates content when setContextBreakdown called while open', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    expect(panel!.textContent).toContain('System prompt')

    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'Messages', tokens: 40000, percentage: 20.0, color: '255', isFree: false, isReserved: false },
      ],
    }))
    expect(panel!.textContent).not.toContain('System prompt')
    expect(panel!.textContent).toContain('Messages')
  })

  it('hideContextBreakdown closes the panel', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    expect(panel).not.toBeNull()
    header.hideContextBreakdown()
    expect(panel!.classList.contains('hidden')).toBe(true)
  })
})

describe('Header context popover i18n', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    setLocale('zh')
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onContextCompact: () => {},
    })
    document.body.appendChild(header.root)
  })

  afterEach(() => {
    setLocale('en')
  })

  function getPanel(): HTMLDivElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLDivElement
    }
    return null
  }

  function clickTrigger() {
    const el = header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  it('renders title and compact button in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    clickTrigger()
    const panel = getPanel()
    const text = panel!.textContent ?? ''
    expect(text).toContain('上下文用量')
    expect(text).not.toContain('Context Usage')
    const btn = panel!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement
    expect(btn.textContent).toBe('压缩')
  })

  it('renders loading placeholder in zh', () => {
    header.setContext(500, 200000)
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('加载中…')
    expect(text).not.toContain('Loading')
  })

  it('renders empty-state hint in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(null)
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('先发一条消息即可查看上下文用量')
    expect(text).not.toContain('Send a message first')
  })

  it('renders message breakdown rows and top tools in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      messageBreakdown: {
        toolCallTokens: 5000,
        toolResultTokens: 10000,
        attachmentTokens: 0,
        assistantTextTokens: 2000,
        userTextTokens: 1000,
        toolCallsByType: [{ name: 'Bash', callTokens: 3000, resultTokens: 6000 }],
        attachmentsByType: [],
      },
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('消息明细')
    expect(text).toContain('工具调用')
    expect(text).toContain('工具结果')
    expect(text).toContain('附件')
    expect(text).toContain('助手文本')
    expect(text).toContain('用户文本')
    expect(text).toContain('工具用量')
    expect(text).toContain('Bash')
  })

  it('renders category rows translated from semantic ids in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 3000, percentage: 1.5, color: '45', isFree: false, isReserved: false },
        { name: 'Memory files', id: 'memory_files', tokens: 9500, percentage: 4.8, color: '220', isFree: false, isReserved: false },
        { name: 'Free space', id: 'free_space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
        { name: 'Autocompact buffer', id: 'autocompact_buffer', tokens: 14000, percentage: 7.0, color: '160', isFree: false, isReserved: true },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('系统工具')
    // 2026-11-15: the user kept the "记忆文件" label — the bucket holds
    // behavioral instructions + index, but the familiar name won.
    expect(text).toContain('记忆文件')
    expect(text).toContain('剩余空间')
    expect(text).toContain('自动压缩缓冲区')
    expect(text).not.toContain('System tools')
    expect(text).not.toContain('Free space')
    expect(text).not.toContain('Autocompact buffer')
  })

  it('falls back to the raw name for unknown or absent ids', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'Future category', id: 'future_thing', tokens: 3000, percentage: 1.5, color: '39', isFree: false, isReserved: false },
        { name: 'Legacy server', tokens: 3000, percentage: 1.5, color: '51', isFree: false, isReserved: false },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('Future category')
    expect(text).toContain('Legacy server')
  })

  it('system prompt sub-rows do not render: the categories list already covers them', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 300, percentage: 0.2, color: '45', isFree: false, isReserved: false },
        { name: 'MCP tools', id: 'mcp_tools', tokens: 1500, percentage: 0.8, color: '46', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    // No dedicated base-prompt section exists: its content rides in the
    // "System prompt" category row.
    expect(text).not.toContain('基础提示词')
    expect(text).not.toContain('Base prompt')
    expect(text).toContain('系统工具')
    expect(text).toContain('MCP 工具')
  })

  it('base segment pre-renders inside the hidden accordion container when sections ride the payload', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 300, percentage: 0.2, color: '45', isFree: false, isReserved: false },
        { name: 'System prompt', id: 'system_prompt', tokens: 3000, percentage: 1.5, color: '12', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemPromptSections: [{ id: 'base', text: 'BASE', tokens: 300, source: 'builtin' }],
    }))
    clickTrigger()
    const panel = getPanel()!
    const segs = panel.querySelector('[data-testid="ctx-cat-segments"][data-cat="system_prompt"]')
    expect(segs).not.toBeNull()
    // Pre-rendered but collapsed — visibility is the container's hidden
    // class, not textContent.
    expect(segs!.classList.contains('hidden')).toBe(true)
    const segRow = segs!.querySelector('[data-testid="ctx-segment-row"][data-seg="base"]')
    expect(segRow).not.toBeNull()
    expect(segRow!.textContent).toContain('基础提示词')
  })

  it('renders section titles in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemTools: [{ name: 'grep', tokens: 200, text: 'grep\n\nP\n\n{}' }],
      mcpToolsLoaded: [{ name: 'search', serverName: 'ddg', tokens: 300, isLoaded: true }],
      mcpToolsDeferred: [{ name: 'fetch', serverName: 'ddg', tokens: 0, isLoaded: false }],
      agents: [{ agentType: 'general', source: 'builtin', tokens: 400 }],
      skills: [{ name: 'pdf', source: 'builtin', tokens: 500 }],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    // '系统工具' now comes from the category row label (ctxCatSystemTools),
    // the retired detail section's own key no longer renders a second copy.
    expect(text).toContain('系统工具')
    expect(text).toContain('已加载 MCP 工具')
    expect(text).toContain('延迟加载 MCP 工具')
    expect(text).toContain('子代理')
    expect(text).toContain('技能')
  })
})

describe('Header model picker popover', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  // breadcrumb order: [enginePicker] [sep] [modelPicker]. The context popover
  // trigger also matches button.text-\\[14px\\] but starts hidden, so we
  // filter it out. Model picker is the LAST visible match.
  function modelTrigger(): HTMLButtonElement {
    const triggers = header.root.querySelectorAll('button.text-\\[15px\\]:not(.hidden)')
    return triggers[triggers.length - 1] as HTMLButtonElement
  }

  function visibleModelPanel(): HTMLDivElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      const hasModelSearch = !!p.querySelector('textarea[placeholder="Search..."]')
      if (hasModelSearch && !p.classList.contains('hidden')) return p as HTMLDivElement
    }
    return null
  }

  it('click trigger opens panel with search input', () => {
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleModelPanel()
    expect(panel).not.toBeNull()
    expect(panel!.querySelector('textarea[placeholder="Search..."]')).not.toBeNull()
  })

  it('opening the panel does not steal focus for the search input', () => {
    // Auto-focusing pops the soft keyboard over the popup on Android.
    // The removed autofocus ran in a 50ms setTimeout — fake the timers and
    // advance past it, asserting focus never lands on the search box.
    vi.useFakeTimers()
    try {
      modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
      expect(visibleModelPanel()).not.toBeNull()
      const search = visibleModelPanel()!.querySelector('textarea[placeholder="Search..."]') as HTMLElement
      const focusSpy = vi.spyOn(search, 'focus')
      vi.advanceTimersByTime(60)
      expect(focusSpy).not.toHaveBeenCalled()
      expect(document.activeElement).not.toBe(search)
    } finally {
      vi.useRealTimers()
    }
  })

  it('outside click closes panel', () => {
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleModelPanel()
    expect(panel).not.toBeNull()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('reopen after close re-appends without duplicates', () => {
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleModelPanel()
    expect(panel).not.toBeNull()
    // Only one model-picker panel in the DOM.
    let count = 0
    for (const p of document.body.querySelectorAll('.modal-enter')) {
      if (p.querySelector('textarea[placeholder="Search..."]')) count++
    }
    expect(count).toBe(1)
  })
})

describe('Header engine picker popover', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  // enginePicker trigger is the FIRST visible button.mono.text-\\[14px\\].
  function engineTrigger(): HTMLButtonElement {
    return header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
  }

  function visibleEnginePanel(): HTMLDivElement | null {
    const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) {
        return p as HTMLDivElement
      }
    }
    return null
  }

  it('click trigger opens panel', () => {
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleEnginePanel()
    expect(panel).not.toBeNull()
  })

  it('outside click closes panel', () => {
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleEnginePanel()
    expect(panel).not.toBeNull()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel!.classList.contains('hidden')).toBe(true)
  })

  it('reopen after close re-appends without duplicates', () => {
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panel = visibleEnginePanel()
    expect(panel).not.toBeNull()
    // Only one engine-picker panel in the DOM (PopupHost guards re-append).
    let count = 0
    for (const p of document.body.querySelectorAll('.modal-enter')) {
      const hasModelSearch = !!p.querySelector('textarea[placeholder="Search..."]')
      const hasContext = !!(p as HTMLElement).dataset && (p as HTMLElement).dataset.testid === 'context-panel'
      if (!hasModelSearch && !hasContext) count++
    }
    expect(count).toBe(1)
    // Panel body is a single listContainer (no duplicate children stacked).
    expect(panel!.children.length).toBe(1)
  })
})

describe('Header Compact button', () => {
  let header: ReturnType<typeof createHeader>

  function sampleBreakdown(percentage = 25): ContextBreakdownData {
    return {
      model: 'test',
      contextWindow: 200000,
      totalTokens: 50000,
      percentage,
      isAutoCompact: false,
      categories: [
        { name: 'Messages', tokens: 50000, percentage: 25, color: '255', isFree: false, isReserved: false },
      ],
      mcpToolsLoaded: [],
      mcpToolsDeferred: [],
      deferredBuiltinTools: [],
      systemTools: [],
      agents: [],
      skills: [],
    }
  }

  function getPanel(): HTMLElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLElement
    }
    return null
  }

  function openPopover() {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    const trigger = header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    trigger.click()
  }

  beforeEach(() => {
    document.body.innerHTML = ''
  })

  it('not rendered when onContextCompact not provided', () => {
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    openPopover()
    const btn = getPanel()!.querySelector('[data-testid="compact-btn"]')
    expect(btn).toBeNull()
  })

  it('blue and clickable when not streaming', () => {
    let clicked = false
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onContextCompact: () => { clicked = true },
    })
    document.body.appendChild(header.root)
    openPopover()

    const btn = getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement
    expect(btn).not.toBeNull()
    expect(btn.className).toContain('text-blue')
    expect(btn.className).not.toContain('pointer-events-none')

    btn.click()
    expect(clicked).toBe(true)
  })
  it('escalates the compact button on the header meter thresholds', () => {
    const toneAt = (pct: number) => {
      document.body.innerHTML = ''
      const h = createHeader({
        onModelSelect: () => {},
        onEngineSwitch: () => {},
        onEngineNew: () => {},
        onContextCompact: () => {},
      })
      document.body.appendChild(h.root)
      h.setContext(500, 200000)
      h.setContextBreakdown(sampleBreakdown(pct))
      ;(h.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement).click()
      return (getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement).className
    }
    expect(toneAt(79.9)).toContain('text-blue')
    expect(toneAt(80)).toContain('text-amber-500')
    expect(toneAt(80)).not.toContain('text-blue')
    expect(toneAt(90)).toContain('text-red-500')
    expect(toneAt(90)).not.toContain('text-amber-500')
  })

  it('compact button class ends with the composed base+tone, no idle suffix', () => {
    const toneAt = (pct: number) => {
      document.body.innerHTML = ''
      const h = createHeader({
        onModelSelect: () => {},
        onEngineSwitch: () => {},
        onEngineNew: () => {},
        onContextCompact: () => {},
      })
      document.body.appendChild(h.root)
      h.setContext(500, 200000)
      h.setContextBreakdown(sampleBreakdown(pct))
      ;(h.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement).click()
      return (getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement).className
    }
    // textButtonRecipe prefixes its own classes; the header's contribution is
    // a contiguous base+tone suffix with nothing appended in the idle state.
    expect(toneAt(79.9).endsWith('text-[13px] shrink-0 rounded-md border px-2 py-px text-blue border-blue/60 hover:bg-blue/10')).toBe(true)
    expect(toneAt(80).endsWith('text-[13px] shrink-0 rounded-md border px-2 py-px text-amber-500 border-amber-500/60 hover:bg-amber-500/10')).toBe(true)
    expect(toneAt(90).endsWith('text-[13px] shrink-0 rounded-md border px-2 py-px text-red-500 border-red-500/60 hover:bg-red-500/10')).toBe(true)
  })

  it('grey and disabled when streaming', () => {
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onContextCompact: () => {},
    })
    document.body.appendChild(header.root)
    openPopover()

    header.setStreaming(true)

    const btn = getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement
    expect(btn.className).toContain('text-t3')
    // ink3 is the panel's own background colour in the dark theme, so a border
    // derived from it renders invisible there — the border needs a text token.
    expect(btn.className).toContain('border-t3/60')
    expect(btn.className).not.toContain('border-ink3')
    expect(btn.className).toContain('pointer-events-none')
  })

  it('updates from disabled to enabled when streaming ends', () => {
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onContextCompact: () => {},
    })
    document.body.appendChild(header.root)
    openPopover()
    header.setStreaming(true)

    let btn = getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement
    expect(btn.className).toContain('pointer-events-none')

    header.setStreaming(false)

    btn = getPanel()!.querySelector('[data-testid="compact-btn"]') as HTMLButtonElement
    expect(btn.className).toContain('text-blue')
    expect(btn.className).not.toContain('pointer-events-none')
  })
})

describe('Header picker streaming state', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function modelTrigger(): HTMLButtonElement {
    const triggers = header.root.querySelectorAll('button.text-\\[15px\\]:not(.hidden)')
    return triggers[triggers.length - 1] as HTMLButtonElement
  }

  function engineTrigger(): HTMLButtonElement {
    return header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
  }

  function visibleModelPanel(): HTMLElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      const hasModelSearch = !!p.querySelector('textarea[placeholder="Search..."]')
      if (hasModelSearch && !p.classList.contains('hidden')) return p as HTMLElement
    }
    return null
  }

  function visibleEnginePanel(): HTMLElement | null {
    const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLElement
    }
    return null
  }

  it('model picker items greyed out when streaming', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    header.setStreaming(true)

    const panel = visibleModelPanel()!
    const items = panel.querySelectorAll('button')
    for (const item of items) {
      if (!item.querySelector('textarea')) {
        expect(item.className).toContain('pointer-events-none')
      }
    }
  })

  it('engine picker items remain interactive when streaming', () => {
    header.setEngines(
      [{ id: 'main', name: 'Main', model: 'gpt-5' }, { id: 'second', name: 'Second', model: 'claude' }],
      'main',
    )
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    header.setStreaming(true)

    const panel = visibleEnginePanel()!
    const items = panel.querySelectorAll('button')
    let foundNonActive = false
    for (const item of items) {
      // Engine switch is a pointer swap, not a query — all items stay
      // interactive during streaming.
      if (item.textContent?.includes('Second')) {
        expect(item.className).not.toContain('pointer-events-none')
        foundNonActive = true
      }
    }
    expect(foundNonActive).toBe(true)
  })

  it('model picker items restored when streaming ends', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    header.setStreaming(true)
    header.setStreaming(false)

    const panel = visibleModelPanel()!
    const items = panel.querySelectorAll('button')
    for (const item of items) {
      if (!item.querySelector('textarea')) {
        expect(item.className).not.toContain('pointer-events-none')
      }
    }
  })
})

describe('Header model picker list', () => {
  let header: ReturnType<typeof createHeader>
  let selected: { provider: string; model: string } | null

  beforeEach(() => {
    document.body.innerHTML = ''
    selected = null
    header = createHeader({
      onModelSelect: (provider, model) => { selected = { provider, model } },
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function modelTrigger(): HTMLButtonElement {
    const triggers = header.root.querySelectorAll('button.text-\\[15px\\]:not(.hidden)')
    return triggers[triggers.length - 1] as HTMLButtonElement
  }

  function openPanel(): HTMLElement {
    modelTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (p.querySelector('textarea') && !p.classList.contains('hidden')) return p as HTMLElement
    }
    throw new Error('model picker panel not open')
  }

  function list(panel: HTMLElement): HTMLElement {
    return panel.children[1] as HTMLElement
  }

  function rowTexts(panel: HTMLElement): string[] {
    return Array.from(list(panel).children).map((c) => c.textContent ?? '')
  }

  function type(panel: HTMLElement, query: string): void {
    const box = panel.querySelector('textarea') as HTMLTextAreaElement
    box.value = query
    box.dispatchEvent(new Event('input', { bubbles: true }))
  }

  function twoOpenAIModels(): void {
    header.setModels([
      { provider: 'openai', model: 'gpt-5' },
      { provider: 'openai', model: 'o3' },
    ], 'openai', 'gpt-5')
  }

  it('lists every model with one group header per provider, in order', () => {
    header.setModels([
      { provider: 'openai', model: 'gpt-5' },
      { provider: 'openai', model: 'o3' },
      { provider: 'anthropic', model: 'claude' },
    ], 'openai', 'gpt-5')
    const panel = openPanel()
    const children = list(panel).children
    expect(children.length).toBe(5)
    expect(children[0].textContent).toBe('openai')
    expect(children[0].className).toBe('px-3 pt-2 pb-1 mono text-[13px] text-t3 uppercase tracking-wider')
    expect(children[3].textContent).toBe('anthropic')
  })

  it('narrows the list to models matching the query', () => {
    twoOpenAIModels()
    const panel = openPanel()
    type(panel, 'gpt')
    expect(rowTexts(panel)).toEqual(['openai', 'gpt-5'])
  })

  it('matches the query case-insensitively', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    const panel = openPanel()
    type(panel, 'GPT')
    expect(rowTexts(panel)).toEqual(['openai', 'gpt-5'])
  })

  it('joins provider and model with a slash for matching', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    const panel = openPanel()
    // 'ai/g' spans the provider/model boundary; without the literal slash in
    // the haystack this subsequence cannot match.
    type(panel, 'ai/g')
    expect(rowTexts(panel)).toEqual(['openai', 'gpt-5'])
    type(panel, 'gpt-5/x')
    expect(rowTexts(panel)).toEqual(['No models found'])
  })

  it('trims whitespace from the query before matching', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    const panel = openPanel()
    type(panel, '  gpt-5  ')
    expect(rowTexts(panel)).toEqual(['openai', 'gpt-5'])
  })

  it('shows the no-models row when the list is empty', () => {
    const panel = openPanel()
    const row = list(panel).children[0] as HTMLElement
    expect(row.className).toBe('px-3 py-4 text-center text-[13px] text-t3')
    expect(row.textContent).toBe('No models found')
  })

  it('marks the current model active and the others inactive', () => {
    twoOpenAIModels()
    const panel = openPanel()
    const active = list(panel).children[1] as HTMLElement
    const other = list(panel).children[2] as HTMLElement
    expect(active.className).toBe('w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50')
    expect(active.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-blue')
    expect(active.children[1].className).toBe('text-[13px] text-blue')
    expect(active.children[1].textContent).toBe('gpt-5')
    expect(other.className).toBe('w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50')
    expect(other.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-t3/30')
    expect(other.children[1].className).toBe('text-[13px] text-t2')
  })

  it('the same model name under another provider is NOT active', () => {
    // provider+model is the identity: azure/gpt-5 must not light up openai/gpt-5
    header.setModels([
      { provider: 'openai', model: 'gpt-5' },
      { provider: 'azure', model: 'gpt-5' },
    ], 'azure', 'gpt-5')
    const panel = openPanel()
    // two providers → two group headers: [hdr openai, item, hdr azure, item]
    const openai = list(panel).children[1] as HTMLElement
    const azure = list(panel).children[3] as HTMLElement
    expect(openai.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-t3/30')
    expect(openai.children[1].className).toBe('text-[13px] text-t2')
    expect(azure.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-blue')
    expect(azure.children[1].className).toBe('text-[13px] text-blue')
  })

  it('dims non-active model labels while streaming', () => {
    twoOpenAIModels()
    const panel = openPanel()
    header.setStreaming(true)
    const other = list(panel).children[2] as HTMLElement
    expect(other.className).toBe('w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors pointer-events-none')
    expect(other.children[1].className).toBe('text-[13px] text-t3')
  })

  it('shows a freshly fetched quota on the open list without reopening', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    const panel = openPanel()
    header.setQuota('openai', '42/50')
    const item = list(panel).children[1] as HTMLElement
    expect(item.children.length).toBe(3)
    const q = item.children[2] as HTMLElement
    expect(q.className).toBe('text-[10px] text-t3 ml-auto')
    expect(q.textContent).toBe('42/50')
  })

  it('selecting a model updates the trigger label and closes the panel', () => {
    twoOpenAIModels()
    const panel = openPanel()
    ;(list(panel).children[2] as HTMLElement).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(selected).toEqual({ provider: 'openai', model: 'o3' })
    expect(modelTrigger().textContent).toBe('o3')
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('rebuilds the list from scratch between opens (no stale rows)', () => {
    header.setModels([{ provider: 'openai', model: 'gpt-5' }], 'openai', 'gpt-5')
    let panel = openPanel()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
    header.setModels([{ provider: 'anthropic', model: 'claude' }], 'anthropic', 'claude')
    panel = openPanel()
    const container = list(panel)
    expect(container.firstChild).toBe(container.firstElementChild)
    expect(container.children.length).toBe(2)
    expect(container.children[0].textContent).toBe('anthropic')
    expect(container.textContent).not.toContain('gpt-5')
  })
})

describe('Header engine picker list', () => {
  let header: ReturnType<typeof createHeader>
  let switched: string | null
  let newed: boolean

  beforeEach(() => {
    document.body.innerHTML = ''
    switched = null
    newed = false
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: (id) => { switched = id },
      onEngineNew: () => { newed = true },
    })
    document.body.appendChild(header.root)
  })

  function engineTrigger(): HTMLButtonElement {
    return header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
  }

  function openPanel(): HTMLElement {
    engineTrigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLElement
    }
    throw new Error('engine picker panel not open')
  }

  function list(panel: HTMLElement): HTMLElement {
    return panel.children[0] as HTMLElement
  }

  function setTwo(): void {
    header.setEngines(
      [{ id: 'e1', name: 'Main', model: 'm1' }, { id: 'e2', name: 'Second', model: 'm2' }],
      'e1',
    )
  }

  it('renders each engine row with its dot, name, and model', () => {
    setTwo()
    const panel = openPanel()
    const rows = list(panel).children
    expect(rows.length).toBe(3) // two engines + the new-engine footer
    const active = rows[0] as HTMLElement
    expect(active.className).toBe('w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50')
    expect(active.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-blue')
    expect(active.children[1].className).toBe('text-[13px] text-blue')
    expect(active.children[1].textContent).toBe('Main')
    expect(active.children[2].className).toBe('text-[13px] text-t3 ml-2')
    expect(active.children[2].textContent).toBe('m1')
    const other = rows[1] as HTMLElement
    expect(other.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-t3/30')
    expect(other.children[1].className).toBe('text-[13px] text-t2')
  })

  it('the trigger label names the ACTIVE engine, not the first listed', () => {
    header.setEngines(
      [{ id: 'e1', name: 'Main', model: 'm1' }, { id: 'e2', name: 'Second', model: 'm2' }],
      'e2',
    )
    expect(engineTrigger().textContent).toBe('Second')
  })

  it('clicking the active engine just closes the panel without switching', () => {
    setTwo()
    const panel = openPanel()
    ;(list(panel).children[0] as HTMLElement).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(switched).toBeNull()
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('clicking another engine switches to it and closes the panel', () => {
    setTwo()
    const panel = openPanel()
    ;(list(panel).children[1] as HTMLElement).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(switched).toBe('e2')
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('the footer is the right-cluster toolbar: bare 16px +, no − without delete wiring', () => {
    setTwo()
    const panel = openPanel()
    const footer = list(panel).children[2] as HTMLElement
    // Layout C container: right cluster via justify-end + gap, no spacers.
    expect(footer.className).toBe('flex items-center justify-end gap-0.5 border-t border-hairline mt-1 px-1 pt-1')
    const btns = footer.querySelectorAll('button')
    // onEngineDelete is not wired in this mount — the − toggle stays hidden
    // and the + sits alone at the right edge.
    expect(btns.length).toBe(1)
    const plus = btns[0] as HTMLButtonElement
    expect(plus.getAttribute('aria-label')).toBe('New engine')
    expect(plus.querySelector('svg')!.getAttribute('width')).toBe('16')
    expect(plus.className).not.toContain('w-full')
    expect(plus.textContent).toBe('')
  })

  it('the + button starts a new engine and closes the panel', () => {
    setTwo()
    const panel = openPanel()
    const plus = (list(panel).children[2] as HTMLElement).querySelector(
      'button[aria-label="New engine"]',
    ) as HTMLButtonElement
    plus.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(newed).toBe(true)
    expect(panel.classList.contains('hidden')).toBe(true)
  })

  it('rebuilds the list between opens (no stale rows)', () => {
    setTwo()
    let panel = openPanel()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
    header.setEngines([{ id: 'e9', name: 'Ninth', model: 'm9' }], 'e9')
    panel = openPanel()
    const container = list(panel)
    expect(container.firstChild).toBe(container.firstElementChild)
    expect(container.children.length).toBe(2) // one engine + footer
    expect(container.textContent).not.toContain('Main')
  })
})

describe('Header context breakdown structure', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function openWith(bd: ContextBreakdownData | null): HTMLElement {
    header.setContext(500, 200000)
    header.setContextBreakdown(bd)
    const trigger = header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden') && !p.querySelector('textarea')) return p as HTMLElement
    }
    throw new Error('context panel not open')
  }

  // Detail rows sit directly under their section heading; the name span is
  // the only child carrying the truncate recipe.
  function findDetailRow(panel: HTMLElement, name: string): HTMLElement | null {
    for (const child of Array.from(panel.children)) {
      const first = (child as HTMLElement).children[0] as HTMLElement | undefined
      if (first && first.className === 'text-[13px] text-t2 truncate' && first.textContent === name) {
        return child as HTMLElement
      }
    }
    return null
  }

  it('pins the sticky header block: title row, total line, meter wrap', () => {
    const panel = openWith(sampleBreakdown())
    expect(panel.firstChild).toBe(panel.firstElementChild)
    const head = panel.children[0] as HTMLElement
    expect(head.className).toBe('sticky top-0 z-10 card-bg')
    expect(head.children.length).toBe(3)
    const titleRow = head.children[0] as HTMLElement
    expect(titleRow.className).toBe('flex items-center justify-between gap-3 px-4 pt-3')
    expect(titleRow.children[0].className).toBe('text-[14px] font-semibold text-t1')
    expect(titleRow.children[0].textContent).toBe('Context Usage')
    const total = head.children[1] as HTMLElement
    expect(total.className).toBe('mono text-[12px] text-t3 px-4 pt-0.5')
    expect(total.textContent).toBe('48.8k / 195.3k (25.0%)')
    const barWrap = head.children[2] as HTMLElement
    expect(barWrap.className).toBe('px-4 pt-1.5 pb-2.5')
    expect(barWrap.children[0].className).toBe('flex h-2 rounded-full overflow-hidden bg-ink3/50')
  })

  it('pins the category list rows: wrap, row, chevron slot, dot, name, tokens, percentage', () => {
    const panel = openWith(sampleBreakdown())
    const listWrap = panel.children[1] as HTMLElement
    expect(listWrap.className).toBe('pb-1')
    expect(listWrap.children.length).toBe(3)
    // Each category sits in its own wrap; the row is the wrap's first child.
    const wrap = listWrap.children[0] as HTMLElement
    const row = wrap.children[0] as HTMLElement
    expect(row.className).toBe('flex items-center gap-2 px-4 py-1')
    // Degraded row (no id, no sections payload): a 14px placeholder keeps
    // the dots aligned with expandable rows.
    expect(row.children.length).toBe(5)
    expect(row.children[0].className).toBe('w-3.5 shrink-0')
    expect((row.children[1] as HTMLElement).style.backgroundColor).toBe('var(--color-blue)')
    expect(row.children[2].className).toBe('text-[13px] text-t2 flex-1 truncate')
    expect(row.children[2].textContent).toBe('System prompt')
    expect(row.children[3].className).toBe('mono text-[12px] text-t3')
    expect(row.children[3].textContent).toBe('4.9k')
    expect(row.children[4].className).toBe('text-[11px] text-t3 w-10 text-right')
    expect(row.children[4].textContent).toBe('2.5%')
  })

  it('expandable category rows lead with a chevron and gain pointer affordances', () => {
    const panel = openWith(sampleBreakdown({
      categories: [
        { name: 'System prompt', id: 'system_prompt', tokens: 5000, percentage: 2.5, color: '12', isFree: false, isReserved: false },
        { name: 'Free space', id: 'free_space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemPromptSections: [{ id: 'base', text: 'BASE', tokens: 400, source: 'builtin' }],
    }))
    const listWrap = panel.children[1] as HTMLElement
    const row = (listWrap.children[0] as HTMLElement).children[0] as HTMLElement
    expect(row.className).toContain('cursor-pointer')
    expect(row.className).toContain('select-none')
    expect(row.children[0].classList.contains('transition-transform')).toBe(true)
    const free = (listWrap.children[1] as HTMLElement).children[0] as HTMLElement
    expect(free.children[0].className).toBe('w-3.5 shrink-0')
  })

  it('zero-token categories get no bar segment but keep their list row', () => {
    const panel = openWith(sampleBreakdown({
      categories: [
        { name: 'Messages', tokens: 30000, percentage: 15.0, color: '255', isFree: false, isReserved: false },
        { name: 'Odd color', tokens: 0, percentage: 0.0, color: '999', isFree: false, isReserved: false },
      ],
    }))
    const bar = (panel.children[0] as HTMLElement).children[2].children[0] as HTMLElement
    expect(bar.querySelectorAll('[data-testid="context-segment"]').length).toBe(1)
    const listWrap = panel.children[1] as HTMLElement
    expect(listWrap.children.length).toBe(2)
    // Unknown ANSI codes fall back to the muted text color, not nothing.
    const odd = (listWrap.children[1] as HTMLElement).children[0] as HTMLElement
    expect((odd.children[1] as HTMLElement).style.backgroundColor).toBe('var(--color-t3)')
  })

  it('accordion structure: segments container, segment rows, hidden bodies', () => {
    const panel = openWith(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemTools: [{ name: 'grep', tokens: 200, text: 'grep\n\nP\n\n{}' }],
    }))
    // The retired detail section leaves the panel at head + category list.
    expect(panel.children.length).toBe(2)
    const listWrap = panel.children[1] as HTMLElement
    const wrap = listWrap.children[0] as HTMLElement
    expect(wrap.children.length).toBe(2) // catRow + segments container
    const segs = wrap.children[1] as HTMLElement
    expect(segs.dataset.testid).toBe('ctx-cat-segments')
    expect(segs.dataset.cat).toBe('system_tools')
    expect(segs.classList.contains('hidden')).toBe(true)
    const segWrap = segs.children[0] as HTMLElement
    expect(segWrap.children.length).toBe(2) // segRow + body
    const segRow = segWrap.children[0] as HTMLElement
    expect(segRow.dataset.testid).toBe('ctx-segment-row')
    expect(segRow.dataset.seg).toBe('grep')
    expect(segRow.textContent).toContain('grep')
    expect(segRow.textContent).toContain('200')
    const body = segWrap.children[1] as HTMLElement
    expect(body.dataset.testid).toBe('ctx-segment-body')
    expect(body.dataset.seg).toBe('grep')
    expect(body.classList.contains('hidden')).toBe(true)
    expect(body.textContent).toBe('grep\n\nP\n\n{}')
    expect(body.className).toContain('whitespace-pre-wrap')
    expect(body.className).toContain('max-h-[40dvh]')
  })

  it('empty detail sections render no heading at all', () => {
    const panel = openWith(sampleBreakdown())
    const text = panel.textContent ?? ''
    expect(text).not.toContain('System tools')
    expect(text).not.toContain('MCP tools deferred')
  })

  it('system tools rows appear after expanding the category', () => {
    const panel = openWith(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
      ],
      systemTools: [{ name: 'grep', tokens: 200, text: 'grep\n\nP\n\n{}' }],
    }))
    const catRow = panel.querySelector('[data-testid="ctx-category"][data-cat="system_tools"]') as HTMLElement
    catRow.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const segs = panel.querySelector('[data-testid="ctx-cat-segments"][data-cat="system_tools"]') as HTMLElement
    expect(segs.classList.contains('hidden')).toBe(false)
    const segRow = segs.querySelector('[data-testid="ctx-segment-row"][data-seg="grep"]') as HTMLElement
    expect(segRow.textContent).toContain('200')
  })

  it('loaded MCP tool rows read "name (server)"', () => {
    const panel = openWith(sampleBreakdown({
      mcpToolsLoaded: [{ name: 'search', serverName: 'ddg', tokens: 300, isLoaded: true }],
    }))
    const row = findDetailRow(panel, 'search (ddg)')
    expect(row).not.toBeNull()
    expect(row!.children[1].textContent).toBe('300')
  })

  it('deferred MCP tool rows are name-only rows without tokens', () => {
    const panel = openWith(sampleBreakdown({
      mcpToolsDeferred: [{ name: 'fetch', serverName: 'ddg', tokens: 0, isLoaded: false }],
    }))
    const sec = panel.children[2] as HTMLElement
    expect(sec.children[0].textContent).toBe('MCP tools deferred')
    const row = panel.children[3] as HTMLElement
    expect(row.className).toBe('flex items-center justify-between px-4 py-1')
    expect(row.children.length).toBe(1)
    const span = row.children[0] as HTMLElement
    expect(span.className).toBe('text-[13px] text-t2 truncate')
    expect(span.textContent).toBe('fetch (ddg)')
  })

  it('agent rows read "type (source)"', () => {
    const panel = openWith(sampleBreakdown({
      agents: [{ agentType: 'general', source: 'builtin', tokens: 400 }],
    }))
    const row = findDetailRow(panel, 'general (builtin)')
    expect(row).not.toBeNull()
    expect(row!.children[1].textContent).toBe('400')
  })

  it('skill rows read "name (source)"', () => {
    const panel = openWith(sampleBreakdown({
      skills: [{ name: 'pdf', source: 'builtin', tokens: 500 }],
    }))
    const row = findDetailRow(panel, 'pdf (builtin)')
    expect(row).not.toBeNull()
    expect(row!.children[1].textContent).toBe('500')
  })

  it('top-tool rows sum call and result tokens', () => {
    const panel = openWith(sampleBreakdown({
      messageBreakdown: {
        toolCallTokens: 5000,
        toolResultTokens: 10000,
        attachmentTokens: 0,
        assistantTextTokens: 2000,
        userTextTokens: 1000,
        toolCallsByType: [{ name: 'Bash', callTokens: 3000, resultTokens: 6000 }],
        attachmentsByType: [],
      },
    }))
    const row = findDetailRow(panel, 'Bash')
    expect(row).not.toBeNull()
    expect(row!.children[1].textContent).toBe('8.8k')
  })

  it('an empty tool-by-type list renders no Top tools section', () => {
    const panel = openWith(sampleBreakdown({
      messageBreakdown: {
        toolCallTokens: 5000,
        toolResultTokens: 10000,
        attachmentTokens: 0,
        assistantTextTokens: 2000,
        userTextTokens: 1000,
        toolCallsByType: [],
        attachmentsByType: [],
      },
    }))
    const text = panel.textContent ?? ''
    expect(text).not.toContain('Top tools')
    expect(text).toContain('Message breakdown')
  })
})

describe('Header context accordion', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function getPanel(): HTMLDivElement | null {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLDivElement
    }
    return null
  }

  function clickTrigger() {
    const el = header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
    el.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  function catRow(panel: HTMLElement, cat: string): HTMLElement {
    const row = panel.querySelector(`[data-testid="ctx-category"][data-cat="${cat}"]`)
    if (!row) throw new Error(`category row ${cat} not found`)
    return row as HTMLElement
  }

  function catSegs(panel: HTMLElement, cat: string): HTMLElement {
    const segs = panel.querySelector(`[data-testid="ctx-cat-segments"][data-cat="${cat}"]`)
    if (!segs) throw new Error(`segments container ${cat} not found`)
    return segs as HTMLElement
  }

  function segRow(panel: HTMLElement, seg: string): HTMLElement {
    const row = panel.querySelector(`[data-testid="ctx-segment-row"][data-seg="${seg}"]`)
    if (!row) throw new Error(`segment row ${seg} not found`)
    return row as HTMLElement
  }

  function segBody(panel: HTMLElement, seg: string): HTMLElement {
    const body = panel.querySelector(`[data-testid="ctx-segment-body"][data-seg="${seg}"]`)
    if (!body) throw new Error(`segment body ${seg} not found`)
    return body as HTMLElement
  }

  const click = (el: HTMLElement) => el.dispatchEvent(new MouseEvent('click', { bubbles: true }))

  function accordionBreakdown(overrides: Partial<ContextBreakdownData> = {}): ContextBreakdownData {
    return sampleBreakdown({
      categories: [
        { name: 'System prompt', id: 'system_prompt', tokens: 5000, percentage: 2.5, color: '12', isFree: false, isReserved: false },
        { name: 'Memory files', id: 'memory_files', tokens: 2000, percentage: 1.0, color: '220', isFree: false, isReserved: false },
        { name: 'Free space', id: 'free_space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemPromptSections: [
        { id: 'base', text: 'BASE TEXT', tokens: 400, source: 'builtin' },
        { id: 'other', text: 'OTHER TEXT', tokens: 100 },
      ],
      memorySections: [
        { id: 'instructions', text: 'INSTR', tokens: 300, source: 'readonly' },
        { id: 'index', text: 'IDX', tokens: 50, source: 'memory_md' },
      ],
      ...overrides,
    })
  }

  it('cold open: every segments container pre-rendered but hidden', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    expect(catSegs(panel, 'system_prompt').classList.contains('hidden')).toBe(true)
    expect(catSegs(panel, 'memory_files').classList.contains('hidden')).toBe(true)
    expect(panel.querySelectorAll('[data-testid="ctx-segment-body"]').length).toBe(4)
  })

  it('expands and collapses categories with same-layer mutual exclusion', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    const spRow = catRow(panel, 'system_prompt')
    const memRow = catRow(panel, 'memory_files')
    const spSegs = catSegs(panel, 'system_prompt')
    const memSegs = catSegs(panel, 'memory_files')

    click(spRow)
    expect(spSegs.classList.contains('hidden')).toBe(false)
    expect(spRow.children[0].classList.contains('rotate-90')).toBe(true)

    // Opening memory_files closes system_prompt (same layer, at most one).
    click(memRow)
    expect(spSegs.classList.contains('hidden')).toBe(true)
    expect(spRow.children[0].classList.contains('rotate-90')).toBe(false)
    expect(memSegs.classList.contains('hidden')).toBe(false)
    expect(memRow.children[0].classList.contains('rotate-90')).toBe(true)

    // Clicking the open category collapses it; nothing stays open.
    click(memRow)
    expect(memSegs.classList.contains('hidden')).toBe(true)
    expect(memRow.children[0].classList.contains('rotate-90')).toBe(false)
  })

  it('segment bodies show byte-exact text with segment-level mutual exclusion', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    click(catRow(panel, 'system_prompt'))

    const baseRow = segRow(panel, 'base')
    const otherRow = segRow(panel, 'other')
    const baseBody = segBody(panel, 'base')
    const otherBody = segBody(panel, 'other')
    expect(baseBody.classList.contains('hidden')).toBe(true)

    click(baseRow)
    expect(baseBody.classList.contains('hidden')).toBe(false)
    expect(baseBody.textContent).toBe('BASE TEXT')

    click(otherRow)
    expect(baseBody.classList.contains('hidden')).toBe(true)
    expect(otherBody.classList.contains('hidden')).toBe(false)
    expect(otherBody.textContent).toBe('OTHER TEXT')

    click(otherRow)
    expect(otherBody.classList.contains('hidden')).toBe(true)
  })

  it('memory segment badges: literal MEMORY.md file badge, none on instructions', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    click(catRow(panel, 'memory_files'))
    expect(segRow(panel, 'index').textContent).toContain('MEMORY.md')
    expect(segRow(panel, 'instructions').textContent).not.toContain('MEMORY.md')
  })

  it('switching category collapses the open segment', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    click(catRow(panel, 'system_prompt'))
    click(segRow(panel, 'base'))
    expect(segBody(panel, 'base').classList.contains('hidden')).toBe(false)

    click(catRow(panel, 'memory_files'))
    expect(segBody(panel, 'base').classList.contains('hidden')).toBe(true)
    expect(catSegs(panel, 'system_prompt').classList.contains('hidden')).toBe(true)
  })

  it('degrades: plain categories get no chevron and no segments container', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(accordionBreakdown())
    clickTrigger()
    const panel = getPanel()!
    const free = catRow(panel, 'free_space')
    expect(free.children[0].className).toBe('w-3.5 shrink-0')
    click(free)
    expect(panel.querySelectorAll('[data-testid="ctx-cat-segments"]:not(.hidden)').length).toBe(0)
  })

  it('degrades: old-server payload without section arrays renders plain rows without crashing', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System prompt', id: 'system_prompt', tokens: 5000, percentage: 2.5, color: '12', isFree: false, isReserved: false },
        { name: 'Memory files', id: 'memory_files', tokens: 2000, percentage: 1.0, color: '220', isFree: false, isReserved: false },
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
      ],
      // systemTools present but every entry lacks text (old server): the
      // category expands to plain rows, no segment bodies.
      systemTools: [{ name: 'grep', tokens: 200 }],
    }))
    clickTrigger()
    const panel = getPanel()!
    for (const cat of ['system_prompt', 'memory_files']) {
      expect(catRow(panel, cat).children[0].className).toBe('w-3.5 shrink-0')
    }
    const toolsRow = catRow(panel, 'system_tools')
    expect(toolsRow.children[0].classList.contains('transition-transform')).toBe(true)
    click(toolsRow)
    const segs = catSegs(panel, 'system_tools')
    expect(segs.classList.contains('hidden')).toBe(false)
    const grepRow = segRow(panel, 'grep')
    expect(grepRow.children[0].className).toBe('w-3.5 shrink-0')
    expect(segs.querySelectorAll('[data-testid="ctx-segment-body"]').length).toBe(0)
  })

  it('system tools accordion: retirement of the detail section, neighbors intact', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      categories: [
        { name: 'System tools', id: 'system_tools', tokens: 200, percentage: 0.1, color: '45', isFree: false, isReserved: false },
        { name: 'Free space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
      ],
      systemTools: [{ name: 'grep', tokens: 200, text: 'grep\n\nP\n\n{}' }],
      mcpToolsLoaded: [{ name: 'search', serverName: 'ddg', tokens: 300, isLoaded: true }],
      agents: [{ agentType: 'general', source: 'builtin', tokens: 400 }],
    }))
    clickTrigger()
    const panel = getPanel()!

    // Retirement proof: no section heading node titled 'System tools' — the
    // same text on the category row must not mask the check, so query the
    // heading class, not textContent absence.
    const headings: string[] = []
    for (const el of panel.querySelectorAll('div')) {
      if (el.className === 'mono text-[11px] text-t3 uppercase tracking-wider') {
        headings.push(el.textContent ?? '')
      }
    }
    expect(headings).not.toContain('System tools')
    expect(headings).toContain('MCP tools loaded')
    expect(headings).toContain('Agents')

    click(catRow(panel, 'system_tools'))
    expect(catSegs(panel, 'system_tools').classList.contains('hidden')).toBe(false)
    click(segRow(panel, 'grep'))
    expect(segBody(panel, 'grep').textContent).toBe('grep\n\nP\n\n{}')
  })

  it('rebuild after data refresh resets open state', () => {
    header.setContext(500, 200000)
    const bd = accordionBreakdown()
    header.setContextBreakdown(bd)
    clickTrigger()
    const panel = getPanel()!
    click(catRow(panel, 'system_prompt'))
    click(segRow(panel, 'base'))
    expect(segBody(panel, 'base').classList.contains('hidden')).toBe(false)

    header.setContextBreakdown(bd)
    expect(catSegs(panel, 'system_prompt').classList.contains('hidden')).toBe(true)
    expect(segBody(panel, 'base').classList.contains('hidden')).toBe(true)
  })
})

describe('Header context popover lifecycle', () => {
  let header: ReturnType<typeof createHeader>

  beforeEach(() => {
    document.body.innerHTML = ''
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
  })

  function trigger(): HTMLButtonElement {
    return header.root.querySelector('[data-testid="context-trigger"]') as HTMLButtonElement
  }

  function clickTrigger(): void {
    header.setContext(500, 200000)
    trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  function panel(): HTMLElement {
    const panels = document.body.querySelectorAll('.modal-enter')
    for (const p of panels) {
      if (!p.classList.contains('hidden') && !p.querySelector('textarea')) return p as HTMLElement
    }
    throw new Error('context panel not open')
  }

  it('the loading placeholder is a single centered line of copy', () => {
    clickTrigger()
    const p = panel()
    expect(p.textContent).toBe('Loading...')
    expect(p.children[0].className).toBe('px-4 py-6 text-center text-[13px] text-t3')
  })

  it('data arriving while the loading panel is open swaps in the breakdown', () => {
    clickTrigger()
    expect(panel().textContent).toBe('Loading...')
    header.setContextBreakdown(sampleBreakdown())
    expect(panel().textContent).toContain('Context Usage')
  })

  it('null arriving while the loading panel is open swaps in the empty hint', () => {
    clickTrigger()
    header.setContextBreakdown(null)
    const p = panel()
    expect(p.firstChild).toBe(p.firstElementChild)
    expect(p.textContent).toBe('Send a message first to see context usage.')
    expect(p.children[0].className).toBe('px-4 py-6 text-center text-[13px] text-t3')
  })

  it('opening after null data shows the empty hint directly (not loading)', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(null)
    trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const p = panel()
    expect(p.textContent).toBe('Send a message first to see context usage.')
    expect(p.children[0].className).toBe('px-4 py-6 text-center text-[13px] text-t3')
  })

  it('reopening after a data change rebuilds the panel from scratch', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown())
    trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const opened = panel()
    expect(opened.textContent).toContain('System prompt')
    trigger().dispatchEvent(new MouseEvent('click', { bubbles: true })) // toggle closed
    expect(opened.classList.contains('hidden')).toBe(true)
    header.setContextBreakdown(null)
    trigger().dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const p = panel()
    expect(p).toBe(opened)
    expect(p.firstChild).toBe(p.firstElementChild)
    expect(p.textContent).toBe('Send a message first to see context usage.')
  })
})

describe('Header target switch (multi-endpoint WUI)', () => {
  // The testid sits on the wrapping button (the click target); the text and
  // the connected-state class live on the inner wordmark span.
  const wordmark = (header: ReturnType<typeof createHeader>): HTMLElement =>
    header.root.querySelector('[data-testid="gbot-wordmark"]') as HTMLElement
  const markText = (header: ReturnType<typeof createHeader>): HTMLElement =>
    wordmark(header).querySelector('span') as HTMLElement

  interface TargetPayload {
    target: string
    current: string
    remotes: { name: string; host: string; port: number }[]
  }
  const REMOTES: TargetPayload['remotes'] = [
    { name: '桌面', host: '192.168.1.5', port: 8765 },
    { name: 'nas', host: 'nas.box.local', port: 9000 },
  ]

  const applyTarget = (payload: TargetPayload | string): void => {
    ;(window as unknown as Record<string, unknown>).__gbotApplyTarget(
      typeof payload === 'string' ? payload : JSON.stringify(payload),
    )
  }

  const stubNative = (remotes: TargetPayload['remotes'] = REMOTES) => {
    const native = {
      getRemoteTargets: vi.fn(() => JSON.stringify(remotes)),
      switchTo: vi.fn(),
    }
    vi.stubGlobal('GBotNative', native)
    return native
  }

  const popup = (): HTMLElement | null =>
    document.body.querySelector('[data-testid="target-picker-panel"]') as HTMLElement | null

  const clickWordmark = (header: ReturnType<typeof createHeader>) =>
    wordmark(header).dispatchEvent(new MouseEvent('click', { bubbles: true }))

  const row = (testid: string, name?: string): HTMLElement => {
    const rows = document.body.querySelectorAll(`[data-testid="${testid}"]`)
    for (const r of rows) {
      if (name === undefined || (r as HTMLElement).dataset.targetName === name) return r as HTMLElement
    }
    return rows[0] as HTMLElement
  }

  beforeEach(() => {
    document.body.innerHTML = ''
    // zh pins the popup copy ('本地') regardless of jsdom's en-US navigator —
    // the same pinning convention the settings tests use.
    setLocale('zh')
  })

  afterEach(() => {
    vi.unstubAllGlobals()
    delete (window as unknown as Record<string, unknown>).__gbotApplyTarget
  })

  it('browser degradation: no GBotNative → static "GBot" wordmark, no hook, inert click', () => {
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    expect(wordmark(header).textContent).toBe('GBot')
    expect((window as unknown as Record<string, unknown>).__gbotApplyTarget).toBeUndefined()
    expect(() => clickWordmark(header)).not.toThrow()
    expect(popup()).toBeNull()
    expect(wordmark(header).textContent).toBe('GBot')
  })

  it('registers __gbotApplyTarget; the LOCAL target keeps the "GBot" brand wordmark', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    expect(wordmark(header).textContent).toBe('GBot') // before Kotlin injects
    applyTarget({ target: 'local', current: '桌面', remotes: REMOTES })
    expect(wordmark(header).textContent).toBe('GBot')
  })

  it('remote target shows the CURRENT remote name in the wordmark', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    expect(wordmark(header).textContent).toBe('桌面')
    applyTarget({ target: 'remote', current: 'nas', remotes: REMOTES })
    expect(wordmark(header).textContent).toBe('nas')
  })
  it('remote target with EMPTY current degrades to the "GBot" brand wordmark', () => {
    // E.g. the active endpoint was deleted in settings while a remote page
    // stayed open — Kotlin reconciles target state, the label must not lie.
    stubNative([])
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '', remotes: [] })
    expect(wordmark(header).textContent).toBe('GBot')
  })

  it('malformed apply payload is ignored (wordmark untouched)', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget('not json')
    expect(wordmark(header).textContent).toBe('GBot')
  })

  it('wordmark click opens a popup listing 本地 + every remote; the current is marked', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    clickWordmark(header)
    const p = popup()
    expect(p).not.toBeNull()
    expect(row('target-local').textContent).toContain('本地')
    expect(row('target-entry', '桌面').textContent).toContain('桌面')
    expect(row('target-entry', 'nas').textContent).toContain('nas')
    // The active entry carries the blue DOT (no check glyph — recipe matches
    // the model rows); non-active entries do not.
    expect(row('target-entry', '桌面').querySelector('.bg-blue')).not.toBeNull()
    expect(row('target-entry', 'nas').querySelector('.bg-blue')).toBeNull()
    expect(row('target-local').querySelector('.bg-blue')).toBeNull()
    // Remote rows echo their address; the local row is this device.
    expect(row('target-entry', '桌面').textContent).toContain('192.168.1.5:8765')
    expect(row('target-entry', 'nas').textContent).toContain('nas.box.local:9000')
    expect(row('target-local').textContent).not.toContain('192.168.1.5')
  })

  it('local target marks the 本地 row instead', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'local', current: '', remotes: REMOTES })
    clickWordmark(header)
    expect(row('target-local').querySelector('.bg-blue')).not.toBeNull()
    expect(row('target-entry', '桌面').querySelector('.bg-blue')).toBeNull()
  })

  it('clicking the 本地 entry calls GBotNative.switchTo("local") and closes the popup', () => {
    const native = stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    clickWordmark(header)
    row('target-local').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(native.switchTo).toHaveBeenCalledTimes(1)
    expect(native.switchTo).toHaveBeenCalledWith('local')
    expect(popup()!.classList.contains('hidden')).toBe(true)
  })

  it('clicking a remote entry calls GBotNative.switchTo(name) and closes the popup', () => {
    const native = stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'local', current: '', remotes: REMOTES })
    clickWordmark(header)
    row('target-entry', 'nas').dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(native.switchTo).toHaveBeenCalledTimes(1)
    expect(native.switchTo).toHaveBeenCalledWith('nas')
    expect(popup()!.classList.contains('hidden')).toBe(true)
  })

  it('re-reads getRemoteTargets at every open so saved endpoints appear without a reload', () => {
    const native = stubNative([])
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'local', current: '', remotes: [] })
    clickWordmark(header)
    expect(row('target-entry', 'nas')).toBeUndefined()
    // Popup closed by the outside-click path, then a NEW endpoint was saved
    // in settings; the next open must show it.
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    native.getRemoteTargets.mockReturnValue(JSON.stringify(REMOTES))
    clickWordmark(header)
    expect(native.getRemoteTargets).toHaveBeenCalledTimes(2)
    expect(row('target-entry', 'nas').textContent).toContain('nas')
  })
  it('opens the target popup without remote rows when the bridge lacks getRemoteTargets', () => {
    // Older Kotlin builds expose GBotNative without this method — pin the
    // fallback path (empty list, popup intact). The catch below makes the
    // '[]' default behaviorally equivalent to a parse failure, so this is
    // coverage of the path, not a discriminator between the two.
    const native = stubNative([])
    delete (native as Partial<typeof native>).getRemoteTargets
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'local', current: '', remotes: [] })
    clickWordmark(header)
    expect(popup()).not.toBeNull()
    expect(row('target-entry', 'nas')).toBeUndefined()
  })

  it('popup panel and rows carry their layout classes; the local row carries no target name', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    clickWordmark(header)
    const p = popup()!
    expect(p.className).toContain('min-w-[220px]')
    const local = row('target-local')
    expect(local.className).toBe('w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50')
    expect(local.getAttribute('data-target-name')).toBeNull()
    expect(local.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-t3/30')
    expect(local.children[1].className).toBe('text-[13px] truncate text-t2')
    const active = row('target-entry', '桌面')
    expect(active.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-blue')
    expect(active.children[1].className).toBe('text-[13px] truncate text-blue')
    const nas = row('target-entry', 'nas')
    expect(nas.children[0].className).toBe('h-2 w-2 rounded-full shrink-0 bg-t3/30')
    expect(nas.children[1].className).toBe('text-[13px] truncate text-t2')
    expect(nas.children[2].className).toBe('ml-2 text-[10px] text-t3 font-mono shrink-0 whitespace-nowrap')
    expect(nas.children[2].textContent).toBe('nas.box.local:9000')
  })

  it('the popup rebuilds from scratch on every open (no stray text)', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    clickWordmark(header)
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    clickWordmark(header)
    const p = popup()!
    expect(p.firstChild).toBe(p.firstElementChild)
    expect(p.querySelectorAll('[data-testid="target-local"]').length).toBe(1)
  })

  it('a non-array getRemoteTargets payload degrades to the local row only', () => {
    // The Kotlin bridge promise is JSON; anything that is not an array of
    // endpoints must collapse to local-only rather than crash the render.
    const native = {
      getRemoteTargets: vi.fn(() => '{"not":"an array"}'),
      switchTo: vi.fn(),
    }
    vi.stubGlobal('GBotNative', native)
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: '桌面', remotes: REMOTES })
    clickWordmark(header)
    const p = popup()!
    expect(p.querySelectorAll('[data-testid="target-entry"]').length).toBe(0)
    expect(p.querySelectorAll('[data-testid="target-local"]').length).toBe(1)
  })

  it('setStatus still swaps the connected-state class after a target apply', () => {
    stubNative()
    const header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
    })
    document.body.appendChild(header.root)
    applyTarget({ target: 'remote', current: 'nas', remotes: REMOTES })
    header.setStatus(true)
    expect(markText(header).textContent).toBe('nas')
    expect(markText(header).className).toContain('pulse')
    header.setStatus(false)
    expect(markText(header).className).not.toContain('pulse')
    expect(markText(header).className).toContain('text-t3')
  })
})

describe('Header engine picker row status', () => {
  let header: ReturnType<typeof createHeader>
  let pulled: number
  beforeEach(() => {
    document.body.innerHTML = ''
    pulled = 0
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: () => {},
      onEngineNew: () => {},
      onEngineListRequest: () => { pulled++ },
    })
    document.body.appendChild(header.root)
  })
  function openPanel(): HTMLElement {
    const trigger = header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLElement
    }
    throw new Error('engine picker panel not open')
  }
  function rows(panel: HTMLElement): HTMLElement[] {
    return Array.from(panel.children[0].children).slice(0, -1) as HTMLElement[]
  }
  function nameSpan(row: HTMLElement): HTMLElement {
    return row.children[1] as HTMLElement
  }

  it('opening the picker requests a fresh engine list (pull on open)', () => {
    expect(pulled).toBe(0)
    openPanel()
    expect(pulled).toBe(1)
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    openPanel()
    expect(pulled).toBe(2)
  })

  it('busy engine: green name with breathe animation', () => {
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2', busy: true },
    ], 'main')
    const panel = openPanel()
    const cls = nameSpan(rows(panel)[1]).className
    expect(cls).toBe('text-[13px] text-engine-busy breathe')
  })

  it('idle engines keep current colors', () => {
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2' },
    ], 'main')
    const panel = openPanel()
    expect(nameSpan(rows(panel)[0]).className).toBe('text-[13px] text-blue')
    expect(nameSpan(rows(panel)[1]).className).toBe('text-[13px] text-t2')
  })

  it('busy green wins over the active blue', () => {
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2', busy: true },
    ], 'e2')
    const panel = openPanel()
    const cls = nameSpan(rows(panel)[1]).className
    expect(cls).toBe('text-[13px] text-engine-busy breathe')
    expect(cls).not.toContain('text-blue')
  })

  it('an open panel re-renders on setEngines (re-pull refresh)', () => {
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2' },
    ], 'main')
    const panel = openPanel()
    expect(nameSpan(rows(panel)[1]).className).toBe('text-[13px] text-t2')
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2', busy: true },
    ], 'main')
    expect(nameSpan(rows(panel)[1]).className).toBe('text-[13px] text-engine-busy breathe')
  })

  it('closing and reopening drops stale animation state', () => {
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2', busy: true },
    ], 'main')
    let panel = openPanel()
    expect(nameSpan(rows(panel)[1]).className).toContain('breathe')
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2' },
    ], 'main')
    panel = openPanel()
    expect(nameSpan(rows(panel)[1]).className).not.toContain('breathe')
  })
})

describe('Header engine picker delete', () => {
  let header: ReturnType<typeof createHeader>
  let deleted: string[]
  let switched: string | null
  beforeEach(() => {
    document.body.innerHTML = ''
    setLocale('en')
    deleted = []
    switched = null
    header = createHeader({
      onModelSelect: () => {},
      onEngineSwitch: (id) => { switched = id },
      onEngineNew: () => {},
      onEngineDelete: (id) => { deleted.push(id) },
    })
    document.body.appendChild(header.root)
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e2', name: 'Second', model: 'm2' },
      { id: 'dream', name: 'Dream', model: 'm3', system: true },
    ], 'main')
  })
  function openPanel(): HTMLElement {
    const trigger = header.root.querySelector('button.text-\\[15px\\]:not(.hidden)') as HTMLButtonElement
    trigger.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const panels = document.body.querySelectorAll('[data-testid="engine-picker-panel"]')
    for (const p of panels) {
      if (!p.classList.contains('hidden')) return p as HTMLElement
    }
    throw new Error('engine picker panel not open')
  }
  function trashFor(panel: HTMLElement, engineID: string): HTMLElement | null {
    return panel.querySelector(`[data-engine-delete="${engineID}"]`)
  }
  function rowFor(panel: HTMLElement, engineID: string): HTMLElement {
    const row = panel.querySelector(`[data-engine-id="${engineID}"]`)
    if (!row) throw new Error(`row for ${engineID} not found`)
    return row as HTMLElement
  }
  function clickTrash(panel: HTMLElement, engineID: string): void {
    trashFor(panel, engineID)!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }
  function delToggle(panel: HTMLElement): HTMLButtonElement {
    const btn = panel.querySelector('button[aria-label="Delete engines"]') as HTMLButtonElement
    if (!btn) throw new Error('delete-mode toggle not found')
    return btn
  }
  function enableDelMode(panel: HTMLElement): void {
    delToggle(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
  }

  it('the footer carries bare + and − bunched at the right edge', () => {
    const panel = openPanel()
    const footer = panel.children[0].lastElementChild as HTMLElement
    // Layout C container: right cluster via justify-end + gap, no spacers.
    expect(footer.className).toBe('flex items-center justify-end gap-0.5 border-t border-hairline mt-1 px-1 pt-1')
    const btns = footer.querySelectorAll('button')
    expect(btns.length).toBe(2)
    expect((btns[0] as HTMLElement).getAttribute('aria-label')).toBe('New engine')
    expect((btns[1] as HTMLElement).getAttribute('aria-label')).toBe('Delete engines')
    const spacers = [...footer.children].filter(
      (c) => (c as HTMLElement).classList.contains('flex-1'),
    )
    expect(spacers.length).toBe(0)
    for (const b of btns) {
      expect(b.querySelector('svg')!.getAttribute('width')).toBe('16')
      expect(b.className).not.toContain('w-full')
      // Bare icon button spec: pad + round + hover wash, shared by + and −.
      expect(b.className).toContain('p-1.5 rounded-md hover:bg-ink3/50')
    }
  })

  it('mode off by default: zero delete affordances anywhere', () => {
    const panel = openPanel()
    expect(panel.querySelector('[data-engine-delete]')).toBeNull()
    expect(delToggle(panel).getAttribute('aria-pressed')).toBe('false')
    expect(delToggle(panel).style.color).toBe('')
  })

  it('mode on: trash appears on non-main, non-system rows only', () => {
    const panel = openPanel()
    enableDelMode(panel)
    const toggle = delToggle(panel)
    expect(toggle.getAttribute('aria-pressed')).toBe('true')
    expect(toggle.style.color).toBe('rgb(248, 81, 73)')
    expect(toggle.style.backgroundColor).not.toBe('')
    expect(trashFor(panel, 'main')).toBeNull()
    expect(trashFor(panel, 'dream')).toBeNull()
    const trash = trashFor(panel, 'e2')
    expect(trash).not.toBeNull()
    expect(trash!.getAttribute('aria-pressed')).toBe('false')
    expect(trash!.getAttribute('aria-label')).toBe('Delete engine')
    expect(trash!.getAttribute('role')).toBe('button')
    expect(trash!.className).toContain('ml-auto')
    expect(trash!.className).toContain('cursor-pointer')
    expect(trash!.querySelector('svg')!.getAttribute('width')).toBe('14')
  })

  it('toggling the mode off again hides every trash', () => {
    const panel = openPanel()
    enableDelMode(panel)
    expect(panel.querySelector('[data-engine-delete="e2"]')).not.toBeNull()
    delToggle(panel).dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(panel.querySelector('[data-engine-delete]')).toBeNull()
    expect(delToggle(panel).getAttribute('aria-pressed')).toBe('false')
    expect(delToggle(panel).style.color).toBe('')
  })

  it('closing the popover resets the mode; a reopen starts unarmed', () => {
    const panel = openPanel()
    enableDelMode(panel)
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    const reopened = openPanel()
    expect(reopened.querySelector('[data-engine-delete]')).toBeNull()
    expect(delToggle(reopened).getAttribute('aria-pressed')).toBe('false')
  })

  it('setEngines re-render while open keeps the mode and drops armed state', () => {
    vi.useFakeTimers()
    try {
      const panel = openPanel()
      enableDelMode(panel)
      clickTrash(panel, 'e2')
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('true')
      header.setEngines([
        { id: 'main', name: 'Main', model: 'm1' },
        { id: 'e2', name: 'Second', model: 'm2' },
        { id: 'dream', name: 'Dream', model: 'm3', system: true },
      ], 'main')
      // The snapshot re-render must not exit delete mode...
      expect(delToggle(panel).getAttribute('aria-pressed')).toBe('true')
      expect(trashFor(panel, 'e2')).not.toBeNull()
      // ...and must not carry the armed intermediate state into the new rows:
      // the rebuilt trash is fresh, so one click arms instead of executing.
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('false')
      clickTrash(panel, 'e2')
      expect(deleted).toEqual([])
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('true')
    } finally {
      vi.useRealTimers()
    }
  })

  it('first click arms red, does not delete, does not switch', () => {
    const panel = openPanel()
    enableDelMode(panel)
    clickTrash(panel, 'e2')
    const trash = trashFor(panel, 'e2')!
    expect(trash.getAttribute('aria-pressed')).toBe('true')
    expect(trash.style.color).toBe('rgb(248, 81, 73)')
    expect(deleted).toEqual([])
    expect(switched).toBeNull()
  })

  it('arm auto-expires after 5s', () => {
    vi.useFakeTimers()
    try {
      const panel = openPanel()
      enableDelMode(panel)
      clickTrash(panel, 'e2')
      vi.advanceTimersByTime(5000)
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('false')
      expect(trashFor(panel, 'e2')!.style.color).toBe('')
      // Post-expiry click re-arms rather than executing.
      clickTrash(panel, 'e2')
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('true')
      expect(deleted).toEqual([])
    } finally {
      vi.useRealTimers()
    }
  })

  it('second click inside the window executes: delete sent, row collapses away', () => {
    vi.useFakeTimers()
    try {
      const panel = openPanel()
      enableDelMode(panel)
      clickTrash(panel, 'e2')
      clickTrash(panel, 'e2')
      expect(deleted).toEqual(['e2'])
      expect(switched).toBeNull()
      // Execute disarms immediately — the dying row must not linger armed.
      expect(trashFor(panel, 'e2')!.getAttribute('aria-pressed')).toBe('false')
      expect(trashFor(panel, 'e2')!.style.color).toBe('')
      const row = rowFor(panel, 'e2')
      expect(row.style.overflow).toBe('hidden')
      expect(row.style.maxHeight).toBe('0px')
      expect(row.style.transition).toContain('max-height')
      expect(row.style.transition).toContain('opacity')
      expect(row.style.paddingTop).toBe('0px')
      expect(row.style.paddingBottom).toBe('0px')
      expect(row.style.opacity).toBe('0')
      vi.advanceTimersByTime(400)
      expect(panel.querySelector('[data-engine-id="e2"]')).toBeNull()
    } finally {
      vi.useRealTimers()
    }
  })

  it('closing and reopening resets the armed state', () => {
    const panel = openPanel()
    enableDelMode(panel)
    clickTrash(panel, 'e2')
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    const reopened = openPanel()
    expect(reopened.querySelector('[data-engine-delete]')).toBeNull()
    enableDelMode(reopened)
    expect(trashFor(reopened, 'e2')!.getAttribute('aria-pressed')).toBe('false')
    // A click on the fresh row arms again, not executes.
    clickTrash(reopened, 'e2')
    expect(deleted).toEqual([])
  })

  it('setEngines while closed leaves the hidden list untouched; reopen rebuilds', () => {
    let panel = openPanel()
    document.body.dispatchEvent(new MouseEvent('mousedown', { bubbles: true }))
    expect(panel.classList.contains('hidden')).toBe(true)
    header.setEngines([
      { id: 'main', name: 'Main', model: 'm1' },
      { id: 'e9', name: 'Ninth', model: 'm9' },
    ], 'main')
    // Stale content must persist in the hidden panel — re-render happens on
    // open, not on push.
    expect(panel.textContent).toContain('Second')
    expect(panel.textContent).not.toContain('Ninth')
    panel = openPanel()
    expect(panel.textContent).not.toContain('Second')
    expect(panel.textContent).toContain('Ninth')
  })
})
