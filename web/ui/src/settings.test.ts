import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchSettings,
  saveSettings,
  testProvider,
  fetchProviderModels,
  createSettingsPage,
  type SettingsPayload,
  type SettingsProvider,
  type ModelsResult,
} from './settings'

// Two providers with the default on the SECOND one — discriminates name-match
// from index-0 in the default-pill rendering. First provider has no type
// (AUTO badge), second is typed with extra_params for round-trip checks.
const PAYLOAD: SettingsPayload = {
  providers: [
    {
      name: 'xiaomi',
      url: 'https://api.xiaomimimo.com/anthropic',
      keys: ['sk-xiaomi'],
      models: { 'mimo-v2.5': { context: '500k', input: ['text', 'image'] } },
    },
    {
      name: 'zhipu',
      type: 'openai',
      url: 'https://open.bigmodel.cn/api/coding/paas/v4',
      keys: ['zk-1'],
      models: { 'glm-5.3': { context: '1M' } },
      extra_params: { tool_stream: true },
    },
  ],
  default: { provider: 'zhipu', model: 'glm-5.3' },
}

interface MockOptions {
  payload?: SettingsPayload
  models?: ModelsResult
  testResult?: { ok: boolean; latencyMs?: number; error?: string }
  putError?: string
}

// makeFetchHandler stubs the four settings endpoints, routing by URL+method
// and capturing PUT bodies via opts.onPut.
function makeFetchHandler(opts: MockOptions & { onPut?: (p: SettingsProvider[]) => void } = {}) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (url === '/api/settings/providers') {
      if (method === 'PUT') {
        opts.onPut?.(JSON.parse(String(init?.body)) as SettingsProvider[])
        if (opts.putError) {
          return { ok: false, status: 400, json: async () => ({ error: opts.putError }) }
        }
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      return {
        ok: true,
        status: 200,
        json: async () => opts.payload ?? { providers: [], default: { provider: '', model: '' } },
      }
    }
    if (url === '/api/settings/test') {
      return { ok: true, status: 200, json: async () => opts.testResult ?? { ok: true, latencyMs: 412 } }
    }
    if (url === '/api/settings/models') {
      return { ok: true, status: 200, json: async () => opts.models ?? { mode: 'fetched', models: [] } }
    }
    throw new Error('unexpected fetch ' + method + ' ' + url)
  })
}

async function openPage(
  mock: ReturnType<typeof makeFetchHandler>,
  payload: SettingsPayload = PAYLOAD,
) {
  vi.stubGlobal('fetch', mock)
  const page = createSettingsPage()
  document.body.appendChild(page.root)
  page.open()
  await vi.waitFor(() => {
    expect(page.root.querySelectorAll('[data-provider-card]').length).toBe(payload.providers.length)
  })
  return page
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  document.body.innerHTML = ''
})

// Flush the open() fetch→render chain with microtasks only — usable under
// fake timers (which must not be advanced past the toast window).
async function flushMicrotasks() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

function input(el: HTMLInputElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

describe('fetchSettings', () => {
  it('GETs /api/settings/providers and returns the payload as-is', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    vi.stubGlobal('fetch', mock)

    const got = await fetchSettings()

    expect(mock).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledWith('/api/settings/providers')
    expect(got).toEqual(PAYLOAD)
  })
})

describe('saveSettings', () => {
  it('PUTs the providers array as the request body', async () => {
    const mock = makeFetchHandler({})
    vi.stubGlobal('fetch', mock)

    await saveSettings(PAYLOAD.providers)

    expect(mock).toHaveBeenCalledWith(
      '/api/settings/providers',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify(PAYLOAD.providers),
      }),
    )
  })

  it('throws carrying the response error text on !ok', async () => {
    const mock = makeFetchHandler({ putError: 'duplicate provider name "zhipu"' })
    vi.stubGlobal('fetch', mock)

    await expect(saveSettings(PAYLOAD.providers)).rejects.toThrow('duplicate provider name "zhipu"')
  })
})

