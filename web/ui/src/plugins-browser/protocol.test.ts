// protocol.js unit tests: the plugin bridge is a stateless text-frame pump.
// module top-level runs host.channel('browser') on import, so every test
// installs a fresh globalThis.host and re-imports the module (vi.resetModules)
// to get a clean pending/connecting state.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type MockTool = ReturnType<typeof vi.fn>

interface ConnStub {
  inLog: string[]
  send: (frame: string) => void
  onMessage: MockTool
  onClose: MockTool
}

interface HostStub {
  chLog: string[]
  channelNames: string[]
  onMessage: ((m: string) => void) | null
  fetch: MockTool
  dial: MockTool
  log: MockTool
  channel: (name: string) => { send(s: string): void; onMessage(cb: (m: string) => void): void }
}

function makeConn(): ConnStub {
  const conn: ConnStub = {
    inLog: [],
    send: (frame: string) => conn.inLog.push(frame),
    onMessage: vi.fn(),
    onClose: vi.fn(),
  }
  return conn
}

function makeHost(): HostStub {
  const host = {
    chLog: [] as string[],
    channelNames: [] as string[],
    onMessage: null as ((m: string) => void) | null,
    fetch: vi.fn<[], Promise<{ status: number; body: string }>>(),
    dial: vi.fn<[], Promise<ConnStub>>(),
    log: vi.fn(),
  }
  host.channel = (name: string) => {
    host.channelNames.push(name)
    return {
      send: (s: string) => host.chLog.push(s),
      onMessage: (cb: (m: string) => void) => {
        host.onMessage = cb
      },
    }
  }
  return host
}

async function loadProtocol(host: HostStub): Promise<void> {
  vi.resetModules()
  ;(globalThis as Record<string, unknown>).host = host
  await import('../../plugins-browser/protocol.js')
}

// setImmediate hop flushes every pending microtask of the fetch→dial chain.
const tick = (): Promise<void> => new Promise<void>((resolve) => setImmediate(resolve))

async function loadAndSend(host: HostStub, frames: string[]): Promise<void> {
  await loadProtocol(host)
  if (!host.onMessage) throw new Error('protocol.js never registered ch.onMessage')
  for (const f of frames) host.onMessage(f)
  await tick()
}

