// @ts-expect-error fuzzysearch has no types
import fuzzysearch from 'fuzzysearch'
import { createPopupPanel, createOutsideClick, formatTokenCount } from './utils'
import { createCopyButton } from './utils/copy_button'
import { getDebugLogs, onDebugLog } from './log'
import type { ContextBreakdownData } from './types'
import { createElement, createNode, cx } from './dom'
import { createIconButton, createTextButton, createComboButton } from './buttons'
import { renderIcon } from './icons'

export interface HeaderHandles {
  root: HTMLElement
  setStatus: (connected: boolean) => void
  setModel: (model: string) => void
  onHamburgerClick: (handler: () => void) => void
  setModels: (models: { provider: string; model: string; quota?: string }[], curProvider: string, curModel: string) => void
  setQuota: (provider: string, quota: string) => void
  setEngines: (engines: EngineEntry[], activeID: string) => void
  setContext: (used: number, total: number) => void
  setContextBreakdown: (data: ContextBreakdownData | null) => void
  hideContextBreakdown: () => void
  setStreaming: (streaming: boolean) => void
}

interface ModelEntry {
  provider: string
  model: string
  quota?: string
}

interface EngineEntry {
  id: string
  name: string
  model: string
}

function createModelPicker(
  onSelect: (provider: string, model: string) => void,
  onRequestQuota?: () => void,
): { wrap: HTMLElement; setLabel: (text: string) => void; setModels: (models: ModelEntry[], curProvider: string, curModel: string) => void; setQuota: (provider: string, quota: string) => void; setStreaming: (s: boolean) => void } {
  const searchWrap = createElement('div', 'flex items-center gap-2 mx-3 my-2 px-3 py-2 rounded-lg bg-ink3/40')
  searchWrap.appendChild(renderIcon('search', { size: 14, className: 'text-t3 shrink-0' }))
  const searchInput = createNode('textarea', {
    className:
      'flex-1 bg-transparent text-[13px] text-t1 placeholder-t3 outline-none resize-none',
    props: { rows: 1, placeholder: 'Search...', spellcheck: false },
    attrs: { autocapitalize: 'off', autocorrect: 'off' },
    style: { fontFamily: 'inherit' },
  })
  ;(searchInput.style as unknown as Record<string, string>).webkitAppearance = 'none'
  searchWrap.appendChild(searchInput)

  const listContainer = createElement('div', 'max-h-[50dvh] overflow-y-auto p-1')

  let allModels: ModelEntry[] = []
  let currentProvider = ''
  let currentModel = ''
  let streaming = false
  const quotaByProvider = new Map<string, string>()

  const renderList = () => {
    listContainer.innerHTML = ''
    const query = searchInput.value.trim()
    const filtered = allModels.filter(
      (m) => !query || fuzzysearch(query.toLowerCase(), (m.provider + '/' + m.model).toLowerCase()),
    )

    if (filtered.length === 0) {
      const empty = createElement('div', 'px-3 py-4 text-center text-[13px] text-t3')
      empty.textContent = 'No models found'
      listContainer.appendChild(empty)
      return
    }

    let lastProvider = ''
    for (const entry of filtered) {
      if (entry.provider !== lastProvider) {
        lastProvider = entry.provider
        const header = createElement('div', 'px-3 pt-2 pb-1 mono text-[13px] text-t3 uppercase tracking-wider')
        header.textContent = entry.provider
        listContainer.appendChild(header)
      }
      const isActive = entry.provider === currentProvider && entry.model === currentModel
      const item = createElement(
        'button',
        cx(
          'w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors',
          streaming ? 'pointer-events-none' : 'hover:bg-ink3/50',
        ),
      )
      const dot = createElement('span', cx('h-2 w-2 rounded-full shrink-0', isActive ? 'bg-blue' : 'bg-t3/30'))
      item.appendChild(dot)
      const span = createElement('span', cx('text-[13px]', isActive ? 'text-blue' : streaming ? 'text-t3' : 'text-t2'))
      span.textContent = entry.model
      item.appendChild(span)
      const qText = entry.quota ?? quotaByProvider.get(entry.provider)
      if (qText) {
        const q = createElement('span', 'text-[10px] text-t3 ml-auto')
        q.textContent = qText
        item.appendChild(q)
      }
      item.addEventListener('click', () => {
        currentProvider = entry.provider
        currentModel = entry.model
        combo.setLabel(entry.model)
        onSelect(entry.provider, entry.model)
        combo.close()
      })
      listContainer.appendChild(item)
    }
  }

  searchInput.addEventListener('input', renderList)

  // Caller-tracked open flag replaces host.isOpen(): ComboButtonHandle does
  // not leak host internals (locked decision 6). setQuota re-renders the
  // list only while the popup is open so freshly fetched quotas show up
  // without a close/reopen.
  let open = false
  let panelSetup = false
  const combo = createComboButton({
    label: '',
    className: 'text-[15px]',
    onOpen: (panel) => {
      if (!panelSetup) {
        panel.appendChild(searchWrap)
        panel.appendChild(listContainer)
        panelSetup = true
      }
      open = true
      searchInput.value = ''
      renderList()
      // No autofocus: focusing the search box pops the soft keyboard over
      // the popup on Android, hiding half the model list. Users tap the
      // box when they want to filter.
      onRequestQuota?.()
    },
    onClose: () => {
      open = false
      searchInput.value = ''
    },
  })

  const setModels = (models: ModelEntry[], curProvider: string, curModel: string) => {
    allModels = models
    currentProvider = curProvider
    currentModel = curModel
    if (curModel) combo.setLabel(curModel)
  }

  const setQuota = (provider: string, quota: string) => {
    quotaByProvider.set(provider, quota)
    if (open) renderList()
  }

  const setStreaming = (s: boolean) => {
    streaming = s
    if (open) renderList()
  }

  return { wrap: combo.wrap, setLabel: combo.setLabel, setModels, setQuota, setStreaming }
}

