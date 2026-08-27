import { describe, it, expect, vi, afterEach } from 'vitest'
import {
  collectArtifactWrites,
  artifactURL,
  formatArtifactSize,
  createArtifactCard,
  createArtifactSheet,
} from './artifact'
import type { Block } from './model'

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
