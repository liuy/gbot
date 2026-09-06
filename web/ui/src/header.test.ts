import { describe, it, expect, beforeEach, afterEach } from 'vitest'
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
    systemPromptSections: [],
    memoryFiles: [],
    agents: [],
    skills: [],
    messageBreakdown: null,
    apiUsage: null,
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
      memoryFiles: [{ path: '/home/user/.gbot/memory/user_name.md', tokens: 1500 }],
      agents: [],
      skills: [],
      mcpToolsLoaded: [],
    }))
    clickTrigger()
    const panel = getPanel()
    expect(panel!.textContent).toContain('Memory files')
    expect(panel!.textContent).toContain('user_name.md')
    expect(panel!.textContent).not.toContain('/home/user/.gbot/memory/user_name.md')
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
        { name: 'Platform info', id: 'platform_info', tokens: 3000, percentage: 1.5, color: '39', isFree: false, isReserved: false },
        { name: 'Free space', id: 'free_space', tokens: 150000, percentage: 75.0, color: '240', isFree: true, isReserved: false },
        { name: 'Autocompact buffer', id: 'autocompact_buffer', tokens: 14000, percentage: 7.0, color: '160', isFree: false, isReserved: true },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('平台信息')
    expect(text).toContain('剩余空间')
    expect(text).toContain('自动压缩缓冲区')
    expect(text).not.toContain('Platform info')
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

  it('renders system prompt sub-rows translated from semantic ids in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      systemPromptSections: [
        { name: 'Base prompt', id: 'base_prompt', tokens: 6000 },
        { name: 'Platform info', id: 'platform_info', tokens: 300 },
        { name: 'Tool prompts', id: 'tool_prompts', tokens: 1500 },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('基础提示词')
    expect(text).toContain('平台信息')
    expect(text).toContain('工具提示词')
    expect(text).not.toContain('Base prompt')
    expect(text).not.toContain('Platform info')
    expect(text).not.toContain('Tool prompts')
  })

  it('falls back to the raw name for system prompt sub-rows with unknown or absent ids', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      systemPromptSections: [
        { name: 'Future section', id: 'future_section', tokens: 100 },
        { name: 'Legacy section', tokens: 200 },
      ],
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('Future section')
    expect(text).toContain('Legacy section')
  })

  it('renders section titles and API usage rows in zh', () => {
    header.setContext(500, 200000)
    header.setContextBreakdown(sampleBreakdown({
      systemPromptSections: [{ name: 'Core', tokens: 1000 }],
      memoryFiles: [{ path: '/m/user_name.md', tokens: 1500 }],
      systemTools: [{ name: 'grep', tokens: 200 }],
      mcpToolsLoaded: [{ name: 'search', serverName: 'ddg', tokens: 300, isLoaded: true }],
      mcpToolsDeferred: [{ name: 'fetch', serverName: 'ddg', tokens: 0, isLoaded: false }],
      agents: [{ agentType: 'general', source: 'builtin', tokens: 400 }],
      skills: [{ name: 'pdf', source: 'builtin', tokens: 500 }],
      apiUsage: {
        inputTokens: 1000,
        outputTokens: 2000,
        cacheCreationInputTokens: 300,
        cacheReadInputTokens: 400,
      },
    }))
    clickTrigger()
    const text = getPanel()!.textContent ?? ''
    expect(text).toContain('系统提示词')
    expect(text).toContain('记忆文件')
    expect(text).toContain('系统工具')
    expect(text).toContain('已加载 MCP 工具')
    expect(text).toContain('延迟加载 MCP 工具')
    expect(text).toContain('子代理')
    expect(text).toContain('技能')
    expect(text).toContain('API 用量')
    expect(text).toContain('输入')
    expect(text).toContain('输出')
    expect(text).toContain('缓存创建')
    expect(text).toContain('缓存读取')
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

  function sampleBreakdown(): ContextBreakdownData {
    return {
      model: 'test',
      contextWindow: 200000,
      totalTokens: 50000,
      percentage: 25,
      isAutoCompact: false,
      categories: [
        { name: 'Messages', tokens: 50000, percentage: 25, color: '255', isFree: false, isReserved: false },
      ],
      mcpToolsLoaded: [],
      mcpToolsDeferred: [],
      deferredBuiltinTools: [],
      systemTools: [],
      systemPromptSections: [],
      memoryFiles: [],
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
