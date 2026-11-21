// gbot browser plugin UI module (plain ESM, zero dependencies, live-editable).
// Loaded by the wui card host at /plugins/browser/ui. Contract:
//   export function mount(container, ctx) -> optional cleanup
//     ctx.openChannel(plugin) -> Channel   one pipe to the bridge
//     ctx.close()                           closes the card
//     ctx.headerSlot()                      empty card-header slot; the module
//                                           owns its classes and children
//
// Wire: each pipe frame is ONE CDP JSON message (NDJSON both directions,
// mirroring CDP-over-WS text frames). The module attaches to the page target
// the LLM is driving, streams Page.screencast frames onto a canvas, follows
// target switches, and dispatches input back into the page.
//
// Input model: page coordinates come from the LAST frame's deviceWidth/
// deviceHeight (remote CSS px) mapped through the canvas's on-screen rect.
// Touch pointers classify into tap (press+release) vs drag (wheel stream) —
// remote drag is not offered on touch in v1; mouse pointers map 1:1.

// ------------------------------------------------------------------ cdp client

// Minimal CDP client: request/response by id + event dispatch. Deliberately
// not puppeteer-core — six methods beat a 1 MB dependency. Exported for the
// unit tests (dispose/lifecycle behavior is not observable through mount).
export class CDPClient {
  constructor(sendBytes, requestTimeoutMs = 15000) {
    this.sendBytes = sendBytes
    this.requestTimeoutMs = requestTimeoutMs
    this.nextId = 1
    this.pending = new Map()
    this.handlers = new Map()
    this.disposed = false
    // onStatus receives bridge control frames ({"__status": "idle"|..."}).
    this.onStatus = null
  }

  // feed handles ONE decoded pipe payload (one CDP JSON message).
  feed(text) {
    if (this.disposed) return
    let msg
    try {
      msg = JSON.parse(text)
    } catch {
      return
    }
    // Bridge control frames carry a reserved __status key and are checked
    // before CDP id/method dispatch (an adversarial __status+id frame still
    // routes to the control path).
    if (msg.__status !== undefined) {
      if (this.onStatus) this.onStatus(msg.__status)
      return
    }
    if (msg.id !== undefined) {
      const p = this.pending.get(msg.id)
      if (p) {
        this.pending.delete(msg.id)
        clearTimeout(p.timer)
        if (msg.error) p.reject(new Error(msg.error.message || 'cdp error'))
        else p.resolve(msg.result)
      }
      return
    }
    if (msg.method) {
      const cbs = this.handlers.get(msg.method)
      if (cbs) for (const cb of cbs) cb(msg.params, msg.sessionId)
    }
  }

  // send fires one CDP command; the id is returned for raw callers.
  send(method, params, sessionId) {
    const id = this.nextId++
    const msg = { id, method, params: params || {} }
    if (sessionId) msg.sessionId = sessionId
    this.sendBytes(new TextEncoder().encode(JSON.stringify(msg)))
    return id
  }

  // request = send + promise, resolved by the matching id reply. timeoutMs
  // overrides requestTimeoutMs per call (the attach probe passes a short
  // one: the reply is expected in milliseconds, and a 15s dead wait only
  // lengthens the dead end).
  request(method, params, sessionId, timeoutMs) {
    return new Promise((resolve, reject) => {
      const id = this.send(method, params, sessionId)
      const timer = setTimeout(() => {
        this.pending.delete(id)
        reject(new Error(`cdp timeout: ${method}`))
      }, timeoutMs || this.requestTimeoutMs)
      this.pending.set(id, { resolve, reject, timer })
    })
  }

  on(method, cb) {
    let set = this.handlers.get(method)
    if (!set) {
      set = new Set()
      this.handlers.set(method, set)
    }
    set.add(cb)
    return () => set.delete(cb)
  }

  dispose() {
    this.disposed = true
    for (const [, p] of this.pending) {
      clearTimeout(p.timer)
      p.reject(new Error('cdp client disposed'))
    }
    this.pending.clear()
    this.handlers.clear()
  }
}

// --------------------------------------------------------------------- module

function el(tag, className) {
  const e = document.createElement(tag)
  if (className) e.className = className
  return e
}

// --------------------------------------------------------------------- icons
// Plugin-local icon registry: named 24-grid outline paths, wui token style
// (stroke currentColor). Adding an icon = one entry here; renderIcon(name)
// returns a sized wrapper element.
const ICONS = {
  keyboard: {
    path: '<rect x="2" y="4" width="20" height="16" rx="2"/><path d="M6 8h.01M10 8h.01M14 8h.01M18 8h.01M6 12h.01M10 12h.01M14 12h.01M18 12h.01M8 16h8"/>',
    width: 2,
  },
}

