// gbot fork addition (not upstream).
// Pure math only (no three imports) so vitest's jsdom environment can pin
// every contract without WebGL.

// Spec-silent tuning constants; module-level so retuning is a one-line edit.
export const EYE_HEIGHT = 1.6;          // m
export const WALK_SPEED = 3.0;          // m/s
export const LOOK_SENSITIVITY = 0.005;  // rad per px (drag / locked mouse)
export const PITCH_LIMIT = 1.4835;      // ±85°
export const JOYSTICK_RADIUS = 48;      // px
export const JOYSTICK_DEADZONE = 8;     // px
export const GROUND_DAMP = 12;          // 1/s exp smoothing of eye height

// World step from an input axis: forward is -Z at yaw 0 (camera space), so
// the yaw-rotated step is (axisX strafe, axisZ forward) * WALK_SPEED * dt.
export function integrateMove(
    x: number, z: number, yaw: number, axisX: number, axisZ: number,
    dt: number): {x: number, z: number} {
  const sin = Math.sin(yaw);
  const cos = Math.cos(yaw);
  const distance = WALK_SPEED * dt;
  return {
    x: x + (axisX * cos - axisZ * sin) * distance,
    z: z + (-axisX * sin - axisZ * cos) * distance
  };
}

// Exp-damped eye height toward hitY + EYE_HEIGHT; a null hit (over a void)
// leaves the eye untouched rather than dropping it — the walker ghosts over
// gaps instead of falling.
export function groundEyeY(
    currentEyeY: number, hitY: number | null, dt: number): number {
  if (hitY == null) {
    return currentEyeY;
  }
  const target = hitY + EYE_HEIGHT;
  const blend = 1 - Math.exp(-GROUND_DAMP * dt);
  return currentEyeY + (target - currentEyeY) * blend;
}

export function clampPitch(pitch: number): number {
  return Math.max(-PITCH_LIMIT, Math.min(PITCH_LIMIT, pitch));
}

// Radial dead zone with a unit-circle clamp at JOYSTICK_RADIUS: (48, 48)
// resolves to a diagonal of magnitude 1, not sqrt(2), and (9, 0) stays a
// feather-touch instead of jumping to full speed past the dead-zone edge.
export function joystickAxis(dxPx: number, dyPx: number): {x: number, y: number} {
  const magnitude = Math.hypot(dxPx, dyPx);
  if (magnitude <= JOYSTICK_DEADZONE) {
    return {x: 0, y: 0};
  }
  const clamped = Math.min(magnitude, JOYSTICK_RADIUS);
  const factor =
      (clamped - JOYSTICK_DEADZONE) / (JOYSTICK_RADIUS - JOYSTICK_DEADZONE);
  return {
    x: (dxPx / magnitude) * factor,
    y: (dyPx / magnitude) * factor
  };
}
