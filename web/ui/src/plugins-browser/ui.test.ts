// ui.js unit tests: card DOM skeleton, CDPClient dispatch/timeout behavior,
// the lazy probe loop, screencast drawing (canvas backing store at full bitmap
// resolution — the pixel-blur regression pin), and pointer/keyboard input
// mapping. CDPClient is module-private, so every test drives it through
// mount() with a stubbed channel: feed via channel.onMessage, observe via
// channel.send.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type MockTool = ReturnType<typeof vi.fn>

interface SentMessage {
  id?: number
  method?: string
  params?: Record<string, unknown>
  sessionId?: string
  __status?: string
}

interface ChannelStub {
  sendLog: Uint8Array[]
  send: (bytes: Uint8Array) => void
  setOnMessage: MockTool
  setOnClose: MockTool
  close: MockTool
  message: ((data: Uint8Array) => void) | null
  closeCb: (() => void) | null
}

interface CtxStub {
  openChannel: MockTool
  close: MockTool
  headerSlot: MockTool
  slot: HTMLDivElement
}

const encoder = new TextEncoder()
const decoder = new TextDecoder()

function makeChannel(): ChannelStub {
  const channel: ChannelStub = {
    sendLog: [],
    send: (bytes: Uint8Array) => {
      channel.sendLog.push(bytes)
    },
    setOnMessage: vi.fn(),
    setOnClose: vi.fn(),
    close: vi.fn(),
    message: null,
    closeCb: null,
  }
  channel.setOnMessage.mockImplementation((cb: (data: Uint8Array) => void) => {
    channel.message = cb
  })
  channel.setOnClose.mockImplementation((cb: () => void) => {
    channel.closeCb = cb
  })
  return channel
}

function makeCtx(channel: ChannelStub): CtxStub {
  const slot = document.createElement('div')
  return {
    openChannel: vi.fn(() => channel),
    close: vi.fn(),
    headerSlot: vi.fn(() => slot),
    slot,
  }
}

const drawImage = vi.fn()

interface Mounted {
  container: HTMLDivElement
  canvas: HTMLCanvasElement
  hidden: HTMLInputElement
  status: HTMLDivElement
  channel: ChannelStub
  ctx: CtxStub
  cleanup: () => void
}

const mounted: Mounted[] = []

async function mountCard(): Promise<Mounted> {
  vi.resetModules()
  const channel = makeChannel()
  const ctx = makeCtx(channel)
  const container = document.createElement('div')
  document.body.append(container)
  const mod = await import('../../plugins-browser/ui.js')
  const cleanup = mod.mount(container, ctx)
  const canvas = container.children[0] as HTMLCanvasElement
  const hidden = container.children[1] as HTMLInputElement
  const status = container.children[2] as HTMLDivElement
  const m: Mounted = { container, canvas, hidden, status, channel, ctx, cleanup }
  mounted.push(m)
  return m
}

const tick = (): Promise<void> => new Promise<void>((resolve) => setImmediate(resolve))

async function feed(m: Mounted, obj: SentMessage | string): Promise<void> {
  if (!m.channel.message) throw new Error('channel.onMessage never registered')
  m.channel.message(encoder.encode(typeof obj === 'string' ? obj : JSON.stringify(obj)))
  await tick()
}

function sentMessages(m: Mounted): SentMessage[] {
  return m.channel.sendLog.map((b) => JSON.parse(decoder.decode(b)))
}

function sentOfMethod(m: Mounted, method: string): SentMessage[] {
  return sentMessages(m).filter((msg) => msg.method === method)
}

// Drive the startup chain: getTargets reply → attachToTarget reply →
// startScreencast reply. Send ids: getTargets=1, setDiscoverTargets=2,
// attachToTarget=3, startScreencast=4.
async function attachLive(m: Mounted): Promise<void> {
  await feed(m, {
    id: 1,
    result: { targetInfos: [{ type: 'iframe', targetId: 'frame1' }, { type: 'page', targetId: 'p1' }] },
  })
  await feed(m, { id: 3, result: { sessionId: 's1' } })
  await feed(m, { id: 4, result: {} })
}

function pointerEvent(type: string, init: Record<string, unknown>): Event {
  const e = new Event(type, { bubbles: true, cancelable: true })
  Object.assign(e, init)
  return e
}

function stubRect(m: Mounted, width: number, height: number): void {
  vi.spyOn(m.canvas, 'getBoundingClientRect').mockReturnValue({
    width,
    height,
    top: 0,
    left: 0,
    right: width,
    bottom: height,
    x: 0,
    y: 0,
    toJSON: () => ({}),
  } as DOMRect)
}

// Frame with bitmap decode: returns the created bitmaps for close/size asserts.
let bmpSize = { w: 640, h: 480 }
let madeBlobs: Blob[] = []

function stubBitmap(): { width: number; height: number; close: MockTool }[] {
  const made: { width: number; height: number; close: MockTool }[] = []
  ;(globalThis as Record<string, unknown>).createImageBitmap = vi.fn(async (blob: Blob) => {
    madeBlobs.push(blob)
    const bmp = { width: bmpSize.w, height: bmpSize.h, close: vi.fn() }
    made.push(bmp)
    return bmp
  })
  return made
}