describe('testProvider', () => {
  it('POSTs the provider object and maps the ok envelope', async () => {
    const mock = makeFetchHandler({})
    vi.stubGlobal('fetch', mock)

    const got = await testProvider(PAYLOAD.providers[1])

    expect(mock).toHaveBeenCalledWith(
      '/api/settings/test',
      expect.objectContaining({
        method: 'POST',
        body: JSON.stringify(PAYLOAD.providers[1]),
      }),
    )
    expect(got).toEqual({ ok: true, latencyMs: 412 })
  })

  it('maps the failure envelope', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({ testResult: { ok: false, error: 'HTTP 401' } }))
    await expect(testProvider(PAYLOAD.providers[1])).resolves.toEqual({ ok: false, error: 'HTTP 401' })
  })
})

describe('fetchProviderModels', () => {
  it('maps all three modes', async () => {
    const cases: Array<[ModelsResult, ModelsResult]> = [
      [{ mode: 'fetched', models: ['glm-4.5', 'glm-4.6'] }, { mode: 'fetched', models: ['glm-4.5', 'glm-4.6'] }],
      [{ mode: 'manual' }, { mode: 'manual' }],
      [{ mode: 'error', error: 'HTTP 500' }, { mode: 'error', error: 'HTTP 500' }],
    ]
    for (const [stub, want] of cases) {
      const mock = makeFetchHandler({ models: stub })
      vi.stubGlobal('fetch', mock)
      await expect(fetchProviderModels('https://x', 'k', 'openai')).resolves.toEqual(want)
      expect(mock).toHaveBeenCalledWith(
        '/api/settings/models',
        expect.objectContaining({ method: 'POST', body: JSON.stringify({ url: 'https://x', key: 'k', type: 'openai' }) }),
      )
      vi.unstubAllGlobals()
    }
  })
})

