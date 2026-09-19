import type RFB from '@novnc/novnc'
import { createElement, createNode } from './dom'
import { t, type StaticKey } from './i18n'
import { renderIcon, type IconName } from './icons'
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

  const head = createElement('div', 'flex items-center gap-2.5 px-4 pb-2 pt-1 text-sm font-semibold')
  const titleEl = createNode('span', { className: 'flex-1', attrs: { 'data-vnc-title': '' } })
  const closeBtn = createNode('span', {
    className: 'text-t3 cursor-pointer px-2 text-[15px]',
    text: '✕',
    attrs: { 'data-vnc-close': '' },
  })
  head.append(titleEl, closeBtn)

  const bar = createElement(
    'div',
    'flex flex-wrap items-center gap-[7px] px-3.5 pb-2.5 text-[11px] text-t2',
  )
  bar.setAttribute('data-vnc-bar', '')
  const iconBtn = (id: string, icon: IconName, labelKey: StaticKey): HTMLElement => {
    const el = createNode('span', {
      className: ICON_BTN,
      attrs: { 'data-vnc-chip': id },
    })
    el.append(renderIcon(icon, { size: 15 }))
    el.setAttribute('title', t(labelKey))
    return el
  }
  // Green pointer = control; grey = view-only — one binary, one button.
  const controlChip = iconBtn('control', 'pointer', 'rdChipControl')
  const fullscreenChip = iconBtn('fullscreen', 'maximize', 'rdChipFullscreen')
  const latencyEl = createNode('span', { className: 'ml-auto text-t3', attrs: { 'data-vnc-latency': '' } })
  const keyboardBtn = createNode('span', {
    className: ICON_BTN,
    attrs: { 'data-vnc-keyboard': '' },
  })
  keyboardBtn.append(renderIcon('keyboard', { size: 15 }))
  keyboardBtn.setAttribute('title', t('rdChipClipboard'))
  bar.append(controlChip, keyboardBtn, fullscreenChip, latencyEl)

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

  sheet.append(head, bar, screen, kbdInput)
  root.append(sheet)

  sheet.append(head, bar, screen)
  root.append(sheet)

  // ------------------------------------------------------------- state
  let rfb: RFB | null = null
  let device: RemoteDevice | null = null
  // Bumped on every close/open/connect: stale async import() resolutions and
  // probe results from a superseded session are dropped by seq re-check.
  let connectSeq = 0
  let mode: 'view' | 'control' = 'view'

  const chipOn = (el: HTMLElement) => {
    el.className = ICON_BTN + ' text-green'
  }
  const chipOff = (el: HTMLElement) => {
    el.className = ICON_BTN + ' text-t2'
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
    // session's DOM while keeping the state overlay AND the input bar
    // (replaceChildren wipes siblings; the bar lives inside the screen).
    screen.replaceChildren(stateEl, kbdInput)
    setState('rdStateConnecting')
    latencyEl.textContent = '…'
    void (async () => {
      const { default: RFBClass } = await import('@novnc/novnc')
      if (seq !== connectSeq) return
      const inst = new RFBClass(screen, `ws://${location.host}/wui/vnc/${encodeURIComponent(device!.name)}`, {
        credentials: device!.pass ? { password: device!.pass } : undefined,
        wsProtocols: ['binary'],
      })
      rfb = inst
      inst.addEventListener('connect', () => {
        stateEl.classList.add('hidden')
      })
      inst.addEventListener('disconnect', (e: CustomEvent) => {
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
      inst.scaleViewport = true
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
    connect()
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
  // Printed characters (incl. IME composition results) arrive here.
  kbdInput.addEventListener('input', (ev) => {
    const e = ev as InputEvent
    if (!rfb || !e.data) return
    for (const ch of e.data) {
      const cp = ch.codePointAt(0)!
      const keysym = cp > 0xff ? 0x01000000 + cp : cp
      rfb.sendKey(keysym, null, true)
      rfb.sendKey(keysym, null, false)
    }
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
    mode = 'view'
    chipOff(controlChip)
    chipOff(fullscreenChip)
    keyboardModeOff()
    if (document.activeElement === kbdInput) kbdInput.blur()
    kbdInput.value = ''
    sheet.classList.remove('vnc-max')
    titleEl.textContent = t('rdConsole')(d.name)
    root.style.display = ''
    connect()
  }
  const closeSheet = () => {
    connectSeq++
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
