import type { ServerMessage } from './types'

type Listener = (msg: ServerMessage) => void

export interface WebSocketConnection {
  subscribe: (listener: Listener) => () => void
  send: (payload: object) => void
  connected: boolean
}

// Listeners live in a Set, not state — streaming deltas arrive
// synchronously from ws.onmessage and must be dispatched without batching.
interface ConnState {
  listeners: Set<Listener>
  ws: WebSocket | null
  connected: boolean
  reconnectCount: number
  reconnectTimer: ReturnType<typeof setTimeout> | null
  disposed: boolean
}

let state: ConnState | null = null

function createState(): ConnState {
  return {
    listeners: new Set(),
    ws: null,
    connected: false,
    reconnectCount: 0,
    reconnectTimer: null,
    disposed: false,
  }
}

function connect(s: ConnState, wsUrl: string) {
  if (s.disposed) return
  if (s.ws) {
    s.ws.onclose = null
    s.ws.onerror = null
    s.ws.onmessage = null
    try { s.ws.close() } catch {}
  }
  const ws = new WebSocket(wsUrl)
  s.ws = ws

  ws.onopen = () => {
    s.reconnectCount = 0
    s.connected = true
  }

  ws.onclose = () => {
    s.connected = false
    if (s.disposed) return
    if (s.reconnectTimer) clearTimeout(s.reconnectTimer)
    s.reconnectCount++
    if (s.reconnectCount > 5) return
    s.reconnectTimer = setTimeout(() => {
      connect(s, wsUrl)
    }, 1000)
  }

  ws.onerror = () => {
    // onclose will follow and trigger reconnect.
  }

  ws.onmessage = (ev: MessageEvent) => {
    let msg: ServerMessage
    try {
      msg = JSON.parse(ev.data) as ServerMessage
    } catch {
      return
    }
    s.listeners.forEach((fn) => fn(msg))
  }
}

// Lazily creates the singleton WS on first call. Idempotent — returns the
// same instance on every subsequent call.
export function getConnection(): WebSocketConnection {
  if (state) return connFromState(state)
  const s = createState()
  state = s
  const wsUrl = `ws://${location.host}/ws/chat`
  connect(s, wsUrl)
  return connFromState(s)
}

function connFromState(s: ConnState): WebSocketConnection {
  return {
    subscribe: (listener: Listener) => {
      s.listeners.add(listener)
      return () => {
        s.listeners.delete(listener)
      }
    },
    send: (payload: object) => {
      const ws = s.ws
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(payload))
      }
    },
    get connected() {
      return s.connected
    },
  }
}
