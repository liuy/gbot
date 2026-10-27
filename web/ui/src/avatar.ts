import { createElement, createNode } from './dom'
import { renderIcon } from './icons'

// Avatar infrastructure: the file IS the interface. ~/.gbot/avatars/<who>.png
// exists = custom avatar served from /api/avatar/<who>; absent = the default
// letter/icon rendering. This module owns the client-side mirror of that
// state and the DOM plumbing that swaps every holder when it changes.

export type AvatarWho = 'agent' | 'user'

// Sentinel message for settings to map onto i18n copy — callers compare by
// identity instead of sniffing prose.
export const AVATAR_TOO_LARGE = 'avatar too large after crop'

// Migrated from chat.ts: the agent default square's gradient + white ink.
// Inline literal, not a class, because index.css's light-theme .avatar-g-bg
// override carries !important and wins over it — same behavior the chat
// stream always had.
export const avatarGStyle = 'background: linear-gradient(to bottom right, #00B4FF, #9D5CFF);color:#FFFFFF'

// null = default rendering (either never probed or probed to 404/cleared;
// undefined = never probed). The value is the cache-busting version for the
// URL: ETag plus a monotonic client counter, so even an ETag collision
// (same-second, same-size replacement) still yields a fresh URL.
const versions: Partial<Record<AvatarWho, string | null>> = {}
let counter = 0

export function resetAvatarStateForTest(): void {
  for (const k of Object.keys(versions) as AvatarWho[]) delete versions[k]
  counter = 0
}

export function customAvatarURL(who: AvatarWho): string | null {
  const v = versions[who]
  if (v == null) return null
  return `/api/avatar/${who}?v=${encodeURIComponent(v)}`
}

// Fills one holder with the current rendering for who. Holders carry their
// own size/rounded/gradient styling; this function only owns the content.
export function renderAvatarInto(holder: HTMLElement, who: AvatarWho, iconSize = 13): void {
  holder.dataset.avatar = who
  holder.dataset.avatarIcon = String(iconSize)
  const url = customAvatarURL(who)
  if (url) {
    // A custom image is opaque-in-its-own-terms (transparent PNGs are
    // expected): the default gradient backdrop (G letter / user icon) must
    // go, or it bleeds through every transparent pixel as a blue wash.
    holder.classList.remove('avatar-g-bg', 'bg-gradient-to-br', 'from-t2', 'to-t3')
    holder.style.background = ''
    const img = createElement('img', 'h-full w-full object-cover')
    img.alt = ''
    img.src = url
    // Custom file vanished outside PUT/DELETE (hand-deleted, disk swap):
    // drop the in-memory state and rewrite every holder back to the default.
    // The rewrite renders no img, so this cannot loop.
    img.onerror = () => {
      versions[who] = null
      rewriteAvatarHolders(who)
    }
    holder.replaceChildren(img)
    return
  }
  restoreDefaultBackdrop(holder, who)
  if (who === 'agent') {
    holder.replaceChildren(createNode('span', { text: 'G' }))
    return
  }
  // Literal #FFFFFF, not text-white: index.css remaps --color-white to #333
  // in light theme, which would make the icon invisible on the gray gradient.
  holder.replaceChildren(renderIcon('user', { size: iconSize, className: 'text-[#FFFFFF]' }))
}
function restoreDefaultBackdrop(holder: HTMLElement, who: AvatarWho): void {
  if (who === 'agent') {
    holder.classList.add('avatar-g-bg')
  } else {
    holder.classList.add('bg-gradient-to-br', 'from-t2', 'to-t3')
  }
}

export function rewriteAvatarHolders(who: AvatarWho): void {
  document
    .querySelectorAll<HTMLElement>(`[data-avatar="${who}"]`)
    .forEach((el) => renderAvatarInto(el, who, Number(el.dataset.avatarIcon ?? '13')))
}

async function probeAvatar(who: AvatarWho): Promise<void> {
  try {
    const resp = await fetch(`/api/avatar/${who}`, { method: 'HEAD', cache: 'no-store' })
    if (resp.status === 200) {
      // `?.` guards bare-object fetch stubs (jsdom tests) that carry no
      // headers — they fall back to a timestamp version instead of crashing.
      const etag = resp.headers?.get('ETag') ?? String(Date.now())
      versions[who] = `${etag}-${++counter}`
    } else if (resp.status === 404) {
      versions[who] = null
    }
    // Any other status (5xx…) keeps the previous state: a transient failure
    // must not blank a known custom avatar back to the default.
  } catch {
    // Offline / relative-URL fetch rejection — same keep-old-state rule.
  }
}

// Boot hook: fire-and-forget probes. The whole chain swallows rejections so
// an offline start (or a test env without fetch) never produces an
// unhandledrejection; late probe results rewrite already-rendered holders.
export function initAvatars(): void {
  for (const who of ['agent', 'user'] as const) {
    probeAvatar(who)
      .then(() => rewriteAvatarHolders(who))
      .catch(() => {})
  }
}

export async function refreshAvatar(who: AvatarWho): Promise<void> {
  await probeAvatar(who)
  rewriteAvatarHolders(who)
}

export async function uploadAvatar(who: AvatarWho, blob: Blob): Promise<void> {
  const res = await fetch(`/api/avatar/${who}`, {
    method: 'PUT',
    headers: { 'Content-Type': blob.type },
    body: blob,
  })
  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const body = (await res.json()) as { error?: string }
      if (body.error) msg = body.error
    } catch {
      // non-JSON failure body — the status text is enough
    }
    throw new Error(msg)
  }
}

export async function deleteAvatar(who: AvatarWho): Promise<void> {
  const res = await fetch(`/api/avatar/${who}`, { method: 'DELETE' })
  if (!res.ok) throw new Error(`HTTP ${res.status}`)
}

export const AVATAR_SIDE = 256
// 256×256 PNG is typically tens of KB; photographic PNGs can exceed 300KB,
// which the JPEG q0.9 fallback handles — only both failing is an error.
const AVATAR_MAX_BYTES = 300 * 1024

function canvasToBlob(canvas: HTMLCanvasElement, type: string, quality?: number): Promise<Blob | null> {
  return new Promise((resolve) => canvas.toBlob(resolve, type, quality))
}

// jsdom safety: no getContext/drawImage here — the cropper's $toCanvas
// product is already the 256×256 final frame, so this only owns the
// encoding fallback chain.
export async function encodeCanvas(canvas: HTMLCanvasElement): Promise<Blob> {
  const png = await canvasToBlob(canvas, 'image/png')
  if (png && png.size <= AVATAR_MAX_BYTES) return png
  const jpg = await canvasToBlob(canvas, 'image/jpeg', 0.9)
  if (jpg && jpg.size <= AVATAR_MAX_BYTES) return jpg
  throw new Error(AVATAR_TOO_LARGE)
}

export async function setAvatarFromBlob(who: AvatarWho, blob: Blob): Promise<void> {
  await uploadAvatar(who, blob)
  // Serialize behind the upload so the probe never races a stale 404.
  await refreshAvatar(who)
}
