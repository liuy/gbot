import { createElement, createNode } from './dom'
import { renderIcon } from './icons'
import { createIconButton } from './buttons'
import { getThemePref, setThemePref, getResolvedTheme, type ThemePref } from './theme'
import { HLJS_THEMES, getSavedHljsTheme, saveHljsTheme, applyHljsTheme } from './hljs_themes'

// Settings page — provider CRUD against /api/settings/*. The page is a
// full-screen overlay (z above sidebar and artifact sheet) opened from the
// sidebar gear. UI ported from the settings prototype (DOM structure,
// interactions, and copy are the source of truth).

// ---------------------------------------------------------------------------
// Wire types + API helpers
// ---------------------------------------------------------------------------

export interface SettingsModelMeta {
  context?: string | number
  max_tokens?: string | number
  input?: string[]
  thinking?: string
}

export interface SettingsProvider {
  name: string
  url: string
  keys: string[]
  models: Record<string, SettingsModelMeta>
  type?: string // '' | 'auto' | 'openai' | 'anthropic' | 'responses'
  free?: boolean
  extra_params?: Record<string, unknown>
}

export interface SettingsPayload {
  providers: SettingsProvider[]
  default: { provider: string; model: string }
}

const jsonHeaders = { 'Content-Type': 'application/json' }

export async function fetchSettings(): Promise<SettingsPayload> {
  const res = await fetch('/api/settings/providers')
  if (!res.ok) throw new Error(`settings fetch failed: ${res.status}`)
  return res.json()
}

export async function saveSettings(providers: SettingsProvider[]): Promise<void> {
  const res = await fetch('/api/settings/providers', {
    method: 'PUT',
    headers: jsonHeaders,
    body: JSON.stringify(providers),
  })
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // non-JSON failure body — keep the status text
    }
    throw new Error(msg)
  }
}

export interface TestResult {
  ok: boolean
  latencyMs?: number
  error?: string
}

export async function saveDefaultModel(provider: string, model: string): Promise<void> {
  const res = await fetch('/api/settings/default', {
    method: 'PUT',
    headers: jsonHeaders,
    body: JSON.stringify({ provider, model }),
  })
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // status text is enough
    }
    throw new Error(msg)
  }
}
export async function testProvider(p: SettingsProvider): Promise<TestResult> {
  const res = await fetch('/api/settings/test', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify(p),
  })
  if (!res.ok) return { ok: false, error: `HTTP ${res.status}` }
  return res.json()
}

export type ModelsResult =
  | { mode: 'fetched'; models: Array<string | { id: string; context?: string; input?: string[] }> }
  | { mode: 'manual' }
  | { mode: 'error'; error: string }

export async function fetchProviderModels(
  url: string,
  key: string,
  type: string,
  free = false,
): Promise<ModelsResult> {
  // free stays out of the body unless requested — the wire shape matches
  // the old callers and the backend's omitempty semantics.
  const body: Record<string, unknown> = { url, key, type }
  if (free) body.free = true
  const res = await fetch('/api/settings/models', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify(body),
  })
  if (!res.ok) return { mode: 'error', error: `HTTP ${res.status}` }
  return res.json()
}

// ---------------------------------------------------------------------------
// Page
// ---------------------------------------------------------------------------

export interface SettingsPageHandles {
  root: HTMLElement // fixed inset-0 full-screen overlay, starts hidden (display:none)
  open: () => void // display + fetchSettings + render home
  close: () => void
  isOpen: () => boolean
}

// Canonical display/storage order for input modalities — toggle history
// must not decide whether the row reads "text image" or "image text".
const MODALITY_ORDER = ['text', 'image', 'audio', 'video']
const modalityRank = (m: string) => {
  const i = MODALITY_ORDER.indexOf(m)
  return i < 0 ? MODALITY_ORDER.length : i
}

// The full effort axis accepted by pkg/llm (NormalizeThinkingMode).
const THINK_OPTS = ['none', 'auto', 'low', 'medium', 'high', 'max'] as const
const TOAST_MS = 2200

const TYPE_LABELS: Record<string, string> = {
  openai: 'OPENAI',
  responses: 'RESPONSES',
  anthropic: 'ANTHROPIC',
}
const TYPE_BADGE_CLASS: Record<string, string> = {
  openai: 'text-[#7fd4ff] bg-blue/10',
  responses: 'text-[#c9a3ff] bg-[#9D5CFF]/15',
  anthropic: 'text-[#e8b98a] bg-[#FFB547]/10',
}
const AUTO_BADGE_CLASS = 'text-t2 bg-t2/10'

const EYE =
  '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M1 12s4-7 11-7 11 7 11 7-4 7-11 7S1 12 1 12z"/><circle cx="12" cy="12" r="3"/></svg>'
const EYE_OFF =
  '<svg viewBox="0 0 24 24" width="15" height="15" fill="none" stroke="currentColor" stroke-width="1.5"><path d="M17.94 17.94A10.07 10.07 0 0 1 12 20c-7 0-11-8-11-8a18.45 18.45 0 0 1 5.06-5.94"/><path d="M9.9 4.24A9.12 9.12 0 0 1 12 4c7 0 11 8 11 8a18.5 18.5 0 0 1-2.16 3.19"/><line x1="1" y1="1" x2="23" y2="23"/></svg>'

interface KVRow {
  k: string
  v: string
}

