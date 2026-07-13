import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'

class MockWebSocket {
  static instances: MockWebSocket[] = []
  static OPEN = 1
  static CLOSED = 3
  readyState = MockWebSocket.OPEN
  onopen: (() => void) | null = null
  onclose: (() => void) | null = null
  onerror: (() => void) | null = null
  onmessage: ((ev: { data: string }) => void) | null = null
  sentMessages: string[] = []

  constructor() {
    MockWebSocket.instances.push(this)
  }
  close() {
    this.readyState = MockWebSocket.CLOSED
  }
  send(data: string) {
    this.sentMessages.push(data)
  }
}

describe('ws reconnect backoff', () => {
  beforeEach(() => {
    MockWebSocket.instances = []
    vi.stubGlobal('WebSocket', MockWebSocket)
    vi.useFakeTimers()
    vi.resetModules()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.unstubAllGlobals()
  })

  it('reconnect delay stays 1s when onclose fires before onopen (rapid disconnect)', async () => {
    const { getConnection } = await import('./ws')
    getConnection()

    MockWebSocket.instances[0].onopen!()

    MockWebSocket.instances[0].onclose!()
    vi.advanceTimersByTime(999)
    expect(MockWebSocket.instances.length).toBe(1)
    vi.advanceTimersByTime(1)
    expect(MockWebSocket.instances.length).toBe(2)

    MockWebSocket.instances[1].onclose!()
    vi.advanceTimersByTime(999)
    expect(MockWebSocket.instances.length).toBe(2)
    vi.advanceTimersByTime(1)
    expect(MockWebSocket.instances.length).toBe(3)
  })

  it('forwards messages to listeners', async () => {
    const { getConnection } = await import('./ws')
    const conn = getConnection()
    const listener = vi.fn()
    conn.subscribe(listener)

    const ws = MockWebSocket.instances[0]
    ws.onopen!()

    ws.onmessage!({ data: JSON.stringify({ type: 'error', message: 'test' }) })
    expect(listener).toHaveBeenCalledTimes(1)
    expect(listener).toHaveBeenCalledWith({ type: 'error', message: 'test' })
  })
})
