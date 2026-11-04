import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import { setLocale, initLocale, localeOptions, t } from './i18n'
import { pushDebugLog } from './log'
import { AVATAR_TOO_LARGE, refreshAvatar, resetAvatarStateForTest } from './avatar'
import { openAvatarCropper } from './avatar_crop'
import type { RemoteDevice } from './vnc'
import {
  fetchSettings,
  fetchAdminState,
  fetchRawSettings,
  saveSettings,
  saveDefaultModel,
  saveSttDefault,
  saveTTSDefault,
  postRestart,
  nativeRestartAvailable,
  postReloadConfig,
  testProvider,
  fetchProviderModels,
  createSettingsPage,
  type SettingsPayload,
  type SettingsProvider,
  type ModelsResult,
  type ReloadReport,
} from './settings'
import { dict as enDict } from './i18n/locales/en'
import { dict as zhDict } from './i18n/locales/zh'
import { dict as zhTWDict } from './i18n/locales/zh-TW'

// markUpgrading must be a no-op spy here: settings.ts arms upgrade mode
// on a 202, and the real one would touch module state no WS exists for.
// refuseUpgrade likewise — the capsule lives in the chat shell.
vi.mock('./upgrade_mode', () => ({
  refuseUpgrade: vi.fn(),
}))
vi.mock('./ws', () => ({
  markUpgrading: vi.fn(),
}))
// The cropper overlay is jsdom-hostile (real cropperjs elements, pointer
// gestures) — the wiring tests only need to control its Blob|null outcome.
vi.mock('./avatar_crop', () => ({
  openAvatarCropper: vi.fn(),
}))

// log.ts's ring buffer is module state with no reset hook; merging the two
// suites into one file made its app-log tests share that state, so the
// buffer is mocked with an interface-identical, resettable twin — afterEach
// drains it so every test starts from an empty WUI console.
const logMock = vi.hoisted(() => {
  const buffer: string[] = []
  const pad = (n: number, w: number) => String(n).padStart(w, '0')
  return {
    buffer,
    getDebugLogs: (): string[] => buffer.slice(),
    pushDebugLog: (...args: unknown[]): void => {
      const now = new Date()
      const ts = `${pad(now.getHours(), 2)}:${pad(now.getMinutes(), 2)}:${pad(now.getSeconds(), 2)}.${pad(now.getMilliseconds(), 3)}`
      const text = args
        .map((a) => {
          if (typeof a === 'object' && a !== null) {
            try {
              return JSON.stringify(a)
            } catch {
              return String(a)
            }
          }
          return String(a)
        })
        .join(' ')
      buffer.push(`${ts} ${text}`)
      if (buffer.length > 1000) buffer.shift()
    },
  }
})
vi.mock('./log', () => ({
  getDebugLogs: logMock.getDebugLogs,
  pushDebugLog: logMock.pushDebugLog,
}))

// vitest's jsdom pipeline resolves every highlight.js `?raw` CSS import to
// '' — darkCss and lightCss are then byte-identical and the dark/light
// branch at the chip handler is unobservable. Feed each module a fixture
// that encodes its file and role so the applied variant is assertable.
vi.mock('highlight.js/styles/github-dark.css?raw', () => ({ default: 'FIXTURE:github-dark' }))
vi.mock('highlight.js/styles/github.css?raw', () => ({ default: 'FIXTURE:github-light' }))
vi.mock('highlight.js/styles/atom-one-dark.css?raw', () => ({ default: 'FIXTURE:atom-one-dark' }))
vi.mock('highlight.js/styles/atom-one-light.css?raw', () => ({ default: 'FIXTURE:atom-one-light' }))
vi.mock('highlight.js/styles/a11y-dark.css?raw', () => ({ default: 'FIXTURE:a11y-dark' }))
vi.mock('highlight.js/styles/a11y-light.css?raw', () => ({ default: 'FIXTURE:a11y-light' }))
vi.mock('highlight.js/styles/gradient-dark.css?raw', () => ({ default: 'FIXTURE:gradient-dark' }))
vi.mock('highlight.js/styles/gradient-light.css?raw', () => ({ default: 'FIXTURE:gradient-light' }))
vi.mock('highlight.js/styles/isbl-editor-dark.css?raw', () => ({ default: 'FIXTURE:isbl-editor-dark' }))
vi.mock('highlight.js/styles/isbl-editor-light.css?raw', () => ({ default: 'FIXTURE:isbl-editor-light' }))
vi.mock('highlight.js/styles/kimbie-dark.css?raw', () => ({ default: 'FIXTURE:kimbie-dark' }))
vi.mock('highlight.js/styles/kimbie-light.css?raw', () => ({ default: 'FIXTURE:kimbie-light' }))
vi.mock('highlight.js/styles/nnfx-dark.css?raw', () => ({ default: 'FIXTURE:nnfx-dark' }))
vi.mock('highlight.js/styles/nnfx-light.css?raw', () => ({ default: 'FIXTURE:nnfx-light' }))
vi.mock('highlight.js/styles/panda-syntax-dark.css?raw', () => ({ default: 'FIXTURE:panda-syntax-dark' }))
vi.mock('highlight.js/styles/panda-syntax-light.css?raw', () => ({ default: 'FIXTURE:panda-syntax-light' }))
vi.mock('highlight.js/styles/paraiso-dark.css?raw', () => ({ default: 'FIXTURE:paraiso-dark' }))
vi.mock('highlight.js/styles/paraiso-light.css?raw', () => ({ default: 'FIXTURE:paraiso-light' }))
vi.mock('highlight.js/styles/qtcreator-dark.css?raw', () => ({ default: 'FIXTURE:qtcreator-dark' }))
vi.mock('highlight.js/styles/qtcreator-light.css?raw', () => ({ default: 'FIXTURE:qtcreator-light' }))
vi.mock('highlight.js/styles/stackoverflow-dark.css?raw', () => ({ default: 'FIXTURE:stackoverflow-dark' }))
vi.mock('highlight.js/styles/stackoverflow-light.css?raw', () => ({ default: 'FIXTURE:stackoverflow-light' }))
vi.mock('highlight.js/styles/tokyo-night-dark.css?raw', () => ({ default: 'FIXTURE:tokyo-night-dark' }))
vi.mock('highlight.js/styles/tokyo-night-light.css?raw', () => ({ default: 'FIXTURE:tokyo-night-light' }))
vi.mock('highlight.js/styles/rose-pine.css?raw', () => ({ default: 'FIXTURE:rose-pine-dark' }))
vi.mock('highlight.js/styles/rose-pine-dawn.css?raw', () => ({ default: 'FIXTURE:rose-pine-dawn-light' }))

// Two providers with the default on the SECOND one — discriminates name-match
// from index-0 in the default-pill rendering. First provider has no type
// (AUTO badge), second is typed with extra_params for round-trip checks.
const PAYLOAD: SettingsPayload = {
  providers: [
    {
      name: 'xiaomi',
      // The mutate suites' home-rendering assertion pins this URL verbatim;
      // no assertion on the main side reads it, so the union takes theirs.
      url: 'https://api.xiaomivivo.com/anthropic',
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

// The main and mutate suites pin different reports through one base shape:
// the main variant's 3 files drive banner 2/0/1 and expanded-failure rows,
// the mutate variant's 2 files drive banner 1/1/0 with settingsChanged off.
// One parameterized fixture keeps both assertable without drifting copies.
function makeReloadReport(variant: 'main' | 'mutate'): ReloadReport {
  if (variant === 'mutate') {
    return {
      applied: true,
      phase: 4,
      files: [
        { path: '/root/.gbot/settings.json', status: 'reloaded' },
        { path: '/root/.gbot/prompt.md', status: 'unchanged' },
      ],
      counts: { reloaded: 1, unchanged: 1, failed: 0 },
      promptChanged: true,
      enginesRefreshed: 2,
      settingsChanged: false,
      lastReloadAt: '2026-11-03T00:00:00Z',
    }
  }
  return {
    applied: true,
    phase: 4,
    files: [
      { path: '~/.gbot/settings.json', status: 'reloaded', note: 'max_tokens: 32768 → 16384', noteKey: 'newSessionsOnly' },
      { path: '~/.gbot/plugins/browser/mcp.json', status: 'reloaded' },
      {
        path: '~/.gbot/plugins/dbquery/mcp.json',
        status: 'failed',
        errLine: 'line 7: invalid character',
      },
    ],
    counts: { reloaded: 2, unchanged: 0, failed: 1 },
    promptChanged: true,
    enginesRefreshed: 2,
    settingsChanged: true,
    lastReloadAt: '2026-10-09T04:12:58Z',
  }
}

const OK_REPORT: ReloadReport = makeReloadReport('main')

interface MockOptions {
  payload?: SettingsPayload
  providersGetError?: boolean
  raw?: string
  rawError?: boolean
  models?: unknown
  testResult?: { ok: boolean; latencyMs?: number; error?: string }
  putError?: string
  onPutStt?: (body: { provider: string; model: string }) => void
  onPutTts?: (body: { provider: string; model: string; voice?: string }) => void
  onPutDefault?: (body: { provider: string; model: string }) => void
  sttPutError?: boolean
  ttsPutError?: boolean
  sttPutReject?: unknown
  ttsPutReject?: unknown
  deviceTestRejectWith?: unknown
  testRejectWith?: unknown
  ttsVoices?: string[]
  ttsVoicesError?: boolean
  ttsVoicesAbsent?: boolean
  logs?: string
  logsError?: boolean
  devices?: RemoteDevice[]
  devicesError?: boolean
  devicesPutError?: boolean
  deviceTest?: { ok: boolean; latencyMs?: number; error?: string }
  deviceTestReject?: boolean
  onDeviceTest?: (body: { addr: string; pass: string }) => void
  admin?: { busy: boolean; items: unknown[]; upgradable: boolean; build: string, reason?: string }
  restartPostStatus?: number
  restartPost?: { status: number; body: unknown }
  restartPostReject?: boolean
  reloadReport?: ReloadReport
  reloadError?: string
  // Avatar endpoint surface. HEAD defaults to 404 (no custom avatar); a
  // 200 override must go through the ETag header below, never a bare
  // object — probeAvatar reads resp.headers.
  avatarHead?: number
  avatarPutError?: string
  avatarDeleteStatus?: number
}

// Captures every POST body sent to /api/settings/models so tests can pin
// url/key/type/free without reaching into the mock's call log.
const modelsBodies: Array<Record<string, unknown>> = []

// makeFetchHandler stubs the settings endpoints, routing by URL+method and
// capturing PUT bodies via opts.onPut / opts.onPutDevices.
function makeFetchHandler(
  opts: MockOptions & {
    onPut?: (p: SettingsProvider[]) => void
    onPutDevices?: (d: RemoteDevice[]) => void
  } = {},
) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (url === '/api/settings/providers') {
      if (method === 'GET' && opts.providersGetError) {
        return { ok: false, status: 503, json: async () => ({}) }
      }
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
        json: async () => structuredClone(opts.payload ?? { providers: [], default: { provider: '', model: '' } }),
      }
    }
    if (url === '/api/settings/default' && method === 'PUT') {
      opts.onPutDefault?.(JSON.parse(String(init?.body)) as { provider: string; model: string })
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
    if (url === '/api/settings/raw') {
      if (opts.rawError) {
        return { ok: false, status: 500, json: async () => ({}) }
      }
      return { ok: true, status: 200, json: async () => ({ raw: opts.raw ?? '' }) }
    }
    if (url === '/api/settings/remotedesktop') {
      if (method === 'PUT') {
        if (opts.devicesPutError) {
          return { ok: false, status: 500, json: async () => ({ error: 'devices write failed' }) }
        }
        opts.onPutDevices?.(JSON.parse(String(init?.body)) as RemoteDevice[])
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      if (opts.devicesError) {
        return { ok: false, status: 500, json: async () => ({}) }
      }
      return { ok: true, status: 200, json: async () => ({ devices: opts.devices ?? [] }) }
    }
    if (url === '/api/settings/remotedesktop/test') {
      if (opts.deviceTestRejectWith !== undefined) throw opts.deviceTestRejectWith
      if (opts.deviceTestReject) throw new TypeError('fetch failed')
      const body = JSON.parse(String(init?.body ?? '{}')) as { addr?: string; pass?: string }
      opts.onDeviceTest?.({ addr: body.addr ?? '', pass: body.pass ?? '' })
      return { ok: true, status: 200, json: async () => opts.deviceTest ?? { ok: true, latencyMs: 12 } }
    }
    if (url === '/api/logs?tail=500') {
      if (opts.logsError) {
        return { ok: false, status: 500, text: async () => '' }
      }
      return { ok: true, status: 200, text: async () => opts.logs ?? '' }
    }
    if (url === '/api/settings/test') {
      if (opts.testRejectWith !== undefined) throw opts.testRejectWith
      return { ok: true, status: 200, json: async () => opts.testResult ?? { ok: true, latencyMs: 412 } }
    }
    if (url === '/api/admin/restart') {
      if (method === 'POST') {
        if (opts.restartPostReject) {
          throw new TypeError('fetch failed')
        }
        if (opts.restartPost) return { ok: opts.restartPost.status < 400, status: opts.restartPost.status, json: async () => opts.restartPost!.body }
        return { ok: opts.restartPostStatus !== 409, status: opts.restartPostStatus ?? 202, json: async () => ({ status: 'upgrading' }) }
      }
      return { ok: true, status: 200, json: async () => opts.admin ?? { busy: false, items: [], upgradable: true, build: 'dev · none' } }
    }
    if (url === '/api/settings/reload') {
      if (method === 'POST') {
        if (opts.reloadError) {
          return { ok: false, status: 500, text: async () => opts.reloadError }
        }
        return { ok: true, status: 200, json: async () => opts.reloadReport ?? OK_REPORT }
      }
    }
    if (url === '/api/settings/models') {
      modelsBodies.push(JSON.parse(String(init?.body ?? '{}')))
      return { ok: true, status: 200, json: async () => opts.models ?? { mode: 'fetched', models: [] } }
    }
    if (url === '/api/settings/stt' && method === 'PUT') {
      if (opts.sttPutReject !== undefined) throw opts.sttPutReject
      if (opts.sttPutError) {
        return { ok: false, status: 400, json: async () => ({ error: 'stt write failed' }) }
      }
      opts.onPutStt?.(JSON.parse(String(init?.body)) as { provider: string; model: string })
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
    if (url === '/api/settings/tts' && method === 'PUT') {
      if (opts.ttsPutReject !== undefined) throw opts.ttsPutReject
      if (opts.ttsPutError) {
        return { ok: false, status: 400, json: async () => ({ error: 'tts write failed' }) }
      }
      opts.onPutTts?.(JSON.parse(String(init?.body)) as { provider: string; model: string; voice?: string })
      return { ok: true, status: 200, json: async () => ({ ok: true }) }
    }
    if (url === '/api/tts/voices') {
      if (opts.ttsVoicesError) {
        return { ok: false, status: 500, json: async () => ({}) }
      }
      if (opts.ttsVoicesAbsent) {
        return { ok: true, status: 200, json: async () => ({}) }
      }
      return { ok: true, status: 200, json: async () => ({ voices: opts.ttsVoices ?? ['Aiden', 'Serena'] }) }
    }
    if (url.startsWith('/api/avatar/')) {
      if (method === 'HEAD') {
        const status = opts.avatarHead ?? 404
        return {
          ok: status < 400,
          status,
          headers: status === 200 ? new Headers({ ETag: '"t-1"' }) : new Headers(),
        }
      }
      if (method === 'PUT') {
        if (opts.avatarPutError) {
          return { ok: false, status: 400, json: async () => ({ error: opts.avatarPutError }) }
        }
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      if (method === 'DELETE') {
        const status = opts.avatarDeleteStatus ?? 204
        if (status >= 400) return { ok: false, status, json: async () => ({}) }
        return { ok: true, status }
      }
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

function toastText(page: { root: HTMLElement }): string {
  return (page.root.querySelector('[data-toast]') as HTMLElement).textContent ?? ''
}

// Flush the open() fetch→render chain with microtasks only — usable under
// fake timers (which must not be advanced past the toast window).
async function flushMicrotasks() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

function input(el: HTMLInputElement, value: string) {
  el.value = value
  el.dispatchEvent(new Event('input', { bubbles: true }))
}

function body0(page: { root: HTMLElement }): HTMLElement {
  return page.root.querySelector('[data-applog-lines]') as HTMLElement
}

beforeEach(() => {
  setLocale('en')
  Element.prototype.scrollIntoView = vi.fn()
})

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
  localStorage.removeItem('gbot-language')
  // Avatar module state survives across pages; without the reset one test's
  // probe result flips every later page's holders to <img>.
  resetAvatarStateForTest()
  logMock.buffer.length = 0
  delete window.GBotNative
  delete (window as unknown as Record<string, unknown>).GBotAppLogs
  document.body.innerHTML = ''
})

const RESTART_REST =
  'ml-auto shrink-0 bg-transparent border-none px-2 py-1 rounded-lg text-[13px] font-medium text-t1 cursor-pointer'
const RESTART_ARMED =
  'ml-auto shrink-0 bg-red/10 border-none px-2 py-1 rounded-lg text-[13px] font-semibold text-red cursor-pointer'

const AUDIO_PAYLOAD: SettingsPayload = {
  providers: [
    ...PAYLOAD.providers,
    {
      name: 'audiocpp',
      url: 'http://127.0.0.1:8082/v1',
      type: 'audio',
      keys: [],
      models: { 'stt-stream': {}, 'tts-voice': {} },
    },
  ],
  default: PAYLOAD.default,
  stt: { provider: 'audiocpp', model: 'stt-stream' },
  tts: { provider: 'audiocpp', model: 'tts-voice', voice: 'Aiden' },
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

describe('fetchRawSettings', () => {
  it('GETs /api/settings/raw and unwraps the raw string', async () => {
    const mock = makeFetchHandler({ raw: '{\n  "providers": []\n}' })
    vi.stubGlobal('fetch', mock)

    await expect(fetchRawSettings()).resolves.toBe('{\n  "providers": []\n}')

    expect(mock).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledWith('/api/settings/raw')
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
  it('passes free:true when the provider URL is OpenRouter', async () => {
    const bodies: Array<Record<string, unknown>> = []
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      bodies.push(JSON.parse(String(init?.body)))
      return { ok: true, status: 200, json: async () => ({ mode: 'fetched', models: [] }) }
    })
    vi.stubGlobal('fetch', mock)
    await fetchProviderModels('https://openrouter.ai/api/v1', 'k', 'openai', true)
    await fetchProviderModels('https://openrouter.ai/api/v1', 'k', 'openai', false)
    expect(bodies[0]).toEqual({ url: 'https://openrouter.ai/api/v1', key: 'k', type: 'openai', free: true })
    expect(bodies[1]).toEqual({ url: 'https://openrouter.ai/api/v1', key: 'k', type: 'openai' })
    vi.unstubAllGlobals()
  })
})

describe('createSettingsPage', () => {
  // i18n is a module-level singleton — tests set locales (zh renders), so
  // every case starts from the en baseline.
  beforeEach(() => {
    setLocale('en')
  })

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

  it('relabeled to free top 10 when the loaded provider URL is OpenRouter', async () => {
    const payload = {
      providers: [
        { ...PAYLOAD.providers[0], url: 'https://openrouter.ai/api/v1', keys: ['sk-x'] },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    const btn = page.root.querySelector('[data-fetch-models]') as HTMLElement
    expect(btn.textContent).toBe('· fetch free top 10')
    // typing a non-openrouter URL reverts the label
    const real = Array.from(page.root.querySelectorAll('input')).find((i) => i.value.startsWith('https://openrouter')) as HTMLInputElement
    real.value = 'https://api.zhipu.cn'
    real.dispatchEvent(new Event('input'))
    expect(btn.textContent).toBe('· fetch from API')
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
  it('free fetch REPLACES the previously fetched set (stale ids dropped, hand-added kept)', async () => {
    const FREE_PAYLOAD: SettingsPayload = {
      providers: [
        {
          name: 'openrouter-free',
          url: 'https://openrouter.ai/api/v1',
          type: 'openai',
          keys: ['sk-free'],
          free_fetched: ['old-free'],
          models: {
            'old-free': { context: '1M' },
            'hand-made': { context: '500k' },
          },
        },
      ],
      default: { provider: '', model: '' },
    }
    let putBody: SettingsProvider[] | undefined
    const mock = makeFetchHandler({
      payload: FREE_PAYLOAD,
      models: { mode: 'fetched', models: ['fresh-free'] },
      onPut: (p) => {
        putBody = p
      },
    })
    const page = await openPage(mock, FREE_PAYLOAD)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelector('[data-fetch-models]')).toBeTruthy()
    })
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(modelNames(page.root)).toContain('fresh-free')
    })
    expect(modelNames(page.root)).not.toContain('old-free')
    expect(modelNames(page.root)).toContain('hand-made')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    const models = putBody![0].models
    expect(Object.keys(models).sort()).toEqual(['fresh-free', 'hand-made'])
    expect(putBody![0].free_fetched).toEqual(['fresh-free'])
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

  // File content deliberately carrying a top-level key that exists in NO
  // provider — its presence in the sheet is the proof we render the FILE,
  // not a re-stringified providers slice.
  const RAW_FILE = `{
  "permission_mode": "default",
  "providers": [{"name": "zhipu"}]
}`

  it('JSON sheet shows the full settings.json file with key highlighting, refetching on every open', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, raw: RAW_FILE })
    const page = await openPage(mock)

    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const sheet = page.root.querySelector('[data-json-sheet]') as HTMLElement
    expect(sheet.style.display).not.toBe('none')
    const pre = sheet.querySelector('pre') as HTMLElement
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('permission_mode')
    })
    // Highlight chain ran over the file: keys are wrapped in spans.
    expect([...pre.querySelectorAll('span')].map((s) => s.textContent)).toContain('"permission_mode"')

    ;(sheet.querySelector('[data-sheet-close]') as HTMLElement).click()
    expect(sheet.style.display).toBe('none')
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('permission_mode')
    })
    expect(mock.mock.calls.filter((c) => c[0] === '/api/settings/raw')).toHaveLength(2)
  })

  it('JSON sheet from the edit screen shows the file, not the unsaved form state', async () => {
    // File lists zhipu only; the opened form edits xiaomi — the old
    // slice/collectFormProvider branch would render xiaomi instead.
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, raw: RAW_FILE }))

    ;(page.root.querySelectorAll('[data-provider-card]')[0] as HTMLElement).click()
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const pre = page.root.querySelector('[data-json-sheet] pre') as HTMLElement
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('permission_mode')
    })
    expect(pre.textContent).not.toContain('xiaomi')
  })

  // The sheet funnels arbitrary user-written file bytes into innerHTML —
  // the escape chain is the only guard. Swapping the &/< escape order or
  // widening a capture group must fail HERE, not in production.
  it('JSON sheet escapes hostile file content instead of parsing it as HTML', async () => {
    const hostile = '{"note": "<img src=x onerror=alert(1)></span><script>evil()</script> & \\"q\\""}'
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, raw: hostile }))

    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const pre = page.root.querySelector('[data-json-sheet] pre') as HTMLElement
    await vi.waitFor(() => {
      // textContent decodes entities, so the payload appears as literal
      // text — proof it was escaped, not parsed as markup.
      expect(pre.textContent).toContain('<img src=x onerror=alert(1)>')
    })
    expect(pre.querySelectorAll('img, script')).toHaveLength(0)
    // A stray closing tag must not break out of a highlight span either.
    expect(pre.querySelectorAll('script')).toHaveLength(0)
  })

  it('JSON sheet distinguishes a fetch failure from a missing file', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, rawError: true }))

    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const pre = page.root.querySelector('[data-json-sheet] pre') as HTMLElement
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('Failed to load settings.json')
    })
    expect(pre.textContent).not.toContain('not found')
  })

  it('JSON sheet shows a loading placeholder and a stale fetch cannot overwrite a newer open', async () => {
    const inner = makeFetchHandler({ payload: PAYLOAD })
    const parked: Array<{ reject: (e: unknown) => void }> = []
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/raw') {
        if (parked.length === 0) {
          // First open: park the fetch until the test rejects it.
          return new Promise((_resolve, reject) => parked.push({ reject }))
        }
        return { ok: true, status: 200, json: async () => ({ raw: '{"note":"second"}' }) }
      }
      return inner(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const pre = page.root.querySelector('[data-json-sheet] pre') as HTMLElement

    // First open: the placeholder shows while the fetch is parked.
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    expect(pre.textContent).toBe('…')

    // Close, reopen: the second fetch resolves and renders the file.
    ;(page.root.querySelector('[data-sheet-close]') as HTMLElement).click()
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('second')
    })

    // Now the stale first fetch rejects — it must not clobber the sheet.
    parked[0].reject(new Error('stale'))
    // Drain microtasks (the stale catch is 2-3 awaits deep) — a real write
    // would land deterministically, no timer race.
    for (let i = 0; i < 5; i++) await Promise.resolve()
    expect(pre.textContent).toContain('second')
  })

  it('JSON sheet shows a placeholder when settings.json does not exist yet', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))

    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()

    const pre = page.root.querySelector('[data-json-sheet] pre') as HTMLElement
    await vi.waitFor(() => {
      expect(pre.textContent).toContain('not found')
    })
    // No highlight spans over a placeholder.
    expect(pre.querySelectorAll('span')).toHaveLength(0)
  })

  it('toast auto-hides after 2.2s', async () => {
    vi.useFakeTimers()
    vi.stubGlobal('fetch', makeFetchHandler({ payload: PAYLOAD }))
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    page.open()
    await flushMicrotasks()

    // The Language row no longer toasts — the toast timer is exercised via
    // the synchronous name/url validation path (new-provider form has an
    // empty name) instead.
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    const toastEl = page.root.querySelector('[data-toast]') as HTMLElement
    expect(toastEl.textContent).toBe('Name and URL are required')
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
    const themeRow = rows[2] as HTMLElement // Avatars, Language, Theme, Highlights
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
    const hljsRow = rows[3] as HTMLElement
    hljsRow.click()
    const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    const chip = panel.querySelector('[data-hljs-opt="atom-one"]') as HTMLElement
    chip.click()
    expect(localStorage.getItem('gbot-hljs-theme')).toBe('atom-one')
    const value = hljsRow.querySelector('span.text-t2') as HTMLElement
    expect(value.textContent).toBe('Atom One')
  })
  it('header frosts over the status bar strip itself', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    // The root is the full-page scroller, so scrolled content passes through
    // the strip under the transparent status bar. The header must cover that
    // strip itself (top-0 + status-bar-height padding) so its backdrop blur
    // frosts it; the frame must not rely on safe-top padding, which would
    // leave the strip transparent.
    const frame = page.root.firstElementChild as HTMLElement
    expect(frame.className).not.toContain('sidebar-safe-top')
    const hdr = frame.firstElementChild as HTMLElement
    expect(hdr.className).toContain('top-0')
    expect(hdr.className).toContain('pt-[calc(env(safe-area-inset-top,0px)+14px)]')
    // Glass style parity with the chat top bar (header.ts): same card tint,
    // same 4px backdrop blur — not the page-local bg-bg/60 + 8px variant.
    expect(hdr.className).toContain('card-bg')
    expect(hdr.className).not.toContain('backdrop-blur')
  })

  it('renders the page in Chinese when localStorage pins zh', async () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))

    expect((page.root.querySelector('[data-title]') as HTMLElement).textContent).toBe('设置')
    expect((page.root.querySelector('[data-add-provider]') as HTMLElement).textContent).toBe('添加提供方')
    const home = page.root.querySelector('[data-screen="home"]') as HTMLElement
    const labels = [...home.children]
      .filter((c) => c.className.includes('text-t3'))
      .map((c) => c.textContent)
    expect(labels).toEqual(['提供方', '默认模型', '桌面', '配置', '通用'])
    // Protocol identifiers stay literal — only UI copy is translated.
    const cards = [...page.root.querySelectorAll('[data-provider-card]')] as HTMLElement[]
    expect(cards[0].querySelector('[data-type-badge]')?.textContent).toBe('AUTO')
    expect(cards[1].querySelector('[data-type-badge]')?.textContent).toBe('OPENAI')
  })

  it('Language row expands an Auto|English|简体中文 panel; no stored value preselects Auto', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const langRow = page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement
    const panel = page.root.querySelector('[data-language-panel]') as HTMLElement
    const chev = langRow.querySelector('span.text-t3') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(true)
    expect(chev.classList.contains('rotate-90')).toBe(false)
    langRow.click()
    expect(panel.classList.contains('hidden')).toBe(false)
    expect(chev.classList.contains('rotate-90')).toBe(true)
    const autoBtn = panel.querySelector('[data-lang-opt="auto"]') as HTMLElement
    const enBtn = panel.querySelector('[data-lang-opt="en"]') as HTMLElement
    const zhBtn = panel.querySelector('[data-lang-opt="zh"]') as HTMLElement
    expect(autoBtn.textContent).toBe('System')
    expect(enBtn.textContent).toBe('English')
    expect(zhBtn.textContent).toBe('简体中文')
    expect(autoBtn.className).toContain('text-blue')
    expect(enBtn.className).not.toContain('text-blue')
    expect(zhBtn.className).not.toContain('text-blue')
    expect((langRow.querySelector('span.text-t2') as HTMLElement).textContent).toBe('System')
  })

  it('the language segment offers System plus every registered locale in registry order', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    const opts = [...page.root.querySelectorAll('[data-lang-opt]')] as HTMLElement[]
    // Derived from localeOptions(), so a new locales/<x>.ts grows the
    // segment without touching settings.ts.
    expect(opts.map((b) => b.getAttribute('data-lang-opt'))).toEqual([
      'auto',
      ...localeOptions().map((o) => o.locale),
    ])
  })

  it('persisted zh preselects 简体中文 in the panel and the row value', async () => {
    localStorage.setItem('gbot-language', 'zh')
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const langRow = page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement
    langRow.click()
    const panel = page.root.querySelector('[data-language-panel]') as HTMLElement
    const sel = (opt: string) =>
      ((panel.querySelector(`[data-lang-opt="${opt}"]`) as HTMLElement).className.includes('text-blue'))
    expect(sel('zh')).toBe(true)
    expect(sel('auto')).toBe(false)
    expect(sel('en')).toBe(false)
    expect((langRow.querySelector('span.text-t2') as HTMLElement).textContent).toBe('简体中文')
  })

  it('persisted zh-TW renders Traditional Chinese copy in place', async () => {
    localStorage.setItem('gbot-language', 'zh-TW')
    initLocale()
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const title = page.root.querySelector('[data-title]') as HTMLElement
    const addBtn = page.root.querySelector('[data-add-provider]') as HTMLElement
    expect(title.textContent).toBe('設定')
    expect(addBtn.textContent).toBe('新增提供者')
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    const twBtn = page.root.querySelector('[data-lang-opt="zh-TW"]') as HTMLElement
    expect(twBtn.textContent).toBe('繁體中文')
    expect(twBtn.className).toContain('text-blue')
  })

  it('interpolated copy (model count) swaps language in place via its anchor', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const count = page.root.querySelector('[data-provider-card] span[data-i18n-arg]') as HTMLElement
    expect(count.textContent).toBe('1 model')
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()
    expect(count.textContent).toBe('1个模型')
    // Same DOM node — interpolated copy swaps via its anchor, no rebuild.
    expect(page.root.querySelector('[data-provider-card] span[data-i18n-arg]')).toBe(count)
  })
  it('switching to 简体中文 swaps text in place — same DOM nodes, no reload', async () => {
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const title = page.root.querySelector('[data-title]') as HTMLElement
    const addBtn = page.root.querySelector('[data-add-provider]') as HTMLElement
    expect(title.textContent).toBe('Settings')
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()
    expect(title.textContent).toBe('设置')
    expect(addBtn.textContent).toBe('添加提供方')
    expect(page.root.querySelector('[data-title]')).toBe(title)
    expect(localStorage.getItem('gbot-language')).toBe('zh')
    expect(reloadSpy).not.toHaveBeenCalled()
  })

  it('switching from zh to English swaps text in place (live-state regression)', async () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const title = page.root.querySelector('[data-title]') as HTMLElement
    expect(title.textContent).toBe('设置')
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="en"]') as HTMLElement).click()
    expect(title.textContent).toBe('Settings')
    expect(page.root.querySelector('[data-title]')).toBe(title)
    expect(localStorage.getItem('gbot-language')).toBe('en')
    expect(reloadSpy).not.toHaveBeenCalled()
  })

  it('after a switch the Language panel stays open and its names refresh', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const langRow = page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement
    langRow.click()
    const panel = page.root.querySelector('[data-language-panel]') as HTMLElement
    const chev = langRow.querySelector('span.text-t3') as HTMLElement
    const value = page.root.querySelector('[data-language-value]') as HTMLElement
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()
    // No rebuild: the expanded panel and rotated chevron survive the switch.
    expect(panel.classList.contains('hidden')).toBe(false)
    expect(chev.classList.contains('rotate-90')).toBe(true)
    expect(value.textContent).toBe('简体中文')
    const autoBtn = panel.querySelector('[data-lang-opt="auto"]') as HTMLElement
    const zhBtn = panel.querySelector('[data-lang-opt="zh"]') as HTMLElement
    expect(autoBtn.textContent).toBe('系统')
    expect(zhBtn.className).toContain('text-blue')
    expect(autoBtn.className).not.toContain('text-blue')
  })
  it('expanding a General row scrolls its panel into view (jsdom spies scroll)', async () => {
    // jsdom has no layout engine, so scrollIntoView is spied instead of felt.
    const spy = vi.fn()
    Element.prototype.scrollIntoView = spy
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    rows[3].click() // highlights row — the card's tail, the reported case
    expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
    const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(false)
    spy.mockClear()
    rows[3].click() // collapsing must not scroll
    expect(spy).not.toHaveBeenCalled()
  })

  it('selecting 系统 removes the stored value and re-resolves in place', async () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="auto"]') as HTMLElement).click()
    expect(localStorage.getItem('gbot-language')).toBeNull()
    // jsdom's navigator is en — auto resolves the copy back to English.
    expect((page.root.querySelector('[data-title]') as HTMLElement).textContent).toBe('Settings')
    expect((page.root.querySelector('[data-language-value]') as HTMLElement).textContent).toBe('System')
    expect(reloadSpy).not.toHaveBeenCalled()
  })

  it('failed localStorage write toasts and keeps the previous selection', async () => {
    vi.stubGlobal('localStorage', {
      getItem: () => null,
      setItem: () => {
        throw new Error('quota exceeded')
      },
      removeItem: () => {},
    })
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe('Save failed: quota exceeded')
    })
    expect(reloadSpy).not.toHaveBeenCalled()
    const autoBtn = page.root.querySelector('[data-lang-opt="auto"]') as HTMLElement
    const zhBtn = page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement
    expect(autoBtn.className).toContain('text-blue')
    expect(zhBtn.className).not.toContain('text-blue')
  })

  it('clicking the already-persisted language option does nothing', async () => {
    localStorage.setItem('gbot-language', 'en')
    const reloadSpy = vi.fn()
    vi.stubGlobal('location', { reload: reloadSpy })
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    ;(page.root.querySelectorAll('[data-general-row]')[1] as HTMLElement).click()
    ;(page.root.querySelector('[data-lang-opt="en"]') as HTMLElement).click()
    await flushMicrotasks()
    expect(reloadSpy).not.toHaveBeenCalled()
  })
})

