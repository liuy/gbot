// @ts-expect-error fuzzysearch has no types
import fuzzysearch from 'fuzzysearch'
import { createPopupPanel } from './utils'

export interface HeaderHandles {
  root: HTMLElement
  setStatus: (connected: boolean) => void
  setModel: (model: string) => void
  onHamburgerClick: (handler: () => void) => void
  setModels: (models: { provider: string; model: string }[], curProvider: string, curModel: string) => void
  setEngines: (engines: EngineEntry[], activeID: string) => void
}

interface ModelEntry {
  provider: string
  model: string
}

interface EngineEntry {
  id: string
  name: string
  model: string
}

function createModelPicker(
  onSelect: (provider: string, model: string) => void,
): { wrap: HTMLElement; setModels: (models: ModelEntry[], curProvider: string, curModel: string) => void } {
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

function createEnginePicker(
  onSwitch: (engineID: string) => void,
  onNew: () => void,
): { wrap: HTMLElement; setEngines: (engines: EngineEntry[], activeID: string) => void } {
  const wrap = document.createElement('div')
  wrap.className = 'relative'

  const trigger = document.createElement('button')
  trigger.className = 'mono text-[14px] text-t2 hover:text-t1 transition-colors'

  const panel = createPopupPanel()

  const listContainer = document.createElement('div')
  listContainer.className = 'max-h-[50dvh] overflow-y-auto p-1'
  panel.appendChild(listContainer)

  let allEngines: EngineEntry[] = []
  let activeID = ''
  let open = false

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

  const closePanel = () => {
    open = false
    panel.classList.add('hidden')
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
      renderList()
      document.addEventListener('mousedown', onDocClick)
    } else closePanel()
  })

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

export function createHeader(opts: {
  onModelSelect: (provider: string, model: string) => void
  onEngineSwitch: (engineID: string) => void
  onEngineNew: () => void
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
  const wordmark = document.createElement('span')
  wordmark.className =
    'text-[14px] font-semibold tracking-tight text-t3 transition-colors group-hover:text-blue'
  wordmark.textContent = 'GBot'
  gbotWrap.appendChild(wordmark)

  const modelPicker = createModelPicker(opts.onModelSelect)
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

  return { root, setStatus, setModel, onHamburgerClick, setModels, setEngines }
}
