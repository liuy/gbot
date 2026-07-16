import type { ServerMessage } from './types'

type Listener = (msg: ServerMessage) => void

export type ConnState = 'connected' | 'reconnecting' | 'disconnected'

export interface WebSocketConnection {
  subscribe: (listener: Listener) => () => void
  send: (payload: object) => void
  connected: boolean
  onStateChange: (cb: (state: ConnState) => void) => void
  reconnect: () => void
}

interface InternalState {
  listeners: Set<Listener>
  stateCbs: Set<(s: ConnState) => void>
  ws: WebSocket | null
  connected: boolean
  reconnectCount: number
  reconnectTimer: ReturnType<typeof setTimeout> | null
  disposed: boolean
}

let state: InternalState | null = null

function createState(): InternalState {
  return {
    listeners: new Set(),
    stateCbs: new Set(),
    ws: null,
    connected: false,
    reconnectCount: 0,
    reconnectTimer: null,
    disposed: false,
  }
}

function notifyState(s: InternalState, cs: ConnState) {
  s.stateCbs.forEach(cb => cb(cs))
}

function scheduleReconnect(s: InternalState, wsUrl: string) {
  s.connected = false
  if (s.disposed) return
  if (s.reconnectTimer) clearTimeout(s.reconnectTimer)
  s.reconnectCount++
  if (s.reconnectCount > 5) {
    if (s.ws) s.ws.onclose = null
    notifyState(s, 'disconnected')
    return
  }
  notifyState(s, 'reconnecting')
  s.reconnectTimer = setTimeout(() => {
    connect(s, wsUrl)
  }, 1000)
}

function connect(s: InternalState, wsUrl: string) {
  if (s.disposed) return
  if (s.ws) {
    s.ws.onclose = null
    s.ws.onerror = null
    s.ws.onmessage = null
    try { s.ws.close() } catch { /* socket already closed */ }
  }
  const ws = new WebSocket(wsUrl)
  s.ws = ws

  ws.onopen = () => {
    s.reconnectCount = 0
    s.connected = true
    notifyState(s, 'connected')
  }

  ws.onclose = () => {
    scheduleReconnect(s, wsUrl)
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

export function getConnection(): WebSocketConnection {
  if (state) return connFromState(state)
  const s = createState()
  state = s
  const wsUrl = `ws://${location.host}/ws/chat`
  connect(s, wsUrl)
  return connFromState(s)
}

function manualReconnect() {
  if (!state) return
  state.reconnectCount = 0
  const wsUrl = `ws://${location.host}/ws/chat`
  connect(state, wsUrl)
}

function connFromState(s: InternalState): WebSocketConnection {
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
    onStateChange: (cb: (cs: ConnState) => void) => {
      s.stateCbs.add(cb)
    },
    reconnect: manualReconnect,
  }
}
