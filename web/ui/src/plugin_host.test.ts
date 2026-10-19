import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest'
import { openPluginCard, closePluginCard, fetchPluginList } from './plugin_host'
import type { Channel } from './transport'

const fakeChannel: Channel = {
  name: 'browser',
  opened: true,
  send: () => {},
  setOnMessage: () => {},
  setOnClose: () => {},
  close: () => {},
}

describe('plugin host', () => {
  beforeEach(() => {
    closePluginCard()
    document.body.innerHTML = ''
  })
  afterEach(() => {
    closePluginCard()
    vi.unstubAllGlobals()
  })

  it('fetchPluginList decodes the daemon payload', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({
        ok: true,
        status: 200,
        json: async () => [{ name: 'browser', ui: '/plugins/browser/ui', running: true }],
      })),
    )
    const cards = await fetchPluginList()
    expect(cards).toEqual([{ name: 'browser', ui: '/plugins/browser/ui', running: true }])
  })

  it('fetchPluginList rejects on non-2xx', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 500, json: async () => [] })),
    )
    await expect(fetchPluginList()).rejects.toThrow('plugin list 500')
  })

  it('openPluginCard mounts the UI module with openChannel and close ctx', async () => {
    const mount = vi.fn((_c: HTMLElement, _ctx: unknown) => () => {})
    const card = { name: 'browser', ui: '/plugins/browser/ui', running: true }
    await openPluginCard(card, { loader: async () => ({ mount }) })
    expect(mount).toHaveBeenCalledTimes(1)
    const [container, ctx] = mount.mock.calls[0]
    expect(container.tagName).toBe('DIV')
    expect(typeof ctx.openChannel).toBe('function')
    expect(typeof ctx.close).toBe('function')
    const sheet = document.querySelector('.vnc-sheet')
    expect(sheet).not.toBeNull()
    expect(document.querySelector('.plugin-card-body')).not.toBeNull()
    // Title row carries the plugin name.
    expect(sheet!.textContent).toContain('browser')
  })

  it('closing runs the module cleanup and removes the sheet', async () => {
    const cleanup = vi.fn()
    const card = { name: 'browser', ui: '/x.js', running: true }
    await openPluginCard(card, { loader: async () => ({ mount: () => cleanup }) })
    expect(document.querySelector('.vnc-sheet')).not.toBeNull()
    closePluginCard()
    expect(cleanup).toHaveBeenCalledTimes(1)
    expect(document.querySelector('.vnc-sheet')).toBeNull()
  })

  it('reopen swaps sheets: only one card at a time, old cleanup runs', async () => {
    const cleanup1 = vi.fn()
    const cleanup2 = vi.fn()
    const card = { name: 'browser', ui: '/x.js', running: true }
    await openPluginCard(card, { loader: async () => ({ mount: () => cleanup1 }) })
    await openPluginCard(card, { loader: async () => ({ mount: () => cleanup2 }) })
    expect(cleanup1).toHaveBeenCalledTimes(1)
    expect(cleanup2).not.toHaveBeenCalled()
    expect(document.querySelectorAll('.vnc-sheet')).toHaveLength(1)
    closePluginCard()
    expect(cleanup2).toHaveBeenCalledTimes(1)
  })

  it('module load failure shows the error in the card instead of a blank sheet', async () => {
    const card = { name: 'broken', ui: '/broken.js', running: true }
    await openPluginCard(card, { loader: async () => { throw new Error('404') } })
    const body = document.querySelector('.plugin-card-body')
    expect(body?.textContent).toContain('plugin ui failed')
    expect(body?.textContent).toContain('404')
  })

  it('ctx.openChannel routes through the injected connection', async () => {
    const openChannel = vi.fn(() => fakeChannel)
    const card = { name: 'browser', ui: '/x.js', running: true }
    await openPluginCard(card, {
      loader: async () => ({ mount: (_c, ctx) => { ctx.openChannel('browser') } }),
      getChannel: () => ({ openChannel }),
    })
    expect(openChannel).toHaveBeenCalledWith('browser')
  })
})
