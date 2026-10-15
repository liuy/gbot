// Long-press share card: render a message's text through the real wui
// markdown pipeline into an offscreen card, rasterize it to a PNG dataURL
// and hand it to the Android shell's window.GBotShare bridge. Desktop
// never gets here — getShareBridge() probes the shell and share_menu
// refuses to arm without it.
import { domToPng } from 'modern-screenshot'
import { renderMarkdown } from './markdown'
import { createNode } from './dom'

/** Local-timezone "YYYY-MM-DD HH:mm" — the share-card header stamp. */
export function formatShareStamp(ms: number): string {
  const d = new Date(ms)
  const pad = (n: number): string => String(n).padStart(2, '0')
  return (
    `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    ` ${pad(d.getHours())}:${pad(d.getMinutes())}`
  )
}

export interface ShareRequest {
  text: string
  role: 'user' | 'assistant'
  startedAt: number
}

/** Card DOM (unmounted): div.share-card > (.share-card-header:
 *  .share-card-title "GBot" + .share-card-stamp stamp) + .share-card-body.
 *  assistant: body = div.md-body.md-text with renderMarkdown(text) — the
 *  exact pipeline the chat uses, so the PNG is literally what the user saw.
 *  user: plain pre-wrap text — user messages skip markdown in chat too.
 */
export function buildShareCard(req: ShareRequest): HTMLDivElement {
  const card = createNode('div', { className: 'share-card' })
  const header = createNode('div', { className: 'share-card-header' })
  header.append(
    createNode('div', { className: 'share-card-title', text: 'GBot' }),
    createNode('div', { className: 'share-card-stamp', text: formatShareStamp(req.startedAt) }),
  )
  const body =
    req.role === 'assistant'
      ? createNode('div', { className: 'md-body md-text share-card-body' })
      : createNode('div', {
          className: 'share-card-body whitespace-pre-wrap break-words text-[15px] text-t1',
        })
  if (req.role === 'assistant') {
    body.innerHTML = renderMarkdown(req.text)
  } else {
    body.textContent = req.text
  }
  card.append(header, body)
  return card
}

/** gbot-share-YYYY-MM-DD-HHmmss.png (local time). */
export function shareFileName(startedAt: number): string {
  const d = new Date(startedAt)
  const pad = (n: number): string => String(n).padStart(2, '0')
  return (
    `gbot-share-${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}` +
    `-${pad(d.getHours())}${pad(d.getMinutes())}${pad(d.getSeconds())}.png`
  )
}

/** Mount the card on an offscreen body host (position:fixed left:-10000px
 *  via .share-card-offscreen-host), wait a double rAF so layout and computed
 *  styles settle, then rasterize. Scale tracks the device DPR (clamped 2-3):
 *  scale 2 on a 3x phone produces visibly soft text once WeChat zooms in.
 *  The host — card included — is removed in finally, so a failed capture
 *  never leaks DOM. */
export async function captureShareCard(card: HTMLElement): Promise<string> {
  const host = createNode('div', { className: 'share-card-offscreen-host' })
  // Chat-density width: the card mirrors what the phone showed (15px body),
  // capped for tablets/desktop windows.
  card.style.width = `${Math.min(window.innerWidth - 32, 480)}px`
  host.appendChild(card)
  document.body.appendChild(host)
  try {
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()))
    const dpr = window.devicePixelRatio || 2
    return await domToPng(card, { scale: Math.min(Math.max(dpr, 2), 3) })
  } finally {
    host.remove()
  }
}

// Bridge probing lives here, not in share_menu: share_menu's share item
// already imports shareMessage, so share_card importing a share_menu-side
// probe would close a dependency cycle. One-way: share_menu → share_card.
export const SHARE_BRIDGE_NAME = 'GBotShare'

export interface ShareBridge {
  shareImage: (dataUrl: string, fileName: string) => void
}

export function getShareBridge(): ShareBridge | null {
  const candidate = (window as unknown as Record<string, unknown>)[SHARE_BRIDGE_NAME] as
    | ShareBridge
    | undefined
  return candidate && typeof candidate.shareImage === 'function' ? candidate : null
}

/** buildShareCard → captureShareCard → bridge.shareImage(dataUrl, file name).
 *  Missing bridge returns 'no-bridge' (defensive — the menu should never
 *  have opened); capture failures propagate to the menu's failure toast. */
export async function shareMessage(req: ShareRequest): Promise<'opened' | 'no-bridge'> {
  const bridge = getShareBridge()
  if (!bridge) return 'no-bridge'
  const dataUrl = await captureShareCard(buildShareCard(req))
  bridge.shareImage(dataUrl, shareFileName(req.startedAt))
  return 'opened'
}