describe('protocol.js bridge', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
  })
  afterEach(() => {
    delete (globalThis as Record<string, unknown>).host
  })

  it('opens exactly one channel named browser on import', async () => {
    const host = makeHost()
    await loadProtocol(host)
    expect(host.channelNames).toEqual(['browser'])
    expect(host.onMessage).toBeTypeOf('function')
  })

  it('first client frame discovers 127.0.0.1:9333 and dials the debugger URL', async () => {
    const host = makeHost()
    const conn = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/browser/x"}' })
    host.dial.mockResolvedValue(conn)
    await loadAndSend(host, ['{"id":1,"method":"Target.getTargets"}'])
    expect(host.fetch).toHaveBeenCalledTimes(1)
    expect(host.fetch).toHaveBeenCalledWith('http://127.0.0.1:9333/json/version')
    expect(host.dial).toHaveBeenCalledTimes(1)
    expect(host.dial).toHaveBeenCalledWith('ws://127.0.0.1:9222/devtools/browser/x')
    expect(host.log).toHaveBeenCalledWith('CDP attached')
  })

  it('attached status frame precedes the pending flush, frames forwarded in arrival order', async () => {
    const host = makeHost()
    const conn = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockResolvedValue(conn)
    const first = '{"id":1,"method":"Target.getTargets"}'
    const second = '{"id":2,"method":"Runtime.evaluate"}'
    await loadProtocol(host)
    if (!host.onMessage) throw new Error('no onMessage registered')
    // Both frames arrive while the dial is still in flight.
    host.onMessage(first)
    host.onMessage(second)
    await tick()
    expect(host.chLog[0]).toBe('{"__status":"attached"}')
    expect(conn.inLog).toEqual([first, second])
    expect(host.chLog).toHaveLength(1)
  })

  it('frames after attach go straight to the conn without rediscovery', async () => {
    const host = makeHost()
    const conn = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockResolvedValue(conn)
    await loadAndSend(host, ['{"id":1}'])
    host.onMessage!('{"id":2,"method":"Page.enable"}')
    await tick()
    expect(conn.inLog).toEqual(['{"id":1}', '{"id":2,"method":"Page.enable"}'])
    expect(host.fetch).toHaveBeenCalledTimes(1)
  })

  it('chromium frames arriving on the conn are piped to the channel', async () => {
    const host = makeHost()
    const conn = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockResolvedValue(conn)
    await loadAndSend(host, ['{"id":1}'])
    const onConnMessage = conn.onMessage.mock.calls[0][0] as (frame: string) => void
    expect(conn.onMessage).toHaveBeenCalledTimes(1)
    onConnMessage('{"method":"Page.screencastFrame","params":{}}')
    expect(host.chLog).toEqual(['{"__status":"attached"}', '{"method":"Page.screencastFrame","params":{}}'])
  })

  it('conn close drops the conn so the next client frame reconnects', async () => {
    const host = makeHost()
    const conn1 = makeConn()
    const conn2 = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockResolvedValueOnce(conn1).mockResolvedValueOnce(conn2)
    await loadAndSend(host, ['{"id":1}'])
    expect(host.fetch).toHaveBeenCalledTimes(1)
    const onConnClose = conn1.onClose.mock.calls[0][0] as () => void
    expect(conn1.onClose).toHaveBeenCalledTimes(1)
    onConnClose()
    host.onMessage!('{"id":2}')
    await tick()
    expect(host.fetch).toHaveBeenCalledTimes(2)
    expect(host.dial).toHaveBeenCalledTimes(2)
    expect(conn2.inLog).toEqual(['{"id":2}'])
  })

  it('non-200 discovery reply fails the connect with the exact status message', async () => {
    const host = makeHost()
    host.fetch.mockResolvedValue({ status: 503, body: 'nope' })
    await loadAndSend(host, ['{"id":42,"method":"Target.getTargets"}'])
    expect(host.dial).not.toHaveBeenCalled()
    expect(host.chLog).toEqual([
      '{"id":42,"error":{"code":-32000,"message":"browser idle"}}',
      '{"__status":"idle"}',
    ])
    expect(host.log).toHaveBeenCalledWith('CDP connect failed: Error: json/version status 503')
  })

  it('refused dial replies browser idle to every pending request then goes idle', async () => {
    const host = makeHost()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockRejectedValue(new Error('ECONNREFUSED 127.0.0.1:9222'))
    const pending = [
      '{"id":7,"method":"Target.getTargets"}',
      '{"id":8,"method":"Runtime.evaluate"}',
    ]
    await loadProtocol(host)
    if (!host.onMessage) throw new Error('no onMessage registered')
    host.onMessage(pending[0])
    host.onMessage(pending[1])
    await tick()
    expect(host.chLog).toEqual([
      '{"id":7,"error":{"code":-32000,"message":"browser idle"}}',
      '{"id":8,"error":{"code":-32000,"message":"browser idle"}}',
      '{"__status":"idle"}',
    ])
    expect(host.log).toHaveBeenCalledWith('CDP connect failed: Error: ECONNREFUSED 127.0.0.1:9222')
  })

  it('idle failure path skips non-JSON frames and frames without an id', async () => {
    const host = makeHost()
    host.fetch.mockRejectedValue(new Error('idle'))
    await loadAndSend(host, [
      'not json at all',
      '{"method":"Runtime.enable"}',
      '{"id":9,"method":"Page.enable"}',
    ])
    expect(host.chLog).toEqual([
      '{"id":9,"error":{"code":-32000,"message":"browser idle"}}',
      '{"__status":"idle"}',
    ])
  })

  it('a second client frame during an in-flight connect only queues, never re-dials', async () => {
    const host = makeHost()
    const conn = makeConn()
    let releaseFetch: (v: { status: number; body: string }) => void = () => {}
    host.fetch.mockReturnValue(
      new Promise((resolve) => {
        releaseFetch = resolve
      }),
    )
    host.dial.mockResolvedValue(conn)
    await loadProtocol(host)
    if (!host.onMessage) throw new Error('no onMessage registered')
    host.onMessage('{"id":1}')
    host.onMessage('{"id":2}')
    await tick()
    expect(host.fetch).toHaveBeenCalledTimes(1)
    expect(host.dial).not.toHaveBeenCalled()
    releaseFetch({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    await tick()
    expect(host.dial).toHaveBeenCalledTimes(1)
    expect(host.chLog[0]).toBe('{"__status":"attached"}')
    expect(conn.inLog).toEqual(['{"id":1}', '{"id":2}'])
  })

  it('after a failed connect the next client frame retries discovery cleanly', async () => {
    const host = makeHost()
    const conn2 = makeConn()
    host.fetch
      .mockRejectedValueOnce(new Error('ECONNREFUSED'))
      .mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u2"}' })
    host.dial.mockResolvedValue(conn2)
    const first = '{"id":1,"method":"Target.getTargets"}'
    const second = '{"id":2,"method":"Target.getTargets"}'
    await loadProtocol(host)
    if (!host.onMessage) throw new Error('no onMessage registered')
    host.onMessage(first)
    await tick()
    expect(host.chLog).toEqual([
      '{"id":1,"error":{"code":-32000,"message":"browser idle"}}',
      '{"__status":"idle"}',
    ])
    host.onMessage(second)
    await tick()
    // The connecting flag must be reset by the catch: without it no retry runs.
    expect(host.fetch).toHaveBeenCalledTimes(2)
    // The first attempt failed before dialing, so only the retry dials.
    expect(host.dial).toHaveBeenCalledTimes(1)
    expect(host.dial).toHaveBeenLastCalledWith('ws://u2')
    // The failed queue must have been emptied, not refilled with junk.
    expect(conn2.inLog).toEqual([second])
    expect(host.chLog).toContain('{"__status":"attached"}')
  })

  it('frames arriving while attached never touch discovery', async () => {
    const host = makeHost()
    const conn = makeConn()
    host.fetch.mockResolvedValue({ status: 200, body: '{"webSocketDebuggerUrl":"ws://u"}' })
    host.dial.mockResolvedValue(conn)
    await loadAndSend(host, ['{"id":1}'])
    for (let i = 2; i <= 6; i++) {
      host.onMessage!('{"id":' + i + '}')
    }
    await tick()
    expect(host.fetch).toHaveBeenCalledTimes(1)
    expect(conn.inLog).toHaveLength(6) // the attached frame plus ids 2..6
  })
})
