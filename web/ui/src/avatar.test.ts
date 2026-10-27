import { describe, it, expect, afterEach, vi } from 'vitest'
import {
  AVATAR_TOO_LARGE,
  customAvatarURL,
  deleteAvatar,
  encodeCanvas,
  refreshAvatar,
  renderAvatarInto,
  resetAvatarStateForTest,
  rewriteAvatarHolders,
  uploadAvatar,
} from './avatar'
import { createElement } from './dom'

function agentHolder(): HTMLElement {
  const el = createElement('div', 'h-5 w-5 rounded-md')
  document.body.appendChild(el)
  return el
}

function userHolder(): HTMLElement {
  const el = createElement('div', 'h-5 w-5 rounded-md')
  document.body.appendChild(el)
  return el
}

// A HEAD stub whose 200 carries a real ETag header — probeAvatar reads
// resp.headers, so bare-object stubs silently fall back to Date.now().
function stubHead(status: number, etag?: string) {
  const handler = vi.fn(async () => ({
    ok: status >= 200 && status < 300,
    status,
    headers: etag !== undefined ? new Headers({ ETag: etag }) : new Headers(),
  }))
  vi.stubGlobal('fetch', handler)
  return handler
}

afterEach(() => {
  resetAvatarStateForTest()
  vi.unstubAllGlobals()
  document.body.innerHTML = ''
})

describe('default avatar rendering', () => {
  it('renders the G letter for agent and stamps dataset markers', () => {
    const el = agentHolder()
    renderAvatarInto(el, 'agent')
    expect(el.dataset.avatar).toBe('agent')
    expect(el.textContent).toBe('G')
  })

  it('renders the user icon at the requested size', () => {
    const el = userHolder()
    renderAvatarInto(el, 'user', 12)
    expect(el.dataset.avatar).toBe('user')
    expect(el.dataset.avatarIcon).toBe('12')
    expect(el.querySelector('svg')).toBeTruthy()
  })
  it('custom image mode strips the default backdrop so transparent PNGs do not bleed the gradient', async () => {
    const el = agentHolder()
    // Hostile fixture: the holder carries the default backdrop exactly the
    // way chat.ts buildShell and settings.ts mount it.
    el.className += ' avatar-g-bg'
    el.style.background = 'linear-gradient(to bottom right, #00B4FF, #9D5CFF)'
    renderAvatarInto(el, 'agent')
    stubHead(200, '"t-1"')
    await refreshAvatar('agent')
    expect(el.querySelector('img')).toBeTruthy()
    expect(el.classList.contains('avatar-g-bg')).toBe(false)
    expect(el.style.background).toBe('')
    // Back to default (probe 404): the backdrop returns with the letter —
    // dark theme paints via the inline style (the class is light-only), so
    // assert the style too: this exact assertion caught the restore gap.
    stubHead(404)
    await refreshAvatar('agent')
    expect(el.classList.contains('avatar-g-bg')).toBe(true)
    // jsdom's cssstyle drops gradient declarations on parse, so assert the
    // attribute string — browsers parse it fine.
    expect(el.getAttribute('style')).toContain('linear-gradient')
    expect(el.textContent).toBe('G')
  })
  it('user mode: strip mirrors agent and restore brings the gray gradient classes back', async () => {
    const el = userHolder()
    el.className += ' bg-gradient-to-br from-t2 to-t3'
    renderAvatarInto(el, 'user')
    stubHead(200, '"t-2"')
    await refreshAvatar('user')
    expect(el.querySelector('img')).toBeTruthy()
    expect(el.classList.contains('bg-gradient-to-br')).toBe(false)
    expect(el.classList.contains('from-t2')).toBe(false)
    expect(el.classList.contains('to-t3')).toBe(false)
    stubHead(404)
    await refreshAvatar('user')
    expect(el.classList.contains('bg-gradient-to-br')).toBe(true)
    expect(el.classList.contains('from-t2')).toBe(true)
    expect(el.classList.contains('to-t3')).toBe(true)
    expect(el.querySelector('svg')).toBeTruthy()
  })
})