describe('app log card', () => {
  const LINES = '[17:01:01] gbot: ready\n[17:01:02] Connected to gbot at 127.0.0.1:8765'

  beforeEach(() => {
    // A zh test inside this block pins the locale; the file-level afterEach
    // clears storage but not the module's resolved locale, so reset here to
    // keep the en-expecting tests deterministic.
    localStorage.removeItem('gbot-language')
    initLocale()
  })

  async function openAppLog(tail: ReturnType<typeof vi.fn>) {
    vi.stubGlobal('GBotAppLogs', { tail, clear: vi.fn() })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    return page
  }

  const rowTitle = (page: { root: HTMLElement }) =>
    (page.root.querySelector('[data-applog-row]') as HTMLElement).firstElementChild as HTMLElement

  it('expands a panel pulling tail(500) from the Android bridge, one line per row, monospaced', async () => {
    const tail = vi.fn(() => LINES)
    const page = await openAppLog(tail)
    expect(rowTitle(page).textContent).toBe('Logs')
    expect(tail).toHaveBeenCalledTimes(1)
    expect(tail).toHaveBeenCalledWith(500)
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.className).toContain('font-mono')
    expect([...body.children].map((c) => (c as HTMLElement).textContent)).toEqual([
      '[17:01:01] gbot: ready',
      '[17:01:02] Connected to gbot at 127.0.0.1:8765',
    ])
  })

  it('rebuilds the panel on every open — the second open shows the fresh tail', async () => {
    const tail = vi.fn().mockReturnValueOnce('first open').mockReturnValueOnce('second open')
    const page = await openAppLog(tail)
    const row = page.root.querySelector('[data-applog-row]') as HTMLElement
    row.click() // collapse
    row.click() // reopen
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(tail).toHaveBeenCalledTimes(2)
    expect([...body.children].map((c) => (c as HTMLElement).textContent)).toEqual(['second open'])
  })

  it('renders the card and panel copy in Chinese when zh is pinned', async () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const page = await openAppLog(vi.fn(() => LINES))
    expect(rowTitle(page).textContent).toBe('日志')
    // Single copy ICON — no text buttons for clear/scroll-bottom.
    const copyBtn = page.root.querySelector('[data-applog-copy]') as HTMLElement
    expect(copyBtn.querySelector('svg')).not.toBeNull()
    expect(copyBtn.textContent).not.toContain('复制')
    expect(page.root.querySelector('[data-applog-clear]')).toBeNull()
    expect(page.root.querySelector('[data-applog-bottom]')).toBeNull()
  })

  it('shows the log viewer on desktop with no app tab, defaulting to gbot', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, logs: 'daemon desktop line' })
    const page = await openPage(mock)
    // No GBotAppLogs stub: desktop browser. The viewer still mounts...
    expect(page.root.querySelector('[data-applog-row]')).not.toBeNull()
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    // ...without the app tab, defaulting to the daemon log tab.
    expect(page.root.querySelector('[data-applog-tab="app"]')).toBeNull()
    expect(page.root.querySelector('[data-applog-tab="wui"]')).not.toBeNull()
    expect(page.root.querySelector('[data-applog-tab="gbot"]')?.classList.contains('text-t1')).toBe(true)
    await vi.waitFor(() => {
      const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
      expect(body.textContent).toContain('daemon desktop line')
    })
  })

  it('copy writes the fetched tail via the clipboard API and toasts success', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    try {
      const page = await openAppLog(vi.fn(() => LINES))
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe('Copied')
      })
      expect(writeText).toHaveBeenCalledWith(LINES)
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })

  it('renders a 3-tab row app/wui/gbot with the app tab active by default', async () => {
    const page = await openAppLog(vi.fn(() => LINES))
    const tabs = [...page.root.querySelectorAll('[data-applog-tab]')] as HTMLElement[]
    expect(tabs.map((b) => b.getAttribute('data-applog-tab'))).toEqual(['app', 'wui', 'gbot'])
    expect(tabs.map((b) => b.textContent)).toEqual(['app', 'wui', 'GBot'])
    const active = page.root.querySelector('[data-applog-tab="app"]') as HTMLElement
    const inactive = page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement
    expect(active.className).toContain('text-t1')
    expect(active.className).not.toContain('text-t3')
    expect(inactive.className).toContain('text-t3')
    expect(inactive.className).not.toContain('text-t1')
    // Default tab is app — body already shows the bridge tail.
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.textContent).toContain('[17:01:01] gbot: ready')
  })

  it('keeps the tab labels literal app/wui/GBot under zh (technical terms)', async () => {
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    const page = await openAppLog(vi.fn(() => LINES))
    const labels = [...page.root.querySelectorAll('[data-applog-tab]')].map(
      (b) => (b as HTMLElement).textContent,
    )
    expect(labels).toEqual(['app', 'wui', 'GBot'])
  })

  it('switching to the wui tab swaps the body to the WUI console ring buffer', async () => {
    pushDebugLog('MARKER-WUI-A')
    pushDebugLog('MARKER-WUI-B')
    const page = await openAppLog(vi.fn(() => LINES))
    ;(page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement).click()
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.textContent).toContain('MARKER-WUI-A')
    expect(body.textContent).toContain('MARKER-WUI-B')
  })

  it('switching back to the app tab re-pulls the bridge tail', async () => {
    const tail = vi.fn(() => LINES)
    const page = await openAppLog(tail)
    ;(page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement).click()
    ;(page.root.querySelector('[data-applog-tab="app"]') as HTMLElement).click()
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.textContent).toContain('[17:01:02] Connected to gbot at 127.0.0.1:8765')
    expect(tail).toHaveBeenCalledTimes(2)
  })

  it('gbot tab fetches /api/logs?tail=500 and renders the daemon log text', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, logs: 'daemon line 1\ndaemon line 2' })
    vi.stubGlobal('GBotAppLogs', { tail: vi.fn(() => LINES), clear: vi.fn() })
    const page = await openPage(mock)
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    ;(page.root.querySelector('[data-applog-tab="gbot"]') as HTMLElement).click()
    await vi.waitFor(() => {
      const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
      expect(body.textContent).toContain('daemon line 2')
    })
    expect(mock).toHaveBeenCalledWith('/api/logs?tail=500')
  })

  it('reopening the panel resets to the app tab', async () => {
    const page = await openAppLog(vi.fn(() => LINES))
    ;(page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement).click()
    expect(
      (page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement).getAttribute('class'),
    ).toContain('text-t1')
    // Close (row click toggles) and reopen.
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    const active = page.root.querySelector('[data-applog-tab="app"]') as HTMLElement
    expect(active.getAttribute('class')).toContain('text-t1')
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.textContent).toContain('[17:01:02] Connected to gbot at 127.0.0.1:8765')
  })

  it('gbot tab fetch failure leaves the body empty', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, logsError: true })
    vi.stubGlobal('GBotAppLogs', { tail: vi.fn(() => LINES), clear: vi.fn() })
    const page = await openPage(mock)
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    ;(page.root.querySelector('[data-applog-tab="gbot"]') as HTMLElement).click()
    await flushMicrotasks()
    const body = page.root.querySelector('[data-applog-lines]') as HTMLElement
    expect(body.children.length).toBe(0)
  })

  it('copy copies the ACTIVE tab content — wui buffer instead of the app tail', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    try {
      pushDebugLog('MARKER-WUI-COPY')
      const page = await openAppLog(vi.fn(() => LINES))
      ;(page.root.querySelector('[data-applog-tab="wui"]') as HTMLElement).click()
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe('Copied')
      })
      expect(writeText).toHaveBeenLastCalledWith(expect.stringContaining('MARKER-WUI-COPY'))
      expect(writeText).toHaveBeenLastCalledWith(expect.not.stringContaining('[17:01:01] gbot: ready'))
    } finally {
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })

  it('copy falls back to execCommand when the clipboard API is absent (WebView on http)', async () => {
    const page = await openAppLog(vi.fn(() => LINES))
    // jsdom does not implement execCommand at all — install the WebView's
    // contract by hand.
    const exec = vi.fn(() => true)
    document.execCommand = exec as unknown as typeof document.execCommand
    try {
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe('Copied')
      })
      expect(exec).toHaveBeenCalledWith('copy')
    } finally {
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('copy falls back to execCommand when writeText rejects', async () => {
    const page = await openAppLog(vi.fn(() => LINES))
    Object.defineProperty(navigator, 'clipboard', {
      value: { writeText: vi.fn().mockRejectedValue(new Error('denied')) },
      configurable: true,
    })
    const exec = vi.fn(() => true)
    document.execCommand = exec as unknown as typeof document.execCommand
    try {
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-toast]') as HTMLElement).textContent).toBe('Copied')
      })
      expect(exec).toHaveBeenCalledWith('copy')
    } finally {
      delete (document as unknown as Record<string, unknown>).execCommand
      Reflect.deleteProperty(navigator, 'clipboard')
    }
  })
})

describe('remote endpoints card list + detail form', () => {
  beforeEach(() => {
    setLocale('en')
  })

  interface Endpoint {
    name: string
    host: string
    port: number
  }
  interface NativeStub {
    getRemoteTargets: () => string
    setRemoteTargets: ReturnType<typeof vi.fn>
    switchTo: ReturnType<typeof vi.fn>
  }
  const ENDPOINTS: Endpoint[] = [
    { name: '桌面', host: '192.168.1.5', port: 8765 },
    { name: 'nas', host: 'nas.box.local', port: 9000 },
  ]
  const stubNative = (remotes: Endpoint[] = []): NativeStub => {
    const stub: NativeStub = {
      getRemoteTargets: () => JSON.stringify(remotes),
      setRemoteTargets: vi.fn(),
      switchTo: vi.fn(),
    }
    vi.stubGlobal('GBotNative', stub)
    return stub
  }

  const card = (page: { root: HTMLElement }): HTMLElement =>
    page.root.querySelector('[data-remote-card]') as HTMLElement
  const entries = (page: { root: HTMLElement }): HTMLElement[] =>
    Array.from(card(page).querySelectorAll('[data-remote-entry]')) as HTMLElement[]
  // entry DOM: [0] = name row (name div + chevron), [1] = host:port subtitle.
  const entryName = (e: HTMLElement): string => (e.children[0].children[0] as HTMLElement).textContent ?? ''
  const entrySubtitle = (e: HTMLElement): string => (e.children[1] as HTMLElement).textContent ?? ''
  const formInput = (page: { root: HTMLElement }, sel: string): HTMLInputElement =>
    card(page).querySelector(sel) as HTMLInputElement
  const formVisible = (page: { root: HTMLElement }): boolean =>
    !(card(page).querySelector('[data-remote-form]') as HTMLElement).classList.contains('hidden')
  const deleteHidden = (page: { root: HTMLElement }): boolean =>
    (card(page).querySelector('[data-remote-delete]') as HTMLElement).classList.contains('hidden')
  const clickSave = (page: { root: HTMLElement }) =>
    (card(page).querySelector('[data-remote-save]') as HTMLElement).click()
  const clickDelete = (page: { root: HTMLElement }) =>
    (card(page).querySelector('[data-remote-delete]') as HTMLElement).click()
  const toastText = (page: { root: HTMLElement }): string =>
    (page.root.querySelector('[data-toast]') as HTMLElement).textContent ?? ''

  it('does not render without the GBotNative bridge (desktop browser)', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    expect(page.root.querySelector('[data-remote-card]')).toBeNull()
  })

  it('renders one card per stored endpoint — name + host:port subtitle', async () => {
    stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    expect(entries(page)).toHaveLength(2)
    expect(entries(page).map(entryName)).toEqual(['桌面', 'nas'])
    expect(entries(page).map(entrySubtitle)).toEqual(['192.168.1.5:8765', 'nas.box.local:9000'])
  })

  it('a blank stored name still shows verbatim (no host fallback) with the subtitle', async () => {
    stubNative([{ name: '', host: 'nas.box.local', port: 9000 }])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    expect(entryName(entries(page)[0])).toBe('')
    expect(entrySubtitle(entries(page)[0])).toBe('nas.box.local:9000')
  })

  it('card click opens the prefilled detail form; back returns to the list', async () => {
    stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    entries(page)[1].click()
    expect(formVisible(page)).toBe(true)
    expect(formInput(page, '[data-remote-name]').value).toBe('nas')
    expect(formInput(page, '[data-remote-host]').value).toBe('nas.box.local')
    expect(formInput(page, '[data-remote-port]').value).toBe('9000')
    expect(deleteHidden(page)).toBe(false)
    ;(card(page).querySelector('[data-remote-back]') as HTMLElement).click()
    expect(formVisible(page)).toBe(false)
    expect(entries(page)).toHaveLength(2)
  })

  it('+ opens an empty form; saving persists ALL entries in ONE setRemoteTargets call', async () => {
    const native = stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    expect(formVisible(page)).toBe(true)
    expect(deleteHidden(page)).toBe(true)
    input(formInput(page, '[data-remote-name]'), ' studio ')
    input(formInput(page, '[data-remote-host]'), ' 192.168.2.7 ')
    input(formInput(page, '[data-remote-port]'), '9001')
    clickSave(page)
    await flushMicrotasks()
    expect(native.setRemoteTargets).toHaveBeenCalledTimes(1)
    expect(native.setRemoteTargets).toHaveBeenCalledWith(
      JSON.stringify([
        { name: '桌面', host: '192.168.1.5', port: 8765 },
        { name: 'nas', host: 'nas.box.local', port: 9000 },
        { name: 'studio', host: '192.168.2.7', port: 9001 },
      ]),
    )
    expect(toastText(page)).toBe('Saved')
    expect(formVisible(page)).toBe(false)
    expect(entries(page)).toHaveLength(3)
  })

  it('delete removes the entry, persists, and returns to the list', async () => {
    const native = stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    entries(page)[1].click()
    clickDelete(page)
    await flushMicrotasks()
    expect(native.setRemoteTargets).toHaveBeenCalledTimes(1)
    expect(native.setRemoteTargets).toHaveBeenCalledWith(
      JSON.stringify([{ name: '桌面', host: '192.168.1.5', port: 8765 }]),
    )
    expect(toastText(page)).toBe('Saved')
    expect(formVisible(page)).toBe(false)
    expect(entries(page)).toHaveLength(1)
  })

  it('blank name toasts and never calls the bridge', async () => {
    const native = stubNative([])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-host]'), 'my.host')
    input(formInput(page, '[data-remote-port]'), '8765')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe('Name is required')
    expect(native.setRemoteTargets).not.toHaveBeenCalled()
    expect(formVisible(page)).toBe(true)
  })

  it('blank host toasts and never calls the bridge', async () => {
    const native = stubNative([])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'nas')
    input(formInput(page, '[data-remote-host]'), '   ')
    input(formInput(page, '[data-remote-port]'), '8765')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe('Host is required')
    expect(native.setRemoteTargets).not.toHaveBeenCalled()
  })

  it('out-of-range or non-numeric port toasts and never calls the bridge', async () => {
    const native = stubNative([])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'nas')
    input(formInput(page, '[data-remote-host]'), 'my.host')
    input(formInput(page, '[data-remote-port]'), '70000')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe('Port must be 1-65535')
    expect(native.setRemoteTargets).not.toHaveBeenCalled()
    input(formInput(page, '[data-remote-port]'), 'abc')
    clickSave(page)
    await flushMicrotasks()
    expect(native.setRemoteTargets).not.toHaveBeenCalled()
  })

  it('full-origin host (https://…) saves clean; a bare host/path still toasts invalid', async () => {
    const native = stubNative([])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'tail')
    input(formInput(page, '[data-remote-host]'), 'https://machine.ts.net')
    input(formInput(page, '[data-remote-port]'), '8765')
    clickSave(page)
    await flushMicrotasks()
    // Origin hosts carry scheme+port in the string — the validation passes
    // and the save path runs to its own 'Saved' toast.
    expect(native.setRemoteTargets).toHaveBeenCalledWith(
      JSON.stringify([{ name: 'tail', host: 'https://machine.ts.net', port: 8765 }]),
    )
    expect(toastText(page)).toBe('Saved')
    // The colon/slash ban still guards bare hostnames: a path in a
    // scheme-less host is a paste error, not an origin.
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'nas')
    input(formInput(page, '[data-remote-host]'), 'host/path')
    input(formInput(page, '[data-remote-port]'), '8765')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe('Invalid host (hostname only — no port or path)')
    expect(native.setRemoteTargets).toHaveBeenCalledTimes(1)
    expect(formVisible(page)).toBe(true)
  })

  it('duplicate names toast and never call the bridge (name is the identity)', async () => {
    const native = stubNative([{ name: 'nas', host: 'nas.box.local', port: 9000 }])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), ' nas ')
    input(formInput(page, '[data-remote-host]'), 'b.host')
    input(formInput(page, '[data-remote-port]'), '9000')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe('Names must be unique')
    expect(native.setRemoteTargets).not.toHaveBeenCalled()
  })

  it('a bridge rejection (false) skips the saved toast — Kotlin toasted the reason', async () => {
    const native = stubNative([])
    native.setRemoteTargets.mockReturnValue(false)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'nas')
    input(formInput(page, '[data-remote-host]'), 'my.host')
    input(formInput(page, '[data-remote-port]'), '8765')
    clickSave(page)
    await flushMicrotasks()
    expect(native.setRemoteTargets).toHaveBeenCalledTimes(1)
    expect(toastText(page)).toBe('')
  })
})

describe('remote desktop section', () => {
  beforeEach(() => {
    setLocale('en')
  })

  const DEVICES: RemoteDevice[] = [
    { name: 'win11', addr: 'ws://127.0.0.1:8006', pass: '' },
    { name: 'servere5', addr: 'http://127.0.0.1:5901', pass: 'pw' },
  ]

  const card = (page: { root: HTMLElement }): HTMLElement =>
    page.root.querySelector('[data-rd-card]') as HTMLElement
  const entries = (page: { root: HTMLElement }): HTMLElement[] =>
    Array.from(card(page).querySelectorAll('[data-rd-entry]')) as HTMLElement[]
  // entry DOM: [0] = name row (name div + chevron), [1] = addr subtitle.
  const entryName = (e: HTMLElement): string => (e.children[0].children[0] as HTMLElement).textContent ?? ''
  const entryAddr = (e: HTMLElement): string => (e.children[1] as HTMLElement).textContent ?? ''
  const formInput = (page: { root: HTMLElement }, sel: string): HTMLInputElement =>
    card(page).querySelector(sel) as HTMLInputElement
  const formVisible = (page: { root: HTMLElement }): boolean =>
    !(card(page).querySelector('[data-rd-form]') as HTMLElement).classList.contains('hidden')
  const deleteHidden = (page: { root: HTMLElement }): boolean =>
    (card(page).querySelector('[data-rd-delete]') as HTMLElement).classList.contains('hidden')
  const toastText = (page: { root: HTMLElement }): string =>
    (page.root.querySelector('[data-toast]') as HTMLElement).textContent ?? ''
  const devicePuts = (mock: ReturnType<typeof makeFetchHandler>) =>
    mock.mock.calls.filter(
      (c) => c[0] === '/api/settings/remotedesktop' && (c[1] as RequestInit | undefined)?.method === 'PUT',
    )

  it('renders one entry per device with exact name/addr, between providers and DEFAULT MODEL', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    await vi.waitFor(() => {
      expect(entries(page)).toHaveLength(2)
    })
    expect(entryName(entries(page)[0])).toBe('win11')
    expect(entryAddr(entries(page)[0])).toBe('ws://127.0.0.1:8006')
    expect(entryName(entries(page)[1])).toBe('servere5')
    expect(entryAddr(entries(page)[1])).toBe('http://127.0.0.1:5901')
    // DOM order: the section card comes after the whole endpoint config —
    // below the (bridge-gated) remote-endpoints card when it renders; in
    // this desktop env (no bridge) it is the home screen's last child.
    const home = page.root.querySelector('[data-screen="home"]') as HTMLElement
    const pos = (el: HTMLElement) => Array.from(home.children).indexOf(el)
    expect(pos(page.root.querySelector('[data-default-card]') as HTMLElement)).toBeLessThan(pos(card(page)))
    // The GENERAL card closes the page — remote desktop sits above it, and
    // the version row merged into it (same card as the language rows).
    const general = (page.root.querySelector('[data-general-row]') as HTMLElement).closest('.mx-3') as HTMLElement
    expect(pos(card(page))).toBeLessThan(pos(general))
    expect(home.lastElementChild).toBe(general)
    expect((page.root.querySelector('[data-system-row]') as HTMLElement).parentElement).toBe(general)
  })

  it('card click opens the prefilled form; back returns to the list', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))
    entries(page)[1].click()
    expect(formVisible(page)).toBe(true)
    expect((card(page).querySelector('[data-rd-list]') as HTMLElement).classList.contains('hidden')).toBe(true)
    expect(formInput(page, '[data-rd-name]').value).toBe('servere5')
    expect(formInput(page, '[data-rd-addr]').value).toBe('http://127.0.0.1:5901')
    expect(formInput(page, '[data-rd-pass]').value).toBe('pw')
    expect(formInput(page, '[data-rd-pass]').type).toBe('password')
    ;(card(page).querySelector('[data-rd-back]') as HTMLElement).click()
    expect(formVisible(page)).toBe(false)
    expect((card(page).querySelector('[data-rd-list]') as HTMLElement).classList.contains('hidden')).toBe(false)
  })

  it('+ opens an empty form with delete hidden', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))
    ;(card(page).querySelector('[data-rd-add]') as HTMLElement).click()
    expect(formVisible(page)).toBe(true)
    expect(deleteHidden(page)).toBe(true)
    expect(formInput(page, '[data-rd-name]').value).toBe('')
    expect(formInput(page, '[data-rd-addr]').value).toBe('')
    expect(formInput(page, '[data-rd-pass]').value).toBe('')
  })

  it('save validation: empty name / empty addr / duplicate name toast and send zero PUTs', async () => {
    let putDevices: RemoteDevice[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      devices: DEVICES,
      onPutDevices: (d) => (putDevices = d),
    })
    const page = await openPage(mock)
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))

    ;(card(page).querySelector('[data-rd-add]') as HTMLElement).click()
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    expect(toastText(page)).toBe('Name is required')

    input(formInput(page, '[data-rd-name]'), 'nas')
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    expect(toastText(page)).toBe('Address is required')

    input(formInput(page, '[data-rd-addr]'), 'ws://127.0.0.1:1')
    input(formInput(page, '[data-rd-name]'), 'win11')
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    expect(toastText(page)).toBe('Names must be unique')

    expect(devicePuts(mock)).toHaveLength(0)
    expect(putDevices).toBeUndefined()
    expect(formVisible(page)).toBe(true)
  })

  it('save PUTs the full next list, re-renders the addr, toasts, and closes the form', async () => {
    let putDevices: RemoteDevice[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      devices: DEVICES,
      onPutDevices: (d) => (putDevices = d),
    })
    const page = await openPage(mock)
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))

    entries(page)[0].click()
    input(formInput(page, '[data-rd-addr]'), 'ws://10.0.0.9:8006')
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    // The toast fires after the PUT resolves AND the list re-renders — wait
    // on it rather than the body capture, which happens mid-flight.
    await vi.waitFor(() => expect(toastText(page)).toBe('Saved'))

    expect(putDevices).toEqual([
      { name: 'win11', addr: 'ws://10.0.0.9:8006', pass: '' },
      { name: 'servere5', addr: 'http://127.0.0.1:5901', pass: 'pw' },
    ])
    expect(formVisible(page)).toBe(false)
    expect(entries(page)).toHaveLength(2)
    expect(entryAddr(entries(page)[0])).toBe('ws://10.0.0.9:8006')
  })

  it('delete PUTs the list without the entry and shrinks it by exactly one', async () => {
    let putDevices: RemoteDevice[] | undefined
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      devices: DEVICES,
      onPutDevices: (d) => (putDevices = d),
    })
    const page = await openPage(mock)
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))

    entries(page)[1].click()
    ;(card(page).querySelector('[data-rd-delete]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe('Saved'))

    expect(putDevices).toEqual([{ name: 'win11', addr: 'ws://127.0.0.1:8006', pass: '' }])
    expect(entries(page)).toHaveLength(1)
    expect(formVisible(page)).toBe(false)
  })

  it('test button: pending …/Testing… then ✓ latency (green) or ✗ error (red)', async () => {
    let resolveProbe: (v: { ok: boolean; latencyMs?: number; error?: string }) => void = () => {}
    const probe = new Promise<{ ok: boolean; latencyMs?: number; error?: string }>((r) => {
      resolveProbe = r
    })
    const base = makeFetchHandler({ payload: PAYLOAD, devices: DEVICES })
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/remotedesktop/test') {
        return { ok: true, status: 200, json: async () => probe }
      }
      return base(url, init)
    })
    vi.stubGlobal('fetch', mock)
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    page.open()
    await vi.waitFor(() => expect(entries(page)).toHaveLength(2))

    entries(page)[0].click()
    ;(card(page).querySelector('[data-rd-test]') as HTMLElement).click()
    expect((card(page).querySelector('[data-rd-test]') as HTMLElement).textContent).toBe('…')
    expect((card(page).querySelector('[data-rd-test-result]') as HTMLElement).textContent).toBe('Testing…')

    resolveProbe({ ok: true, latencyMs: 37 })
    await vi.waitFor(() => {
      expect((card(page).querySelector('[data-rd-test-result]') as HTMLElement).textContent).toBe('✓ 37ms')
    })
    expect((card(page).querySelector('[data-rd-test-result]') as HTMLElement).className).toContain('text-green')
    expect((card(page).querySelector('[data-rd-test]') as HTMLElement).textContent).toBe('Test')

    vi.stubGlobal('fetch', makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, deviceTest: { ok: false, error: 'boom' } }))
    const failPage = createSettingsPage()
    document.body.appendChild(failPage.root)
    failPage.open()
    await vi.waitFor(() => expect(entries(failPage)).toHaveLength(2))
    entries(failPage)[0].click()
    ;(card(failPage).querySelector('[data-rd-test]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((card(failPage).querySelector('[data-rd-test-result]') as HTMLElement).textContent).toBe('✗ boom')
    })
    expect((card(failPage).querySelector('[data-rd-test-result]') as HTMLElement).className).toContain('text-red')
    expect((card(failPage).querySelector('[data-rd-test]') as HTMLElement).textContent).toBe('Test')
  })

  it('devices fetch failure still renders providers and leaves the section empty', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devicesError: true }))
    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-provider-card]').length).toBe(2)
    })
    await flushMicrotasks()
    expect(page.root.querySelectorAll('[data-rd-entry]')).toHaveLength(0)
    expect(page.root.querySelector('[data-rd-card]')).not.toBeNull()
    expect(page.root.querySelector('[data-rd-add]')).not.toBeNull()
  })
})

