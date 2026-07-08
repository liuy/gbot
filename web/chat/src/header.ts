// @ts-expect-error fuzzysearch has no types
import fuzzysearch from 'fuzzysearch'

export interface HeaderHandles {
  root: HTMLElement
  setStatus: (connected: boolean) => void
  setAgentModel: (agent: string, model: string) => void
  onHamburgerClick: (handler: () => void) => void
  setModels: (models: { provider: string; model: string }[], curProvider: string, curModel: string) => void
}

interface ModelEntry {
  provider: string
  model: string
}

function createModelPicker(
  onSelect: (provider: string, model: string) => void,
): { wrap: HTMLElement; setModels: (models: ModelEntry[], curProvider: string, curModel: string) => void } {
  const wrap = document.createElement('div')
  wrap.className = 'relative'

  const trigger = document.createElement('button')
  trigger.className = 'mono text-[12px] text-t2 hover:text-t1 transition-colors'

  const panel = document.createElement('div')
  panel.className =
    'fixed left-1/2 -translate-x-1/2 top-12 border border-hairline rounded-xl shadow-2xl modal-enter z-40 hidden w-[90vw] max-w-sm'
  panel.style.background = 'rgba(12, 16, 24, 0.75)'
  panel.style.backdropFilter = 'blur(20px) saturate(1.5)'
  panel.style.setProperty('-webkit-backdrop-filter', 'blur(20px) saturate(1.5)')

  const searchInput = document.createElement('input')
  searchInput.type = 'text'
  searchInput.placeholder = 'Search models...'
  searchInput.className =
    'w-full bg-transparent px-4 py-2.5 text-[14px] text-t1 placeholder-t3 outline-none border-b border-hairline'
  panel.appendChild(searchInput)

  const listContainer = document.createElement('div')
  listContainer.className = 'max-h-[50dvh] overflow-y-auto p-1'
  panel.appendChild(listContainer)

  let allModels: ModelEntry[] = []
  let currentProvider = ''
  let currentModel = ''
  let open = false

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
        header.className = 'px-3 pt-2 pb-1 mono text-[10px] text-t3 uppercase tracking-wider'
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

  const closePanel = () => {
    open = false
    panel.classList.add('hidden')
    searchInput.value = ''
    document.removeEventListener('mousedown', onDocClick)
  }
  const onDocClick = (e: MouseEvent) => {
    if (!wrap.contains(e.target as Node) && !panel.contains(e.target as Node)) closePanel()
  }
  trigger.addEventListener('click', () => {
    open = !open
    if (open) {
      if (!panel.parentElement) document.body.appendChild(panel)
      panel.classList.remove('hidden')
      searchInput.value = ''
      renderList()
      setTimeout(() => searchInput.focus(), 50)
      document.addEventListener('mousedown', onDocClick)
    } else closePanel()
  })

  trigger.textContent = ''

  const setModels = (models: ModelEntry[], curProvider: string, curModel: string) => {
    allModels = models
    currentProvider = curProvider
    currentModel = curModel
    if (curModel) trigger.textContent = curModel
  }

  wrap.appendChild(trigger)
  return { wrap, setModels }
}

export function createHeader(opts: { onModelSelect: (provider: string, model: string) => void }): HeaderHandles {
  const root = document.createElement('header')
  root.className = 'sticky top-0 z-30 card-bg'

  const inner = document.createElement('div')
  inner.className = 'flex items-center gap-2 px-4 h-11 max-w-2xl mx-auto'

  const hamburgerWrap = document.createElement('button')
  hamburgerWrap.className =
    'flex items-center text-t2 hover:text-t1 transition-colors'
  hamburgerWrap.innerHTML =
    '<svg width="20" height="14" viewBox="0 0 20 14" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round">' +
    '<rect x="2" y="2" width="16" height="2.5" rx="1.25" fill="currentColor" stroke="none"/>' +
    '<rect x="4" y="9.5" width="12" height="2.5" rx="1.25" fill="currentColor" stroke="none"/>' +
    '</svg>'

  const hamburgerHandler = { fn: () => {} }
  hamburgerWrap.addEventListener('click', () => hamburgerHandler.fn())

  const gbotWrap = document.createElement('button')
  gbotWrap.className = 'group flex items-center'
  const wordmark = document.createElement('span')
  wordmark.className =
    'text-[14px] font-semibold tracking-tight text-t3 transition-colors group-hover:text-blue'
  wordmark.textContent = 'GBot'
  gbotWrap.appendChild(wordmark)

  const agentSpan = document.createElement('span')
  agentSpan.className = 'text-[12px] text-t2'
  agentSpan.textContent = ''

  const separator = document.createElement('span')
  separator.className = 'text-t3 text-[10px]'
  separator.textContent = '\u203a'

  const modelPicker = createModelPicker(opts.onModelSelect)

  const breadcrumb = document.createElement('div')
  breadcrumb.className = 'flex items-baseline gap-1.5'
  breadcrumb.appendChild(agentSpan)
  breadcrumb.appendChild(separator)
  breadcrumb.appendChild(modelPicker.wrap)

  inner.appendChild(hamburgerWrap)
  inner.appendChild(gbotWrap)
  inner.appendChild(breadcrumb)

  const spacer = document.createElement('div')
  spacer.className = 'flex-1'
  inner.appendChild(spacer)

  root.appendChild(inner)

  const setStatus = (connected: boolean) => {
    wordmark.className =
      'text-[14px] font-semibold tracking-tight transition-colors group-hover:text-blue ' +
      (connected ? 'text-blue pulse' : 'text-t3')
  }

  const setAgentModel = (agent: string, model: string) => {
    agentSpan.textContent = agent
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

  return { root, setStatus, setAgentModel, onHamburgerClick, setModels }
}
