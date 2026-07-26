import { describe, it, expect } from 'vitest'
import {
  progressRingCircles,
  progressRingDashOffset,
  PROGRESS_RING_CIRCUMFERENCE,
} from './progress_ring'

describe('progressRingCircles', () => {
  it('emits chat-style ring with scroll classes and 150ms ease-out', () => {
    const s = progressRingCircles({
      progressClassName: 'scroll-progress',
      backgroundClassName: 'scroll-ring',
    })
    expect(s).toContain('class="scroll-ring"')
    expect(s).toContain('class="scroll-progress"')
    expect(s).toContain('stroke-dasharray="113.10"')
    expect(s).toContain('stroke-dashoffset="113.10"')
    expect(s).toContain('style="transition:stroke-dashoffset 150ms ease-out"')
  })

  it('emits task-style ring with opacity, no bg class, 300ms ease', () => {
    const s = progressRingCircles({
      progressClassName: 'task-ring',
      backgroundOpacity: 0.2,
      transitionMs: 300,
      transitionEasing: 'ease',
    })
    expect(s).toContain('opacity="0.2"')
    expect(s).toContain('class="task-ring"')
    expect(s).toContain('style="transition:stroke-dashoffset 300ms ease"')
    expect(s).not.toContain('class="scroll-ring"')
  })

  it('default transition is 150ms ease-out when only progressClassName is given', () => {
    const s = progressRingCircles({ progressClassName: 'x' })
    expect(s).toContain('150ms')
    expect(s).toContain('ease-out')
  })

  it('custom transitionMs leaks into string only when passed', () => {
    const s = progressRingCircles({
      progressClassName: 'p',
      transitionMs: 300,
    })
    expect(s).toContain('300ms')
    expect(s).not.toContain('150ms')
  })
})

describe('progressRingDashOffset', () => {
  it('returns full circumference at progress 0 (empty ring)', () => {
    expect(progressRingDashOffset(0)).toBe(PROGRESS_RING_CIRCUMFERENCE)
  })

  it('returns 0 at progress 1 (full ring)', () => {
    expect(progressRingDashOffset(1)).toBe(0)
  })

  it('matches 2*pi*18*(1-progress) at progress=1/3 within 0.01', () => {
    const expected = 2 * Math.PI * 18 * (1 - 1 / 3)
    expect(progressRingDashOffset(1 / 3)).toBeCloseTo(expected, 2)
  })
})
