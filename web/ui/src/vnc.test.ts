import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchRemoteDevices,
  saveRemoteDevices,
  testRemoteDevice,
  createDeviceProber,
  PROBE_TIMEOUT_MS,
  type RemoteDevice,
} from './vnc'

const DEVICES: RemoteDevice[] = [
  { name: 'win11', addr: 'ws://127.0.0.1:8006', pass: '' },
  { name: 'nas', addr: 'wss://nas.local:8006', pass: 'pw' },
]

// Stubs the remote-desktop endpoints, routing by URL+method. devices
// controls the GET payload (undefined = key missing from the body);
// neverResolve hangs the probe until its signal aborts (timeout path).
function makeFetchHandler(
  opts: {
    devices?: RemoteDevice[] | null
    testResult?: { ok: boolean; latencyMs?: number; error?: string }
    testStatus?: number
    neverResolve?: boolean
    onPut?: (devices: RemoteDevice[]) => void
    putError?: string
  } = {},
) {
  return vi.fn(async (url: string, init?: RequestInit) => {
    const method = init?.method ?? 'GET'
    if (url === '/api/settings/remotedesktop') {
      if (method === 'PUT') {
        opts.onPut?.(JSON.parse(String(init?.body)) as RemoteDevice[])
        if (opts.putError) {
          return { ok: false, status: 400, json: async () => ({ error: opts.putError }) }
        }
        return { ok: true, status: 200, json: async () => ({ ok: true }) }
      }
      const payload =
        opts.devices === undefined ? {} : { devices: opts.devices ?? [] }
      return { ok: true, status: 200, json: async () => payload }
    }
    if (url === '/api/settings/remotedesktop/test') {
      if (opts.neverResolve) {
        return new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new Error('aborted')))
        })
      }
      if (opts.testStatus !== undefined && opts.testStatus !== 200) {
        return { ok: false, status: opts.testStatus, json: async () => ({}) }
      }
      return { ok: true, status: 200, json: async () => opts.testResult ?? { ok: true, latencyMs: 12 } }
    }
    throw new Error('unexpected fetch ' + method + ' ' + url)
  })
}

afterEach(() => {
  vi.unstubAllGlobals()
  vi.useRealTimers()
})

describe('fetchRemoteDevices', () => {
  it('GETs /api/settings/remotedesktop and returns the devices as-is', async () => {
    const mock = makeFetchHandler({ devices: DEVICES })
    vi.stubGlobal('fetch', mock)

    const got = await fetchRemoteDevices()

    expect(mock).toHaveBeenCalledTimes(1)
    expect(mock).toHaveBeenCalledWith('/api/settings/remotedesktop')
    expect(got).toEqual(DEVICES)
  })

  it('returns [] on empty and on missing devices payloads', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({ devices: [] }))
    expect(await fetchRemoteDevices()).toEqual([])

    vi.stubGlobal('fetch', makeFetchHandler({ devices: null }))
    expect(await fetchRemoteDevices()).toEqual([])

    vi.stubGlobal('fetch', makeFetchHandler({}))
    expect(await fetchRemoteDevices()).toEqual([])
  })
})

describe('saveRemoteDevices', () => {
  it('PUTs the bare devices array as the request body', async () => {
    const onPut = vi.fn()
    const mock = makeFetchHandler({ onPut })
    vi.stubGlobal('fetch', mock)

    await saveRemoteDevices(DEVICES)

    expect(mock).toHaveBeenCalledWith(
      '/api/settings/remotedesktop',
      expect.objectContaining({ method: 'PUT', body: JSON.stringify(DEVICES) }),
    )
    expect(onPut).toHaveBeenCalledWith(DEVICES)
  })

  it('throws carrying the response error text on !ok', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({ putError: 'duplicate device name "win11"' }))

    await expect(saveRemoteDevices(DEVICES)).rejects.toThrow('duplicate device name "win11"')
  })
})

describe('testRemoteDevice', () => {
  it('POSTs {addr,pass} when pass is set and {addr} when empty', async () => {
    const mock = makeFetchHandler({ testResult: { ok: true, latencyMs: 12 } })
    vi.stubGlobal('fetch', mock)

    await testRemoteDevice('ws://127.0.0.1:8006', 'pw')
    await testRemoteDevice('ws://127.0.0.1:8006', '')

    const bodies = mock.mock.calls.map((call) => String(call[1]?.body))
    expect(bodies).toEqual([
      '{"addr":"ws://127.0.0.1:8006","pass":"pw"}',
      '{"addr":"ws://127.0.0.1:8006"}',
    ])
  })

  it('maps a transport !ok to {ok:false, error:"HTTP 500"}', async () => {
    vi.stubGlobal('fetch', makeFetchHandler({ testStatus: 500 }))

    expect(await testRemoteDevice('ws://x', '')).toEqual({ ok: false, error: 'HTTP 500' })
  })
})