describe('SYSTEM card (admin restart)', () => {
  it('keeps the button clickable under the TUI refusal and routes the code to the capsule, not static text', async () => {
    const { refuseUpgrade } = await import('./upgrade_mode')
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: {
        busy: false, items: [], upgradable: false, build: '1.2.3 · abc1234',
        reason: 'tui_mode',
      },
      restartPost: { status: 501, body: { error: 'TUI mode cannot hot-restart — exit and start the new binary manually', reason: 'tui_mode' } },
    })
    const page = await openPage(mock)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    ;(page.root.querySelector('[data-restart-btn]') as HTMLElement).click()
    ;(page.root.querySelector('[data-restart-btn]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(refuseUpgrade).toHaveBeenCalledWith('tui_mode', 'TUI mode cannot hot-restart — exit and start the new binary manually')
    })
    expect(page.root.querySelector('[data-system-active]')).toBeNull()
  })

  it('renders the build identity and an enabled restart button when idle + upgradable', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)

    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    expect(page.root.querySelector('[data-system-active]')).toBeNull()
  })

  it('busy keeps the button disabled with no inline notice', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: true, items: [{}, {}], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)

    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    expect((page.root.querySelector('[data-restart-btn]') as HTMLButtonElement).disabled).toBe(true)
    expect(page.root.querySelector('[data-system-active]')).toBeNull()
  })

  it('restart is two-tap: first tap arms (red confirm label, no POST), second tap fires', async () => {
    const { markUpgrading } = await import('./ws')
    const mock = makeFetchHandler({ payload: PAYLOAD, restartPostStatus: 202 })
    const page = await openPage(mock)

    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => { expect(btn.disabled).toBe(false) })
    btn.click()
    await flushMicrotasks()

    expect(btn.textContent).toBe(t('restartConfirmArm'))
    expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(false)
    btn.click()
    await flushMicrotasks()

    expect(markUpgrading).toHaveBeenCalledTimes(1)
    const posts = mock.mock.calls.filter(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')
    expect(posts.length).toBe(1)
  })

  it('armed restart disarms after 5s, restoring the resting label', async () => {
    vi.useFakeTimers()
    try {
      await import('./ws')
      const mock = makeFetchHandler({ payload: PAYLOAD, restartPostStatus: 202 })
      const page = await openPage(mock)
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => { expect(btn.disabled).toBe(false) })
      btn.click()
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      vi.advanceTimersByTime(4999)
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      vi.advanceTimersByTime(1)
      expect(btn.textContent).toBe(t('restartBtn'))
      btn.click()
      expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(false)
    } finally {
      vi.useRealTimers()
    }
  })

  it('409 silently refreshes — busy disables the button, no notice', async () => {
    await import('./ws')
    // Mutated in place after the initial idle render — the handler's json()
    // reads opts.admin at call time, so the 409's refresh sees the busy turn.
    const admin = { busy: false, items: [] as unknown[], upgradable: true, build: '1.2.3 · abc1234' }
    const mock = makeFetchHandler({ payload: PAYLOAD, restartPostStatus: 409, admin })
    const page = await openPage(mock)

    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => { expect(btn.disabled).toBe(false) })
    admin.busy = true
    admin.items = [{ kind: 'query' }]
    btn.click()
    btn.click()
    await flushMicrotasks()

    expect(page.root.querySelector('[data-toast]')?.textContent ?? '').not.toContain('busy')
    expect(btn.disabled).toBe(true)
  })

  it('long-press on the build value copies it; a short tap does not', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const write = vi.fn(async () => {})
    Object.defineProperty(navigator, 'clipboard', { value: { writeText: write }, configurable: true })
    const page = await openPage(mock)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    const value = page.root.querySelector('[data-system-build]') as HTMLElement

    vi.useFakeTimers()
    value.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    vi.advanceTimersByTime(499)
    value.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
    vi.advanceTimersByTime(2000)
    expect(write).not.toHaveBeenCalled()

    value.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    vi.advanceTimersByTime(500)
    expect(write).toHaveBeenCalledWith('1.2.3 · abc1234')
    await Promise.resolve()
    await Promise.resolve()
    vi.advanceTimersByTime(1800)
    vi.useRealTimers()
    expect(page.root.querySelector('[data-toast]')?.textContent).toContain('Copied')
  })
  it('long-press copies via execCommand when the clipboard API is absent (WebView on http)', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    // jsdom does not implement execCommand at all — install the WebView's
    // contract by hand.
    const exec = vi.fn(() => true)
    document.execCommand = exec as unknown as typeof document.execCommand
    const page = await openPage(mock)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    const value = page.root.querySelector('[data-system-build]') as HTMLElement
    vi.useFakeTimers()
    value.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    vi.advanceTimersByTime(500)
    vi.advanceTimersByTime(1800)
    vi.useRealTimers()
    expect(exec).toHaveBeenCalledWith('copy')
    expect(page.root.querySelector('[data-toast]')?.textContent).toContain('Copied')
    delete (document as unknown as Record<string, unknown>).execCommand
  })
  it('places version as the last row of the GENERAL card, log rows above it (android)', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    vi.stubGlobal('GBotAppLogs', { tail: vi.fn(() => ''), clear: vi.fn() })
    const page = await openPage(mock)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    const home = page.root.querySelector('[data-screen="home"]') as HTMLElement
    const general = (page.root.querySelector('[data-general-row]') as HTMLElement).closest('.mx-3') as HTMLElement
    // The version row lives INSIDE the general card — one card, not a
    // standalone system card after it.
    expect((page.root.querySelector('[data-system-row]') as HTMLElement).parentElement).toBe(general)
    // GENERAL is the page's last section and the version row is its last row.
    expect(home.lastElementChild).toBe(general)
    const rows = [...general.children]
    const sysIdx = rows.indexOf(page.root.querySelector('[data-system-row]') as HTMLElement)
    expect(sysIdx).toBe(rows.length - 1)
    // Android ordering: log head, then its panel, then a divider, then the
    // version row.
    const head = page.root.querySelector('[data-applog-row]') as HTMLElement
    const panel = page.root.querySelector('[data-applog-panel]') as HTMLElement
    const headIdx = rows.indexOf(head)
    const panelIdx = rows.indexOf(panel)
    expect(headIdx).toBeGreaterThan(-1)
    expect(panelIdx).toBe(headIdx + 1)
    const between = rows.slice(panelIdx + 1, sysIdx)
    expect(between).toHaveLength(1)
    expect(between[0].className).toContain('border-hairline')
    const value = page.root.querySelector('[data-system-build]') as HTMLElement
    expect(value.className).not.toContain('text-right')
    expect((general.querySelector('[data-restart-btn]') as HTMLElement).className).toContain('ml-auto')
  })

  it('renders the restart action as a borderless red row, not a chunky CTA button', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-restart-btn]') as HTMLElement).textContent).toContain('Restart')
    })
    const row = page.root.querySelector('[data-restart-btn]') as HTMLElement
    expect(row.tagName).toBe('BUTTON')
    expect(row.className).not.toMatch(/border-(?!none)/)
    // Resting keeps the normal label color — destructive red appears only
    // when armed. Compact inline chip, not a block row.
    expect(row.className).toContain('text-t1')
    expect(row.className).not.toContain('text-red')
    expect(row.className).toContain('px-2')
    expect(row.className).not.toContain('w-full')
    expect(row.className).not.toContain('font-semibold')
  })

  it('renders localized labels under zh via data-i18n anchors', async () => {
    setLocale('zh')
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)

    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
    })
    expect((page.root.querySelector('[data-restart-btn]') as HTMLElement).textContent).toBe('重启')
    expect(page.root.textContent).toContain('系统')
    expect(page.root.textContent).toContain('版本号')
    setLocale('en')
  })
})

describe('SYSTEM card (native restart bridge)', () => {
  const withNativeBridge = (fn: (fired: () => number) => Promise<void>) => async () => {
    let fired = 0
    window.GBotNative = { restartDaemon: () => { fired++ } }
    try {
      await fn(() => fired)
    } finally {
      delete window.GBotNative
    }
  }
  it('keeps the button enabled when the daemon is not upgradable (supervised Android)',
    withNativeBridge(async (_fired) => {
      const mock = makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: false, build: '1.2.3 · abc1234' },
      })
      const page = await openPage(mock)
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
      })
      expect((page.root.querySelector('[data-restart-btn]') as HTMLButtonElement).disabled).toBe(false)
    }))
  it('two-tap fires the bridge instead of POST and still arms upgrade mode',
    withNativeBridge(async (fired) => {
      const { markUpgrading } = await import('./ws')
      const mock = makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: false, build: '1.2.3 · abc1234' },
      })
      const page = await openPage(mock)
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => { expect(btn.disabled).toBe(false) })
      btn.click()
      await flushMicrotasks()
      btn.click()
      await flushMicrotasks()
      expect(fired()).toBe(1)
      expect(markUpgrading).toHaveBeenCalled()
      expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(false)
    }))
  it('remote page target never fires the bridge — POSTs the remote daemon instead',
    withNativeBridge(async (fired) => {
      // The Android WebView injects the bridge on every page it loads,
      // including remote gbot endpoints; the bridge only restarts the
      // PHONE-LOCAL daemon. A remote page must restart the remote server.
      vi.stubGlobal('location', { hostname: '100.117.35.55', port: '1234', protocol: 'https:', origin: 'https://100.117.35.55:1234' })
      const mock = makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      })
      const page = await openPage(mock)
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => { expect(btn.disabled).toBe(false) })
      btn.click()
      await flushMicrotasks()
      btn.click()
      await flushMicrotasks()
      expect(fired()).toBe(0)
      expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(true)
      // Remote 202 arms the capsule the same way — the page now waits on
      // the REMOTE daemon's 1012, which the POSTed target will produce.
      const { markUpgrading } = await import('./ws')
      expect(markUpgrading).toHaveBeenCalled()
      vi.unstubAllGlobals()
    }))
  it('busy still disables the button even with the bridge',
    withNativeBridge(async (_fired) => {
      const mock = makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: true, items: [{}], upgradable: false, build: '1.2.3 · abc1234' },
      })
      const page = await openPage(mock)
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('1.2.3 · abc1234')
      })
      expect((page.root.querySelector('[data-restart-btn]') as HTMLButtonElement).disabled).toBe(true)
    }))
})

// ---------------------------------------------------------------------------
// Config reload card
// ---------------------------------------------------------------------------


describe('config reload card', () => {
  beforeEach(() => {
    setLocale('en')
  })

  it('renders report rows from a successful daemon report', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: OK_REPORT }))
    const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
    expect(btn).toBeTruthy()
    btn.click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerOk')(2, 0, 1),
      )
    })
    const rows = page.root.querySelectorAll('[data-config-file]')
    expect(rows.length).toBe(3)
    const failedRow = page.root.querySelector('[data-config-file-status="failed"]') as HTMLElement
    expect(failedRow.textContent).toContain('line 7: invalid character')
    const reloadedRow = page.root.querySelector('[data-config-file-status="reloaded"]') as HTMLElement
    expect(reloadedRow.textContent).toContain('max_tokens: 32768 → 16384')
    expect(reloadedRow.textContent).toContain(t('configNoteNewSessions'))
    const failedBadge = failedRow.querySelector('[data-config-badge]') as HTMLElement
    const reloadedBadge = reloadedRow.querySelector('[data-config-badge]') as HTMLElement
    expect(failedBadge.className).toContain('bg-red/10')
    expect(reloadedBadge.className).toContain('bg-blue/10')
    expect((page.root.querySelector('[data-config-phases]') as HTMLElement).classList.contains('hidden')).toBe(true)
    expect((page.root.querySelector('[data-config-reload]') as HTMLButtonElement).disabled).toBe(false)
    const meta = (page.root.querySelector('[data-config-meta]') as HTMLElement).textContent ?? ''
    expect(meta.startsWith('Last reload:')).toBe(true)
  })
  it('collapses file rows after a clean reload and expands on demand', async () => {
    const clean = { ...OK_REPORT, counts: { reloaded: 3, unchanged: 0, failed: 0 } }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: clean }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerOk')(3, 0, 0),
      )
    })
    const rows = page.root.querySelector('[data-config-file]')?.parentElement as HTMLElement
    expect(rows.classList.contains('hidden')).toBe(true)
    const bannerRow = page.root.querySelector('[data-config-banner-row]') as HTMLElement
    const chev = bannerRow.querySelector('[data-config-chev]') as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(false)
    bannerRow.click()
    expect(rows.classList.contains('hidden')).toBe(false)
    expect(chev.classList.contains('rotate-90')).toBe(true)
    expect(page.root.querySelectorAll('[data-config-file]').length).toBe(3)
  })

  it('marks the failed phase and shows the abort banner on an aborted reload', async () => {
    const report: ReloadReport = {
      applied: false,
      phase: 1,
      err: 'plugins: invalid mcp.json',
      files: [{ path: '/x/plugins/broken/mcp.json', status: 'failed', errLine: 'bad json' }],
      counts: { reloaded: 0, unchanged: 0, failed: 1 },
      promptChanged: false,
      enginesRefreshed: 0,
      settingsChanged: false,
      lastReloadAt: '2026-10-09T04:12:58Z',
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerErr')(1),
      )
    })
    const phase0 = page.root.querySelector('[data-config-phase="0"]') as HTMLElement
    const phase1 = page.root.querySelector('[data-config-phase="1"]') as HTMLElement
    const phase2 = page.root.querySelector('[data-config-phase="2"]') as HTMLElement
    expect(phase0.textContent).toContain('✓')
    expect(phase1.textContent).toContain('✕')
    expect(phase2.classList.contains('hidden')).toBe(true)
  })

  it('uses only theme utilities — no hardcoded colors', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: OK_REPORT }))
    const card = page.root.querySelector('[data-config-card]') as HTMLElement
    expect(card.className).toContain('bg-ink2')
    expect(card.className).toContain('border-hairline')
    // Drive a full reload first: the phase dots only get their terminal
    // (done/fail) classNames after the daemon answers, and those are where
    // hardcoded hex would sneak in.
    ;(page.root.querySelector('[data-config-reload]') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerOk')(2, 0, 1),
      )
    })
    const walk = [card as Element, ...card.querySelectorAll('*')]
    for (const el of walk) {
      const style = el.getAttribute('style') ?? ''
      expect(style.includes('#')).toBe(false)
      expect(style.includes('rgb(')).toBe(false)
      const cls = el.getAttribute('class') ?? ''
      expect(cls.includes('[#')).toBe(false)
      expect(cls.includes('rgb(')).toBe(false)
    }
  })

  it('disables the button and advances the phase pulse while in flight', async () => {
    let resolveFetch!: (v: unknown) => void
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const hanging = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/reload' && init?.method === 'POST') {
        await new Promise((r) => {
          resolveFetch = r
        })
        return { ok: true, status: 200, json: async () => OK_REPORT }
      }
      return (mock as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(hanging as unknown as ReturnType<typeof makeFetchHandler>)
    const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
    btn.click()
    await flushMicrotasks()
    expect(btn.disabled).toBe(true)
    expect(btn.textContent).toBe(t('configReloading'))
    const phases = page.root.querySelector('[data-config-phases]') as HTMLElement
    expect(phases.classList.contains('hidden')).toBe(false)
    for (const ph of page.root.querySelectorAll('[data-config-phase]')) {
      expect((ph as HTMLElement).textContent).not.toContain('✓')
      expect((ph as HTMLElement).textContent).not.toContain('✕')
    }
    resolveFetch(undefined)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-reload]') as HTMLButtonElement).disabled).toBe(false)
    })
  })

  it('shows busy banner when the daemon reports a reload in progress', async () => {
    const report: ReloadReport = {
      applied: false,
      phase: 0,
      err: 'reload already in progress',
      files: [],
      counts: { reloaded: 0, unchanged: 0, failed: 0 },
      promptChanged: false,
      enginesRefreshed: 0,
      settingsChanged: false,
      lastReloadAt: '2026-10-09T04:12:58Z',
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLButtonElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerBusy'),
      )
    })
  })

  it('config card keys exist in every locale', () => {
    const configKeys = Object.keys(enDict).filter((k) => k.startsWith('config')) as Array<
      keyof typeof enDict
    >
    expect(configKeys.length).toBe(22)
    for (const key of configKeys) {
      expect(String((zhDict as Record<string, unknown>)[key] ?? '')).not.toBe('')
      expect(String((zhTWDict as Record<string, unknown>)[key] ?? '')).not.toBe('')
    }
  })
})

describe('STT settings surface', () => {
  const STT_PAYLOAD: SettingsPayload = {
    ...PAYLOAD,
    providers: [
      ...PAYLOAD.providers,
      {
        name: 'audiocpp',
        url: 'http://127.0.0.1:8082/v1',
        type: 'audio',
        keys: [],
        models: { 'stt-stream': {}, stt: {} },
      },
    ],
    stt: { provider: 'audiocpp', model: 'stt-stream' },
  }

  it('protocol tiles are 2×2 with Audio', async () => {
    const page = await openPage(makeFetchHandler({ payload: STT_PAYLOAD }), STT_PAYLOAD)
    const protos = page.root.querySelectorAll('[data-proto]')
    expect(protos.length).toBe(4)
    expect(page.root.querySelector('[data-proto="audio"]')!.textContent).toContain('OpenAI Audio API')
    const grid = (page.root.querySelector('[data-proto="audio"]') as HTMLElement).parentElement!
    expect(grid.className).toContain('grid-cols-2')
  })

  it('STT card lists audio providers only; LLM card filters them out', async () => {
    const page = await openPage(makeFetchHandler({ payload: STT_PAYLOAD }), STT_PAYLOAD)
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement    ;(sttCard.firstElementChild as HTMLElement).click()
    const sttPanel = page.root.querySelector('[data-stt-panel]') as HTMLElement
    expect(sttPanel.querySelectorAll('[data-stt-model="audiocpp/stt-stream"]').length).toBe(1)
    expect(sttPanel.textContent).not.toContain('zhipu')

    const llmCard = page.root.querySelector('[data-default-card]') as HTMLElement
    ;(llmCard.firstElementChild as HTMLElement).click()
    const llmPanel = page.root.querySelector('[data-default-panel]') as HTMLElement
    expect(llmPanel.textContent).not.toContain('audiocpp')
    expect(llmPanel.textContent).toContain('zhipu')
  })

  it('unconfigured STT card is greyed; selecting a model PUTs /api/settings/stt', async () => {
    let putBody: { provider: string; model: string } | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: { ...STT_PAYLOAD, stt: undefined },
        onPutStt: (b) => (putBody = b),
      }),
      { ...STT_PAYLOAD, stt: undefined },
    )
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    expect(sttCard.classList.contains('opacity-55')).toBe(true)
    expect(sttCard.textContent).toContain('Not configured')

    ;(sttCard.firstElementChild as HTMLElement).click()
    const row = page.root.querySelector('[data-stt-model="audiocpp/stt-stream"]') as HTMLElement
    row.click()
    await vi.waitFor(() =>
      expect(putBody).toEqual({ provider: 'audiocpp', model: 'stt-stream' }),
    )
    await vi.waitFor(() => {
      expect(sttCard.classList.contains('opacity-55')).toBe(false)
      expect((sttCard.querySelector('.font-mono') as HTMLElement).textContent).toBe(
        'audiocpp / stt-stream',
      )
    })
  })

  it('TTS third card: audio-only models, PUT /api/settings/tts on select, grey when unset', async () => {
    let putBody: { provider: string; model: string } | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: { ...STT_PAYLOAD, tts: undefined },
        onPutTts: (b) => (putBody = b),
      }),
      { ...STT_PAYLOAD, tts: undefined },
    )
    const ttsCard = page.root.querySelector('[data-tts-card]') as HTMLElement
    expect(ttsCard).toBeTruthy()
    expect(ttsCard.classList.contains('opacity-55')).toBe(true)
    expect(ttsCard.textContent).toContain('Not configured')

    ;(ttsCard.firstElementChild as HTMLElement).click()
    const panel = page.root.querySelector('[data-tts-panel]') as HTMLElement
    expect(panel.querySelectorAll('[data-tts-model="audiocpp/qwen3-tts-1.7b"]').length).toBe(0) // not in fixture models yet
    const row = page.root.querySelector('[data-tts-model="audiocpp/stt"]') as HTMLElement
    expect(row).toBeTruthy() // any audio model is selectable
    row.click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'audiocpp', model: 'stt', voice: '' }))
    await vi.waitFor(() => {
      expect(ttsCard.classList.contains('opacity-55')).toBe(false)
      expect((ttsCard.querySelector('.font-mono') as HTMLElement).textContent).toBe('audiocpp / stt')
    })
  })

  it('TTS voice rows: engine catalog renders as radio rows (no select popup), tapping PUTs tts.voice', async () => {
    let putBody: { provider: string; model: string; voice?: string } | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: { ...STT_PAYLOAD, tts: { provider: 'audiocpp', model: 'stt' } },
        onPutTts: (b) => (putBody = b),
        ttsVoices: ['Aiden', 'Serena', 'Ryan'],
      }),
      { ...STT_PAYLOAD, tts: { provider: 'audiocpp', model: 'stt' } },
    )
    const ttsCard = page.root.querySelector('[data-tts-card]') as HTMLElement
    ;(ttsCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => {
      const rows = Array.from(page.root.querySelectorAll('[data-tts-voice]')) as HTMLElement[]
      expect(rows.map((r) => r.dataset.ttsVoice)).toEqual(['', 'Aiden', 'Serena', 'Ryan'])
    })
    // No native picker — the rows ride the model-row radio idiom.
    expect(page.root.querySelector('[data-tts-panel] select')).toBeNull()
    const serena = Array.from(page.root.querySelectorAll('[data-tts-voice]')).find(
      (r) => (r as HTMLElement).dataset.ttsVoice === 'Serena',
    ) as HTMLElement
    serena.click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'audiocpp', model: 'stt', voice: 'Serena' }))
    // Selection state moves to the tapped row (radio dot + label tint).
    await vi.waitFor(() => {
      const sel = Array.from(page.root.querySelectorAll('[data-tts-voice]')).find(
        (r) => (r as HTMLElement).dataset.ttsVoice === 'Serena',
      ) as HTMLElement
      expect(sel.querySelector('.bg-blue')).toBeTruthy()
    })
    // Collapse/reopen re-renders while the catalog promise may still be in
    // flight — exactly one row set must survive (render-epoch guard).
    ;(ttsCard.firstElementChild as HTMLElement).click()
    ;(ttsCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-tts-voice]')).toHaveLength(4)
    })
  })

  it('audio task probe: a failed first probe retries on the next card open (badges self-heal)', async () => {
    let released = false
    let modelsCalls = 0
    const base = makeFetchHandler({
      payload: STT_PAYLOAD,
      models: { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }, { id: 'stt', task: 'tts' }] },
    })
    const handler = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/models' && !released) {
        modelsCalls++
        return { ok: true, status: 200, json: async () => ({ mode: 'error', error: 'backend restarting' }) }
      }
      return base(url, init)
    })
    const page = await openPage(handler, STT_PAYLOAD)
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    ;(sttCard.firstElementChild as HTMLElement).click()
    // The gated first probe errors; its cache-drop lands with it.
    await vi.waitFor(() => expect(modelsCalls).toBe(1))
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-stt-panel]') as HTMLElement).classList.contains('hidden')).toBe(false)
    })
    // Unfiltered first render: no task knowledge → both config rows listed.
    expect(page.root.querySelectorAll('[data-stt-model]')).toHaveLength(2)
    // Backend comes back; the failed probe must not stay cached forever.
    released = true
    // Keep reopening until the retried probe succeeds: it goes through
    // base's success stub (outside the error counter), so the task filter
    // engaging (the tts-tasked row drops off the stt card) is the
    // user-visible outcome that matters.
    await vi.waitFor(() => {
      ;(sttCard.firstElementChild as HTMLElement).click() // collapse
      ;(sttCard.firstElementChild as HTMLElement).click() // reopen
      expect(page.root.querySelectorAll('[data-stt-model]')).toHaveLength(1)
    })
  })

  it('audio task probe: a REJECTED probe also retries (catch-branch cache drop)', async () => {
    let released = false
    let modelsCalls = 0
    const base = makeFetchHandler({
      payload: STT_PAYLOAD,
      models: { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }, { id: 'stt', task: 'tts' }] },
    })
    const handler = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/models' && !released) {
        modelsCalls++
        throw new Error('transport down') // rejection, not a resolved error mode
      }
      return base(url, init)
    })
    const page = await openPage(handler, STT_PAYLOAD)
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    ;(sttCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => expect(modelsCalls).toBe(1))
    released = true
    await vi.waitFor(() => {
      ;(sttCard.firstElementChild as HTMLElement).click() // collapse
      ;(sttCard.firstElementChild as HTMLElement).click() // reopen
      expect(page.root.querySelectorAll('[data-stt-model]')).toHaveLength(1)
    })
  })

  it('provider model rows wear the TTS tag on the configured tts default', async () => {
    const payload = { ...STT_PAYLOAD, tts: { provider: 'audiocpp', model: 'stt', voice: 'Mike' } }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    const audioCard = Array.from(page.root.querySelectorAll('[data-provider-card]')).find(
      (c) => c.textContent!.includes('audiocpp'),
    ) as HTMLElement
    audioCard.click()
    await vi.waitFor(() => {
      const rows = page.root.querySelectorAll('[data-model-row]')
      const tagged = Array.from(rows).filter((r) => r.querySelector('[data-tts-badge]'))
      expect(tagged.length).toBe(1)
      expect((tagged[0].querySelector('[data-model-name]') as HTMLInputElement).value).toBe('stt')
    })
  })

  it('audio provider card shows an AUDIO badge; model rows carry the STT tag on the default, no disclosure', async () => {
    const page = await openPage(makeFetchHandler({ payload: STT_PAYLOAD }), STT_PAYLOAD)
    const audioCard = Array.from(page.root.querySelectorAll('[data-provider-card]')).find(
      (c) => c.textContent!.includes('audiocpp'),
    ) as HTMLElement
    expect(audioCard.querySelector('[data-type-badge]')!.textContent).toBe('AUDIO')

    audioCard.click()
    await vi.waitFor(() => {
      const rows = page.root.querySelectorAll('[data-model-row]')
      // The row configured as stt.default wears the STT tag (which voice
      // model the mic uses); others stay bare. No disclosure chevron —
      // voice models have no editable detail.
      const tagged = Array.from(rows).filter((r) => r.querySelector('[data-stt-badge]'))
      expect(tagged.length).toBe(1)
      expect((tagged[0].querySelector('[data-model-name]') as HTMLInputElement).value).toBe('stt-stream')
      for (const r of Array.from(rows)) {
        expect(r.textContent).not.toContain('›')
      }
    })
  })

  it('audio type shows the fetch-models affordance and the keyless hint', async () => {
    const page = await openPage(makeFetchHandler({ payload: STT_PAYLOAD }), STT_PAYLOAD)
    const audioCard = Array.from(page.root.querySelectorAll('[data-provider-card]')).find(
      (c) => c.textContent!.includes('audiocpp'),
    ) as HTMLElement
    audioCard.click()
    // audio.cpp serves the standard /models list — the fetch affordance
    // applies (only Anthropic drops it).
    await vi.waitFor(() => {
      expect(
        (page.root.querySelector('[data-fetch-models]') as HTMLElement).style.display,
      ).toBe('')
    })
    expect(page.root.textContent).toContain('fetch from API')
    // Audio model detail renders the voice note instead of chat params —
    // unconditional click: a silent miss must fail the test, not skip it.
    const firstModel = page.root.querySelector('[data-model-name]') as HTMLElement
    expect(firstModel).toBeTruthy()
    firstModel.click()
    await vi.waitFor(() => {
      expect(page.root.textContent).toContain('Voice models take no session params')
      expect(page.root.textContent).not.toContain('Context')
    })
  })
})

// Audio model task metadata (audio.cpp /v1/models "task":"asr"|"tts" field):
// opening an audio default card fetches the provider's model list and splits
// it — asr models into the STT card, tts models into the TTS card, both
// badged; models without task info stay listed everywhere (10-28 ruling: 有
// task 就用，没有就不过滤). Fetch failure falls back to the config list.
describe('audio model task badges', () => {
  const TASK_PAYLOAD: SettingsPayload = {
    ...PAYLOAD,
    providers: [
      ...PAYLOAD.providers,
      {
        name: 'audiocpp',
        url: 'http://127.0.0.1:8082/v1',
        type: 'audio',
        keys: [],
        models: { 'asr-m': {}, 'tts-m': {}, 'plain-m': {} },
      },
    ],
    stt: { provider: 'audiocpp', model: 'asr-m' },
    tts: { provider: 'audiocpp', model: 'tts-m' },
  }
  const TASK_MODELS: ModelsResult = {
    mode: 'fetched',
    models: [
      { id: 'asr-m', task: 'asr' },
      { id: 'tts-m', task: 'tts' },
      { id: 'plain-m' },
    ],
  }

  it('task metadata splits both cards; taskless models stay unfiltered', async () => {
    const page = await openPage(makeFetchHandler({ payload: TASK_PAYLOAD, models: TASK_MODELS }), TASK_PAYLOAD)

    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    ;(sttCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => {
      const sttPanel = page.root.querySelector('[data-stt-panel]') as HTMLElement
      expect(sttPanel.querySelectorAll('[data-stt-model="audiocpp/asr-m"]').length).toBe(1)
      // tts model hidden from the STT card; taskless model stays listed
      expect(sttPanel.querySelectorAll('[data-stt-model="audiocpp/tts-m"]').length).toBe(0)
      expect(sttPanel.querySelectorAll('[data-stt-model="audiocpp/plain-m"]').length).toBe(1)
    })

    const ttsCard = page.root.querySelector('[data-tts-card]') as HTMLElement
    ;(ttsCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => {
      const ttsPanel = page.root.querySelector('[data-tts-panel]') as HTMLElement
      expect(ttsPanel.querySelectorAll('[data-tts-model="audiocpp/tts-m"]').length).toBe(1)
      expect(ttsPanel.querySelectorAll('[data-stt-model="audiocpp/asr-m"]').length).toBe(0)
      expect(ttsPanel.querySelectorAll('[data-tts-model="audiocpp/asr-m"]').length).toBe(0)
      expect(ttsPanel.querySelectorAll('[data-tts-model="audiocpp/plain-m"]').length).toBe(1)
    })
  })

  it('taskless /v1/models response → full config list, no badges', async () => {
    const page = await openPage(
      makeFetchHandler({
        payload: TASK_PAYLOAD,
        models: { mode: 'fetched', models: [{ id: 'asr-m' }, { id: 'tts-m' }, { id: 'plain-m' }] },
      }),
      TASK_PAYLOAD,
    )
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    ;(sttCard.firstElementChild as HTMLElement).click()
    await vi.waitFor(() => {
      const sttPanel = page.root.querySelector('[data-stt-panel]') as HTMLElement
      expect(sttPanel.querySelectorAll('[data-stt-model]').length).toBe(3)
    })
  })

  it('fetch failure → config-list fallback, silent (no toast)', async () => {
    const mock = makeFetchHandler({ payload: TASK_PAYLOAD, models: { mode: 'error', error: 'HTTP 500' } })
    const page = await openPage(mock, TASK_PAYLOAD)
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    ;(sttCard.firstElementChild as HTMLElement).click()
    // The panel must render the full list immediately (fetch never blocks)
    const sttPanel = page.root.querySelector('[data-stt-panel]') as HTMLElement
    expect(sttPanel.querySelectorAll('[data-stt-model]').length).toBe(3)
    // ...and stay that way once the failed fetch settles — no toast either.
    await vi.waitFor(() =>
      expect(mock).toHaveBeenCalledWith('/api/settings/models', expect.objectContaining({ method: 'POST' })),
    )
    for (let i = 0; i < 10; i++) await Promise.resolve()
    expect(sttPanel.querySelectorAll('[data-stt-model]').length).toBe(3)
    expect((page.root.querySelector('[data-toast]') as HTMLElement).classList.contains('toast-show')).toBe(false)
  })
})

