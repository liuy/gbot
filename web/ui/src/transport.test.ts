import { describe, it, expect, vi } from 'vitest'
import { createTransportClient } from './transport'

function setup() {
  const texts: object[] = []
  const binaries: Uint8Array[] = []
  const hub = createTransportClient({
    sendText: (o) => texts.push(o),
    sendBinary: (d) => binaries.push(d),
  })
  return { hub, texts, binaries }
}

const opened = (hub: ReturnType<typeof setup>['hub'], id: number, name = 'browser.live') =>
  hub.handleServerText({ type: 'channel_opened', channel: name, channelId: id })

describe('channel hub', () => {
  it('queues outbound frames until the server assigns an id', () => {
    const { hub, texts, binaries } = setup()
    const ch = hub.openChannel('browser.live')
    expect(texts).toEqual([{ type: 'channel_open', channel: 'browser.live' }])

    ch.send(new Uint8Array([1, 2, 3]))
    expect(binaries).toHaveLength(0) // queued, not sent

    opened(hub, 7)
    ch.send(new Uint8Array([9]))
    expect(binaries).toEqual([new Uint8Array([7, 1, 2, 3]), new Uint8Array([7, 9])])
  })

  it('routes server binary frames by channel id', () => {
    const { hub } = setup()
    const ch = hub.openChannel('browser.live')
    opened(hub, 3)
    const got: Uint8Array[] = []
    ch.setOnMessage((d) => got.push(d))
    hub.handleServerBinary(new Uint8Array([3, 104, 105]).buffer)
    hub.handleServerBinary(new Uint8Array([9, 1]).buffer) // unknown id: dropped
    expect(got).toEqual([new Uint8Array([104, 105])])
  })

  it('close notifies the server once and fires onclose exactly once', () => {
    const { hub, texts } = setup()
    const ch = hub.openChannel('browser.live')
    opened(hub, 4)
    const onClose = vi.fn()
    ch.setOnClose(onClose)
    ch.close()
    ch.close() // idempotent
    expect(texts.filter((t) => (t as { type: string }).type === 'channel_close')).toEqual([
      { type: 'channel_close', channelId: 4, channel: 'browser.live' },
    ])
    expect(onClose).toHaveBeenCalledTimes(1)
    // After local close, binary routing to the dead channel is a no-op.
    expect(() => hub.handleServerBinary(new Uint8Array([4, 1]).buffer)).not.toThrow()
  })

  it('close before the id is assigned goes by channel name (no id to reference)', () => {
    const { hub, texts } = setup()
    const ch = hub.openChannel('browser.live')
    ch.close() // closed mid-connecting: the server never assigned an id
    expect(texts.filter((t) => (t as { type: string }).type === 'channel_close')).toEqual([
      { type: 'channel_close', channel: 'browser.live' },
    ])
  })

  it('server-initiated channel_close tears the channel down', () => {
    const { hub } = setup()
    const ch = hub.openChannel('browser.live')
    opened(hub, 5)
    const onClose = vi.fn()
    ch.setOnClose(onClose)
    expect(hub.handleServerText({ type: 'channel_close', channelId: 5 })).toBe(true)
    expect(onClose).toHaveBeenCalledTimes(1)
    expect(ch.opened).toBe(false)
  })

  it('channel_error tears the pending channel down', () => {
    const { hub } = setup()
    const ch = hub.openChannel('no.such')
    const onClose = vi.fn()
    ch.setOnClose(onClose)
    expect(hub.handleServerText({ type: 'channel_error', channel: 'no.such', error: 'no provider' })).toBe(true)
    expect(onClose).toHaveBeenCalledTimes(1)
  })

  it('reset closes every channel (WS died or takeover)', () => {
    const { hub } = setup()
    const a = hub.openChannel('a.b')
    const b = hub.openChannel('c.d')
    opened(hub, 1, 'a.b')
    const onCloseA = vi.fn()
    const onCloseB = vi.fn()
    a.setOnClose(onCloseA)
    b.setOnClose(onCloseB)
    hub.reset()
    expect(onCloseA).toHaveBeenCalledTimes(1)
    expect(onCloseB).toHaveBeenCalledTimes(1)
    expect(a.opened).toBe(false)
    expect(b.opened).toBe(false)
  })

  it('non-channel messages are not consumed', () => {
    const { hub } = setup()
    expect(hub.handleServerText({ type: 'message', text: 'hi' })).toBe(false)
    expect(hub.handleServerText(null)).toBe(false)
  })

  it('reopen after close is allowed with a fresh id', () => {
    const { hub } = setup()
    const first = hub.openChannel('browser.live')
    opened(hub, 1)
    first.close()
    const second = hub.openChannel('browser.live')
    expect(second.opened).toBe(false)
    opened(hub, 2)
    expect(second.opened).toBe(true)
  })
})
