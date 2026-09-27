import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest'
import {
  collectArtifactWrites,
  artifactURL,
  formatArtifactSize,
  createArtifactCard,
  createArtifactSheet,
  fetchArtifactList,
  isGLTFArtifactName,
  isWalkToggleEnabled,
} from './artifact'
import { t } from './i18n'
import { __record } from './model_viewer_loader'
import type { Block } from './model'

// The component bundle import is the sheet's external dependency seam —
// stubbed with a recording factory. __record is the single mechanism tests
// read; the real module loads Google's <model-viewer> bundle from the
// daemon, which jsdom must never do (no WebGL).
vi.mock('./model_viewer_loader', () => {
  const loads: string[] = []
  return {
    __record: { loads },
    MODEL_VIEWER_URL: '/assets/model-viewer.esm.js',
    loadModelViewer: () => {
      loads.push('load')
      return Promise.resolve()
    },
  }
})

function toolBlock(
  name: string,
  summary: string,
  state: 'running' | 'done' | 'error',
  children: Block[] = [],
): Block {
  return {
    kind: 'tool',
    id: name + '-' + summary + '-' + state + '-' + Math.random().toString(36).slice(2),
    name,
    summary,
    isSearch: false,
    isRead: false,
    isList: false,
    isLsp: false,
    isWeb: false,
    state,
    timingNs: 0,
    displayOutput: '',
    startedAt: 0,
    children,
  }
}

describe('collectArtifactWrites', () => {
  it('Write done with absolute artifacts path produces one card', () => {
    const blocks = [toolBlock('Write', '/home/u/.gbot/projects/abc/artifacts/game.html', 'done')]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: false },
    ])
  })

  it('legacy relative artifacts/ prefix still produces a card (old transcripts)', () => {
    const blocks = [toolBlock('Write', 'artifacts/game.html', 'done')]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: false },
    ])
  })

  it('absolute and legacy forms of the same file dedupe to one card', () => {
    const blocks = [
      toolBlock('Write', 'artifacts/game.html', 'done'),
      toolBlock('Edit', '/home/u/.gbot/projects/abc/artifacts/game.html', 'done'),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: true },
    ])
  })

  it('Edit done marks updated', () => {
    const blocks = [toolBlock('Edit', '/pd/artifacts/game.html', 'done')]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: true },
    ])
  })

  it('same-name Write then Edit dedupes to one card, updated from the LAST write', () => {
    const blocks = [
      toolBlock('Write', '/pd/artifacts/game.html', 'done'),
      toolBlock('Edit', '/pd/artifacts/game.html', 'done'),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: true },
    ])
  })

  it('same-name Edit then Write dedupes to one card, updated from the LAST write', () => {
    const blocks = [
      toolBlock('Edit', '/pd/artifacts/game.html', 'done'),
      toolBlock('Write', '/pd/artifacts/game.html', 'done'),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: false },
    ])
  })

  it('running and error tool states produce no card', () => {
    const blocks = [
      toolBlock('Write', '/pd/artifacts/running.html', 'running'),
      toolBlock('Write', '/pd/artifacts/failed.html', 'error'),
      toolBlock('Edit', '/pd/artifacts/failed-edit.html', 'error'),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([])
  })

  it('non-artifact paths and Read tool produce no card', () => {
    const blocks = [
      toolBlock('Write', 'src/main.go', 'done'),
      toolBlock('Write', '/abs/src/main.go', 'done'),
      toolBlock('Write', '/home/u/game.html', 'done'),
      // "myartifacts" must not match the /artifacts/ segment.
      toolBlock('Write', '/x/myartifacts/foo.html', 'done'),
      toolBlock('Read', '/pd/artifacts/game.html', 'done'),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([])
  })

  it('Write nested in tool children (sub-agent) produces a card', () => {
    const blocks = [
      toolBlock('Agent', 'explore', 'done', [
        toolBlock('Write', '/pd/artifacts/game.html', 'done'),
      ]),
    ]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'game.html', updated: false },
    ])
  })

  it('nested path keeps directory prefix in name', () => {
    const blocks = [toolBlock('Write', '/pd/artifacts/sub/game.html', 'done')]
    expect(collectArtifactWrites(blocks)).toEqual([
      { name: 'sub/game.html', updated: false },
    ])
  })
})