function createEnginePicker(
  onSwitch: (engineID: string) => void,
  onNew: () => void,
): { wrap: HTMLElement; setEngines: (engines: EngineEntry[], activeID: string) => void } {
  const listContainer = createElement('div', 'max-h-[50dvh] overflow-y-auto p-1')

  let allEngines: EngineEntry[] = []
  let activeID = ''

  const renderList = () => {
    listContainer.innerHTML = ''
    for (const entry of allEngines) {
      const isActive = entry.id === activeID
      const item = createElement(
        'button',
        cx(
          'w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors',
          'hover:bg-ink3/50',
        ),
      )
      const dot = createElement('span', cx('h-2 w-2 rounded-full shrink-0', isActive ? 'bg-blue' : 'bg-t3/30'))
      item.appendChild(dot)
      const nameSpan = createElement('span', cx('text-[13px]', isActive ? 'text-blue' : 'text-t2'))
      nameSpan.textContent = entry.name || entry.id
      item.appendChild(nameSpan)
      const modelSpan = createElement('span', 'text-[13px] text-t3 ml-2')
      modelSpan.textContent = entry.model
      item.appendChild(modelSpan)
      if (isActive) {
        item.addEventListener('click', () => combo.close())
      } else {
        item.addEventListener('click', () => {
          onSwitch(entry.id)
          combo.close()
        })
      }
      listContainer.appendChild(item)
    }
    const footer = createIconButton({
      icon: 'plus',
      label: 'New engine',
      variant: 'default',
      size: 'md',
      iconSize: 18,
      className: 'w-full justify-center px-3 py-2 rounded-lg border-t border-hairline mt-1 hover:bg-ink3/50',
      onClick: () => {
        onNew()
        combo.close()
      },
    })
    listContainer.appendChild(footer)
  }

  let panelSetup = false
  const combo = createComboButton({
    label: '',
    className: 'text-[15px]',
    onOpen: (panel) => {
      if (!panelSetup) {
        panel.dataset.testid = 'engine-picker-panel'
        panel.appendChild(listContainer)
        panelSetup = true
      }
      renderList()
    },
    onClose: () => {},
  })

  const setEngines = (engines: EngineEntry[], active: string) => {
    allEngines = engines
    activeID = active
    const cur = engines.find((e) => e.id === active)
    if (cur) combo.setLabel(cur.name || cur.id)
  }

  return { wrap: combo.wrap, setEngines }
}