describe('avatar row', () => {
  it('renders as the first General row with two 22px thumbnails', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    const avatarRow = rows[0] as HTMLElement
    expect((avatarRow.querySelector('span.text-\\[13px\\]') as HTMLElement).textContent).toBe('Avatars')
    const miniAgent = avatarRow.querySelector('[data-avatar="agent"]') as HTMLElement
    const miniUser = avatarRow.querySelector('[data-avatar="user"]') as HTMLElement
    expect(miniAgent).toBeTruthy()
    expect(miniUser).toBeTruthy()
    expect(miniAgent.className).toContain('w-[22px]')
    expect(miniUser.className).toContain('w-[22px]')
    // Prototype truth: the user mini overlaps by -5px.
    expect(miniUser.className).toContain('-ml-[5px]')
    expect(miniAgent.textContent).toBe('G')
    expect(miniUser.querySelector('svg')).toBeTruthy()
  })

  it('folds: panel starts hidden, row click opens it and rotates the chevron', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const avatarRow = page.root.querySelectorAll('[data-general-row]')[0] as HTMLElement
    const panel = page.root.querySelector('[data-avatar-panel]') as HTMLElement
    const chev = avatarRow.querySelector('span.text-t3') as HTMLElement
    expect(panel.classList.contains('hidden')).toBe(true)
    expect(chev.classList.contains('rotate-90')).toBe(false)
    avatarRow.click()
    expect(panel.classList.contains('hidden')).toBe(false)
    expect(chev.classList.contains('rotate-90')).toBe(true)
    avatarRow.click()
    expect(panel.classList.contains('hidden')).toBe(true)
    expect(chev.classList.contains('rotate-90')).toBe(false)
  })

  it('remove flow: DELETE hits the wire, then the 404 probe resets every holder', async () => {
    const opts: Parameters<typeof makeFetchHandler>[0] = { payload: PAYLOAD, avatarHead: 200 }
    const mock = makeFetchHandler(opts)
    const page = await openPage(mock)
    await refreshAvatar('user')
    const minis = page.root.querySelectorAll('[data-avatar="user"]')
    const bigs = page.root.querySelectorAll('[data-avatar-panel] [data-avatar="user"]')
    expect(minis.length).toBe(2) // row mini + panel big
    expect(bigs.length).toBe(1)
    for (const el of Array.from(minis)) expect(el.querySelector('img')).toBeTruthy()

    // The remove click re-probes; flipping the mock to 404 makes the refresh
    // fall back to defaults.
    opts.avatarHead = 404
    ;(page.root.querySelector('[data-avatar-remove="user"]') as HTMLElement).click()

    await vi.waitFor(() => {
      const after = Array.from(page.root.querySelectorAll('[data-avatar="user"]'))
      for (const el of after) expect(el.querySelector('img')).toBeNull()
    })
    expect(mock.mock.calls.some(([url, init]) =>
      url === '/api/avatar/user' && init?.method === 'DELETE')).toBe(true)
    const svg = page.root.querySelector('[data-avatar-panel] [data-avatar="user"] svg')
    expect(svg?.getAttribute('width')).toBe('32')
  })

  it('upload flow: change opens the cropper, confirm PUTs the blob then probe swaps holders', async () => {
    const opts: Parameters<typeof makeFetchHandler>[0] = { payload: PAYLOAD, avatarHead: 200 }
    const mock = makeFetchHandler(opts)
    const page = await openPage(mock)
    const blob = new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' })
    // Module mock call history leaks across tests in this file — reset it.
    vi.mocked(openAvatarCropper).mockReset().mockResolvedValue(blob)
    // The old direct-decode chain must be gone: no bitmap decode anywhere.
    const createImageBitmap = vi.fn()
    vi.stubGlobal('createImageBitmap', createImageBitmap)

    const input = page.root.querySelector(
      '[data-avatar-panel] input[type="file"]') as HTMLInputElement
    input.dataset.target = 'user'
    const file = new File([new Uint8Array([9, 9])], 'me.png', { type: 'image/png' })
    Object.defineProperty(input, 'files', { value: [file], configurable: true })
    input.dispatchEvent(new Event('change'))
    await vi.waitFor(() => {
      const put = mock.mock.calls.find(([u, init]) =>
        u === '/api/avatar/user' && init?.method === 'PUT')
      expect(put).toBeTruthy()
      expect(put![1]?.body).toBe(blob)
      expect((put![1]?.headers as Record<string, string>)['Content-Type']).toBe('image/png')
    })
    // PUT succeeded → the follow-up probe (HEAD 200) repainted the holders.
    await vi.waitFor(() => {
      const minis = Array.from(page.root.querySelectorAll('[data-avatar="user"]'))
      for (const el of minis) expect(el.querySelector('img')).toBeTruthy()
    })
    expect(openAvatarCropper).toHaveBeenCalledTimes(1)
    expect(openAvatarCropper).toHaveBeenCalledWith(file)
    expect(input.value).toBe('')
    expect(createImageBitmap).not.toHaveBeenCalled()
  })

  it('upload canceled in the cropper: no PUT ever leaves the page', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, avatarHead: 200 })
    const page = await openPage(mock)
    vi.mocked(openAvatarCropper).mockReset().mockResolvedValue(null)

    const input = page.root.querySelector(
      '[data-avatar-panel] input[type="file"]') as HTMLInputElement
    input.dataset.target = 'user'
    Object.defineProperty(input, 'files', {
      value: [new File([new Uint8Array([9, 9])], 'me.png', { type: 'image/png' })],
      configurable: true,
    })
    input.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(openAvatarCropper).toHaveBeenCalledTimes(1))
    await flushMicrotasks()
    expect(mock.mock.calls.some(([u, init]) =>
      u.startsWith('/api/avatar/') && init?.method === 'PUT')).toBe(false)
  })

  it('upload whose encoding exceeds the cap toasts avatarTooLarge and keeps holders', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, avatarHead: 200 })
    const page = await openPage(mock)
    await refreshAvatar('user')
    vi.mocked(openAvatarCropper).mockReset().mockRejectedValue(new Error(AVATAR_TOO_LARGE))

    const input = page.root.querySelector(
      '[data-avatar-panel] input[type="file"]') as HTMLInputElement
    input.dataset.target = 'user'
    Object.defineProperty(input, 'files', {
      value: [new File([new Uint8Array([9, 9])], 'me.png', { type: 'image/png' })],
      configurable: true,
    })
    input.dispatchEvent(new Event('change'))
    await vi.waitFor(() => {
      const toastEl = page.root.querySelector('[data-toast]') as HTMLElement
      expect(toastEl.classList.contains('toast-show')).toBe(true)
      expect(toastEl.textContent).toContain('Image too large after cropping')
    })
    expect(mock.mock.calls.some(([u, init]) =>
      u.startsWith('/api/avatar/') && init?.method === 'PUT')).toBe(false)
    // Failed upload keeps the custom rendering — no silent fallback.
    const minis = Array.from(page.root.querySelectorAll('[data-avatar="user"]'))
    for (const el of minis) expect(el.querySelector('img')).toBeTruthy()
  })
  it('big avatar click routes the hidden file input at that who', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const input = page.root.querySelector('[data-avatar-panel] input[type="file"]') as HTMLInputElement
    expect(input).toBeTruthy()
    expect(input.accept.split(',').sort()).toEqual(['image/jpeg', 'image/png', 'image/webp'])
    const spy = vi.spyOn(input, 'click')
    const bigAgent = page.root.querySelector('[data-avatar-panel] [data-avatar="agent"]') as HTMLElement
    bigAgent.click()
    expect(spy).toHaveBeenCalledTimes(1)
    expect(input.dataset.target).toBe('agent')
  })

  it('remove failure toasts saveFailed instead of resetting holders', async () => {
    const opts: Parameters<typeof makeFetchHandler>[0] = {
      payload: PAYLOAD,
      avatarHead: 200,
      avatarDeleteStatus: 500,
    }
    const page = await openPage(makeFetchHandler(opts))
    await refreshAvatar('user')
    ;(page.root.querySelector('[data-avatar-remove="user"]') as HTMLElement).click()
    await vi.waitFor(() => {
      const toastEl = page.root.querySelector('[data-toast]') as HTMLElement
      expect(toastEl.classList.contains('toast-show')).toBe(true)
      expect(toastEl.textContent).toContain('Save failed: HTTP 500')
    })
    // Failed DELETE keeps the custom rendering — no silent fallback.
    for (const el of Array.from(page.root.querySelectorAll('[data-avatar="user"]'))) {
      expect(el.querySelector('img')).toBeTruthy()
    }
  })
})


describe('wire helpers', () => {
  it('fetchSettings rejects with the HTTP status on !ok', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 503, json: async () => ({}) })),
    )
    await expect(fetchSettings()).rejects.toThrow('settings fetch failed: 503')
  })

  it('fetchAdminState rejects with the HTTP status on !ok', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 502, json: async () => ({}) })),
    )
    await expect(fetchAdminState()).rejects.toThrow('admin state fetch failed: 502')
  })

  it('postRestart returns the exact 202 envelope without error keys', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({}))
    await expect(postRestart()).resolves.toStrictEqual({ status: 202 })
  })

  it('postRestart maps a non-202 JSON body to status+error+reason', async () => {
    vi.stubGlobal(
      'fetch',
      makeFetchHandler({
        restartPost: { status: 501, body: { error: 'TUI mode cannot restart', reason: 'tui_mode' } },
      }),
    )
    await expect(postRestart()).resolves.toStrictEqual({
      status: 501,
      error: 'TUI mode cannot restart',
      reason: 'tui_mode',
    })
  })

  it('postRestart keeps a 409 empty body as the bare status envelope', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({ restartPost: { status: 409, body: {} } }))
    await expect(postRestart()).resolves.toStrictEqual({ status: 409, error: undefined, reason: undefined })
  })

  it('postRestart survives a non-JSON refusal body', async () => {
    const mock = vi.fn(async () => ({
      ok: false,
      status: 500,
      json: async () => {
        throw new SyntaxError('Unexpected token')
      },
    }))
    vi.stubGlobal('fetch', mock)
    await expect(postRestart()).resolves.toStrictEqual({ status: 500, error: undefined, reason: undefined })
  })

  it('fetchRawSettings rejects with the HTTP status on !ok', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 404, json: async () => ({}) })),
    )
    await expect(fetchRawSettings()).rejects.toThrow('settings fetch failed: 404')
  })

  it('postReloadConfig rejects with the HTTP status on !ok', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 500, json: async () => ({}) })),
    )
    await expect(postReloadConfig()).rejects.toThrow('reload failed: 500')
  })

  it('saveSettings keeps the HTTP status when the failure body is not JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 400,
        json: async () => {
          throw new SyntaxError('no json')
        },
      })),
    )
    await expect(saveSettings(PAYLOAD.providers)).rejects.toThrow('HTTP 400')
  })

  it('saveDefaultModel PUTs the pair and surfaces the server error text', async () => {
    const mock = makeFetchHandler({ onPutDefault: undefined })
    vi.stubGlobal('fetch', mock)
    await saveDefaultModel('zhipu', 'glm-5.3')
    expect(mock).toHaveBeenCalledWith(
      '/api/settings/default',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ provider: 'zhipu', model: 'glm-5.3' }),
      }),
    )
  })

  it('saveDefaultModel rejects with the server error, and with the status on non-JSON', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 400, json: async () => ({ error: 'no such provider' }) })),
    )
    await expect(saveDefaultModel('x', 'y')).rejects.toThrow('no such provider')

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 422,
        json: async () => {
          throw new SyntaxError('no json')
        },
      })),
    )
    await expect(saveDefaultModel('x', 'y')).rejects.toThrow('HTTP 422')
  })

  it('saveSttDefault PUTs the pair and rejects through both error paths', async () => {
    const mock = makeFetchHandler({})
    vi.stubGlobal('fetch', mock)
    await saveSttDefault('audiocpp', 'stt-stream')
    expect(mock).toHaveBeenCalledWith(
      '/api/settings/stt',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ provider: 'audiocpp', model: 'stt-stream' }),
      }),
    )

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 400, json: async () => ({ error: 'stt invalid' }) })),
    )
    await expect(saveSttDefault('a', 'b')).rejects.toThrow('stt invalid')

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 500,
        json: async () => {
          throw new SyntaxError('no json')
        },
      })),
    )
    await expect(saveSttDefault('a', 'b')).rejects.toThrow('HTTP 500')
  })

  it('saveTTSDefault PUTs the triple and rejects through both error paths', async () => {
    const mock = makeFetchHandler({})
    vi.stubGlobal('fetch', mock)
    await saveTTSDefault('audiocpp', 'tts-1', 'Aiden')
    expect(mock).toHaveBeenCalledWith(
      '/api/settings/tts',
      expect.objectContaining({
        method: 'PUT',
        body: JSON.stringify({ provider: 'audiocpp', model: 'tts-1', voice: 'Aiden' }),
      }),
    )

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 400, json: async () => ({ error: 'tts invalid' }) })),
    )
    await expect(saveTTSDefault('a', 'b', 'c')).rejects.toThrow('tts invalid')

    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: false,
        status: 503,
        json: async () => {
          throw new SyntaxError('no json')
        },
      })),
    )
    await expect(saveTTSDefault('a', 'b', 'c')).rejects.toThrow('HTTP 503')
  })

  it('testProvider maps a non-2xx reply to the HTTP error envelope', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 401, json: async () => ({}) })))
    await expect(testProvider(PAYLOAD.providers[0])).resolves.toEqual({ ok: false, error: 'HTTP 401' })
  })

  it('fetchProviderModels maps a non-2xx reply to the error mode', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 502, json: async () => ({}) })))
    await expect(fetchProviderModels('https://x', 'k', 'openai')).resolves.toEqual({
      mode: 'error',
      error: 'HTTP 502',
    })
  })

  it('nativeRestartAvailable follows the hostname allowlist', async () => {
    window.GBotNative = { restartDaemon: () => {} }
    // jsdom runs on localhost — the allowlist admits it when the bridge is up.
    expect(nativeRestartAvailable()).toBe(true)

    vi.stubGlobal('location', { hostname: '127.0.0.1' })
    expect(nativeRestartAvailable()).toBe(true)

    vi.stubGlobal('location', { hostname: '::1' })
    expect(nativeRestartAvailable()).toBe(true)

    vi.stubGlobal('location', { hostname: '100.117.35.55' })
    expect(nativeRestartAvailable()).toBe(false)

    vi.stubGlobal('location', { hostname: 'localhost' })
    delete window.GBotNative
    expect(nativeRestartAvailable()).toBe(false)

    window.GBotNative = { notRestart: () => {} } as unknown as { restartDaemon: () => void }
    expect(nativeRestartAvailable()).toBe(false)
    delete window.GBotNative
  })
})

// ---------------------------------------------------------------------------
// Default-model cards — LLM picker selection, group headers, TTS voices.
// ---------------------------------------------------------------------------

describe('default model cards', () => {
  it('LLM panel marks exactly the default row selected with the full dot and label classes', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-default-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    const sel = page.root.querySelector('[data-default-model="zhipu/glm-5.3"]') as HTMLElement
    expect(sel).toBeTruthy()
    const unselected = page.root.querySelector('[data-default-model="xiaomi/mimo-v2.5"]') as HTMLElement
    expect(unselected).toBeTruthy()
    const selDot = sel.firstElementChild as HTMLElement
    const unsDot = unselected.firstElementChild as HTMLElement
    expect(selDot.className).toBe('w-[14px] h-[14px] rounded-full border flex items-center justify-center border-blue')
    expect(selDot.firstElementChild!.className).toBe('w-[7px] h-[7px] rounded-full bg-blue')
    expect(unsDot.className).toBe('w-[14px] h-[14px] rounded-full border flex items-center justify-center border-t3')
    expect(unsDot.firstElementChild).toBeNull()
    const selLabel = sel.children[1] as HTMLElement
    const unsLabel = unselected.children[1] as HTMLElement
    expect(selLabel.className).toBe('text-xs font-mono text-blue')
    expect(unsLabel.className).toBe('text-xs font-mono')
  })

  it('LLM panel selection is provider+model scoped — a same-id model elsewhere stays unselected', async () => {
    const payload: SettingsPayload = {
      providers: [
        { name: 'a', url: 'https://a', keys: [], models: { 'shared-id': {} } },
        { name: 'b', url: 'https://b', keys: [], models: { 'shared-id': {} } },
      ],
      default: { provider: 'b', model: 'shared-id' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    ;(page.root.querySelector('[data-default-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    const rows = page.root.querySelectorAll('[data-default-model]')
    expect(rows.length).toBe(2)
    const selected = Array.from(rows).filter(
      (r) => (r.firstElementChild as HTMLElement).className.includes('border-blue'),
    )
    expect(selected.length).toBe(1)
    expect(selected[0].getAttribute('data-default-model')).toBe('b/shared-id')
  })

  it('selecting an LLM default PUTs the pair, updates the title, and toasts savedRestart', async () => {
    let putBody: { provider: string; model: string } | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPutDefault: (b) => (putBody = b) }))
    const cardTitle = (page.root.querySelector('[data-default-card]') as HTMLElement).querySelector('.font-mono') as HTMLElement
    ;(page.root.querySelector('[data-default-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    ;(page.root.querySelector('[data-default-model="xiaomi/mimo-v2.5"]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'xiaomi', model: 'mimo-v2.5' }))
    await vi.waitFor(() => expect(cardTitle.textContent).toBe('xiaomi / mimo-v2.5'))
    await vi.waitFor(() => expect(toastText(page)).toBe(t('savedRestart')))
  })

  it('a provider whose every model is task-gated off leaves no group header', async () => {
    const models = {
      mode: 'fetched',
      models: [
        { id: 'stt-stream', task: 'tts' },
        { id: 'tts-voice', task: 'tts' },
      ],
    }
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD, models }), AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    // Tasks land after the open; the still-open panel re-renders with every
    // audiocpp row gated off — no rows and no orphan group header either.
    const panel = page.root.querySelector('[data-stt-panel]') as HTMLElement
    await vi.waitFor(() => {
      expect(panel.children.length).toBe(0)
    })
  })

  it('STT card selection styles exactly the configured stt default', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    const sel = page.root.querySelector('[data-stt-model="audiocpp/stt-stream"]') as HTMLElement
    const uns = page.root.querySelector('[data-stt-model="audiocpp/tts-voice"]') as HTMLElement
    expect(sel).toBeTruthy()
    expect(uns).toBeTruthy()
    expect((sel.firstElementChild as HTMLElement).className).toContain('border-blue')
    expect((sel.firstElementChild as HTMLElement).firstElementChild).toBeTruthy()
    expect((uns.firstElementChild as HTMLElement).className).toContain('border-t3')
    expect((uns.firstElementChild as HTMLElement).firstElementChild).toBeNull()
  })

  it('a failed STT select PUT toasts saveFailed and keeps the old title', async () => {
    const page = await openPage(makeFetchHandler({ payload: { ...AUDIO_PAYLOAD, stt: undefined }, sttPutError: true }), { ...AUDIO_PAYLOAD })
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    ;(page.root.querySelector('[data-stt-model="audiocpp/stt-stream"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('stt write failed')))
  })

  it('a failed TTS select PUT toasts saveFailed', async () => {
    const page = await openPage(makeFetchHandler({ payload: { ...AUDIO_PAYLOAD, tts: undefined }, ttsPutError: true }), { ...AUDIO_PAYLOAD })
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    ;(page.root.querySelector('[data-tts-model="audiocpp/tts-voice"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('tts write failed')))
  })

  it('the TTS voice catalog renders as rows with the configured voice selected', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-tts-voice]').length).toBe(3)
    })
    const def = page.root.querySelector('[data-tts-voice=""]') as HTMLElement
    const aiden = page.root.querySelector('[data-tts-voice="Aiden"]') as HTMLElement
    const serena = page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement
    // Configured voice carries the blue selection; default and others do not.
    expect((aiden.firstElementChild as HTMLElement).className).toContain('border-blue')
    expect((aiden.firstElementChild as HTMLElement).firstElementChild).toBeTruthy()
    expect((def.firstElementChild as HTMLElement).className).toContain('border-t3')
    expect((serena.firstElementChild as HTMLElement).className).toContain('border-t3')
    expect((aiden.children[1] as HTMLElement).className).toBe('text-xs font-mono text-blue')
    expect((serena.children[1] as HTMLElement).className).toBe('text-xs font-mono')
    expect(def.textContent).toBe(t('ttsVoiceDefault'))
    // The voice group is the second non-row header (after the provider group).
    const headers = Array.from((page.root.querySelector('[data-tts-panel]') as HTMLElement).children).filter(
      (el) => !el.hasAttribute('data-tts-voice') && !el.hasAttribute('data-tts-model'),
    )
    expect(headers.map((h) => h.textContent)).toEqual(['audiocpp', t('ttsVoice')])
    expect((headers[1] as HTMLElement).className).toBe(
      'text-[11px] text-t3 font-medium px-3.5 pt-2.5 pb-1 border-t border-hairline',
    )
    expect((headers[0] as HTMLElement).className).toBe('text-[11px] text-t3 font-medium px-3.5 pt-2.5 pb-1')
  })

  it('selecting a voice PUTs the triple with the voice, re-renders, and no-ops on the same voice', async () => {
    let putBody: { provider: string; model: string; voice?: string } | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: { ...AUDIO_PAYLOAD, tts: { provider: 'audiocpp', model: 'tts-voice' } },
        onPutTts: (b) => (putBody = b),
      }),
      { ...AUDIO_PAYLOAD, tts: { provider: 'audiocpp', model: 'tts-voice' } },
    )
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Serena"]')).toBeTruthy())
    ;(page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'audiocpp', model: 'tts-voice', voice: 'Serena' }))
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement).firstElementChild!
        .className).toContain('border-blue')
    })
    putBody = undefined
    ;(page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement).click()
    await flushMicrotasks()
    expect(putBody).toBeUndefined()
  })

  it('voice rows stay inert while no tts default is configured', async () => {
    const mock = makeFetchHandler({ payload: { ...AUDIO_PAYLOAD, tts: undefined } })
    const page = await openPage(mock, { ...AUDIO_PAYLOAD, tts: undefined })
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Serena"]')).toBeTruthy())
    const row = page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement
    expect(row.className).toBe('flex items-center gap-2.5 px-3.5 py-2 cursor-default opacity-55')
    row.click()
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/tts' && i?.method === 'PUT')).toHaveLength(0)
  })

  it('a voices reply without the field renders only the default row', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD, ttsVoicesAbsent: true }), AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await flushMicrotasks()
    await flushMicrotasks()
    expect(page.root.querySelectorAll('[data-tts-voice]').length).toBe(1)
    expect(page.root.querySelector('[data-tts-voice=""]')).toBeTruthy()
  })

  it('the voices catalog is cached per page — reopening does not refetch', async () => {
    const mock = makeFetchHandler({ payload: AUDIO_PAYLOAD })
    const page = await openPage(mock, AUDIO_PAYLOAD)
    const card = page.root.querySelector('[data-tts-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Serena"]')).toBeTruthy())
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true })) // collapse
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true })) // reopen
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u]) => u === '/api/tts/voices')).toHaveLength(1)
  })

  it('a failed voices fetch clears the cache so the next open retries', async () => {
    let fail = true
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/tts/voices') {
        if (fail) return { ok: false, status: 500, json: async () => ({}) }
        return { ok: true, status: 200, json: async () => ({ voices: ['Aiden'] }) }
      }
      return (makeFetchHandler({ payload: AUDIO_PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>, AUDIO_PAYLOAD)
    const card = page.root.querySelector('[data-tts-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.waitFor(() => expect(page.root.querySelectorAll('[data-tts-voice]').length).toBe(1))
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true })) // collapse
    fail = false
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true })) // reopen retries
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Aiden"]')).toBeTruthy())
  })

  it('a stale voices reply never overwrites a newer panel render', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/tts/voices') {
        await gate
        return { ok: true, status: 200, json: async () => ({ voices: ['StaleVoice'] }) }
      }
      return (makeFetchHandler({ payload: AUDIO_PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>, AUDIO_PAYLOAD)
    const card = page.root.querySelector('[data-tts-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushMicrotasks()
    // collapse + reopen → a newer render owns the panel; both renders share
    // the one cached voices promise, so both callbacks fire on resolve —
    // the stale one must drop, leaving exactly one row, not two.
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushMicrotasks()
    release()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(page.root.querySelectorAll('[data-tts-voice="StaleVoice"]')).toHaveLength(1)
  })

  it('audio tasks are probed when an STT card opens, never for the LLM card', async () => {
    const models = { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }] }
    const mock = makeFetchHandler({ payload: AUDIO_PAYLOAD, models })
    const page = await openPage(mock, AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-default-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(0)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
    })
  })

  it('task knowledge rerenders a still-open panel; opening the chevron rotates it', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    const card = page.root.querySelector('[data-stt-card]') as HTMLElement
    const head = card.firstElementChild as HTMLElement
    const chev = head.querySelector('span:last-child') as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(false)
    head.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(chev.classList.contains('rotate-90')).toBe(true)
    head.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    expect(chev.classList.contains('rotate-90')).toBe(false)
  })

  it('an STT-card open never renders the closed tts panel — zero /api/tts/voices fetches', async () => {
    // rerenderIfOpen must skip CLOSED panels: rendering the unseen tts card
    // cache-misses fetchTTSVoices and fires a catalog request nothing asked
    // for. jsdom's classList.contains('') returns false (real browsers throw
    // SyntaxError) — asserted per actual jsdom behavior.
    const mock = makeFetchHandler({ payload: AUDIO_PAYLOAD })
    const page = await openPage(mock, AUDIO_PAYLOAD)
    const ttsPanel = page.root.querySelector('[data-tts-panel]') as HTMLElement
    expect(ttsPanel.classList.contains('hidden')).toBe(true)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    // The audio task probe settling is what drives rerenderIfOpen — wait for
    // it before judging the fetch log.
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
    })
    await flushMicrotasks()
    expect(ttsPanel.classList.contains('hidden')).toBe(true)
    expect(mock.mock.calls.filter(([u]) => u === '/api/tts/voices')).toHaveLength(0)
  })

  it('with zero audio providers a pending voices reply still lands (empty-probe early return)', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/tts/voices') {
        await gate
        return { ok: true, status: 200, json: async () => ({ voices: ['Aiden'] }) }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>, PAYLOAD)
    const card = page.root.querySelector('[data-tts-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushMicrotasks()
    release()
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Aiden"]')).toBeTruthy())
  })
})

// ---------------------------------------------------------------------------
// GENERAL rows — scroll reveals, segment restyles, avatar panel extras.
// ---------------------------------------------------------------------------

describe('general rows', () => {
  const SEL = 'flex-1 py-1.5 text-[10px] rounded-lg cursor-pointer border text-blue border-blue/40 bg-blue/10'
  const UNSEL = 'flex-1 py-1.5 text-[10px] rounded-lg cursor-pointer border bg-ink3 text-t2 border-transparent'

  it('avatar, language and theme rows scroll their panel in on open only', async () => {
    const spy = vi.fn()
    Element.prototype.scrollIntoView = spy
    try {
      const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
      const rows = page.root.querySelectorAll('[data-general-row]')
      for (const idx of [0, 1, 2]) {
        spy.mockClear()
        rows[idx].click()
        expect(spy).toHaveBeenCalledTimes(1)
        expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
        spy.mockClear()
        rows[idx].click() // collapse — no scroll
        expect(spy).not.toHaveBeenCalled()
      }
    } finally {
      Element.prototype.scrollIntoView = vi.fn()
    }
  })

  it('avatar and language rows rotate their chevron with the panel', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    const chevOf = (row: Element) => row.lastElementChild as HTMLElement
    for (const idx of [0, 1]) {
      const chev = chevOf(rows[idx])
      expect(chev.classList.contains('rotate-90')).toBe(false)
      ;(rows[idx] as HTMLElement).click()
      expect(chev.classList.contains('rotate-90')).toBe(true)
      ;(rows[idx] as HTMLElement).click()
      expect(chev.classList.contains('rotate-90')).toBe(false)
    }
  })

  it('avatar remove failure toasts saveFailed with the thrown message', async () => {
    const fallback = makeFetchHandler({ payload: PAYLOAD })
    const failing = vi.fn(async (url: string, init?: RequestInit) => {
      if (url.startsWith('/api/avatar/') && init?.method === 'DELETE') {
        return { ok: false, status: 500, json: async () => ({ error: 'avatar unlink failed' }) }
      }
      return (fallback as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(failing as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-general-row]') as HTMLElement).click()
    ;(page.root.querySelector('[data-avatar-remove="agent"]') as HTMLElement).click()
    // deleteAvatar surfaces the bare status on !ok (avatar.ts contract).
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('HTTP 500')))
  })

  it('an avatar cropper error that is not the size cap toasts the raw message', async () => {
    const { openAvatarCropper } = await import('./avatar_crop')
    vi.mocked(openAvatarCropper).mockRejectedValueOnce(new Error('decoder crashed'))
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-general-row]') as HTMLElement).click()
    const fileInput = page.root.querySelector('input[type="file"]') as HTMLInputElement
    Object.defineProperty(fileInput, 'files', {
      value: [new File([new Uint8Array([1])], 'a.png', { type: 'image/png' })],
      configurable: true,
    })
    fileInput.dataset.target = 'agent'
    fileInput.dispatchEvent(new Event('change'))
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('decoder crashed')))
  })

  it('an avatar change without a target or file is a silent no-op', async () => {
    const { openAvatarCropper } = await import('./avatar_crop')
    vi.mocked(openAvatarCropper).mockClear()
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-general-row]') as HTMLElement).click()
    const fileInput = page.root.querySelector('input[type="file"]') as HTMLInputElement
    // File present but no target, then target set but no file.
    Object.defineProperty(fileInput, 'files', {
      value: [new File([new Uint8Array([1])], 'a.png', { type: 'image/png' })],
      configurable: true,
    })
    fileInput.dispatchEvent(new Event('change'))
    Object.defineProperty(fileInput, 'files', { value: [], configurable: true })
    fileInput.dataset.target = 'agent'
    fileInput.dispatchEvent(new Event('change'))
    await flushMicrotasks()
    expect(openAvatarCropper).not.toHaveBeenCalled()
    expect(toastText(page)).toBe('')
  })

  it('theme select updates the value, anchor and every segment style', async () => {
    localStorage.setItem('gbot-theme', 'system')
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    ;(rows[2] as HTMLElement).click()
    const seg = (page.root.querySelector('[data-language-panel]') as HTMLElement).previousElementSibling as HTMLElement
    void seg
    const themePanel = (rows[2] as HTMLElement).nextElementSibling as HTMLElement
    const light = themePanel.querySelector('[data-theme-opt="light"]') as HTMLElement
    const dark = themePanel.querySelector('[data-theme-opt="dark"]') as HTMLElement
    const system = themePanel.querySelector('[data-theme-opt="system"]') as HTMLElement
    light.click()
    const value = (rows[2] as HTMLElement).children[1] as HTMLElement
    expect(value.textContent).toBe(t('lightTheme'))
    expect(value.getAttribute('data-i18n')).toBe('lightTheme')
    expect(light.className).toBe(SEL)
    expect(dark.className).toBe(UNSEL)
    expect(system.className).toBe(UNSEL)
    // Theme application also lands: the html dataset follows the pref.
    expect(document.documentElement.dataset.theme).toBe('light')
  })

  it('theme chevron rotates with the fold', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const row = page.root.querySelectorAll('[data-general-row]')[2] as HTMLElement
    const chev = row.lastElementChild as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(false)
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(true)
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(false)
  })

  it('hljs chip select restyles every chip, updates the value, and applies the right CSS', async () => {
    localStorage.setItem('gbot-theme', 'light')
    try {
      const { HLJS_THEMES } = await import('./hljs_themes')
      const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
      const rows = page.root.querySelectorAll('[data-general-row]')
      ;(rows[3] as HTMLElement).click()
      const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
      const chips = Array.from(panel.querySelectorAll('[data-hljs-opt]')) as HTMLElement[]
      const atomOne = panel.querySelector('[data-hljs-opt="atom-one"]') as HTMLElement
      atomOne.click()
      const value = (rows[3] as HTMLElement).children[1] as HTMLElement
      expect(value.textContent).toBe('Atom One')
      expect(localStorage.getItem('gbot-hljs-theme')).toBe('atom-one')
      const SELCHIP = 'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate text-blue border-blue/40 bg-blue/10'
      const UNSCHIP = 'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate bg-ink3 text-t2 border-transparent'
      for (const chip of chips) {
        expect(chip.className).toBe(chip === atomOne ? SELCHIP : UNSCHIP)
      }
      // Light pref resolves light — the style element carries the light CSS.
      const theme = HLJS_THEMES.find((x) => x.key === 'atom-one')!
      expect((document.getElementById('hljs-theme-css') as HTMLStyleElement).textContent).toBe(theme.lightCss)
    } finally {
      localStorage.removeItem('gbot-theme')
      localStorage.removeItem('gbot-hljs-theme')
    }
  })

  it('hljs chip select under a dark pref applies the dark CSS', async () => {
    localStorage.setItem('gbot-theme', 'dark')
    try {
      const { HLJS_THEMES } = await import('./hljs_themes')
      const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
      const rows = page.root.querySelectorAll('[data-general-row]')
      ;(rows[3] as HTMLElement).click()
      const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
      ;(panel.querySelector('[data-hljs-opt="github"]') as HTMLElement).click()
      const theme = HLJS_THEMES.find((x) => x.key === 'github')!
      expect((document.getElementById('hljs-theme-css') as HTMLStyleElement).textContent).toBe(theme.darkCss)
    } finally {
      localStorage.removeItem('gbot-theme')
      localStorage.removeItem('gbot-hljs-theme')
    }
  })

  it('hljs chevron rotates with the fold', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const row = page.root.querySelectorAll('[data-general-row]')[3] as HTMLElement
    const chev = row.lastElementChild as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(false)
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(true)
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(false)
  })

  it('a language save failure toasts saveFailed with the storage error', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const rows = page.root.querySelectorAll('[data-general-row]')
    ;(rows[1] as HTMLElement).click()
    const err = Object.defineProperty(new Error('quota full'), 'name', { value: 'QuotaExceededError' })
    vi.spyOn(Storage.prototype, 'setItem').mockImplementationOnce(() => {
      throw err
    })
    ;(page.root.querySelector('[data-lang-opt="zh"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('quota full')))
  })
})

