import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { setLocale, t } from './i18n'
import { createSettingsPage, type SettingsPayload } from './settings'

// markUpgrading/refuseUpgrade must be no-op spies: the page arms upgrade mode
// on a restart 202 and the real ones touch module state no WS exists for.
vi.mock('./upgrade_mode', () => ({
  refuseUpgrade: vi.fn(),
}))
vi.mock('./ws', () => ({
  markUpgrading: vi.fn(),
}))

// Minimal web-tool fixture: one provider (open() waits on its card), proxy
// set, three engines so summary/order/count assertions have signal.
const WEB_PAYLOAD: SettingsPayload = {
  providers: [
    {
      name: 'zhipu',
      url: 'https://open.bigmodel.cn/api/coding/paas/v4',
      keys: ['zk-1'],
      models: { 'glm-5.3': {} },
    },
  ],
  default: { provider: 'zhipu', model: 'glm-5.3' },
  web: { proxy: 'http://localhost:10809', engines: { anysearch: 'as-1', tavily: 'tv-1', jina: 'jn-1' } },
}

interface WebMockOptions {
  payload?: SettingsPayload
  webPutError?: string
  onPutWeb?: (body: { proxy: string; engines: Record<string, string> }) => void
}

// Minimal settings-endpoint stub: only the surface open()/the web screens
// touch, anything else throws. The PUT /api/settings/web branch mirrors the
// server merge ("" removes an engine, absent engines preserved) so the
// follow-up GET reflects what the real daemon would answer.
function makeFetchHandler(opts: WebMockOptions = {}) {
  let current: SettingsPayload = structuredClone(opts.payload ?? WEB_PAYLOAD)
  return vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (url === '/api/settings/providers') {
      if (method === 'PUT') {
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      return { ok: true, status: 200, json: async () => structuredClone(current) }
    }
    if (url === '/api/settings/web' && method === 'PUT') {
      if (opts.webPutError) {
        return { ok: false, status: 400, json: async () => ({ error: opts.webPutError }) }
      }
      const body = JSON.parse(String(init?.body)) as { proxy: string; engines: Record<string, string> }
      opts.onPutWeb?.(body)
      const engines: Record<string, string> = { ...current.web?.engines }
      for (const [id, key] of Object.entries(body.engines)) {
        if (key === '') delete engines[id]
        else engines[id] = key
      }
      current = { ...current, web: { proxy: body.proxy, engines } }
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
    if (url === '/api/admin/restart') {
      return {
        ok: true,
        status: 200,
        json: async () => ({ busy: false, items: [], upgradable: false, build: 'dev' }),
      }
    }
    if (url === '/api/settings/remotedesktop') {
      return { ok: true, status: 200, json: async () => ({ devices: [] }) }
    }
    if (url.startsWith('/api/avatar/') && method === 'HEAD') {
      return { ok: false, status: 404, headers: new Headers() }
    }
    throw new Error('unexpected fetch ' + method + ' ' + url)
  })
}

async function openPage(mock: ReturnType<typeof makeFetchHandler>, payload: SettingsPayload = WEB_PAYLOAD) {
  vi.stubGlobal('fetch', mock)
  const page = createSettingsPage()
  document.body.appendChild(page.root)
  page.open()
  await vi.waitFor(() => {
    expect(page.root.querySelectorAll('[data-provider-card]').length).toBe(payload.providers.length)
  })
  return page
}

function toastText(page: { root: HTMLElement }): string {
  return (page.root.querySelector('[data-toast]') as HTMLElement).textContent ?? ''
}