describe('browser card ui', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    drawImage.mockReset()
    // Type-aware: a mutated contextType ('2d' → '') must get null, not the mock.
    vi.spyOn(HTMLCanvasElement.prototype, 'getContext').mockImplementation(
      (type: string) => (type === '2d' ? ({ drawImage } as unknown as CanvasRenderingContext2D) : null),
    )
    bmpSize = { w: 640, h: 480 }
    madeBlobs = []
  })
  afterEach(() => {
    while (mounted.length) mounted.pop()?.cleanup()
    document.body.innerHTML = ''
    delete (globalThis as Record<string, unknown>).createImageBitmap
    vi.unstubAllGlobals()
    vi.useRealTimers()
  })

  describe('DOM skeleton', () => {
    it('mounts canvas, hidden input, status overlay in order and opens the browser channel', async () => {
      const m = await mountCard()
      expect(m.ctx.openChannel).toHaveBeenCalledTimes(1)
      expect(m.ctx.openChannel).toHaveBeenCalledWith('browser')
      // childNodes (not children): a leftover text node from a broken innerHTML
      // clear would otherwise stay invisible.
      expect(m.container.childNodes).toHaveLength(3)
      expect(m.canvas.tagName).toBe('CANVAS')
      // h-auto + 412:839 attrs: the idle placeholder keeps the phone aspect
      // instead of the default 300x150 landscape strip (reads as desktop).
      expect(m.canvas.className).toBe('w-full h-auto block bg-black')
      expect(m.canvas.width).toBe(412)
      expect(m.canvas.height).toBe(839)
      expect(m.canvas.style.touchAction).toBe('none')
      expect(m.hidden.tagName).toBe('INPUT')
      expect(m.hidden.className).toBe('absolute opacity-0 w-px h-px p-0 border-0')
      expect(m.hidden.autocapitalize).toBe('off')
      expect(m.hidden.getAttribute('autocomplete')).toBe('off')
      expect(m.status.className).toBe('absolute inset-0 flex items-center justify-center text-[12px] text-t2')
      expect(m.status.textContent).toBe('connecting…')
      expect(m.status.style.display).toBe('')
    })

    it('drops the keyboard toggle into the host header slot with the keyboard icon', async () => {
      const m = await mountCard()
      const slot = m.ctx.slot
      expect(slot.children).toHaveLength(1)
      const btn = slot.children[0] as HTMLButtonElement
      expect(btn.tagName).toBe('BUTTON')
      expect(btn.type).toBe('button')
      expect(btn.title).toBe('Keyboard')
      expect(btn.className).toBe('cursor-pointer px-1 select-none flex items-center text-t2')
      expect(btn.querySelector('span')?.className).toBe('flex items-center')
      const svg = btn.querySelector('svg')
      expect(svg?.getAttribute('width')).toBe('15')
      expect(svg?.getAttribute('height')).toBe('15')
      expect(svg?.getAttribute('viewBox')).toBe('0 0 24 24')
      expect(svg?.getAttribute('fill')).toBe('none')
      expect(svg?.getAttribute('stroke')).toBe('currentColor')
      expect(svg?.getAttribute('stroke-width')).toBe('2')
      expect(svg?.getAttribute('stroke-linecap')).toBe('round')
      expect(svg?.getAttribute('stroke-linejoin')).toBe('round')
      expect(svg?.querySelector('rect')?.getAttribute('rx')).toBe('2')
      expect(svg?.querySelector('rect')?.getAttribute('width')).toBe('20')
      expect(svg?.querySelector('path')?.getAttribute('d')).toBe(
        'M6 8h.01M10 8h.01M14 8h.01M18 8h.01M6 12h.01M10 12h.01M14 12h.01M18 12h.01M8 16h8',
      )
    })

    it('contextmenu on the canvas is prevented', async () => {
      const m = await mountCard()
      const e = new Event('contextmenu', { bubbles: true, cancelable: true })
      const prevent = vi.fn()
      Object.assign(e, { preventDefault: prevent })
      m.canvas.dispatchEvent(e)
      expect(prevent).toHaveBeenCalledTimes(1)
    })
  })

  describe('attach chain', () => {
    it('probes targets, discovers the last page, attaches and goes live', async () => {
      const m = await mountCard()
      await attachLive(m)
      const all = sentMessages(m)
      expect(all.map((msg) => msg.method)).toEqual([
        'Target.getTargets',
        'Target.setDiscoverTargets',
        'Target.attachToTarget',
        'Page.startScreencast',
      ])
      expect(all[0]).toMatchObject({ id: 1, params: {} })
      expect('sessionId' in all[0]).toBe(false)
      expect(all[1]).toMatchObject({ id: 2, params: { discover: true } })
      expect(all[2]).toMatchObject({ id: 3, params: { targetId: 'p1', flatten: true } })
      expect(all[3]).toMatchObject({
        id: 4,
        params: { format: 'jpeg', maxWidth: 1920, quality: 88 },
        sessionId: 's1',
      })
      // everyNthFrame>1 starves static pages: chromium emits exactly ONE
      // compositor frame after startScreencast on an idle page, and odd
      // frames are skipped — the card would stream black forever.
      expect(all[3].params).not.toHaveProperty('everyNthFrame')
      // Honest status: the screencast being ENABLED is not live — the first
      // drawn frame is. Until then the connecting overlay stays up.
      expect(m.status.textContent).toBe('connecting…')
      expect(m.status.style.display).toBe('')
      await feed(m, { method: 'Page.screencastFrame', params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} }, sessionId: 's1' })
      expect(m.status.textContent).toBe('')
    })

    it('picks the LAST page target, not the first', async () => {
      const m = await mountCard()
      await feed(m, {
        id: 1,
        result: { targetInfos: [{ type: 'page', targetId: 'first' }, { type: 'page', targetId: 'second' }] },
      })
      await feed(m, { id: 3, result: { sessionId: 's1' } })
      await feed(m, { id: 4, result: {} })
      expect(sentOfMethod(m, 'Target.attachToTarget')[0]?.params).toEqual({ targetId: 'second', flatten: true })
    })

    it('attaches to pages announced by Target.targetCreated, detaching the old session', async () => {
      const m = await mountCard()
      await attachLive(m)
      // Draw a frame first: the overlay gets hidden while live.
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: { deviceWidth: 412, deviceHeight: 839 } },
        sessionId: 's1',
      })
      expect(m.status.style.display).toBe('none')
      await feed(m, { method: 'Target.targetCreated', params: { targetInfo: { type: 'page', targetId: 'p9' } } })
      // Send ids keep counting: frame ack (5), detach (6), attachToTarget (7),
      // startScreencast (8).
      await feed(m, { id: 7, result: { sessionId: 's2' } })
      await feed(m, { id: 8, result: {} })
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 's1' })
      const screencasts = sentOfMethod(m, 'Page.startScreencast')
      expect(screencasts).toHaveLength(2)
      expect(screencasts[1]?.sessionId).toBe('s2')
      // Streaming continues across a tab switch: no connecting flash, the
      // overlay stays hidden and the next session's frames keep painting.
      expect(m.status.textContent).toBe('')
      expect(m.status.style.display).toBe('none')
    })

    it('ignores targetCreated for non-page targets', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { method: 'Target.targetCreated', params: { targetInfo: { type: 'iframe', targetId: 'f2' } } })
      expect(sentOfMethod(m, 'Target.attachToTarget')).toHaveLength(1)
    })

    it('reports target crashes for the followed target only', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { method: 'Target.targetCrashed', params: { targetId: 'other' } })
      expect(sentOfMethod(m, 'Target.detachFromTarget')).toHaveLength(0)
      await feed(m, { method: 'Target.targetCrashed', params: { targetId: 'p1' } })
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 's1' })
      expect(m.status.textContent).toBe('target crashed; waiting…')
    })

    it('targetDestroyed for the followed tab tears the session down and re-probes for the next page', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { method: 'Target.targetDestroyed', params: { targetId: 'p1' } })
      await tick()
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 's1' })
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
      // The re-probe finds the next page target and goes live on it.
      await feed(m, { id: 6, result: { targetInfos: [{ type: 'page', targetId: 'p2' }] } })
      await feed(m, { id: 8, result: { sessionId: 's3' } })
      await feed(m, { id: 9, result: {} })
      expect(m.status.textContent).toBe('connecting…')
      expect(sentOfMethod(m, 'Target.attachToTarget')[1]?.params).toEqual({ targetId: 'p2', flatten: true })
    })
    it('the first drawn frame flips connecting to live (overlay cleared)', async () => {
      const m = await mountCard()
      await attachLive(m)
      expect(m.status.textContent).toBe('connecting…')
      await feed(m, { method: 'Page.screencastFrame', params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} }, sessionId: 's1' })
      expect(m.status.textContent).toBe('')
    })

    it('targetDestroyed for other targets or without params changes nothing', async () => {
      const m = await mountCard()
      await attachLive(m)
      const before = m.channel.sendLog.length
      await feed(m, { method: 'Target.targetDestroyed', params: { targetId: 'other' } })
      await feed(m, { method: 'Target.targetDestroyed' })
      await feed(m, { method: 'Target.targetDestroyed', params: { targetInfo: { targetId: 'p1' } } })
      expect(m.channel.sendLog.length).toBe(before)
    })

    it('getTargets timeouts at exactly 3000ms with the honest connect-failed status', async () => {
      const m = await mountCard()
      await vi.advanceTimersByTimeAsync(2999)
      await tick()
      expect(m.status.textContent).toBe('connecting…')
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(m.status.textContent).toBe('connect failed: Error: cdp timeout: Target.getTargets')
    })

    it('attachToTarget failure names the CDP error', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      await feed(m, { id: 3, error: { message: 'browser idle' } })
      expect(m.status.textContent).toBe('attach failed: Error: browser idle')
    })

    it('error replies without a message fall back to the generic cdp error', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      await feed(m, { id: 3, error: {} })
      expect(m.status.textContent).toBe('attach failed: Error: cdp error')
    })

    it('startScreencast times out at the default 15s and the failed attach re-arms the 1.5s probe (self-heal)', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      await feed(m, { id: 3, result: { sessionId: 's1' } })
      await vi.advanceTimersByTimeAsync(14999)
      await tick()
      expect(m.status.textContent).toBe('connecting…')
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(m.status.textContent).toBe('attach failed: Error: cdp timeout: Page.startScreencast')
      // THE SELF-HEAL PIN: a transient attach failure (chromium is up, the
      // screencast start just timed out) must re-enter the probe loop instead
      // of stranding the card until the next bridge redial.
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(1)
      await vi.advanceTimersByTimeAsync(1499)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(1)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
    })
  })

  describe('idle probe loop', () => {
    it('no page target keeps probing every 1500ms, re-arming after each dead probe', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'iframe', targetId: 'f1' }] } })
      expect(m.status.textContent).toBe('no page target')
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(1)
      await vi.advanceTimersByTimeAsync(1499)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(1)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
      // The unanswered probe dies at 3000ms, then the loop re-arms at +1500.
      await vi.advanceTimersByTimeAsync(2999)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
      expect(m.status.textContent).toBe('no page target')
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      await vi.advanceTimersByTimeAsync(1500)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(3)
    })

    it('repeated idle frames arm a single probe timer, not one per frame', async () => {
      const m = await mountCard()
      await feed(m, { __status: 'idle' })
      await feed(m, { __status: 'idle' })
      await feed(m, { __status: 'idle' })
      expect(m.status.textContent).toBe('browser not running')
      await vi.advanceTimersByTimeAsync(1500)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2) // initial + one probe
    })

    it('bridge attached clears the probe timer and reruns the attach chain', async () => {
      const m = await mountCard()
      await feed(m, { __status: 'idle' })
      await feed(m, { __status: 'attached' })
      await vi.advanceTimersByTimeAsync(2000)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
      expect(m.status.style.display).toBe('')
      expect(m.status.textContent).toBe('connecting…')
      // Complete the rerun chain to reach the live state.
      await feed(m, { id: 2, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
       await feed(m, { id: 4, result: { sessionId: 's1' } })
       await feed(m, { id: 5, result: {} })
       expect(m.status.textContent).toBe('connecting…')
       await feed(m, { method: 'Page.screencastFrame', params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} }, sessionId: 's1' })
       expect(m.status.textContent).toBe('')
     })

    it('attached while live (chromium restart) tears the stale session down, re-runs the chain and returns to live', async () => {
      // THE RESTART-RECOVERY PIN: the bridge redials after a chromium restart
      // while the card still holds the dead session s1 and never went idle —
      // the attached frame must unconditionally restart the attach chain.
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { __status: 'attached' })
      await feed(m, { id: 5, result: { targetInfos: [{ type: 'page', targetId: 'np1' }] } })
      // ids: detach=7, attachToTarget=8, startScreencast=9.
      await feed(m, { id: 8, result: { sessionId: 's2' } })
      await feed(m, { id: 9, result: {} })
      expect(m.status.textContent).toBe('connecting…')
      await feed(m, { method: 'Page.screencastFrame', params: { sessionId: 's2', data: 'aGVsbG8=', metadata: {} }, sessionId: 's2' })
      expect(m.status.textContent).toBe('')
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
      // The new connection must re-arm target discovery too.
      expect(sentOfMethod(m, 'Target.setDiscoverTargets')).toHaveLength(2)
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 's1' })
      expect(sentOfMethod(m, 'Target.attachToTarget')[1]?.params).toEqual({ targetId: 'np1', flatten: true })
      expect(sentOfMethod(m, 'Page.startScreencast')[1]?.sessionId).toBe('s2')
    })

    it('attached while idle but still holding a session reruns the chain', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { __status: 'idle' })
      expect(m.status.textContent).toBe('browser not running')
      await feed(m, { __status: 'attached' })
      await vi.advanceTimersByTimeAsync(100)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
    })

    it('attached after a failed attach reruns the chain even with no idle marker', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      await feed(m, { id: 3, error: { message: 'nope' } })
      expect(m.status.textContent).toBe('attach failed: Error: nope')
      await feed(m, { __status: 'attached' })
      await vi.advanceTimersByTimeAsync(100)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(2)
    })

    it('an adversarial __status+id frame routes to the control path only', async () => {
      const m = await mountCard()
      await feed(m, { __status: 'idle', id: 1 })
      expect(m.status.textContent).toBe('browser not running')
      // The getTargets request (id 1) was NOT resolved by that frame.
      await vi.advanceTimersByTimeAsync(3000)
      await tick()
      expect(m.status.textContent).toBe('connect failed: Error: cdp timeout: Target.getTargets')
    })

    it('a newer switch request wins: the stale attach is adopted briefly, then detached by the trailing attach', async () => {
      const m = await mountCard()
      // The mount chain picks p1; the attach is in flight when the switch to
      // p9 is announced (trailing-target single-flight records it).
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      await feed(m, { method: 'Target.targetCreated', params: { targetInfo: { type: 'page', targetId: 'p9' } } })
      // The p1 attach reply lands with a stale session: adopted for the
      // interim, then the trailing attach to p9 tears it down.
      await feed(m, { id: 2, result: {} })
      // attach reply: session=old-s → startScreencast issued; its reply
      // settles the attach, then the trailing attach to p9 detaches it.
      await feed(m, { id: 3, result: { sessionId: 'old-s' } })
      await feed(m, { id: 4, result: {} })
      await tick()
      console.log('[dbg1]', JSON.stringify(sentOfMethod(m, 'Target.attachToTarget')), JSON.stringify(sentOfMethod(m, 'Target.detachFromTarget')))
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 'old-s' })
      // The trailing attach to p9 completes and streams.
      await feed(m, { id: 6, result: { sessionId: 'new-s' } })
      await feed(m, { id: 7, result: {} })
      const screencasts = sentOfMethod(m, 'Page.startScreencast')
      expect(screencasts).toHaveLength(2)
      expect(screencasts[1]?.sessionId).toBe('new-s')
      expect(sentOfMethod(m, 'Target.attachToTarget')).toHaveLength(2)
    })

    it('channel close shows the disconnected state', async () => {
      const m = await mountCard()
      await attachLive(m)
      m.channel.closeCb!()
      expect(m.status.textContent).toBe('disconnected — reopen the card')
    })

    it('a connect failure carrying "browser idle" shows the waiting label, not the raw error', async () => {
      const m = await mountCard()
      await feed(m, { id: 1, error: { message: 'browser idle' } })
      expect(m.status.textContent).toBe('browser not running')
    })
  })

  describe('input lane guards', () => {
    it('no input is dispatched before a session exists', async () => {
      const m = await mountCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(0)
    })
  })

  describe('screencast drawing', () => {
    function screencastFrame(m: Mounted, data: string, sessionId = 's1'): Promise<void> {
      return feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId, data, metadata: { deviceWidth: 412, deviceHeight: 839 } },
        sessionId,
      })
    }

    it('acks matching frames and draws the bitmap at full resolution on the canvas', async () => {
      const bitmaps = stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      const acks = sentOfMethod(m, 'Page.screencastFrameAck')
      expect(acks).toHaveLength(1)
      expect(acks[0]).toMatchObject({ params: { sessionId: 's1' }, sessionId: 's1' })
      // THE PIXEL REGRESSION PIN: backing store equals the bitmap 1:1.
      expect(m.canvas.width).toBe(640)
      expect(m.canvas.height).toBe(480)
      expect(drawImage.mock.calls).toHaveLength(1)
      expect(drawImage.mock.calls[0][0]).toBe(bitmaps[0])
      expect(drawImage.mock.calls[0][1]).toBe(0)
      expect(drawImage.mock.calls[0][2]).toBe(0)
      expect(bitmaps[0].close).toHaveBeenCalledTimes(1)
      expect(m.status.style.display).toBe('none')
      expect(m.status.textContent).toBe('')
    })

    it('decodes base64 into jpeg bytes on the wire', async () => {
      stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      expect(madeBlobs).toHaveLength(1)
      expect(madeBlobs[0].size).toBe(5)
      expect(madeBlobs[0].type).toBe('image/jpeg')
      const bytes = new Uint8Array(await madeBlobs[0].arrayBuffer())
      expect(Array.from(bytes)).toEqual([104, 101, 108, 108, 111])
    })

    it('resizes the backing store when a later bitmap changes resolution', async () => {
      const bitmaps = stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      bmpSize = { w: 320, h: 200 }
      await screencastFrame(m, 'aGVsbG8=')
      expect(m.canvas.width).toBe(320)
      expect(m.canvas.height).toBe(200)
      expect(drawImage.mock.calls).toHaveLength(2)
      expect(drawImage.mock.calls[1][0]).toBe(bitmaps[1])
    })

    it('frames from another session are neither acked nor drawn', async () => {
      stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=', 's-other')
      expect(sentOfMethod(m, 'Page.screencastFrameAck')).toHaveLength(0)
      expect(drawImage.mock.calls).toHaveLength(0)
    })

    it('event frames without a session id are dropped', async () => {
      stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      await feed(m, { method: 'Page.screencastFrame', params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} } })
      expect(sentOfMethod(m, 'Page.screencastFrameAck')).toHaveLength(0)
      expect(drawImage.mock.calls).toHaveLength(0)
    })

    it('screencastFrame without params is dropped without a crash', async () => {
      const m = await mountCard()
      await attachLive(m)
      const before = m.channel.sendLog.length
      await expect(feed(m, { method: 'Page.screencastFrame', sessionId: 's1' })).resolves.toBeUndefined()
      expect(m.channel.sendLog.length).toBe(before)
    })

    it('a failing bitmap decode drops the frame silently; the next frame still draws', async () => {
      let fail = true
      ;(globalThis as Record<string, unknown>).createImageBitmap = vi.fn(async (blob: Blob) => {
        madeBlobs.push(blob)
        if (fail) throw new Error('decode failed')
        return { width: 640, height: 480, close: vi.fn() }
      })
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      fail = false
      await screencastFrame(m, 'aGVsbG8=')
      expect(drawImage.mock.calls).toHaveLength(1)
    })

    it('without bitmap support the remote size is still recorded for input mapping', async () => {
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      expect(drawImage.mock.calls).toHaveLength(0)
      stubRect(m, 206, 839)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 1, pointerType: 'mouse', offsetX: 103, offsetY: 400, buttons: 1 }))
      const pressed = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(pressed[0]?.params).toMatchObject({ type: 'mousePressed', x: 206, y: 400 })
    })

    it('bitmap support without Blob still leaves the frame undrawn', async () => {
      ;(globalThis as Record<string, unknown>).createImageBitmap = vi.fn()
      vi.stubGlobal('Blob', undefined)
      const m = await mountCard()
      await attachLive(m)
      await screencastFrame(m, 'aGVsbG8=')
      expect(drawImage.mock.calls).toHaveLength(0)
    })

    it('an unavailable 2d context stops drawing without crashing the frame pump', async () => {
      // Synchronous-then bitmap: if the !c2d guard is removed, the draw
      // callback throws during this feed instead of silently never running.
      ;(globalThis as Record<string, unknown>).createImageBitmap = vi.fn(() => ({
        then: (cb: (bmp: { width: number; height: number; close: () => void }) => void) =>
          cb({ width: 10, height: 10, close: () => {} }),
      }))
      const getContext = HTMLCanvasElement.prototype.getContext as unknown as { mockImplementation: (f: () => null) => void }
      getContext.mockImplementation(() => null)
      const m = await mountCard()
      await attachLive(m)
      await expect(feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: { deviceWidth: 412, deviceHeight: 839 } },
        sessionId: 's1',
      })).resolves.toBeUndefined()
      expect(drawImage.mock.calls).toHaveLength(0)
    })

    it('method events without a registered handler are dropped silently', async () => {
      const m = await mountCard()
      await attachLive(m)
      const sendsBefore = m.channel.sendLog.length
      await feed(m, { method: 'Page.frameStartedLoading', params: {} })
      expect(m.channel.sendLog.length).toBe(sendsBefore)
    })

    it('tracks the backing store 1:1 per axis: same-width frames resize height only', async () => {
      stubBitmap()
      const m = await mountCard()
      await attachLive(m)
      // Spy the canvas dimension accessors to observe every assignment.
      let wVal = m.canvas.width
      let hVal = m.canvas.height
      const widthSets: number[] = []
      const heightSets: number[] = []
      Object.defineProperty(m.canvas, 'width', {
        configurable: true,
        get: () => wVal,
        set: (v: number) => {
          wVal = v
          widthSets.push(v)
        },
      })
      Object.defineProperty(m.canvas, 'height', {
        configurable: true,
        get: () => hVal,
        set: (v: number) => {
          hVal = v
          heightSets.push(v)
        },
      })
      // Frame A: new resolution → both dimensions assigned.
      await screencastFrame(m, 'aGVsbG8=')
      expect(widthSets).toEqual([640])
      expect(heightSets).toEqual([480])
      // Frame B: guard passes via the height diff → both dimensions assigned.
      bmpSize = { w: 640, h: 200 }
      await screencastFrame(m, 'aGVsbG8=')
      expect(widthSets).toEqual([640, 640])
      expect(heightSets).toEqual([480, 200])
      // Frame C: identical resolution → guard false → nothing reassigned.
      await screencastFrame(m, 'aGVsbG8=')
      expect(widthSets).toEqual([640, 640])
      expect(heightSets).toEqual([480, 200])
      // Frame D: width changed, height same → guard passes via width only;
      // the body assigns both dimensions again.
      bmpSize = { w: 320, h: 200 }
      await screencastFrame(m, 'aGVsbG8=')
      expect(widthSets).toEqual([640, 640, 320])
      expect(heightSets).toEqual([480, 200, 200])
    })

    it('metadata keys absent from a later frame keep the previously recorded size', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: { deviceWidth: 200, deviceHeight: 100 } },
        sessionId: 's1',
      })
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params).toMatchObject({ x: 100, y: 50 })
      // A frame without metadata keys must not reset the recorded sizes.
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} },
        sessionId: 's1',
      })
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')[1]?.params).toMatchObject({ x: 100, y: 50 })
    })

    it('never-recorded sizes leave both axes at identity', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} },
        sessionId: 's1',
      })
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params).toMatchObject({ x: 50, y: 25 })
    })
  })

  describe('input mapping', () => {
    async function liveCard(): Promise<Mounted> {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: { deviceWidth: 200, deviceHeight: 100 } },
        sessionId: 's1',
      })
      return m
    }

    it('mouse press maps screen css px through the remote ratio', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      const down = pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 })
      m.canvas.dispatchEvent(down)
      expect(down.defaultPrevented).toBe(true)
      const pressed = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(pressed[0]?.sessionId).toBe('s1')
      expect(pressed[0]?.params).toEqual({ type: 'mousePressed', x: 100, y: 50, button: 'left', buttons: 1, clickCount: 1 })
    })

    it('mouse release and move carry their own shapes', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 10, offsetY: 10, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 7, pointerType: 'mouse', offsetX: 33, offsetY: 11, buttons: 0 }))
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 7, pointerType: 'mouse', offsetX: 33, offsetY: 11 }))
      const msgs = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(msgs[1]?.params).toEqual({ type: 'mouseMoved', x: 66, y: 22, button: 'none', buttons: 0 })
      expect(msgs[2]?.params).toEqual({ type: 'mouseReleased', x: 66, y: 22, button: 'left', buttons: 0, clickCount: 1 })
    })

    it('pointers of other ids are ignored', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 10, offsetY: 10, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 8, pointerType: 'mouse', offsetX: 50, offsetY: 50, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 8, pointerType: 'mouse', offsetX: 50, offsetY: 50 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(1) // only the press
    })

    it('touch tap under the slop lifts into press+release at the touch point', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 9, pointerType: 'touch', offsetX: 30, offsetY: 20 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 9, pointerType: 'touch', offsetX: 32, offsetY: 21 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(0)
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 9, pointerType: 'touch', offsetX: 30, offsetY: 20 }))
      const msgs = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(msgs[0]?.params).toEqual({ type: 'mousePressed', x: 60, y: 40, button: 'left', buttons: 1, clickCount: 1 })
      expect(msgs[1]?.params).toEqual({ type: 'mouseReleased', x: 60, y: 40, button: 'left', buttons: 0, clickCount: 1 })
    })

    it('crossing the 8px slop switches to an inverted wheel stream', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 9, pointerType: 'touch', offsetX: 10, offsetY: 10 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 9, pointerType: 'touch', offsetX: 18, offsetY: 10 }))
      const msgs = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(msgs).toHaveLength(1)
      expect(msgs[0]?.params).toEqual({ type: 'mouseWheel', x: 36, y: 20, button: 'none', deltaX: -16, deltaY: 0 })
    })

    it('drag deltas invert like a touchpad and rescale per move', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 9, pointerType: 'touch', offsetX: 10, offsetY: 10 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 9, pointerType: 'touch', offsetX: 25, offsetY: 12 }))
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 9, pointerType: 'touch', offsetX: 25, offsetY: 20 }))
      // A small move while dragging must keep wheeling (drag flag latched).
      m.canvas.dispatchEvent(pointerEvent('pointermove', { pointerId: 9, pointerType: 'touch', offsetX: 26, offsetY: 22 }))
      const msgs = sentOfMethod(m, 'Input.dispatchMouseEvent')
      expect(msgs[0]?.params).toEqual({ type: 'mouseWheel', x: 50, y: 24, button: 'none', deltaX: -30, deltaY: -4 })
      expect(msgs[1]?.params).toEqual({ type: 'mouseWheel', x: 50, y: 40, button: 'none', deltaX: 0, deltaY: -16 })
      expect(msgs[2]?.params).toEqual({ type: 'mouseWheel', x: 52, y: 44, button: 'none', deltaX: -2, deltaY: -4 })
      // Lifting after a drag is NOT a tap.
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 9, pointerType: 'touch', offsetX: 26, offsetY: 22 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(3)
    })

    it('physical wheel events forward deltas and are prevented from scrolling the card', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      const e = pointerEvent('wheel', { deltaX: 33, deltaY: -7, offsetX: 40, offsetY: 60 })
      const prevent = vi.fn()
      Object.assign(e, { preventDefault: prevent })
      m.canvas.dispatchEvent(e)
      expect(prevent).toHaveBeenCalledTimes(1)
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params).toEqual({
        type: 'mouseWheel',
        x: 80,
        y: 120,
        button: 'none',
        deltaX: 33,
        deltaY: -7,
      })
    })

    it('pointercancel clears the active pointer; a cancel of another id does not', async () => {
      const m = await liveCard()
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 10, offsetY: 10, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointercancel', { pointerId: 7 }))
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 7, pointerType: 'mouse', offsetX: 10, offsetY: 10 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(1)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 8, pointerType: 'mouse', offsetX: 10, offsetY: 10, buttons: 1 }))
      m.canvas.dispatchEvent(pointerEvent('pointercancel', { pointerId: 99 }))
      m.canvas.dispatchEvent(pointerEvent('pointerup', { pointerId: 8, pointerType: 'mouse', offsetX: 10, offsetY: 10 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')).toHaveLength(3) // press, press, release
    })

    it('a zero-sized rect degrades the ratio to identity instead of NaN', async () => {
      const m = await liveCard()
      stubRect(m, 0, 0)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      expect(sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params).toMatchObject({ x: 50, y: 25 })
    })

    it('missing deviceWidth metadata keeps the axis at identity', async () => {
      const m = await mountCard()
      await attachLive(m)
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: { deviceHeight: 100 } },
        sessionId: 's1',
      })
      stubRect(m, 100, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 50, offsetY: 25, buttons: 1 }))
      const params = sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params
      expect(params).toMatchObject({ x: 50, y: 50 }) // y scaled 2x, x identity
    })

    it('rounds mapped coordinates to page pixels', async () => {
      const m = await liveCard()
      stubRect(m, 97, 50)
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 7, pointerType: 'mouse', offsetX: 33, offsetY: 25, buttons: 1 }))
      const params = sentOfMethod(m, 'Input.dispatchMouseEvent')[0]?.params
      expect(params?.x).toBe(68) // 33 * 200/97 = 68.04…; exact-round vs raw distinguishable
    })
  })

  describe('keyboard input', () => {
    async function liveCard(): Promise<Mounted> {
      const m = await mountCard()
      await attachLive(m)
      return m
    }

    function keydown(m: Mounted, key: string): Event {
      const e = new Event('keydown', { bubbles: true, cancelable: true })
      Object.assign(e, { key })
      m.hidden.dispatchEvent(e)
      return e
    }

    it('the keyboard toggle focuses and blurs the hidden input with its highlight', async () => {
      const m = await liveCard()
      const btn = m.ctx.slot.children[0] as HTMLButtonElement
      btn.click()
      expect(document.activeElement).toBe(m.hidden)
      expect(btn.classList.contains('text-t1')).toBe(true)
      m.hidden.value = 'typed stuff'
      btn.click()
      expect(document.activeElement).not.toBe(m.hidden)
      expect(btn.classList.contains('text-t1')).toBe(false)
      expect(m.hidden.value).toBe('')
    })

    it('soft-key input forwards the exact delta as insertText and empties the field', async () => {
      const m = await liveCard()
      m.hidden.value = 'stale'
      const e = new Event('input', { bubbles: true })
      Object.assign(e, { data: 'ab', inputType: 'insertText' })
      m.hidden.dispatchEvent(e)
      expect(sentOfMethod(m, 'Input.insertText')[0]?.params).toEqual({ text: 'ab' })
      expect(m.hidden.value).toBe('')
    })

    it('empty data inserts nothing', async () => {
      const m = await liveCard()
      const e = new Event('input', { bubbles: true })
      Object.assign(e, { data: '', inputType: 'insertText' })
      m.hidden.dispatchEvent(e)
      expect(sentMessages(m).filter((msg) => msg.method?.startsWith('Input.'))).toHaveLength(0)
    })

    it('non-string data inserts nothing (string type guard)', async () => {
      const m = await liveCard()
      const e = new Event('input', { bubbles: true })
      Object.assign(e, { data: 123, inputType: 'insertText' })
      m.hidden.dispatchEvent(e)
      expect(sentMessages(m).filter((msg) => msg.method?.startsWith('Input.'))).toHaveLength(0)
    })

    it('soft-keyboard backspace arrives as deleteContentBackward and becomes a key pair', async () => {
      const m = await liveCard()
      const e = new Event('input', { bubbles: true })
      Object.assign(e, { inputType: 'deleteContentBackward' })
      m.hidden.dispatchEvent(e)
      const keys = sentOfMethod(m, 'Input.dispatchKeyEvent')
      expect(keys).toHaveLength(2)
      expect(keys[0]?.params).toEqual({ type: 'keyDown', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 })
      expect(keys[1]?.params).toEqual({ type: 'keyUp', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 })
    })

    it('Enter dispatches a text-bearing pair and swallows the local newline', async () => {
      const m = await liveCard()
      const e = keydown(m, 'Enter')
      expect(e.defaultPrevented).toBe(true)
      const keys = sentOfMethod(m, 'Input.dispatchKeyEvent')
      expect(keys).toHaveLength(2)
      expect(keys[0]?.params).toEqual({
        type: 'keyDown',
        key: 'Enter',
        code: 'Enter',
        windowsVirtualKeyCode: 13,
        text: '\r',
      })
      expect(keys[1]?.params).toEqual({ type: 'keyUp', key: 'Enter', code: 'Enter', windowsVirtualKeyCode: 13 })
    })

    it('Tab dispatches a pair with its own virtual key code', async () => {
      const m = await liveCard()
      const e = keydown(m, 'Tab')
      expect(e.defaultPrevented).toBe(true)
      const keys = sentOfMethod(m, 'Input.dispatchKeyEvent')
      expect(keys[0]?.params).toEqual({ type: 'keyDown', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
      expect(keys[1]?.params).toEqual({ type: 'keyUp', key: 'Tab', code: 'Tab', windowsVirtualKeyCode: 9 })
    })

    it('physical Backspace on the empty field dispatches a pair; non-empty passes through', async () => {
      const m = await liveCard()
      const e1 = keydown(m, 'Backspace')
      expect(e1.defaultPrevented).toBe(true)
      const keys = sentOfMethod(m, 'Input.dispatchKeyEvent')
      expect(keys).toHaveLength(2)
      expect(keys[0]?.params).toEqual({ type: 'keyDown', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 })
      expect(keys[1]?.params).toEqual({ type: 'keyUp', key: 'Backspace', code: 'Backspace', windowsVirtualKeyCode: 8 })
      m.hidden.value = 'x'
      const e2 = keydown(m, 'Backspace')
      expect(e2.defaultPrevented).toBe(false)
      expect(sentOfMethod(m, 'Input.dispatchKeyEvent')).toHaveLength(2) // unchanged
    })

    it('other keys are left to the input event path', async () => {
      const m = await liveCard()
      keydown(m, 'a')
      expect(sentOfMethod(m, 'Input.dispatchKeyEvent')).toHaveLength(0)
    })
  })

  describe('feed robustness and lifecycle', () => {
    it('non-JSON frames and unknown-id replies change nothing', async () => {
      const m = await mountCard()
      const before = m.channel.sendLog.length
      await feed(m, 'not json at all')
      await feed(m, { id: 999, result: {} })
      expect(m.channel.sendLog.length).toBe(before)
      expect(m.status.textContent).toBe('connecting…')
    })

    it('cleanup detaches the session, closes the channel and stops all timers', async () => {
      const m = await mountCard()
      await attachLive(m)
      m.cleanup()
      const sendsBefore = m.channel.sendLog.length // includes the detach
      expect(m.channel.close).toHaveBeenCalledTimes(1)
      expect(sentOfMethod(m, 'Target.detachFromTarget')[0]?.params).toEqual({ sessionId: 's1' })
      await vi.advanceTimersByTimeAsync(5000)
      await tick()
      expect(sentOfMethod(m, 'Target.getTargets')).toHaveLength(1) // no probe fired
      await feed(m, { __status: 'idle' })
      await feed(m, {
        method: 'Page.screencastFrame',
        params: { sessionId: 's1', data: 'aGVsbG8=', metadata: {} },
        sessionId: 's1',
      })
      m.canvas.dispatchEvent(pointerEvent('pointerdown', { pointerId: 1, pointerType: 'mouse', offsetX: 5, offsetY: 5, buttons: 1 }))
      expect(m.channel.sendLog.length).toBe(sendsBefore) // input lane dead
      // Even a bridge attach broadcast after cleanup must not touch the card.
      await feed(m, { __status: 'attached' })
      expect(m.status.textContent).toBe('connecting…')
      expect(m.status.style.display).toBe('')
    })

    it('cleanup drops pending replies so a late getTargets result cannot resurrect the chain', async () => {
      const m = await mountCard()
      const sendsBefore = m.channel.sendLog.length // the getTargets request
      m.cleanup()
      await feed(m, { id: 1, result: { targetInfos: [{ type: 'page', targetId: 'p1' }] } })
      expect(m.channel.sendLog.length).toBe(sendsBefore)
    })

    it('targetCreated after cleanup attaches nothing', async () => {
      const m = await mountCard()
      await attachLive(m)
      m.cleanup()
      const sendsBefore = m.channel.sendLog.length
      await feed(m, { method: 'Target.targetCreated', params: { targetInfo: { type: 'page', targetId: 'p9' } } })
      expect(m.channel.sendLog.length).toBe(sendsBefore)
    })

    it('a late channel close after cleanup leaves the status alone', async () => {
      const m = await mountCard()
      await attachLive(m)
      m.cleanup()
      m.channel.closeCb!()
      expect(m.status.textContent).toBe('connecting…')
    })

    it('cleanup with a pending request rejects it internally without touching the card', async () => {
      const m = await mountCard()
      const statusBefore = m.status.textContent
      m.cleanup()
      await vi.advanceTimersByTimeAsync(5000)
      await tick()
      expect(m.status.textContent).toBe(statusBefore)
      expect(m.channel.close).toHaveBeenCalledTimes(1)
    })
  })

  describe('CDPClient lifecycle (direct)', () => {
    interface DirectClient {
      send: (method: string, params?: Record<string, unknown>, sessionId?: string) => number
      request: (method: string, params?: Record<string, unknown>, sessionId?: string, timeoutMs?: number) => Promise<unknown>
      on: (method: string, cb: (params: unknown, sessionId: string) => void) => () => void
      feed: (text: string) => void
      dispose: () => void
      onStatus: ((s: string) => void) | null
      pending: Map<number, unknown>
      handlers: Map<string, unknown>
    }

    async function newClient(): Promise<DirectClient> {
      vi.resetModules()
      const mod = await import('../../plugins-browser/ui.js')
      const CDPClient = mod.CDPClient as new (send: (b: Uint8Array) => void) => DirectClient
      return new CDPClient(() => {})
    }

    it('dispose rejects pending requests with the exact message and clears their timers', async () => {
      const cdp = await newClient()
      const p = cdp.request('Target.getTargets', {}, undefined, 3000)
      expect(vi.getTimerCount()).toBe(1)
      cdp.dispose()
      expect(vi.getTimerCount()).toBe(0)
      await expect(p).rejects.toThrow('cdp client disposed')
      expect(cdp.pending.size).toBe(0)
      expect(cdp.handlers.size).toBe(0)
    })

    it('dispose clears every pending request, not just the first', async () => {
      const cdp = await newClient()
      const ps = [cdp.request('A'), cdp.request('B'), cdp.request('C')]
      cdp.dispose()
      for (const p of ps) {
        await expect(p).rejects.toThrow('cdp client disposed')
      }
      expect(cdp.pending.size).toBe(0)
    })

    it('feed after dispose dispatches nothing: events, replies and status frames all early-return', async () => {
      const cdp = await newClient()
      const onStatus = vi.fn()
      const onEvt = vi.fn()
      cdp.onStatus = onStatus
      const off = cdp.on('Target.targetCreated', onEvt)
      const p = cdp.request('X')
      cdp.dispose()
      off()
      cdp.feed('{"__status":"attached"}')
      cdp.feed('{"method":"Target.targetCreated","params":{}}')
      cdp.feed('{"id":1,"result":{}}')
      expect(onStatus).not.toHaveBeenCalled()
      expect(onEvt).not.toHaveBeenCalled()
      await expect(p).rejects.toThrow('cdp client disposed')
    })
  })
})