const ANSI_TO_CSS: Record<string, string> = {
  '12': 'var(--color-blue)',
  '39': '#38BDF8',
  '33': '#22D3EE',
  '51': '#06B6D4',
  '201': 'var(--color-violet)',
  '220': 'var(--color-amber)',
  '45': '#14B8A6',
  '46': 'var(--color-green)',
  '22': '#15803D',
  '93': 'var(--color-violet)',
  '255': 'var(--color-t1)',
  '240': 'var(--color-t3)',
  '160': 'var(--color-red)',
}
function ansiToCss(code: string): string {
  return ANSI_TO_CSS[code] ?? 'var(--color-t3)'
}

function createSection(title: string): HTMLDivElement {
  const sec = createElement('div', 'pt-2 pb-1 px-4')
  const h = createNode('div', {
    className: 'mono text-[11px] text-t3 uppercase tracking-wider',
    text: title,
  })
  sec.appendChild(h)
  return sec
}

function createDetailRow(name: string, tokens: number): HTMLDivElement {
  const row = createElement('div', 'flex items-center justify-between px-4 py-1')
  const n = createElement('span', 'text-[13px] text-t2 truncate')
  n.textContent = name
  row.appendChild(n)
  const t = createElement('span', 'mono text-[12px] text-t3 ml-2 shrink-0')
  t.textContent = formatTokenCount(tokens)
  row.appendChild(t)
  return row
}

