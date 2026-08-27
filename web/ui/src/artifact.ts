import type { Block } from './model'
import type { ArtifactListItem } from './types'
import { createElement } from './dom'

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
  root.append(frame, handle)

  // Height is state-driven, never measured: jsdom's getBoundingClientRect is
  // always 0, and the drag math needs the pre-drag pct as a number anyway.
  let heightPct = 0
  let opened = false
  const setHeight = (pct: number) => {
    heightPct = pct
    root.style.height = pct > 0 ? `${Math.round(pct)}%` : '0px'
  }
  setHeight(0)

  const open = (name: string) => {
    frame.src = artifactURL(name)
    opened = true
    root.classList.remove('dragging')
    setHeight(SHEET_DEFAULT_H)
  }

  const close = () => {
    opened = false
    root.classList.remove('dragging')
    setHeight(0)
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
    // Re-assigning src reloads the frame: the serve route is no-store with a
    // zero modtime, so the fetch cannot be satisfied from cache.
    // eslint-disable-next-line no-self-assign -- intentional reload idiom
    frame.src = frame.src
  }

  return { root, open, close, isOpen: () => opened, reload }
}
