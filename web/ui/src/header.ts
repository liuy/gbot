// @ts-expect-error fuzzysearch has no types
import fuzzysearch from 'fuzzysearch'
import { createPopupPanel, createOutsideClick, createPopupHost, formatTokenCount } from './utils'
import { createCopyButton } from './utils/copy_button'
import { getDebugLogs, onDebugLog } from './log'
import type { ContextBreakdownData } from './types'

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
): { wrap: HTMLElement; setModels: (models: ModelEntry[], curProvider: string, curModel: string) => void; setQuota: (provider: string, quota: string) => void } {
  const wrap = document.createElement('div')
  wrap.className = 'relative'

  const trigger = document.createElement('button')
  trigger.className = 'mono text-[14px] text-t2 hover:text-t1 transition-colors'

  const panel = createPopupPanel()

  const searchInput = document.createElement('textarea')
  searchInput.rows = 1
  searchInput.placeholder = 'Search models...'
  searchInput.setAttribute('autocapitalize', 'off')
  searchInput.setAttribute('autocorrect', 'off')
  searchInput.spellcheck = false
  searchInput.className =
    'w-full bg-transparent px-4 py-2.5 text-[13px] text-t1 placeholder-t3 outline-none border-b border-hairline resize-none'
  searchInput.style.fontFamily = 'inherit'
  searchInput.style.fontSize = 'inherit'
  ;(searchInput.style as unknown as Record<string, string>).webkitAppearance = 'none'
  panel.appendChild(searchInput)

  const listContainer = document.createElement('div')
  listContainer.className = 'max-h-[50dvh] overflow-y-auto p-1'
  panel.appendChild(listContainer)

  let allModels: ModelEntry[] = []
  let currentProvider = ''
  let currentModel = ''
  const quotaByProvider = new Map<string, string>()

  const renderList = () => {
    listContainer.innerHTML = ''
    const query = searchInput.value.trim()
    const filtered = allModels.filter(
      (m) => !query || fuzzysearch(query.toLowerCase(), (m.provider + '/' + m.model).toLowerCase()),
    )

    if (filtered.length === 0) {
      const empty = document.createElement('div')
      empty.className = 'px-3 py-4 text-center text-[13px] text-t3'
      empty.textContent = 'No models found'
      listContainer.appendChild(empty)
      return
    }

    let lastProvider = ''
    for (const entry of filtered) {
      if (entry.provider !== lastProvider) {
        lastProvider = entry.provider
        const header = document.createElement('div')
        header.className = 'px-3 pt-2 pb-1 mono text-[13px] text-t3 uppercase tracking-wider'
        header.textContent = entry.provider
        listContainer.appendChild(header)
      }
      const item = document.createElement('button')
      const isActive = entry.provider === currentProvider && entry.model === currentModel
      item.className =
        'w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50'
      const span = document.createElement('span')
      span.className = 'text-[13px] ' + (isActive ? 'text-blue' : 'text-t2')
      span.textContent = entry.model
      item.appendChild(span)
      const qText = entry.quota ?? quotaByProvider.get(entry.provider)
      if (qText) {
        const q = document.createElement('span')
        q.className = 'text-[10px] text-t3 ml-auto'
        q.textContent = qText
        item.appendChild(q)
      }
      item.addEventListener('click', () => {
        currentProvider = entry.provider
        currentModel = entry.model
        trigger.textContent = entry.model
        onSelect(entry.provider, entry.model)
        closePanel()
      })
      listContainer.appendChild(item)
    }
  }

  searchInput.addEventListener('input', renderList)

  const host = createPopupHost({
    trigger: wrap,
    panel,
    onOpen: () => {
      searchInput.value = ''
      renderList()
      setTimeout(() => searchInput.focus(), 50)
      onRequestQuota?.()
    },
    onClose: () => { searchInput.value = '' },
  })
  const closePanel = () => host.close()
  trigger.addEventListener('click', () => host.toggle())

  trigger.textContent = ''

  const setModels = (models: ModelEntry[], curProvider: string, curModel: string) => {
    allModels = models
    currentProvider = curProvider
    currentModel = curModel
    if (curModel) trigger.textContent = curModel
  }

  const setQuota = (provider: string, quota: string) => {
    quotaByProvider.set(provider, quota)
    if (host.isOpen()) renderList()
  }

  wrap.appendChild(trigger)
  return { wrap, setModels, setQuota }
}

