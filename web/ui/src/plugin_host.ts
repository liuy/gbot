// Plugin card host: fetches the daemon's plugin list, opens a bottom-sheet
// card and mounts the plugin's UI module (served same-origin at the stable
// /plugins/{name}/ui route, backed by the manifest's declared ui file). The
// module contract:
//
//   export function mount(container: HTMLElement, ctx: {
//     openChannel: (plugin: string) => Channel
//     close: () => void             // closes the sheet
//     headerSlot: () => HTMLElement // header slot the module fills (idempotent)
//   }): void | (() => void)         // optional cleanup fn
//
// Trust model: plugin UI modules run with full page access — installing a
// plugin is VSCode-extension-grade trust (v1: first-party plugins only).
import { createElement } from './dom'
import { getConnection } from './ws'
import type { Channel } from './transport'

export interface PluginCard {
  name: string
  ui: string
  running: boolean
}

export async function fetchPluginList(): Promise<PluginCard[]> {
  const res = await fetch('/api/plugins')
  if (!res.ok) throw new Error(`plugin list ${res.status}`)
  return (await res.json()) as PluginCard[]
}

type MountFn = (
  container: HTMLElement,
  ctx: {
    openChannel: (name: string) => Channel
    close: () => void
    headerSlot: () => HTMLElement
  },
) => void | (() => void)

export type RendererLoader = (url: string) => Promise<{ mount: MountFn }>

const defaultLoader: RendererLoader = (url) => import(/* @vite-ignore */ url)

let sheetEl: HTMLElement | null = null
let cleanup: (() => void) | null = null

export function closePluginCard(): void {
  cleanup?.()
  cleanup = null
  sheetEl?.remove()
  sheetEl = null
}

// openPluginCard shows one card at a time (reopening swaps). loader and
// getChannel are injectable for tests; production uses a dynamic import and
// the live ws connection.
export async function openPluginCard(
  card: PluginCard,
  opts: { loader?: RendererLoader; getChannel?: () => { openChannel: (name: string) => Channel } } = {},
): Promise<void> {
  closePluginCard()
  const getChannel = opts.getChannel ?? (() => getConnection())

  const veil = createElement('div', 'vnc-veil')
  veil.style.display = ''
  const sheet = createElement('div', 'vnc-sheet')
  // Bar mirrors the VNC console toolbar's design language (vnc_console.ts):
  // one compact row whose right side is the plugin slot, then ✕.
  const bar = createElement('div', 'flex items-center gap-[7px] px-3.5 pb-2 pt-2 text-[11px] text-t2')
  const title = createElement('span', 'min-w-0 truncate text-[13px] font-semibold text-t1')
  title.textContent = card.name
  const dot = createElement('span', 'h-[4px] w-[4px] shrink-0 rounded-full bg-t3')
  // The slot leads the bar (plugin buttons render first, left-to-right via
  // plain append). It stays bare — header ordering inside the slot and any
  // spacing tweaks are the module's decisions, same trust boundary as the
  // rest of the plugin UI; mount() hands the element over via
  // ctx.headerSlot. items-center aligns the bare span's button with the
  // title baseline.
  const slot = createElement('span', 'flex items-center')
  const closeBtn = createElement('span', 'text-t3 cursor-pointer px-2 text-[15px]')
  closeBtn.textContent = '✕'
  closeBtn.addEventListener('click', closePluginCard)
  const spacer = createElement('span', 'flex-1')
  bar.append(title, dot, slot, spacer, closeBtn)
  const container = createElement('div', 'vnc-screen plugin-card-body')
  container.style.setProperty('position', 'relative')
  sheet.append(bar, container)
  veil.append(sheet)
  document.body.append(veil)
  sheetEl = veil

  const ctx = {
    openChannel: (plugin: string) => getChannel().openChannel(plugin),
    close: closePluginCard,
    // Same element every call: modules keep their reference across mounts.
    headerSlot: () => slot,
  }
  try {
    const mod = await (opts.loader ?? defaultLoader)(card.ui)
    const maybe = mod.mount(container, ctx)
    if (typeof maybe === 'function') cleanup = maybe
  } catch (e) {
    container.textContent = `plugin ui failed: ${String(e)}`
  }
}