function renderBreakdownContent(panel: HTMLDivElement, data: ContextBreakdownData, onCompact?: () => void, streaming?: boolean) {
  panel.innerHTML = ''

  const titleBar = createElement('div', 'flex items-start justify-between px-4 pt-3 pb-1')
  const titleCol = createElement('div')
  const title = createNode('div', {
    className: 'text-[14px] font-semibold text-t1',
    text: 'Context Usage',
  })
  titleCol.appendChild(title)
  const total = createElement('div', 'mono text-[12px] text-t3 mt-0.5')
  total.textContent =
    formatTokenCount(data.totalTokens) + ' / ' + formatTokenCount(data.contextWindow) +
    ' (' + data.percentage.toFixed(1) + '%)'
  titleCol.appendChild(total)
  titleBar.appendChild(titleCol)
  if (onCompact) {
    const compactBtn = createTextButton({
      text: 'Compact',
      variant: 'link',
      className: streaming
        ? 'text-[13px] text-t3 shrink-0 pointer-events-none'
        : 'text-[13px] text-blue shrink-0',
    })
    compactBtn.dataset.testid = 'compact-btn'
    compactBtn.addEventListener('click', onCompact)
    titleBar.appendChild(compactBtn)
  }
  panel.appendChild(titleBar)

  const barWrap = createElement('div', 'px-4 py-2')
  const bar = createElement('div', 'flex h-2 rounded-full overflow-hidden bg-ink3/50')
  bar.dataset.testid = 'context-bar'
  for (const cat of data.categories) {
    if (cat.tokens === 0) continue
    const seg = createElement('div')
    seg.style.width = cat.percentage + '%'
    seg.style.backgroundColor = ansiToCss(cat.color)
    seg.dataset.testid = 'context-segment'
    seg.dataset.name = cat.name
    bar.appendChild(seg)
  }
  barWrap.appendChild(bar)
  panel.appendChild(barWrap)

  const listWrap = createElement('div', 'pb-1')
  for (const cat of data.categories) {
    const row = createElement('div', 'flex items-center gap-2 px-4 py-1')
    const dot = createElement('span', 'h-2 w-2 rounded-full shrink-0')
    dot.style.backgroundColor = ansiToCss(cat.color)
    row.appendChild(dot)
    const name = createElement('span', 'text-[13px] text-t2 flex-1 truncate')
    name.textContent = cat.name
    row.appendChild(name)
    const tok = createElement('span', 'mono text-[12px] text-t3')
    tok.textContent = formatTokenCount(cat.tokens)
    row.appendChild(tok)
    const pct = createElement('span', 'text-[11px] text-t3 w-10 text-right')
    pct.textContent = cat.percentage.toFixed(1) + '%'
    row.appendChild(pct)
    listWrap.appendChild(row)
  }
  panel.appendChild(listWrap)

  if (data.messageBreakdown) {
    const mb = data.messageBreakdown
    const sec = createSection('Message breakdown')
    panel.appendChild(sec)
    panel.appendChild(createDetailRow('Tool calls', mb.toolCallTokens))
    panel.appendChild(createDetailRow('Tool results', mb.toolResultTokens))
    panel.appendChild(createDetailRow('Attachments', mb.attachmentTokens))
    panel.appendChild(createDetailRow('Assistant text', mb.assistantTextTokens))
    panel.appendChild(createDetailRow('User text', mb.userTextTokens))
    if (mb.toolCallsByType.length > 0) {
      const subSec = createSection('Top tools')
      panel.appendChild(subSec)
      for (const tc of mb.toolCallsByType) {
        panel.appendChild(createDetailRow(tc.name, tc.callTokens + tc.resultTokens))
      }
    }
  }

  if (data.systemPromptSections.length > 0) {
    const sec = createSection('System prompt')
    panel.appendChild(sec)
    for (const s of data.systemPromptSections) {
      panel.appendChild(createDetailRow(s.name, s.tokens))
    }
  }

  if (data.memoryFiles.length > 0) {
    const sec = createSection('Memory files')
    panel.appendChild(sec)
    for (const f of data.memoryFiles) {
      const name = f.path.split('/').pop() ?? f.path
      panel.appendChild(createDetailRow(name, f.tokens))
    }
  }

  if (data.systemTools.length > 0) {
    const sec = createSection('System tools')
    panel.appendChild(sec)
    for (const t of data.systemTools) {
      panel.appendChild(createDetailRow(t.name, t.tokens))
    }
  }

  if (data.mcpToolsLoaded.length > 0) {
    const sec = createSection('MCP tools loaded')
    panel.appendChild(sec)
    for (const t of data.mcpToolsLoaded) {
      panel.appendChild(createDetailRow(t.name + ' (' + t.serverName + ')', t.tokens))
    }
  }

  if (data.mcpToolsDeferred.length > 0) {
    const sec = createSection('MCP tools deferred')
    panel.appendChild(sec)
    for (const t of data.mcpToolsDeferred) {
      const row = createElement('div', 'flex items-center justify-between px-4 py-1')
      const n = createElement('span', 'text-[13px] text-t2 truncate')
      n.textContent = t.name + ' (' + t.serverName + ')'
      row.appendChild(n)
      panel.appendChild(row)
    }
  }

  if (data.agents.length > 0) {
    const sec = createSection('Agents')
    panel.appendChild(sec)
    for (const a of data.agents) {
      panel.appendChild(createDetailRow(a.agentType + ' (' + a.source + ')', a.tokens))
    }
  }

  if (data.skills.length > 0) {
    const sec = createSection('Skills')
    panel.appendChild(sec)
    for (const s of data.skills) {
      panel.appendChild(createDetailRow(s.name + ' (' + s.source + ')', s.tokens))
    }
  }

  if (data.apiUsage) {
    const sec = createSection('API usage')
    panel.appendChild(sec)
    panel.appendChild(createDetailRow('Input', data.apiUsage.inputTokens))
    panel.appendChild(createDetailRow('Output', data.apiUsage.outputTokens))
    panel.appendChild(createDetailRow('Cache creation', data.apiUsage.cacheCreationInputTokens))
    panel.appendChild(createDetailRow('Cache read', data.apiUsage.cacheReadInputTokens))
  }
}