function createEnginePicker(
  onSwitch: (engineID: string) => void,
  onNew: () => void,
): { wrap: HTMLElement; setEngines: (engines: EngineEntry[], activeID: string) => void } {
  const wrap = document.createElement('div')
  wrap.className = 'relative'

  const trigger = document.createElement('button')
  trigger.className = 'mono text-[14px] text-t2 hover:text-t1 transition-colors'

  const panel = createPopupPanel()
  panel.dataset.testid = 'engine-picker-panel'

  const listContainer = document.createElement('div')
  listContainer.className = 'max-h-[50dvh] overflow-y-auto p-1'
  panel.appendChild(listContainer)

  let allEngines: EngineEntry[] = []
  let activeID = ''

  const renderList = () => {
    listContainer.innerHTML = ''
    for (const entry of allEngines) {
      const isActive = entry.id === activeID
      const item = document.createElement('button')
      item.className =
        'w-full flex items-center gap-2 px-3 py-2 rounded-lg text-left transition-colors hover:bg-ink3/50'
      const dot = document.createElement('span')
      dot.className = 'h-2 w-2 rounded-full shrink-0 ' + (isActive ? 'bg-blue' : 'bg-t3/30')
      item.appendChild(dot)
      const nameSpan = document.createElement('span')
      nameSpan.className = 'text-[13px] ' + (isActive ? 'text-blue' : 'text-t2')
      nameSpan.textContent = entry.name || entry.id
      item.appendChild(nameSpan)
      const modelSpan = document.createElement('span')
      modelSpan.className = 'text-[13px] text-t3 ml-2'
      modelSpan.textContent = entry.model
      item.appendChild(modelSpan)
      if (isActive) {
        item.addEventListener('click', () => closePanel())
      } else {
        item.addEventListener('click', () => {
          onSwitch(entry.id)
          closePanel()
        })
      }
      listContainer.appendChild(item)
    }
    // Footer: + icon only
    const footer = document.createElement('button')
    footer.className =
      'w-full flex items-center justify-center px-3 py-2 rounded-lg transition-colors hover:bg-ink3/50 border-t border-hairline mt-1'
    footer.innerHTML =
      '<svg width="14" height="14" viewBox="0 0 14 14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round"><path d="M7 2v10M2 7h10"/></svg>'
    footer.style.color = 'var(--color-blue)'
    footer.addEventListener('click', () => {
      onNew()
      closePanel()
    })
    listContainer.appendChild(footer)
  }

  const host = createPopupHost({
    trigger: wrap,
    panel,
    onOpen: () => renderList(),
  })
  const closePanel = () => host.close()
  trigger.addEventListener('click', () => host.toggle())

  trigger.textContent = ''

  const setEngines = (engines: EngineEntry[], active: string) => {
    allEngines = engines
    activeID = active
    const cur = engines.find((e) => e.id === active)
    if (cur) trigger.textContent = cur.name || cur.id
  }

  wrap.appendChild(trigger)
  return { wrap, setEngines }
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
  const sec = document.createElement('div')
  sec.className = 'pt-2 pb-1 px-4'
  const h = document.createElement('div')
  h.className = 'mono text-[11px] text-t3 uppercase tracking-wider'
  h.textContent = title
  sec.appendChild(h)
  return sec
}

function createDetailRow(name: string, tokens: number): HTMLDivElement {
  const row = document.createElement('div')
  row.className = 'flex items-center justify-between px-4 py-1'
  const n = document.createElement('span')
  n.className = 'text-[13px] text-t2 truncate'
  n.textContent = name
  row.appendChild(n)
  const t = document.createElement('span')
  t.className = 'mono text-[12px] text-t3 ml-2 shrink-0'
  t.textContent = formatTokenCount(tokens)
  row.appendChild(t)
  return row
}