describe('custom avatar rendering', () => {
  it('probes 200 and swaps every agent holder to an img with a versioned URL', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    const user = userHolder()
    renderAvatarInto(user, 'user')
    stubHead(200, '"t-1"')

    await refreshAvatar('agent')

    const img = agent.querySelector('img')
    expect(img).toBeTruthy()
    expect(img?.getAttribute('src')).toMatch(/^\/api\/avatar\/agent\?v=/)
    expect(img?.getAttribute('src')).toContain('t-1')
    expect(customAvatarURL('agent')).toContain('t-1')
    expect(user.querySelector('svg')).toBeTruthy()
    expect(user.querySelector('img')).toBeNull()
  })

  it('falls back to the default on img error, once and without a loop', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    stubHead(200, '"t-1"')
    await refreshAvatar('agent')
    const img = agent.querySelector('img') as HTMLImageElement
    expect(img).toBeTruthy()

    img.dispatchEvent(new Event('error'))

    expect(agent.textContent).toBe('G')
    expect(customAvatarURL('agent')).toBeNull()
    expect(agent.querySelector('img')).toBeNull()
  })

  it('clears state on probe 404', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    stubHead(200, '"t-1"')
    await refreshAvatar('agent')
    expect(customAvatarURL('agent')).not.toBeNull()

    stubHead(404)
    await refreshAvatar('agent')
    expect(customAvatarURL('agent')).toBeNull()
    expect(agent.textContent).toBe('G')
  })

  it('keeps the known state on probe 500 (transient failures must not reset)', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    stubHead(200, '"t-1"')
    await refreshAvatar('agent')

    stubHead(500)
    await refreshAvatar('agent')

    expect(customAvatarURL('agent')).toContain('t-1')
    expect(agent.querySelector('img')).toBeTruthy()
  })

  it('keeps the known state when fetch rejects', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    stubHead(200, '"t-1"')
    await refreshAvatar('agent')

    vi.stubGlobal('fetch', vi.fn(async () => {
      throw new TypeError('Failed to parse URL')
    }))
    await refreshAvatar('agent')

    expect(customAvatarURL('agent')).toContain('t-1')
  })

  it('falls back to Date.now() when the stub carries no headers object', async () => {
    const agent = agentHolder()
    renderAvatarInto(agent, 'agent')
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200 })))
    await refreshAvatar('agent')
    const src = (agent.querySelector('img') as HTMLImageElement).getAttribute('src') ?? ''
    expect(src).toMatch(/^\/api\/avatar\/agent\?v=\d+-1$/)
  })
})

describe('rewriteAvatarHolders', () => {
  it('rewrites only holders marked with data-avatar', async () => {
    const a1 = agentHolder()
    const a2 = agentHolder()
    const plain = userHolder()
    renderAvatarInto(a1, 'agent')
    renderAvatarInto(a2, 'agent')
    renderAvatarInto(plain, 'user', 12)
    stubHead(200, '"t-9"')
    await refreshAvatar('agent')

    rewriteAvatarHolders('agent')

    expect(a1.querySelector('img')).toBeTruthy()
    expect(a2.querySelector('img')).toBeTruthy()
    expect(plain.querySelector('svg')).toBeTruthy()
    expect(plain.querySelector('img')).toBeNull()
  })

  it('restores the recorded icon size when rewriting back to the default', async () => {
    const el = createElement('div', 'h-5 w-5 rounded-md')
    document.body.appendChild(el)
    renderAvatarInto(el, 'user', 32)
    stubHead(200, '"t-2"')
    await refreshAvatar('user')
    expect(el.querySelector('img')).toBeTruthy()

    stubHead(404)
    await refreshAvatar('user')

    const svg = el.querySelector('svg')
    expect(svg).toBeTruthy()
    expect(svg?.getAttribute('width')).toBe('32')
  })
})

