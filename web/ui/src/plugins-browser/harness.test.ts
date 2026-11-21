// harness.js unit tests: browser.* helper bar over mocked playwright tools.
// The module installs globalThis.browser at import time; each test re-imports
// (vi.resetModules) for a clean _mode/_baseTabs state and points globalThis.tools
// at a fresh mock table. Real setTimeout sleeps run under fake timers, with
// stepwise advances pinning the 600/3000/5000/150/400 ms boundaries exactly.
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

type MockTool = ReturnType<typeof vi.fn>

interface ModeP {
  ua: string
  w: number
  h: number
  dsf: number
  mobile: boolean
  scale: number
  platform?: string
}

interface BrowserHarness {
  PREFIX: string
  _t(name: string): MockTool
  _sleep(ms: number): Promise<void>
  _classifyNetworkError(e: unknown): Error | null
  _mode: string
  _desktop: ModeP
  _mobileBaseline: ModeP
  _applyMode(p: ModeP): Promise<void>
  setMode(mode: string): Promise<string>
  mode(): string
  snapshot(): Promise<string>
  _looksEmpty(yaml: string): boolean
  goto(url: string): Promise<void>
  _gotoBase(url: string): Promise<void>
  find(yaml: string, pattern: RegExp): string
  _tabIndices(): Promise<number[]>
  _tabCount(): Promise<number>
  _maybeSwitchNewTab(before: number[]): Promise<boolean>
  click(desc: string, pattern: RegExp): Promise<string>
  type(desc: string, pattern: RegExp, text: string, submit?: boolean): Promise<string>
  select(desc: string, pattern: RegExp, values: string[]): Promise<void>
  waitText(text: string, tries?: number): Promise<boolean>
  waitGone(text: string, tries?: number): Promise<boolean>
  evaluate(fn: string): Promise<string>
  shot(name: string): Promise<unknown>
  _safeName(name: string): string
  _parseEval(raw: string): unknown
  tabBaseline(): Promise<number>
  tabCleanup(): Promise<string>
  vshot(name: string): Promise<{ file: string; vw: number; vh: number }>
  vclick(fx: number, fy: number, desc?: string): Promise<string>
  _baseTabs: number | null
}

const PREFIX = 'mcp__plugin_browser_playwright__'

const PAGE_YAML = '### Snapshot\n- heading "Title"\n- button "Go" [ref=e12]\n- textbox "Search" [ref=e7]'
const EMPTY_YAML = '### Snapshot\n- frame'

const DESKTOP_UA =
  'Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36'
const MOBILE_UA =
  'Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.8059.12 Mobile Safari/537.36'

const DESKTOP_CODE =
  'async (page) => {' +
  '  const cdp = await page.context().newCDPSession(page);' +
  "  await cdp.send('Emulation.setUserAgentOverride', { userAgent: " +
  JSON.stringify(DESKTOP_UA) +
  ' });' +
  "  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 1024, height: 768, deviceScaleFactor: 3, mobile: false });" +
  "  await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 });" +
  "  return 'ok';" +
  '}'

const MOBILE_CODE =
  'async (page) => {' +
  '  const cdp = await page.context().newCDPSession(page);' +
  "  await cdp.send('Emulation.setUserAgentOverride', { userAgent: " +
  JSON.stringify(MOBILE_UA) +
  ' });' +
  "  await cdp.send('Emulation.setDeviceMetricsOverride', { width: 412, height: 839, deviceScaleFactor: 2.625, mobile: true });" +
  "  await cdp.send('Emulation.setPageScaleFactor', { pageScaleFactor: 1 });" +
  "  return 'ok';" +
  '}'

const tools: Record<string, MockTool> = {}

function tool(name: string): MockTool {
  const fn = tools[PREFIX + name]
  if (!fn) throw new Error('test bug: missing mock tool ' + PREFIX + name)
  return fn
}

function given(name: string): MockTool {
  const fn = vi.fn()
  tools[PREFIX + name] = fn
  return fn
}

async function loadHarness(): Promise<BrowserHarness> {
  vi.resetModules()
  ;(globalThis as Record<string, unknown>).tools = tools
  await import('../../plugins-browser/harness.js')
  const b = (globalThis as Record<string, unknown>).browser
  if (!b) throw new Error('harness.js did not install globalThis.browser')
  return b as BrowserHarness
}

const tick = (): Promise<void> => new Promise<void>((resolve) => setImmediate(resolve))

// Drive every timer the flow arms (600+3000+5000 sleeps are far below 60s of
// virtual time) and flush the microtasks between them.
async function settle<T>(p: Promise<T>): Promise<T> {
  await vi.advanceTimersByTimeAsync(60000)
  return await p
}

function expectReject(p: Promise<unknown>): Promise<Error> {
  return p.then(
    () => {
      throw new Error('expected the promise to reject')
    },
    (e: unknown) => e as Error,
  )
}

