import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { initLocale, retranslate } from './i18n'
import { createVNCSheet } from './vnc_console'
import type { RemoteDevice } from './vnc'
import { instances } from '@novnc/novnc'
import { loadRFB } from './rfb_loader'

// The real noVNC package never loads in tests — an external dependency is
// mocked, not the system under test. MockRFB (in __mocks__/@novnc/novnc.ts)
// records the constructor wire shape, stores event listeners, and tracks the
// surface the sheet touches. Registration must be a __mocks__ redirect, not
// a vi.mock factory: vitest 4.1.9 resolves the second of two concurrently
// pending dynamic imports of a factory-mocked module to the real package,
// and the seq-guard test opens twice with no await between, so both imports
// are in flight at once.
vi.mock('@novnc/novnc')
vi.mock('./rfb_loader', async () => {
  const mod = await import('@novnc/novnc')
  // vi.fn so individual tests can fail the load (the loader is the seam the
  // TDZ bug lived behind) without mocking the sheet itself.
  return { loadRFB: vi.fn(async () => mod.default) }
})

const WIN11: RemoteDevice = { name: 'win11', addr: 'ws://127.0.0.1:8006', pass: 'pw' }
const NO_PASS: RemoteDevice = { name: 'mac', addr: 'ws://10.0.0.4:5901', pass: '' }
const NAS: RemoteDevice = { name: 'nas', addr: 'ws://10.0.0.8:8006', pass: '' }

// The sheet's only fetch is the latency probe — a flat stub suffices.
function stubTestEndpoint(result: { ok: boolean; latencyMs?: number; error?: string }) {
  vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => result })))
}

async function flushMicrotasks() {
  for (let i = 0; i < 10; i++) await Promise.resolve()
}