export function createSettingsPage(): SettingsPageHandles {
  // ------------------------------------------------------------------ state
  let payload: SettingsPayload = { providers: [], default: { provider: '', model: '' } }
  let opened = false
  // Form session state. form.provider is a deep copy of the source provider
  // so untouched fields (free, numeric context values, unrendered metadata)
  // round-trip verbatim — only touched fields are overwritten on save.
  const form = {
    isNew: true,
    index: 0,
    provider: null as SettingsProvider | null,
    type: '',
    keys: [] as string[],
    models: {} as Record<string, SettingsModelMeta>,
    kv: [] as KVRow[],
  }

  // ------------------------------------------------------------------ toast
  const toastEl = createNode('div', {
    className:
      'fixed left-1/2 bottom-10 -translate-x-1/2 z-[80] bg-ink3 border border-hairline text-t1 text-[12px] px-4 py-2 rounded-full opacity-0 transition-opacity duration-250 pointer-events-none whitespace-nowrap',
    attrs: { 'data-toast': '' },
  })
  let toastTimer: number | undefined
  const toast = (msg: string) => {
    toastEl.textContent = msg
    // 'toast-show' has no CSS rule — it is the test-observable marker for
    // the opacity-0 toggle that actually drives visibility.
    toastEl.classList.remove('opacity-0')
    toastEl.classList.add('toast-show')
    window.clearTimeout(toastTimer)
    toastTimer = window.setTimeout(() => {
      toastEl.classList.add('opacity-0')
      toastEl.classList.remove('toast-show')
    }, TOAST_MS)
  }

  // ------------------------------------------------------------------ frame
  const root = createNode('div', {
    className: 'fixed inset-0 z-[60] overflow-y-auto bg-bg',
    style: { display: 'none' },
  })
  const frame = createElement('div', 'sidebar-safe-top max-w-[420px] mx-auto min-h-full flex flex-col relative')

  const hdr = createElement(
    'div',
    'sticky top-[env(safe-area-inset-top,0px)] z-10 flex items-center gap-2.5 px-4 py-3.5 bg-bg/60 backdrop-blur border-b border-hairline',
  )
  const backBtn = createNode('div', {
    className: 'w-8 h-8 -ml-1.5 rounded-lg flex items-center justify-center text-t2 cursor-pointer select-none',
    attrs: { 'data-back': '' },
  })
  backBtn.append(renderIcon('chevron-left'))
  const titleEl = createNode('div', { className: 'text-base font-semibold flex-1', attrs: { 'data-title': '' }, text: 'Settings' })
  const jsonAction = createNode('div', {
    className: 'text-blue text-[13px] cursor-pointer select-none px-0.5',
    text: 'JSON',
    attrs: { 'data-json-action': '' },
  })
  hdr.append(backBtn, titleEl, jsonAction)

  // ------------------------------------------------------------------ home
  const homeScreen = createElement('div', '')
  homeScreen.setAttribute('data-screen', 'home')

  const sectionLabel = (text: string, extra?: Node) => {
    const wrap = createElement('div', 'text-[11px] text-t3 font-medium px-4 pt-4 pb-1.5')
    wrap.append(document.createTextNode(text))
    if (extra) wrap.appendChild(extra)
    return wrap
  }

  const provList = createElement('div', 'px-3')
  const addProviderBtn = createNode('div', {
    className: 'flex items-center gap-1.5 px-3.5 py-2.5 text-[13px] text-blue cursor-pointer select-none mb-1',
    attrs: { 'data-add-provider': '' },
  })
  addProviderBtn.append(renderIcon('plus', { size: 16 }), document.createTextNode('Add Provider'))

  const defaultCard = createNode('div', {
    className: 'mx-3 mb-2.5 bg-ink2 border border-hairline rounded-xl overflow-hidden cursor-pointer select-none',
    attrs: { 'data-default-card': '' },
  })
  const defaultHead = createElement('div', 'flex items-center gap-2 px-3.5 py-3')
  const defaultTitle = createElement('div', 'font-mono text-[13px] font-medium flex-1')
  const defaultChev = createNode('span', { className: 'text-t3 text-[15px] leading-none transition-transform', text: '›' })
  defaultHead.append(defaultTitle, defaultChev)
  // Picker: providers as group labels, their models as radio rows.
  const defaultPanel = createElement('div', 'hidden border-t border-hairline max-h-[260px] overflow-y-auto')
  defaultPanel.setAttribute('data-default-panel', '')
  const renderDefaultPanel = () => {
    defaultPanel.replaceChildren()
    for (const p of payload.providers) {
      const group = createElement('div', 'text-[11px] text-t3 font-medium px-3.5 pt-2.5 pb-1')
      group.textContent = p.name
      defaultPanel.appendChild(group)
      for (const name of Object.keys(p.models ?? {})) {
        const isSel = p.name === payload.default.provider && name === payload.default.model
        const row = createElement('div', 'flex items-center gap-2.5 px-3.5 py-2 cursor-pointer')
        row.setAttribute('data-default-model', `${p.name}/${name}`)
        const dot = createElement(
          'span',
          'w-[14px] h-[14px] rounded-full border flex items-center justify-center ' +
            (isSel ? 'border-blue' : 'border-t3'),
        )
        if (isSel) dot.appendChild(createElement('span', 'w-[7px] h-[7px] rounded-full bg-blue'))
        const label = createElement('span', 'text-xs font-mono' + (isSel ? ' text-blue' : ''))
        label.textContent = name
        row.append(dot, label)
        row.addEventListener('click', async (e) => {
          e.stopPropagation()
          try {
            await saveDefaultModel(p.name, name)
            payload.default = { provider: p.name, model: name }
            defaultTitle.textContent = `${p.name} / ${name}`
            renderDefaultPanel()
            toast('Saved · restart daemon to apply')
          } catch (err) {
            toast(`Save failed: ${err instanceof Error ? err.message : 'error'}`)
          }
        })
        defaultPanel.appendChild(row)
      }
    }
  }
  defaultCard.append(defaultHead, defaultPanel)
  defaultHead.addEventListener('click', () => {
    renderDefaultPanel()
    const open = defaultPanel.classList.toggle('hidden')
    defaultChev.classList.toggle('rotate-90', !open)
  })

  const segBtnStyle = (b: HTMLElement, selected: boolean) => {
    b.className =
      'flex-1 py-1.5 text-[10px] rounded-lg cursor-pointer border ' +
      (selected ? 'text-blue border-blue/40 bg-blue/10' : 'bg-ink3 text-t2 border-transparent')
  }

  // GENERAL rows live in one card with divided rows — same shape as the
  // models list. Theme and Highlights expand in place (select applies
  // immediately; nothing to save).
  const generalCard = createElement('div', 'mx-3 bg-ink2 border border-hairline rounded-xl overflow-hidden')
  const divider = () => createElement('div', 'border-t border-hairline')
  const generalRow = (name: string, valueEl: HTMLElement) => {
    const row = createElement('div', 'flex items-center gap-2 px-3.5 py-3 cursor-pointer select-none')
    row.setAttribute('data-general-row', '')
    const n = createNode('span', { className: 'text-[13px] font-medium flex-1', text: name })
    const chev = createNode('span', { className: 'text-t3 text-[15px] leading-none transition-transform', text: '›' })
    row.append(n, valueEl, chev)
    return { row, chev }
  }

  const languageValue = createNode('span', { className: 'text-[12px] text-t2', text: 'English' })
  const languageRow = generalRow('Language', languageValue)
  languageRow.row.addEventListener('click', () => toast('v1 ships English only — i18n comes later'))
  languageRow.chev.classList.add('invisible')

  const prefLabel = (p: ThemePref) => (p === 'system' ? 'System' : p === 'light' ? 'Light' : 'Dark')
  const themeValue = createNode('span', { className: 'text-[12px] text-t2', text: prefLabel(getThemePref()) })
  const themeRow = generalRow('Theme', themeValue)
  const themePanel = createElement('div', 'hidden px-3.5 pb-3')
  const themeSeg = createElement('div', 'flex gap-1')
  for (const opt of ['system', 'light', 'dark'] as const) {
    const b = createNode('button', { className: '', text: prefLabel(opt), attrs: { type: 'button' } })
    b.setAttribute('data-theme-opt', opt)
    segBtnStyle(b, getThemePref() === opt)
    b.addEventListener('click', (e) => {
      e.stopPropagation()
      setThemePref(opt)
      themeValue.textContent = prefLabel(opt)
      for (const sib of themeSeg.children) {
        const el = sib as HTMLElement
        segBtnStyle(el, el.getAttribute('data-theme-opt') === opt)
      }
    })
    themeSeg.appendChild(b)
  }
  themePanel.appendChild(themeSeg)
  themeRow.row.addEventListener('click', () => {
    const open = themePanel.classList.toggle('hidden')
    themeRow.chev.classList.toggle('rotate-90', !open)
  })

  const hljsLabel = () => HLJS_THEMES.find((t) => t.key === getSavedHljsTheme())?.label ?? getSavedHljsTheme()
  const hljsValue = createNode('span', { className: 'text-[12px] text-t2', text: hljsLabel() })
  const hljsRow = generalRow('Highlights', hljsValue)
  const hljsPanel = createElement('div', 'hidden px-3 pb-3 grid grid-cols-2 gap-1.5')
  hljsPanel.setAttribute('data-hljs-panel', '')
  for (const t of HLJS_THEMES) {
    const chip = createNode('button', {
      className:
        'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate ' +
        (t.key === getSavedHljsTheme()
          ? 'text-blue border-blue/40 bg-blue/10'
          : 'bg-ink3 text-t2 border-transparent'),
      text: t.label,
      attrs: { type: 'button', 'data-hljs-opt': t.key },
    })
    chip.addEventListener('click', (e) => {
      e.stopPropagation()
      saveHljsTheme(t.key)
      applyHljsTheme(t.key, getResolvedTheme() === 'dark')
      hljsValue.textContent = t.label
      for (const sib of hljsPanel.children) {
        const el = sib as HTMLElement
        el.className =
          'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate ' +
          (el.getAttribute('data-hljs-opt') === t.key
            ? 'text-blue border-blue/40 bg-blue/10'
            : 'bg-ink3 text-t2 border-transparent')
      }
    })
    hljsPanel.appendChild(chip)
  }
  hljsRow.row.addEventListener('click', () => {
    const open = hljsPanel.classList.toggle('hidden')
    hljsRow.chev.classList.toggle('rotate-90', !open)
  })

  generalCard.append(
    languageRow.row,
    divider(),
    themeRow.row,
    themePanel,
    divider(),
    hljsRow.row,
    hljsPanel,
  )

  homeScreen.append(
    sectionLabel('PROVIDERS'),
    provList,
    addProviderBtn,
    sectionLabel('DEFAULT MODEL'),
    defaultCard,
    sectionLabel('GENERAL'),
    generalCard,
  )
  addProviderBtn.addEventListener('click', () => loadForm(null, true, payload.providers.length))

  // ------------------------------------------------------------------ edit
  const editScreen = createElement('div', '')
  editScreen.setAttribute('data-screen', 'edit')

  const protoSel = createElement('div', 'grid grid-cols-3 gap-2 px-3')
  const protos: Array<{ el: HTMLElement; p: string }> = []
  for (const p of [
    { id: 'openai', name: 'OpenAI', path: '/chat/completions' },
    { id: 'responses', name: 'Responses', path: '/v1/responses' },
    { id: 'anthropic', name: 'Anthropic', path: '/v1/messages' },
  ]) {
    const tile = createNode('div', {
      className: 'py-2.5 px-2 rounded-xl text-center cursor-pointer bg-ink2 border border-hairline',
      attrs: { 'data-proto': p.id },
    })
    tile.append(
      createNode('div', { className: 'text-xs font-semibold mb-0.5', text: p.name }),
      createNode('div', { className: 'text-[9px] text-t3 font-mono', text: p.path }),
    )
    tile.addEventListener('click', () => selectProto(p.id))
    protoSel.appendChild(tile)
    protos.push({ el: tile, p: p.id })
  }

  const nameInput = createNode('input', {
    className:
      'w-full px-3 py-2.5 bg-ink2 border border-hairline rounded-xl text-t1 text-[13px] font-mono outline-none focus:border-blue/40',
    props: { type: 'text', spellcheck: false, placeholder: 'provider name' },
    attrs: { 'data-field': 'name' },
  }) as HTMLInputElement
  const urlInput = createNode('input', {
    className:
      'w-full px-3 py-2.5 bg-ink2 border border-hairline rounded-xl text-t1 text-[13px] font-mono outline-none focus:border-blue/40',
    props: { type: 'text', spellcheck: false, placeholder: 'https://…' },
    attrs: { 'data-field': 'url' },
  }) as HTMLInputElement
  // Trailing test affordance ON the URL row — the probe answers "does
  // THIS url+key work", so its trigger and its result belong at the input.
  const testBtn = createNode('button', {
    className: 'text-blue text-[13px] font-medium px-1.5 shrink-0 cursor-pointer bg-transparent border-none',
    text: 'Test',
    attrs: { type: 'button', 'data-test-conn': '' },
  }) as HTMLButtonElement
  const testResult = createNode('span', {
    className: 'text-[11px] text-t3',
    attrs: { 'data-test-result': '' },
  })
  const urlRow = createElement('div', 'flex items-center gap-1')
  urlRow.append(urlInput, testBtn)

  const keyList = createElement('div', 'px-3')
  const addKeyBtn = createNode('div', {
    className: 'flex items-center gap-1.5 px-3.5 py-2.5 text-[13px] text-blue cursor-pointer select-none mt-1 px-3',
    attrs: { 'data-add-key': '' },
  })
  addKeyBtn.append(renderIcon('plus', { size: 16 }), document.createTextNode('Add Key'))

  const fetchModelsBtn = createNode('span', {
    className: 'text-blue cursor-pointer',
    text: '· fetch from API',
    attrs: { 'data-fetch-models': '' },
  })
  // OpenRouter's full /models list is hundreds of entries — noise in the
  // picker. When the URL points at OpenRouter, fetch the free top 10
  // instead (same filtered query as the free:true startup path). Hoisted
  // above loadForm: the programmatic URL fill calls updateFetchLabel.
  const isFreeFetch = () => urlInput.value.toLowerCase().includes('openrouter')
  const updateFetchLabel = () => {
    fetchModelsBtn.textContent = isFreeFetch() ? '· fetch free top 10' : '· fetch from API'
  }
  urlInput.addEventListener('input', updateFetchLabel)
  updateFetchLabel()
  const modelList = createElement('div', 'mx-3 bg-ink2 border border-hairline rounded-xl overflow-hidden')
  const addModelBtn = createNode('div', {
    className: 'flex items-center gap-1.5 px-3.5 py-2.5 mb-2 text-[13px] text-blue cursor-pointer select-none',
    attrs: { 'data-add-model': '' },
  })
  addModelBtn.append(renderIcon('plus', { size: 16 }), document.createTextNode('Add Model'))

  // Extra params mirror the models section: section label, one card with
  // divided rows, aligned add link — always expanded (two fields per row,
  // a fold buys nothing).
  const kvLabel = sectionLabel('EXTRA PARAMS · appended to request body')
  const kvBody = createElement('div', 'mx-3 bg-ink2 border border-hairline rounded-xl overflow-hidden')
  const kvAdd = createNode('div', {
    className: 'flex items-center gap-1.5 px-3.5 py-2.5 mb-2 text-[13px] text-blue cursor-pointer select-none',
    attrs: { 'data-add-param': '' },
  })
  kvAdd.append(renderIcon('plus', { size: 16 }), document.createTextNode('Add Param'))
  const kvFold = createElement('div', '')
  kvFold.append(kvLabel, kvBody, kvAdd)

  const delZone = createElement('div', '')

  const cancelBtn = createNode('button', {
    className: 'flex-1 py-2.5 rounded-xl text-[13px] font-semibold bg-transparent text-t2 border border-hairline',
    text: 'Cancel',
    attrs: { 'data-cancel': '' },
  }) as HTMLButtonElement
  const saveBtn = createNode('button', {
    className: 'flex-[2] py-2.5 rounded-xl text-[13px] font-semibold bg-blue/15 text-blue border border-blue/35',
    text: 'Save',
    attrs: { 'data-save': '' },
  }) as HTMLButtonElement

  const btnRow = (a: HTMLElement, b: HTMLElement) => {
    const row = createElement('div', 'flex gap-2 px-3')
    row.append(a, b)
    return row
  }

  editScreen.append(
    sectionLabel('PROTOCOL'),
    protoSel,
    createNode('div', { className: 'px-3 pb-2.5 pt-1.5' }, createNode('label', { className: 'block text-[11px] text-t2 mb-1.5', text: 'NAME' }), nameInput),
    createNode(
      'div',
      { className: 'px-3 pb-2.5' },
      (() => {
        const labelRow = createElement('div', 'flex items-baseline mb-1.5')
        const label = createNode('span', { className: 'flex-1 text-[11px] text-t2', text: 'BASE URL' })
        labelRow.append(label, testResult)
        return labelRow
      })(),
      urlRow,
    ),
    sectionLabel('API KEYS', createNode('span', { className: 'text-t3', text: ' · tried in order' })),
    keyList,
    addKeyBtn,
    sectionLabel('MODELS', fetchModelsBtn),
    modelList,
    addModelBtn,
    kvFold,
    createElement('div', 'h-2'),
    delZone,
    createElement('div', 'h-3.5'),
    btnRow(cancelBtn, saveBtn),
  )

  // ------------------------------------------------------------- JSON sheet
  const sheetWrap = createNode('div', { style: { display: 'none' }, attrs: { 'data-json-sheet': '' } })
  const sheetMask = createNode('div', { className: 'fixed inset-0 z-[70] bg-black/50' })
  const sheet = createNode('div', {
    className:
      'fixed left-1/2 -translate-x-1/2 bottom-0 z-[71] w-full max-w-[420px] bg-ink border border-hairline rounded-t-[20px] p-4 max-h-[70vh] overflow-y-auto',
  })
  const sheetTitle = createElement('div', 'flex justify-between text-[13px] font-semibold mb-2.5')
  sheetTitle.append(
    createNode('span', { text: 'settings.json · providers' }),
    createNode('span', { className: 'text-t3 cursor-pointer', text: '✕', attrs: { 'data-sheet-close': '' } }),
  )
  const jsonBox = createElement('pre', 'bg-ink2 border border-hairline rounded-xl p-3 font-mono text-[10.5px] leading-relaxed text-t2 whitespace-pre overflow-auto max-h-[380px]')
  sheet.append(sheetTitle, jsonBox)
  sheetWrap.append(sheetMask, sheet)
  const closeSheet = () => {
    sheetWrap.style.display = 'none'
  }
  sheetMask.addEventListener('click', closeSheet)
  sheetTitle.querySelector('[data-sheet-close]')?.addEventListener('click', closeSheet)

  // Escape first, then wrap with highlight spans — same chain as the
  // prototype; nothing dynamic reaches innerHTML unescaped.
  const renderJSONPreview = () => {
    const editing = !editScreen.classList.contains('hidden')
    const data = editing ? collectFormProvider() : payload.providers
    const j = JSON.stringify(data, null, 2)
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/"([^"]+)":/g, '<span class="text-[#7fd4ff]">"$1"</span>:')
      .replace(/: "([^"]*)"/g, ': <span class="text-[#a8d8b9]">"$1"</span>')
      .replace(/: (\d+)/g, ': <span class="text-[#e8b98a]">$1</span>')
    jsonBox.innerHTML = j
  }
  const openSheet = () => {
    renderJSONPreview()
    sheetWrap.style.display = ''
  }
  jsonAction.addEventListener('click', openSheet)

  frame.append(hdr, homeScreen, editScreen)
  root.append(frame, sheetWrap, toastEl)

  // --------------------------------------------------------------- screens
  const showScreen = (home: boolean) => {
    homeScreen.classList.toggle('hidden', !home)
    editScreen.classList.toggle('hidden', home)
    titleEl.textContent = home ? 'Settings' : form.isNew ? 'Add provider' : 'Edit provider'
    // scrollTop (not scrollTo): jsdom lacks the scrolling API, and the
    // property write is a no-op there while still scrolling in the browser.
    root.scrollTop = 0
  }

  const renderHome = () => {
    provList.replaceChildren()
    for (const p of payload.providers) {
      provList.appendChild(createProviderCard(p))
    }
    defaultTitle.textContent = `${payload.default.provider} / ${payload.default.model}`
  }

  const createProviderCard = (p: SettingsProvider): HTMLElement => {
    const card = createNode('div', {
      className: 'mx-0 mb-2.5 px-3.5 py-3 bg-card border border-hairline rounded-2xl cursor-pointer',
      attrs: { 'data-provider-card': '' },
    })
    const r1 = createElement('div', 'flex items-center gap-2')
    r1.append(createNode('div', { className: 'text-sm font-semibold flex-1', text: p.name }))
    r1.appendChild(createNode('span', { className: 'text-t3 text-[13px]', text: '›' }))

    const type = p.type ?? ''
    const label = TYPE_LABELS[type] ?? 'AUTO'
    const badgeClass = TYPE_BADGE_CLASS[type] ?? AUTO_BADGE_CLASS
    const r2 = createElement('div', 'flex items-center gap-2 mt-2')
    r2.append(
      createNode('span', { className: 'text-[10px] font-semibold rounded-md px-1.5 py-0.5 ' + badgeClass, text: label, attrs: { 'data-type-badge': '' } }),
      createNode('span', { className: 'text-[11px] text-t2 flex-1', text: `${Object.keys(p.models ?? {}).length} models` }),
    )
    card.append(
      r1,
      createNode('div', {
        className: 'text-[11px] text-t3 font-mono mt-0.5 truncate',
        text: p.url,
      }),
      r2,
    )
    card.addEventListener('click', () => loadForm(p, false, payload.providers.indexOf(p)))
    return card
  }

  // ------------------------------------------------------------- edit form
  const selectProto = (p: string) => {
    form.type = p
    for (const t of protos) t.el.classList.toggle('border-blue/50', t.p === p)
    for (const t of protos) t.el.classList.toggle('bg-blue/10', t.p === p)
    for (const t of protos) t.el.classList.toggle('border-hairline', t.p !== p)
    for (const t of protos) t.el.classList.toggle('bg-ink2', t.p !== p)
  }


  const loadForm = (p: SettingsProvider | null, isNew: boolean, index: number) => {
    form.isNew = isNew
    form.index = index
    const source: SettingsProvider = p
      ? (JSON.parse(JSON.stringify(p)) as SettingsProvider)
      : { name: '', url: '', type: 'openai', keys: [], models: {} }
    form.provider = source
    form.type = source.type ?? ''
    form.keys = source.keys && source.keys.length > 0 ? [...source.keys] : ['']
    form.models = source.models ?? {}
    form.kv = Object.entries(source.extra_params ?? {}).map(([k, v]) => ({ k, v: JSON.stringify(v) }))

    nameInput.value = source.name
    urlInput.value = source.url
    updateFetchLabel() // programmatic fill fires no input event — refresh the fetch label here

    const explicit = TYPE_LABELS[form.type] !== undefined
    for (const t of protos) t.el.classList.toggle('border-blue/50', explicit && t.p === form.type)
    for (const t of protos) t.el.classList.toggle('bg-blue/10', explicit && t.p === form.type)
    for (const t of protos) t.el.classList.toggle('border-hairline', !(explicit && t.p === form.type))
    for (const t of protos) t.el.classList.toggle('bg-ink2', !(explicit && t.p === form.type))

    renderKeys()
    renderModels()
    renderKV()
    testResult.replaceChildren()
    testResult.className = 'text-[11px] text-t2'
    delZone.replaceChildren()
    if (!isNew) {
      const del = createNode('div', {
        className: 'text-[11px] text-red/85 text-center pt-2 cursor-pointer',
        text: 'Delete this provider',
        attrs: { 'data-delete-provider': '' },
      })
      del.addEventListener('click', askDelete)
      delZone.appendChild(del)
    }
    showScreen(false)
  }

  // Builds the wire provider from the current form state — the probe and the
  // PUT body both use it, so uncommitted edits are exactly what gets tested.
  const collectFormProvider = (): SettingsProvider => {
    const base = JSON.parse(JSON.stringify(form.provider)) as SettingsProvider
    base.name = nameInput.value.trim()
    base.url = urlInput.value.trim()
    base.type = form.type
    base.keys = form.keys.filter(Boolean)
    // Stable modality order on disk; empty model names (a fresh
    // "+ Add Model" row never filled in) are dropped, not saved.
    const models: Record<string, SettingsModelMeta> = {}
    for (const [n, mm] of Object.entries(form.models)) {
      if (!n.trim() || n.startsWith('__new_')) continue
      // Sort only when input exists — absent stays absent (round-trip
      // fidelity for models configured without modality metadata).
      models[n] = mm.input
        ? { ...mm, input: [...mm.input].sort((a, b) => modalityRank(a) - modalityRank(b)) }
        : mm
    }
    base.models = models
    const extra: Record<string, unknown> = {}
    for (const { k, v } of form.kv) {
      if (!k) continue
      try {
        extra[k] = JSON.parse(v)
      } catch {
        extra[k] = v
      }
    }
    if (Object.keys(extra).length > 0) base.extra_params = extra
    else delete base.extra_params
    return base
  }

  const renderKeys = () => {
    keyList.replaceChildren()
    form.keys.forEach((k, i) => {
      const row = createElement('div', 'flex items-center gap-1.5 mb-1.5')
      const inp = createNode('input', {
        className:
          'flex-1 px-3 py-2.5 bg-ink2 border border-hairline rounded-xl text-t1 text-[13px] font-mono outline-none focus:border-blue/40',
        props: { type: 'password', spellcheck: false, placeholder: 'API key', value: k },
      }) as HTMLInputElement
      inp.addEventListener('input', () => {
        form.keys[i] = inp.value
      })
      const eye = createNode('button', {
        className: 'w-[30px] h-9 bg-transparent border-none text-t3 flex items-center justify-center cursor-pointer shrink-0',
        attrs: { title: 'show/hide', type: 'button' },
      })
      eye.innerHTML = EYE
      eye.addEventListener('click', () => {
        const show = inp.type === 'password'
        inp.type = show ? 'text' : 'password'
        eye.innerHTML = show ? EYE_OFF : EYE
      })
      const del = createNode('button', {
        className: 'w-9 h-9 -mr-1 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none shrink-0 transition-colors',
        text: '✕',
        attrs: { type: 'button' },
      })
      del.addEventListener('click', () => {
        form.keys.splice(i, 1)
        // Deleting the last row leaves one empty row — the provider must
        // still be editable without a key (probe then reports no key).
        if (form.keys.length === 0) form.keys = ['']
        renderKeys()
      })
      row.append(inp, eye, del)
      keyList.appendChild(row)
    })
  }
  addKeyBtn.addEventListener('click', () => {
    form.keys.push('')
    renderKeys()
  })

  const renderModels = () => {
    modelList.replaceChildren()
    const entries = Object.entries(form.models)
    // No empty-state box: with zero models the container hides and only
    // the manual add link remains.
    modelList.classList.toggle('hidden', entries.length === 0)
    for (const [name, m] of entries) {
      modelList.appendChild(createModelRow(name, m))
    }
  }

  const createModelRow = (name: string, m: SettingsModelMeta): HTMLElement => {
    const card = createNode('div', { className: 'border-b border-hairline last:border-b-0' })
    const mrow = createElement('div', 'flex items-center gap-2 px-3 py-2 cursor-pointer')
    mrow.setAttribute('data-model-row', '')
    // Leading disclosure chevron (left = expand in place; the provider
    // cards' right chevron means drill-in) and a lone trailing delete —
    // maximum separation, one trailing control per Material 3 lists.
    const chev = createNode('span', { className: 'text-t3 text-[15px] leading-none transition-transform', text: '›' })
    mrow.appendChild(chev)
    // The model name is the map key — an inline input (styled as plain
    // text until focused) so renaming and "+ Add Model" share one shape.
    const nameInp = createNode('input', {
      className: 'text-xs font-mono flex-1 min-w-0 truncate bg-transparent border border-transparent rounded px-1 -ml-1 py-0.5 text-t1 outline-none focus:border-blue/40',
      // Fresh rows carry a unique __new_N map key so unnamed rows don't
      // collide — but the user sees an empty field, not the key.
      props: { type: 'text', spellcheck: false, placeholder: 'model id', value: name.startsWith('__new_') ? '' : name },
      attrs: { 'data-model-name': '' },
    }) as HTMLInputElement
    let currentKey = name
    // Focus taps must not bubble to mrow's toggle — collapsing the row
    // mid-rename on a phone is a daily-rage bug.
    nameInp.addEventListener('click', (e) => e.stopPropagation())
    nameInp.addEventListener('input', () => {
      const next = nameInp.value
      if (next === currentKey) return
      // Renaming onto an existing id would silently merge two models —
      // reject instead and snap the field back to the current key.
      if (next !== '' && form.models[next] !== undefined) {
        nameInp.value = currentKey
        return
      }
      // re-key preserving position
      const rebuilt: Record<string, SettingsModelMeta> = {}
      for (const [k, v] of Object.entries(form.models)) {
        rebuilt[k === currentKey ? next : k] = v
      }
      form.models = rebuilt
      currentKey = next
    })
    mrow.appendChild(nameInp)
    // Input-modality chips, exactly as configured (text/image/video/…);
    // thinking stays editable in the detail rows but is not surfaced here.
    // The chips render into a container the detail's INPUT toggle can
    // refresh in place — a list-wide re-render would collapse open rows.
    const mods = createElement('span', 'flex items-center gap-1')
    const renderMods = () => {
      mods.replaceChildren()
      const sorted = [...(m.input ?? [])].sort((a, b) => modalityRank(a) - modalityRank(b))
      for (const mod of sorted) {
        mods.appendChild(createNode('span', { className: 'text-[9px] text-[#9D5CFF] bg-ink3 rounded px-1.5 py-0.5', text: mod }))
      }
    }
    renderMods()
    mrow.appendChild(mods)
    mrow.appendChild(createNode('span', { className: 'text-[9px] text-t3 bg-ink3 rounded px-1.5 py-0.5', text: `${m.context ?? ''}` }))
    // 36px hit box around a small glyph — a bare 11px span has a tap
    // target far below thumb accuracy on a phone (Material floor: 48dp).
    const mdel = createNode('span', {
      className: 'w-9 h-9 -mr-2 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none transition-colors',
      text: '✕',
      attrs: { 'data-model-del': '' },
    })
    mrow.appendChild(mdel)

    const detail = createElement('div', 'hidden border-t border-hairline px-3 pb-3')
    const prow = (label: string, ...kids: Node[]) => {
      const row = createElement('div', 'flex items-center gap-2 mt-2.5')
      row.append(createNode('div', { className: 'text-[11px] text-t2 w-[76px] shrink-0', text: label }), ...kids)
      return row
    }
    const smallInput = (value: string | number | undefined, oninput: (v: string) => void) => {
      const inp = createNode('input', {
        className: 'flex-1 px-2.5 py-1.5 bg-ink2 border border-hairline rounded-xl text-t1 text-xs font-mono outline-none focus:border-blue/40',
        props: { type: 'text', spellcheck: false, value: String(value ?? '') },
      }) as HTMLInputElement
      inp.addEventListener('input', () => oninput(inp.value))
      return inp
    }
    detail.append(
      prow('CONTEXT', smallInput(m.context, (v) => (m.context = v))),
      prow('MAX TOKENS', smallInput(m.max_tokens, (v) => (m.max_tokens = v))),
    )
    const segRow = (label: string, seg: HTMLElement) => detail.appendChild(prow(label, seg))
    const seg = createElement('div', 'flex gap-1 flex-1')
    for (const kind of ['text', 'image', 'audio', 'video'] as const) {
      const b = createNode('button', {
        className: '',
        text: kind === 'text' ? 'Text' : kind === 'image' ? 'Image' : kind === 'audio' ? 'Audio' : 'Video',
        attrs: { type: 'button' },
      })
      segBtnStyle(b, (m.input ?? []).includes(kind))
      b.addEventListener('click', (e) => {
        e.stopPropagation()
        const arr = (m.input ??= [])
        const idx = arr.indexOf(kind)
        if (idx >= 0) arr.splice(idx, 1)
        else arr.push(kind)
        // idx is the PRE-mutation state — selected iff it was absent
        // (we just added it). In-place restyle; renderModels() would
        // collapse every open detail row.
        segBtnStyle(b, idx < 0)
        renderMods()
      })
      seg.appendChild(b)
    }
    segRow('INPUT', seg)
    const tseg = createElement('div', 'flex gap-1 flex-1')
    const current = m.thinking ?? 'auto'
    for (const opt of THINK_OPTS) {
      const b = createNode('button', {
        className: '',
        text: opt,
        attrs: { type: 'button' },
      })
      segBtnStyle(b, opt === current)
      b.addEventListener('click', (e) => {
        e.stopPropagation()
        m.thinking = opt
        for (const sib of tseg.children) segBtnStyle(sib as HTMLElement, sib === b)
      })
      tseg.appendChild(b)
    }
    segRow('THINKING', tseg)

    mrow.addEventListener('click', (e) => {
      if ((e.target as HTMLElement).isSameNode(mdel)) {
        // currentKey, not the render-time `name` — a renamed row's closure
        // key is stale, and the first ✕ click would silently no-op.
        delete form.models[currentKey]
        renderModels()
        return
      }
      const open = detail.classList.toggle('hidden')
      chev.classList.toggle('rotate-90', !open)
    })
    card.append(mrow, detail)
    return card
  }

  let newModelSeq = 0
  addModelBtn.addEventListener('click', () => {
    // Inline like Add Key / Add Param: an empty model row whose NAME is
    // an editable input (the map key). Placeholder keys must be UNIQUE —
    // a shared '' key made each Add overwrite the previous unnamed row.
    const name = `__new_${++newModelSeq}`
    form.models[name] = { context: '200k', max_tokens: '32k', input: ['text'], thinking: 'auto' }
    renderModels()
  })

  const renderKV = () => {
    kvBody.replaceChildren()
    kvBody.classList.toggle('hidden', form.kv.length === 0)
    form.kv.forEach((row0, i) => {
      const row = createElement('div', 'flex gap-2 items-center border-b border-hairline last:border-b-0 pl-3 pr-1.5 py-1')
      const kInp = createNode('input', {
        className: 'flex-[2] min-w-0 px-2 py-1.5 bg-ink3 border border-hairline rounded-lg text-t1 text-[11px] font-mono text-center outline-none focus:border-blue/40',
        props: { type: 'text', spellcheck: false, placeholder: 'param name, e.g. temperature', value: row0.k },
      }) as HTMLInputElement
      kInp.addEventListener('input', () => (form.kv[i].k = kInp.value))
      const vInp = createNode('input', {
        className: 'flex-[3] min-w-0 px-2 py-1.5 bg-ink3 border border-hairline rounded-lg text-t1 text-[11px] font-mono text-center outline-none focus:border-blue/40',
        props: { type: 'text', spellcheck: false, placeholder: 'value (JSON literal)', value: row0.v },
      }) as HTMLInputElement
      vInp.addEventListener('input', () => (form.kv[i].v = vInp.value))
      const del = createNode('span', {
        className: 'w-9 h-9 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none transition-colors',
        text: '✕',
        attrs: { 'data-kv-del': '' },
      })
      del.addEventListener('click', () => {
        form.kv.splice(i, 1)
        renderKV()
      })
      row.append(kInp, vInp, del)
      kvBody.appendChild(row)
    })
  }
  kvAdd.addEventListener('click', () => {
    form.kv.push({ k: '', v: '' })
    renderKV()
  })

  // ------------------------------------------------------------ fetch models
  fetchModelsBtn.addEventListener('click', () => {
    fetchModelsBtn.textContent = '· fetching…'
    void (async () => {
      try {
        const res = await fetchProviderModels(urlInput.value.trim(), form.keys.find(Boolean) ?? '', form.type, isFreeFetch())
        if (res.mode === 'fetched') {
          let added = 0
          for (const entry of res.models) {
            const id = typeof entry === 'string' ? entry : entry.id
            if (!id || form.models[id]) continue
            // Metadata from the endpoint (codex-shape) wins; placeholders
            // only fill what the endpoint didn't provide.
            form.models[id] = {
              context: typeof entry === 'object' && entry.context ? entry.context : '1M',
              max_tokens: '32k',
              input: typeof entry === 'object' && entry.input?.length ? entry.input : ['text'],
              thinking: 'auto',
            }
            added++
          }
          renderModels()
          toast(
            added > 0
              ? `Fetched ${res.models.length} models, ${added} added (existing kept)`
              : 'All models already configured',
          )
        } else if (res.mode === 'manual') {
          toast('Anthropic has no model list endpoint — add models manually')
        } else {
          toast(res.error)
        }
      } finally {
        updateFetchLabel()
      }
    })()
  })

  // ------------------------------------------------------------ test button
  testBtn.addEventListener('click', () => {
    const first = Object.keys(form.models)[0] ?? 'provider'
    testResult.replaceChildren()
    testBtn.textContent = '…'
    testResult.textContent = `Pinging ${first}…`
    void (async () => {
      try {
        const res = await testProvider(collectFormProvider())
        if (res.ok) {
          testResult.className = 'text-[11px] text-green'
          testResult.textContent = `✓ ${res.latencyMs ?? 0}ms`
        } else {
          testResult.className = 'text-[11px] text-red'
          testResult.textContent = `✗ ${res.error ?? 'failed'}`
        }
      } catch (err) {
        // daemon restarting → connection refused: fetch rejects, no HTTP status
        testResult.className = 'text-[11px] text-red'
        testResult.textContent = `✗ ${err instanceof Error ? err.message : 'network error'}`
      } finally {
        testBtn.textContent = 'Test'
      }
    })()
  })

  // ------------------------------------------------------------- save/delete
  const persist = async (next: SettingsProvider[], successMsg: string) => {
    try {
      await saveSettings(next)
      payload = await fetchSettings()
      renderHome()
      showScreen(true)
      toast(successMsg)
    } catch (e) {
      // Stay on the edit screen — the user's form state is untouched.
      toast(`Save failed: ${(e as Error).message}`)
    }
  }

  saveBtn.addEventListener('click', () => {
    const name = nameInput.value.trim()
    const url = urlInput.value.trim()
    if (!name || !url) {
      toast('Name and URL are required')
      return
    }
    // Validate the COLLECTED form, not the raw key count: collect drops
    // unnamed (placeholder-key) rows, so a screen showing one unnamed row
    // would otherwise pass here and 400 on the server instead.
    const edited = collectFormProvider()
    if (Object.keys(edited.models).length === 0) {
      toast('At least one named model is required')
      return
    }
    const next = [...payload.providers]
    if (form.isNew) next.push(edited)
    else next[form.index] = edited
    void persist(next, 'Saved · original backed up · restart daemon to apply')
  })

  const askDelete = () => {
    if (payload.providers.length <= 1) {
      toast('At least one provider is required')
      return
    }
    const isDefault =
      payload.default.provider !== '' && nameInput.value.trim() === payload.default.provider
    const msg = isDefault
      ? '⚠ This provider owns the current default model — deleting it breaks the default.\n\nDelete anyway?'
      : 'Delete this provider?\nThe original settings.json was backed up and can be restored.'
    if (!window.confirm(msg)) return
    const next = payload.providers.filter((_, i) => i !== form.index)
    void persist(next, 'Deleted · restorable from backup')
  }

  cancelBtn.addEventListener('click', () => showScreen(true))
  backBtn.addEventListener('click', () => {
    // Drill-down: from the edit screen back goes to settings home; from
    // home it leaves Settings entirely and returns to the chat.
    if (editScreen.classList.contains('hidden')) close()
    else showScreen(true)
  })

  // ------------------------------------------------------------------ open
  const open = () => {
    opened = true
    root.style.display = ''
    void fetchSettings()
      .then((p) => {
        payload = p
      })
      .catch((e: Error) => {
        payload = { providers: [], default: { provider: '', model: '' } }
        toast(`Load failed: ${e.message}`)
      })
      .finally(() => {
        renderHome()
        showScreen(true)
      })
  }
  const close = () => {
    opened = false
    root.style.display = 'none'
    closeSheet()
  }

  return { root, open, close, isOpen: () => opened }
}
