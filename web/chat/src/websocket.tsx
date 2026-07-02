import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useRef,
  useState,
  type ReactNode,
} from 'react'
import type { ServerMessage } from './types'

type Listener = (msg: ServerMessage) => void

type WebSocketContextValue = {
  subscribe: (listener: Listener) => () => void
  send: (payload: object) => void
  connected: boolean
}

const WebSocketContext = createContext<WebSocketContextValue | null>(null)

export function WebSocketProvider({ children }: { children: ReactNode }) {
  // Listeners live in a ref, NOT React state — streaming deltas arrive
  // synchronously from ws.onmessage and must be dispatched without waiting
  // for a re-render. Storing in state would batch/drop them.
  const listenersRef = useRef<Set<Listener>>(new Set())
  const wsRef = useRef<WebSocket | null>(null)
  const [connected, setConnected] = useState(false)

  const wsUrl = `ws://${location.host}/ws/chat`

  useEffect(() => {
    let backoff = 1000
    let reconnectTimer: ReturnType<typeof setTimeout> | null = null
    let disposed = false

    const connect = () => {
      if (disposed) return
      const ws = new WebSocket(wsUrl)
      wsRef.current = ws

      ws.onopen = () => {
        backoff = 1000
        setConnected(true)
      }

      ws.onclose = () => {
        setConnected(false)
        if (disposed) return
        reconnectTimer = setTimeout(() => {
          backoff = Math.min(backoff * 2, 5000)
          connect()
        }, backoff)
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
        listenersRef.current.forEach((fn) => fn(msg))
      }
    }

    connect()

    return () => {
      disposed = true
      if (reconnectTimer) clearTimeout(reconnectTimer)
      if (wsRef.current) {
        wsRef.current.onclose = null
        wsRef.current.close()
      }
    }
  }, [wsUrl])

  const subscribe = useCallback((listener: Listener) => {
    listenersRef.current.add(listener)
    return () => {
      listenersRef.current.delete(listener)
    }
  }, [])

  const send = useCallback((payload: object) => {
    const ws = wsRef.current
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(JSON.stringify(payload))
    }
  }, [])

  return (
    <WebSocketContext.Provider value={{ subscribe, send, connected }}>
      {children}
    </WebSocketContext.Provider>
  )
}

export function useWebSocket(): WebSocketContextValue {
  const ctx = useContext(WebSocketContext)
  if (!ctx) {
    throw new Error('useWebSocket must be used within WebSocketProvider')
  }
  return ctx
}