function createContextPopover(onRequest: () => void, onCompact?: () => void): {
  trigger: HTMLButtonElement
  setBreakdown: (data: ContextBreakdownData | null) => void
  hide: () => void
  setStreaming: (streaming: boolean) => void
} {
  // contextPopover keeps its own 3-state click handler (close-if-open /
  // show-loading / show-panel) instead of going through createComboButton —
  // the popover may need to display a Loading placeholder before the
  // breakdown data arrives, which createPopupHost's open/close model does
  // not express. link variant gives us text-t2/hover:text-t1; the className
  // option layers on mono / text-[14px] / cursor-pointer / hidden.
  const trigger = createTextButton({
    text: '',
    variant: 'link',
    className: 'text-[15px] cursor-pointer hidden',
  })
  trigger.dataset.testid = 'context-trigger'

  const panel = createPopupPanel({ className: 'max-h-[60dvh] overflow-y-auto max-w-md' })

  let breakdown: ContextBreakdownData | null = null
  let dataReceived = false
  let open = false
  let streaming = false

  const outsideClick = createOutsideClick(trigger, panel, () => {
    open = false
    panel.classList.add('hidden')
  })

  const showPanel = () => {
    open = true
    if (!panel.parentElement) document.body.appendChild(panel)
    panel.classList.remove('hidden')
    panel.innerHTML = ''
    if (breakdown) {
      renderBreakdownContent(panel, breakdown, onCompact, streaming)
    } else {
      panel.appendChild(createNode('div', {
        className: 'px-4 py-6 text-center text-[13px] text-t3',
        text: 'Send a message first to see context usage.',
      }))
    }
  }

  const showLoading = () => {
    open = true
    if (!panel.parentElement) document.body.appendChild(panel)
    panel.classList.remove('hidden')
    panel.innerHTML = ''
    panel.appendChild(createNode('div', {
      className: 'px-4 py-6 text-center text-[13px] text-t3',
      text: 'Loading...',
    }))
  }

  const closePanel = () => {
    open = false
    panel.classList.add('hidden')
    outsideClick.remove()
  }

  trigger.addEventListener('click', () => {
    onRequest()
    if (open) {
      closePanel()
      return
    }
    if (dataReceived) {
      showPanel()
    } else {
      showLoading()
    }
    outsideClick.add()
  })

  const setBreakdown = (data: ContextBreakdownData | null) => {
    breakdown = data
    dataReceived = true
    if (open) {
      if (data) {
        renderBreakdownContent(panel, data, onCompact, streaming)
      } else {
        panel.innerHTML = ''
        panel.appendChild(createNode('div', {
          className: 'px-4 py-6 text-center text-[13px] text-t3',
          text: 'Send a message first to see context usage.',
        }))
      }
    }
  }

  const setStreaming = (s: boolean) => {
    streaming = s
    if (open && breakdown) {
      renderBreakdownContent(panel, breakdown, onCompact, streaming)
    }
  }

  return { trigger, setBreakdown, hide: closePanel, setStreaming }
}