function renderBreakdownContent(panel: HTMLDivElement, data: ContextBreakdownData) {
  panel.innerHTML = ''

  const titleBar = document.createElement('div')
  titleBar.className = 'px-4 pt-3 pb-1'
  const title = document.createElement('div')
  title.className = 'text-[14px] font-semibold text-t1'
  title.textContent = 'Context Usage'
  titleBar.appendChild(title)
  const total = document.createElement('div')
  total.className = 'mono text-[12px] text-t3 mt-0.5'
  total.textContent =
    formatTokenCount(data.totalTokens) + ' / ' + formatTokenCount(data.contextWindow) +
    ' (' + data.percentage.toFixed(1) + '%)'
  titleBar.appendChild(total)
  panel.appendChild(titleBar)

  const barWrap = document.createElement('div')
  barWrap.className = 'px-4 py-2'
  const bar = document.createElement('div')
  bar.className = 'flex h-2 rounded-full overflow-hidden bg-ink3/50'
  bar.dataset.testid = 'context-bar'
  for (const cat of data.categories) {
    if (cat.tokens === 0) continue
    const seg = document.createElement('div')
    seg.style.width = cat.percentage + '%'
    seg.style.backgroundColor = ansiToCss(cat.color)
    seg.dataset.testid = 'context-segment'
    seg.dataset.name = cat.name
    bar.appendChild(seg)
  }
  barWrap.appendChild(bar)
  panel.appendChild(barWrap)

  const listWrap = document.createElement('div')
  listWrap.className = 'pb-1'
  for (const cat of data.categories) {
    const row = document.createElement('div')
    row.className = 'flex items-center gap-2 px-4 py-1'
    const dot = document.createElement('span')
    dot.className = 'h-2 w-2 rounded-full shrink-0'
    dot.style.backgroundColor = ansiToCss(cat.color)
    row.appendChild(dot)
    const name = document.createElement('span')
    name.className = 'text-[13px] text-t2 flex-1 truncate'
    name.textContent = cat.name
    row.appendChild(name)
    const tok = document.createElement('span')
    tok.className = 'mono text-[12px] text-t3'
    tok.textContent = formatTokenCount(cat.tokens)
    row.appendChild(tok)
    const pct = document.createElement('span')
    pct.className = 'text-[11px] text-t3 w-10 text-right'
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
      const row = document.createElement('div')
      row.className = 'flex items-center justify-between px-4 py-1'
      const n = document.createElement('span')
      n.className = 'text-[13px] text-t2 truncate'
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

function createContextPopover(onRequest: () => void): {
  trigger: HTMLButtonElement
  setBreakdown: (data: ContextBreakdownData | null) => void
  hide: () => void
} {
  const trigger = document.createElement('button')
  trigger.className = 'mono text-[14px] text-t2 hover:text-t1 transition-colors cursor-pointer hidden'
  trigger.dataset.testid = 'context-trigger'

  const panel = createPopupPanel({ className: 'max-h-[60dvh] overflow-y-auto max-w-md' })

  let breakdown: ContextBreakdownData | null = null
  let dataReceived = false
  let open = false

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
      renderBreakdownContent(panel, breakdown)
    } else {
      const hint = document.createElement('div')
      hint.className = 'px-4 py-6 text-center text-[13px] text-t3'
      hint.textContent = 'Send a message first to see context usage.'
      panel.appendChild(hint)
    }
  }

  const showLoading = () => {
    open = true
    if (!panel.parentElement) document.body.appendChild(panel)
    panel.classList.remove('hidden')
    panel.innerHTML = ''
    const loading = document.createElement('div')
    loading.className = 'px-4 py-6 text-center text-[13px] text-t3'
    loading.textContent = 'Loading...'
    panel.appendChild(loading)
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
        renderBreakdownContent(panel, data)
      } else {
        panel.innerHTML = ''
        const hint = document.createElement('div')
        hint.className = 'px-4 py-6 text-center text-[13px] text-t3'
        hint.textContent = 'Send a message first to see context usage.'
        panel.appendChild(hint)
      }
    }
  }

  return { trigger, setBreakdown, hide: closePanel }
}

