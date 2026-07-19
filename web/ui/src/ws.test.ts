import { describe, it, expect, beforeEach, vi, afterEach } from 'vitest'

class MockWebSocket {
  static instances: MockWebSocket[] = []
  static OPEN = 1
  static CLOSED = 3
  readyState = MockWebSocket.OPEN
  onopen: (() => void) | null = null
  onclose: ((ev: CloseEvent) => void) | null = null
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
    vi.stubGlobal('location', { host: 'localhost' })
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

    MockWebSocket.instances[0].onclose!({ code: 1006 } as CloseEvent)
    vi.advanceTimersByTime(999)
    expect(MockWebSocket.instances.length).toBe(1)
    vi.advanceTimersByTime(1)
    expect(MockWebSocket.instances.length).toBe(2)

    MockWebSocket.instances[1].onclose!({ code: 1006 } as CloseEvent)
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

  it('onStateChange fires connected on onopen', async () => {
    const { getConnection } = await import('./ws')
    const conn = getConnection()
    const states: string[] = []
    conn.onStateChange((s) => states.push(s))

    MockWebSocket.instances[0].onopen!()
    expect(states).toEqual(['connected'])
  })

  it('onStateChange fires reconnecting on onclose then connected on retry', async () => {
    const { getConnection } = await import('./ws')
    const conn = getConnection()
    const states: string[] = []
    conn.onStateChange((s) => states.push(s))

    MockWebSocket.instances[0].onopen!()
    MockWebSocket.instances[0].onclose!({ code: 1006 } as CloseEvent)
    expect(states).toEqual(['connected', 'reconnecting'])

    vi.advanceTimersByTime(1000)
    MockWebSocket.instances[1].onopen!()
    expect(states).toEqual(['connected', 'reconnecting', 'connected'])
  })

  it('onStateChange fires disconnected after 5 failed reconnects', async () => {
    const { getConnection } = await import('./ws')
    const conn = getConnection()
    const states: string[] = []
    conn.onStateChange((s) => states.push(s))

    MockWebSocket.instances[0].onopen!()
    // 5 failed reconnect attempts
    for (let i = 0; i < 5; i++) {
      MockWebSocket.instances[MockWebSocket.instances.length - 1].onclose!({ code: 1006 } as CloseEvent)
      vi.advanceTimersByTime(1000)
    }
    // After 5th failure, reconnectCount=6 > 5 → disconnected
    MockWebSocket.instances[MockWebSocket.instances.length - 1].onclose!({ code: 1006 } as CloseEvent)

    const reconnectingCount = states.filter(s => s === 'reconnecting').length
    expect(reconnectingCount).toBe(5)
    expect(states[states.length - 1]).toBe('disconnected')
  })

  it('disconnected is not fired again after cap (stale onclose guard)', async () => {
    const { getConnection } = await import('./ws')
    const conn = getConnection()
    const states: string[] = []
    conn.onStateChange((s) => states.push(s))

    MockWebSocket.instances[0].onopen!()
    for (let i = 0; i < 5; i++) {
      MockWebSocket.instances[MockWebSocket.instances.length - 1].onclose!({ code: 1006 } as CloseEvent)
      vi.advanceTimersByTime(1000)
    }
    MockWebSocket.instances[MockWebSocket.instances.length - 1].onclose!({ code: 1006 } as CloseEvent)

    const beforeLen = states.length
    // Try triggering another onclose on the same socket
    MockWebSocket.instances[MockWebSocket.instances.length - 1].onclose?.()
    expect(states.length).toBe(beforeLen)
  })
})
