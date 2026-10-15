import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import {
  formatShareStamp,
  buildShareCard,
  shareFileName,
  captureShareCard,
  getShareBridge,
  shareMessage,
  type ShareRequest,
} from './share_card'

const domToPngMock = vi.hoisted(() => vi.fn())

vi.mock('modern-screenshot', () => ({
  domToPng: domToPngMock,
}))

// rAF is faked together with the timers by vitest 4 — driving the double-rAF
// inside captureShareCard requires advancing exactly two frames, then letting
// microtasks settle so the captured promise resolves.
async function driveDoubleRaf(): Promise<void> {
  await vi.advanceTimersByTimeAsync(16)
  await vi.advanceTimersByTimeAsync(16)
}

const startedAt = new Date(2026, 9, 15, 10, 50).getTime()

function shareBridgeStub(): { shareImage: ReturnType<typeof vi.fn> } {
  return { shareImage: vi.fn() }
}

describe('formatShareStamp', () => {
  it('formats local-timezone "YYYY-MM-DD HH:mm"', () => {
    expect(formatShareStamp(startedAt)).toBe('2026-10-15 10:50')
  })

  it('zero-pads single-digit month/day/hour/minute', () => {
    expect(formatShareStamp(new Date(2026, 0, 3, 8, 5).getTime())).toBe('2026-01-03 08:05')
  })
})

describe('buildShareCard', () => {
  it('assistant card runs the real markdown pipeline into .md-body.md-text', () => {
    const req: ShareRequest = {
      text: '# Title\n\n**bold** text\n\n| H1 | H2 |\n|---|---|\n| a | b |',
      role: 'assistant',
      startedAt,
    }
    const card = buildShareCard(req)
    expect(card.className).toBe('share-card')
    const title = card.querySelector('.share-card-title')
    expect(title?.textContent).toBe('GBot')
    const stamp = card.querySelector('.share-card-stamp')
    expect(stamp?.textContent).toBe('2026-10-15 10:50')
    const body = card.querySelector('.md-body.md-text')
    expect(body?.querySelector('strong')?.textContent).toBe('bold')
    expect(body?.querySelector('.table-wrap')).not.toBeNull()
    expect(body?.querySelector('h1')?.textContent).toBe('Title')
    // The card is a content snapshot: no chrome beyond header + body.
    expect(card.querySelector('.share-card-footer')).toBeNull()
    expect(card.querySelector('svg')).toBeNull()
    expect(card.querySelector('img')).toBeNull()
  })

  it('user card keeps text literal (no markdown pass)', () => {
    const req: ShareRequest = {
      text: '*not bold*\nline two',
      role: 'user',
      startedAt,
    }
    const card = buildShareCard(req)
    const body = card.querySelector('.share-card-body')
    expect(body?.textContent).toBe('*not bold*\nline two')
    expect(body?.querySelector('strong')).toBeNull()
    expect(body?.className).toContain('whitespace-pre-wrap')
  })
})

describe('shareFileName', () => {
  it('encodes local startedAt as gbot-share-YYYY-MM-DD-HHmmss.png', () => {
    expect(shareFileName(startedAt)).toBe('gbot-share-2026-10-15-105000.png')
  })
})

