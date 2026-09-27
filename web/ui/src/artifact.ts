import type { Block } from './model'
import type { ArtifactListItem } from './types'
import { createElement } from './dom'
import { loadModelViewer } from './model_viewer_loader'
import { t } from './i18n'

// Tool summaries carry the raw Write/Edit file_path. The current convention
// is an absolute <projectspace>/artifacts/... path; the relative artifacts/
// prefix stays recognized for transcripts recorded by the old redirect
// mechanism (same file in both forms dedupes to the same card name).
const ARTIFACT_PREFIX = 'artifacts/'
const ARTIFACT_SEG = '/artifacts/'

// isArtifactPath reports whether a Write/Edit file_path lands inside an
// artifacts directory — an /artifacts/ path segment (absolute form) or the
// legacy relative prefix.
function isArtifactPath(path: string): boolean {
  return path.startsWith(ARTIFACT_PREFIX) || path.includes(ARTIFACT_SEG)
}

// artifactName extracts the path below the artifacts directory.
function artifactName(path: string): string {
  if (path.startsWith(ARTIFACT_PREFIX)) return path.slice(ARTIFACT_PREFIX.length)
  const i = path.indexOf(ARTIFACT_SEG)
  return path.slice(i + ARTIFACT_SEG.length)
}

export type ArtifactWrite = {
  name: string
  updated: boolean
}

// collectArtifactWrites derives artifact cards purely from a block tree — the
// same function serves live query_end and history replay, so replay needs no
// separate logic. Dedup keeps the LAST write per file: iteration prompts say
// "Edit the existing file", and updated === last write was an Edit is a purely
// local rule that stays consistent across out-of-order history pages.
export function collectArtifactWrites(blocks: Block[]): ArtifactWrite[] {
  const byName = new Map<string, ArtifactWrite>()
  walk(blocks)
  return [...byName.values()]

  function walk(list: Block[]) {
    for (const b of list) {
      if (b.kind !== 'tool') continue
      if (
        (b.name === 'Write' || b.name === 'Edit') &&
        b.state === 'done' &&
        isArtifactPath(b.summary)
      ) {
        const name = artifactName(b.summary)
        if (name) byName.set(name, { name, updated: b.name === 'Edit' })
      }
      // Wire blocks may omit children (JSON omitempty on the wire type).
      if (b.children && b.children.length > 0) walk(b.children)
    }
  }
}

export function artifactURL(name: string): string {
  // Encode per segment so the directory slash survives as a separator.
  const encoded = name.split('/').map(encodeURIComponent).join('/')
  return `/artifacts/${encoded}`
}

// isGLTFArtifactName reports whether an artifact opens the 3D viewer instead
// of the iframe: glTF binaries and JSON are the only artifact types a browser
// cannot render from a Content-Type alone.
export function isGLTFArtifactName(name: string): boolean {
  return /\.(glb|gltf)$/i.test(name)
}

// The walk toggle is always visible (single-user deployment); ?walk-off
// hides it as a kill-switch.
export function isWalkToggleEnabled(search: string): boolean {
  return !new URLSearchParams(search).has('walk-off')
}

// The artifacts directory is the source of truth, so the list is fetched on
// demand (sidebar open) rather than tracked as state pushed over the WS.
export async function fetchArtifactList(): Promise<ArtifactListItem[]> {
  const res = await fetch('/api/artifacts')
  if (!res.ok) throw new Error(`artifact list fetch failed: ${res.status}`)
  return res.json()
}