// ---------------------------------------------------------------------------
// SYSTEM card — armed-state bookkeeping, disabled gating, POST statuses.
// ---------------------------------------------------------------------------

describe('SYSTEM card', () => {
  it('not upgradable, no reason, no bridge: disabled with the dim class', async () => {
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: false, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => {
      expect(btn.disabled).toBe(true)
      expect(btn.classList.contains('opacity-40')).toBe(true)
    })
  })

  it('idle + upgradable keeps the button enabled and undimmed', async () => {
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => {
      expect(btn.disabled).toBe(false)
      expect(btn.classList.contains('opacity-40')).toBe(false)
    })
  })

  it('arming drops the i18n anchor and wears the armed class; disarm restores both', async () => {
    vi.useFakeTimers()
    try {
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
        }),
      )
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => expect(btn.disabled).toBe(false))
      btn.click()
      expect(btn.className).toBe(RESTART_ARMED)
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      expect(btn.getAttribute('data-i18n')).toBeNull()
      vi.advanceTimersByTime(5000)
      expect(btn.className).toBe(RESTART_REST)
      expect(btn.textContent).toBe(t('restartBtn'))
      expect(btn.getAttribute('data-i18n')).toBe('restartBtn')
    } finally {
      vi.useRealTimers()
    }
  })

  it('a fresh arm clears the previous window — no early disarm from a stale timer', async () => {
    vi.useFakeTimers()
    try {
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          restartPost: { status: 202, body: { status: 'upgrading' } },
          admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
        }),
      )
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => expect(btn.disabled).toBe(false))
      btn.click() // arm at t=0
      vi.advanceTimersByTime(1000)
      btn.click() // fire → disarm
      await flushMicrotasks()
      btn.click() // re-arm at t≈1000, its own 5s window
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      vi.advanceTimersByTime(3999)
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      // t=5000 is where the FIRST window would fire if it survived the fire.
      vi.advanceTimersByTime(2)
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      vi.advanceTimersByTime(1000)
      expect(btn.textContent).toBe(t('restartBtn'))
    } finally {
      vi.useRealTimers()
    }
  })

  it('a busy turn during the armed window disarms the red label', async () => {
    vi.useFakeTimers()
    try {
      const admin = { busy: false, items: [] as unknown[], upgradable: true, build: '1.2.3 · abc1234' }
      const page = await openPage(makeFetchHandler({ payload: PAYLOAD, admin }))
      const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
      await vi.waitFor(() => expect(btn.disabled).toBe(false))
      btn.click()
      expect(btn.textContent).toBe(t('restartConfirmArm'))
      admin.busy = true
      vi.advanceTimersByTime(5000) // the poll refresh sees the busy turn
      await vi.waitFor(() => {
        expect(btn.disabled).toBe(true)
        expect(btn.textContent).toBe(t('restartBtn'))
        expect(btn.className).toBe(RESTART_REST + ' opacity-40')
        expect(btn.getAttribute('data-i18n')).toBe('restartBtn')
      })
    } finally {
      vi.useRealTimers()
    }
  })

  it('a disabled button ignores clicks entirely — no arming, no POST', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: true, items: [{}], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(true))
    btn.click()
    btn.click()
    await flushMicrotasks()
    expect(btn.textContent).toBe(t('restartBtn'))
    expect(btn.className).toBe(RESTART_REST + ' opacity-40')
    expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(false)
  })

  it('firing disarms first, then arms upgrade mode on 202', async () => {
    const { markUpgrading } = await import('./ws')
    vi.mocked(markUpgrading).mockClear()
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        restartPost: { status: 202, body: { status: 'upgrading' } },
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
    btn.click()
    btn.click()
    await flushMicrotasks()
    expect(markUpgrading).toHaveBeenCalledTimes(1)
    expect(btn.textContent).toBe(t('restartBtn'))
    expect(btn.className).toBe(RESTART_REST)
  })

  it('a non-202/409/501 POST toasts restartFailed and never touches the capsule', async () => {
    const { refuseUpgrade } = await import('./upgrade_mode')
    const { markUpgrading } = await import('./ws')
    vi.mocked(markUpgrading).mockClear()
    vi.mocked(refuseUpgrade).mockClear()
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        restartPost: { status: 500, body: { error: 'boom' } },
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
    btn.click()
    btn.click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('restartFailed')))
    expect(refuseUpgrade).not.toHaveBeenCalled()
    expect(markUpgrading).not.toHaveBeenCalled()
  })

  it('a POST network failure toasts restartFailed', async () => {
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        restartPostReject: true,
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
    btn.click()
    btn.click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('restartFailed')))
  })

  it('a 501 with an empty body routes empty strings into the capsule', async () => {
    const { refuseUpgrade } = await import('./upgrade_mode')
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        restartPost: { status: 501, body: {} },
        admin: { busy: false, items: [], upgradable: true, build: '1.2.3 · abc1234' },
      }),
    )
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
    btn.click()
    btn.click()
    await vi.waitFor(() => expect(refuseUpgrade).toHaveBeenCalledWith('', ''))
  })

  it('build long-press copies through the clipboard API and toasts success', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: true, build: '9.9.9 · deadbee' },
      }),
    )
    const build = page.root.querySelector('[data-system-build]') as HTMLElement
    await vi.waitFor(() => expect(build.textContent).toBe('9.9.9 · deadbee'))
    build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await vi.waitFor(() => expect(toastText(page)).toBe(t('appLogCopied')))
    expect(writeText).toHaveBeenCalledWith('9.9.9 · deadbee')
  })

  it('a clipboard write rejection toasts the copy failure', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('denied'))
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        admin: { busy: false, items: [], upgradable: true, build: '9.9.9 · deadbee' },
      }),
    )
    const build = page.root.querySelector('[data-system-build]') as HTMLElement
    await vi.waitFor(() => expect(build.textContent).toBe('9.9.9 · deadbee'))
    build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
    await vi.waitFor(() => expect(toastText(page)).toBe(t('appLogCopyFailed')))
  })

  it('long-press without the clipboard API copies via a scratch textarea', async () => {
    vi.useFakeTimers()
    try {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
      const execSpy = vi.fn(() => true)
      document.execCommand = execSpy as unknown as typeof document.execCommand
      const selectSpy = vi.spyOn(HTMLTextAreaElement.prototype, 'select').mockReturnValue(undefined)
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          admin: { busy: false, items: [], upgradable: true, build: '7.7.7 · cafebabe' },
        }),
      )
      const build = page.root.querySelector('[data-system-build]') as HTMLElement
      await vi.waitFor(() => expect(build.textContent).toBe('7.7.7 · cafebabe'))
      build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      vi.advanceTimersByTime(500)
      expect(execSpy).toHaveBeenCalledWith('copy')
      expect(toastText(page)).toBe(t('appLogCopied'))
      expect(selectSpy).toHaveBeenCalled()
      expect(document.querySelector('textarea')).toBeNull()
      expect(execSpy).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
      vi.restoreAllMocks()
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('a short tap on the build value cancels the press — nothing is copied', async () => {
    vi.useFakeTimers()
    try {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
      const execSpy = vi.fn(() => true)
      document.execCommand = execSpy as unknown as typeof document.execCommand
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          admin: { busy: false, items: [], upgradable: true, build: '7.7.7 · cafebabe' },
        }),
      )
      const build = page.root.querySelector('[data-system-build]') as HTMLElement
      await vi.waitFor(() => expect(build.textContent).toBe('7.7.7 · cafebabe'))
      build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      build.dispatchEvent(new MouseEvent('pointerup', { bubbles: true }))
      vi.advanceTimersByTime(600)
      expect(execSpy).not.toHaveBeenCalled()
      expect(toastText(page)).toBe('')
    } finally {
      vi.useRealTimers()
      vi.restoreAllMocks()
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })
})

// ---------------------------------------------------------------------------
// Remote endpoints (GBotNative bridge) — prefill, validation, persistence.
// ---------------------------------------------------------------------------

describe('remote endpoints', () => {
  interface NativeStub {
    getRemoteTargets: () => string
    setRemoteTargets: ReturnType<typeof vi.fn>
  }
  const ENDPOINTS = [
    { name: 'desk', host: '192.168.1.5', port: 8765 },
    { name: 'nas', host: 'nas.box.local', port: 9000 },
  ]
  const stubNative = (remotes: unknown[] = [], setImpl?: NativeStub['setRemoteTargets']): NativeStub => {
    const stub: NativeStub = {
      getRemoteTargets: () => JSON.stringify(remotes),
      setRemoteTargets: setImpl ?? vi.fn(),
    }
    vi.stubGlobal('GBotNative', stub)
    return stub
  }
  const card = (page: { root: HTMLElement }): HTMLElement =>
    page.root.querySelector('[data-remote-card]') as HTMLElement
  const entries = (page: { root: HTMLElement }): HTMLElement[] =>
    Array.from(card(page).querySelectorAll('[data-remote-entry]')) as HTMLElement[]
  const formInput = (page: { root: HTMLElement }, sel: string): HTMLInputElement =>
    card(page).querySelector(sel) as HTMLInputElement
  const formVisible = (page: { root: HTMLElement }): boolean =>
    !(card(page).querySelector('[data-remote-form]') as HTMLElement).classList.contains('hidden')

  it('entry cards carry the full class string, chevron glyph, and host:port subtitle', async () => {
    stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const e0 = entries(page)[0]
    expect(e0.className).toBe('mb-2.5 px-3.5 py-3 bg-card border border-hairline rounded-2xl cursor-pointer')
    expect(e0.children[0].className).toBe('flex items-center gap-2')
    expect((e0.children[0].children[0] as HTMLElement).className).toBe('text-sm font-semibold flex-1')
    expect((e0.children[0].children[1] as HTMLElement).className).toBe('text-t3 text-[13px]')
    expect((e0.children[0].children[1] as HTMLElement).textContent).toBe('›')
    expect((e0.children[1] as HTMLElement).className).toBe('text-[11px] text-t3 font-mono mt-0.5 truncate')
    expect((e0.children[1] as HTMLElement).textContent).toBe('192.168.1.5:8765')
  })

  it('opening the FIRST entry prefills index 0 exactly; + opens empty fields', async () => {
    const spy = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    stubNative(ENDPOINTS)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    page.root.scrollTop = 123
    entries(page)[0].click()
    expect(formInput(page, '[data-remote-name]').value).toBe('desk')
    expect(formInput(page, '[data-remote-host]').value).toBe('192.168.1.5')
    expect(formInput(page, '[data-remote-port]').value).toBe('8765')
    // Opening the form resets the page scroll and scrolls the form in.
    expect(page.root.scrollTop).toBe(0)
    expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
    // Delete is offered for an existing entry, hidden for the add flow.
    expect((card(page).querySelector('[data-remote-delete]') as HTMLElement).classList.contains('hidden')).toBe(false)
    ;(card(page).querySelector('[data-remote-back]') as HTMLElement).click()
    expect(page.root.scrollTop).toBe(0)
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    expect(formInput(page, '[data-remote-name]').value).toBe('')
    expect(formInput(page, '[data-remote-host]').value).toBe('')
    expect(formInput(page, '[data-remote-port]').value).toBe('')
    expect((card(page).querySelector('[data-remote-delete]') as HTMLElement).classList.contains('hidden')).toBe(true)
  })

  it('saving through the bridge sends parsed ports and survives a true return', async () => {
    const set = vi.fn(() => true)
    stubNative(ENDPOINTS, set)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'pi')
    input(formInput(page, '[data-remote-host]'), 'pi.local')
    input(formInput(page, '[data-remote-port]'), '9090')
    ;(card(page).querySelector('[data-remote-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
    expect(set).toHaveBeenCalledWith(
      JSON.stringify([...ENDPOINTS, { name: 'pi', host: 'pi.local', port: 9090 }]),
    )
    expect(formVisible(page)).toBe(false)
  })

  it('a bridge rejection skips the saved toast and keeps the form open', async () => {
    const set = vi.fn(() => false)
    stubNative(ENDPOINTS, set)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'pi')
    input(formInput(page, '[data-remote-host]'), 'pi.local')
    input(formInput(page, '[data-remote-port]'), '9090')
    ;(card(page).querySelector('[data-remote-save]') as HTMLElement).click()
    await flushMicrotasks()
    expect(toastText(page)).not.toBe(t('remoteSaved'))
    expect(formVisible(page)).toBe(true)
  })

  it('editing the first entry replaces it in place — no duplicate push', async () => {
    const set = vi.fn(() => true)
    stubNative(ENDPOINTS, set)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    entries(page)[0].click()
    input(formInput(page, '[data-remote-name]'), 'desk2')
    ;(card(page).querySelector('[data-remote-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
    const sent = JSON.parse(set.mock.calls[0][0]) as Array<{ name: string; host: string; port: number }>
    expect(sent).toHaveLength(2)
    expect(sent[0]).toEqual({ name: 'desk2', host: '192.168.1.5', port: 8765 })
    expect(sent[1]).toEqual(ENDPOINTS[1])
  })

  it('deleting the first entry removes exactly it; delete in the add form is inert', async () => {
    const set = vi.fn(() => true)
    stubNative(ENDPOINTS, set)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    entries(page)[0].click()
    ;(card(page).querySelector('[data-remote-delete]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
    const sent = JSON.parse(set.mock.calls[0][0]) as unknown[]
    expect(sent).toEqual([ENDPOINTS[1]])
    expect(entries(page)).toHaveLength(1)
    // Add flow: the hidden delete must not persist anything.
    set.mockClear()
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    ;(card(page).querySelector('[data-remote-delete]') as HTMLElement).click()
    await flushMicrotasks()
    expect(set).not.toHaveBeenCalled()
    expect(toastText(page)).toBe(t('remoteSaved')) // unchanged from the real delete
  })

  it('a bare host with an embedded https:// stays invalid (anchored scheme test)', async () => {
    stubNative([])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(card(page).querySelector('[data-remote-add]') as HTMLElement).click()
    input(formInput(page, '[data-remote-name]'), 'x')
    input(formInput(page, '[data-remote-host]'), 'evil.box/https://inner')
    input(formInput(page, '[data-remote-port]'), '9090')
    ;(card(page).querySelector('[data-remote-save]') as HTMLElement).click()
    await flushMicrotasks()
    expect(toastText(page)).toBe(t('remoteHostInvalid'))
  })

  it('an http:// full origin saves; port 65535 is in range', async () => {
    const set = vi.fn(() => true)
    stubNative(ENDPOINTS, set)
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    entries(page)[0].click()
    input(formInput(page, '[data-remote-host]'), 'http://nas.local:9000/base')
    input(formInput(page, '[data-remote-port]'), '65535')
    ;(card(page).querySelector('[data-remote-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
    const sent = JSON.parse(set.mock.calls[0][0]) as Array<{ host: string; port: number }>
    expect(sent[0].host).toBe('http://nas.local:9000/base')
    expect(sent[0].port).toBe(65535)
  })

  it('a malformed bridge payload renders an empty list; partial entries fall back per field', async () => {
    stubNative([{ host: 'h1', port: 1 }, { name: 'n2' }, { name: 'n3', host: 'h3', port: 0 }])
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const list = entries(page)
    expect(list).toHaveLength(3)
    expect((list[0].children[0].children[0] as HTMLElement).textContent).toBe('')
    expect((list[0].children[1] as HTMLElement).textContent).toBe('h1:1')
    expect((list[1].children[0].children[0] as HTMLElement).textContent).toBe('n2')
    expect((list[1].children[1] as HTMLElement).textContent).toBe(':')
    expect((list[2].children[1] as HTMLElement).textContent).toBe('h3:0')
  })
})

// ---------------------------------------------------------------------------
// Remote desktop devices — prefill, trims, probe, delete guards.
// ---------------------------------------------------------------------------

describe('remote desktop', () => {
  const DEVICES: RemoteDevice[] = [
    { name: 'mac', addr: '192.168.1.9:5900', pass: 'pw1' },
    { name: 'win', addr: '192.168.1.10:5900' },
  ]
  const card = (page: { root: HTMLElement }): HTMLElement =>
    page.root.querySelector('[data-rd-card]') as HTMLElement
  const entries = (page: { root: HTMLElement }): HTMLElement[] =>
    Array.from(card(page).querySelectorAll('[data-rd-entry]')) as HTMLElement[]
  const formInput = (page: { root: HTMLElement }, sel: string): HTMLInputElement =>
    card(page).querySelector(sel) as HTMLInputElement
  const openForm = (page: { root: HTMLElement }, idx: number) => entries(page)[idx].click()

  it('device cards carry the exact classes, chevron, and addr text', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    const e0 = entries(page)[0]
    expect(e0.className).toBe('mb-2.5 px-3.5 py-3 bg-card border border-hairline rounded-2xl cursor-pointer')
    expect((e0.children[0].children[0] as HTMLElement).className).toBe('text-sm font-semibold flex-1')
    expect((e0.children[0].children[1] as HTMLElement).textContent).toBe('›')
    expect((e0.children[1] as HTMLElement).className).toBe('text-[11px] text-t3 font-mono mt-0.5 truncate')
    expect((e0.children[1] as HTMLElement).textContent).toBe('192.168.1.9:5900')
  })

  it('opening device 0 prefills it and resets the probe line; missing pass renders empty', async () => {
    const spy = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    openForm(page, 0)
    expect(formInput(page, '[data-rd-name]').value).toBe('mac')
    expect(formInput(page, '[data-rd-addr]').value).toBe('192.168.1.9:5900')
    expect(formInput(page, '[data-rd-pass]').value).toBe('pw1')
    expect((card(page).querySelector('[data-rd-delete]') as HTMLElement).classList.contains('hidden')).toBe(false)
    expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
    const result = card(page).querySelector('[data-rd-test-result]') as HTMLElement
    expect(result.className).toBe('text-[11px] text-t3')
    openForm(page, 1)
    expect(formInput(page, '[data-rd-pass]').value).toBe('')
    expect((card(page).querySelector('[data-rd-delete]') as HTMLElement).classList.contains('hidden')).toBe(false)
    ;(card(page).querySelector('[data-rd-add]') as HTMLElement).click()
    expect(formInput(page, '[data-rd-name]').value).toBe('')
    expect(formInput(page, '[data-rd-addr]').value).toBe('')
    expect((card(page).querySelector('[data-rd-delete]') as HTMLElement).classList.contains('hidden')).toBe(true)
  })

  it('reopening the form clears a previous probe result', async () => {
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, deviceTest: { ok: true, latencyMs: 44 } }),
    )
    openForm(page, 0)
    ;(card(page).querySelector('[data-rd-test]') as HTMLElement).click()
    await vi.waitFor(() =>
      expect((card(page).querySelector('[data-rd-test-result]') as HTMLElement).textContent).toBe('✓ 44ms'),
    )
    openForm(page, 0)
    const result = card(page).querySelector('[data-rd-test-result]') as HTMLElement
    expect(result.textContent).toBe('')
    expect(result.className).toBe('text-[11px] text-t3')
  })

  it('saving trims name and addr before the PUT', async () => {
    let putBody: RemoteDevice[] | undefined
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, onPutDevices: (b) => (putBody = b) }),
    )
    ;(card(page).querySelector('[data-rd-add]') as HTMLElement).click()
    input(formInput(page, '[data-rd-name]'), '  pi  ')
    input(formInput(page, '[data-rd-addr]'), ' 10.0.0.4:5901 ')
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![2]).toEqual({ name: 'pi', addr: '10.0.0.4:5901', pass: '' })
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
  })

  it('a failed devices PUT toasts saveFailed and keeps the form open', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, devicesPutError: true }))
    openForm(page, 0)
    input(formInput(page, '[data-rd-name]'), 'mac2')
    ;(card(page).querySelector('[data-rd-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('devices write failed')))
    expect((card(page).querySelector('[data-rd-form]') as HTMLElement).classList.contains('hidden')).toBe(false)
  })

  it('deleting device 0 removes exactly it; the add-form delete is inert', async () => {
    let putBody: RemoteDevice[] | undefined
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, onPutDevices: (b) => (putBody = b) }),
    )
    openForm(page, 0)
    ;(card(page).querySelector('[data-rd-delete]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toEqual([DEVICES[1]]))
    await vi.waitFor(() => expect(entries(page)).toHaveLength(1))
    putBody = undefined
    ;(card(page).querySelector('[data-rd-add]') as HTMLElement).click()
    ;(card(page).querySelector('[data-rd-delete]') as HTMLElement).click()
    await flushMicrotasks()
    expect(putBody).toBeUndefined()
  })

  it('the probe sends a trimmed addr and maps both failure shapes', async () => {
    let probeBody: { addr: string; pass: string } | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        devices: DEVICES,
        deviceTest: { ok: false },
        onDeviceTest: (b) => (probeBody = b),
      }),
    )
    openForm(page, 0)
    input(formInput(page, '[data-rd-addr]'), ' 192.168.1.9:5900 ')
    ;(card(page).querySelector('[data-rd-test]') as HTMLElement).click()
    await vi.waitFor(() => {
      const result = card(page).querySelector('[data-rd-test-result]') as HTMLElement
      expect(result.textContent).toBe('✗ failed')
      expect(result.className).toBe('text-[11px] text-red')
    })
    expect(probeBody).toEqual({ addr: '192.168.1.9:5900', pass: 'pw1' })
    expect((card(page).querySelector('[data-rd-test]') as HTMLElement).textContent).toBe(t('testBtn'))
  })

  it('a probe fetch rejection renders the network error in red', async () => {
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: DEVICES, deviceTestReject: true }),
    )
    openForm(page, 0)
    ;(card(page).querySelector('[data-rd-test]') as HTMLElement).click()
    await vi.waitFor(() => {
      const result = card(page).querySelector('[data-rd-test-result]') as HTMLElement
      expect(result.textContent).toBe('✗ fetch failed')
      expect(result.className).toBe('text-[11px] text-red')
    })
  })
})

// ---------------------------------------------------------------------------
// App log card — tabs, stale guards, copy paths, chevron/scroll.
// ---------------------------------------------------------------------------

describe('app log card', () => {
  const LINES = '[17:01:01] gbot: ready\n[17:01:02] Connected'
  const openAppLog = async (tail: () => string) => {
    ;(window as unknown as Record<string, unknown>).GBotAppLogs = { tail, clear: vi.fn() }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    return page
  }
  const body = (page: { root: HTMLElement }) =>
    page.root.querySelector('[data-applog-lines]') as HTMLElement
  const tabs = (page: { root: HTMLElement }) =>
    Array.from((page.root.querySelector('[data-applog-tabs]') as HTMLElement).children) as HTMLElement[]

  it('renders tab buttons and copy affordance with exact classes and labels', async () => {
    const page = await openAppLog(() => LINES)
    const tabRow = page.root.querySelector('[data-applog-tabs]') as HTMLElement
    expect(tabRow.className).toBe('flex items-center gap-2.5')
    const copyBtn = page.root.querySelector('[data-applog-copy]') as HTMLElement
    expect(copyBtn.className).toBe('text-t3 hover:text-t1 cursor-pointer select-none p-1 -m-1')
    expect(copyBtn.getAttribute('aria-label')).toBe(t('appLogCopy'))
    const icon = copyBtn.firstElementChild as HTMLElement
    expect(icon.getAttribute('width')).toBe('14')
    const appTab = tabs(page)[0]
    // The default tab is active — the base class plus the active styling.
    expect(appTab.className).toBe('text-[11px] leading-none cursor-pointer select-none py-1 text-t1 font-bold')
    expect(appTab.textContent).toBe('app')
  })

  it('the app tab renders one whitespace-pre row per line', async () => {
    const page = await openAppLog(() => LINES)
    const rows = Array.from(body(page).children) as HTMLElement[]
    expect(rows.map((r) => r.textContent)).toEqual(['[17:01:01] gbot: ready', '[17:01:02] Connected'])
    for (const r of rows) expect(r.className).toBe('whitespace-pre')
  })

  it('an empty tail renders zero rows, not one blank row', async () => {
    const page = await openAppLog(() => '')
    expect(body(page).children.length).toBe(0)
  })

  it('desktop defaults to the gbot tab — active styling and daemon text', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, logs: 'daemon line 1' }))
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(Array.from(body(page).children).map((c) => c.textContent)).toEqual(['daemon line 1'])
    })
    const [wuiTab, gbotTab] = tabs(page)
    expect(gbotTab.classList.contains('font-bold')).toBe(true)
    expect(gbotTab.classList.contains('text-t1')).toBe(true)
    expect(gbotTab.classList.contains('text-t3')).toBe(false)
    expect(wuiTab.classList.contains('font-bold')).toBe(false)
    expect(wuiTab.classList.contains('text-t3')).toBe(true)
  })

  it('clicking the already-active tab is a no-op — no refetch', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD, logs: 'line' })
    const page = await openPage(mock)
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    await vi.waitFor(() => expect(body(page).children.length).toBe(1))
    const fetches = mock.mock.calls.filter(([u]) => u === '/api/logs?tail=500').length
    ;(tabs(page).find((b) => b.textContent === 'GBot') as HTMLElement).click()
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u]) => u === '/api/logs?tail=500')).toHaveLength(fetches)
  })

  it('a pending gbot fetch renders an empty body first', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/logs?tail=500') {
        await gate
        return { ok: true, status: 200, text: async () => 'late daemon text' }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(body(page).children.length).toBe(0)
    release()
    await vi.waitFor(() => expect(body(page).children.length).toBe(1))
  })

  it('a stale gbot fetch never overwrites a newer tab view', async () => {
    const gates: Array<() => void> = []
    const results: string[] = []
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/logs?tail=500') {
        const idx = results.length
        results.push('')
        await new Promise<void>((r) => gates.push(r))
        return { ok: true, status: 200, text: async () => `daemon payload ${idx}` }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    await flushMicrotasks()
    // gbot fetch pending; switch away to wui.
    ;(tabs(page).find((b) => b.textContent === 'wui') as HTMLElement).click()
    await flushMicrotasks()
    // Now the stale gbot reply lands — the wui view must survive.
    gates[0]()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(body(page).textContent).not.toContain('daemon payload 0')
    // Switch back to gbot: a fresh fetch (still gated) must not be clobbered
    // by the older one either.
    ;(tabs(page).find((b) => b.textContent === 'GBot') as HTMLElement).click()
    await flushMicrotasks()
    gates[1]()
    gates.shift()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(Array.from(body(page).children).map((c) => c.textContent)).toEqual(['daemon payload 1'])
  })

  it('copy ships the ACTIVE tab content via the clipboard API', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined)
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const page = await openAppLog(() => LINES)
    ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('appLogCopied')))
    expect(writeText).toHaveBeenCalledWith(LINES)
  })

  it('a clipboard rejection falls back to execCommand; a plain copy toasts either way', async () => {
    const writeText = vi.fn().mockRejectedValue(new Error('no'))
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true })
    const execSpy = vi.fn(() => true)
    document.execCommand = execSpy as unknown as typeof document.execCommand
    try {
      const page = await openAppLog(() => LINES)
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect(execSpy).toHaveBeenCalledWith('copy')
        expect(toastText(page)).toBe(t('appLogCopied'))
      })
      expect(document.querySelector('textarea')).toBeNull()
    } finally {
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('copy without the clipboard API uses the scratch textarea', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    const execSpy = vi.fn(() => false)
    document.execCommand = execSpy as unknown as typeof document.execCommand
    const selectSpy = vi.spyOn(HTMLTextAreaElement.prototype, 'select').mockReturnValue(undefined)
    try {
      const page = await openAppLog(() => LINES)
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => expect(toastText(page)).toBe(t('appLogCopyFailed')))
      expect(execSpy).toHaveBeenCalledWith('copy')
      expect(selectSpy).toHaveBeenCalled()
      expect(document.querySelector('textarea')).toBeNull()
    } finally {
      selectSpy.mockRestore()
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('the chevron rotates and the panel scrolls into view on open', async () => {
    const spy = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const row = page.root.querySelector('[data-applog-row]') as HTMLElement
    const chev = row.lastElementChild as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(false)
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(true)
    expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
    spy.mockClear()
    row.click()
    expect(chev.classList.contains('rotate-90')).toBe(false)
    expect(spy).not.toHaveBeenCalled()
  })
})

// ---------------------------------------------------------------------------
// Config reload card — phase dots, badges, notes, error paths, pulse.
// ---------------------------------------------------------------------------