describe('artifactURL', () => {
  it('simple name maps to /artifacts/<name>', () => {
    expect(artifactURL('game.html')).toBe('/artifacts/game.html')
  })

  it('each segment is URI-encoded', () => {
    expect(artifactURL('a b/c.html')).toBe('/artifacts/a%20b/c.html')
  })
})

describe('formatArtifactSize', () => {
  it.each([
    [0, '0 B'],
    [999, '999 B'],
    [14541, '14.2 KB'],
    [5 * 1024 * 1024, '5.0 MB'],
  ])('%d bytes formats to %s', (bytes, expected) => {
    expect(formatArtifactSize(bytes)).toBe(expected)
  })
})

function stubFetchWithLength(len: string) {
  vi.stubGlobal(
    'fetch',
    vi.fn(async () => ({
      ok: true,
      headers: new Headers({ 'content-length': len }),
    })),
  )
}

afterEach(() => {
  vi.unstubAllGlobals()
})

describe('createArtifactCard', () => {
  it('renders preview iframe, title, and size from HEAD content-length', async () => {
    stubFetchWithLength('14541')
    const card = createArtifactCard({ name: 'game.html', updated: false }, () => {})

    const iframe = card.querySelector('.card-preview iframe') as HTMLIFrameElement
    // jsdom absolutizes the .src IDL property — the attribute keeps the literal.
    expect(iframe.getAttribute('src')).toBe('/artifacts/game.html')
    expect(iframe.getAttribute('loading')).toBe('lazy')
    expect(iframe.getAttribute('tabindex')).toBe('-1')
    expect((card.querySelector('.card-title') as HTMLElement).textContent).toBe('game.html')
    await vi.waitFor(() => {
      expect(card.querySelector('.card-meta-inline')?.textContent).toContain('14.2 KB')
    })
  })

  it('fetch HEAD url is the artifact url', () => {
    const fetchMock = vi.fn(async () => ({
      ok: false,
      headers: new Headers(),
    }))
    vi.stubGlobal('fetch', fetchMock)
    createArtifactCard({ name: 'game.html', updated: false }, () => {})
    expect(fetchMock).toHaveBeenCalledWith(
      '/artifacts/game.html',
      expect.objectContaining({ method: 'HEAD' }),
    )
  })

  it('updated card shows stale class, fresh dot, and Updated text; plain card shows none', async () => {
    stubFetchWithLength('1')
    const stale = createArtifactCard({ name: 'a.html', updated: true }, () => {})
    const fresh = createArtifactCard({ name: 'b.html', updated: false }, () => {})

    expect(stale.classList.contains('stale')).toBe(true)
    expect(stale.querySelector('.card-fresh-dot')).toBeTruthy()
    expect(stale.querySelector('.card-updated')?.textContent).toBe('Updated')
    expect(fresh.classList.contains('stale')).toBe(false)
    expect(fresh.querySelector('.card-fresh-dot')).toBeNull()
    expect(fresh.querySelector('.card-updated')).toBeNull()
  })

  it('nested name shows basename title but full-path iframe src', async () => {
    stubFetchWithLength('10')
    const card = createArtifactCard({ name: 'sub/game.html', updated: false }, () => {})
    expect((card.querySelector('.card-title') as HTMLElement).textContent).toBe('game.html')
    expect((card.querySelector('.card-preview iframe') as HTMLIFrameElement).getAttribute('src')).toBe(
      '/artifacts/sub/game.html',
    )
  })

  it('failed HEAD shows em-dash placeholder', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => {
        throw new Error('network down')
      }),
    )
    const card = createArtifactCard({ name: 'game.html', updated: false }, () => {})
    await vi.waitFor(() => {
      expect(card.querySelector('.card-meta-inline')?.textContent).toContain('—')
    })
  })

  it('click fires onOpen with the artifact name and consumes the stale marker', () => {
    stubFetchWithLength('1')
    const opened: string[] = []
    const card = createArtifactCard({ name: 'game.html', updated: true }, (name) => {
      opened.push(name)
    })
    ;(card as HTMLElement).click()
    expect(opened).toEqual(['game.html'])
    expect(card.classList.contains('stale')).toBe(false)
  })
})

// jsdom's default innerHeight is not pinned by any in-repo test — stub it so
// the drag-math expectations below are exact.
function stubViewportHeight(px: number) {
  vi.stubGlobal('innerHeight', px)
}