export function formatArtifactSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / (1024 * 1024)).toFixed(1)} MB`
}

export function createArtifactCard(
  write: ArtifactWrite,
  onOpen: (name: string) => void,
): HTMLElement {
  const card = createElement('div', 'artifact-card')

  const preview = createElement('div', 'card-preview')
  const frame = createElement('iframe')
  frame.src = artifactURL(write.name)
  frame.setAttribute('loading', 'lazy')
  // Thumbnail is decorative — keep it out of the tab order; preview is not
  // interactive either (pointer-events:none in CSS), the whole card clicks.
  frame.setAttribute('tabindex', '-1')
  preview.appendChild(frame)

  const body = createElement('div', 'card-body')
  const titleRow = createElement('div', 'card-title-row')
  const title = createElement('span', 'card-title')
  title.textContent = write.name.split('/').pop() ?? write.name
  titleRow.appendChild(title)
  const meta = createElement('span', 'card-meta-inline')
  const sizeEl = createElement('span')
  sizeEl.textContent = '—'
  meta.appendChild(sizeEl)
  if (write.updated) {
    const dot = createElement('span', 'card-fresh-dot')
    const sep = createElement('span')
    sep.textContent = '·'
    const updated = createElement('span', 'card-updated')
    updated.textContent = 'Updated'
    titleRow.appendChild(dot)
    meta.append(sep, updated)
    card.classList.add('stale')
  }
  titleRow.appendChild(meta)
  body.appendChild(titleRow)
  card.append(preview, body)

  // The wire stream never carries byte counts (Write/Edit output is a one-line
  // confirmation), so HEAD is the only size source; failures keep the em-dash.
  fetch(artifactURL(write.name), { method: 'HEAD' })
    .then((res) => res.headers.get('content-length'))
    .then((len) => {
      const bytes = len === null ? NaN : Number.parseInt(len, 10)
      if (Number.isFinite(bytes)) sizeEl.textContent = formatArtifactSize(bytes)
    })
    .catch(() => {})

  card.addEventListener('click', () => {
    // Opening the sheet shows the current content — consume the stale marker.
    card.classList.remove('stale')
    onOpen(write.name)
  })
  return card
}

export interface ArtifactSheetHandles {
  root: HTMLElement
  open: (name: string) => void
  close: () => void
  isOpen: () => boolean
  current: () => string
  reload: () => void
}

const SHEET_MIN_H = 15 // below this pct on release → collapse
const SHEET_MAX_H = 92 // ceiling: leave room for the system status bar
const SHEET_DRAG_FLOOR = 10 // floor while dragging; release still decides close
const SHEET_DRAG_SLOP = 5 // movement under this counts as a click (close)
const SHEET_DEFAULT_H = 70

export function createArtifactSheet(): ArtifactSheetHandles {
  const root = createElement('div', 'artifact-sheet')
  const frame = createElement('iframe')
  // No sandbox attribute here: the response CSP header carries the sandbox
  // policy (allow-scripts allow-same-origin). An attribute-level sandbox
  // without allow-same-origin would re-opaque the origin and kill
  // localStorage despite the header.
  const handle = createElement('div', 'sheet-handle')
  const modelHost = createElement('div', 'gltf-host')
  modelHost.style.display = 'none'

  // Walk-mode overlay (the forked <model-viewer walk> attribute): sibling of
  // modelHost so modelHost.replaceChildren() can never wipe it. dataset.walk
  // on the sheet root — '' (orbit) | 'entering' | 'active' — is the CSS and
  // test anchor; the walkFailed notice is spinner-only state.
  const sheetUi = createElement('div', 'sheet-ui')
  const walkToggle = createElement('button', 'walk-toggle')
  walkToggle.type = 'button'
  walkToggle.textContent = t('walkEnter')
  const walkSpinner = createElement('div', 'walk-loading')
  const walkHint = createElement('div', 'walk-hint')
  sheetUi.append(walkToggle, walkSpinner, walkHint)
  root.append(frame, modelHost, sheetUi, handle)

  // One of the two surfaces is visible at a time; display:'' restores the
  // CSS default (iframe block, host flex child of the sheet).
  const showFrame = (show: boolean) => {
    frame.style.display = show ? '' : 'none'
    modelHost.style.display = show ? 'none' : ''
  }

  let walkElement: HTMLElement | null = null

  const showWalkSpinner = (text: string | null) => {
    if (text === null) {
      walkSpinner.style.display = 'none'
      return
    }
    walkSpinner.classList.toggle('failed', text === t('walkFailed'))
    // Informational copy: no spinner glyph — nothing is loading.
    walkSpinner.classList.toggle('notice', text === t('walkNoFloor'))
    walkSpinner.textContent = text
    walkSpinner.style.display = ''
  }

  // UI-only reset: the mounted element itself is torn down by the
  // replaceChildren() that every caller of this performs (or is about to).
  const resetWalkUi = () => {
    walkElement = null
    root.dataset.walk = ''
    showWalkSpinner(null)
    walkToggle.textContent = t('walkEnter')
    walkHint.classList.remove('visible')
  }

  // Desktop hint renders real keycap chips — the fastest-to-read control
  // idiom; touch keeps the plain zone sentence. Coarse pointer is the
  // touch-first test (a touchscreen laptop has maxTouchPoints > 0 but a
  // fine mouse); jsdom has no matchMedia and falls back to maxTouchPoints.
  const setWalkHint = () => {
    walkHint.replaceChildren()
    const coarse = window.matchMedia == null
      ? navigator.maxTouchPoints > 0
      : window.matchMedia('(pointer: coarse)').matches
    if (coarse) {
      walkHint.textContent = t('walkHintTouch')
      return
    }
    for (const key of ['W', 'A', 'S', 'D']) {
      const kbd = document.createElement('kbd')
      kbd.textContent = key
      walkHint.append(kbd)
    }
    const move = document.createElement('span')
    move.textContent = t('walkHintMove')
    const dot = document.createElement('i')
    dot.textContent = '·'
    const look = document.createElement('span')
    look.textContent = t('walkHintLook')
    walkHint.append(move, dot, look)
  }

  const enterWalk = () => {
    if (walkElement == null) return
    walkElement.setAttribute('walk', '')
    root.dataset.walk = 'entering'
    showWalkSpinner(t('walkIndexing'))
    walkToggle.textContent = t('walkExit')
    setWalkHint()
    walkHint.classList.add('visible')
  }

  const exitWalk = () => {
    walkElement?.removeAttribute('walk')
    root.dataset.walk = ''
    showWalkSpinner(null)
    walkToggle.textContent = t('walkEnter')
    walkHint.classList.remove('visible')
  }

  // Both failure paths (fork walk-error, base-element webglcontextlost) land
  // here: back to orbit with the failure copy held until the next transition.
  // `no-walkable-floor` is not a malfunction — the model simply has nothing
  // to stand on — so the copy names that and the toggle hides for this mount
  // (clicking it again could only repeat the answer).
  let noFloorTimer = 0
  const failWalk = (message?: string) => {
    if (root.dataset.walk === '') return
    exitWalk()
    if (message === 'no-walkable-floor') {
      walkToggle.style.display = 'none'
      showWalkSpinner(t('walkNoFloor'))
      // Informational, not an error state — auto-dismiss like a toast (3 s:
      // the short-copy convention, Ant Design's default). The text latch
      // keeps the timer from clearing a newer transition's copy, and the
      // handle keeps back-to-back refusals from stacking timers.
      if (noFloorTimer !== 0) {
        window.clearTimeout(noFloorTimer)
      }
      noFloorTimer = window.setTimeout(() => {
        noFloorTimer = 0
        if (walkSpinner.textContent === t('walkNoFloor')) {
          showWalkSpinner(null)
        }
      }, 3000)
      return
    }
    showWalkSpinner(t('walkFailed'))
  }

  // The hint persists the whole walk session and retires on the first real
  // input only: a touch pointerdown anywhere on the viewer (joystick anchor
  // or look drag) on touch devices; a desktop drag retires it via the same
  // pointerdown path (pointer lock is no longer used on desktop).
  const hideWalkHint = () => {
    walkHint.classList.remove('visible')
  }
  const onWalkPointerDown = () => {
    if (root.dataset.walk === '') return
    hideWalkHint()
  }

  walkToggle.addEventListener('click', () => {
    if (walkElement == null) return
    if (root.dataset.walk === '') {
      enterWalk()
    } else {
      exitWalk()
    }
  })

  // Toggle visibility: GLB artifact open; ?walk-off is the kill-switch.
  const syncWalkToggle = (glTF: boolean) => {
    walkToggle.style.display = glTF && isWalkToggleEnabled(location.search) ? '' : 'none'
  }

  // The <model-viewer> element (our model-viewer fork) carries its own
  // loading/error UI — no overlay of ours on top of it.
  const mountModelViewer = (url: string) => {
    const el = document.createElement('model-viewer') as HTMLElement
    el.className = 'w-full h-full'
    el.setAttribute('src', url)
    el.setAttribute('camera-controls', '')
    el.setAttribute('tone-mapping', 'neutral')
    el.setAttribute('shadow-intensity', '1')
    el.setAttribute('touch-action', 'pan-y')
    // Walk events ride on the element itself so a remount starts clean.
    el.addEventListener('walk-indexed', () => {
      // Only the entering state accepts the event — a late event after an
      // exit (exit-during-indexing race) must change nothing.
      if (root.dataset.walk !== 'entering') return
      root.dataset.walk = 'active'
      showWalkSpinner(null)
    })
    el.addEventListener('walk-error', (event) => {
      failWalk((event as CustomEvent<{ message?: string }>).detail?.message)
    })
    // The base element re-dispatches WebGL context loss and load failure as
    // error events with detail.type — only those types are walk-relevant.
    el.addEventListener('error', (event) => {
      // The DOM types the element error event as ErrorEvent; the fork's
      // re-dispatches are CustomEvents carrying detail.
      const type = (event as unknown as CustomEvent<{ type?: string }>).detail?.type
      // A load failure while entering means walk-indexed never fires — fail
      // the walk instead of holding the indexing copy forever.
      if (type === 'webglcontextlost' || type === 'loadfailure') failWalk()
    })
    el.addEventListener('pointerdown', onWalkPointerDown)
    walkElement = el
    modelHost.replaceChildren(el)
    return el
  }

  // Height is state-driven, never measured: jsdom's getBoundingClientRect is
  // always 0, and the drag math needs the pre-drag pct as a number anyway.
  let heightPct = 0
  let opened = false
  let currentName = ''
  const setHeight = (pct: number) => {
    heightPct = pct
    root.style.height = pct > 0 ? `${Math.round(pct)}%` : '0px'
  }
  setHeight(0)

  const open = (name: string) => {
    currentName = name
    opened = true
    root.classList.remove('dragging')
    setHeight(SHEET_DEFAULT_H)
    if (isGLTFArtifactName(name)) {
      // about:blank unloads a previously-open HTML artifact — clearing the
      // attribute alone would leave its JS running in the frame.
      frame.src = 'about:blank'
      showFrame(false)
      // A fresh mount never carries the walk attribute, so the walk state
      // reset is explicit — the sheetUi overlay survives the remount.
      resetWalkUi()
      syncWalkToggle(true)
      loadModelViewer()
        .then(() => {
          // Close clears currentName; a collapse (close) during the
          // multi-second bundle import must not mount afterwards.
          if (currentName !== name || !opened) return
          mountModelViewer(artifactURL(name))
        })
        .catch((err: unknown) => {
          if (currentName !== name || !opened) return
          console.error('glTF: model-viewer bundle failed to load', err)
        })
    } else {
      resetWalkUi()
      syncWalkToggle(false)
      // A non-glTF open retires any mounted model-viewer element.
      modelHost.replaceChildren()
      showFrame(true)
      frame.src = artifactURL(name)
    }
  }
  const current = () => currentName

  const close = () => {
    opened = false
    root.classList.remove('dragging')
    setHeight(0)
    // Covers the drag-to-collapse path: the viewer must not keep GPU
    // resources or an in-flight fetch while collapsed.
    modelHost.replaceChildren()
    resetWalkUi()
    syncWalkToggle(false)
  }

  let dragStartY = 0
  let dragStartH = 0
  let dragging = false
  let maybeDrag = false

  handle.addEventListener('pointerdown', (e) => {
    e.preventDefault()
    // jsdom has no pointer capture — optional chain keeps tests running.
    handle.setPointerCapture?.(e.pointerId)
    maybeDrag = true
    dragging = false
    dragStartY = e.clientY
    dragStartH = heightPct
  })

  handle.addEventListener('pointermove', (e) => {
    if (!maybeDrag) return
    const dy = e.clientY - dragStartY // downward positive → taller
    if (!dragging && Math.abs(dy) > SHEET_DRAG_SLOP) {
      dragging = true
      root.classList.add('dragging')
    }
    if (dragging) {
      const h = Math.max(
        SHEET_DRAG_FLOOR,
        Math.min(SHEET_MAX_H, dragStartH + (dy / window.innerHeight) * 100),
      )
      setHeight(h)
    }
  })

  const endDrag = () => {
    if (!maybeDrag) return
    maybeDrag = false
    if (!dragging) {
      close()
      return
    }
    dragging = false
    root.classList.remove('dragging')
    if (heightPct < SHEET_MIN_H) close()
  }
  handle.addEventListener('pointerup', endDrag)
  handle.addEventListener('pointercancel', endDrag)

  const reload = () => {
    if (isGLTFArtifactName(currentName)) {
      // Reload always lands in orbit: the fresh element mounts without the
      // walk attribute, and the sheetUi overlay survives the remount as a
      // sibling — only this explicit reset clears walk state.
      resetWalkUi()
      // Remounting the element refetches the model — that IS the reload
      // for the viewer (no frame src to self-assign).
      mountModelViewer(artifactURL(currentName))
      return
    }
    // Re-assigning src reloads the frame: the serve route is no-store with a
    // zero modtime, so the fetch cannot be satisfied from cache.
    // eslint-disable-next-line no-self-assign -- intentional reload idiom
    frame.src = frame.src
  }

  return { root, open, close, isOpen: () => opened, current, reload }
}
