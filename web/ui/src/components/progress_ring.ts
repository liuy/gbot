export interface ProgressRingOptions {
  progressClassName: string
  backgroundClassName?: string
  backgroundOpacity?: number
  transitionMs?: number
  transitionEasing?: string
  radius?: number
}

export const PROGRESS_RING_CIRCUMFERENCE: number = 2 * Math.PI * 18

export function progressRingCircles(opts: ProgressRingOptions): string {
  const r = opts.radius ?? 18
  const dashArray = PROGRESS_RING_CIRCUMFERENCE.toFixed(2)
  const ms = opts.transitionMs ?? 150
  const easing = opts.transitionEasing ?? 'ease-out'

  const bgClass = opts.backgroundClassName ? ` class="${opts.backgroundClassName}"` : ''
  const bgOpacity = opts.backgroundOpacity !== undefined ? ` opacity="${opts.backgroundOpacity}"` : ''

  return (
    `<circle${bgClass}${bgOpacity} cx="22" cy="22" r="${r}" fill="none" stroke="currentColor" stroke-width="2"/>` +
    `<circle class="${opts.progressClassName}" cx="22" cy="22" r="${r}" fill="none" stroke="currentColor" stroke-width="2" ` +
    `stroke-linecap="round" stroke-dasharray="${dashArray}" stroke-dashoffset="${dashArray}" ` +
    `transform="rotate(-90 22 22)" style="transition:stroke-dashoffset ${ms}ms ${easing}"/>`
  )
}

export function progressRingDashOffset(progress: number): number {
  return PROGRESS_RING_CIRCUMFERENCE * (1 - progress)
}