// Re-assigning the same src does not change the attribute, so attribute
// before/after comparison cannot observe a reload. Spying on the property
// setter is the only reliable signal.
function spyFrameSrcSetter(
  frame: HTMLIFrameElement,
): { calls: unknown[]; restore: () => void } {
  const desc = Object.getOwnPropertyDescriptor(HTMLIFrameElement.prototype, 'src')!
  const calls: unknown[] = []
  Object.defineProperty(frame, 'src', {
    configurable: true,
    get: desc.get,
    set: (v: unknown) => {
      calls.push(v)
      desc.set!.call(frame, v)
    },
  })
  return {
    calls,
    restore: () => {
      delete (frame as Partial<HTMLIFrameElement> & { src?: unknown }).src
    },
  }
}

describe('createArtifactSheet', () => {
  beforeEach(() => {
    __record.loads.length = 0
  })

  function makeSheet() {
    const sheet = createArtifactSheet()
    document.body.appendChild(sheet.root)
    const handle = sheet.root.querySelector('.sheet-handle') as HTMLElement
    const frame = sheet.root.querySelector('iframe') as HTMLIFrameElement
    const pointer = (el: HTMLElement, type: string, clientY: number) => {
      el.dispatchEvent(new PointerEvent(type, { clientY, bubbles: true, cancelable: true }))
    }
    return { sheet, handle, frame, pointer }
  }

  it('starts collapsed at 0px and closed', () => {
    stubViewportHeight(768)
    const { sheet } = makeSheet()
    expect(sheet.root.style.height).toBe('0px')
    expect(sheet.isOpen()).toBe(false)
  })

  it('open sets 70% height and iframe src, no sandbox attribute', () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('game.html')
    expect(sheet.root.style.height).toBe('70%')
    expect(sheet.isOpen()).toBe(true)
    expect(frame.getAttribute('src')).toBe('/artifacts/game.html')
    // Sandbox lives on the response CSP header (allow-scripts
    // allow-same-origin). An attribute without allow-same-origin would
    // re-opaque the origin and kill localStorage.
    expect(frame.getAttribute('sandbox')).toBeNull()
  })

  it('open on a .glb swaps to the 3D host and blanks the iframe', () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('model.glb')
    expect(frame.getAttribute('src')).toBe('about:blank')
    expect(frame.style.display).toBe('none')
    expect((sheet.root.querySelector('.gltf-host') as HTMLElement).style.display).toBe('')
    expect(__record.loads).toEqual(['load'])
  })

  it('MODEL.GLB and scene.gltf take the same branch (case-insensitive extensions)', () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('MODEL.GLB')
    expect(frame.getAttribute('src')).toBe('about:blank')
    sheet.open('scene.gltf')
    expect(frame.getAttribute('src')).toBe('about:blank')
    expect(__record.loads).toEqual(['load', 'load'])
  })

  it('open on a non-glTF artifact keeps the iframe branch, viewer never opens', () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('pic.png')
    expect(frame.getAttribute('src')).toBe('/artifacts/pic.png')
    expect(frame.style.display).toBe('')
    expect((sheet.root.querySelector('.gltf-host') as HTMLElement).style.display).toBe('none')
    expect(__record.loads).toEqual([])
  })

  it('glTF → HTML switch tears the model-viewer down and restores the iframe', async () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('model.glb')
    await vi.waitFor(() => expect(sheet.root.querySelector('model-viewer')).not.toBeNull())
    sheet.open('game.html')
    expect(sheet.root.querySelector('model-viewer')).toBeNull()
    expect(frame.getAttribute('src')).toBe('/artifacts/game.html')
    expect(frame.style.display).toBe('')
    expect((sheet.root.querySelector('.gltf-host') as HTMLElement).style.display).toBe('none')
  })

  it('close() during a pending bundle import keeps the collapsed sheet empty', async () => {
    stubViewportHeight(768)
    const { sheet } = makeSheet()
    sheet.open('model.glb')
    // Collapse before the import continuation runs: the !opened guard must
    // suppress the late mount into the collapsed sheet.
    sheet.close()
    // Deterministic microtask drain: the import continuation is microtask
    // work, so draining beats a timeout wait (weak scanner bans those).
    for (let i = 0; i < 5; i++) await Promise.resolve()
    expect(sheet.root.querySelector('model-viewer')).toBeNull()
    expect(sheet.root.style.height).toBe('0px')
  })

  it('reload with a glTF current remounts it with the same URL', async () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('model.glb')
    const spy = spyFrameSrcSetter(frame)
    sheet.reload()
    // Viewer reload is a remount, not a frame src touch.
    expect(spy.calls).toHaveLength(0)
    // The component module is already registered in the page — the remount
    // does not re-import it.
    expect(__record.loads).toEqual(['load'])
    spy.restore()
  })

  it('single click on handle (no movement) collapses the sheet', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 100)
    pointer(handle, 'pointerup', 100)
    expect(sheet.root.style.height).toBe('0px')
    expect(sheet.isOpen()).toBe(false)
  })

  it('dragging down grows height continuously and stays open on release', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 500)
    pointer(handle, 'pointermove', 600)
    // 70 + 100/768*100 = 83.02 → rounded before writing.
    expect(sheet.root.style.height).toBe('83%')
    pointer(handle, 'pointerup', 600)
    expect(sheet.isOpen()).toBe(true)
    expect(sheet.root.style.height).toBe('83%')
  })

  it('release below 15% collapses the sheet', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 500)
    // 70 - 450/768*100 = 11.4% — above the drag floor, below the close line.
    pointer(handle, 'pointermove', 50)
    expect(sheet.root.style.height).toBe('11%')
    pointer(handle, 'pointerup', 50)
    expect(sheet.root.style.height).toBe('0px')
    expect(sheet.isOpen()).toBe(false)
  })

  it('drag clamps at 92% upward and 10% during drag (release decides close)', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 100)
    pointer(handle, 'pointermove', 600)
    expect(sheet.root.style.height).toBe('92%')

    sheet.open('game.html')
    pointer(handle, 'pointerdown', 700)
    pointer(handle, 'pointermove', 100)
    expect(sheet.root.style.height).toBe('10%')
    // Floor only bounds the drag; release still applies the 15% close rule.
    pointer(handle, 'pointerup', 100)
    expect(sheet.root.style.height).toBe('0px')
  })

  it('reopen after a drag resets to 70% (no height memory)', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 100)
    pointer(handle, 'pointermove', 400)
    expect(sheet.root.style.height).toBe('92%')
    sheet.open('game.html')
    expect(sheet.root.style.height).toBe('70%')
  })

  it('reload re-assigns the iframe src exactly once', () => {
    stubViewportHeight(768)
    const { sheet, frame } = makeSheet()
    sheet.open('game.html')
    const spy = spyFrameSrcSetter(frame)
    sheet.reload()
    expect(spy.calls).toHaveLength(1)
    expect(spy.calls[0]).toBe(frame.src)
    spy.restore()
  })

  it('pointercancel during a drag is treated as release', () => {
    stubViewportHeight(768)
    const { sheet, handle, pointer } = makeSheet()
    sheet.open('game.html')
    pointer(handle, 'pointerdown', 500)
    pointer(handle, 'pointermove', 600)
    pointer(handle, 'pointercancel', 600)
    expect(sheet.root.style.height).toBe('83%')
    expect(sheet.isOpen()).toBe(true)

    sheet.open('game.html')
    pointer(handle, 'pointerdown', 500)
    pointer(handle, 'pointermove', 50)
    pointer(handle, 'pointercancel', 50)
    expect(sheet.root.style.height).toBe('0px')
  })
})