describe('createVNCSheet', () => {
  beforeEach(() => {
    instances().length = 0
    localStorage.removeItem('gbot-language')
    initLocale()
    stubTestEndpoint({ ok: true, latencyMs: 12 })
  })
  afterEach(() => {
    vi.unstubAllGlobals()
    localStorage.removeItem('gbot-language')
    initLocale()
    document.body.innerHTML = ''
  })

  function mount() {
    const sheet = createVNCSheet()
    document.body.appendChild(sheet.root)
    return sheet
  }
  async function openWin11() {
    const sheet = mount()
    sheet.open(WIN11)
    await vi.waitFor(() => expect(instances()).toHaveLength(1))
    return sheet
  }
  const stateOf = (sheet: { root: HTMLElement }): HTMLElement =>
    sheet.root.querySelector('[data-vnc-state]') as HTMLElement

  it('open shows the veil, titles, and constructs RFB against the proxy URL', async () => {
    const sheet = mount()
    expect(sheet.root.style.display).toBe('none')
    sheet.open(WIN11)
    expect(sheet.root.style.display).toBe('')
    expect(sheet.isOpen()).toBe(true)
    expect((sheet.root.querySelector('[data-vnc-title]') as HTMLElement).textContent).toBe('win11')
    expect(stateOf(sheet).textContent).toBe('Connecting…')
    await vi.waitFor(() => expect(instances()).toHaveLength(1))
    const inst = instances()[0]
    expect(inst.url).toBe(`ws://${location.host}/wui/vnc/win11`)
    expect(inst.options?.credentials?.password).toBe('pw')
    expect(inst.options?.wsProtocols).toEqual(['binary'])
    expect(inst.viewOnly).toBe(true)
    expect(inst.scaleViewport).toBe(true)
  })

  it('connect event hides the state overlay', async () => {
    const sheet = await openWin11()
    instances()[0].fire('connect')
    expect(stateOf(sheet).classList.contains('hidden')).toBe(true)
  })

  it('control chip flips the live session in place; chips restyle mutually exclusively', async () => {
    const sheet = await openWin11()
    const chip = sheet.root.querySelector('[data-vnc-chip="control"]') as HTMLElement
    expect(instances()[0].viewOnly).toBe(true)

    chip.click()
    // Same session: no rebuild, no dropped connection — viewOnly is a live
    // property in noVNC (its setter grabs/ungrabs the keyboard).
    expect(instances()).toHaveLength(1)
    expect(instances()[0].disconnectCalls).toBe(0)
    expect(instances()[0].viewOnly).toBe(false)
    expect(chip.className).toContain('text-green')

    chip.click()
    expect(instances()).toHaveLength(1)
    expect(instances()[0].viewOnly).toBe(true)
    expect(chip.className).toContain('text-t2')
    expect(chip.className).not.toContain('text-green')
  })

  it('leaving control releases held buttons before flipping view-only', async () => {
    const sheet = await openWin11()
    const inst = instances()[0]
    const chip = sheet.root.querySelector('[data-vnc-chip="control"]') as HTMLElement
    chip.click()
    expect(inst.viewOnly).toBe(false)
    const seen: Array<{ viewOnlyWhenDispatched: boolean }> = []
    inst.canvas.addEventListener('mouseup', () => {
      seen.push({ viewOnlyWhenDispatched: inst.viewOnly })
    })
    chip.click()
    // Released while still in control — the guard would drop it afterwards.
    expect(seen).toEqual([{ viewOnlyWhenDispatched: false }])
    expect(inst.viewOnly).toBe(true)
  })

  it('view chip switches to 1:1 clip+drag in place, and back', async () => {
    const sheet = await openWin11()
    const inst = instances()[0]
    const chip = sheet.root.querySelector('[data-vnc-chip="view"]') as HTMLElement
    expect(chip).not.toBeNull()
    expect(inst.scaleViewport).toBe(true)
    expect(inst.clipViewport).toBe(false)
    expect(inst.dragViewport).toBe(false)

    chip.click()
    // Same session — viewport properties are live in noVNC.
    expect(instances()).toHaveLength(1)
    expect(inst.disconnectCalls).toBe(0)
    expect(inst.scaleViewport).toBe(false)
    expect(inst.clipViewport).toBe(true)
    expect(inst.dragViewport).toBe(true)
    expect(chip.className).toContain('text-green')

    chip.click()
    expect(instances()).toHaveLength(1)
    expect(inst.scaleViewport).toBe(true)
    expect(inst.clipViewport).toBe(false)
    expect(inst.dragViewport).toBe(false)
    expect(chip.className).toContain('text-t2')
  })

  it('view chip rebuilds only a dead session', async () => {
    const sheet = await openWin11()
    instances()[0].fire('disconnect', { clean: false })
    ;(sheet.root.querySelector('[data-vnc-chip="view"]') as HTMLElement).click()
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    const fresh = instances()[1]
    expect(fresh.scaleViewport).toBe(false)
    expect(fresh.clipViewport).toBe(true)
    expect(fresh.dragViewport).toBe(true)
  })

  it('the IME overlays content while the console is open, and reverts on close', async () => {
    // index.html carries the viewport meta; jsdom's bare document does not,
    // so the test installs the production-shaped tag itself.
    const meta = document.createElement('meta')
    meta.setAttribute('name', 'viewport')
    meta.setAttribute('content', 'width=device-width, initial-scale=1.0, viewport-fit=cover')
    document.head.appendChild(meta)
    try {
      const sheet = await openWin11()
      expect(meta.getAttribute('content')).toContain('interactive-widget=overlays-content')
      sheet.close()
      // Exact restoration, not just key absence — a sloppy remover that
      // leaves a dangling separator must fail.
      expect(meta.getAttribute('content')).toBe(
        'width=device-width, initial-scale=1.0, viewport-fit=cover',
      )
    } finally {
      meta.remove()
    }
  })

  it('chip tooltips retranslate on a mid-session language switch', async () => {
    const sheet = await openWin11()
    const chip = sheet.root.querySelector('[data-vnc-chip="control"]') as HTMLElement
    expect(chip.getAttribute('title')).toBe('Control')
    localStorage.setItem('gbot-language', 'zh')
    initLocale()
    retranslate(document.body)
    expect(chip.getAttribute('title')).toBe('接管')
  })

  it('toolbar row keeps title, chips, latency, close order', () => {
    const sheet = mount()
    const row = sheet.root.querySelector('[data-vnc-bar]') as HTMLElement
    // The keyboard chip predates iconBtn and carries its own attribute, so
    // children are classified by attribute rather than one selector.
    const ids = Array.from(row.children).map((el) => {
      if (el.hasAttribute('data-vnc-title')) return 'title'
      if (el.hasAttribute('data-vnc-dot')) return 'dot'
      if (el.hasAttribute('data-vnc-latency')) return 'latency'
      if (el.hasAttribute('data-vnc-close')) return 'close'
      if (el.hasAttribute('data-vnc-keyboard')) return 'keyboard'
      return el.getAttribute('data-vnc-chip')
    })
    expect(ids).toEqual(['title', 'dot', 'control', 'view', 'keyboard', 'fullscreen', 'latency', 'close'])
  })

  it('reopening the console resets the view mode to fit', async () => {
    const sheet = await openWin11()
    const chip = sheet.root.querySelector('[data-vnc-chip="view"]') as HTMLElement
    chip.click()
    expect(instances()[0].clipViewport).toBe(true)
    sheet.close()
    sheet.open(NO_PASS)
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    expect(instances()[1].scaleViewport).toBe(true)
    expect(instances()[1].clipViewport).toBe(false)
    expect(chip.className).toContain('text-t2')
  })

  it('control chip rebuilds only a dead session', async () => {
    const sheet = await openWin11()
    instances()[0].fire('disconnect', { clean: false })
    ;(sheet.root.querySelector('[data-vnc-chip="control"]') as HTMLElement).click()
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    expect(instances()[1].viewOnly).toBe(false)
  })

  it('keyboard button focuses the hidden capture; typing forwards keysyms live', async () => {
    const sheet = await openWin11()
    const kbd = sheet.root.querySelector('[data-vnc-kbd-input]') as HTMLTextAreaElement
    const focus = vi.spyOn(kbd, 'focus')
    ;(sheet.root.querySelector('[data-vnc-keyboard]') as HTMLElement).click()
    expect(focus).toHaveBeenCalledTimes(1)
    expect(
      (sheet.root.querySelector('[data-vnc-keyboard]') as HTMLElement).classList.contains(
        'text-green',
      ),
    ).toBe(true)
    kbd.dispatchEvent(new InputEvent('input', { data: 'a你' }))
    expect(instances()[0].sentKeys).toEqual([
      { keysym: 0x61, down: true },
      { keysym: 0x61, down: false },
      { keysym: 0x01000000 + 0x4f60, down: true },
      { keysym: 0x01000000 + 0x4f60, down: false },
    ])
    // The capture stays empty — it is an event source, not a text field.
    expect(kbd.value).toBe('')
    kbd.dispatchEvent(new KeyboardEvent('keydown', { key: 'Backspace' }))
    expect(instances()[0].sentKeys.slice(-2)).toEqual([
      { keysym: 0xff08, down: true },
      { keysym: 0xff08, down: false },
    ])
    kbd.dispatchEvent(new Event('blur'))
    expect(
      (sheet.root.querySelector('[data-vnc-keyboard]') as HTMLElement).classList.contains(
        'text-blue',
      ),
    ).toBe(false)
  })

  it('fullscreen chip falls back to in-page maximize when the browser cannot fullscreen', async () => {
    const sheet = await openWin11()
    const sheetEl = sheet.root.querySelector('[data-vnc-sheet]') as HTMLElement
    const chip = sheet.root.querySelector('[data-vnc-chip="fullscreen"]') as HTMLElement
    // jsdom: fullscreenEnabled undefined → the WebView-shaped fallback path
    expect(document.fullscreenEnabled).toBeUndefined()
    const resizes: string[] = []
    window.addEventListener('resize', () => resizes.push('resize'))
    chip.click()
    expect(sheetEl.classList.contains('vnc-max')).toBe(true)
    expect(resizes).toEqual(['resize'])
    chip.click()
    expect(sheetEl.classList.contains('vnc-max')).toBe(false)
    expect(resizes).toEqual(['resize', 'resize'])
  })

  it('fullscreen chip uses the native API when the browser reports it enabled', async () => {
    const sheet = await openWin11()
    const sheetEl = sheet.root.querySelector('[data-vnc-sheet]') as HTMLElement & {
      requestFullscreen?: () => void
    }
    Object.defineProperty(document, 'fullscreenEnabled', { value: true, configurable: true })
    const enter = vi.fn()
    sheetEl.requestFullscreen = enter
    const exit = vi.fn()
    Object.defineProperty(document, 'exitFullscreen', { value: exit, configurable: true })
    try {
      ;(sheet.root.querySelector('[data-vnc-chip="fullscreen"]') as HTMLElement).click()
      expect(enter).toHaveBeenCalledTimes(1)
      Object.defineProperty(document, 'fullscreenElement', { get: () => sheetEl, configurable: true })
      ;(sheet.root.querySelector('[data-vnc-chip="fullscreen"]') as HTMLElement).click()
      expect(exit).toHaveBeenCalledTimes(1)
    } finally {
      Reflect.deleteProperty(document, 'fullscreenElement')
      Reflect.deleteProperty(document, 'exitFullscreen')
      Reflect.deleteProperty(document, 'fullscreenEnabled')
    }
  })

  it('latency label shows Nms on ok and clears on probe failure', async () => {
    const sheet = mount()
    sheet.open(WIN11)
    await vi.waitFor(() => {
      expect((sheet.root.querySelector('[data-vnc-latency]') as HTMLElement).textContent).toBe(
        '12ms',
      )
    })
    stubTestEndpoint({ ok: false, error: 'dial tcp refused' })
    // Reconnect by reopening — the control chip no longer rebuilds the session.
    sheet.close()
    sheet.open(NO_PASS)
    await vi.waitFor(() => {
      expect((sheet.root.querySelector('[data-vnc-latency]') as HTMLElement).textContent).toBe('')
    })
  })

  it('a failed module load shows the failed state and logs the cause', async () => {
    vi.mocked(loadRFB).mockRejectedValueOnce(new Error('boom'))
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const sheet = mount()
      sheet.open(WIN11)
      await vi.waitFor(() => expect(stateOf(sheet).textContent).toBe('Connection failed'))
      expect(errSpy).toHaveBeenCalled()
      expect(instances()).toHaveLength(0)
    } finally {
      errSpy.mockRestore()
    }
  })

  it("a superseded session's load failure cannot clobber the newer session", async () => {
    let rejectFirst!: (err: unknown) => void
    vi.mocked(loadRFB).mockImplementationOnce(
      () =>
        new Promise((_, rej) => {
          rejectFirst = rej
        }),
    )
    const errSpy = vi.spyOn(console, 'error').mockImplementation(() => {})
    try {
      const sheet = mount()
      sheet.open(WIN11)
      sheet.open(NAS)
      await vi.waitFor(() => expect(instances()).toHaveLength(1))
      rejectFirst(new Error('stale load failed'))
      await flushMicrotasks()
      expect(stateOf(sheet).textContent).toBe('Connecting…')
      expect(errSpy).not.toHaveBeenCalled()
      expect(instances()[0].url).toBe(`ws://${location.host}/wui/vnc/nas`)
    } finally {
      errSpy.mockRestore()
    }
  })

  it('✕ and veil-background clicks disconnect and hide the sheet', async () => {
    const sheet = await openWin11()
    ;(sheet.root.querySelector('[data-vnc-close]') as HTMLElement).click()
    expect(instances()[0].disconnectCalls).toBe(1)
    expect(sheet.root.style.display).toBe('none')
    expect(sheet.isOpen()).toBe(false)

    sheet.open(WIN11)
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    sheet.root.click()
    expect(instances()[1].disconnectCalls).toBe(1)
    expect(sheet.root.style.display).toBe('none')
  })

  it('disconnect clean shows Disconnected; unclean shows Connection failed', async () => {
    const sheet = await openWin11()
    instances()[0].fire('disconnect', { clean: true })
    expect(stateOf(sheet).textContent).toBe('Disconnected')
    instances()[0].fire('disconnect', { clean: false })
    expect(stateOf(sheet).textContent).toBe('Connection failed')
  })

  it('securityfailure appends the raw server reason when present', async () => {
    const sheet = await openWin11()
    instances()[0].fire('securityfailure', { reason: 'auth failed' })
    expect(stateOf(sheet).textContent).toBe('Connection failed — auth failed')
    instances()[0].fire('securityfailure', {})
    expect(stateOf(sheet).textContent).toBe('Connection failed')
  })

  it('opening a new console leaves no stray document copy listener behind', async () => {
    await openWin11()
    document.dispatchEvent(new Event('copy'))
    await flushMicrotasks()
    expect(instances()[0].sentKeys).toEqual([])
  })

  it('credentialsrequired sends the configured password, or shows the password state', async () => {
    const sheet = await openWin11()
    instances()[0].fire('credentialsrequired', { types: ['password'] })
    expect(instances()[0].sentCredentials).toEqual([{ password: 'pw' }])
    // Credentials were answered automatically — the session is still
    // connecting, so the overlay stays visible on Connecting….
    expect(stateOf(sheet).classList.contains('hidden')).toBe(false)
    expect(stateOf(sheet).textContent).toBe('Connecting…')

    const bare = mount()
    bare.open(NO_PASS)
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    instances()[1].fire('credentialsrequired', { types: ['password'] })
    expect(instances()[1].sentCredentials).toEqual([])
    expect(stateOf(bare).textContent).toBe('Password required — configure it in Settings')
  })

  it('opening B while A is open disconnects A exactly once and follows B', async () => {
    const sheet = await openWin11()
    sheet.open({ name: 'nas', addr: 'ws://10.0.0.8:8006', pass: '' })
    await vi.waitFor(() => expect(instances()).toHaveLength(2))
    expect(instances()[0].disconnectCalls).toBe(1)
    expect((sheet.root.querySelector('[data-vnc-title]') as HTMLElement).textContent).toBe('nas')
    expect(instances()[1].url).toBe(`ws://${location.host}/wui/vnc/nas`)
  })

  it('immediate second open while the first import is settling keeps only the second session', async () => {
    const probeAddrs: string[] = []
    // Distinct latency per addr: a stale WIN11 probe landing would read
    // 12ms; the live NAS probe reads 34ms.
    vi.stubGlobal(
      'fetch',
      vi.fn(async (_url: string, init: { body: string }) => {
        const addr = JSON.parse(init.body).addr
        probeAddrs.push(addr)
        return {
          ok: true,
          status: 200,
          json: async () => ({ ok: true, latencyMs: addr === WIN11.addr ? 12 : 34 }),
        }
      }),
    )
    const sheet = mount()
    sheet.open(WIN11)
    // Deliberately no await — the first connect's import() is still settling
    // when the second open bumps the seq.
    sheet.open(NAS)
    await vi.waitFor(() => {
      expect((sheet.root.querySelector('[data-vnc-latency]') as HTMLElement).textContent).toBe(
        '34ms',
      )
    })
    expect(instances()).toHaveLength(1)
    expect(instances()[0].url).toBe(`ws://${location.host}/wui/vnc/nas`)
    // The first session's probe is never issued, so its result cannot land.
    expect(probeAddrs).toEqual([NAS.addr])
  })
  it('a late-resolving probe from a superseded session cannot update the latency label', async () => {
    let releaseWin11!: (resp: unknown) => void
    const win11Probe = new Promise<unknown>((r) => {
      releaseWin11 = r
    })
    const fetchMock = vi.fn(async (_url: string, init: { body: string }) => {
      const addr = JSON.parse(init.body).addr
      // WIN11's probe stays in flight until released — unlike the
      // rapid-reopen shape, this session got past the pre-import check
      // and ISSUED its probe before NAS superseded it.
      if (addr === WIN11.addr) return win11Probe
      return { ok: true, status: 200, json: async () => ({ ok: true, latencyMs: 34 }) }
    })
    vi.stubGlobal('fetch', fetchMock)
    const sheet = mount()
    sheet.open(WIN11)
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    sheet.open(NAS)
    const label = sheet.root.querySelector('[data-vnc-latency]') as HTMLElement
    await vi.waitFor(() => expect(label.textContent).toBe('34ms'))
    releaseWin11({ ok: true, status: 200, json: async () => ({ ok: true, latencyMs: 12 }) })
    await flushMicrotasks()
    expect(label.textContent).toBe('34ms')
  })
  it('a late-rejecting probe from a superseded session cannot clear the latency label', async () => {
    let rejectWin11!: (err: unknown) => void
    const win11Probe = new Promise<never>((_, rej) => {
      rejectWin11 = rej
    })
    const fetchMock = vi.fn(async (_url: string, init: { body: string }) => {
      const addr = JSON.parse(init.body).addr
      if (addr === WIN11.addr) return win11Probe
      return { ok: true, status: 200, json: async () => ({ ok: true, latencyMs: 34 }) }
    })
    vi.stubGlobal('fetch', fetchMock)
    const sheet = mount()
    sheet.open(WIN11)
    // WIN11's probe must be in flight before NAS supersedes it, otherwise
    // the catch-guard is never reachable.
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1))
    sheet.open(NAS)
    const label = sheet.root.querySelector('[data-vnc-latency]') as HTMLElement
    await vi.waitFor(() => expect(label.textContent).toBe('34ms'))
    rejectWin11(new Error('probe aborted'))
    await flushMicrotasks()
    expect(label.textContent).toBe('34ms')
  })
})
