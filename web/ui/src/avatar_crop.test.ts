import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, it, expect, beforeAll, afterAll, beforeEach, vi } from 'vitest'
import { AVATAR_TOO_LARGE, AVATAR_SIDE } from './avatar'
import { t } from './i18n'
import { openAvatarCropper } from './avatar_crop'

// The cropperjs module is mocked away entirely: the custom elements stay
// HTMLUnknownElement and every test attaches $toCanvas / $ready by hand, so
// the only substituted part is the cropper library itself.
vi.mock('cropperjs', () => ({}))

// jsdom has no URL.createObjectURL — same save/restore stub as chat.test.ts,
// but revoke is a recording spy because teardown must revoke exactly once.
const hadCreateObjectURL = 'createObjectURL' in URL
const origCreateObjectURL = (URL as { createObjectURL?: (b: Blob) => string }).createObjectURL
const hadRevokeObjectURL = 'revokeObjectURL' in URL
const origRevokeObjectURL = (URL as { revokeObjectURL?: (u: string) => void }).revokeObjectURL
let blobSeq = 0
const createObjectURL = vi.fn(() => `blob:mock-${++blobSeq}`)
const revokeObjectURL = vi.fn()

beforeAll(() => {
  Object.defineProperty(URL, 'createObjectURL', {
    value: createObjectURL,
    configurable: true,
    writable: true,
  })
  Object.defineProperty(URL, 'revokeObjectURL', {
    value: revokeObjectURL,
    configurable: true,
    writable: true,
  })
})
afterAll(() => {
  if (hadCreateObjectURL) {
    Object.defineProperty(URL, 'createObjectURL', {
      value: origCreateObjectURL,
      configurable: true,
      writable: true,
    })
  } else {
    delete (URL as { createObjectURL?: unknown }).createObjectURL
  }
  if (hadRevokeObjectURL) {
    Object.defineProperty(URL, 'revokeObjectURL', {
      value: origRevokeObjectURL,
      configurable: true,
      writable: true,
    })
  } else {
    delete (URL as { revokeObjectURL?: unknown }).revokeObjectURL
  }
})

beforeEach(() => {
  document.body.innerHTML = ''
  vi.clearAllMocks()
})

interface SelectionStub {
  $toCanvas: ReturnType<typeof vi.fn>
  width?: number
  height?: number
  x?: number
  y?: number
}
interface ImageStub {
  $ready: ReturnType<typeof vi.fn>
}

function makeFile(): File {
  return new File([new Uint8Array([1])], 'a.png', { type: 'image/png' })
}

// Same structural fake as avatar.test.ts: width/height/toBlob, no context.
function fakeCanvasEl(blobSizes: number[]) {
  let next = 0
  return {
    width: AVATAR_SIDE,
    height: AVATAR_SIDE,
    toBlob: (cb: (b: Blob | null) => void, type?: string) => {
      const size = blobSizes[next]
      next++
      cb(new Blob([new ArrayBuffer(size)], { type: type ?? '' }))
    },
  } as unknown as HTMLCanvasElement
}

const flush = () => new Promise<void>((r) => setTimeout(r))

// The $ready hook fires one microtask after open (the impl defers it), so
// stubs attach synchronously right after openAvatarCropper returns and the
// flush lets the hook (and any rejection) run.
function openRaw(file?: File) {
  const p = openAvatarCropper(file ?? makeFile())
  const image = document.querySelector('cropper-image') as unknown as ImageStub
  const selection = document.querySelector('cropper-selection') as unknown as SelectionStub
  return { p, image, selection }
}

async function openOverlay(file?: File) {
  const ctx = openRaw(file)
  ctx.image.$ready = vi.fn(() => Promise.resolve(document.createElement('img')))
  ctx.selection.$toCanvas = vi.fn(() => Promise.resolve(fakeCanvasEl([8 * 1024])))
  await flush()
  return ctx
}

function clickConfirm(): void {
  ;(document.querySelector('[data-avatar-crop-confirm]') as HTMLElement).click()
}

function pressEscape(): void {
  document.dispatchEvent(new KeyboardEvent('keydown', { key: 'Escape' }))
}

const overlayGone = () => {
  expect(document.querySelector('[data-avatar-crop-mask]')).toBeNull()
  expect(document.querySelector('[data-avatar-crop]')).toBeNull()
}