describe('captureShareCard', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    domToPngMock.mockReset()
    domToPngMock.mockResolvedValue('data:image/png;base64,QUJD')
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
    vi.unstubAllGlobals()
  })

  function setViewportWidth(px: number): void {
    Object.defineProperty(window, 'innerWidth', { value: px, configurable: true })
  }

  it('mounts an offscreen host, captures the card at scale 2, removes the host', async () => {
    setViewportWidth(390)
    const card = buildShareCard({ text: 'hello', role: 'assistant', startedAt })
    const p = captureShareCard(card)
    await driveDoubleRaf()
    const result = await p
    expect(result).toBe('data:image/png;base64,QUJD')
    expect(domToPngMock).toHaveBeenCalledTimes(1)
    expect(domToPngMock).toHaveBeenCalledWith(card, { scale: 2 })
    expect(document.querySelector('.share-card-offscreen-host')).toBeNull()
  })
  it('clamps the raster scale to the device DPR within [2, 3]', async () => {
    setViewportWidth(390)
    // jsdom has no devicePixelRatio → falls back to 2 (the first case pins
    // the fallback); 4 must NOT pass through — 4x canvas memory is the OOM
    // edge the clamp exists for.
    for (const [dpr, expected] of [[undefined, 2], [1, 2], [3, 3], [4, 3]] as const) {
      if (dpr === undefined) {
        Object.defineProperty(window, 'devicePixelRatio', { value: undefined, configurable: true })
      } else {
        vi.stubGlobal('devicePixelRatio', dpr)
      }
      const card = buildShareCard({ text: 'x', role: 'assistant', startedAt })
      const p = captureShareCard(card)
      await driveDoubleRaf()
      await p
      expect(domToPngMock).toHaveBeenLastCalledWith(card, { scale: expected })
      vi.unstubAllGlobals()
    }
  })

  it('sizes the card to min(innerWidth - 32, 480)px', async () => {
    setViewportWidth(390)
    const phone = buildShareCard({ text: 'a', role: 'assistant', startedAt })
    const pp = captureShareCard(phone)
    await driveDoubleRaf()
    await pp
    expect(phone.style.width).toBe('358px')

    setViewportWidth(2000)
    const desktop = buildShareCard({ text: 'b', role: 'assistant', startedAt })
    const pd = captureShareCard(desktop)
    await driveDoubleRaf()
    await pd
    expect(desktop.style.width).toBe('480px')
  })

  it('rejects when domToPng rejects and still cleans up the host', async () => {
    // Per-call rejection, not mockRejectedValue: an eagerly-created rejected
    // promise would go unhandled until the fake-timer drive consumes it.
    domToPngMock.mockImplementation(async () => {
      throw new Error('boom')
    })
    const card = buildShareCard({ text: 'hello', role: 'assistant', startedAt })
    const p = captureShareCard(card)
    // p rejects inside the fake-timer flush, before the rejects() assertion
    // below attaches — mark it handled or vitest logs an unhandled rejection.
    p.catch(() => {})
    await driveDoubleRaf()
    await expect(p).rejects.toThrow('boom')
    expect(document.querySelector('.share-card-offscreen-host')).toBeNull()
  })
})

describe('getShareBridge', () => {
  afterEach(() => {
    delete (window as unknown as Record<string, unknown>).GBotShare
  })

  it('returns null without a window stub', () => {
    expect(getShareBridge()).toBeNull()
  })

  it('returns the window.GBotShare object when shareImage is a function', () => {
    const stub = shareBridgeStub()
    ;(window as unknown as Record<string, unknown>).GBotShare = stub
    expect(getShareBridge()).toBe(stub)
  })

  it('returns null when shareImage is not a function', () => {
    ;(window as unknown as Record<string, unknown>).GBotShare = { shareImage: 42 }
    expect(getShareBridge()).toBeNull()
  })
})

describe('shareMessage', () => {
  beforeEach(() => {
    vi.useFakeTimers()
    domToPngMock.mockReset()
    domToPngMock.mockResolvedValue('data:image/png;base64,QUJD')
  })

  afterEach(() => {
    vi.useRealTimers()
    document.body.innerHTML = ''
    delete (window as unknown as Record<string, unknown>).GBotShare
    vi.unstubAllGlobals()
  })

  it('captures and hands the dataURL plus file name to the bridge exactly once', async () => {
    const stub = shareBridgeStub()
    ;(window as unknown as Record<string, unknown>).GBotShare = stub
    const p = shareMessage({ text: 'hello **world**', role: 'assistant', startedAt })
    await driveDoubleRaf()
    await expect(p).resolves.toBe('opened')
    expect(stub.shareImage).toHaveBeenCalledTimes(1)
    expect(stub.shareImage.mock.calls[0][0]).toBe('data:image/png;base64,QUJD')
    expect(stub.shareImage.mock.calls[0][1]).toMatch(/^gbot-share-\d{4}-\d{2}-\d{2}-\d{6}\.png$/)
  })

  it('returns no-bridge and never captures when the bridge is absent', async () => {
    const p = shareMessage({ text: 'hello', role: 'assistant', startedAt })
    await expect(p).resolves.toBe('no-bridge')
    expect(domToPngMock).toHaveBeenCalledTimes(0)
  })

  it('propagates capture failure and never calls the bridge', async () => {
    const stub = shareBridgeStub()
    ;(window as unknown as Record<string, unknown>).GBotShare = stub
    domToPngMock.mockImplementation(async () => {
      throw new Error('boom')
    })
    const p = shareMessage({ text: 'hello', role: 'assistant', startedAt })
    p.catch(() => {})
    await driveDoubleRaf()
    await expect(p).rejects.toThrow('boom')
    expect(stub.shareImage).toHaveBeenCalledTimes(0)
    expect(document.querySelector('.share-card-offscreen-host')).toBeNull()
  })
})