function renderIcon(name, { size = 15 } = {}) {
  const ic = ICONS[name]
  if (!ic) return null
  const wrap = el('span', 'flex items-center')
  wrap.innerHTML =
    `<svg width="${size}" height="${size}" viewBox="0 0 24 24" fill="none" ` +
    `stroke="currentColor" stroke-width="${ic.width}" stroke-linecap="round" ` +
    `stroke-linejoin="round">${ic.path}</svg>`
  return wrap
}

export function mount(container, ctx) {
  container.innerHTML = ''
  const canvas = el('canvas', 'w-full block bg-black')
  // The canvas owns every touch: browser panning/zoom must not steal them.
  canvas.style.touchAction = 'none'
  // Invisible input carrying soft-keyboard/physical-key text into CDP.
  const hidden = el('input', 'absolute opacity-0 w-px h-px p-0 border-0')
  hidden.autocapitalize = 'off'
  hidden.autocomplete = 'off'
  const status = el('div', 'absolute inset-0 flex items-center justify-center text-[12px] text-t2')
  status.textContent = 'connecting…'
  container.append(canvas, hidden, status)
  // Keyboard toggle lives in the HOST header row (outside the viewport):
  // the host hands over an empty slot via ctx.headerSlot(); this module
  // takes over its layout (right-aligned, host-gap spacing) and drops the
  // button in, keeping the focus-highlight logic.
  // Focusing the hidden input summons the mobile soft keyboard. Not on
  // canvas taps — every scroll/pan would pop the keyboard.
  const kbBtn = el('button', 'cursor-pointer px-1 select-none flex items-center text-t2')
  kbBtn.type = 'button'
  kbBtn.title = 'Keyboard'
  kbBtn.append(renderIcon('keyboard'))
  const slot = ctx.headerSlot()
  slot.append(kbBtn)

  let channel = null
  let cdp = null
  let session = null
  let followTarget = null
  let disposed = false
  // Probe state lives in mount scope so the cleanup closure returned by
  // mount can clearTimeout(probeTimer).
  let probeTimer = null
  // Remote page CSS-pixel size, from the last screencast frame's metadata.
  let devW = 0
  let devH = 0

  const setStatus = (s) => {
    status.textContent = s
    status.style.display = s ? '' : 'none'
  }

  // Input.* helpers — every dispatch is session-scoped and dropped while
  // detached (frames would land on the wrong or no target).
  const sendInput = (method, params) => {
    if (session && cdp && !disposed) cdp.send(method, params, session)
  }
  const mouse = (type, p, extra = {}) =>
    sendInput('Input.dispatchMouseEvent', { type, x: p.x, y: p.y, ...extra })

  const drawFrame = (base64, metadata) => {
    setStatus('')
    // Recorded even when bitmap decode is unavailable: input mapping
    // depends on the remote size, not on drawing.
    if (metadata.deviceWidth) devW = metadata.deviceWidth
    if (metadata.deviceHeight) devH = metadata.deviceHeight
    const c2d = canvas.getContext('2d')
    if (!c2d) return
    if (typeof createImageBitmap !== 'function' || typeof Blob === 'undefined') return
    const blob = new Blob([Uint8Array.from(atob(base64), (c) => c.charCodeAt(0))], { type: 'image/jpeg' })
    void createImageBitmap(blob).then((bmp) => {
      // Backing store at FULL bitmap resolution: the phone's dense screen
      // then samples it down for display, preserving every pixel. Sizing at
      // CSS scale crushed the bitmap ~8x and was the source of the blur.
      if (canvas.width !== bmp.width || canvas.height !== bmp.height) {
        canvas.width = bmp.width
        canvas.height = bmp.height
      }
      c2d.drawImage(bmp, 0, 0)
      bmp.close()
    }).catch(() => {
      // A corrupt frame drops silently: screencast sends the next one.
    })
  }

  const stopSession = () => {
    if (session && cdp) {
      // Best-effort; a dead session ignores this and the server GCs it.
      cdp.send('Target.detachFromTarget', { sessionId: session })
    }
    session = null
  }

  const attachTo = async (targetId) => {
    if (!cdp || disposed) return
    stopSession()
    followTarget = targetId
    try {
      const res = await cdp.request('Target.attachToTarget', { targetId, flatten: true })
      if (disposed || followTarget !== targetId) {
        cdp.send('Target.detachFromTarget', { sessionId: res.sessionId })
        return
      }
      session = res.sessionId
      await cdp.request('Page.startScreencast', {
        format: 'jpeg',
        maxWidth: 1920,
        quality: 88,
        everyNthFrame: 2, // cap the frame rate; animation pages would otherwise flood the pipe
      }, session)
      setStatus('live')
    } catch (e) {
      setStatus(`attach failed: ${String(e)}`)
    }
  }

  const pickTarget = (infos) => {
    const pages = infos.filter((ti) => ti.type === 'page')
    return pages.length ? pages[pages.length - 1].targetId : null
  }

  // ------------------------------------------------------------- input lane

  // CSS px on screen → CSS px on the remote page. A zero rect degrades to
  // identity rather than NaN.
  const ratio = () => {
    const rect = canvas.getBoundingClientRect()
    return {
      x: rect.width > 0 && devW > 0 ? devW / rect.width : 1,
      y: rect.height > 0 && devH > 0 ? devH / rect.height : 1,
    }
  }
  const toPage = (offsetX, offsetY) => {
    const r = ratio()
    return { x: Math.round(offsetX * r.x), y: Math.round(offsetY * r.y) }
  }

  // Touch gesture classification: under the slop a lift is a tap (click);
  // crossing it switches to a wheel stream (drag = scroll).
  const TOUCH_SLOP = 8
  let activePointer = null
  let touchDragging = false
  let lastTouch = { x: 0, y: 0 }

  canvas.addEventListener('contextmenu', (e) => e.preventDefault())
  canvas.addEventListener('pointerdown', (e) => {
    e.preventDefault()
    activePointer = e.pointerId
    if (e.pointerType === 'touch') {
      touchDragging = false
      lastTouch = { x: e.offsetX, y: e.offsetY }
    } else {
      mouse('mousePressed', toPage(e.offsetX, e.offsetY), { button: 'left', buttons: 1, clickCount: 1 })
    }
  })
  canvas.addEventListener('pointermove', (e) => {
    if (e.pointerId !== activePointer) return
    if (e.pointerType === 'touch') {
      const dx = e.offsetX - lastTouch.x
      const dy = e.offsetY - lastTouch.y
      if (!touchDragging && Math.hypot(dx, dy) < TOUCH_SLOP) return
      touchDragging = true
      // Dragging up must scroll content down: invert the delta like a
      // touchpad, then scale into remote page pixels.
      const r = ratio()
      mouse('mouseWheel', toPage(e.offsetX, e.offsetY), {
        button: 'none',
        deltaX: -Math.round(dx * r.x),
        deltaY: -Math.round(dy * r.y),
      })
      lastTouch = { x: e.offsetX, y: e.offsetY }
    } else {
      mouse('mouseMoved', toPage(e.offsetX, e.offsetY), { button: 'none', buttons: e.buttons })
    }
  })
  const pointerEnd = (e) => {
    if (e.pointerId !== activePointer) return
    activePointer = null
    if (e.pointerType === 'touch') {
      if (!touchDragging) {
        const p = toPage(e.offsetX, e.offsetY)
        mouse('mousePressed', p, { button: 'left', buttons: 1, clickCount: 1 })
        mouse('mouseReleased', p, { button: 'left', buttons: 0, clickCount: 1 })
      }
    } else {
      mouse('mouseReleased', toPage(e.offsetX, e.offsetY), { button: 'left', buttons: 0, clickCount: 1 })
    }
  }
  canvas.addEventListener('pointerup', pointerEnd)
  canvas.addEventListener('pointercancel', (e) => {
    if (e.pointerId === activePointer) activePointer = null
  })
  canvas.addEventListener('wheel', (e) => {
    e.preventDefault()
    mouse('mouseWheel', toPage(e.offsetX, e.offsetY), {
      button: 'none',
      deltaX: Math.round(e.deltaX),
      deltaY: Math.round(e.deltaY),
    })
  })

  // Keyboard: the hidden input exists only to host the soft keyboard. It is
  // kept empty so every `input` event's `data` is exactly the typed delta.
  const keyEvent = (key, code, text) => {
    sendInput('Input.dispatchKeyEvent', {
      type: 'keyDown', key, code: key, windowsVirtualKeyCode: code, ...(text ? { text } : {}),
    })
    sendInput('Input.dispatchKeyEvent', { type: 'keyUp', key, code: key, windowsVirtualKeyCode: code })
  }
  kbBtn.addEventListener('click', () => {
    if (document.activeElement === hidden) hidden.blur()
    else hidden.focus()
  })
  hidden.addEventListener('focus', () => kbBtn.classList.add('text-t1'))
  hidden.addEventListener('blur', () => {
    kbBtn.classList.remove('text-t1')
    hidden.value = ''
  })
  hidden.addEventListener('input', (e) => {
    if (typeof e.data === 'string' && e.data) {
      sendInput('Input.insertText', { text: e.data })
    } else if (e.inputType === 'deleteContentBackward') {
      // Soft keyboards report backspace as a deletion on empty content.
      keyEvent('Backspace', 8)
    }
    hidden.value = ''
  })
  hidden.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      keyEvent('Enter', 13, '\r')
    } else if (e.key === 'Tab') {
      e.preventDefault()
      keyEvent('Tab', 9)
    } else if (e.key === 'Backspace' && hidden.value === '') {
      // Physical backspace on the already-empty host fires no input event.
      e.preventDefault()
      keyEvent('Backspace', 8)
    }
  })

  const start = () => {
    if (disposed) return
    channel = ctx.openChannel('browser')
    cdp = new CDPClient((bytes) => channel.send(bytes))
    channel.setOnMessage((data) => cdp.feed(new TextDecoder().decode(data)))
    // No auto-retry: a closed channel shows the disconnected state and the
    // user reopens the card (fresh open). A retry loop here turned a daemon
    // bug into an infinite silent "connecting…" — reopen is the honest UX.
    channel.setOnClose(() => {
      if (disposed) return
      session = null
      setStatus('disconnected — reopen the card')
    })

    // Probe model: chromium is launched lazily by playwright-mcp on its
    // first tool call. While it is down the bridge replies "browser idle";
    // ANY failed attach (idle, timeout, a frame lost across an engine
    // switch) re-arms the probe every 1.5s (fixed — a refused fetch is
    // cheap) until the attach succeeds or the card closes. ui drives
    // probing so protocol.js stays a stateless pump.
    const attachChain = () =>
      cdp
        .request('Target.getTargets', {}, undefined, 3000)
        .then((res) => {
          const target = pickTarget(res.targetInfos)
          if (!target) {
            // Chromium up but no page yet (playwright always makes one, but a
            // closed last tab can leave a gap): keep probing instead of
            // settling, or the card hangs with no event to revive it.
            enterIdle('no page target')
            return
          }
          // Subscribe to target lifecycle before attaching so a switch made
          // mid-attach is not missed.
          cdp.send('Target.setDiscoverTargets', { discover: true })
          return attachTo(target)
        })
        .catch((e) => {
          // Every failure re-arms the probe loop: the fixed cadence
          // self-heals transient/timing breakage; the status stays honest
          // about the cause instead of dying silently.
          const idle = String(e.message) === 'browser idle'
          enterIdle(idle ? 'browser not running' : `connect failed: ${String(e)}`)
        })
    const enterIdle = (label = 'browser not running') => {
      if (disposed) return
      setStatus(label)
      // A failed probe re-enters here and must re-arm the timer, or the
      // loop dies after one probe and a late chromium never gets picked up.
      if (!probeTimer) {
        probeTimer = setTimeout(() => {
          probeTimer = null
          if (!disposed) attachChain()
        }, 1500) // REAL-TIME: fixed probe cadence while chromium is down
      }
    }
    cdp.onStatus = (s) => {
      if (disposed) return
      if (s === 'idle') enterIdle()
      else if (s === 'attached') {
        if (probeTimer) {
          clearTimeout(probeTimer)
          probeTimer = null
        }
        setStatus('')
        // Always rerun the chain: after a chromium restart the bridge
        // redials while the card may still hold a stale session (neither
        // idle nor session-less), and the fresh connection never re-sends
        // setDiscoverTargets — only a new attachChain revives the card.
        // Duplicate attaches are absorbed by attachTo's followTarget check
        // plus stopSession.
        attachChain()
      }
    }

    void attachChain()

    cdp.on('Target.targetCreated', (params) => {
      const ti = params && params.targetInfo
      if (ti && ti.type === 'page') void attachTo(ti.targetId)
    })
    cdp.on('Target.targetCrashed', (params) => {
      if (params.targetId === followTarget) {
        stopSession()
        setStatus('target crashed; waiting…')
      }
    })
    // A closed followed tab must tear the session down at once and re-probe
    // for the next page target instead of streaming a frozen last frame.
    cdp.on('Target.targetDestroyed', (params) => {
      if (params && params.targetId === followTarget) {
        stopSession()
        attachChain()
      }
    })
    cdp.on('Page.screencastFrame', (params, sessionId) => {
      if (!params || !sessionId || sessionId !== session) return
      cdp.send('Page.screencastFrameAck', { sessionId: params.sessionId }, session)
      drawFrame(params.data, params.metadata)
    })
  }
  start()

  return () => {
    disposed = true
    if (probeTimer) clearTimeout(probeTimer)
    stopSession()
    if (cdp) cdp.dispose()
    if (channel) channel.close()
  }
}