export function createHeader(opts: {
  onModelSelect: (provider: string, model: string) => void
  onEngineSwitch: (engineID: string) => void
  onEngineNew: () => void
  onContextRequest?: () => void
  onRequestQuota?: () => void
}): HeaderHandles {
  const root = document.createElement('header')
  root.className = 'sticky top-0 z-30 card-bg'

  const inner = document.createElement('div')
  inner.className = 'flex items-center gap-2 px-4 h-11 max-w-2xl mx-auto'

  const hamburgerWrap = document.createElement('button')
  hamburgerWrap.className =
    'flex items-center text-t2 hover:text-t1 transition-colors'
  hamburgerWrap.innerHTML =
    '<svg width="18" height="14" viewBox="0 0 18 14" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round">' +
    '<rect x="1" y="1" width="16" height="2.5" rx="1.25" fill="currentColor" stroke="none"/>' +
    '<rect x="3" y="10.5" width="12" height="2.5" rx="1.25" fill="currentColor" stroke="none"/>' +
    '</svg>'

  const hamburgerHandler = { fn: () => {} }
  hamburgerWrap.addEventListener('click', () => hamburgerHandler.fn())

  const gbotWrap = document.createElement('button')
  gbotWrap.className = 'group flex items-center'

  const debugPanel = createPopupPanel({ className: 'flex flex-col h-[60vh]' })
  const copyBtn = createCopyButton(() => getDebugLogs().join('\n'))
  copyBtn.classList.add('absolute', 'top-2', 'right-2', 'text-t3', 'z-10')
  debugPanel.appendChild(copyBtn)

  const debugList = document.createElement('div')
  debugList.className = 'flex-1 overflow-y-auto p-2 min-h-0 space-y-0.5'
  debugPanel.appendChild(debugList)

  let debugOpen = false
  const renderDebugLogs = () => {
    if (!debugOpen) return
    const logs = getDebugLogs()
    debugList.innerHTML = ''
    for (const line of logs) {
      const el = document.createElement('div')
      el.className = 'text-[11px] text-t3 font-mono break-all leading-tight'
      el.textContent = line
      debugList.appendChild(el)
    }
    debugList.scrollTop = debugList.scrollHeight
  }

  const debugOutside = createOutsideClick(gbotWrap, debugPanel, () => {
    debugOpen = false
    debugPanel.classList.add('hidden')
  })

  gbotWrap.addEventListener('dblclick', (e) => {
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
  })
  onDebugLog(renderDebugLogs)
  const wordmark = document.createElement('span')
  wordmark.className =
    'text-[14px] font-semibold tracking-tight text-t3 transition-colors group-hover:text-blue'
  wordmark.textContent = 'GBot'
  gbotWrap.appendChild(wordmark)

  const modelPicker = createModelPicker(opts.onModelSelect, opts.onRequestQuota)
  const enginePicker = createEnginePicker(opts.onEngineSwitch, opts.onEngineNew)

  const sep = () => {
    const s = document.createElement('span')
    s.className = 'text-t3 text-[14px]'
    s.textContent = '\u203a'
    return s
  }

  const breadcrumb = document.createElement('div')
  breadcrumb.className = 'flex items-center gap-1.5'
  breadcrumb.style.lineHeight = '14px'
  breadcrumb.appendChild(enginePicker.wrap)
  breadcrumb.appendChild(sep())
  breadcrumb.appendChild(modelPicker.wrap)

  inner.appendChild(hamburgerWrap)
  inner.appendChild(gbotWrap)
  inner.appendChild(breadcrumb)

  const spacer = document.createElement('div')
  spacer.className = 'flex-1'
  inner.appendChild(spacer)

  const ctxPopover = createContextPopover(() => opts.onContextRequest?.())
  inner.appendChild(ctxPopover.trigger)

  root.appendChild(inner)

  const setStatus = (connected: boolean) => {
    wordmark.className =
      'text-[14px] font-semibold tracking-tight transition-colors group-hover:text-blue ' +
      (connected ? 'text-blue pulse' : 'text-t3')
  }

  const setModel = (model: string) => {
    if (model) {
      const trigger = modelPicker.wrap.querySelector('button') as HTMLButtonElement
      if (trigger) trigger.textContent = model
    }
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
    let color = 'text-t2'
    if (pct >= 90) color = 'text-red-500'
    else if (pct >= 80) color = 'text-amber-500'
    ctxPopover.trigger.className = 'mono text-[14px] hover:text-t1 transition-colors cursor-pointer ' + color
    ctxPopover.trigger.textContent = formatTokenCount(used) + '/' + formatTokenCount(total)
  }

  return { root, setStatus, setModel, onHamburgerClick, setModels, setQuota: modelPicker.setQuota, setEngines, setContext, setContextBreakdown: ctxPopover.setBreakdown, hideContextBreakdown: ctxPopover.hide }
}
