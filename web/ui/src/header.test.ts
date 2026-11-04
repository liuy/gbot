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

  it('the footer starts a new engine and closes the panel', () => {
    setTwo()
    const panel = openPanel()
    const footer = list(panel).children[2] as HTMLButtonElement
    expect(footer.getAttribute('aria-label')).toBe('New engine')
    expect(footer.className).toContain('w-full justify-center px-3 py-2 rounded-lg border-t border-hairline mt-1 hover:bg-ink3/50')
    footer.dispatchEvent(new MouseEvent('click', { bubbles: true }))
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

  it('pins the category list rows: wrap, row, dot, name, tokens, percentage', () => {
    const panel = openWith(sampleBreakdown())
    const listWrap = panel.children[1] as HTMLElement
    expect(listWrap.className).toBe('pb-1')
    expect(listWrap.children.length).toBe(3)
    const row = listWrap.children[0] as HTMLElement
    expect(row.className).toBe('flex items-center gap-2 px-4 py-1')
    expect(row.children.length).toBe(4)
    expect(row.children[0].className).toBe('h-2 w-2 rounded-full shrink-0')
    expect((row.children[0] as HTMLElement).style.backgroundColor).toBe('var(--color-blue)')
    expect(row.children[1].className).toBe('text-[13px] text-t2 flex-1 truncate')
    expect(row.children[1].textContent).toBe('System prompt')
    expect(row.children[2].className).toBe('mono text-[12px] text-t3')
    expect(row.children[2].textContent).toBe('4.9k')
    expect(row.children[3].className).toBe('text-[11px] text-t3 w-10 text-right')
    expect(row.children[3].textContent).toBe('2.5%')
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
    const odd = listWrap.children[1] as HTMLElement
    expect((odd.children[0] as HTMLElement).style.backgroundColor).toBe('var(--color-t3)')
  })

  it('sections and detail rows: heading classes and name/value spans', () => {
    const panel = openWith(sampleBreakdown({
      systemPromptSections: [{ name: 'Core', tokens: 500 }],
    }))
    expect(panel.children.length).toBe(4) // head, category list, section, detail row
    const sec = panel.children[2] as HTMLElement
    expect(sec.className).toBe('pt-2 pb-1 px-4')
    expect(sec.children.length).toBe(1)
    expect(sec.children[0].className).toBe('mono text-[11px] text-t3 uppercase tracking-wider')
    expect(sec.children[0].textContent).toBe('System prompt')
    const row = panel.children[3] as HTMLElement
    expect(row.className).toBe('flex items-center justify-between px-4 py-1')
    expect(row.children.length).toBe(2)
    expect(row.children[0].className).toBe('text-[13px] text-t2 truncate')
    expect(row.children[0].textContent).toBe('Core')
    expect(row.children[1].className).toBe('mono text-[12px] text-t3 ml-2 shrink-0')
    expect(row.children[1].textContent).toBe('500')
  })

  it('empty detail sections render no heading at all', () => {
    const panel = openWith(sampleBreakdown())
    const text = panel.textContent ?? ''
    expect(text).not.toContain('Memory files')
    expect(text).not.toContain('System tools')
    expect(text).not.toContain('MCP tools deferred')
  })

  it('system tools rows list name and tokens', () => {
    const panel = openWith(sampleBreakdown({ systemTools: [{ name: 'grep', tokens: 200 }] }))
    const row = findDetailRow(panel, 'grep')
    expect(row).not.toBeNull()
    expect(row!.children[1].textContent).toBe('200')
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