describe('createSettingsPage', () => {
  it('hidden by default; open() shows, fetches, and renders provider cards', async () => {
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    expect(page.root.style.display).toBe('none')
    expect(page.isOpen()).toBe(false)

    const mock = makeFetchHandler({ payload: PAYLOAD })
    vi.stubGlobal('fetch', mock)
    page.open()

    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-provider-card]').length).toBe(2)
    })
    expect(page.root.style.display).toBe('')
    expect(page.isOpen()).toBe(true)
    expect(mock).toHaveBeenCalledWith('/api/settings/providers')

    const cards = [...page.root.querySelectorAll('[data-provider-card]')] as HTMLElement[]
    expect(cards[0].textContent).toContain('xiaomi')
    expect(cards[1].textContent).toContain('zhipu')
    // Badge removed (redundant with the DEFAULT MODEL card) — no pill anywhere.
    expect(document.querySelector('[data-default-pill]')).toBeNull()
    expect(cards[0].querySelector('[data-type-badge]')?.textContent).toBe('AUTO')
    expect(cards[1].querySelector('[data-type-badge]')?.textContent).toBe('OPENAI')
  })

  it('null keys render without a dots element', async () => {
    const payload: SettingsPayload = {
      providers: [{ ...PAYLOAD.providers[0], keys: null as unknown as string[] }],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)

    // Key dots removed — a keyless provider renders without a dots element.
    expect(page.root.querySelector('[data-provider-card] [data-keydots]')).toBeNull()
  })

  it('toggling an INPUT chip updates the row chips live and reaches the PUT body', async () => {
    // Start from a text-only model so clicking Image means ADDING it.
    const payload = {
      providers: [
        {
          ...PAYLOAD.providers[0],
          models: { 'mimo-v2.5': { context: '500k', input: ['text'] } },
        },
      ],
      default: PAYLOAD.default,
    }
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    row.click()
    const detail = page.root.querySelector('[data-model-row] + div') as HTMLElement
    const imgBtn = Array.from(detail.querySelectorAll('button')).find((b) => b.textContent === 'Image') as HTMLElement
    imgBtn.click()
    // Live: the collapsed row now shows the image chip WITHOUT collapsing.
    const rowChips = Array.from(row.querySelectorAll('span')).map((x) => x.textContent)
    expect(rowChips).toContain('image')
    expect(detail.classList.contains('hidden')).toBe(false)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].models['mimo-v2.5'].input).toContain('image')
  })
  it('fetch with responses type adds models not yet configured', async () => {
    const payload = {
      providers: [
        {
          ...PAYLOAD.providers[0],
          type: 'openai',
          models: { 'mimo-v2.5': { context: '500k', input: ['text'] } },
        },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(
      makeFetchHandler({
        payload,
        models: { mode: 'fetched', models: ['mimo-v2.5', 'glm-a', 'glm-b'] },
      }),
      payload,
    )
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    // switch protocol to responses
    ;(page.root.querySelector('[data-proto="responses"]') as HTMLElement).click()
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-toast]')?.textContent).toContain('2 added')
    })
    expect(modelNames(page.root)).toContain('glm-a')
    expect(modelNames(page.root)).toContain('glm-b')
  })
  it('renamed model deletes by its CURRENT key (stale-closure regression)', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    const nameInp = row.querySelector('[data-model-name]') as HTMLInputElement
    input(nameInp, 'renamed-id')
    ;(row.querySelector('[data-model-del]') as HTMLElement).click()
    expect(page.root.querySelectorAll('[data-model-row]').length).toBe(0)
    // save must not resurrect the old key either
    let putBody: Array<Record<string, unknown>> = []
    const h2 = makeFetchHandler({ payload: PAYLOAD, onPut: (p) => { putBody = p } })
    vi.stubGlobal('fetch', h2)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(Object.keys((putBody[0]?.models ?? {}) as object)).not.toContain('mimo-v2.5')
    })
  })

  it('renaming onto an existing model id snaps back instead of silently merging', async () => {
    const payload = {
      providers: [
        {
          ...PAYLOAD.providers[0],
          models: {
            'model-a': { context: '1M', input: ['text'] },
            'model-b': { context: '1M', input: ['text'] },
          },
        },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const rows = [...page.root.querySelectorAll('[data-model-name]')] as HTMLInputElement[]
    const rowFor = (id: string) =>
      rows.find((r) => (r as HTMLInputElement).dataset.currentKeyTest === id) ??
      rows.find((r) => r.value === id || r.placeholder === 'model id') ??
      rows[0]
    // rename model-a → model-b (existing id): rejected, field snaps back
    const inp = rows[0]
    input(inp, 'model-b')
    expect(inp.value).toBe('model-a')
    expect(modelNames(page.root)).toContain('model-a')
    expect(modelNames(page.root)).toContain('model-b')
    void rowFor
  })

  it('save with only an unnamed row is rejected client-side (no PUT)', async () => {
    const payload = {
      providers: [{ ...PAYLOAD.providers[0], models: {} }],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    let puts = 0
    const handler = makeFetchHandler({
      payload,
      onPut: () => { puts++ },
    })
    vi.stubGlobal('fetch', handler)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    expect(puts).toBe(0)
    expect(page.root.querySelector('[data-toast]')?.textContent).toContain('named model')
  })

  it('adding two unnamed models keeps both rows', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const add = () => (page.root.querySelector('[data-add-model]') as HTMLElement).click()
    add()
    const rows1 = page.root.querySelectorAll('[data-model-row]').length
    add()
    const rows2 = page.root.querySelectorAll('[data-model-row]').length
    expect(rows1).toBeGreaterThan(0)
    expect(rows2).toBe(rows1 + 1) // the first unnamed row SURVIVED the second add
    // fresh name fields render empty (placeholder keys hidden)
    const names = Array.from(page.root.querySelectorAll('[data-model-name]')).map(
      (i) => (i as HTMLInputElement).value,
    )
    expect(names).not.toContain('__new_1')
    expect(names).not.toContain('__new_2')
  })
  it('INPUT toggle keeps button state and row chips in sync across repeated presses', async () => {
    const payload = {
      providers: [
        { ...PAYLOAD.providers[0], models: { 'mimo-v2.5': { context: '500k', input: ['text'] } } },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    row.click()
    const detail = page.root.querySelector('[data-model-row] + div') as HTMLElement
    const textBtn = Array.from(detail.querySelectorAll('button')).find((b) => b.textContent === 'Text') as HTMLElement
    const chips = () => Array.from(row.querySelectorAll('span')).map((x) => x.textContent)
    const btnSelected = () => textBtn.className.includes('text-blue')

    expect(btnSelected()).toBe(true) // starts selected (input: ['text'])
    textBtn.click() // remove → both off
    expect(btnSelected()).toBe(false)
    expect(chips()).not.toContain('text')
    textBtn.click() // add back → both on
    expect(btnSelected()).toBe(true)
    expect(chips()).toContain('text')
  })
  it('selecting a THINKING option keeps the detail row open', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    ;(page.root.querySelector('[data-model-row]') as HTMLElement).click()
    const detail = page.root.querySelector('[data-model-row] + div') as HTMLElement
    expect(detail.classList.contains('hidden')).toBe(false)
    // click a THINKING option (the 4th row's segmented buttons)
    const thinkBtn = Array.from(detail.querySelectorAll('button')).find((b) => b.textContent === 'low') as HTMLElement
    thinkBtn.click()
    const detailAfter = page.root.querySelector('[data-model-row] + div') as HTMLElement
    expect(detailAfter.classList.contains('hidden')).toBe(false) // NOT re-rendered collapsed
  })
  const modelNames = (root: HTMLElement) =>
    Array.from(root.querySelectorAll('[data-model-name]')).map((i) => (i as HTMLInputElement).value)
  it('model rows show input-modality chips, never a think chip', async () => {
    const payload = {
      providers: [
        {
          ...PAYLOAD.providers[0],
          models: {
            'glm-v': { context: '1M', max_tokens: '32k', input: ['text', 'image'], thinking: 'auto' },
          },
        },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    const chips = Array.from(row.querySelectorAll('span')).map((s) => s.textContent)
    expect(chips).toContain('text')
    expect(chips).toContain('image')
    expect(chips).not.toContain('think')
  })
  it('edit screen loads the form; save PUTs the full array with edits, others untouched', async () => {
    let putBody: SettingsProvider[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      onPut: (p) => {
        putBody = p
      },
    })
    const page = await openPage(mock)

    ;(page.root.querySelectorAll('[data-provider-card]')[1] as HTMLElement).click()
    const nameInput = await vi.waitFor(() => {
      const el = page.root.querySelector('[data-field="name"]') as HTMLInputElement
      expect(el.value).toBe('zhipu')
      return el
    })
    input(nameInput, 'zhipu-renamed')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()

    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody).toHaveLength(2)
    expect(putBody![1].name).toBe('zhipu-renamed')
    // Round-trip fidelity: the untouched provider and untouched fields.
    expect(putBody![0]).toEqual(PAYLOAD.providers[0])
    expect(putBody![1].extra_params).toEqual({ tool_stream: true })
    expect(putBody![1].models).toEqual(PAYLOAD.providers[1].models)
  })

  it('save is blocked client-side without name/url — toast, zero PUTs', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)

    ;(page.root.querySelectorAll('[data-provider-card]')[0] as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-field="name"]') as HTMLInputElement).value).toBe('xiaomi')
    })
    input(page.root.querySelector('[data-field="name"]') as HTMLInputElement, '')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await flushMicrotasks()

    expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe(
      'Name and URL are required',
    )
    const puts = mock.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')
    expect(puts).toHaveLength(0)
  })

  it('deleting the last remaining provider is blocked client-side', async () => {
    const payload: SettingsPayload = {
      providers: [PAYLOAD.providers[0]],
      default: { provider: '', model: '' },
    }
    const mock = makeFetchHandler({ payload })
    const page = await openPage(mock, payload)
    vi.stubGlobal('confirm', vi.fn(() => true))

    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-delete-provider]')).toBeTruthy()
    })
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()
    await flushMicrotasks()

    expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe(
      'At least one provider is required',
    )
    const puts = mock.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')
    expect(puts).toHaveLength(0)
  })

  it('+ Add provider create flow appends a new provider, existing untouched', async () => {
    let putBody: SettingsProvider[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      onPut: (p) => {
        putBody = p
      },
    })
    const page = await openPage(mock)

    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-field="name"]') as HTMLInputElement).value).toBe('')
    })
    input(page.root.querySelector('[data-field="name"]') as HTMLInputElement, 'minimax')
    input(page.root.querySelector('[data-field="url"]') as HTMLInputElement, 'https://api.minimax.chat/v1')
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    // The new row lands inline with an empty name field — no prompt().
    const nameField = page.root.querySelector('[data-model-name]') as HTMLInputElement
    expect(nameField.value).toBe('') // fresh rows hide their __new_N map key
    input(nameField, 'glm-new')
    expect(modelNames(page.root)).toContain('glm-new')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()

    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody).toHaveLength(3)
    expect(putBody![0]).toEqual(PAYLOAD.providers[0])
    expect(putBody![1]).toEqual(PAYLOAD.providers[1])
    expect(putBody![2].name).toBe('minimax')
    expect(putBody![2].models['glm-new']).toEqual({
      context: '200k',
      max_tokens: '32k',
      input: ['text'],
      thinking: 'auto',
    })
  })

  it('deleting a non-last provider PUTs the array without it', async () => {
    let putBody: SettingsProvider[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      onPut: (p) => {
        putBody = p
      },
    })
    const page = await openPage(mock)
    const confirmMock = vi.fn(() => true)
    vi.stubGlobal('confirm', confirmMock)

    ;(page.root.querySelectorAll('[data-provider-card]')[0] as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-delete-provider]')).toBeTruthy()
    })
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()

    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody).toHaveLength(1)
    expect(putBody![0]).toEqual(PAYLOAD.providers[1])
    // Toast fires only after the post-PUT re-fetch resolves — wait for it
    // rather than racing the persist chain's microtasks.
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe(
        'Deleted · restorable from backup',
      )
    })
    // The default-owner warning fires only for the provider matching default.
    expect(String(confirmMock.mock.calls[0][0])).not.toContain('owns the current default')
  })

  it('deleting the default-owner provider asks the destructive warning', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    const confirmMock = vi.fn(() => false)
    vi.stubGlobal('confirm', confirmMock)

    ;(page.root.querySelectorAll('[data-provider-card]')[1] as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-delete-provider]')).toBeTruthy()
    })
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()

    expect(String(confirmMock.mock.calls[0][0])).toContain('owns the current default model')
    const puts = mock.mock.calls.filter((c) => (c[1] as RequestInit | undefined)?.method === 'PUT')
    expect(puts).toHaveLength(0)
  })

  it('fetch models merges new ids and keeps existing metadata', async () => {
    let putBody: SettingsProvider[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      models: { mode: 'fetched', models: ['mimo-v2.5', 'glm-brand-new'] },
      onPut: (p) => {
        putBody = p
      },
    })
    const page = await openPage(mock)

    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-fetch-models]')).toBeTruthy()
    })
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(modelNames(page.root)).toContain('glm-brand-new')
    })
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()

    await vi.waitFor(() => expect(putBody).toBeDefined())
    const models = putBody![0].models
    expect(Object.keys(models)).toEqual(['mimo-v2.5', 'glm-brand-new'])
    expect(models['mimo-v2.5']).toEqual(PAYLOAD.providers[0].models['mimo-v2.5'])
    expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe(
      'Fetched 2 models, 1 added (existing kept)',
    )
  })

  it('test connection renders the result line for ok and error', async () => {
    const okPage = await openPage(makeFetchHandler({ payload: PAYLOAD, testResult: { ok: true, latencyMs: 412 } }))
    ;(okPage.root.querySelector('[data-provider-card]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(okPage.root.querySelector('[data-test-conn]')).toBeTruthy()
    })
    ;(okPage.root.querySelector('[data-test-conn]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(okPage.root.querySelector('[data-test-result]')?.textContent).toBe('✓ 412ms')
    })
    expect(okPage.root.querySelector('[data-test-conn]')?.textContent).toBe('Test')

    const errPage = await openPage(
      makeFetchHandler({ payload: PAYLOAD, testResult: { ok: false, error: 'HTTP 401' } }),
    )
    ;(errPage.root.querySelector('[data-provider-card]') as HTMLElement).click()
    ;(errPage.root.querySelector('[data-test-conn]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(errPage.root.querySelector('[data-test-result]')?.textContent).toContain('✗ HTTP 401')
    })
  })

  it('JSON sheet shows the provider set', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))

    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const sheet = page.root.querySelector('[data-json-sheet]') as HTMLElement
    expect(sheet.style.display).not.toBe('none')
    expect(sheet.querySelector('pre')?.textContent).toContain('zhipu')

    ;(sheet.querySelector('[data-sheet-close]') as HTMLElement).click()
    expect((page.root.querySelector('[data-json-sheet]') as HTMLElement).style.display).toBe('none')
  })

  it('toast auto-hides after 2.2s', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', makeFetchHandler({ payload: PAYLOAD }))
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    page.open()
    await flushMicrotasks()

    ;(page.root.querySelectorAll('[data-general-row]')[0] as HTMLElement).click()
    const toastEl = page.root.querySelector('[data-toast]') as HTMLElement
    expect(toastEl.textContent).toBe('v1 ships English only — i18n comes later')
    expect(toastEl.classList.contains('toast-show')).toBe(true)

    vi.advanceTimersByTime(2200)
    expect(toastEl.classList.contains('toast-show')).toBe(false)
  })

  it('close() hides the page', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    page.close()
    expect(page.root.style.display).toBe('none')
    expect(page.isOpen()).toBe(false)
  })
  it('back on the home screen is visible and leaves Settings entirely', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const back = page.root.querySelector('[data-back]') as HTMLElement
    expect(back.classList.contains('invisible')).toBe(false)
    expect(page.root.querySelector('svg')).not.toBeNull() // chevron-left icon
    back.click()
    expect(page.root.style.display).toBe('none')
    expect(page.isOpen()).toBe(false)
  })
  it('back on the edit screen returns to the settings home, not out of Settings', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const back = page.root.querySelector('[data-back]') as HTMLElement
    back.click()
    expect(page.isOpen()).toBe(true)
    expect(page.root.style.display).not.toBe('none')
    const title = page.root.querySelector('[data-title]') as HTMLElement
    expect(title.textContent).toBe('Settings')
  })
  it('DEFAULT MODEL expands to a picker; selecting PUTs default and updates the title', async () => {
    let putBody: { provider: string; model: string } | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const card = page.root.querySelector('[data-default-card]') as HTMLElement
    ;(card.firstElementChild as HTMLElement).click()
    const panel = page.root.querySelector('[data-default-panel]') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    const row = panel.querySelector('[data-default-model="zhipu/glm-5.3"]') as HTMLElement
    expect(row).not.toBeNull()
    // mock the default PUT
    const origFetch = window.fetch
    vi.stubGlobal('fetch', (input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input)
      if (url.includes('/api/settings/default')) {
        putBody = JSON.parse(String(init?.body)) as { provider: string; model: string }
        return Promise.resolve(new Response('{"ok":"true"}', { status: 200 }))
      }
      return origFetch(input, init)
    })
    row.click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'zhipu', model: 'glm-5.3' }))
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-default-card] .font-mono') as HTMLElement).textContent).toBe('zhipu / glm-5.3')
    })
    vi.unstubAllGlobals()
  })
  it('Theme fold: selecting Light applies immediately and updates the row value', async () => {
    localStorage.setItem('gbot-theme', 'dark')
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    const themeRow = rows[1] as HTMLElement // Language, Theme, Highlights
    themeRow.click()
    const lightBtn = page.root.querySelector('[data-theme-opt="light"]') as HTMLElement
    expect(lightBtn).not.toBeNull()
    lightBtn.click()
    expect(document.documentElement.dataset.theme).toBe('light')
    expect(localStorage.getItem('gbot-theme')).toBe('light')
    const value = themeRow.querySelector('span.text-t2') as HTMLElement
    expect(value.textContent).toBe('Light')
  })
  it('Highlights fold: grid of themes, selecting one applies and updates the row value', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    const hljsRow = rows[2] as HTMLElement
    hljsRow.click()
    const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    const chip = panel.querySelector('[data-hljs-opt="atom-one"]') as HTMLElement
    chip.click()
    expect(localStorage.getItem('gbot-hljs-theme')).toBe('atom-one')
    const value = hljsRow.querySelector('span.text-t2') as HTMLElement
    expect(value.textContent).toBe('Atom One')
  })
  it('frame pads below the phone status bar', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    // The overlay is inset-0 — without the sidebar-safe-top padding the
    // header renders behind the transparent system status bar.
    const frame = page.root.firstElementChild as HTMLElement
    expect(frame.className).toContain('sidebar-safe-top')
  })
})