describe('isWalkToggleEnabled', () => {
  it.each([
    ['', true],
    ['?walk-off', false],
    ['?a=1&walk-off=1', false],
    ['?walk-offx=1', true],
    ['?other=walk-off', true],
  ])('%s gates to %s', (search, want) => {
    expect(isWalkToggleEnabled(search)).toBe(want)
  })
})

describe('createArtifactSheet walk mode', () => {
  // Sheets stay attached across a test's assertions (two sheets live at once
  // in the ?walk-off case), so cleanup is deferred to afterEach.
  const sheetRoots: HTMLElement[] = []
  const matchMediaRestores: (() => void)[] = []
  beforeEach(() => {
    __record.loads.length = 0
  })
  afterEach(() => {
    window.history.replaceState(null, '', '/')
    for (const root of sheetRoots) root.remove()
    sheetRoots.length = 0
    for (const restore of matchMediaRestores) restore()
    matchMediaRestores.length = 0
    // jsdom has no maxTouchPoints of its own — restore 0, not a delete
    // (undefined would defeat the `<= 0` desktop guard).
    Object.defineProperty(navigator, 'maxTouchPoints', {
      configurable: true,
      value: 0,
    })
    delete (document as { pointerLockElement?: unknown }).pointerLockElement
  })

  // jsdom defaults: maxTouchPoints 0 (desktop), no pointer lock API. The
  // overrides shadow the properties with configurable own ones so the
  // afterEach delete restores the defaults.
  const setTouchDevice = (points: number) => {
    Object.defineProperty(navigator, 'maxTouchPoints', {
      configurable: true,
      value: points,
    })
    // setWalkHint tests coarse-pointer first; jsdom's matchMedia never
    // matches, so pin it to the fixture's intent.
    const orig = window.matchMedia
    window.matchMedia = ((q: string) =>
      ({ matches: points > 0, media: q, onchange: null, addEventListener: () => {}, removeEventListener: () => {}, addListener: () => {}, dispatchEvent: () => false })) as unknown as typeof matchMedia
    matchMediaRestores.push(() => { window.matchMedia = orig })
  }

  function makeWalkSheet() {
    const sheet = createArtifactSheet()
    document.body.appendChild(sheet.root)
    sheetRoots.push(sheet.root)
    const toggle = sheet.root.querySelector('.walk-toggle') as HTMLButtonElement
    const spinner = sheet.root.querySelector('.walk-loading') as HTMLElement
    const hint = sheet.root.querySelector('.walk-hint') as HTMLElement
    const openGlb = async (name = 'model.glb') => {
      sheet.open(name)
      await vi.waitFor(() =>
        expect(sheet.root.querySelector('model-viewer')).not.toBeNull(),
      )
      return sheet.root.querySelector('model-viewer') as HTMLElement
    }
    return { sheet, toggle, spinner, hint, openGlb }
  }

  it('renders the walk toggle for a GLB unless ?walk-off', async () => {
    const shown = makeWalkSheet()
    await shown.openGlb()
    expect(shown.toggle.style.display).toBe('')
    shown.sheet.open('game.html')
    expect(shown.toggle.style.display).toBe('none')

    window.history.replaceState(null, '', '/?walk-off')
    const hidden = makeWalkSheet()
    await hidden.openGlb()
    expect(hidden.toggle.style.display).toBe('none')
    window.history.replaceState(null, '', '/')
  })

  it('toggle click enters walk: attribute set, dataset entering, spinner indexing, toggle becomes door icon', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    expect(el.getAttribute('walk')).toBe('')
    expect(sheet.root.dataset.walk).toBe('entering')
    expect(spinner.style.display).toBe('')
    expect(spinner.classList.contains('failed')).toBe(false)
    expect(spinner.textContent).toBe(t('walkIndexing'))
    // Walk state is icon-only: no text, the door glyph, an a11y label.
    expect(toggle.classList.contains('icon-only')).toBe(true)
    expect(toggle.textContent).toBe('')
    expect(toggle.querySelector('svg')).not.toBeNull()
    expect(toggle.getAttribute('aria-label')).toBe(t('walkExit'))
  })

  it('walk-indexed moves entering to active and hides the spinner', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    el.dispatchEvent(new CustomEvent('walk-indexed'))
    expect(sheet.root.dataset.walk).toBe('active')
    expect(spinner.style.display).toBe('none')
  })

  it('walk-error while entering exits to orbit and shows walkFailed until the next toggle', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    el.dispatchEvent(new CustomEvent('walk-error', { detail: { message: 'x' } }))
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('')
    expect(spinner.classList.contains('failed')).toBe(true)
    expect(spinner.textContent).toBe(t('walkFailed'))
    expect(toggle.textContent).toBe(t('walkEnter'))

    toggle.click()
    expect(el.getAttribute('walk')).toBe('')
    expect(sheet.root.dataset.walk).toBe('entering')
    expect(spinner.classList.contains('failed')).toBe(false)
    expect(spinner.textContent).toBe(t('walkIndexing'))
  })

  it('no-walkable-floor refusal exits, hides the toggle, and names the model', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    expect(sheet.root.dataset.walk).toBe('entering')
    el.dispatchEvent(new CustomEvent('walk-error', { detail: { message: 'no-walkable-floor' } }))
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('')
    expect(spinner.textContent).toBe(t('walkNoFloor'))
    // Not a malfunction — the spinner must not wear the failed look or spin.
    expect(spinner.classList.contains('failed')).toBe(false)
    expect(spinner.classList.contains('notice')).toBe(true)
    // The toggle hides for this mount: clicking again could only repeat the answer.
    expect(toggle.style.display).toBe('none')
  })

  it('no-walkable-floor notice auto-dismisses after 3s', async () => {
    const { toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    // Fake clock first: the dismissal timer must be scheduled on it.
    vi.useFakeTimers()
    try {
      el.dispatchEvent(new CustomEvent('walk-error', { detail: { message: 'no-walkable-floor' } }))
      expect(spinner.textContent).toBe(t('walkNoFloor'))
      vi.advanceTimersByTime(2999)
      expect(spinner.textContent).toBe(t('walkNoFloor'))
      vi.advanceTimersByTime(1)
      expect(spinner.style.display).toBe('none')
    } finally {
      vi.useRealTimers()
    }
  })

  it('toggle while active exits: attribute removed, dataset orbit, label reset, late walk-indexed ignored', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    el.dispatchEvent(new CustomEvent('walk-indexed'))
    toggle.click()
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
    expect(toggle.textContent).toBe(t('walkEnter'))
    // A late indexed event after exit must not resurrect walk state.
    el.dispatchEvent(new CustomEvent('walk-indexed'))
    expect(sheet.root.dataset.walk).toBe('')
  })

  it('close() while walk empties the host and clears the overlay', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    await openGlb()
    toggle.click()
    sheet.close()
    expect(sheet.root.querySelector('model-viewer')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
    expect(toggle.style.display).toBe('none')
  })

  it('reload() while entering remounts without walk and resets the overlay', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    sheet.reload()
    const fresh = sheet.root.querySelector('model-viewer') as HTMLElement
    expect(fresh).not.toBe(el)
    expect(fresh.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
    expect(toggle.textContent).toBe(t('walkEnter'))
  })

  it('webglcontextlost while entering walk exits to orbit and shows walkFailed', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    el.dispatchEvent(new CustomEvent('error', { detail: { type: 'webglcontextlost' } }))
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('')
    expect(spinner.classList.contains('failed')).toBe(true)
    expect(spinner.textContent).toBe(t('walkFailed'))
    expect(toggle.textContent).toBe(t('walkEnter'))
  })

  it('load failure while entering walk exits to orbit and shows walkFailed', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    el.dispatchEvent(new CustomEvent('error', { detail: { type: 'loadfailure' } }))
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('')
    expect(spinner.classList.contains('failed')).toBe(true)
    expect(spinner.textContent).toBe(t('walkFailed'))
    expect(toggle.textContent).toBe(t('walkEnter'))
  })

  it('webglcontextlost outside walk changes nothing (orbit-only recovery)', async () => {
    const { sheet, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    el.dispatchEvent(new CustomEvent('error', { detail: { type: 'webglcontextlost' } }))
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
  })

  it('exit during entering clears the spinner; a late walk-indexed changes nothing', async () => {
    const { sheet, toggle, spinner, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    toggle.click()
    expect(el.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
    // The disposed controller suppresses its callback: the race is pinned at
    // the seam the sheet can observe.
    el.dispatchEvent(new CustomEvent('walk-indexed'))
    expect(sheet.root.dataset.walk).toBe('')
    expect(spinner.style.display).toBe('none')
  })

  it('three enter/exit cycles set and remove the walk attribute exactly 3 times each', async () => {
    const { toggle, openGlb } = makeWalkSheet()
    const el = await openGlb()
    const setSpy = vi.spyOn(el, 'setAttribute')
    const removeSpy = vi.spyOn(el, 'removeAttribute')
    for (let i = 0; i < 3; i++) {
      toggle.click()
      expect(el.getAttribute('walk')).toBe('')
      toggle.click()
      expect(el.getAttribute('walk')).toBeNull()
    }
    expect(setSpy.mock.calls.filter((call) => call[0] === 'walk')).toHaveLength(3)
    expect(removeSpy.mock.calls.filter((call) => call[0] === 'walk')).toHaveLength(3)
  })

  it('opening another GLB while walking mounts a fresh element without walk', async () => {
    const { sheet, toggle, openGlb } = makeWalkSheet()
    const first = await openGlb('a.glb')
    toggle.click()
    const second = await openGlb('b.glb')
    expect(second).not.toBe(first)
    expect(second.getAttribute('walk')).toBeNull()
    expect(sheet.root.dataset.walk).toBe('')
  })

  it('entering walk shows the hint with the touch copy and keeps it up (no auto-hide)', async () => {
    setTouchDevice(1)
    const { sheet, toggle, hint, openGlb } = makeWalkSheet()
    await openGlb()
    toggle.click()
    expect(hint.textContent).toBe(t('walkHintTouch'))
    expect(hint.classList.contains('visible')).toBe(true)
    // The hint stays until the first real input — nothing else retires it.
    expect(sheet.root.dataset.walk).toBe('entering')
  })

  it('entering walk renders desktop keycap chips when the device has no touch points', async () => {
    const { toggle, hint, openGlb } = makeWalkSheet()
    await openGlb()
    toggle.click()
    expect(hint.querySelectorAll('kbd')).toHaveLength(4)
    expect(hint.textContent).toBe('WASD' + t('walkHintMove') + '·' + t('walkHintLook'))
    expect(hint.classList.contains('visible')).toBe(true)
  })

  it('the first touch pointerdown hides the hint — joystick (left) or look (right) half', async () => {
    setTouchDevice(1)
    const { toggle, hint, openGlb } = makeWalkSheet()
    const el = await openGlb()
    vi.spyOn(el, 'getBoundingClientRect').mockReturnValue({
      left: 0,
      width: 100,
    } as DOMRect)
    toggle.click()
    el.dispatchEvent(new PointerEvent('pointerdown', { clientX: 30, bubbles: true }))
    expect(hint.classList.contains('visible')).toBe(false)
    // Re-entry re-arms the hint; the first look-drag (right half) dismisses it too.
    toggle.click()
    toggle.click()
    expect(hint.classList.contains('visible')).toBe(true)
    el.dispatchEvent(new PointerEvent('pointerdown', { clientX: 70, bubbles: true }))
    expect(hint.classList.contains('visible')).toBe(false)
  })

  it('desktop: the first pointerdown (look drag) retires the hint', async () => {
    const { toggle, hint, openGlb } = makeWalkSheet()
    const el = await openGlb()
    toggle.click()
    expect(hint.classList.contains('visible')).toBe(true)
    el.dispatchEvent(new PointerEvent('pointerdown', { clientX: 30, bubbles: true }))
    expect(hint.classList.contains('visible')).toBe(false)
  })

  it('exit and close clear the hint; re-enter shows it again', async () => {
    setTouchDevice(1)
    const { sheet, toggle, hint, openGlb } = makeWalkSheet()
    await openGlb()
    toggle.click()
    expect(hint.classList.contains('visible')).toBe(true)
    toggle.click()
    expect(hint.classList.contains('visible')).toBe(false)
    toggle.click()
    expect(hint.classList.contains('visible')).toBe(true)
    sheet.close()
    expect(hint.classList.contains('visible')).toBe(false)
  })
})

describe('isGLTFArtifactName', () => {
  it.each([
    ['a/b.glb', true],
    ['x.GLTf', true],
    ['game.html', false],
    ['pic.png', false],
    ['glb.txt', false],
    ['model.glb2', false],
  ])('%s dispatches to %s', (name, want) => {
    expect(isGLTFArtifactName(name)).toBe(want)
  })
})

describe('fetchArtifactList', () => {
  it('GETs /api/artifacts and returns the parsed items', async () => {
    const items = [
      { name: 'game.html', size: 29, mtime: 1700000100000 },
      { name: 'old.html', size: 3, mtime: 1700000000000 },
    ]
    const fetchMock = vi.fn(async () => ({
      ok: true,
      json: async () => items,
    }))
    vi.stubGlobal('fetch', fetchMock)

    const got = await fetchArtifactList()

    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(fetchMock).toHaveBeenCalledWith('/api/artifacts')
    expect(got).toEqual(items)
  })
  it('rejects on a non-ok response', async () => {
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => ({ ok: false, status: 500 })),
    )
    await expect(fetchArtifactList()).rejects.toThrow('500')
  })
})