export function createHeader(opts: {
  onModelSelect: (provider: string, model: string) => void
  onEngineSwitch: (engineID: string) => void
  onEngineNew: () => void
  onContextRequest?: () => void
  onContextCompact?: () => void
  onRequestQuota?: () => void
}): HeaderHandles {
  const root = createElement('header', 'sticky top-0 z-30 card-bg')
  root.style.paddingTop = 'env(safe-area-inset-top)'

  const inner = createElement('div', 'flex items-center gap-2 px-4 h-11 max-w-2xl mx-auto')

  const hamburgerHandler = { fn: () => {} }
  // size=sm (w-7 h-7) widens the click target from the menu SVG's intrinsic
  // 18×18 to 28×28 — friendlier on touch screens. rounded-full adds no
  // visible effect because the variant carries no background color.
  const hamburgerWrap = createIconButton({
    icon: 'menu',
    label: 'Menu',
    variant: 'ghost',
    size: 'auto',
    iconSize: 18,
    // p-2 -m-2: touch target 18→34px, visual footprint unchanged.
    className: 'p-2 -m-2',
    onClick: () => hamburgerHandler.fn(),
  })

  const debugPanel = createPopupPanel({ className: 'flex flex-col h-[60vh]' })
  const copyBtn = createCopyButton(() => getDebugLogs().join('\n'))
  copyBtn.classList.add('absolute', 'top-2', 'right-2', 'text-t3', 'z-10')
  debugPanel.appendChild(copyBtn)

  const debugList = createElement('div', 'flex-1 overflow-y-auto p-2 min-h-0 space-y-0.5')
  debugPanel.appendChild(debugList)

  let debugOpen = false
  const renderDebugLogs = () => {
    if (!debugOpen) return
    const logs = getDebugLogs()
    debugList.innerHTML = ''
    for (const line of logs) {
      const el = createElement('div', 'text-[11px] text-t3 font-mono break-all leading-tight')
      el.textContent = line
      debugList.appendChild(el)
    }
    debugList.scrollTop = debugList.scrollHeight
  }

  const gbotWrap = createTextButton({
    text: '',
    variant: 'link',
    className: 'group flex items-center p-2 -m-2',
    onDblClick: (e) => {
      e.stopPropagation()
      debugOpen = !debugOpen
      if (debugOpen) {
        if (!debugPanel.parentElement) document.body.appendChild(debugPanel)
        debugPanel.classList.remove('hidden')
        renderDebugLogs()
        debugOutside.add()
      } else {
        debugPanel.classList.add('hidden')
        debugOutside.remove()
      }
    },
  })

  const debugOutside = createOutsideClick(gbotWrap, debugPanel, () => {
    debugOpen = false
    debugPanel.classList.add('hidden')
  })

  onDebugLog(renderDebugLogs)
  const wordmark = createElement(
    'span',
    'text-[15px] font-semibold tracking-tight text-t3 transition-colors group-hover:text-blue',
  )
  wordmark.textContent = 'GBot'
  gbotWrap.appendChild(wordmark)

  const modelPicker = createModelPicker(opts.onModelSelect, opts.onRequestQuota)
  const enginePicker = createEnginePicker(opts.onEngineSwitch, opts.onEngineNew)
  // Same touch-target trick for the picker combos (text is small).
  for (const w of [enginePicker.wrap, modelPicker.wrap]) {
    w.style.padding = '8px'
    w.style.margin = '-8px'
  }

  const sep = () => {
    const s = createNode('span', {
      className: 'text-t3 text-[15px]',
      text: '\u203a',
    })
    return s
  }

  const breadcrumb = createNode('div', {
    className: 'flex items-center gap-1.5',
    style: { lineHeight: '14px' },
  })
  breadcrumb.appendChild(enginePicker.wrap)
  breadcrumb.appendChild(sep())
  breadcrumb.appendChild(modelPicker.wrap)

  inner.appendChild(hamburgerWrap)
  inner.appendChild(gbotWrap)
  inner.appendChild(breadcrumb)

  const spacer = createElement('div', 'flex-1')
  inner.appendChild(spacer)

  const ctxPopover = createContextPopover(
    () => opts.onContextRequest?.(),
    opts.onContextCompact,
  )
  // Same touch-target trick via inline style: the recipe's px-0 py-0 would
  // fight utility classes, but inline style always wins the cascade.
  ctxPopover.trigger.style.padding = '8px'
  ctxPopover.trigger.style.margin = '-8px'
  inner.appendChild(ctxPopover.trigger)

  root.appendChild(inner)

  const setStatus = (connected: boolean) => {
    wordmark.className =
      'text-[15px] font-semibold tracking-tight transition-colors group-hover:text-blue ' +
      (connected ? 'text-blue pulse' : 'text-t3')
  }

  const setModel = (model: string) => {
    if (model) modelPicker.setLabel(model)
  }

  const onHamburgerClick = (handler: () => void) => {
    hamburgerHandler.fn = handler
  }

  const setModels = (models: { provider: string; model: string }[], curProvider: string, curModel: string) => {
    modelPicker.setModels(models, curProvider, curModel)
  }

  const setEngines = (engines: EngineEntry[], activeID: string) => {
    enginePicker.setEngines(engines, activeID)
  }

  const setContext = (used: number, total: number) => {
    if (total <= 0) {
      ctxPopover.trigger.classList.add('hidden')
      return
    }
    ctxPopover.trigger.classList.remove('hidden')
    const pct = used * 100 / total
    // Toggle color classes in-place instead of replacing className wholesale
    // — the trigger was created via createTextButton which composes the
    // link variant + compoundVariants (px-0 py-0), and a full replace would
    // drop the padding reset and inflate the trigger.
    ctxPopover.trigger.classList.remove('text-t2', 'text-red-500', 'text-amber-500')
    let color = 'text-t2'
    if (pct >= 90) color = 'text-red-500'
    else if (pct >= 80) color = 'text-amber-500'
    ctxPopover.trigger.classList.add(color)
    ctxPopover.trigger.textContent = formatTokenCount(used) + '/' + formatTokenCount(total)
  }

  const setStreaming = (s: boolean) => {
    ctxPopover.setStreaming(s)
    modelPicker.setStreaming(s)
  }

  return { root, setStatus, setModel, onHamburgerClick, setModels, setQuota: modelPicker.setQuota, setEngines, setContext, setContextBreakdown: ctxPopover.setBreakdown, hideContextBreakdown: ctxPopover.hide, setStreaming }
}
