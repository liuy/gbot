import type RFB from '@novnc/novnc'
import { createElement, createNode } from './dom'
import { t, type StaticKey } from './i18n'
import { renderIcon, type IconName } from './icons'
import { loadRFB } from './rfb_loader'
import { testRemoteDevice, type RemoteDevice } from './vnc'

// Remote-desktop console sheet: noVNC rendered into a bottom-sheet (mobile) /
// centered card (>=700px) that connects through the server-side WS reverse
// proxy /wui/vnc/{device}. Sheet CSS lives in index.css (.vnc-*) because the
// media query + animation spec is cleaner as CSS than Tailwind variants.

export interface VNCSheetHandles {
  root: HTMLElement
  open: (device: RemoteDevice) => void
  close: () => void
  isOpen: () => boolean
}

const ICON_BTN = 'cursor-pointer px-1 select-none flex items-center'

export function createVNCSheet(): VNCSheetHandles {
  // Same anchor helper as settings.ts/sidebar.ts.
  const L = <K extends StaticKey>(k: K, extra: Record<string, string> = {}) => ({
    text: t(k),
    attrs: { 'data-i18n': k, ...extra },
  })

  const root = createNode('div', {
    className: 'vnc-veil',
    style: { display: 'none' },
    attrs: { 'data-vnc-veil': '' },
  })
  const sheet = createElement('div', 'vnc-sheet')
  sheet.setAttribute('data-vnc-sheet', '')

  // One compact toolbar row — device name, mode chips, then latency and
  // close right-aligned. Vertical space is the scarcest resource in the
  // sheet, so the old two-row header (title row + chip row) was merged.
  const bar = createElement(
    'div',
    'flex items-center gap-[7px] px-3.5 pb-2 pt-2 text-[11px] text-t2',
  )
  bar.setAttribute('data-vnc-bar', '')
  const titleEl = createNode('span', {
    className: 'min-w-0 truncate text-[13px] font-semibold text-t1',
    attrs: { 'data-vnc-title': '' },
  })
  // Static separator dot between the name and the chips — no state meaning.
  const dotEl = createNode('span', {
    className: 'h-[4px] w-[4px] shrink-0 rounded-full bg-t3',
    attrs: { 'data-vnc-dot': '' },
  })
  const closeBtn = createNode('span', {
    className: 'text-t3 cursor-pointer px-2 text-[15px]',
    text: '✕',
    attrs: { 'data-vnc-close': '' },
  })
  const iconBtn = (id: string, icon: IconName, labelKey: StaticKey): HTMLElement => {
    const el = createNode('span', {
      className: ICON_BTN,
      // data-i18n-title keeps the tooltip refreshable by retranslate().
      attrs: { 'data-vnc-chip': id, 'data-i18n-title': labelKey },
    })
    el.append(renderIcon(icon, { size: 15 }))
    el.setAttribute('title', t(labelKey))
    return el
  }
  // Green pointer = control; grey = view-only — one binary, one button.
  const controlChip = iconBtn('control', 'pointer', 'rdChipControl')
  const viewChip = iconBtn('view', 'one-to-one', 'rdChipViewMode')
  const fullscreenChip = iconBtn('fullscreen', 'maximize', 'rdChipFullscreen')
  const latencyEl = createNode('span', { className: 'ml-auto text-t3', attrs: { 'data-vnc-latency': '' } })
  const keyboardBtn = createNode('span', {
    className: ICON_BTN,
    attrs: { 'data-vnc-keyboard': '' },
  })
  keyboardBtn.append(renderIcon('keyboard', { size: 15 }))
  keyboardBtn.setAttribute('data-i18n-title', 'rdChipKeyboard')
  keyboardBtn.setAttribute('title', t('rdChipKeyboard'))
  bar.append(titleEl, dotEl, controlChip, viewChip, keyboardBtn, fullscreenChip, latencyEl, closeBtn)

  const screen = createElement('div', 'vnc-screen')
  screen.setAttribute('data-vnc-screen', '')
  const stateEl = createNode('span', {
    className: 'absolute inset-0 flex items-center justify-center text-[12px] text-t2',
    ...L('rdStateConnecting', { 'data-vnc-state': '' }),
  })
  screen.append(stateEl)

  // Keystroke capture input: kept empty (value cleared after each event) —
  // it is a keyboard event source, not a text field.
  const kbdInput = createElement('textarea', 'vnc-kbd-input') as HTMLTextAreaElement
  kbdInput.setAttribute('data-vnc-kbd-input', '')
  kbdInput.setAttribute('autocomplete', 'off')
  kbdInput.setAttribute('autocapitalize', 'off')

  sheet.append(bar, screen, kbdInput)
  root.append(sheet)

  // ------------------------------------------------------------- state
  let rfb: RFB | null = null
  let sessionDead = false
  let device: RemoteDevice | null = null
  // Bumped on every close/open/connect: stale async import() resolutions and
  // probe results from a superseded session are dropped by seq re-check.
  let connectSeq = 0
  let mode: 'view' | 'control' = 'view'
  // fit = scale the whole remote screen into the sheet (default);
  // oneToOne = 1:1 pixels, clipped, drag to pan around.
  let viewMode: 'fit' | 'oneToOne' = 'fit'

  const chipOn = (el: HTMLElement) => {
    el.className = ICON_BTN + ' text-green'
  }
  const chipOff = (el: HTMLElement) => {
    el.className = ICON_BTN + ' text-t2'
  }

  const applyViewMode = (inst: RFB) => {
    // noVNC's _updateClip() lets scaling trump clipping, so 1:1 requires
    // scaleViewport off; dragViewport turns a finger drag into a local pan
    // instead of a remote left-drag.
    const oneToOne = viewMode === 'oneToOne'
    inst.scaleViewport = !oneToOne
    inst.clipViewport = oneToOne
    inst.dragViewport = oneToOne
  }

  // Chrome pans the visual viewport to reveal a focused input by default
  // (interactive-widget=resizes-visual), which pushes the whole page up when
  // the hidden keystroke capture is focused. overlays-content makes the IME
  // simply cover the canvas instead — scoped to the console's lifetime so
  // the chat input keeps its ride-above-keyboard behaviour.
  const setIMEOverlay = (on: boolean) => {
    const meta = document.querySelector('meta[name="viewport"]')
    if (!meta) return
    const KEY = ', interactive-widget=overlays-content'
    const cur = meta.getAttribute('content') ?? ''
    if (on && !cur.includes('interactive-widget')) {
      meta.setAttribute('content', cur + KEY)
    } else if (!on && cur.includes(KEY)) {
      meta.setAttribute('content', cur.replace(KEY, ''))
    }
  }

  const setState = (key: StaticKey, suffix = '') => {
    stateEl.classList.remove('hidden')
    stateEl.textContent = t(key) + suffix
  }

  const connect = () => {
    if (!device) return
    const seq = ++connectSeq
    rfb?.disconnect()
    rfb = null
    // The RFB constructor appends its canvas into the screen — drop the old
    // session's canvas while keeping the state overlay and the keystroke
    // capture (replaceChildren wipes siblings; kbdInput lives in screen).
    screen.replaceChildren(stateEl, kbdInput)
    setState('rdStateConnecting')
    latencyEl.textContent = '…'
    void (async () => {
      let RFBClass: typeof import('@novnc/novnc').default
      try {
        RFBClass = await loadRFB()
      } catch (err) {
        // A superseded session must not clobber the newer one's state;
        // a live one keeps the cause in the console — the original bug was
        // an unexplained load failure with nothing to go on.
        if (seq !== connectSeq) return
        console.error('VNC: failed to load the RFB module', err)
        setState('rdStateFailed')
        return
      }
      if (seq !== connectSeq) return
      // wss when the wui itself is https (tailscale serve) — ws:// would be
      // blocked as mixed content.
      const wsProto = location.protocol === 'https:' ? 'wss' : 'ws'
      const inst = new RFBClass(screen, `${wsProto}://${location.host}/wui/vnc/${encodeURIComponent(device!.name)}`, {
        credentials: device!.pass ? { password: device!.pass } : undefined,
        wsProtocols: ['binary'],
      })
      rfb = inst
      sessionDead = false
      inst.addEventListener('connect', () => {
        stateEl.classList.add('hidden')
      })
      inst.addEventListener('disconnect', (e: CustomEvent) => {
        sessionDead = true
        setState(e.detail.clean === true ? 'rdStateDisconnected' : 'rdStateFailed')
      })
      inst.addEventListener('securityfailure', (e: CustomEvent) => {
        // reason is server-sent raw text, deliberately not localized
        setState('rdStateFailed', e.detail.reason ? ` — ${e.detail.reason}` : '')
      })
      inst.addEventListener('credentialsrequired', () => {
        if (device!.pass) inst.sendCredentials({ password: device!.pass })
        else setState('rdStatePassword')
      })
      inst.viewOnly = mode === 'view'
      applyViewMode(inst)
      // Latency label = server-side probe (WS dial time), refreshed per
      // (re)connect; failures clear it (stale ms numbers lie).
      void testRemoteDevice(device!.addr, device!.pass)
        .then((res) => {
          if (seq !== connectSeq) return
          latencyEl.textContent = res.ok ? `${res.latencyMs ?? 0}ms` : ''
        })
        .catch(() => {
          if (seq !== connectSeq) return
          latencyEl.textContent = ''
        })
    })()
  }

  controlChip.addEventListener('click', () => {
    mode = mode === 'control' ? 'view' : 'control'
    if (mode === 'control') chipOn(controlChip)
    else chipOff(controlChip)
    // viewOnly is a live property in noVNC (its setter grabs/ungrabs the
    // keyboard), so an established session keeps its screen and auth — only
    // a dead session needs rebuilding to pick up the mode.
    if (rfb && !sessionDead) {
      if (mode === 'view') {
        // noVNC's setter only ungrabs the keyboard; a button held down at
        // this moment would stay pressed forever on the server (stuck drag).
        // A synthetic mouseup (buttons=0 → mask 0) releases it — dispatched
        // while still in control so it passes the viewOnly guard.
        screen
          .querySelector('canvas')
          ?.dispatchEvent(new MouseEvent('mouseup', { bubbles: true, button: 0 }))
      }
      rfb.viewOnly = mode === 'view'
    } else connect()
  })
  viewChip.addEventListener('click', () => {
    viewMode = viewMode === 'oneToOne' ? 'fit' : 'oneToOne'
    if (viewMode === 'oneToOne') chipOn(viewChip)
    else chipOff(viewChip)
    // Live viewport properties (setters recalc clip/scale) — no reconnect.
    if (rfb && !sessionDead) applyViewMode(rfb)
    else connect()
  })
  keyboardBtn.addEventListener('click', () => {
    if (document.activeElement === kbdInput) kbdInput.blur()
    else kbdInput.focus()
  })
  kbdInput.addEventListener('focus', () => chipOn(keyboardBtn))
  const keyboardModeOff = () => chipOff(keyboardBtn)
  // Special keys ride keydown (they never surface as insertText).
  kbdInput.addEventListener('keydown', (e: KeyboardEvent) => {
    if (!rfb) return
    if (e.key === 'Backspace') {
      rfb.sendKey(0xff08, null, true)
      rfb.sendKey(0xff08, null, false)
      e.preventDefault()
    } else if (e.key === 'Enter') {
      rfb.sendKey(0xff0d, null, true)
      rfb.sendKey(0xff0d, null, false)
      e.preventDefault()
    }
  })
  // QEMU's VNC reverse keymap drops Shift for punctuation when a bare
  // keysym arrives (':' types as ';'), so shifted ASCII is composed as
  // Shift+base key — the vncdotool --force-caps precedent (SPECIAL_KEYS_US).
  // Non-ASCII has no en-US scancode at all: those characters ride the guest
  // IME or clipboard, never keystrokes.
  const SHIFTED_US: Record<string, string> = {
    '~': '`', '!': '1', '@': '2', '#': '3', '$': '4', '%': '5', '^': '6',
    '&': '7', '*': '8', '(': '9', ')': '0', '_': '-', '+': '=',
    '{': '[', '}': ']', '|': '\\', ':': ';', '"': "'", '<': ',', '>': '.',
    '?': '/',
  }
  const kbdToast = createElement(
    'div',
    'fixed left-1/2 -translate-x-1/2 bottom-24 z-[70] rounded-full bg-ink3 border border-hairline px-4 py-1.5 text-[12px] text-t2 shadow-lg opacity-0 transition-opacity duration-300 pointer-events-none',
  )
  kbdToast.setAttribute('data-vnc-toast', '')
  sheet.append(kbdToast)
  let kbdToastTimer: ReturnType<typeof setTimeout> | null = null
  const showKbdToast = (msg: string) => {
    kbdToast.textContent = msg
    kbdToast.classList.remove('opacity-0')
    if (kbdToastTimer) clearTimeout(kbdToastTimer)
    kbdToastTimer = setTimeout(() => kbdToast.classList.add('opacity-0'), 2500)
  }
  const sendCharAsKeys = (inst: RFB, ch: string): boolean => {
    const base = SHIFTED_US[ch] ?? (ch >= 'A' && ch <= 'Z' ? ch.toLowerCase() : null)
    if (base) {
      inst.sendKey(0xffe1, null, true)
      const k = base.charCodeAt(0)
      inst.sendKey(k, null, true)
      inst.sendKey(k, null, false)
      inst.sendKey(0xffe1, null, false)
      return true
    }
    const cp = ch.codePointAt(0)!
    if (cp > 0x7f) return false
    inst.sendKey(cp, null, true)
    inst.sendKey(cp, null, false)
    return true
  }
  // Printed characters (incl. IME composition results) arrive here.
  kbdInput.addEventListener('input', (ev) => {
    const e = ev as InputEvent
    if (!rfb || !e.data) return
    let skipped = false
    for (const ch of e.data) {
      if (!sendCharAsKeys(rfb, ch)) skipped = true
    }
    if (skipped) showKbdToast(t('rdKbdNonAscii'))
    kbdInput.value = ''
  })
  kbdInput.addEventListener('blur', () => keyboardModeOff())
  fullscreenChip.addEventListener('click', () => {
    // Native fullscreen where the browser grants it; WebViews without
    // custom-view support silently no-op there, so fall back to an
    // in-page maximize that behaves the same on a phone.
    if (document.fullscreenEnabled) {
      if (document.fullscreenElement) document.exitFullscreen?.()
      else sheet.requestFullscreen?.()
    } else {
      sheet.classList.toggle('vnc-max')
      if (sheet.classList.contains('vnc-max')) chipOn(fullscreenChip)
      else chipOff(fullscreenChip)
      // scaleViewport listens to WINDOW resizes — a container-only class
      // toggle doesn't fire one, so the canvas would stay at the old
      // scale inside the enlarged sheet.
      window.dispatchEvent(new Event('resize'))
    }
  })
  document.addEventListener('fullscreenchange', () => {
    const inside = document.fullscreenElement !== null && sheet.contains(document.fullscreenElement)
    if (inside) chipOn(fullscreenChip)
    else chipOff(fullscreenChip)
  })

  const openSheet = (d: RemoteDevice) => {
    device = d
    // Always start in control: mainstream mobile remote clients default to
    // interactive with view-only as the opt-out, and in the default fit
    // view a finger has no pan need that control mode would steal.
    mode = 'control'
    chipOn(controlChip)
    viewMode = 'fit'
    chipOff(viewChip)
    chipOff(fullscreenChip)
    keyboardModeOff()
    if (document.activeElement === kbdInput) kbdInput.blur()
    kbdInput.value = ''
    sheet.classList.remove('vnc-max')
    titleEl.textContent = d.name
    root.style.display = ''
    setIMEOverlay(true)
    connect()
  }
  const closeSheet = () => {
    connectSeq++
    // Before disconnect(): a throw there would otherwise strand the meta on,
    // leaving the chat's input typing blind under the IME until a reload.
    setIMEOverlay(false)
    rfb?.disconnect()
    rfb = null
    root.style.display = 'none'
    if (document.fullscreenElement && sheet.contains(document.fullscreenElement)) {
      document.exitFullscreen?.()
    }
  }
  closeBtn.addEventListener('click', closeSheet)
  root.addEventListener('click', (e) => {
    if (e.target === root) closeSheet()
  })

  return { root, open: openSheet, close: closeSheet, isOpen: () => root.style.display !== 'none' }
}