describe('browser harness', () => {
  beforeEach(() => {
    vi.restoreAllMocks()
    // Only the timer pair the harness uses; setImmediate must stay real so
    // tick() can flush microtasks without touching virtual time.
    vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] })
    for (const k of Object.keys(tools)) delete tools[k]
  })
  afterEach(() => {
    vi.useRealTimers()
    delete (globalThis as Record<string, unknown>).browser
  })

  describe('tool table', () => {
    it('exposes the pinned playwright tool prefix', async () => {
      const b = await loadHarness()
      expect(b.PREFIX).toBe(PREFIX)
    })

    it('_t resolves a tool by prefix+name and returns the very function', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot').mockResolvedValue('### Snapshot\nx')
      expect(b._t('browser_snapshot')).toBe(snap)
    })

    it('_t throws naming the missing tool when it is absent from the table', async () => {
      const b = await loadHarness()
      const err = await expectReject(b.snapshot())
      expect(err).toBeInstanceOf(Error)
      expect(err.message).toBe('playwright tool missing: browser_snapshot')
    })
  })

  describe('_sleep', () => {
    it('resolves via setTimeout after exactly ms', async () => {
      const b = await loadHarness()
      let done = false
      const p = b._sleep(50).then(() => {
        done = true
      })
      await tick()
      expect(done).toBe(false)
      await vi.advanceTimersByTimeAsync(49)
      await tick()
      expect(done).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(done).toBe(true)
      await p
    })
  })

  describe('_classifyNetworkError', () => {
    it('matches every network/DNS/proxy signature and wraps with the prefix', async () => {
      const b = await loadHarness()
      const signatures = [
        'ERR_NAME_NOT_RESOLVED no such host',
        'ERR_INTERNET_DISCONNECTED',
        'ERR_CONNECTION_REFUSED 127.0.0.1:9333',
        'ERR_TIMED_OUT after 30s',
        'ERR_TUNNEL_CONNECTION_FAILED via proxy',
        'net::ERR_FAILED',
      ]
      for (const sig of signatures) {
        const out = b._classifyNetworkError(new Error(sig))
        expect(out).toBeInstanceOf(Error)
        expect(out?.message).toBe('Site unreachable (network/DNS/proxy): ' + sig)
      }
    })

    it('classifies error objects by message and raw strings by String()', async () => {
      const b = await loadHarness()
      expect(b._classifyNetworkError({ message: 'net::ERR_ABORTED mid' })?.message).toBe(
        'Site unreachable (network/DNS/proxy): net::ERR_ABORTED mid',
      )
      expect(b._classifyNetworkError('ERR_TIMED_OUT raw')?.message).toBe(
        'Site unreachable (network/DNS/proxy): ERR_TIMED_OUT raw',
      )
    })

    it('caps the carried diagnostic at 200 characters', async () => {
      const b = await loadHarness()
      const out = b._classifyNetworkError(new Error('net::' + 'x'.repeat(500)))
      expect(out?.message).toBe('Site unreachable (network/DNS/proxy): net::' + 'x'.repeat(195))
      expect(out?.message.length).toBe('Site unreachable (network/DNS/proxy): '.length + 200)
    })

    it('returns exactly null for non-network errors', async () => {
      const b = await loadHarness()
      expect(b._classifyNetworkError(new Error('page closed'))).toBeNull()
      expect(b._classifyNetworkError({ message: 'plain failure' })).toBeNull()
    })
  })

  describe('_looksEmpty', () => {
    it('counts non-blank snapshot lines after the ### Snapshot marker', async () => {
      const b = await loadHarness()
      expect(b._looksEmpty('### Snapshot\n- a\n- b\n- c')).toBe(false)
      expect(b._looksEmpty('### Snapshot\n- a\n- b')).toBe(true)
    })

    it('treats a missing snapshot section as empty', async () => {
      const b = await loadHarness()
      expect(b._looksEmpty('no marker at all\nline\nline')).toBe(true)
    })

    it('a marker-less input with many non-blank lines still takes the empty fallback', async () => {
      const b = await loadHarness()
      // Pins the `|| ""` branch: split("### Snapshot")[1] is undefined and the
      // body must fall back to empty, not to the pre-marker text.
      expect(b._looksEmpty('- a\n- b\n- c\n- d\nno marker here')).toBe(true)
    })

    it('ignores whitespace-only lines at the threshold', async () => {
      const b = await loadHarness()
      expect(b._looksEmpty('### Snapshot\n   \n\t\n- only')).toBe(true)
      expect(b._looksEmpty('### Snapshot\n\n  \n- one\n- two\n- three')).toBe(false)
    })
  })

  describe('goto three-stage health check', () => {
    it('healthy page: one navigate, snapshot after exactly 600ms, no retry', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      const p = b.goto('http://example.com/a')
      await vi.advanceTimersByTimeAsync(599)
      await tick()
      expect(tool('browser_snapshot')).not.toHaveBeenCalled()
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(tool('browser_navigate')).toHaveBeenCalledTimes(1)
      expect(tool('browser_navigate')).toHaveBeenCalledWith({ url: 'http://example.com/a' })
      expect(tool('browser_snapshot')).toHaveBeenCalledTimes(1)
      await p
    })

    it('empty settle → recheck after exactly 3000ms heals without a reload', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      const snap = given('browser_snapshot')
      snap.mockResolvedValueOnce(EMPTY_YAML).mockResolvedValueOnce(PAGE_YAML)
      const p = b.goto('http://example.com/a')
      await vi.advanceTimersByTimeAsync(600)
      await tick()
      expect(snap).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(2999)
      await tick()
      expect(snap).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      await p
      expect(tool('browser_navigate')).toHaveBeenCalledTimes(1)
      expect(snap).toHaveBeenCalledTimes(2)
    })

    it('still empty → reload then wait exactly 5000ms before the verdict', async () => {
      const b = await loadHarness()
      const nav = given('browser_navigate').mockResolvedValue(undefined)
      const snap = given('browser_snapshot')
      snap.mockResolvedValueOnce(EMPTY_YAML).mockResolvedValueOnce(EMPTY_YAML).mockResolvedValueOnce(PAGE_YAML)
      const p = b.goto('http://example.com/a')
      await vi.advanceTimersByTimeAsync(600)
      await tick()
      expect(snap).toHaveBeenCalledTimes(1)
      await vi.advanceTimersByTimeAsync(3000)
      await tick()
      expect(snap).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(nav).toHaveBeenCalledTimes(2)
      expect(nav).toHaveBeenLastCalledWith({ url: 'http://example.com/a' })
      await vi.advanceTimersByTimeAsync(4998)
      await tick()
      expect(snap).toHaveBeenCalledTimes(2)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      await p
      expect(snap).toHaveBeenCalledTimes(3)
    })

    it('empty after reload throws the diagnostic message with the URL', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      given('browser_snapshot').mockResolvedValue(EMPTY_YAML)
      const err = await settle(expectReject(b.goto('http://example.com/dead')))
      expect(err).toBeInstanceOf(Error)
      expect(err.message).toBe(
        'Page loaded but returned empty content for http://example.com/dead. Possible causes: JS failed to render, ' +
          'anti-bot measures, or a tunnel/proxy issue. Try a different URL or approach.',
      )
      expect(tool('browser_navigate')).toHaveBeenCalledTimes(2)
      expect(tool('browser_snapshot')).toHaveBeenCalledTimes(3)
    })

    it('network failures during navigate are wrapped, before any snapshot', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockRejectedValue(new Error('net::ERR_NAME_NOT_RESOLVED zzz'))
      const err = await settle(expectReject(b.goto('http://down.example/')))
      expect(err.message).toBe('Site unreachable (network/DNS/proxy): net::ERR_NAME_NOT_RESOLVED zzz')
      // Only navigate was ever touched: the health check never ran.
      expect(Object.keys(tools)).toEqual([PREFIX + 'browser_navigate'])
    })

    it('non-network navigate errors pass through unchanged (same error object)', async () => {
      const b = await loadHarness()
      const original = new Error('page closed mid-navigation')
      given('browser_navigate').mockRejectedValue(original)
      const err = await settle(expectReject(b.goto('http://example.com/')))
      expect(err).toBe(original)
    })

    it('non-http URLs skip the health check entirely', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      const snap = given('browser_snapshot').mockResolvedValue(EMPTY_YAML)
      await settle(b.goto('about:blank'))
      expect(tool('browser_navigate')).toHaveBeenCalledTimes(1)
      expect(snap).not.toHaveBeenCalled()
    })

    it('non-http schemes containing "http" still skip the health check', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      const snap = given('browser_snapshot').mockResolvedValue(EMPTY_YAML)
      await settle(b.goto('not-http://x'))
      expect(tool('browser_navigate')).toHaveBeenCalledTimes(1)
      expect(snap).not.toHaveBeenCalled()
    })

    it('the http(s) health check ignores URL case', async () => {
      const b = await loadHarness()
      given('browser_navigate').mockResolvedValue(undefined)
      const snap = given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      await settle(b.goto('HTTP://example.com/a'))
      expect(snap).toHaveBeenCalledTimes(1)
    })

    it('desktop mode re-applies the CDP overrides after every navigation', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      given('browser_navigate').mockResolvedValue(undefined)
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      expect(await b.setMode('desktop')).toBe('desktop')
      expect(b.mode()).toBe('desktop')
      expect(runCode).toHaveBeenCalledTimes(1)
      await settle(b.goto('http://example.com/'))
      expect(runCode).toHaveBeenCalledTimes(2)
      expect(runCode).toHaveBeenNthCalledWith(2, { code: DESKTOP_CODE })
    })

    it('mobile mode never re-applies overrides on goto', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      given('browser_navigate').mockResolvedValue(undefined)
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      await settle(b.goto('http://example.com/'))
      expect(runCode).not.toHaveBeenCalled()
    })
  })

  describe('setMode / mode / _applyMode', () => {
    it('fresh harness defaults to mobile', async () => {
      const b = await loadHarness()
      expect(b.mode()).toBe('mobile')
    })

    it('mode() falls back to mobile when the state marker is cleared', async () => {
      const b = await loadHarness()
      b._mode = ''
      expect(b.mode()).toBe('mobile')
    })

    it('setMode("desktop") emits the exact desktop CDP program', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      expect(await b.setMode('desktop')).toBe('desktop')
      expect(runCode).toHaveBeenCalledTimes(1)
      expect(runCode).toHaveBeenCalledWith({ code: DESKTOP_CODE })
    })

    it('setMode("mobile") emits the exact Pixel 8 CDP program', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      expect(await b.setMode('mobile')).toBe('mobile')
      expect(runCode).toHaveBeenCalledWith({ code: MOBILE_CODE })
    })

    it('scale 0 falls back to pageScaleFactor 1', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      await b._applyMode({ ua: 'u', w: 100, h: 200, dsf: 1, mobile: false, scale: 0 })
      const code = runCode.mock.calls[0][0] as { code: string }
      expect(code.code).toContain('pageScaleFactor: 1')
    })

    it('mode is sticky: setting mobile after desktop keeps returning mobile', async () => {
      const b = await loadHarness()
      given('browser_run_code_unsafe').mockResolvedValue('ok')
      await b.setMode('desktop')
      expect(await b.setMode('mobile')).toBe('mobile')
      expect(b.mode()).toBe('mobile')
    })

    it('unknown modes throw before any CDP call and leave the mode unchanged', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue('ok')
      const err = await expectReject(b.setMode('tablet'))
      expect(err.message).toBe('unknown mode: tablet')
      expect(runCode).not.toHaveBeenCalled()
      expect(b.mode()).toBe('mobile')
      await expectReject(b.setMode(''))
      expect(runCode).not.toHaveBeenCalled()
      expect(b.mode()).toBe('mobile')
    })
  })

  describe('find', () => {
    it('returns capture group 1 of the first match', async () => {
      const b = await loadHarness()
      expect(b.find(PAGE_YAML, /ref=(e\d+)/)).toBe('e12')
    })

    it('throws with pattern context and a 500-char yaml head on a miss', async () => {
      const b = await loadHarness()
      const yaml = 'y'.repeat(600)
      let msg = ''
      try {
        b.find(yaml, /nope/)
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe('ref not found for /nope/ in:\n' + yaml.slice(0, 500))
    })
  })

  describe('tab bookkeeping', () => {
    it('_tabIndices parses real "- N:" indices, ignoring lookalike lines', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs').mockResolvedValue('  - 0: about:blank\n- 12: http://x\n-  33: two spaces\n\tx - 3: nope')
      expect(await b._tabIndices()).toEqual([0, 12, 33])
      expect(tabs).toHaveBeenCalledWith({ action: 'list' })
    })

    it('_tabIndices falls back to [0] when listing fails', async () => {
      const b = await loadHarness()
      given('browser_tabs').mockRejectedValue(new Error('no browser'))
      expect(await b._tabIndices()).toEqual([0])
    })

    it('_tabCount counts parsed indices and defaults to 1 for an empty list', async () => {
      const b = await loadHarness()
      given('browser_tabs').mockResolvedValue('- 0: a\n- 12: b')
      expect(await b._tabCount()).toBe(2)
      given('browser_tabs').mockResolvedValue('no tabs here')
      expect(await b._tabCount()).toBe(1)
    })

    it('_maybeSwitchNewTab selects the highest fresh index and reports the switch', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs').mockResolvedValue('- 0: a\n- 3: b')
      expect(await settle(b._maybeSwitchNewTab([0]))).toBe(true)
      expect(tabs).toHaveBeenCalledWith({ action: 'select', index: 3 })
    })

    it('picks the maximum among several fresh tabs', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs').mockResolvedValue('- 2: a\n- 5: b\n- 7: c')
      expect(await settle(b._maybeSwitchNewTab([2]))).toBe(true)
      expect(tabs).toHaveBeenCalledWith({ action: 'select', index: 7 })
    })

    it('no fresh tabs → false and no select call', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs').mockResolvedValue('- 0: a\n- 1: b')
      expect(await settle(b._maybeSwitchNewTab([0, 1]))).toBe(false)
      expect(tabs).toHaveBeenCalledTimes(1) // only the list call
    })

    it('detection failures are swallowed and return false', async () => {
      const b = await loadHarness()
      given('browser_tabs').mockRejectedValue(new Error('boom'))
      expect(await settle(b._maybeSwitchNewTab([0]))).toBe(false)
    })

    it('tabBaseline stores the count', async () => {
      const b = await loadHarness()
      given('browser_tabs').mockResolvedValue('- 0: a\n- 1: b')
      expect(await b.tabBaseline()).toBe(2)
      expect(b._baseTabs).toBe(2)
    })

    it('tabCleanup closes task tabs in descending index order, keeps the baseline', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs')
      tabs.mockResolvedValueOnce('- 0: a\n- 1: b\n- 2: c') // baseline: three tabs
      await b.tabBaseline()
      tabs.mockResolvedValue('- 0: a\n- 1: b\n- 2: c\n- 4: d\n- 5: e')
      expect(await settle(b.tabCleanup())).toBe('closed 2 task tab(s), baseline kept')
      const closeCalls = tabs.mock.calls.filter((c) => (c[0] as { action: string }).action === 'close')
      expect(closeCalls.map((c) => (c[0] as { index: number }).index)).toEqual([5, 4])
    })

    it('tabCleanup without a baseline keeps only tab 0', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs').mockResolvedValue('- 0: a\n- 4: b')
      expect(await settle(b.tabCleanup())).toBe('closed 1 task tab(s), baseline kept')
      expect(tabs).toHaveBeenCalledWith({ action: 'close', index: 4 })
    })

    it('tabCleanup resets the baseline: the next run closes above tab 0 again', async () => {
      const b = await loadHarness()
      const tabs = given('browser_tabs')
      tabs.mockResolvedValueOnce('- 0: a') // baseline: one tab
      await b.tabBaseline()
      tabs.mockResolvedValueOnce('- 0: a\n- 1: b')
      expect(await settle(b.tabCleanup())).toBe('closed 1 task tab(s), baseline kept')
      tabs.mockResolvedValue('- 0: a\n- 5: new')
      expect(await settle(b.tabCleanup())).toBe('closed 1 task tab(s), baseline kept')
      expect(tabs).toHaveBeenCalledWith({ action: 'close', index: 5 })
    })
  })

  describe('click', () => {
    it('snapshots fresh, resolves the ref, clicks, reports plain success', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      given('browser_tabs').mockResolvedValue('- 0: a')
      const click = given('browser_click').mockResolvedValue('ok')
      expect(await settle(b.click('Go', /ref=(e\d+)/))).toBe('clicked')
      expect(click).toHaveBeenCalledWith({ element: 'Go', target: 'e12' })
    })

    it('auto-switches to a new tab when the click opened one', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      const tabs = given('browser_tabs')
      tabs.mockResolvedValueOnce('- 0: a') // before
      tabs.mockResolvedValue('- 0: a\n- 2: b') // after + select result
      given('browser_click').mockResolvedValue('ok')
      expect(await settle(b.click('Go', /ref=(e\d+)/))).toBe('clicked; auto-switched to new tab')
      expect(tabs).toHaveBeenCalledWith({ action: 'select', index: 2 })
    })

    it('dropdown-shaped click failures gain the select hint', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      given('browser_tabs').mockResolvedValue('- 0: a')
      given('browser_click').mockRejectedValue(new Error('strict mode violation: combobox'))
      const err = await settle(expectReject(b.click('Tags', /ref=(e\d+)/)))
      expect(err.message).toBe(
        'Error: strict mode violation: combobox' +
          '\nHint: this looks like a dropdown — use browser.select(desc, pattern, values) instead of click.',
      )
    })

    it('other click failures rethrow untouched', async () => {
      const b = await loadHarness()
      const original = new Error('element detached from DOM')
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      given('browser_tabs').mockResolvedValue('- 0: a')
      given('browser_click').mockRejectedValue(original)
      const err = await settle(expectReject(b.click('Go', /ref=(e\d+)/)))
      expect(err.message).toBe('element detached from DOM')
    })
  })

  describe('type', () => {
    it('types into the resolved ref with submit coercion and verifies the value', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      const type = given('browser_type').mockResolvedValue('ok')
      const evaluate = given('browser_evaluate').mockResolvedValue('abc')
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc'))).toBe('typed')
      expect(type).toHaveBeenCalledWith({ element: 'Search', target: 'e7', text: 'abc', submit: false })
      expect(evaluate).toHaveBeenCalledWith({
        function: "() => { const el = document.activeElement; return el ? String(el.value ?? '') : ''; }",
      })
    })

    it('submit flag is forwarded as true', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      const type = given('browser_type').mockResolvedValue('ok')
      given('browser_evaluate').mockResolvedValue('abc')
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc', true))).toBe('typed')
      expect(type).toHaveBeenCalledWith({ element: 'Search', target: 'e7', text: 'abc', submit: true })
    })

    it('warns when the field holds something other than what was typed', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      given('browser_type').mockResolvedValue('ok')
      given('browser_evaluate').mockResolvedValue('zzz')
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc'))).toBe(
        "typed. WARNING: field's actual value 'zzz' differs from typed text — the page may have reformatted or autocompleted your input.",
      )
    })

    it('empty verification result is not a mismatch', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      given('browser_type').mockResolvedValue('ok')
      given('browser_evaluate').mockResolvedValue('')
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc'))).toBe('typed')
    })

    it('a failed post-check after submit resolves as typed (navigation killed the context)', async () => {
      // submit=true can trigger navigation that destroys the evaluation
      // context or moves focus to body — the type call itself succeeded, so
      // the post-check must not turn that into a failure.
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      given('browser_type').mockResolvedValue('ok')
      given('browser_evaluate').mockRejectedValue(new Error('Execution context was destroyed'))
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc', true))).toBe('typed')
    })

    it('a failed post-check without submit also resolves as typed (best-effort check)', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Search" [ref=e7]')
      given('browser_type').mockResolvedValue('ok')
      given('browser_evaluate').mockRejectedValue(new Error('page closed'))
      expect(await settle(b.type('Search', /ref=(e\d+)/, 'abc'))).toBe('typed')
    })

    it('autocomplete fields wait 400ms and return the click-instead-of-Enter note', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue('### Snapshot\n- textbox "Tags" [ref=e9] roles=combobox')
      const type = given('browser_type').mockResolvedValue('ok')
      const evaluate = given('browser_evaluate').mockResolvedValue('zzz')
      let done = false
      const p = b.type('Tags', /ref=(e\d+)/, 'x').then((r) => {
        done = true
        return r
      })
      await vi.advanceTimersByTimeAsync(399)
      await tick()
      expect(done).toBe(false)
      await vi.advanceTimersByTimeAsync(1)
      await tick()
      expect(done).toBe(true)
      expect(await p).toBe(
        'typed. NOTE: autocomplete field — wait for suggestions and CLICK the right one instead of pressing Enter.',
      )
      expect(type).toHaveBeenCalledWith({ element: 'Tags', target: 'e9', text: 'x', submit: false })
      expect(evaluate).not.toHaveBeenCalled()
    })
  })

  describe('select', () => {
    it('selects the given values on the resolved ref', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      const select = given('browser_select_option').mockResolvedValue('ok')
      await settle(b.select('Search', /ref=(e\d+)/, ['a', 'b']))
      expect(select).toHaveBeenCalledWith({ element: 'Search', target: 'e12', values: ['a', 'b'] })
    })
  })

  describe('waitText / waitGone', () => {
    it('waitText polls fresh snapshots until the text appears', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot')
      snap.mockResolvedValueOnce('nothing').mockResolvedValueOnce('still nothing').mockResolvedValueOnce('found zz here')
      expect(await settle(b.waitText('zz'))).toBe(true)
      expect(snap).toHaveBeenCalledTimes(3)
    })

    it('waitText throws its timeout message after the full budget', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot').mockResolvedValue('nothing')
      const err = await settle(expectReject(b.waitText('zz', 3)))
      expect(err.message).toBe('waitText timeout: zz')
      expect(snap).toHaveBeenCalledTimes(3)
    })

    it('waitText default budget is ten tries', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot')
      for (let i = 0; i < 9; i++) snap.mockResolvedValueOnce('nothing')
      snap.mockResolvedValueOnce('found zz')
      expect(await settle(b.waitText('zz'))).toBe(true)
      expect(snap).toHaveBeenCalledTimes(10)
    })

    it('waitGone succeeds once the text disappears', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot')
      snap.mockResolvedValueOnce('has zz').mockResolvedValueOnce('clean now')
      expect(await settle(b.waitGone('zz'))).toBe(true)
      expect(snap).toHaveBeenCalledTimes(2)
    })

    it('waitGone times out naming the lingering text', async () => {
      const b = await loadHarness()
      const snap = given('browser_snapshot').mockResolvedValue('has zz')
      const err = await settle(expectReject(b.waitGone('zz', 2)))
      expect(err.message).toBe('waitGone timeout: zz')
      expect(snap).toHaveBeenCalledTimes(2)
    })
  })

  describe('evaluate', () => {
    it('repairs LLM escaping bugs into browser-ready code', async () => {
      const b = await loadHarness()
      const evaluate = given('browser_evaluate').mockResolvedValue('### Result')
      const input = [
        'async () => {',
        '  const q = \\"#top\\";',
        '  const re = \\\\d{2}\\\\s;',
        '  const esc = \\\\.x\\\\?;',
        '  const r1 = document.evaluate(\\"//h1\\", document, null);',
        '  const r2 = document.querySelector(\\".item\\");',
        '  const r3 = document.querySelectorAll(\\"div > span\\");',
        '  const r4 = node.closest(\\"[data-k]\\");',
        '  const r5 = node.matches(\\":visible\\");',
        // Spaced variants: each \s* in the repair regexes must stay flexible.
        '  const s1 = document.evaluate (\\"//h7\\", document, null);',
        '  const s2 = document.evaluate( \\"//h8\\" , document, null);',
        '  const s3 = document.querySelector ( \\".q\\" );',
        '  const s4 = document.querySelectorAll( \\".w\\" );',
        '  const s5 = node.closest ( \\"[d]\\" );',
        '  const s6 = node.matches( \\":v\\" );',
        '  return [q, re, esc, r1, r2, r3, r4, r5, s1, s2, s3, s4, s5, s6];',
        '}',
      ].join('\n')
      const expected = [
        'async () => {',
        '  const q = "#top";',
        '  const re = \\d{2}\\s;',
        '  const esc = \\.x\\?;',
        '  const r1 = document.evaluate(`//h1`, document, null);',
        '  const r2 = document.querySelector(`.item`);',
        '  const r3 = document.querySelectorAll(`div > span`);',
        '  const r4 = node.closest(`[data-k]`);',
        '  const r5 = node.matches(`:visible`);',
        // The repair consumes the whitespace it matches, so spaced calls become
        // compact backticked calls — every \s* must stay a \s* for that.
        '  const s1 = document.evaluate(`//h7`, document, null);',
        '  const s2 = document.evaluate(`//h8`, document, null);',
        '  const s3 = document.querySelector(`.q`);',
        '  const s4 = document.querySelectorAll(`.w`);',
        '  const s5 = node.closest(`[d]`);',
        '  const s6 = node.matches(`:v`);',
        '  return [q, re, esc, r1, r2, r3, r4, r5, s1, s2, s3, s4, s5, s6];',
        '}',
      ].join('\n')
      await b.evaluate(input)
      expect(evaluate).toHaveBeenCalledWith({ function: expected })
    })

    it('masks inline data images from the result', async () => {
      const b = await loadHarness()
      given('browser_evaluate').mockResolvedValue('before data:image/png;base64,iVBOR xxx after')
      expect(await b.evaluate('f')).toBe('before [Image] xxx after')
    })

    it('truncates results over 20000 characters with the marker', async () => {
      const b = await loadHarness()
      given('browser_evaluate').mockResolvedValue('x'.repeat(20001))
      expect(await b.evaluate('f')).toBe('x'.repeat(19950) + '\n... [Truncated after 20000 characters]')
    })

    it('keeps a 20000-character result intact', async () => {
      const b = await loadHarness()
      given('browser_evaluate').mockResolvedValue('x'.repeat(20000))
      expect(await b.evaluate('f')).toBe('x'.repeat(20000))
    })

    it('coerces tool results to string', async () => {
      const b = await loadHarness()
      given('browser_evaluate').mockResolvedValue(42)
      expect(await b.evaluate('f')).toBe('42')
    })
  })

  describe('shot', () => {
    it('requests a png under the .playwright-mcp root', async () => {
      const b = await loadHarness()
      const shot = given('browser_take_screenshot').mockResolvedValue('saved')
      expect(await b.shot('p1')).toBe('saved')
      expect(shot).toHaveBeenCalledWith({ filename: '.playwright-mcp/p1', type: 'png' })
    })

    it('sanitizes hostile names into safe path segments', async () => {
      const b = await loadHarness()
      const shot = given('browser_take_screenshot').mockResolvedValue('saved')
      await b.shot('a/b:c*d')
      expect(shot).toHaveBeenCalledWith({ filename: '.playwright-mcp/a_b_c_d', type: 'png' })
      await b.shot('../../etc/passwd')
      expect(shot).toHaveBeenLastCalledWith({ filename: '.playwright-mcp/.._.._etc_passwd', type: 'png' })
      expect(b._safeName('ok-name.1_2')).toBe('ok-name.1_2')
    })
  })

  describe('_parseEval', () => {
    it('cuts at the ### section boundary before hunting quotes, double-parses', async () => {
      const b = await loadHarness()
      // Brace-free payload: if the ### cut or quote positions broke, the
      // braces fallback must NOT rescue the parse (there are no braces).
      const raw =
        String.raw`### Result` +
        '\n' +
        String.raw`"[\"a\",\"b\"]"` +
        '\n' +
        String.raw`### Ran Playwright code` +
        '\n' +
        'noisy "quotes" in the echo'
      const out = b._parseEval(raw)
      expect(out).toEqual(['a', 'b'])
    })

    it('the quoted form parses first, with no braces fallback to rescue mutants', async () => {
      const b = await loadHarness()
      // Quote-free tail: if the quote-slice breaks, nothing else can succeed.
      expect(b._parseEval('"[9]" trailing')).toEqual([9])
    })

    it('a bare quote is not valid double JSON and throws (guard stays strict)', async () => {
      const b = await loadHarness()
      // a=0, b=0: the b>a guard must reject the 1-char quote slice instead of
      // letting JSON.parse('"') succeed with the string quote.
      let msg = ''
      try {
        b._parseEval('"')
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe('unparseable evaluate result: "')
    })

    it('a single quote after text also throws rather than parsing the quote char', async () => {
      const b = await loadHarness()
      let msg = ''
      try {
        b._parseEval('x"')
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe('unparseable evaluate result: x"')
    })

    it('falls back to the outer braces when the quoted form is not double JSON', async () => {
      const b = await loadHarness()
      expect(b._parseEval('x "oops" {"k":7}')).toEqual({ k: 7 })
    })

    it('parses plain JSON objects directly', async () => {
      const b = await loadHarness()
      expect(b._parseEval('{"a": [1, 2], "b": true}')).toEqual({ a: [1, 2], b: true })
    })

    it('throws naming a 120-char head slice for unparseable results', async () => {
      const b = await loadHarness()
      let msg = ''
      try {
        b._parseEval('q'.repeat(130))
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe('unparseable evaluate result: ' + 'q'.repeat(120))
    })

    it('stringifies non-string raw input before parsing', async () => {
      const b = await loadHarness()
      let msg = ''
      try {
        b._parseEval(42 as unknown as string)
      } catch (e) {
        msg = (e as Error).message
      }
      expect(msg).toBe('unparseable evaluate result: 42')
    })
  })

  describe('vshot', () => {
    it('shots, then reads viewport dimensions through _parseEval', async () => {
      const b = await loadHarness()
      const shot = given('browser_take_screenshot').mockResolvedValue('saved')
      const evaluate = given('browser_evaluate').mockResolvedValue(
        String.raw`### Result` + '\n' + String.raw`"{"vw":800,"vh":1200}"`,
      )
      expect(await b.vshot('vs1')).toEqual({ file: '.playwright-mcp/vs1', vw: 800, vh: 1200 })
      expect(shot).toHaveBeenCalledWith({ filename: '.playwright-mcp/vs1', type: 'png' })
      expect(evaluate).toHaveBeenCalledWith({
        function: '() => JSON.stringify({ vw: window.innerWidth, vh: window.innerHeight })',
      })
    })

    it('sanitizes the file name like shot', async () => {
      const b = await loadHarness()
      const shot = given('browser_take_screenshot').mockResolvedValue('saved')
      given('browser_evaluate').mockResolvedValue(
        String.raw`### Result` + '\n' + String.raw`"{"vw":412,"vh":839}"`,
      )
      expect(await b.vshot('top view')).toEqual({ file: '.playwright-mcp/top_view', vw: 412, vh: 839 })
      expect(shot).toHaveBeenCalledWith({ filename: '.playwright-mcp/top_view', type: 'png' })
    })
  })

  describe('vclick', () => {
    function evalReply(payload: Record<string, unknown>): string {
      // JSON.stringify of the JSON string already carries the outer quotes.
      return String.raw`### Result` + '\n' + JSON.stringify(JSON.stringify(payload))
    }

    it('rejects fractions outside [0,1] naming them', async () => {
      const b = await loadHarness()
      const err = await expectReject(b.vclick(-0.1, 0.5, 'x'))
      expect(err.message).toBe('vclick fractions out of range: (-0.1, 0.5) — use values in [0,1] from the vshot image')
      const err2 = await expectReject(b.vclick(0.5, 1.5, 'x'))
      expect(err2.message).toBe('vclick fractions out of range: (0.5, 1.5) — use values in [0,1] from the vshot image')
      const err3 = await expectReject(b.vclick(1.5, 0.5, 'x'))
      expect(err3.message).toBe('vclick fractions out of range: (1.5, 0.5) — use values in [0,1] from the vshot image')
      const err4 = await expectReject(b.vclick(0.5, -0.1, 'x'))
      expect(err4.message).toBe('vclick fractions out of range: (0.5, -0.1) — use values in [0,1] from the vshot image')
    })

    it('accepts the exact 0 and 1 boundaries on both axes', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue(evalReply({ x: 0, y: 600, hit: 'body' }))
      given('browser_tabs').mockResolvedValue('- 0: a')
      expect(await settle(b.vclick(0, 1))).toBe('clicked  at (0, 600) → hit <body>')
      expect(runCode).toHaveBeenCalledTimes(1)
      tool('browser_run_code_unsafe').mockResolvedValue(evalReply({ x: 800, y: 0, hit: 'body' }))
      expect(await settle(b.vclick(1, 0))).toBe('clicked  at (800, 0) → hit <body>')
      expect(runCode).toHaveBeenCalledTimes(2)
    })

    it('clicks via page.mouse, reports ground-truth hit, no tab switch', async () => {
      const b = await loadHarness()
      const runCode = given('browser_run_code_unsafe').mockResolvedValue(evalReply({ x: 512, y: 384, hit: 'button#go' }))
      given('browser_tabs').mockResolvedValue('- 0: a')
      expect(await settle(b.vclick(0.5, 0.25, 'Go'))).toBe('clicked Go at (512, 384) → hit <button#go>')
      const code = (runCode.mock.calls[0][0] as { code: string }).code
      expect(code).toContain('const x = Math.round(page.viewportSize().width * 0.5)')
      expect(code).toContain('y = Math.round(page.viewportSize().height * 0.25)')
      expect(code).toContain('await page.mouse.click(x, y)')
      expect(code).toContain('await page.waitForTimeout(200)')
      expect(code).toContain('document.elementFromPoint(x, y)')
    })

    it('new-tab detection inside vclick switches to the fresh tab', async () => {
      // vclick samples the tab-index list before the click and again after;
      // a tab that appears mid-click is followed via an auto-select.
      const b = await loadHarness()
      given('browser_run_code_unsafe').mockResolvedValue(evalReply({ x: 10, y: 20, hit: 'a' }))
      const tabs = given('browser_tabs')
      tabs.mockResolvedValueOnce('- 0: a') // indices before
      tabs.mockResolvedValue('- 0: a\n- 4: b') // a new tab opened mid-click
      expect(await settle(b.vclick(0.1, 0.2, 'Link'))).toBe(
        'clicked Link at (10, 20) → hit <a>; auto-switched to new tab',
      )
      const selects = tabs.mock.calls.filter((c) => (c[0] as { action: string }).action === 'select').map((c) => c[0])
      expect(selects).toEqual([{ action: 'select', index: 4 }])
    })

    it('reports nothing-at-point instead of clicking blind', async () => {
      const b = await loadHarness()
      given('browser_run_code_unsafe').mockResolvedValue(evalReply({ x: 512, y: 384, hit: 'none' }))
      given('browser_tabs').mockResolvedValue('- 0: a')
      const err = await settle(expectReject(b.vclick(0.5, 0.25, 'Go')))
      expect(err.message).toBe('vclick: nothing at (512, 384) — the point is outside the viewport')
    })
  })

  describe('snapshot', () => {
    it('stringifies the tool result', async () => {
      const b = await loadHarness()
      given('browser_snapshot').mockResolvedValue(PAGE_YAML)
      const snap = tool('browser_snapshot')
      expect(await b.snapshot()).toBe(PAGE_YAML)
      expect(snap).toHaveBeenCalledWith({})
    })
  })
})
