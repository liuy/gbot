// Remote-desktop data layer: the settings-managed device list plus the lazy
// sidebar prober. Address normalization lives server-side — every function
// here sends the raw addr.

export interface RemoteDevice {
  name: string
  addr: string
  pass: string
}

export interface RemoteTestResult {
  ok: boolean
  latencyMs?: number
  error?: string
}

const jsonHeaders = { 'Content-Type': 'application/json' }

export async function fetchRemoteDevices(): Promise<RemoteDevice[]> {
  const res = await fetch('/api/settings/remotedesktop')
  if (!res.ok) throw new Error(`remote devices fetch failed: ${res.status}`)
  const payload = (await res.json()) as { devices?: RemoteDevice[] }
  return payload.devices ?? []
}

export async function saveRemoteDevices(devices: RemoteDevice[]): Promise<void> {
  const res = await fetch('/api/settings/remotedesktop', {
    method: 'PUT',
    headers: jsonHeaders,
    body: JSON.stringify(devices),
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

export async function testRemoteDevice(
  addr: string,
  pass: string,
  signal?: AbortSignal,
): Promise<RemoteTestResult> {
  // pass stays out of the body unless set (wire discipline — the probe
  // ignores it anyway; it is carried for endpoint symmetry).
  const body: Record<string, unknown> = { addr }
  if (pass) body.pass = pass
  const res = await fetch('/api/settings/remotedesktop/test', {
    method: 'POST',
    headers: jsonHeaders,
    body: JSON.stringify(body),
    signal,
  })
  if (!res.ok) return { ok: false, error: `HTTP ${res.status}` }
  return res.json()
}

export const PROBE_TIMEOUT_MS = 2000
export const PROBE_CACHE_MS = 60000

export function createDeviceProber(): {
  probe(devices: RemoteDevice[], report: (name: string, ok: boolean) => void): void
} {
  const results = new Map<string, { ok: boolean; at: number }>()
  const inflight = new Map<string, Promise<boolean>>()

  // Resolves with the probe outcome (caching it), rejects on transport
  // failure/abort without caching — the dot stays grey and the next open
  // retries.
  const start = (device: RemoteDevice): Promise<boolean> => {
    const controller = new AbortController()
    const timer = setTimeout(() => controller.abort(), PROBE_TIMEOUT_MS)
    return testRemoteDevice(device.addr, '', controller.signal).then(
      (res) => {
        results.set(device.addr, { ok: res.ok, at: Date.now() })
        return res.ok
      },
      () => {
        throw new Error('probe failed')
      },
    ).finally(() => {
      clearTimeout(timer)
      inflight.delete(device.addr)
    })
  }

  return {
    probe(devices: RemoteDevice[], report: (name: string, ok: boolean) => void): void {
      for (const device of devices) {
        const cached = results.get(device.addr)
        if (cached && Date.now() - cached.at < PROBE_CACHE_MS) {
          report(device.name, cached.ok)
          continue
        }
        let pending = inflight.get(device.addr)
        if (!pending) {
          pending = start(device)
          inflight.set(device.addr, pending)
        }
        pending.then((ok) => report(device.name, ok)).catch(() => {})
      }
    },
  }
}