function input(el: HTMLInputElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

const screenOf = (page: { root: HTMLElement }, id: string) =>
  page.root.querySelector(`[data-screen="${id}"]`) as HTMLElement
const isHidden = (el: HTMLElement) => el.classList.contains('hidden')
const webSubOf = (page: { root: HTMLElement }) =>
  (page.root.querySelector('[data-web-card-sub]') as HTMLElement).textContent ?? ''
const openWebScreen = (page: { root: HTMLElement }) => {
  ;(page.root.querySelector('[data-web-card]') as HTMLElement).click()
  return screenOf(page, 'web')
}
const engineRow = (page: { root: HTMLElement }, id: string) =>
  page.root.querySelector(`[data-web-engine="${id}"]`) as HTMLElement
const engineLabel = (page: { root: HTMLElement }, id: string) =>
  (engineRow(page, id).querySelector('span.block') as HTMLElement).textContent
const engineKey = (page: { root: HTMLElement }, id: string) =>
  engineRow(page, id).querySelector('[data-web-key]') as HTMLInputElement
const engineEye = (page: { root: HTMLElement }, id: string) =>
  engineRow(page, id).querySelector('[title="show/hide"]') as HTMLElement
const engineDel = (page: { root: HTMLElement }, id: string) =>
  ([...engineRow(page, id).querySelectorAll('button')] as HTMLElement[]).find(
    (b) => b.textContent === '✕',
  ) as HTMLElement

beforeEach(() => {
  setLocale('en')
  Element.prototype.scrollIntoView = vi.fn()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  localStorage.removeItem('gbot-language')
  document.body.innerHTML = ''
})

describe('web card', () => {
  it('shows one Web card summarizing engines and proxy', async () => {
    const page = await openPage(makeFetchHandler({}))

    const cards = page.root.querySelectorAll('[data-web-card]')
    expect(cards.length).toBe(1)
    expect(webSubOf(page)).toBe('3 engines · proxy localhost:10809')
  })

  it('summary variants: engines only, proxy only, empty, absent', async () => {
    const enginesOnly = structuredClone(WEB_PAYLOAD)
    enginesOnly.web = { proxy: '', engines: WEB_PAYLOAD.web!.engines }
    const page1 = await openPage(makeFetchHandler({ payload: enginesOnly }), enginesOnly)
    expect(webSubOf(page1)).toBe('3 engines')

    const proxyOnly = structuredClone(WEB_PAYLOAD)
    proxyOnly.web = { proxy: 'http://localhost:10809', engines: {} }
    const page2 = await openPage(makeFetchHandler({ payload: proxyOnly }), proxyOnly)
    expect(webSubOf(page2)).toBe('proxy localhost:10809')

    const emptyWeb = structuredClone(WEB_PAYLOAD)
    emptyWeb.web = { proxy: '', engines: {} }
    const page3 = await openPage(makeFetchHandler({ payload: emptyWeb }), emptyWeb)
    expect(webSubOf(page3)).toBe('Not configured')

    const absentWeb = structuredClone(WEB_PAYLOAD)
    delete absentWeb.web
    const page4 = await openPage(makeFetchHandler({ payload: absentWeb }), absentWeb)
    expect(webSubOf(page4)).toBe('Not configured')
  })
})

describe('web screen', () => {
  it('card click opens the web screen with proxy and sorted engine rows', async () => {
    const page = await openPage(makeFetchHandler({}))

    openWebScreen(page)

    expect(isHidden(screenOf(page, 'web'))).toBe(false)
    expect(isHidden(screenOf(page, 'home'))).toBe(true)
    expect((page.root.querySelector('[data-title]') as HTMLElement).textContent).toBe('Web')
    const proxy = page.root.querySelector('[data-web-proxy]') as HTMLInputElement
    expect(proxy.value).toBe('http://localhost:10809')

    const rows = [...page.root.querySelectorAll('[data-web-engine]')]
    expect(rows.map((r) => r.getAttribute('data-web-engine'))).toEqual(['anysearch', 'tavily', 'jina'])
    expect(engineLabel(page, 'anysearch')).toBe('Anysearch')
    expect(engineLabel(page, 'tavily')).toBe('Tavily')
    expect(engineLabel(page, 'jina')).toBe('Jina')
    for (const [id, key] of [
      ['anysearch', 'as-1'],
      ['tavily', 'tv-1'],
      ['jina', 'jn-1'],
    ] as const) {
      const inp = engineKey(page, id)
      expect(inp.type).toBe('password')
      expect(inp.value).toBe(key)
    }
  })

  it('unknown engines sort last and get a capitalized display name', async () => {
    const unknown = structuredClone(WEB_PAYLOAD)
    unknown.web = { proxy: '', engines: { brave: 'bk', anysearch: 'as' } }
    const page = await openPage(makeFetchHandler({ payload: unknown }), unknown)

    openWebScreen(page)

    const rows = [...page.root.querySelectorAll('[data-web-engine]')]
    expect(rows.map((r) => r.getAttribute('data-web-engine'))).toEqual(['anysearch', 'brave'])
    expect(engineLabel(page, 'anysearch')).toBe('Anysearch')
    expect(engineLabel(page, 'brave')).toBe('Brave')
  })

  it('eye toggles the key input between password and text', async () => {
    const page = await openPage(makeFetchHandler({}))

    openWebScreen(page)
    const inp = engineKey(page, 'anysearch')
    const eye = engineEye(page, 'anysearch')

    eye.click()
    expect(inp.type).toBe('text')
    eye.click()
    expect(inp.type).toBe('password')
  })
})

describe('web save', () => {
  it('✕ removes the row at once; Save PUTs it as "" and refreshes summary, toasts Saved', async () => {
    const bodies: Array<{ proxy: string; engines: Record<string, string> }> = []
    const page = await openPage(makeFetchHandler({ onPutWeb: (b) => bodies.push(b) }))

    openWebScreen(page)
    input(page.root.querySelector('[data-web-proxy]') as HTMLInputElement, 'socks5://h:1')
    engineDel(page, 'tavily').click()
    expect(engineRow(page, 'tavily')).toBeNull()
    expect(page.root.querySelectorAll('[data-web-engine]').length).toBe(2)
    ;(page.root.querySelector('[data-web-save]') as HTMLElement).click()

    await vi.waitFor(() => {
      expect(isHidden(screenOf(page, 'home'))).toBe(false)
    })
    expect(bodies.length).toBe(1)
    // The removed engine rides the PUT as "" — absent keys are preserved by
    // the server merge, so omitting tavily would NOT delete it.
    expect(bodies[0]).toEqual({
      proxy: 'socks5://h:1',
      engines: { anysearch: 'as-1', tavily: '', jina: 'jn-1' },
    })
    // tavily removed by the server merge → count drops, proxy host updates.
    expect(webSubOf(page)).toBe('2 engines · proxy h:1')
    expect(toastText(page)).toBe('Saved')
  })

  it('re-adding a removed engine saves the new key, not the stale "" ', async () => {
    const bodies: Array<{ proxy: string; engines: Record<string, string> }> = []
    const page = await openPage(makeFetchHandler({ onPutWeb: (b) => bodies.push(b) }))

    openWebScreen(page)
    engineDel(page, 'tavily').click()

    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()
    const tavilyTile = page.root.querySelector('[data-web-tile="tavily"]') as HTMLElement
    expect(tavilyTile.className).not.toContain('opacity-35')
    tavilyTile.click()
    input(page.root.querySelector('[data-web-add-key]') as HTMLInputElement, 'tv-2')
    ;(page.root.querySelector('[data-web-add-save]') as HTMLElement).click()
    expect(engineKey(page, 'tavily').value).toBe('tv-2')

    ;(page.root.querySelector('[data-web-save]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(bodies.length).toBe(1)
    })
    expect(bodies[0].engines.tavily).toBe('tv-2')
  })

  it('failed save stays on the web screen with the form intact', async () => {
    const page = await openPage(
      makeFetchHandler({ webPutError: 'proxy must be an http://, https:// or socks5:// URL' }),
    )

    openWebScreen(page)
    input(page.root.querySelector('[data-web-proxy]') as HTMLInputElement, 'localhost:10809')
    ;(page.root.querySelector('[data-web-save]') as HTMLElement).click()

    await vi.waitFor(() => {
      expect(toastText(page)).toBe('Save failed: proxy must be an http://, https:// or socks5:// URL')
    })
    expect(isHidden(screenOf(page, 'web'))).toBe(false)
    expect((page.root.querySelector('[data-web-proxy]') as HTMLInputElement).value).toBe('localhost:10809')
  })

  it('cancel discards edits; reopen rebuilds from the payload', async () => {
    const page = await openPage(makeFetchHandler({}))

    openWebScreen(page)
    input(page.root.querySelector('[data-web-proxy]') as HTMLInputElement, 'http://elsewhere:1')
    engineDel(page, 'jina').click()
    expect(engineRow(page, 'jina')).toBeNull()
    ;(page.root.querySelector('[data-web-cancel]') as HTMLElement).click()
    expect(isHidden(screenOf(page, 'home'))).toBe(false)

    openWebScreen(page)
    expect((page.root.querySelector('[data-web-proxy]') as HTMLInputElement).value).toBe('http://localhost:10809')
    expect(engineKey(page, 'jina').value).toBe('jn-1')
    expect(page.root.querySelectorAll('[data-web-engine]').length).toBe(3)
  })
})

describe('add engine', () => {
  it('key input placeholder matches the provider key fields (phApiKey)', async () => {
    const page = await openPage(makeFetchHandler({}))

    openWebScreen(page)
    // Engine key rows and the add-screen key field share phApiKey so the
    // placeholder reads identically across every key input in the flow.
    expect(engineKey(page, 'anysearch').placeholder).toBe(t('phApiKey'))
    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()
    expect((page.root.querySelector('[data-web-add-key]') as HTMLInputElement).placeholder).toBe(t('phApiKey'))
  })

  it('tiles dim added engines, duckduckgo has no tile, zhipu select→key→save adds the row', async () => {
    const bodies: Array<{ proxy: string; engines: Record<string, string> }> = []
    const page = await openPage(makeFetchHandler({ onPutWeb: (b) => bodies.push(b) }))

    openWebScreen(page)
    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()
    expect(isHidden(screenOf(page, 'webAdd'))).toBe(false)

    // DDG is intrinsically supported — zero config surface, no tile at all.
    const tiles = [...page.root.querySelectorAll('[data-web-tile]')]
    expect(tiles.length).toBe(4)
    expect(page.root.querySelector('[data-web-tile="duckduckgo"]')).toBeNull()
    for (const id of ['anysearch', 'tavily', 'jina']) {
      const tile = page.root.querySelector(`[data-web-tile="${id}"]`) as HTMLElement
      expect(tile.className).toContain('opacity-35')
      expect(tile.className).toContain('pointer-events-none')
    }

    const zhipu = page.root.querySelector('[data-web-tile="zhipu"]') as HTMLElement
    expect(zhipu.className).not.toContain('opacity-35')
    zhipu.click()
    expect(zhipu.className).toContain('border-blue/50')

    input(page.root.querySelector('[data-web-add-key]') as HTMLInputElement, 'zk-2')
    ;(page.root.querySelector('[data-web-add-save]') as HTMLElement).click()
    expect(isHidden(screenOf(page, 'web'))).toBe(false)
    expect(engineLabel(page, 'zhipu')).toBe('Zhipu')
    expect(page.root.querySelectorAll('[data-web-engine]').length).toBe(4)

    ;(page.root.querySelector('[data-web-save]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(bodies.length).toBe(1)
    })
    expect(bodies[0].engines.zhipu).toBe('zk-2')
  })

  it('empty key blocks save with a toast, staying on the add screen', async () => {
    const page = await openPage(makeFetchHandler({}))

    openWebScreen(page)
    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()
    ;(page.root.querySelector('[data-web-add-save]') as HTMLElement).click()

    expect(toastText(page)).toBe('API key is required')
    expect(isHidden(screenOf(page, 'webAdd'))).toBe(false)
  })

  it('saturated payload dims every tile and blocks save with Select an engine', async () => {
    const saturated = structuredClone(WEB_PAYLOAD)
    saturated.web = {
      proxy: '',
      engines: { anysearch: 'a', tavily: 't', jina: 'j', zhipu: 'z' },
    }
    const page = await openPage(makeFetchHandler({ payload: saturated }), saturated)

    openWebScreen(page)
    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()

    const dimmed = [...page.root.querySelectorAll('[data-web-tile]')].filter((t) =>
      t.className.includes('opacity-35'),
    )
    expect(dimmed.length).toBe(4)
    ;(page.root.querySelector('[data-web-add-save]') as HTMLElement).click()
    expect(toastText(page)).toBe('Select an engine')
  })
})

describe('navigation', () => {
  it('back walks webAdd → web → home → close', async () => {
    const page = await openPage(makeFetchHandler({}))
    const back = page.root.querySelector('[data-back]') as HTMLElement

    openWebScreen(page)
    ;(page.root.querySelector('[data-add-web-engine]') as HTMLElement).click()
    expect(isHidden(screenOf(page, 'webAdd'))).toBe(false)

    back.click()
    expect(isHidden(screenOf(page, 'web'))).toBe(false)
    expect(isHidden(screenOf(page, 'webAdd'))).toBe(true)

    back.click()
    expect(isHidden(screenOf(page, 'home'))).toBe(false)

    back.click()
    expect(page.isOpen()).toBe(false)
  })
})

describe('language switch', () => {
  it('recomputes the composite summary in the new locale', async () => {
    const page = await openPage(makeFetchHandler({}))
    expect(webSubOf(page)).toBe('3 engines · proxy localhost:10809')

    setLocale('zh')
    const langRow = page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement
    langRow.click()
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()

    expect(webSubOf(page)).toBe('3 个引擎 · 代理 localhost:10809')
  })
})
