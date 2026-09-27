import { describe, expect, it } from 'vitest'

import {
  EYE_HEIGHT,
  PITCH_LIMIT,
  WALK_SPEED,
  clampPitch,
  groundEyeY,
  integrateMove,
  joystickAxis,
} from './walk-math'

describe('integrateMove', () => {
  it('forward at yaw 0 moves toward -Z', () => {
    expect(integrateMove(0, 0, 0, 0, 1, 1)).toEqual({x: 0, z: -WALK_SPEED})
  })

  it('forward at yaw π/2 moves toward -X', () => {
    const step = integrateMove(0, 0, Math.PI / 2, 0, 1, 1)
    // cos(π/2) is ~6e-17, not 0 — trig axes cannot be compared exactly.
    expect(step.x).toBeCloseTo(-WALK_SPEED, 12)
    expect(step.z).toBeCloseTo(0, 12)
  })

  it('strafe right at yaw 0 moves toward +X', () => {
    expect(integrateMove(0, 0, 0, 1, 0, 1)).toEqual({x: WALK_SPEED, z: 0})
  })

  it('diagonal axes move at WALK_SPEED·dt·√2 along the diagonal', () => {
    const step = integrateMove(0, 0, 0, 1, 1, 1)
    expect(step.x).toBe(WALK_SPEED)
    expect(step.z).toBe(-WALK_SPEED)
    expect(Math.hypot(step.x, step.z)).toBeCloseTo(WALK_SPEED * 1 * Math.SQRT2, 12)
  })

  it('dt 0 keeps the position unchanged', () => {
    expect(integrateMove(5, -7, 0.3, 1, 1, 0)).toEqual({x: 5, z: -7})
  })

  it('negative forward axis reverses direction', () => {
    expect(integrateMove(0, 0, 0, 0, -1, 1)).toEqual({x: 0, z: WALK_SPEED})
  })
})

describe('groundEyeY', () => {
  it('moves toward hitY + EYE_HEIGHT monotonically from below, never overshooting', () => {
    let y = 1.6
    for (let i = 0; i < 50; i++) {
      const next = groundEyeY(y, 0.2, 1 / 60)
      expect(next).toBeGreaterThan(y)
      expect(next).toBeLessThan(0.2 + EYE_HEIGHT)
      y = next
    }
  })

  it('approaches from above monotonically, never dipping past the target', () => {
    let y = 2.4
    for (let i = 0; i < 50; i++) {
      const next = groundEyeY(y, 0.2, 1 / 60)
      expect(next).toBeLessThan(y)
      expect(next).toBeGreaterThan(0.2 + EYE_HEIGHT)
      y = next
    }
  })

  it('null hit leaves the eye height exactly unchanged (ghost over voids)', () => {
    expect(groundEyeY(1.6, null, 0.5)).toBe(1.6)
  })

  it('large dt converges within 1e-3 of hitY + EYE_HEIGHT', () => {
    expect(Math.abs(groundEyeY(1.6, 0.2, 1) - (0.2 + EYE_HEIGHT))).toBeLessThan(1e-3)
  })
})

describe('clampPitch', () => {
  it('clamps past-limit pitches to ±PITCH_LIMIT and passes mid-range through', () => {
    expect(clampPitch(2)).toBe(PITCH_LIMIT)
    expect(clampPitch(-2)).toBe(-PITCH_LIMIT)
    expect(clampPitch(0.5)).toBe(0.5)
  })
})

describe('joystickAxis', () => {
  it('inside the dead zone (inclusive boundary) is a full stop', () => {
    expect(joystickAxis(0, 0)).toEqual({x: 0, y: 0})
    expect(joystickAxis(3, 4)).toEqual({x: 0, y: 0})
    expect(joystickAxis(8, 0)).toEqual({x: 0, y: 0})
  })

  it('just past the dead zone is a feather touch', () => {
    const axis = joystickAxis(9, 0)
    expect(axis.x).toBeGreaterThan(0)
    expect(axis.x).toBeLessThan(0.05)
    expect(axis.y).toBe(0)
  })

  it('full deflection reaches the unit axes and clamps beyond them', () => {
    expect(joystickAxis(48, 0)).toEqual({x: 1, y: 0})
    expect(joystickAxis(96, 0)).toEqual({x: 1, y: 0})
    expect(joystickAxis(0, -96)).toEqual({x: 0, y: -1})
  })

  it('diagonal over-deflection clamps onto the unit circle, not its square', () => {
    const axis = joystickAxis(48, 48)
    expect(axis.x).toBeCloseTo(Math.SQRT1_2, 12)
    expect(axis.y).toBeCloseTo(Math.SQRT1_2, 12)
    expect(Math.hypot(axis.x, axis.y)).toBeCloseTo(1, 12)
  })
})