describe('config reload card', () => {
  const dotOf = (page: { root: HTMLElement }, i: number) =>
    (page.root.querySelector(`[data-config-phase="${i}"]`) as HTMLElement).firstElementChild as HTMLElement
  const BASE_DOT = 'w-[17px] h-[17px] rounded-full border grid place-items-center text-[10px] flex-none'

  const abortReport = (): ReloadReport => ({
    applied: false,
    phase: 1,
    err: 'plugins: invalid mcp.json',
    files: [{ path: '/x/broken.json', status: 'failed', errLine: 'bad json' }],
    counts: { reloaded: 0, unchanged: 0, failed: 1 },
    promptChanged: false,
    enginesRefreshed: 0,
    settingsChanged: false,
    lastReloadAt: '2026-11-03T00:00:00Z',
  })

  it('an applied report marks all five phases done with exact classes', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: makeReloadReport('mutate') }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerOk')(1, 1, 0),
      )
    })
    for (let i = 0; i < 5; i++) {
      expect(dotOf(page, i).className).toBe(BASE_DOT + ' border-green bg-green text-ink font-bold')
      expect(dotOf(page, i).textContent).toBe('✓')
      const row = page.root.querySelector(`[data-config-phase="${i}"]`) as HTMLElement
      expect(row.classList.contains('hidden')).toBe(false)
      expect(row.classList.contains('text-t1')).toBe(true)
    }
  })

  it('an aborted report splits done/fail/hidden phases at the reported boundary', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: abortReport() }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerErr')(1),
      )
    })
    expect(dotOf(page, 0).className).toBe(BASE_DOT + ' border-green bg-green text-ink font-bold')
    expect(dotOf(page, 0).textContent).toBe('✓')
    expect(dotOf(page, 1).className).toBe(BASE_DOT + ' border-red bg-red text-white font-bold')
    expect(dotOf(page, 1).textContent).toBe('✕')
    for (const i of [2, 3, 4]) {
      const row = page.root.querySelector(`[data-config-phase="${i}"]`) as HTMLElement
      expect(row.classList.contains('hidden')).toBe(true)
    }
  })

  it('a not-applied report without a busy err is the plain error banner', async () => {
    const report = { ...abortReport(), err: undefined, files: [] }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    const banner = page.root.querySelector('[data-config-banner]') as HTMLElement
    await vi.waitFor(() => expect(banner.textContent).toBe(t('configBannerErr')(1)))
    expect(banner.className).toBe('flex-1 min-w-0 text-xs leading-relaxed text-red')
    // No files: the banner row is not even clickable.
    const bannerRow = page.root.querySelector('[data-config-banner-row]') as HTMLElement
    expect(bannerRow.classList.contains('cursor-pointer')).toBe(false)
  })

  it('banner classes are exact for the ok and busy branches', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: makeReloadReport('mutate') }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    const banner = page.root.querySelector('[data-config-banner]') as HTMLElement
    await vi.waitFor(() => expect(banner.textContent).toBe(t('configBannerOk')(1, 1, 0)))
    expect(banner.className).toBe('flex-1 min-w-0 text-xs leading-relaxed text-green')

    const busy: ReloadReport = {
      ...abortReport(),
      err: 'reload already in progress',
      files: [],
    }
    const page2 = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: busy }))
    ;(page2.root.querySelector('[data-config-reload]') as HTMLElement).click()
    const banner2 = page2.root.querySelector('[data-config-banner]') as HTMLElement
    await vi.waitFor(() => expect(banner2.textContent).toBe(t('configBannerBusy')))
    expect(banner2.className).toBe('flex-1 min-w-0 text-xs leading-relaxed text-t2')
  })

  it('badges and rows render exact classes, paths, and note joins for all three statuses', async () => {
    const report: ReloadReport = {
      ...makeReloadReport('mutate'),
      files: [
        { path: '/a.json', status: 'reloaded', note: 'meta only', noteKey: 'effectiveNextRequest' },
        { path: '/b.json', status: 'unchanged', note: 'same bytes' },
        { path: '/c.json', status: 'failed', noteKey: 'newSessionsOnly', errLine: 'line 9: boom' },
      ],
      counts: { reloaded: 1, unchanged: 1, failed: 1 },
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-config-file]').length).toBe(3)
    })
    const rows = Array.from(page.root.querySelectorAll('[data-config-file]')) as HTMLElement[]
    expect(rows.map((r) => r.getAttribute('data-config-file-status'))).toEqual([
      'reloaded',
      'unchanged',
      'failed',
    ])
    for (const r of rows) {
      expect(r.className).toBe('flex items-start gap-2 py-2 border-b border-hairline last:border-b-0')
      expect((r.children[1] as HTMLElement).className).toBe('flex-1 min-w-0')
      const path = (r.children[1] as HTMLElement).firstElementChild as HTMLElement
      expect(path.className).toBe('font-mono text-[11.5px] text-t1 break-all')
    }
    expect((rows[0].children[1].children[1] as HTMLElement).textContent).toBe(
      `meta only · ${t('configNoteNextRequest')}`,
    )
    expect((rows[1].children[1].children[1] as HTMLElement).textContent).toBe('same bytes')
    expect((rows[2].children[1].children[1] as HTMLElement).textContent).toBe(t('configNoteNewSessions'))
    const errLine = rows[2].children[1].children[2] as HTMLElement
    expect(errLine.className).toBe(
      'bg-red/10 text-red font-mono text-[11px] rounded px-1.5 py-0.5 inline-block mt-1',
    )
    expect(errLine.textContent).toBe('line 9: boom')
    const badges = rows.map((r) => r.querySelector('[data-config-badge]') as HTMLElement)
    for (const b of badges) expect(b.getAttribute('data-config-badge')).toBe('')
    expect(badges.map((b) => b.textContent)).toEqual([
      t('configBadgeReloaded'),
      t('configBadgeUnchanged'),
      t('configBadgeFailed'),
    ])
    expect(badges[0].className).toBe('flex-none text-[10.5px] font-semibold rounded px-1.5 py-0.5 mt-0.5 bg-blue/10 text-blue')
    expect(badges[1].className).toBe('flex-none text-[10.5px] font-semibold rounded px-1.5 py-0.5 mt-0.5 bg-ink3 text-t3')
    expect(badges[2].className).toBe('flex-none text-[10.5px] font-semibold rounded px-1.5 py-0.5 mt-0.5 bg-red/10 text-red')
  })

  it('a failed reload keeps rows expanded; a second reload replaces them wholesale', async () => {
    let calls = 0
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/reload' && init?.method === 'POST') {
        calls++
        return { ok: true, status: 200, json: async () => abortReport() }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const reportEl = page.root.querySelector('[data-config-report]') as HTMLElement
    const rows = reportEl.lastElementChild as HTMLElement
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerErr')(1),
      )
    })
    expect(rows.classList.contains('hidden')).toBe(false)
    // Second reload: rows reset, not appended.
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => expect(calls).toBe(2))
    await vi.waitFor(() => {
      expect(page.root.querySelectorAll('[data-config-file]').length).toBe(1)
    })
  })

  it('the button is single-flight: a second click while hanging sends no second POST', async () => {
    let resolveFetch!: () => void
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/reload' && init?.method === 'POST') {
        await new Promise<void>((r) => (resolveFetch = r))
        return { ok: true, status: 200, json: async () => makeReloadReport('mutate') }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
    btn.click()
    await flushMicrotasks()
    btn.click()
    await flushMicrotasks()
    const posts = mock.mock.calls.filter(([u, i]) => u === '/api/settings/reload' && i?.method === 'POST')
    expect(posts).toHaveLength(1)
    resolveFetch()
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
  })

  it('the phase pulse advances one dot at a time and resets before a new reload', async () => {
    vi.useFakeTimers()
    try {
      let resolveFetch!: () => void
      const mock = vi.fn(async (url: string, init?: RequestInit) => {
        if (url === '/api/settings/reload' && init?.method === 'POST') {
          await new Promise<void>((r) => (resolveFetch = r))
          return { ok: true, status: 200, json: async () => makeReloadReport('mutate') }
        }
        return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
      })
      const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
      const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
      const activeDots = () =>
        Array.from(page.root.querySelectorAll('[data-config-phase]'))
          .map((r) => r.firstElementChild as HTMLElement)
          .filter((d) => d.className.includes('border-blue'))
      btn.click()
      await flushMicrotasks()
      expect(activeDots().length).toBe(1)
      expect(dotOf(page, 0).className).toBe(BASE_DOT + ' border-blue')
      expect((dotOf(page, 0).firstElementChild as HTMLElement).className).toBe(
        'w-[7px] h-[7px] rounded-full bg-blue animate-pulse',
      )
      vi.advanceTimersByTime(700)
      expect(dotOf(page, 0).className).toBe(BASE_DOT + ' border-hairline')
      expect(dotOf(page, 1).className).toBe(BASE_DOT + ' border-blue')
      vi.advanceTimersByTime(700 * 10)
      expect(activeDots().length).toBe(1)
      expect(dotOf(page, 4).className).toBe(BASE_DOT + ' border-blue')
      resolveFetch()
      await flushMicrotasks()
      await vi.waitFor(() => expect(dotOf(page, 4).textContent).toBe('✓'))
      // The interval is dead after completion — no dot returns to active.
      vi.advanceTimersByTime(3000)
      expect(activeDots().length).toBe(0)
      // A second reload resets the done marks before the pulse restarts —
      // its response stays gated so the reset is observable.
      btn.click()
      await flushMicrotasks()
      expect(activeDots().length).toBe(1)
      expect(dotOf(page, 0).className).toBe(BASE_DOT + ' border-blue')
      for (const i of [1, 2, 3, 4]) {
        expect(dotOf(page, i).textContent).not.toBe('✓')
      }
      resolveFetch()
      await vi.waitFor(() => expect(dotOf(page, 4).textContent).toBe('✓'))
    } finally {
      vi.useRealTimers()
    }
  })

  it('a reload POST failure hides the phases and shows the error banner', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadError: 'daemon gone' }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    const banner = page.root.querySelector('[data-config-banner]') as HTMLElement
    await vi.waitFor(() => expect(banner.textContent).toBe('reload failed: 500'))
    expect(banner.className).toBe('flex-1 min-w-0 text-xs leading-relaxed text-red')
    expect((page.root.querySelector('[data-config-phases]') as HTMLElement).classList.contains('hidden')).toBe(true)
    expect((page.root.querySelector('[data-config-report]') as HTMLElement).classList.contains('hidden')).toBe(false)
    expect(page.root.querySelectorAll('[data-config-file]').length).toBe(0)
    const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
    expect(btn.disabled).toBe(false)
    expect(btn.textContent).toBe(t('configReloadBtn'))
  })
})

// ---------------------------------------------------------------------------
// JSON sheet — highlight chain, stale guards, placeholder for whitespace.
// ---------------------------------------------------------------------------

describe('JSON sheet', () => {
  const openSheet = async (raw: string) => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, raw }))
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('pre') as HTMLElement).textContent).not.toBe('…')
    })
    return page
  }

  it('highlights keys, string values, and numbers with exact HTML', async () => {
    const page = await openSheet('{"a": "b", "n": 42, "u": "&<"}')
    const pre = page.root.querySelector('pre') as HTMLElement
    expect(pre.innerHTML).toBe(
      '{<span class="text-[#7fd4ff]">"a"</span>: <span class="text-[#a8d8b9]">"b"</span>, ' +
        '<span class="text-[#7fd4ff]">"n"</span>: <span class="text-[#e8b98a]">42</span>, ' +
        '<span class="text-[#7fd4ff]">"u"</span>: <span class="text-[#a8d8b9]">"&amp;&lt;"</span>}',
    )
  })

  it('a whitespace-only file shows the empty placeholder', async () => {
    const page = await openSheet('   ')
    const pre = page.root.querySelector('pre') as HTMLElement
    expect(pre.textContent).toBe(t('jsonSheetEmpty'))
  })

  it('a fetch failure shows the error placeholder and survives a later good open', async () => {
    let fail = true
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/raw') {
        if (fail) return { ok: false, status: 500, json: async () => ({}) }
        return { ok: true, status: 200, json: async () => ({ raw: '{"ok": 1}' }) }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('pre') as HTMLElement).textContent).toBe(t('jsonSheetError'))
    })
    fail = false
    ;(page.root.querySelector('[data-sheet-close]') as HTMLElement).click()
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await vi.waitFor(() => {
      expect((page.root.querySelector('pre') as HTMLElement).textContent).toContain('"ok"')
    })
  })

  it('a slow first open cannot overwrite a fast second open', async () => {
    const gates: Array<() => void> = []
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/raw') {
        const idx = gates.length
        await new Promise<void>((r) => gates.push(r))
        return { ok: true, status: 200, json: async () => ({ raw: `{"payload": ${idx}}` }) }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await flushMicrotasks()
    ;(page.root.querySelector('[data-sheet-close]') as HTMLElement).click()
    ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
    await flushMicrotasks()
    // Second fetch resolves first; the stale first reply lands afterwards.
    gates[1]()
    await flushMicrotasks()
    await flushMicrotasks()
    expect((page.root.querySelector('pre') as HTMLElement).textContent).toContain('"payload": 1')
    gates[0]()
    await flushMicrotasks()
    await flushMicrotasks()
    expect((page.root.querySelector('pre') as HTMLElement).textContent).toContain('"payload": 1')
  })
})

// ---------------------------------------------------------------------------
// Home rendering — provider cards, default titles, screen swapping.
// ---------------------------------------------------------------------------

describe('home rendering', () => {
  it('provider cards wear the exact structural classes and badge families', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const cards = Array.from(page.root.querySelectorAll('[data-provider-card]')) as HTMLElement[]
    const c0 = cards[0]
    expect(c0.className).toBe('mx-0 mb-2.5 px-3.5 py-3 bg-card border border-hairline rounded-2xl cursor-pointer')
    expect(c0.getAttribute('data-provider-card')).toBe('')
    expect((c0.children[0] as HTMLElement).className).toBe('flex items-center gap-2')
    expect((c0.children[0].children[0] as HTMLElement).className).toBe('text-sm font-semibold flex-1')
    expect((c0.children[0].children[0] as HTMLElement).textContent).toBe('xiaomi')
    expect((c0.children[0].children[1] as HTMLElement).className).toBe('text-t3 text-[13px]')
    expect((c0.children[0].children[1] as HTMLElement).textContent).toBe('›')
    expect((c0.children[1] as HTMLElement).className).toBe('text-[11px] text-t3 font-mono mt-0.5 truncate')
    expect((c0.children[1] as HTMLElement).textContent).toBe('https://api.xiaomivivo.com/anthropic')
    expect((c0.children[2] as HTMLElement).className).toBe('flex items-center gap-2 mt-2')
    const badge0 = c0.children[2].children[0] as HTMLElement
    expect(badge0.className).toBe('text-[10px] font-semibold rounded-md px-1.5 py-0.5 text-t2 bg-t2/10')
    expect(badge0.textContent).toBe('AUTO')
    expect(badge0.getAttribute('data-type-badge')).toBe('')
    const badge1 = (cards[1].children[2].children[0] as HTMLElement)
    expect(badge1.className).toBe('text-[10px] font-semibold rounded-md px-1.5 py-0.5 text-[#7fd4ff] bg-blue/10')
    expect(badge1.textContent).toBe('OPENAI')
    const count0 = c0.children[2].children[1] as HTMLElement
    expect(count0.className).toBe('text-[11px] text-t2 flex-1')
    expect(count0.getAttribute('data-i18n')).toBe('modelsCount')
    expect(count0.getAttribute('data-i18n-arg')).toBe('1')
    expect(count0.textContent).toBe(t('modelsCount')(1))
  })

  it('configured stt/tts cards show their pair titles without the grey', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    const sttCard = page.root.querySelector('[data-stt-card]') as HTMLElement
    const ttsCard = page.root.querySelector('[data-tts-card]') as HTMLElement
    const llmCard = page.root.querySelector('[data-default-card]') as HTMLElement
    await vi.waitFor(() => {
      expect((llmCard.querySelector('.font-mono') as HTMLElement).textContent).toBe('zhipu / glm-5.3')
    })
    expect((sttCard.querySelector('.font-mono') as HTMLElement).textContent).toBe('audiocpp / stt-stream')
    expect((ttsCard.querySelector('.font-mono') as HTMLElement).textContent).toBe('audiocpp / tts-voice')
    expect(sttCard.classList.contains('opacity-55')).toBe(false)
    expect(ttsCard.classList.contains('opacity-55')).toBe(false)
  })

  it('screens swap hidden flags with the direction of travel', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const home = page.root.querySelector('[data-screen="home"]') as HTMLElement
    const edit = page.root.querySelector('[data-screen="edit"]') as HTMLElement
    expect(home.classList.contains('hidden')).toBe(false)
    expect(edit.classList.contains('hidden')).toBe(true)
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    expect(home.classList.contains('hidden')).toBe(true)
    expect(edit.classList.contains('hidden')).toBe(false)
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    expect(home.classList.contains('hidden')).toBe(false)
    expect(edit.classList.contains('hidden')).toBe(true)
  })

  it('+ Add provider opens the edit screen with the ADD title', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    const title = page.root.querySelector('[data-title]') as HTMLElement
    expect(title.textContent).toBe(t('addProviderTitle'))
    expect(title.getAttribute('data-i18n')).toBe('addProviderTitle')
    // Editing an existing provider shows the EDIT title instead.
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    ;(page.root.querySelector('[data-provider-card]') as HTMLElement).click()
    expect(title.textContent).toBe(t('editProviderTitle'))
    expect(title.getAttribute('data-i18n')).toBe('editProviderTitle')
  })
})

// ---------------------------------------------------------------------------
// Edit screen — proto tiles, loadForm prefill, collectFormProvider.
// ---------------------------------------------------------------------------

describe('edit screen', () => {
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }
  const field = (page: { root: HTMLElement }, name: string) =>
    page.root.querySelector(`[data-field="${name}"]`) as HTMLInputElement
  const clickSave = (page: { root: HTMLElement }) =>
    (page.root.querySelector('[data-save]') as HTMLElement).click()
  const modelRows = (page: { root: HTMLElement }) =>
    Array.from(page.root.querySelectorAll('[data-model-row]')) as HTMLElement[]
  // classList toggles reorder tokens, so tint is asserted as CONTENT.
  const expectTinted = (t: HTMLElement, sel: boolean) => {
    expect(t.classList.contains('border-blue/50')).toBe(sel)
    expect(t.classList.contains('bg-blue/10')).toBe(sel)
    expect(t.classList.contains('border-hairline')).toBe(!sel)
    expect(t.classList.contains('bg-ink2')).toBe(!sel)
  }

  it('selecting a protocol tile tints exactly that tile and updates the saved type', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 1)
    const tiles = Array.from(page.root.querySelectorAll('[data-proto]')) as HTMLElement[]
    ;(page.root.querySelector('[data-proto="anthropic"]') as HTMLElement).click()
    for (const t of tiles) {
      expectTinted(t, t.getAttribute('data-proto') === 'anthropic')
    }
    input(field(page, 'name'), 'zhipu')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].type).toBe('anthropic')
  })

  it('editing a typed provider pre-tints its tile; a typeless provider tints none', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    await openEdit(page, 1) // zhipu: openai
    expectTinted(page.root.querySelector('[data-proto="openai"]') as HTMLElement, true)
    expectTinted(page.root.querySelector('[data-proto="anthropic"]') as HTMLElement, false)
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    await openEdit(page, 0) // xiaomi: no type
    for (const t of page.root.querySelectorAll('[data-proto]')) {
      expectTinted(t as HTMLElement, false)
    }
  })

  it('a typeless provider round-trips an empty type through the PUT', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 0) // xiaomi — no type on the wire
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].type).toBe('')
  })

  it('the add flow seeds openai with one empty key row and default model meta', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    await flushMicrotasks()
    expect(field(page, 'name').value).toBe('')
    expect(field(page, 'url').value).toBe('')
    const keyRows = (page.root.querySelector('[data-add-key]') as HTMLElement).previousElementSibling!
      .children
    expect(keyRows.length).toBe(1)
    expect((keyRows[0].children[0] as HTMLInputElement).value).toBe('')
    input(field(page, 'name'), 'newp')
    input(field(page, 'url'), 'https://newp')
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    const nameInp = page.root.querySelector('[data-model-name]') as HTMLInputElement
    input(nameInp, 'm1')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    const added = putBody![2]
    expect(added.type).toBe('openai')
    expect(added.keys).toEqual([])
    expect(added.models['m1']).toEqual({
      context: '200k',
      max_tokens: '32k',
      input: ['text'],
      thinking: 'auto',
    })
  })

  it('providers with empty or null keys still get exactly one empty key row', async () => {
    const payload: SettingsPayload = {
      providers: [
        { ...PAYLOAD.providers[0], keys: [] },
        { ...PAYLOAD.providers[1], keys: null as unknown as string[], name: 'nokeys' },
      ],
      default: PAYLOAD.default,
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    const keyRowCount = () =>
      (page.root.querySelector('[data-add-key]') as HTMLElement).previousElementSibling!.children.length
    await openEdit(page, 0)
    expect(keyRowCount()).toBe(1)
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    await openEdit(page, 1)
    expect(keyRowCount()).toBe(1)
  })

  it('reopening a form clears the probe line and the delete zone follows isNew', async () => {
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, testResult: { ok: true, latencyMs: 9 } }),
    )
    await openEdit(page, 1)
    ;(page.root.querySelector('[data-test-conn]') as HTMLElement).click()
    await vi.waitFor(() =>
      expect((page.root.querySelector('[data-test-result]') as HTMLElement).textContent).toBe('✓ 9ms'),
    )
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    await openEdit(page, 1)
    const probe = page.root.querySelector('[data-test-result]') as HTMLElement
    expect(probe.textContent).toBe('')
    expect(probe.className).toBe('text-[11px] text-t2')
    const del = page.root.querySelector('[data-delete-provider]') as HTMLElement
    expect(del.textContent).toBe(t('deleteProviderRow'))
    expect(del.className).toBe('text-[11px] text-red/85 text-center pt-2 cursor-pointer')
    expect(del.getAttribute('data-delete-provider')).toBe('')
    ;(page.root.querySelector('[data-back]') as HTMLElement).click()
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    expect(page.root.querySelector('[data-delete-provider]')).toBeNull()
  })

  it('collectFormProvider trims name/url, drops empty keys, and sorts modalities', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'xiaomi',
          url: 'https://api.xiaomi.com',
          keys: ['sk-1', '', 'sk-2'],
          models: {
            'mimo-v2.5': { context: '500k', input: ['video', 'audio', 'text'] },
          },
        },
      ],
      default: { provider: 'xiaomi', model: 'mimo-v2.5' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page, 0)
    input(field(page, 'name'), '  xiaomi2  ')
    input(field(page, 'url'), '  https://api2.xiaomi.com  ')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].name).toBe('xiaomi2')
    expect(putBody![0].url).toBe('https://api2.xiaomi.com')
    expect(putBody![0].keys).toEqual(['sk-1', 'sk-2'])
    expect(putBody![0].models['mimo-v2.5'].input).toEqual(['text', 'audio', 'video'])
  })

  it('a whitespace-only model name is dropped, not saved', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    input(field(page, 'name'), 'p')
    input(field(page, 'url'), 'https://p')
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    const nameInp = page.root.querySelector('[data-model-name]') as HTMLInputElement
    input(nameInp, '   ')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe(t('oneModelRequired'))
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/providers' && i?.method === 'PUT')).toHaveLength(0)
  })

  it('extra params: empty keys are dropped, raw strings survive, all-empty deletes the field', async () => {
    const withParams: SettingsPayload = {
      providers: [{ ...PAYLOAD.providers[1], extra_params: { tool_stream: true, old: 'gone' } }],
      default: { provider: 'zhipu', model: 'glm-5.3' },
    }
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: withParams, onPut: (b) => (putBody = b) }), withParams)
    await openEdit(page, 0)
    const kvInputs = () =>
      Array.from(page.root.querySelectorAll('[data-kv-del]')) as HTMLElement[]
    expect(kvInputs().length).toBe(2)
    // Rewrite: one valid pair with an unparseable value, one blank key row.
    const k0 = page.root.querySelectorAll('[data-kv-del]')[0]!.parentElement!.children[0] as HTMLInputElement
    const v0 = page.root.querySelectorAll('[data-kv-del]')[0]!.parentElement!.children[1] as HTMLInputElement
    input(k0, 'flag')
    input(v0, 'oops-not-json')
    const k1 = page.root.querySelectorAll('[data-kv-del]')[1]!.parentElement!.children[0] as HTMLInputElement
    const v1 = page.root.querySelectorAll('[data-kv-del]')[1]!.parentElement!.children[1] as HTMLInputElement
    input(k1, '')
    input(v1, 'x')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].extra_params).toEqual({ flag: 'oops-not-json' })

    // A second save after deleting every kv row removes extra_params.
    putBody = undefined
    await openEdit(page, 0)
    while (page.root.querySelector('[data-kv-del]')) {
      ;(page.root.querySelector('[data-kv-del]') as HTMLElement).click()
    }
    expect(page.root.querySelectorAll('[data-kv-del]').length).toBe(0)
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].extra_params).toBeUndefined()
  })

  it('key rows: eye reveals and hides, delete keeps one row, add appends, input reaches the PUT', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [{ name: 'p', url: 'https://p', keys: ['k1', 'k2'], models: { m1: {} } }],
      default: { provider: 'p', model: 'm1' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page, 0)
    const keyRowEls = () =>
      Array.from((page.root.querySelector('[data-add-key]') as HTMLElement).previousElementSibling!
        .children) as HTMLElement[]
    expect(keyRowEls().length).toBe(2)
    expect(keyRowEls()[0].className).toBe('flex items-center gap-1.5 mb-1.5')
    const inp0 = keyRowEls()[0].children[0] as HTMLInputElement
    const eye = keyRowEls()[0].children[1] as HTMLElement
    expect(eye.className).toBe('w-[30px] h-9 bg-transparent border-none text-t3 flex items-center justify-center cursor-pointer shrink-0')
    expect(eye.getAttribute('title')).toBe(t('showHideTitle'))
    expect(eye.getAttribute('type')).toBe('button')
    expect(eye.innerHTML).toContain('M1 12s4-7 11-7')
    eye.click()
    expect(inp0.type).toBe('text')
    expect(eye.innerHTML).toContain('M17.94 17.94')
    expect(eye.innerHTML).toContain('x1="1"')
    eye.click()
    expect(inp0.type).toBe('password')
    expect(eye.innerHTML).toContain('M1 12s4-7 11-7')
    // Editing a key reaches the PUT body in position.
    input(inp0, 'k1-edited')
    // Deleting down to zero keeps one empty row.
    const del1 = keyRowEls()[1].children[2] as HTMLElement
    const del0 = keyRowEls()[0].children[2] as HTMLElement
    expect(del1.textContent).toBe('✕')
    expect(del1.className).toBe('w-9 h-9 -mr-1 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none shrink-0 transition-colors')
    del1.click()
    del0.click()
    const after = keyRowEls()
    expect(after.length).toBe(1)
    expect((after[0].children[0] as HTMLInputElement).value).toBe('')
    ;(page.root.querySelector('[data-add-key]') as HTMLElement).click()
    expect(keyRowEls().length).toBe(2)
    // Final PUT: the untouched empty rows are filtered out.
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].keys).toEqual([])
  })

  it('model rows wear exact classes; audio rows drop the chevron and params', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    await openEdit(page, 2) // audiocpp
    const row = modelRows(page)[0]
    expect((row.parentElement as HTMLElement).className).toBe('border-b border-hairline last:border-b-0')
    expect(row.className).toBe('flex items-center gap-2 px-3 py-2 cursor-pointer')
    expect(row.getAttribute('data-model-row')).toBe('')
    // Audio: no disclosure chevron, no chips, no context counter.
    expect(row.querySelector('.leading-none')).toBeNull()
    expect((row.querySelector('[data-model-name]') as HTMLInputElement).value).toBe('stt-stream')
    const del = row.lastElementChild as HTMLElement
    expect(del.textContent).toBe('✕')
    expect(del.className).toBe('w-9 h-9 -mr-2 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none transition-colors')
    expect(del.getAttribute('data-model-del')).toBe('')
    // Clicking an audio row never opens a detail.
    row.click()
    expect((row.nextElementSibling as HTMLElement).classList.contains('hidden')).toBe(true)
  })

  it('non-audio rows carry chevron, sorted chips, and the context counter', async () => {
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://p',
          keys: [],
          models: {
            m1: { context: '1M', input: ['image', 'text'] },
            m2: {},
          },
        },
      ],
      default: { provider: 'p', model: 'm1' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    const rows = modelRows(page)
    const chev = rows[0].firstElementChild as HTMLElement
    expect(chev.className).toBe('text-t3 text-[15px] leading-none transition-transform')
    expect(chev.textContent).toBe('›')
    const mods = rows[0].children[2] as HTMLElement
    expect(mods.className).toBe('flex items-center gap-1')
    const chips = Array.from(mods.children) as HTMLElement[]
    expect(chips.map((c) => c.textContent)).toEqual(['text', 'image'])
    expect(chips[0].className).toBe('text-[9px] text-[#9D5CFF] bg-ink3 rounded px-1.5 py-0.5')
    const ctx = rows[0].children[3] as HTMLElement
    expect(ctx.className).toBe('text-[9px] text-t3 bg-ink3 rounded px-1.5 py-0.5')
    expect(ctx.textContent).toBe('1M')
    // A model without context renders an empty counter, not "undefined".
    const ctx2 = rows[1].children[2] as HTMLElement
    expect(ctx2.textContent).toBe('')
    expect(rows[1].children[1].className).toContain('font-mono')
  })

  it('the model list hides with zero models and shows after an add', async () => {
    const payload: SettingsPayload = {
      providers: [{ name: 'p', url: 'https://p', keys: [], models: {} }],
      default: { provider: '', model: '' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    const list = (page.root.querySelector('[data-add-model]') as HTMLElement).previousElementSibling as HTMLElement
    expect(list.classList.contains('hidden')).toBe(true)
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    expect(list.classList.contains('hidden')).toBe(false)
    expect(modelRows(page).length).toBe(1)
  })

  it('renaming a model re-keys the map preserving position; empty rename drops it on save', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 1) // zhipu: glm-5.3
    const nameInp = modelRows(page)[0].querySelector('[data-model-name]') as HTMLInputElement
    input(nameInp, 'glm-6')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(Object.keys(putBody![1].models)).toEqual(['glm-6'])

    putBody = undefined
    await openEdit(page, 1)
    const inp = modelRows(page)[0].querySelector('[data-model-name]') as HTMLInputElement
    input(inp, '')
    clickSave(page)
    await flushMicrotasks()
    expect(toastText(page)).toBe(t('oneModelRequired'))
    expect(putBody).toBeUndefined()
  })

  it('clearing both model names leaves the second row input empty', async () => {
    // Clearing row A re-keys it to '' — so form.models[''] EXISTS when row B
    // is cleared, and the `next !== ''` conjunct is the only thing keeping
    // the takeover guard from snapping B back to its old name.
    const payload: SettingsPayload = {
      providers: [{ name: 'p', url: 'https://p', keys: [], models: { one: {}, two: {} } }],
      default: { provider: 'p', model: 'one' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    const rows = modelRows(page)
    const oneInp = rows[0].querySelector('[data-model-name]') as HTMLInputElement
    const twoInp = rows[1].querySelector('[data-model-name]') as HTMLInputElement
    input(oneInp, '')
    expect(oneInp.value).toBe('') // premise: the '' map key now exists
    input(twoInp, '')
    expect(twoInp.value).toBe('')
  })

  it('clicking the model name input never toggles the detail row', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    await openEdit(page, 1)
    const row = modelRows(page)[0]
    const detail = row.nextElementSibling as HTMLElement
    expect(detail.classList.contains('hidden')).toBe(true)
    ;(row.querySelector('[data-model-name]') as HTMLInputElement).click()
    expect(detail.classList.contains('hidden')).toBe(true)
    row.click()
    expect(detail.classList.contains('hidden')).toBe(false)
    const chev = row.firstElementChild as HTMLElement
    expect(chev.classList.contains('rotate-90')).toBe(true)
    row.click()
    expect(detail.classList.contains('hidden')).toBe(true)
    expect(chev.classList.contains('rotate-90')).toBe(false)
  })

  it('STT and TTS badges mark exactly the configured defaults on model rows', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    await openEdit(page, 2) // audiocpp
    const sttBadge = page.root.querySelector('[data-stt-badge]') as HTMLElement
    const ttsBadge = page.root.querySelector('[data-tts-badge]') as HTMLElement
    expect(sttBadge.textContent).toBe('STT')
    expect(sttBadge.className).toBe('text-[9px] text-[#7fd4ff] bg-blue/10 rounded-full px-1.5 py-0.5 font-semibold')
    expect(sttBadge.getAttribute('data-stt-badge')).toBe('')
    expect(ttsBadge.textContent).toBe('TTS')
    expect(ttsBadge.getAttribute('data-tts-badge')).toBe('')
    expect(page.root.querySelectorAll('[data-stt-badge]').length).toBe(1)
    expect(page.root.querySelectorAll('[data-tts-badge]').length).toBe(1)
  })

  it('detail rows render label/input pairs for non-audio; edits reach the PUT', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 1)
    const row = modelRows(page)[0]
    row.click()
    const detail = row.nextElementSibling as HTMLElement
    const labels = Array.from(detail.querySelectorAll('.mt-2\\.5 > div:first-child')).map(
      (d) => (d as HTMLElement).textContent,
    )
    expect(labels.slice(0, 2)).toEqual([t('contextLabel'), t('maxTokensLabel')])
    const ctxInp = (detail.querySelector('.mt-2\\.5') as HTMLElement).children[1] as HTMLInputElement
    input(ctxInp, '2M')
    const mtRow = (detail.querySelectorAll('.mt-2\\.5')[1] as HTMLElement)
    input(mtRow.children[1] as HTMLInputElement, '64k')
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].models['glm-5.3']).toMatchObject({ context: '2M', max_tokens: '64k' })
  })

  it('the input segment carries localized labels and the configured selection', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    await openEdit(page, 0) // xiaomi mimo: input text+image
    const row = modelRows(page)[0]
    row.click()
    const detail = row.nextElementSibling as HTMLElement
    const seg = detail.querySelector('.flex.gap-1') as HTMLElement
    expect(seg.className).toBe('flex gap-1 flex-1')
    const btns = Array.from(seg.children) as HTMLElement[]
    expect(btns.map((b) => b.textContent)).toEqual([
      t('textModality'),
      t('imageModality'),
      t('audioModality'),
      t('videoModality'),
    ])
    expect(btns[0].className).toContain('text-blue')
    expect(btns[1].className).toContain('text-blue')
    expect(btns[2].className).not.toContain('text-blue')
    expect(btns[3].className).not.toContain('text-blue')
    expect(btns[0].getAttribute('type')).toBe('button')
  })

  it('audio providers show the no-params note instead of the session fields', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    await openEdit(page, 2)
    const detail = modelRows(page)[0].nextElementSibling as HTMLElement
    expect(detail.classList.contains('hidden')).toBe(true)
    // The note is the FIRST child of the detail; no context/max rows exist.
    const note = detail.firstElementChild as HTMLElement
    expect(note.textContent).toBe(t('sttModelNoParams'))
    expect(note.className).toBe('mt-2.5 text-[11px] text-t3')
    expect(detail.querySelectorAll('input').length).toBe(0)
    expect(detail.querySelectorAll('.flex.gap-1').length).toBe(0)
  })

  it('the thinking segment defaults to auto, restyles on select, and saves the choice', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 1)
    const row = modelRows(page)[0]
    row.click()
    const detail = row.nextElementSibling as HTMLElement
    const tseg = detail.querySelectorAll('.flex.gap-1')[1] as HTMLElement
    expect(tseg.className).toBe('flex gap-1 flex-1')
    const btns = Array.from(tseg.children) as HTMLElement[]
    expect(btns.map((b) => b.textContent)).toEqual(['none', 'auto', 'low', 'medium', 'high', 'max'])
    expect(btns.map((b) => b.className.includes('text-blue'))).toEqual([false, true, false, false, false, false])
    ;(btns[2] as HTMLElement).click()
    for (const [i, b] of btns.entries()) {
      expect(b.className.includes('text-blue')).toBe(i === 2)
    }
    clickSave(page)
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].models['glm-5.3'].thinking).toBe('low')
  })

  it('a saved thinking preset stays the selected segment on reopen', async () => {
    const payload: SettingsPayload = {
      providers: [
        { name: 'p', url: 'https://p', keys: [], models: { m1: { thinking: 'high' } } },
      ],
      default: { provider: 'p', model: 'm1' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    const row = modelRows(page)[0]
    row.click()
    const tseg = (row.nextElementSibling as HTMLElement).querySelectorAll('.flex.gap-1')[1] as HTMLElement
    const btns = Array.from(tseg.children) as HTMLElement[]
    expect(btns.map((b) => b.className.includes('text-blue'))).toEqual([false, false, false, false, true, false])
  })

  it('kv rows render prefilled inputs and deletes remove exactly one row', async () => {
    const withParams: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://p',
          keys: [],
          models: { m1: {} },
          extra_params: { a: 1, b: 'two' },
        },
      ],
      default: { provider: 'p', model: 'm1' },
    }
    const page = await openPage(makeFetchHandler({ payload: withParams }), withParams)
    await openEdit(page, 0)
    const rows = () => Array.from(page.root.querySelectorAll('[data-kv-del]')) as HTMLElement[]
    expect(rows().length).toBe(2)
    const r0 = rows()[0].parentElement as HTMLElement
    expect(r0.className).toBe('flex gap-2 items-center border-b border-hairline last:border-b-0 pl-3 pr-1.5 py-1')
    const kInp = r0.children[0] as HTMLInputElement
    const vInp = r0.children[1] as HTMLInputElement
    expect(kInp.value).toBe('a')
    expect(vInp.value).toBe('1')
    const del = rows()[0]
    expect(del.textContent).toBe('✕')
    expect(del.className).toBe('w-9 h-9 flex items-center justify-center text-[13px] text-red/70 active:text-red cursor-pointer select-none transition-colors')
    expect(del.getAttribute('data-kv-del')).toBe('')
    del.click()
    expect(rows().length).toBe(1)
    expect((rows()[0].parentElement as HTMLElement).children[0] as HTMLInputElement).toHaveProperty('value', 'b')
    ;(page.root.querySelector('[data-add-param]') as HTMLElement).click()
    expect(rows().length).toBe(2)
    // The kv body hides once the last row is gone.
    ;(rows()[0] as HTMLElement).click()
    ;(rows()[0] as HTMLElement).click()
    expect(rows().length).toBe(0)
    const kvBody = (page.root.querySelector('[data-add-param]') as HTMLElement).previousElementSibling as HTMLElement
    expect(kvBody.classList.contains('hidden')).toBe(true)
  })
})

