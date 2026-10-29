// Transport client: multiplexes named byte channels over the single chat WS.
// Wire protocol (server contract, pkg/wui/server.go):
//   text   {"type":"channel_open","channel":name} → channel_opened{channelId}
//          {"type":"channel_close","channelId":N} (both directions)
//   binary [1-byte channelId][payload]            (ids start at 1; 0x00 is
//                                                 never assigned)
//
// Ids are assigned by the server; a channel object exists locally from
// openChannel() and queues outbound frames until channel_opened arrives
// (the renderer fires its first CDP call immediately).
//
// Lifecycle: a WS close (takeover or drop) invalidates every channel — the
// server unbinds them all — so reset() fires onclose handlers and rejects
// pending opens. Fresh opens on the new connection get fresh ids.

export interface Channel {
  readonly name: string
  /** Sends payload bytes; queued until the server confirms the channel. */
  send: (data: Uint8Array) => void
  setOnMessage: (cb: (data: Uint8Array) => void) => void
  setOnClose: (cb: () => void) => void
  /** Client-initiated close: notifies the server and tears down locally. */
  close: () => void
  /** True once the server assigned an id. */
  readonly opened: boolean
}

export interface TransportClient {
  openChannel: (name: string) => Channel
  /** Routes one parsed server text frame; true when it is a channel_* message. */
  handleServerText: (msg: unknown) => boolean
  /** Routes one server binary frame ([id][payload]). */
  handleServerBinary: (buf: ArrayBuffer) => void
  /** Tears everything down (WS closed or taken over); fires onclose. */
  reset: () => void
}

interface ChannelState {
  name: string
  id: number | null
  onMessage: ((data: Uint8Array) => void) | null
  onClose: (() => void) | null
  queue: Uint8Array[]
  closed: boolean
}

export function createTransportClient(opts: {
  sendText: (obj: object) => void
  sendBinary: (data: Uint8Array) => void
}): TransportClient {
  const channels = new Map<string, ChannelState>()
  const byId = new Map<number, ChannelState>()

  const teardown = (st: ChannelState) => {
    if (st.closed) return
    st.closed = true
    if (st.id !== null) byId.delete(st.id)
    channels.delete(st.name)
    st.queue = []
    st.onClose?.()
  }

  return {
    openChannel(name: string): Channel {
      const existing = channels.get(name)
      if (existing && !existing.closed) {
        throw new Error(`channel ${name} already open`)
      }
      const st: ChannelState = {
        name,
        id: null,
        onMessage: null,
        onClose: null,
        queue: [],
        closed: false,
      }
      channels.set(name, st)
      opts.sendText({ type: 'channel_open', channel: name })
      return {
        name,
        get opened() {
          return st.id !== null && !st.closed
        },
        send: (data: Uint8Array) => {
          if (st.closed) return
          if (st.id === null) {
            st.queue.push(data)
            return
          }
          const frame = new Uint8Array(1 + data.length)
          frame[0] = st.id
          frame.set(data, 1)
          opts.sendBinary(frame)
        },
        setOnMessage: (cb) => {
          st.onMessage = cb
        },
        setOnClose: (cb) => {
          st.onClose = cb
        },
        close: () => {
          if (st.closed) return
          if (st.id !== null) {
            opts.sendText({ type: 'channel_close', channelId: st.id, channel: st.name })
          } else {
            // Closed before the server assigned an id (card closed mid-
            // connecting): the binding is name-keyed server-side, so close
            // by name or it leaks until a takeover or provider bounce.
            opts.sendText({ type: 'channel_close', channel: st.name })
          }
          teardown(st)
        },
      }
    },

    handleServerText(msg: unknown): boolean {
      const m = msg as { type?: string; channel?: string; channelId?: number; error?: string }
      switch (m?.type) {
        case 'channel_opened': {
          const st = channels.get(m.channel ?? '')
          if (!st || st.closed) return true
          st.id = m.channelId ?? -1
          byId.set(st.id, st)
          for (const q of st.queue.splice(0)) {
            const frame = new Uint8Array(1 + q.length)
            frame[0] = st.id
            frame.set(q, 1)
            opts.sendBinary(frame)
          }
          return true
        }
        case 'channel_error': {
          const st = channels.get(m.channel ?? '')
          if (st) teardown(st)
          return true
        }
        case 'channel_close': {
          const st = byId.get(m.channelId ?? -1)
          if (st) teardown(st)
          return true
        }
        default:
          return false
      }
    },

    handleServerBinary(buf: ArrayBuffer): void {
      const frame = new Uint8Array(buf)
      if (frame.length === 0) return
      const st = byId.get(frame[0])
      if (!st) return
      st.onMessage?.(frame.subarray(1))
    },

    reset(): void {
      for (const st of [...channels.values()]) teardown(st)
    },
  }
}
