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