describe('createDeviceProber', () => {
  it('cold start: probes every device once, reporting (name,true) per device', async () => {
    vi.useFakeTimers()
    const mock = makeFetchHandler({ testResult: { ok: true, latencyMs: 5 } })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const reports: Array<[string, boolean]> = []

    prober.probe(DEVICES, (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)

    expect(mock).toHaveBeenCalledTimes(2)
    expect(reports).toEqual([
      ['win11', true],
      ['nas', true],
    ])
  })

  it('cache hit: replays a fresh result synchronously without refetching', async () => {
    vi.useFakeTimers()
    const mock = makeFetchHandler({ testResult: { ok: true, latencyMs: 5 } })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const reports: Array<[string, boolean]> = []
    prober.probe(DEVICES, (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)
    expect(mock).toHaveBeenCalledTimes(2)
    reports.length = 0

    await vi.advanceTimersByTimeAsync(59_000)
    prober.probe(DEVICES, (name, ok) => reports.push([name, ok]))

    expect(mock).toHaveBeenCalledTimes(2)
    expect(reports).toEqual([
      ['win11', true],
      ['nas', true],
    ])
  })

  it('cache expiry: refetches after the TTL lapses', async () => {
    vi.useFakeTimers()
    const mock = makeFetchHandler({ testResult: { ok: true, latencyMs: 5 } })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const reports: Array<[string, boolean]> = []
    prober.probe(DEVICES, (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)
    expect(mock).toHaveBeenCalledTimes(2)

    await vi.advanceTimersByTimeAsync(60_001)
    prober.probe(DEVICES, (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)

    expect(mock).toHaveBeenCalledTimes(4)
  })

  it('timeout: aborts a hung probe, reports and caches nothing', async () => {
    vi.useFakeTimers()
    const observed: Array<AbortSignal> = []
    const mock = vi.fn((url: string, init?: RequestInit) => {
      if (url === '/api/settings/remotedesktop/test') {
        observed.push(init?.signal as AbortSignal)
        return new Promise((_resolve, reject) => {
          init?.signal?.addEventListener('abort', () => reject(new Error('aborted')))
        })
      }
      throw new Error('unexpected fetch ' + url)
    })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const reports: Array<[string, boolean]> = []

    prober.probe([DEVICES[0]], (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(PROBE_TIMEOUT_MS)

    expect(observed).toHaveLength(1)
    expect(observed[0].aborted).toBe(true)
    expect(reports).toEqual([])

    // Nothing cached: the next open retries (a stale cache would replay
    // synchronously and skip the fetch below).
    const retry = makeFetchHandler({ testResult: { ok: true, latencyMs: 1 } })
    vi.stubGlobal('fetch', retry)
    prober.probe([DEVICES[0]], (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)
    expect(retry).toHaveBeenCalledTimes(1)
    expect(reports).toEqual([['win11', true]])
  })

  it('refused: caches ok:false as a fresh observation (no refetch within TTL)', async () => {
    vi.useFakeTimers()
    const mock = makeFetchHandler({ testResult: { ok: false, error: 'dial tcp refused' } })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const reports: Array<[string, boolean]> = []

    prober.probe([DEVICES[0]], (name, ok) => reports.push([name, ok]))
    await vi.advanceTimersByTimeAsync(0)
    expect(reports).toEqual([['win11', false]])
    reports.length = 0

    prober.probe([DEVICES[0]], (name, ok) => reports.push([name, ok]))
    expect(mock).toHaveBeenCalledTimes(1)
    expect(reports).toEqual([['win11', false]])
  })

  it('in-flight dedup: two callers share one fetch, both reports delivered', async () => {
    vi.useFakeTimers()
    let release!: () => void
    const gate = new Promise<Response>((resolve) => {
      release = () => resolve({ ok: true, status: 200, json: async () => ({ ok: true, latencyMs: 3 }) } as Response)
    })
    const mock = vi.fn((url: string) => {
      if (url === '/api/settings/remotedesktop/test') return gate
      throw new Error('unexpected fetch ' + url)
    })
    vi.stubGlobal('fetch', mock)
    const prober = createDeviceProber()
    const a: Array<[string, boolean]> = []
    const b: Array<[string, boolean]> = []

    prober.probe([DEVICES[0]], (name, ok) => a.push([name, ok]))
    prober.probe([DEVICES[0]], (name, ok) => b.push([name, ok]))
    expect(mock).toHaveBeenCalledTimes(1)

    release()
    await vi.advanceTimersByTimeAsync(0)
    expect(a).toEqual([['win11', true]])
    expect(b).toEqual([['win11', true]])
  })
})
