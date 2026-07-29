import type { ServerMessage } from './types'

type Listener = (msg: ServerMessage) => void
type BinaryListener = (data: ArrayBuffer) => void

export type ConnState = 'connected' | 'reconnecting' | 'disconnected'

export interface WebSocketConnection {
  subscribe: (listener: Listener) => () => void
  subscribeBinary: (listener: BinaryListener) => () => void
  send: (payload: object) => void
  sendBinary: (data: ArrayBuffer | Uint8Array) => void
  connected: boolean
  onStateChange: (cb: (state: ConnState) => void) => void
  reconnect: () => void
}

interface InternalState {
  listeners: Set<Listener>
  binaryListeners: Set<BinaryListener>
  stateCbs: Set<(s: ConnState) => void>
  ws: WebSocket | null
  connected: boolean
  reconnectCount: number
  reconnectTimer: ReturnType<typeof setTimeout> | null
  disposed: boolean
  disableReconnect: boolean
}

let state: InternalState | null = null

function createState(): InternalState {
  return {
    listeners: new Set(),
    binaryListeners: new Set(),
    stateCbs: new Set(),
    ws: null,
    connected: false,
    reconnectCount: 0,
    reconnectTimer: null,
    disposed: false,
    disableReconnect: false,
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
  ws.binaryType = 'arraybuffer'
  s.ws = ws

  ws.onopen = () => {
    s.reconnectCount = 0
    s.connected = true
    notifyState(s, 'connected')
  }

  ws.onclose = (ev: CloseEvent) => {
    // Server sends close frame with code 1000 + reason "taken_over" when
    // another client takes over this session. We must NOT auto-reconnect,
    // otherwise the two clients ping-pong fighting for the slot. Any
    // other close (1006 abnormal, etc.) is a real disconnect — reconnect.
    if (ev.code === 1000 && ev.reason === 'taken_over') {
      s.disableReconnect = true
      s.connected = false
      notifyState(s, 'disconnected')
      return
    }
    if (s.disableReconnect) return
    scheduleReconnect(s, wsUrl)
  }

  ws.onerror = () => {
    // onclose will follow and trigger reconnect.
  }

  ws.onmessage = (ev: MessageEvent) => {
    // Binary frames (file chunks) route to binaryListeners; text frames
    // parse as JSON and route to text listeners. WS guarantees in-order
    // delivery, so a file_start text frame always precedes its binary
    // chunks which always precede file_end.
    if (ev.data instanceof ArrayBuffer) {
      s.binaryListeners.forEach((fn) => fn(ev.data))
      return
    }
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
  state.disableReconnect = false  // reset takeover guard — user explicitly wants back
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
    subscribeBinary: (listener: BinaryListener) => {
      s.binaryListeners.add(listener)
      return () => {
        s.binaryListeners.delete(listener)
      }
    },
    send: (payload: object) => {
      const ws = s.ws
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(JSON.stringify(payload))
      }
    },
    sendBinary: (data: ArrayBuffer | Uint8Array) => {
      const ws = s.ws
      if (ws && ws.readyState === WebSocket.OPEN) {
        ws.send(data)
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