describe('uploadAvatar', () => {
  it('PUTs the blob with its type as Content-Type and the blob as body', async () => {
    const handler = vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ ok: true }) }))
    vi.stubGlobal('fetch', handler)
    const blob = new Blob([new Uint8Array([1, 2, 3])], { type: 'image/png' })

    await uploadAvatar('agent', blob)

    expect(handler).toHaveBeenCalledTimes(1)
    const [url, init] = handler.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/avatar/agent')
    expect(init.method).toBe('PUT')
    expect((init.headers as Record<string, string>)['Content-Type']).toBe('image/png')
    expect(init.body).toBe(blob)
  })

  it('rejects with the envelope error message on failure', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({
      ok: false,
      status: 413,
      json: async () => ({ error: 'x' }),
    })))
    await expect(uploadAvatar('user', new Blob([]))).rejects.toThrow('x')
  })
})

describe('deleteAvatar', () => {
  it('DELETEs and resolves on 204', async () => {
    const handler = vi.fn(async () => ({ ok: true, status: 204 }))
    vi.stubGlobal('fetch', handler)

    await deleteAvatar('agent')

    const [url, init] = handler.mock.calls[0] as [string, RequestInit]
    expect(url).toBe('/api/avatar/agent')
    expect(init.method).toBe('DELETE')
  })

  it('rejects on non-2xx', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: false, status: 500 })))
    await expect(deleteAvatar('user')).rejects.toThrow()
  })
})

// jsdom has no canvas encoding either — the tests inject a structural fake
// (width/height/toBlob). encodeCanvas must never touch a 2d context: the
// cropper's $toCanvas product is already the final 256×256 frame, which is
// exactly what the no-getContext case pins.
function fakeEncodeCanvas(blobSizes: Array<number | null>) {
  const toBlobCalls: Array<[string | undefined, number | undefined]> = []
  let next = 0
  const canvas = {
    width: 256,
    height: 256,
    toBlob: (cb: (b: Blob | null) => void, type?: string, quality?: number) => {
      toBlobCalls.push([type, quality])
      const size = blobSizes[next]
      next++
      cb(size === null ? null : new Blob([new ArrayBuffer(size)], { type: type ?? '' }))
    },
  } as unknown as HTMLCanvasElement
  return { canvas, toBlobCalls }
}

describe('encodeCanvas', () => {
  it('returns the PNG when it is under the cap, encoding exactly once', async () => {
    const { canvas, toBlobCalls } = fakeEncodeCanvas([50 * 1024])
    const blob = await encodeCanvas(canvas)
    expect(blob.type).toBe('image/png')
    expect(toBlobCalls).toEqual([['image/png', undefined]])
  })

  it('falls back to JPEG q0.9 when the PNG exceeds the cap', async () => {
    const { canvas, toBlobCalls } = fakeEncodeCanvas([400 * 1024, 10 * 1024])
    const blob = await encodeCanvas(canvas)
    expect(blob.type).toBe('image/jpeg')
    expect(toBlobCalls.length).toBe(2)
    expect(toBlobCalls[1]).toEqual(['image/jpeg', 0.9])
  })

  it('rejects with the sentinel when both encodings exceed the cap', async () => {
    const { canvas } = fakeEncodeCanvas([400 * 1024, 400 * 1024])
    await expect(encodeCanvas(canvas)).rejects.toThrow(AVATAR_TOO_LARGE)
  })

  it('rejects with the sentinel when toBlob yields null twice', async () => {
    const { canvas } = fakeEncodeCanvas([null, null])
    await expect(encodeCanvas(canvas)).rejects.toThrow(AVATAR_TOO_LARGE)
  })

  it('resolves on a fake with no getContext at all', async () => {
    const { canvas } = fakeEncodeCanvas([8 * 1024])
    expect('getContext' in canvas).toBe(false)
    await expect(encodeCanvas(canvas)).resolves.toMatchObject({ type: 'image/png' })
  })
})