// ---------------------------------------------------------------------------
// Fetch models — free fetch ownership, zero-result guard, toasts.
// ---------------------------------------------------------------------------

describe('fetch models', () => {
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }
  const fetchBtn = (page: { root: HTMLElement }) =>
    page.root.querySelector('[data-fetch-models]') as HTMLElement
  const modelNames = (page: { root: HTMLElement }) =>
    Array.from(page.root.querySelectorAll('[data-model-name]')).map(
      (i) => (i as HTMLInputElement).value,
    )

  it('a free fetch owns its previous set: keeps returned, drops gone, saves free_fetched ids', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'or',
          url: 'https://openrouter.ai/api/v1',
          keys: ['k'],
          free_fetched: ['old-a', 'keep-b'],
          models: {
            'old-a': { context: '111k' },
            'keep-b': {},
            hand: {},
          },
        },
      ],
      default: { provider: 'or', model: 'hand' },
    }
    const page = await openPage(
      makeFetchHandler({
        payload,
        models: { mode: 'fetched', models: [{ id: 'keep-b' }, { id: 'new-c' }] },
        onPut: (b) => (putBody = b),
      }),
      payload,
    )
    await openEdit(page, 0)
    expect(fetchBtn(page).textContent).toBe(t('fetchFreeTop10'))
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('fetchedModels')(2, 1)))
    expect(modelNames(page).sort()).toEqual(['hand', 'keep-b', 'new-c'])
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].free_fetched).toEqual(['keep-b', 'new-c'])
    expect(modelsBodies.at(-1)).toMatchObject({ url: 'https://openrouter.ai/api/v1', free: true })
  })

  it('a zero-result free fetch never wipes the managed set', async () => {
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'or',
          url: 'https://openrouter.ai/api/v1',
          keys: [],
          free_fetched: ['x'],
          models: { x: {} },
        },
      ],
      default: { provider: 'or', model: 'x' },
    }
    const page = await openPage(
      makeFetchHandler({ payload, models: { mode: 'fetched', models: [] } }),
      payload,
    )
    await openEdit(page, 0)
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(fetchBtn(page).textContent).toBe(t('fetchFreeTop10')))
    expect(modelNames(page)).toEqual(['x'])
    expect(toastText(page)).toBe('')
  })

  it('non-free fetches merge new ids, keep hand metadata, and refresh managed ids', async () => {
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://api.zhipu.example/v4',
          keys: ['k'],
          free_fetched: ['managed-1'],
          models: {
            'managed-1': { context: 'from-free' },
            'hand-1': { context: 'hand-meta' },
          },
        },
      ],
      default: { provider: 'p', model: 'hand-1' },
    }
    const page = await openPage(
      makeFetchHandler({
        payload,
        models: {
          mode: 'fetched',
          models: [
            { id: 'managed-1' },
            { id: 'hand-1' },
            { id: 'fresh-2', context: '9k', input: ['image'] },
            { id: '' },
          ],
        },
      }),
      payload,
    )
    await openEdit(page, 0)
    ;(fetchBtn(page) as HTMLElement).click()
    // 4 entries, one blank dropped: toast counts the wire list and adds 1.
    await vi.waitFor(() => expect(toastText(page)).toBe(t('fetchedModels')(4, 1)))
    expect(modelNames(page).sort()).toEqual(['fresh-2', 'hand-1', 'managed-1'])
    // Managed id refreshed in place; hand metadata kept; defaults applied.
    const rows = page.root.querySelectorAll('[data-model-row]')
    void rows
    const fresh = Array.from(page.root.querySelectorAll('[data-model-name]')).find(
      (i) => (i as HTMLInputElement).value === 'fresh-2',
    ) as HTMLInputElement
    ;(fresh.closest('[data-model-row]') as HTMLElement).click()
    const detail = (fresh.closest('[data-model-row]') as HTMLElement).nextElementSibling as HTMLElement
    const ctxInp = (detail.querySelector('.mt-2\\.5') as HTMLElement).children[1] as HTMLInputElement
    expect(ctxInp.value).toBe('9k')
    expect(modelsBodies.at(-1)).toEqual({ url: 'https://api.zhipu.example/v4', key: 'k', type: '' })
  })

  it('fetched LLM defaults fill 1M/32k/text/auto when the entry has no metadata', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        models: { mode: 'fetched', models: [{ id: 'plain' }] },
        onPut: (b) => (putBody = b),
      }),
    )
    await openEdit(page, 1)
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(modelNames(page)).toContain('plain'))
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].models['plain']).toEqual({
      context: '1M',
      max_tokens: '32k',
      input: ['text'],
      thinking: 'auto',
    })
  })

  it('an audio fetch stores empty model meta', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: AUDIO_PAYLOAD,
        models: { mode: 'fetched', models: [{ id: 'new-voice' }] },
        onPut: (b) => (putBody = b),
      }),
      AUDIO_PAYLOAD,
    )
    await openEdit(page, 2)
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(modelNames(page)).toContain('new-voice'))
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![2].models['new-voice']).toEqual({})
  })

  it('a repeat fetch with nothing new toasts allModelsConfigured and restores the label', async () => {
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        models: { mode: 'fetched', models: [{ id: 'glm-5.3' }] },
      }),
    )
    await openEdit(page, 1)
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('allModelsConfigured')))
    expect(fetchBtn(page).textContent).toBe(t('fetchFromApi'))
    expect(fetchBtn(page).getAttribute('data-i18n')).toBe('fetchFromApi')
    expect(modelNames(page)).toEqual(['glm-5.3'])
  })

  it('manual and error modes toast their own copy', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, models: { mode: 'manual' } }))
    await openEdit(page, 1)
    ;(fetchBtn(page) as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('anthropicNoModelList')))

    const page2 = await openPage(
      makeFetchHandler({ payload: PAYLOAD, models: { mode: 'error', error: 'HTTP 502' } }),
    )
    await openEdit(page2, 1)
    ;(fetchBtn(page2) as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page2)).toBe('HTTP 502'))
  })
})

// ---------------------------------------------------------------------------
// Provider test button — pending copy, result classes, failure shapes.
// ---------------------------------------------------------------------------

describe('provider test button', () => {
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }

  it('pings the first model name with an ellipsis button while in flight', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/test') {
        await gate
        return { ok: true, status: 200, json: async () => ({ ok: true, latencyMs: 33 }) }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    await openEdit(page, 1) // zhipu: glm-5.3
    const result = page.root.querySelector('[data-test-result]') as HTMLElement
    const btn = page.root.querySelector('[data-test-conn]') as HTMLElement
    btn.click()
    expect(result.textContent).toBe(t('pinging')('glm-5.3'))
    expect(btn.textContent).toBe('…')
    release()
    await vi.waitFor(() => {
      expect(result.textContent).toBe('✓ 33ms')
      expect(result.className).toBe('text-[11px] text-green')
      expect(btn.textContent).toBe(t('testBtn'))
    })
  })

  it('a form with zero models pings the bare word provider', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/test') {
        await gate
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    ;(page.root.querySelector('[data-add-provider]') as HTMLElement).click()
    const result = page.root.querySelector('[data-test-result]') as HTMLElement
    ;(page.root.querySelector('[data-test-conn]') as HTMLElement).click()
    expect(result.textContent).toBe(t('pinging')('provider'))
    release()
    await vi.waitFor(() => expect(result.textContent).toBe('✓ 0ms'))
  })

  it('a probe failure without latency or error text renders the fallbacks in red', async () => {
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, testResult: { ok: false } }),
    )
    await openEdit(page, 1)
    ;(page.root.querySelector('[data-test-conn]') as HTMLElement).click()
    const result = page.root.querySelector('[data-test-result]') as HTMLElement
    await vi.waitFor(() => {
      expect(result.textContent).toBe('✗ failed')
      expect(result.className).toBe('text-[11px] text-red')
    })
  })

  it('a probe network rejection renders the thrown message in red', async () => {
    const fallback = makeFetchHandler({ payload: PAYLOAD })
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/test') throw new TypeError('connection refused')
      return (fallback as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    await openEdit(page, 1)
    ;(page.root.querySelector('[data-test-conn]') as HTMLElement).click()
    const result = page.root.querySelector('[data-test-result]') as HTMLElement
    await vi.waitFor(() => {
      expect(result.textContent).toBe('✗ connection refused')
      expect(result.className).toBe('text-[11px] text-red')
    })
  })
})

// ---------------------------------------------------------------------------
// Save / delete / open / close.
// ---------------------------------------------------------------------------

describe('save, delete, open, close', () => {
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }
  const field = (page: { root: HTMLElement }, name: string) =>
    page.root.querySelector(`[data-field="${name}"]`) as HTMLInputElement

  it('a server rejection on save toasts and keeps the edit screen open', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, putError: 'name taken' }))
    await openEdit(page, 1)
    input(field(page, 'name'), 'zhipu2')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('name taken')))
    expect((page.root.querySelector('[data-screen="edit"]') as HTMLElement).classList.contains('hidden')).toBe(false)
    // The form state survives for a retry.
    expect(field(page, 'name').value).toBe('zhipu2')
  })

  it('a successful save re-renders home, swaps screens, and clears stale audio tasks', async () => {
    const models = { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }] }
    const live: SettingsPayload = structuredClone(AUDIO_PAYLOAD)
    const mock = makeFetchHandler({
      payload: live,
      models,
      onPut: (b) => {
        live.providers = b
      },
    })
    const page = await openPage(mock, AUDIO_PAYLOAD)
    // Open the STT card once: the task map caches for audiocpp.
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
    })
    await openEdit(page, 2) // audiocpp
    input(field(page, 'name'), 'audiocpp2')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('savedBackedUp')))
    // Home re-rendered with the new name and the edit screen is gone.
    expect(
      (page.root.querySelector('[data-screen="home"]') as HTMLElement).classList.contains('hidden'),
    ).toBe(false)
    await vi.waitFor(() => {
      const cards = Array.from(page.root.querySelectorAll('[data-provider-card]'))
      // A re-render REPLACES the list — no duplicates of the old name.
      expect(cards.length).toBe(3)
      expect(cards.some((c) => c.textContent?.includes('audiocpp2'))).toBe(true)
      expect(cards.some((c) => c.textContent?.includes('audiocpp') && !c.textContent?.includes('audiocpp2'))).toBe(false)
    })
    // Saving cleared the task caches: the next STT open refetches.
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(2)
    })
  })

  it('cancel returns home without saving', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    await openEdit(page, 1)
    input(field(page, 'name'), 'renamed-no-save')
    ;(page.root.querySelector('[data-cancel]') as HTMLElement).click()
    expect(
      (page.root.querySelector('[data-screen="home"]') as HTMLElement).classList.contains('hidden'),
    ).toBe(false)
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/providers' && i?.method === 'PUT')).toHaveLength(0)
  })

  it('whitespace-only name or url is rejected client-side with zero PUTs', async () => {
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    await openEdit(page, 1)
    input(field(page, 'name'), '   ')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    expect(toastText(page)).toBe(t('nameUrlRequired'))
    input(field(page, 'name'), 'zhipu')
    input(field(page, 'url'), '   ')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    expect(toastText(page)).toBe(t('nameUrlRequired'))
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/providers' && i?.method === 'PUT')).toHaveLength(0)
  })

  it('delete confirm distinguishes the default provider, comparing the trimmed name', async () => {
    const confirms: string[] = []
    vi.stubGlobal('confirm', (msg: string) => {
      confirms.push(msg)
      return true
    })
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }),
    )
    // Non-default: plain confirm.
    await openEdit(page, 0)
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()
    expect(confirms).toEqual([t('deleteProviderConfirm')])
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody).toHaveLength(1)

    // Default owner with a padded name field: trim still matches.
    confirms.length = 0
    putBody = undefined
    const payload: SettingsPayload = {
      providers: [
        { name: 'zhipu', url: 'https://z', keys: [], models: { m: {} } },
        { name: 'other', url: 'https://o', keys: [], models: { m: {} } },
      ],
      default: { provider: 'zhipu', model: 'm' },
    }
    const page2 = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page2, 0)
    input(field(page2, 'name'), '  zhipu  ')
    ;(page2.root.querySelector('[data-delete-provider]') as HTMLElement).click()
    expect(confirms).toEqual([t('deleteDefaultConfirm')])
  })

  it('a rejected confirm deletes nothing', async () => {
    vi.stubGlobal('confirm', () => false)
    const mock = makeFetchHandler({ payload: PAYLOAD })
    const page = await openPage(mock)
    await openEdit(page, 0)
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/providers' && i?.method === 'PUT')).toHaveLength(0)
    expect((page.root.querySelector('[data-screen="edit"]') as HTMLElement).classList.contains('hidden')).toBe(false)
  })

  it('open polls system state on a 5s cadence; close stops the poll and the sheet', async () => {
    vi.useFakeTimers()
    try {
      const admin = { busy: false, items: [] as unknown[], upgradable: true, build: 'v1' }
      const mock = makeFetchHandler({ payload: PAYLOAD, admin })
      const page = createSettingsPage()
      document.body.appendChild(page.root)
      vi.stubGlobal('fetch', mock)
      page.open()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('v1')
      })
      const getsBefore = mock.mock.calls.filter(([u, i]) => u === '/api/admin/restart' && !i?.method).length
      admin.build = 'v2'
      vi.advanceTimersByTime(5000)
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('v2')
      })
      // Open the JSON sheet, then close the page: the sheet hides too.
      ;(page.root.querySelector('[data-json-action]') as HTMLElement).click()
      await vi.waitFor(() => {
        expect((page.root.querySelector('[data-json-sheet]') as HTMLElement).style.display).toBe('')
      })
      page.close()
      expect(page.root.style.display).toBe('none')
      expect(page.isOpen()).toBe(false)
      expect((page.root.querySelector('[data-json-sheet]') as HTMLElement).style.display).toBe('none')
      const getsAfterClose = mock.mock.calls.filter(([u, i]) => u === '/api/admin/restart' && !i?.method).length
      admin.build = 'v3'
      vi.advanceTimersByTime(15000)
      expect(mock.mock.calls.filter(([u, i]) => u === '/api/admin/restart' && !i?.method).length).toBe(getsAfterClose)
      expect(getsAfterClose).toBeGreaterThan(getsBefore)
      expect((page.root.querySelector('[data-system-build]') as HTMLElement).textContent).toBe('v2')
    } finally {
      vi.useRealTimers()
    }
  })

  it('a settings fetch failure opens an empty page with the load-failed toast', async () => {
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    vi.stubGlobal('fetch', makeFetchHandler({ providersGetError: true }))
    page.open()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('loadFailed')('settings fetch failed: 503')))
    expect(page.root.querySelectorAll('[data-provider-card]').length).toBe(0)
    expect(page.isOpen()).toBe(true)
    await vi.waitFor(() => {
      expect((page.root.querySelector('[data-screen="home"]') as HTMLElement).classList.contains('hidden')).toBe(false)
    })
  })
})

// ---------------------------------------------------------------------------
// Round two — pinned escapes from the first census re-run.
// ---------------------------------------------------------------------------