describe('openAvatarCropper', () => {
  it('mounts the overlay with the cropper element matrix', async () => {
    await openOverlay()
    expect(document.querySelector('[data-avatar-crop]')).toBeTruthy()

    const canvases = document.querySelectorAll('cropper-canvas')
    expect(canvases.length).toBe(1)
    expect(canvases[0].getAttribute('theme-color')).toBe('var(--color-blue)')
    expect(canvases[0].className).toContain('aspect-square')

    const images = document.querySelectorAll('cropper-image')
    expect(images.length).toBe(1)
    expect(images[0].getAttribute('src')).toBe(createObjectURL.mock.results[0].value)
    expect(images[0].hasAttribute('translatable')).toBe(true)
    expect(images[0].hasAttribute('scalable')).toBe(true)
    // v2 naming trap: `scalable` is pinch/wheel zoom; v1's "flip" maps to
    // skewable. `rotatable` gates BOTH the two-finger gesture and the
    // programmatic $rotate — on, or the rotate button is a no-op. skewable
    // (flip) stays off.
    expect(images[0].hasAttribute('rotatable')).toBe(true)
    expect(images[0].hasAttribute('skewable')).toBe(false)

    const handles = document.querySelectorAll('cropper-handle')
    expect(handles.length).toBe(2)
    const parents = Array.from(handles).map((h) => h.parentElement?.tagName.toLowerCase())
    // One move handle on the canvas (letterbox drags), one inside the
    // selection (the selection covers its canvas area, so in-viewport drags
    // need the inner handle).
    expect(parents).toEqual(['cropper-canvas', 'cropper-selection'])
    for (const h of Array.from(handles)) {
      expect(h.getAttribute('action')).toBe('move')
      expect(h.hasAttribute('plain')).toBe(true)
    }

    const selections = document.querySelectorAll('cropper-selection')
    expect(selections.length).toBe(1)
    expect(selections[0].getAttribute('aspect-ratio')).toBe('1')
    expect(selections[0].getAttribute('initial-coverage')).toBe('1')
    // No outlined square: the rounded-square frame overlay below is the one
    // visual — the stock outline would double it.
    expect(selections[0].hasAttribute('outlined')).toBe(false)
    for (const absent of ['movable', 'resizable', 'dynamic', 'zoomable', 'keyboard']) {
      expect(selections[0].hasAttribute(absent)).toBe(false)
    }
    // Rounded-square frame: chat thumbnails (primary consumer) are
    // rounded-md squares — the viewport shows that truth, not a circle.
    const frame = document.querySelector('[data-avatar-crop-frame]') as HTMLElement
    expect(frame).toBeTruthy()
    expect(frame.className).toContain('rounded-lg')
    expect(frame.className).toContain('pointer-events-none')
    expect(frame.style.boxShadow).toContain('9999px')
    // 90°-step rotate button lives in the actions row.
    const rotate = document.querySelector('[data-avatar-crop-rotate]') as HTMLElement
    expect(rotate).toBeTruthy()
    expect(rotate.getAttribute('aria-label')).toBe(t('avatarCropRotate'))
  })

  it('frame geometry syncs from the selection once the image settles', async () => {
    const ctx = openRaw()
    // Landscape-ish selection: min(w,h) must win for the square frame and
    // the x/y offset must land verbatim — the frame IS the crop truth.
    ctx.selection.width = 300
    ctx.selection.height = 260
    ctx.selection.x = 4
    ctx.selection.y = 6
    ctx.image.$ready = vi.fn(() => Promise.resolve(document.createElement('img')))
    await flush()
    const frame = document.querySelector('[data-avatar-crop-frame]') as HTMLElement
    expect(frame.style.width).toBe('260px')
    expect(frame.style.height).toBe('260px')
    expect(frame.style.left).toBe('4px')
    expect(frame.style.top).toBe('6px')
  })
  // The rotate behavior lives in avatar_crop_rotate.test.ts — a black-box
  // file with the REAL cropperjs elements: a mocked $rotate call assertion
  // here stayed green while the rotatable gate silently no-opped the image.
  it('confirm: $toCanvas at AVATAR_SIDE with high-quality smoothing, then encodes and closes', async () => {
    const ctx = openRaw()
    ctx.image.$ready = vi.fn(() => Promise.resolve(document.createElement('img')))
    const fakeCtx = { imageSmoothingEnabled: false, imageSmoothingQuality: '' } as CanvasRenderingContext2D
    ctx.selection.$toCanvas = vi.fn((opts: { beforeDraw?: (c: CanvasRenderingContext2D) => void }) => {
      opts.beforeDraw?.(fakeCtx)
      return Promise.resolve(fakeCanvasEl([8 * 1024]))
    })
    await flush()

    clickConfirm()
    const blob = await ctx.p

    expect(ctx.selection.$toCanvas).toHaveBeenCalledTimes(1)
    expect(ctx.selection.$toCanvas).toHaveBeenCalledWith({
      width: AVATAR_SIDE,
      height: AVATAR_SIDE,
      beforeDraw: expect.any(Function),
    })
    expect(fakeCtx.imageSmoothingEnabled).toBe(true)
    expect(fakeCtx.imageSmoothingQuality).toBe('high')
    expect(blob).not.toBeNull()
    expect((blob as Blob).type).toBe('image/png')
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledWith(createObjectURL.mock.results[0].value)
  })

  it('cancel button: resolves null without touching the selection', async () => {
    const ctx = await openOverlay()
    ;(document.querySelector('[data-avatar-crop-cancel]') as HTMLElement).click()
    await expect(ctx.p).resolves.toBeNull()
    expect(ctx.selection.$toCanvas).not.toHaveBeenCalled()
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('mask click: resolves null and tears down without touching the canvas', async () => {
    const ctx = await openOverlay()
    ;(document.querySelector('[data-avatar-crop-mask]') as HTMLElement).click()
    await expect(ctx.p).resolves.toBeNull()
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
    expect(ctx.selection.$toCanvas).not.toHaveBeenCalled()
  })

  it('Escape: resolves null and tears down', async () => {
    const ctx = await openOverlay()
    pressEscape()
    await expect(ctx.p).resolves.toBeNull()
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('reopening after close keeps exactly one overlay; a stale Escape is inert', async () => {
    // Teardown contract: every close removes its own keydown listener —
    // without the assertion a leaked listener stays green (settled no-op).
    const rm = vi.spyOn(document, 'removeEventListener')
    const a = await openOverlay()
    expect(document.querySelectorAll('[data-avatar-crop-mask]').length).toBe(1)
    pressEscape()
    await expect(a.p).resolves.toBeNull()

    const b = await openOverlay()
    expect(document.querySelectorAll('[data-avatar-crop-mask]').length).toBe(1)
    pressEscape()
    await expect(b.p).resolves.toBeNull()

    // A and B each revoked exactly once — a double close would double-revoke.
    expect(revokeObjectURL).toHaveBeenCalledTimes(2)
    const urls = createObjectURL.mock.results.map((r) => r.value)
    for (const u of urls) expect(revokeObjectURL).toHaveBeenCalledWith(u)

    pressEscape()
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(2)
    const removed = rm.mock.calls.filter(([type]) => type === 'keydown')
    expect(removed.length).toBe(2)
    rm.mockRestore()
  })

  it('a second open supersedes the first as canceled', async () => {
    const a = await openOverlay()
    const b = await openOverlay()
    await expect(a.p).resolves.toBeNull()
    expect(revokeObjectURL).toHaveBeenCalledWith(createObjectURL.mock.results[0].value)
    expect(document.querySelectorAll('[data-avatar-crop-mask]').length).toBe(1)

    // B is fully functional after superseding A.
    clickConfirm()
    const blob = await b.p
    expect(blob).not.toBeNull()
    expect((blob as Blob).type).toBe('image/png')
  })

  it('confirm when both encodings exceed the cap: rejects, closes, revokes', async () => {
    const ctx = openRaw()
    ctx.image.$ready = vi.fn(() => Promise.resolve(document.createElement('img')))
    ctx.selection.$toCanvas = vi.fn(() => Promise.resolve(fakeCanvasEl([400 * 1024, 400 * 1024])))
    await flush()

    clickConfirm()
    await expect(ctx.p).rejects.toThrow(AVATAR_TOO_LARGE)
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('confirm when $toCanvas rejects: rejects with that error, closes, revokes', async () => {
    const ctx = openRaw()
    ctx.image.$ready = vi.fn(() => Promise.resolve(document.createElement('img')))
    ctx.selection.$toCanvas = vi.fn(() => Promise.reject(new Error('boom')))
    await flush()

    clickConfirm()
    await expect(ctx.p).rejects.toThrow('boom')
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('image load failure rejects the open promise and closes the overlay', async () => {
    const ctx = openRaw()
    ctx.image.$ready = vi.fn(() => Promise.reject(new Error('load error')))
    ctx.selection.$toCanvas = vi.fn()
    await expect(ctx.p).rejects.toThrow('image load failed')
    overlayGone()
    expect(revokeObjectURL).toHaveBeenCalledTimes(1)
  })

  it('the cropper theme lives in the theme-color attribute, not CSS overrides', () => {
    const css = readFileSync(resolve(__dirname, 'index.css'), 'utf-8')
    expect(css).not.toMatch(/cropper-(canvas|image|selection|handle)/)
    expect(css).not.toMatch(/avatar-cropper/)
  })

  it('renders title and buttons in the active locale', async () => {
    await openOverlay()
    expect(document.querySelector('[data-avatar-crop-title]')?.textContent).toBe('Crop avatar')
    expect(document.querySelector('[data-avatar-crop-cancel]')?.textContent).toBe('Cancel')
    expect(document.querySelector('[data-avatar-crop-confirm]')?.textContent).toBe('Confirm')
  })
})
