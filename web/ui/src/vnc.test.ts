import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  fetchRemoteDevices,
  saveRemoteDevices,
  testRemoteDevice,
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