describe('mutate round two', () => {
  const field = (page: { root: HTMLElement }, name: string) =>
    page.root.querySelector(`[data-field="${name}"]`) as HTMLInputElement
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }
  const SEL = 'flex-1 py-1.5 text-[10px] rounded-lg cursor-pointer border text-blue border-blue/40 bg-blue/10'
  const UNSEL = 'flex-1 py-1.5 text-[10px] rounded-lg cursor-pointer border bg-ink3 text-t2 border-transparent'

  it('an unknown modality keeps its position but sorts after the known ones', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://p',
          keys: [],
          models: { m1: { input: ['weird', 'text', 'image'] } },
        },
      ],
      default: { provider: 'p', model: 'm1' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page, 0)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].models.m1.input).toEqual(['text', 'image', 'weird'])
  })

  it('a second toast restarts the hide timer', async () => {
    vi.useFakeTimers()
    try {
      const mock = makeFetchHandler({ payload: PAYLOAD, putError: 'one' })
      const page = await openPage(mock)
      const toastEl = page.root.querySelector('[data-toast]') as HTMLElement
      await openEdit(page, 1)
      ;(page.root.querySelector('[data-save]') as HTMLElement).click()
      await flushMicrotasks()
      expect(toastText(page)).toBe(t('saveFailed')('one'))
      vi.advanceTimersByTime(2000)
      // Toast B at t=2000: its own 2.2s window must outlive A's would-be end.
      ;(page.root.querySelector('[data-save]') as HTMLElement).click()
      await flushMicrotasks()
      expect(toastText(page)).toBe(t('saveFailed')('one'))
      expect(toastEl.classList.contains('toast-show')).toBe(true)
      vi.advanceTimersByTime(300)
      expect(toastEl.classList.contains('toast-show')).toBe(true)
      expect(toastEl.classList.contains('opacity-0')).toBe(false)
      vi.advanceTimersByTime(1900)
      expect(toastEl.classList.contains('toast-show')).toBe(false)
      expect(toastEl.classList.contains('opacity-0')).toBe(true)
    } finally {
      vi.useRealTimers()
    }
  })

  it('the audio task probe dedupes in flight and across resolved opens', async () => {
    let release!: () => void
    const gate = new Promise<void>((r) => (release = r))
    const models = { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }] }
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/models') {
        await gate
        return { ok: true, status: 200, json: async () => models }
      }
      const base = makeFetchHandler({ payload: AUDIO_PAYLOAD })
      return (base as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>, AUDIO_PAYLOAD)
    const card = page.root.querySelector('[data-stt-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await vi.waitFor(() =>
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1),
    )
    // Reopen while still in flight: the page-scoped promise is reused.
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
    release()
    await flushMicrotasks()
    await flushMicrotasks()
    // After resolution the cache stands: another open still does not refetch.
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
  })

  it('a null-keys audio provider still gets its task probe; a keyless one sends an empty key', async () => {
    const payload: SettingsPayload = {
      providers: [
        { ...AUDIO_PAYLOAD.providers[2], name: 'nullkeys', keys: null as unknown as string[] },
        { ...AUDIO_PAYLOAD.providers[2], name: 'emptykeys', keys: [] },
      ],
      default: { provider: '', model: '' },
    }
    modelsBodies.length = 0
    const page = await openPage(makeFetchHandler({ payload, models: { mode: 'manual' } }), payload)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => expect(modelsBodies.length).toBe(2))
    expect(modelsBodies[0]).toMatchObject({ key: '', type: 'audio' })
    expect(modelsBodies[1]).toMatchObject({ key: '', type: 'audio' })
  })

  it('default-model rows wear the row class and selection moves after a pick', async () => {
    let putBody: { provider: string; model: string } | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPutDefault: (b) => (putBody = b) }))
    const card = page.root.querySelector('[data-default-card]') as HTMLElement
    card.firstElementChild!.dispatchEvent(new MouseEvent('click', { bubbles: true }))
    const xiaomi = page.root.querySelector('[data-default-model="xiaomi/mimo-v2.5"]') as HTMLElement
    expect(xiaomi.className).toBe('flex items-center gap-2.5 px-3.5 py-2 cursor-pointer')
    xiaomi.click()
    await vi.waitFor(() => expect(putBody).toEqual({ provider: 'xiaomi', model: 'mimo-v2.5' }))
    // The panel re-rendered: selection styling moved to the picked row.
    await vi.waitFor(() => {
      const zhipuAfter = page.root.querySelector('[data-default-model="zhipu/glm-5.3"]') as HTMLElement
      const xiaomiAfter = page.root.querySelector('[data-default-model="xiaomi/mimo-v2.5"]') as HTMLElement
      expect((zhipuAfter.firstElementChild as HTMLElement).className).toContain('border-t3')
      expect((zhipuAfter.firstElementChild as HTMLElement).firstElementChild).toBeNull()
      expect((xiaomiAfter.firstElementChild as HTMLElement).className).toContain('border-blue')
      expect((xiaomiAfter.children[1] as HTMLElement).className).toBe('text-xs font-mono text-blue')
    })
  })

  it('a non-Error rejection on stt or tts selects toasts the bare error word', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD, sttPutReject: 'kaboom' }), AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    ;(page.root.querySelector('[data-stt-model="audiocpp/stt-stream"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('saveFailed')('error')))

    const page2 = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD, ttsPutReject: 'kaboom' }), AUDIO_PAYLOAD)
    ;(page2.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    ;(page2.root.querySelector('[data-tts-model="audiocpp/tts-voice"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page2)).toBe(t('saveFailed')('error')))
    // The VOICE row's own catch site (not the model row's) fires on a
    // rejection during a voice change.
    const page3 = await openPage(
      makeFetchHandler({
        payload: { ...AUDIO_PAYLOAD, tts: { provider: 'audiocpp', model: 'tts-voice' } },
        ttsPutReject: 'kaboom',
      }),
      { ...AUDIO_PAYLOAD, tts: { provider: 'audiocpp', model: 'tts-voice' } },
    )
    ;(page3.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => expect(page3.root.querySelector('[data-tts-voice="Serena"]')).toBeTruthy())
    ;(page3.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page3)).toBe(t('saveFailed')('error')))
  })

  it('a configured tts card offers pointer voice rows with exact dot classes; default selected without a voice', async () => {
    let putBody: { provider: string; model: string; voice?: string } | undefined
    const payload: SettingsPayload = {
      ...AUDIO_PAYLOAD,
      tts: { provider: 'audiocpp', model: 'tts-voice' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPutTts: (b) => (putBody = b) }), payload)
    ;(page.root.querySelector('[data-tts-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => expect(page.root.querySelector('[data-tts-voice="Serena"]')).toBeTruthy())
    const serena = page.root.querySelector('[data-tts-voice="Serena"]') as HTMLElement
    expect(serena.className).toBe('flex items-center gap-2.5 px-3.5 py-2 cursor-pointer')
    const def = page.root.querySelector('[data-tts-voice=""]') as HTMLElement
    // No voice configured: the default preset row is the selected one.
    expect((def.firstElementChild as HTMLElement).className).toBe(
      'w-[14px] h-[14px] rounded-full border flex items-center justify-center border-blue',
    )
    expect((def.firstElementChild as HTMLElement).firstElementChild!.className).toBe(
      'w-[7px] h-[7px] rounded-full bg-blue',
    )
    expect((serena.firstElementChild as HTMLElement).className).toBe(
      'w-[14px] h-[14px] rounded-full border flex items-center justify-center border-t3',
    )
    expect((serena.firstElementChild as HTMLElement).firstElementChild).toBeNull()
    // Clicking the already-selected default while unset is a no-op.
    def.click()
    await flushMicrotasks()
    expect(putBody).toBeUndefined()
  })

  it('both copy paths leave a styled scratch textarea behind only transiently', async () => {
    Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
    const execSpy = vi.fn(() => true)
    document.execCommand = execSpy as unknown as typeof document.execCommand
    const selectSpy = vi.spyOn(HTMLTextAreaElement.prototype, 'select').mockReturnValue(undefined)
    const appendSpy = vi.spyOn(document.body, 'appendChild')
    try {
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          admin: { busy: false, items: [], upgradable: true, build: '5.5.5 · feed' },
        }),
      )
      const build = page.root.querySelector('[data-system-build]') as HTMLElement
      await vi.waitFor(() => expect(build.textContent).toBe('5.5.5 · feed'))
      build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      await vi.waitFor(() => expect(toastText(page)).toBe(t('appLogCopied')))
      const scratch = appendSpy.mock.calls.map((c) => c[0]).find((n) => n.nodeName === 'TEXTAREA') as HTMLTextAreaElement
      expect(scratch.value).toBe('5.5.5 · feed')
      expect(scratch.style.position).toBe('fixed')
      expect(scratch.style.opacity).toBe('0')

      // Same shape for the app-log copy button.
      appendSpy.mockClear()
      ;(window as unknown as Record<string, unknown>).GBotAppLogs = {
        tail: () => 'log line',
        clear: vi.fn(),
      }
      ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
      ;(page.root.querySelector('[data-applog-copy]') as HTMLElement).click()
      await vi.waitFor(() => expect(execSpy).toHaveBeenCalledTimes(2))
      const scratch2 = appendSpy.mock.calls.map((c) => c[0]).find((n) => n.nodeName === 'TEXTAREA') as HTMLTextAreaElement
      expect(scratch2.value).toBe('log line')
      expect(scratch2.style.position).toBe('fixed')
      expect(scratch2.style.opacity).toBe('0')
      expect(document.querySelector('textarea')).toBeNull()
    } finally {
      selectSpy.mockRestore()
      appendSpy.mockRestore()
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('a busy turn while armed disarms through the open()-path refresh', async () => {
    const admin = { busy: false, items: [] as unknown[], upgradable: true, build: '1.2.3 · abc1234' }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, admin }))
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
    btn.click()
    expect(btn.textContent).toBe(t('restartConfirmArm'))
    admin.busy = true
    page.open() // re-entry refreshes system state synchronously
      await vi.waitFor(() => {
        expect(btn.disabled).toBe(true)
        expect(btn.textContent).toBe(t('restartBtn'))
        // Disarm must keep the dim the same refresh just applied — a
        // disabled button is always visibly disabled.
        expect(btn.className).toBe(RESTART_REST + ' opacity-40')
        expect(btn.getAttribute('data-i18n')).toBe('restartBtn')
      })
  })

  it('a synthetic click on a disabled restart button is ignored', async () => {
    const mock = makeFetchHandler({
      payload: PAYLOAD,
      admin: { busy: true, items: [{}], upgradable: true, build: '1.2.3 · abc1234' },
    })
    const page = await openPage(mock)
    const btn = page.root.querySelector('[data-restart-btn]') as HTMLButtonElement
    await vi.waitFor(() => expect(btn.disabled).toBe(true))
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    expect(btn.textContent).toBe(t('restartBtn'))
    expect(mock.mock.calls.some(([u, i]) => u === '/api/admin/restart' && i?.method === 'POST')).toBe(false)
  })

  it('remote entry cards expose the data attribute; port 0 is rejected', async () => {
    const set = vi.fn(() => true)
    vi.stubGlobal('GBotNative', {
      getRemoteTargets: () => JSON.stringify([{ name: 'd', host: 'h', port: 1 }]),
      setRemoteTargets: set,
    })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const entry = page.root.querySelector('[data-remote-entry]') as HTMLElement
    expect(entry.getAttribute('data-remote-entry')).toBe('')
    ;(page.root.querySelector('[data-remote-add]') as HTMLElement).click()
    const cardEl = page.root.querySelector('[data-remote-card]') as HTMLElement
    input(cardEl.querySelector('[data-remote-name]') as HTMLInputElement, 'p0')
    input(cardEl.querySelector('[data-remote-host]') as HTMLInputElement, 'p0.box')
    input(cardEl.querySelector('[data-remote-port]') as HTMLInputElement, '0')
    ;(cardEl.querySelector('[data-remote-save]') as HTMLElement).click()
    await flushMicrotasks()
    expect(toastText(page)).toBe(t('remotePortInvalid'))
    expect(set).not.toHaveBeenCalled()
  })

  it('closing the remote form keeps the page scroll', async () => {
    vi.stubGlobal('GBotNative', {
      getRemoteTargets: () => '[]',
      setRemoteTargets: vi.fn(),
    })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const cardEl = page.root.querySelector('[data-remote-card]') as HTMLElement
    ;(cardEl.querySelector('[data-remote-entry]') as HTMLElement | null)?.click()
    ;(cardEl.querySelector('[data-remote-add]') as HTMLElement).click()
    expect(page.root.scrollTop).toBe(0)
    page.root.scrollTop = 42
    ;(cardEl.querySelector('[data-remote-back]') as HTMLElement).click()
    expect(page.root.scrollTop).toBe(42)
  })

  it('device entry cards expose the attribute and inner row classes', async () => {
    const DEVICES: RemoteDevice[] = [{ name: 'mac', addr: '1.2.3.4:5900', pass: '' }]
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, devices: DEVICES }))
    const e0 = page.root.querySelector('[data-rd-entry]') as HTMLElement
    expect(e0.getAttribute('data-rd-entry')).toBe('')
    expect((e0.children[0] as HTMLElement).className).toBe('flex items-center gap-2')
    expect((e0.children[0].children[1] as HTMLElement).className).toBe('text-t3 text-[13px]')
  })

  it('a non-Error probe rejection renders the network-error word', async () => {
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: [{ name: 'd', addr: 'a', pass: '' }], deviceTestRejectWith: 'boom' }),
    )
    ;(page.root.querySelector('[data-rd-card]') as HTMLElement).querySelectorAll('[data-rd-entry]')[0]!
      .dispatchEvent(new MouseEvent('click', { bubbles: true }))
    ;((page.root.querySelector('[data-rd-card]') as HTMLElement).querySelector('[data-rd-test]') as HTMLElement).click()
    const result = (page.root.querySelector('[data-rd-card]') as HTMLElement).querySelector('[data-rd-test-result]') as HTMLElement
    await vi.waitFor(() => {
      expect(result.textContent).toBe('✗ network error')
      expect(result.className).toBe('text-[11px] text-red')
    })

    const page2 = await openPage(makeFetchHandler({ payload: PAYLOAD, testRejectWith: 'boom' }))
    await openEdit(page2, 1)
    ;(page2.root.querySelector('[data-test-conn]') as HTMLElement).click()
    const result2 = page2.root.querySelector('[data-test-result]') as HTMLElement
    await vi.waitFor(() => {
      expect(result2.textContent).toBe('✗ network error')
      expect(result2.className).toBe('text-[11px] text-red')
    })
  })

  it('the wui tab shows the console buffer one row per entry', async () => {
    const { pushDebugLog } = await import('./log')
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    ;(page.root.querySelector('[data-applog-row]') as HTMLElement).click()
    const tabsRow = page.root.querySelector('[data-applog-tabs]') as HTMLElement
    const wuiTab = Array.from(tabsRow.children).find(
      (b) => (b as HTMLElement).getAttribute('data-applog-tab') === 'wui',
    ) as HTMLElement
    expect(wuiTab.textContent).toBe('wui')
    pushDebugLog('first line')
    pushDebugLog('second line')
    wuiTab.click()
    await vi.waitFor(() => {
      const texts = Array.from(body0(page).children).map((c) => c.textContent ?? '')
      expect(texts.length).toBe(2)
      expect(texts[0].endsWith('first line')).toBe(true)
      expect(texts[1].endsWith('second line')).toBe(true)
    })
    // Structural classes and the anchors.
    const actions = tabsRow.parentElement as HTMLElement
    expect(actions.className).toBe('flex items-center justify-between px-3.5 pt-2 pb-1.5')
    expect(tabsRow.getAttribute('data-applog-tabs')).toBe('')
    expect((page.root.querySelector('[data-applog-copy]') as HTMLElement).getAttribute('data-applog-copy')).toBe('')
    expect(body0(page).className).toBe(
      'max-h-[280px] overflow-y-auto px-3.5 py-2 font-mono text-[10.5px] leading-relaxed text-t2',
    )
    expect(body0(page).getAttribute('data-applog-lines')).toBe('')
  })

  it('a reopened log panel is owned by its newer gbot fetch', async () => {
    const gates: Array<() => void> = []
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/logs?tail=500') {
        const idx = gates.length
        await new Promise<void>((r) => gates.push(r))
        return { ok: true, status: 200, text: async () => `daemon payload ${idx}` }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const row = page.root.querySelector('[data-applog-row]') as HTMLElement
    row.click() // panel 1: fetch0 gated
    await flushMicrotasks()
    row.click() // collapse
    row.click() // panel 2: fetch1 gated, newer seq
    await flushMicrotasks()
    gates[1]()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(Array.from(body0(page).children).map((c) => c.textContent)).toEqual(['daemon payload 1'])
    gates[0]()
    await flushMicrotasks()
    await flushMicrotasks()
    expect(Array.from(body0(page).children).map((c) => c.textContent)).toEqual(['daemon payload 1'])
  })

  it('idle phase rows stay dim while the pulse is elsewhere', async () => {
    vi.useFakeTimers()
    try {
      let resolveFetch!: () => void
      const mock = vi.fn(async (url: string, init?: RequestInit) => {
        if (url === '/api/settings/reload' && init?.method === 'POST') {
          await new Promise<void>((r) => (resolveFetch = r))
          return { ok: true, status: 200, json: async () => makeReloadReport('mutate') }
        }
        return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
      })
      const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
      ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
      await flushMicrotasks()
      vi.advanceTimersByTime(1400)
      const row0 = page.root.querySelector('[data-config-phase="0"]') as HTMLElement
      const row2 = page.root.querySelector('[data-config-phase="2"]') as HTMLElement
      const row3 = page.root.querySelector('[data-config-phase="3"]') as HTMLElement
      expect(row0.classList.contains('text-t1')).toBe(false)
      expect(row2.classList.contains('text-t1')).toBe(true)
      expect(row3.classList.contains('text-t1')).toBe(false)
      resolveFetch()
      await vi.waitFor(() =>
        expect((page.root.querySelector('[data-config-phase="4"]') as HTMLElement).firstElementChild!.textContent).toBe('✓'),
      )
    } finally {
      vi.useRealTimers()
    }
  })

  it('config file rows replace wholesale, carry the attribute, and an error after a report clears them', async () => {
    let calls = 0
    let fail = false
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/reload' && init?.method === 'POST') {
        calls++
        if (fail) return { ok: false, status: 500, text: async () => 'nope' }
        return {
          ok: true,
          status: 200,
          json: async () =>
            calls === 1
              ? makeReloadReport('mutate')
              : { ...makeReloadReport('mutate'), files: [{ path: '/only.json', status: 'reloaded' }] },
        }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const btn = page.root.querySelector('[data-config-reload]') as HTMLElement
    btn.click()
    await vi.waitFor(() => expect(page.root.querySelectorAll('[data-config-file]').length).toBe(2))
    expect((page.root.querySelector('[data-config-file]') as HTMLElement).getAttribute('data-config-file')).toBe('')
    btn.click()
    await vi.waitFor(() => expect(calls).toBe(2))
    await vi.waitFor(() => {
      const rows = page.root.querySelectorAll('[data-config-file]')
      expect(rows.length).toBe(1)
      expect((rows[0] as HTMLElement).textContent).toContain('/only.json')
    })
    // A failed reload afterwards clears the rows and shows the raw error.
    fail = true
    btn.click()
    const banner = page.root.querySelector('[data-config-banner]') as HTMLElement
    await vi.waitFor(() => expect(banner.textContent).toBe('reload failed: 500'))
    expect(page.root.querySelectorAll('[data-config-file]').length).toBe(0)
  })

  it('a no-note file renders only the path; notes carry their exact class', async () => {
    const report: ReloadReport = {
      ...makeReloadReport('mutate'),
      files: [
        { path: '/plain.json', status: 'unchanged' },
        { path: '/noted.json', status: 'reloaded', note: 'note text' },
      ],
      counts: { reloaded: 1, unchanged: 1, failed: 0 },
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() => expect(page.root.querySelectorAll('[data-config-file]').length).toBe(2))
    const rows = Array.from(page.root.querySelectorAll('[data-config-file]')) as HTMLElement[]
    const plainBody = rows[0].children[1] as HTMLElement
    expect(plainBody.children.length).toBe(1)
    const note = (rows[1].children[1] as HTMLElement).children[1] as HTMLElement
    expect(note.className).toBe('text-[11.5px] text-t2 mt-0.5')
    expect(note.textContent).toBe('note text')
  })

  it('an aborted report with zero failures still expands rows; files make the banner clickable', async () => {
    const report: ReloadReport = {
      applied: false,
      phase: 0,
      err: undefined,
      files: [{ path: '/stuck.json', status: 'unchanged' }],
      counts: { reloaded: 0, unchanged: 1, failed: 0 },
      promptChanged: false,
      enginesRefreshed: 0,
      settingsChanged: false,
      lastReloadAt: '2026-11-03T00:00:00Z',
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() =>
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerErr')(0),
      ),
    )
    const rows = (page.root.querySelector('[data-config-report]') as HTMLElement).lastElementChild as HTMLElement
    expect(rows.classList.contains('hidden')).toBe(false)
    const bannerRow = page.root.querySelector('[data-config-banner-row]') as HTMLElement
    expect(bannerRow.classList.contains('cursor-pointer')).toBe(true)
  })

  it('a synthetic double click on the disabled reload button stays single-flight', async () => {
    let resolveFetch!: () => void
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/reload' && init?.method === 'POST') {
        await new Promise<void>((r) => (resolveFetch = r))
        return { ok: true, status: 200, json: async () => makeReloadReport('mutate') }
      }
      return (makeFetchHandler({ payload: PAYLOAD }) as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>)
    const btn = page.root.querySelector('[data-config-reload]') as HTMLButtonElement
    btn.click()
    await flushMicrotasks()
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    btn.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true }))
    await flushMicrotasks()
    expect(mock.mock.calls.filter(([u, i]) => u === '/api/settings/reload' && i?.method === 'POST')).toHaveLength(1)
    resolveFetch()
    await vi.waitFor(() => expect(btn.disabled).toBe(false))
  })

  it('switching to a non-default hljs theme restyles every chip and label', async () => {
    localStorage.setItem('gbot-theme', 'light')
    try {
      const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
      const rows = page.root.querySelectorAll('[data-general-row]')
      ;(rows[3] as HTMLElement).click()
      const panel = page.root.querySelector('[data-hljs-panel]') as HTMLElement
      const github = panel.querySelector('[data-hljs-opt="github"]') as HTMLElement
      const atomOne = panel.querySelector('[data-hljs-opt="atom-one"]') as HTMLElement
      github.click()
      const value = (rows[3] as HTMLElement).children[1] as HTMLElement
      expect(value.textContent).toBe('GitHub')
      expect(localStorage.getItem('gbot-hljs-theme')).toBe('github')
      const SELCHIP = 'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate text-blue border-blue/40 bg-blue/10'
      const UNSCHIP = 'py-1.5 text-[11px] rounded-lg cursor-pointer border truncate bg-ink3 text-t2 border-transparent'
      for (const chip of Array.from(panel.querySelectorAll('[data-hljs-opt]')) as HTMLElement[]) {
        expect(chip.className).toBe(chip === github ? SELCHIP : UNSCHIP)
      }
      expect(atomOne.className).toBe(UNSCHIP)
    } finally {
      localStorage.removeItem('gbot-theme')
      localStorage.removeItem('gbot-hljs-theme')
    }
  })

  it('key edits reach the PUT body; the eye button exposes a real type attribute', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, onPut: (b) => (putBody = b) }))
    await openEdit(page, 1)
    const keyRow = (page.root.querySelector('[data-add-key]') as HTMLElement).previousElementSibling!
      .children[0] as HTMLElement
    const inp = keyRow.children[0] as HTMLInputElement
    const eye = keyRow.children[1] as HTMLElement
    const del = keyRow.children[2] as HTMLElement
    expect(del.getAttribute('type')).toBe('button')
    eye.click()
    expect(inp.getAttribute('type')).toBe('text')
    eye.click()
    expect(inp.getAttribute('type')).toBe('password')
    input(inp, 'zk-rotated')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].keys).toEqual(['zk-rotated'])
  })

  it('renaming one of two models preserves its sibling', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://p',
          keys: [],
          models: { one: {}, two: {} },
        },
      ],
      default: { provider: 'p', model: 'one' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page, 0)
    const rows = page.root.querySelectorAll('[data-model-row]')
    const oneInp = (rows[0] as HTMLElement).querySelector('[data-model-name]') as HTMLInputElement
    input(oneInp, 'one-renamed')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(Object.keys(putBody![0].models).sort()).toEqual(['one-renamed', 'two'])
  })

  it('audio model rows carry no mods container and no context counter', async () => {
    const page = await openPage(makeFetchHandler({ payload: AUDIO_PAYLOAD }), AUDIO_PAYLOAD)
    await openEdit(page, 2)
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    // mrow children: name input, (stt badge on default), delete — no chev/mods/ctx.
    const classes = Array.from(row.children).map((c) => (c as HTMLElement).className)
    expect(classes.some((c) => c.includes('flex items-center gap-1'))).toBe(false)
    expect(classes.some((c) => c.includes('bg-ink3 rounded px-1.5'))).toBe(false)
  })

  it('the context counter of a metadata-less model is empty', async () => {
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'p',
          url: 'https://p',
          keys: [],
          models: { rich: { context: '9k', input: ['text'] }, bare: {} },
        },
      ],
      default: { provider: 'p', model: 'rich' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    const rows = page.root.querySelectorAll('[data-model-row]')
    const bare = rows[1] as HTMLElement
    // Non-audio row: chev, name, mods, ctx — the ctx chip is the LAST span before delete.
    const ctx = bare.children[bare.children.length - 2] as HTMLElement
    expect(ctx.className).toBe('text-[9px] text-t3 bg-ink3 rounded px-1.5 py-0.5')
    expect(ctx.textContent).toBe('')
  })

  it('STT and TTS tags never cross providers with the same model id', async () => {
    const payload: SettingsPayload = {
      providers: [
        { name: 'llmhost', url: 'https://l', keys: [], models: { 'voice-m': {} } },
        { name: 'audiocpp', url: 'http://127.0.0.1:8082/v1', type: 'audio', keys: [], models: { 'voice-m': {} } },
      ],
      default: { provider: 'llmhost', model: 'voice-m' },
      stt: { provider: 'audiocpp', model: 'voice-m' },
      tts: { provider: 'audiocpp', model: 'voice-m' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0) // llmhost — same model id, wrong provider
    expect(page.root.querySelector('[data-stt-badge]')).toBeNull()
    expect(page.root.querySelector('[data-tts-badge]')).toBeNull()
    await openEdit(page, 1) // audiocpp — the real default owner
    const ttsBadge = page.root.querySelector('[data-tts-badge]') as HTMLElement
    expect(ttsBadge.className).toBe('text-[9px] text-[#7fd4ff] bg-blue/10 rounded-full px-1.5 py-0.5 font-semibold')
    expect(page.root.querySelectorAll('[data-stt-badge]').length).toBe(1)
    expect(page.root.querySelectorAll('[data-tts-badge]').length).toBe(1)
  })

  it('detail row labels wear their fixed-width class', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    await openEdit(page, 1)
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    row.click()
    const detail = row.nextElementSibling as HTMLElement
    const label = (detail.querySelector('.mt-2\\.5') as HTMLElement).children[0] as HTMLElement
    expect(label.className).toBe('text-[11px] text-t2 w-[76px] shrink-0')
    expect(label.textContent).toBe(t('contextLabel'))
  })

  it('toggling a modality on a model without input metadata writes just that modality', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [{ name: 'p', url: 'https://p', keys: [], models: { bare: {} } }],
      default: { provider: 'p', model: 'bare' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    await openEdit(page, 0)
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    row.click()
    const detail = row.nextElementSibling as HTMLElement
    const seg = detail.querySelector('.flex.gap-1') as HTMLElement
    const imageBtn = Array.from(seg.children).find(
      (b) => b.textContent === t('imageModality'),
    ) as HTMLElement
    expect(imageBtn.className).toBe(UNSEL)
    imageBtn.click()
    expect(imageBtn.className).toBe(SEL)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].models.bare.input).toEqual(['image'])
  })

  it('thinking segment buttons are real buttons', async () => {
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    await openEdit(page, 1)
    const row = page.root.querySelector('[data-model-row]') as HTMLElement
    row.click()
    const tseg = (row.nextElementSibling as HTMLElement).querySelectorAll('.flex.gap-1')[1] as HTMLElement
    for (const b of Array.from(tseg.children) as HTMLElement[]) {
      expect(b.getAttribute('type')).toBe('button')
    }
  })

  it('an added model on an audio provider takes empty meta', async () => {
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(
      makeFetchHandler({ payload: AUDIO_PAYLOAD, onPut: (b) => (putBody = b) }),
      AUDIO_PAYLOAD,
    )
    await openEdit(page, 2)
    ;(page.root.querySelector('[data-add-model]') as HTMLElement).click()
    const nameInps = page.root.querySelectorAll('[data-model-name]')
    const nameInp = nameInps[nameInps.length - 1] as HTMLInputElement
    input(nameInp, 'voice-2')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![2].models['voice-2']).toEqual({})
    expect(putBody![2].models['stt-stream']).toEqual({})
  })

  it('the kv body shows with rows; a fresh param row starts empty', async () => {
    const withParams: SettingsPayload = {
      providers: [
        { name: 'p', url: 'https://p', keys: [], models: { m: {} }, extra_params: { a: 1 } },
      ],
      default: { provider: 'p', model: 'm' },
    }
    const page = await openPage(makeFetchHandler({ payload: withParams }), withParams)
    await openEdit(page, 0)
    const kvBody = (page.root.querySelector('[data-add-param]') as HTMLElement).previousElementSibling as HTMLElement
    expect(kvBody.classList.contains('hidden')).toBe(false)
    ;(page.root.querySelector('[data-add-param]') as HTMLElement).click()
    const rows = page.root.querySelectorAll('[data-kv-del]')
    const fresh = rows[rows.length - 1]!.parentElement!.children
    expect((fresh[0] as HTMLInputElement).value).toBe('')
    expect((fresh[1] as HTMLInputElement).value).toBe('')
  })

  it('model fetches trim the url, default the key, and never write free_fetched on plain fetches', async () => {
    modelsBodies.length = 0
    let putBody: SettingsProvider[] | undefined
    const page = await openPage(
      makeFetchHandler({
        payload: PAYLOAD,
        models: { mode: 'fetched', models: [{ id: 'plain' }] },
        onPut: (b) => (putBody = b),
      }),
    )
    await openEdit(page, 1)
    input(field(page, 'url'), '  https://zhipu.example/v4  ')
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    await vi.waitFor(() => expect(modelsBodies.length).toBe(1))
    expect(modelsBodies[0]).toEqual({ url: 'https://zhipu.example/v4', key: 'zk-1', type: 'openai' })
    // Wait for the fetch to fully land (toast) before collecting the form.
    await vi.waitFor(() => expect(toastText(page)).toBe(t('fetchedModels')(1, 1)))
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![1].free_fetched).toBeUndefined()
  })

  it('a blank id in a free fetch never reaches the managed set', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        {
          name: 'or',
          url: 'https://openrouter.ai/api/v1',
          keys: [],
          free_fetched: ['keep-b'],
          models: { 'keep-b': {}, hand: {} },
        },
      ],
      default: { provider: 'or', model: 'hand' },
    }
    const page = await openPage(
      makeFetchHandler({
        payload,
        models: { mode: 'fetched', models: [{ id: 'keep-b' }, { id: '' }] },
        onPut: (b) => (putBody = b),
      }),
      payload,
    )
    await openEdit(page, 0)
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    // added=0 (keep-b already configured): the fetch has landed once this
    // toast shows — only then may the form be collected.
    await vi.waitFor(() => expect(toastText(page)).toBe(t('allModelsConfigured')))
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].free_fetched).toEqual(['keep-b'])
    expect(Object.keys(putBody![0].models).sort()).toEqual(['hand', 'keep-b'])
  })

  it('saving without a rename still drops the cached audio tasks', async () => {
    const models = { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }] }
    const mock = makeFetchHandler({ payload: AUDIO_PAYLOAD, models })
    const page = await openPage(mock, AUDIO_PAYLOAD)
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
    })
    await openEdit(page, 2)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('savedBackedUp')))
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(2)
    })
  })

  it('deleting with an unset default and a blanked name stays the plain confirm', async () => {
    const confirms: string[] = []
    vi.stubGlobal('confirm', (msg: string) => {
      confirms.push(msg)
      return false
    })
    const payload: SettingsPayload = {
      providers: [
        { name: 'a', url: 'https://a', keys: [], models: { m: {} } },
        { name: 'b', url: 'https://b', keys: [], models: { m: {} } },
      ],
      default: { provider: '', model: '' },
    }
    const page = await openPage(makeFetchHandler({ payload }), payload)
    await openEdit(page, 0)
    input(field(page, 'name'), '   ')
    ;(page.root.querySelector('[data-delete-provider]') as HTMLElement).click()
    expect(confirms).toEqual([t('deleteProviderConfirm')])
  })

  it('a settings load failure leaves the default card title blank-separated', async () => {
    const page = createSettingsPage()
    document.body.appendChild(page.root)
    vi.stubGlobal('fetch', makeFetchHandler({ providersGetError: true }))
    page.open()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('loadFailed')('settings fetch failed: 503')))
    const llmTitle = (page.root.querySelector('[data-default-card]') as HTMLElement).querySelector(
      '.font-mono',
    ) as HTMLElement
    await vi.waitFor(() => expect(llmTitle.textContent).toBe(' / '))
  })
})

describe('mutate round three', () => {
  const openEdit = async (page: { root: HTMLElement }, idx = 0) => {
    ;(page.root.querySelectorAll('[data-provider-card]')[idx] as HTMLElement).click()
    await flushMicrotasks()
  }

  it('a keyless provider fetch-models probe sends an empty key', async () => {
    modelsBodies.length = 0
    const payload: SettingsPayload = {
      providers: [{ name: 'p', url: 'https://p', keys: [], models: { m: {} } }],
      default: { provider: 'p', model: 'm' },
    }
    const page = await openPage(
      makeFetchHandler({ payload, models: { mode: 'fetched', models: [{ id: 'x' }] } }),
      payload,
    )
    await openEdit(page, 0)
    ;(page.root.querySelector('[data-fetch-models]') as HTMLElement).click()
    await vi.waitFor(() => expect(modelsBodies.length).toBe(1))
    expect(modelsBodies[0]).toEqual({ url: 'https://p', key: '', type: '' })
  })

  it('a save drops the cached task map too: changed tasks show after saving', async () => {
    let asrOnly = true
    const models = () =>
      asrOnly
        ? { mode: 'fetched', models: [{ id: 'stt-stream', task: 'asr' }] }
        : { mode: 'fetched', models: [{ id: 'stt-stream', task: 'tts' }, { id: 'tts-voice', task: 'tts' }] }
    const mock = vi.fn(async (url: string, init?: RequestInit) => {
      if (url === '/api/settings/models') {
        return { ok: true, status: 200, json: async () => models() }
      }
      const base = makeFetchHandler({ payload: AUDIO_PAYLOAD })
      return (base as unknown as (u: string, i?: RequestInit) => Promise<unknown>)(url, init)
    })
    const page = await openPage(mock as unknown as ReturnType<typeof makeFetchHandler>, AUDIO_PAYLOAD)
    const sttPanel = () => page.root.querySelector('[data-stt-panel]') as HTMLElement
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(1)
      expect(sttPanel().querySelectorAll('[data-stt-model]').length).toBe(2)
    })
    // Collapse before editing so the post-save open is a fresh expansion.
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    // The daemon now retags every model as tts-only.
    asrOnly = false
    await openEdit(page, 2)
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('savedBackedUp')))
    ;(page.root.querySelector('[data-stt-card]') as HTMLElement).firstElementChild!.dispatchEvent(
      new MouseEvent('click', { bubbles: true }),
    )
    await vi.waitFor(() => {
      expect(mock.mock.calls.filter(([u]) => u === '/api/settings/models')).toHaveLength(2)
      // Fresh knowledge: every model is tts-tagged — all gated off the STT card.
      expect(sttPanel().children.length).toBe(0)
    })
  })
})

describe('mutate round four', () => {
  it('remote port 1 is a valid port', async () => {
    const set = vi.fn(() => true)
    vi.stubGlobal('GBotNative', {
      getRemoteTargets: () => '[]',
      setRemoteTargets: set,
    })
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD }))
    const cardEl = page.root.querySelector('[data-remote-card]') as HTMLElement
    ;(cardEl.querySelector('[data-remote-add]') as HTMLElement).click()
    input(cardEl.querySelector('[data-remote-name]') as HTMLInputElement, 'edge')
    input(cardEl.querySelector('[data-remote-host]') as HTMLInputElement, 'edge.box')
    input(cardEl.querySelector('[data-remote-port]') as HTMLInputElement, '1')
    ;(cardEl.querySelector('[data-remote-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(toastText(page)).toBe(t('remoteSaved')))
    expect(JSON.parse(set.mock.calls[0][0])).toEqual([{ name: 'edge', host: 'edge.box', port: 1 }])
  })

  it('an applied report with a failed file starts with its rows expanded', async () => {
    const report: ReloadReport = {
      ...makeReloadReport('mutate'),
      counts: { reloaded: 1, unchanged: 0, failed: 1 },
    }
    const page = await openPage(makeFetchHandler({ payload: PAYLOAD, reloadReport: report }))
    ;(page.root.querySelector('[data-config-reload]') as HTMLElement).click()
    await vi.waitFor(() =>
      expect((page.root.querySelector('[data-config-banner]') as HTMLElement).textContent).toBe(
        t('configBannerOk')(1, 0, 1),
      ),
    )
    const rows = (page.root.querySelector('[data-config-report]') as HTMLElement).lastElementChild as HTMLElement
    expect(rows.classList.contains('hidden')).toBe(false)
  })
})

describe('mutate round five', () => {
  it('a second build press without a release copies exactly once', async () => {
    vi.useFakeTimers()
    try {
      Object.defineProperty(navigator, 'clipboard', { value: undefined, configurable: true })
      const execSpy = vi.fn(() => true)
      document.execCommand = execSpy as unknown as typeof document.execCommand
      const page = await openPage(
        makeFetchHandler({
          payload: PAYLOAD,
          admin: { busy: false, items: [], upgradable: true, build: '3.3.3 · abcd' },
        }),
      )
      const build = page.root.querySelector('[data-system-build]') as HTMLElement
      await vi.waitFor(() => expect(build.textContent).toBe('3.3.3 · abcd'))
      build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      build.dispatchEvent(new MouseEvent('pointerdown', { bubbles: true }))
      vi.advanceTimersByTime(600)
      expect(execSpy).toHaveBeenCalledTimes(1)
    } finally {
      vi.useRealTimers()
      delete (document as unknown as Record<string, unknown>).execCommand
    }
  })

  it('closing the device form does not scroll it into view', async () => {
    const spy = Element.prototype.scrollIntoView as ReturnType<typeof vi.fn>
    const page = await openPage(
      makeFetchHandler({ payload: PAYLOAD, devices: [{ name: 'd', addr: 'a:1', pass: '' }] }),
    )
    const cardEl = page.root.querySelector('[data-rd-card]') as HTMLElement
    ;(cardEl.querySelector('[data-rd-entry]') as HTMLElement).click()
    expect(spy).toHaveBeenCalledWith({ behavior: 'smooth', block: 'nearest' })
    spy.mockClear()
    ;(cardEl.querySelector('[data-rd-back]') as HTMLElement).click()
    await flushMicrotasks()
    expect(spy).not.toHaveBeenCalled()
  })

  it('deleting one of several keys keeps the survivors editable', async () => {
    let putBody: SettingsProvider[] | undefined
    const payload: SettingsPayload = {
      providers: [
        { name: 'p', url: 'https://p', keys: ['k1', 'k2'], models: { m: {} } },
      ],
      default: { provider: 'p', model: 'm' },
    }
    const page = await openPage(makeFetchHandler({ payload, onPut: (b) => (putBody = b) }), payload)
    ;(page.root.querySelectorAll('[data-provider-card]')[0] as HTMLElement).click()
    await flushMicrotasks()
    const keyRows = () =>
      Array.from((page.root.querySelector('[data-add-key]') as HTMLElement).previousElementSibling!
        .children) as HTMLElement[]
    // Delete the SECOND key: the first row keeps its value.
    ;(keyRows()[1].children[2] as HTMLElement).click()
    expect(keyRows().length).toBe(1)
    expect((keyRows()[0].children[0] as HTMLInputElement).value).toBe('k1')
    ;(page.root.querySelector('[data-save]') as HTMLElement).click()
    await vi.waitFor(() => expect(putBody).toBeDefined())
    expect(putBody![0].keys).toEqual(['k1'])
  })
})

