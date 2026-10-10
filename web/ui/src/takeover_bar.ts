import { createNode } from './dom'

export interface TakeoverBarHandles {
  root: HTMLElement
  start: () => void
  finish: () => void
}

// Curve constants from the approved mockup: ease-out pow 2.2 toward a 90%
// ceiling, where the bar waits until the switch completes.
const MAX_PROGRESS = 90
const CURVE_EXPONENT = 2.2
const FADE_DELAY_MS = 300
const TRANSITION = 'width .35s ease, opacity .3s ease'

// No real duration estimate exists for a takeover transfer, so the curve
// runs against an ASSUMED 2s window: t = elapsed / ASSUMED_MS keeps growing
// once the assumption is exceeded, but Math.min(t, 1) inside the curve pins
// the displayed width at the 90% asymptote — the bar waits there instead of
// finishing on its own; only finish() reaches 100%.
const ASSUMED_MS = 2000

// A 2px progress bar at the header's bottom edge, shown while an engine or
// session switch is in flight. The track is always mounted (idle = fill at
// width 0 / opacity 0) so appearing never shifts layout.
export function createTakeoverBar(): TakeoverBarHandles {
  const fill = createNode('div', {
    className: 'takeover-bar-fill',
    attrs: { 'data-testid': 'takeover-bar-fill' },
    style: {
      position: 'absolute',
      left: '0',
      top: '0',
      bottom: '0',
      width: '0%',
      opacity: '0',
      background: 'var(--color-blue)',
      borderRadius: '0 1px 1px 0',
      transition: TRANSITION,
    },
  })
  const root = createNode(
    'div',
    {
      className: 'takeover-bar-track',
      attrs: { 'data-testid': 'takeover-bar-track' },
      style: { height: '2px', position: 'relative', overflow: 'hidden' },
    },
    fill,
  )

  let running = false
  let raf = 0
  let fadeTimer: ReturnType<typeof setTimeout> | null = null

  const stopLoop = () => {
    if (raf) {
      cancelAnimationFrame(raf)
      raf = 0
    }
  }

  const start = () => {
    // ws.ts re-notifies its state on every reconnect retry (250ms cadence
    // while upgrading), so a window that is already running must absorb
    // repeats instead of snapping the fill back to 0% mid-trickle.
    if (running) return
    stopLoop()
    if (fadeTimer) {
      clearTimeout(fadeTimer)
      fadeTimer = null
    }
    // Reset with transitions disabled: a restart can land while the fill
    // still sits at 100% mid-fade, and the width transition would otherwise
    // animate the 100→0 snap backwards before trickling. The forced reflow
    // commits the reset so re-enabling the transition cannot animate it.
    fill.style.transition = 'none'
    fill.style.width = '0%'
    fill.style.opacity = '1'
    void fill.offsetWidth
    fill.style.transition = TRANSITION

    running = true
    const startTs = performance.now()
    const tick = () => {
      const t = (performance.now() - startTs) / ASSUMED_MS
      const eased = 1 - Math.pow(1 - Math.min(t, 1), CURVE_EXPONENT)
      fill.style.width = MAX_PROGRESS * eased + '%'
      // Once t crosses 1 the width is pinned at 90% — stop scheduling
      // frames until finish() or a restart; the bar just waits there.
      if (t < 1) raf = requestAnimationFrame(tick)
      else raf = 0
    }
    raf = requestAnimationFrame(tick)
  }

  const finish = () => {
    // Boot metadata (and any frame outside a switch window) must never
    // flash the bar: finish is meaningful only while a switch is running.
    if (!running) return
    stopLoop()
    running = false
    fill.style.width = '100%'
    fadeTimer = setTimeout(() => {
      fadeTimer = null
      fill.style.opacity = '0'
    }, FADE_DELAY_MS)
  }

  return { root, start, finish }
}
