import { describe, expect, it } from 'vitest'

import { EYE_HEIGHT } from './walk-math'
import { solveSpawn, type SpawnProbe } from './walk-spawn'

// Stub probe: floors are decided by a per-(x,z) table, blocked-ness by an
// arbitrary predicate — the solver must work against queries alone.
function stubProbe(
    floors: (x: number, z: number) => number | null,
    blocked: (x: number, y: number, z: number, dirX: number) => boolean,
): SpawnProbe {
  return {
    floorY: (x, z, _fromY) => floors(x, z),
    blocked: (x, y, z, dirX, _dirZ, _far) => blocked(x, y, z, dirX),
  };
}

// The villa-shaped box: 10m tall, so ring radii are 1/2/3/5m.
const MIN_Y = 0;
const MAX_Y = 10;
const CX = 100;
const CZ = 200;

describe('solveSpawn', () => {
  it('takes the center floor hit with clearance and stands at eye height', () => {
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(() => -3, () => false));
    expect(solve.source).toBe('center');
    expect(solve.x).toBe(CX);
    expect(solve.z).toBe(CZ);
    expect(solve.eyeY).toBe(-3 + EYE_HEIGHT);
    expect(solve.centerFloorY).toBe(-3);
    expect(solve.ringRadius).toBeNull();
    expect(solve.ringDir).toBeNull();
  });

  it('probes the ring when the center column misses the floor', () => {
    const floorAt = (x: number, z: number) => (x === CX && z === CZ ? null : -1);
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(floorAt, () => false));
    expect(solve.source).toBe('ring');
    // Radius-major iteration: the first (nearest) ring hits at r=0.1*span=1m,
    // direction 0 (+x).
    expect(solve.ringRadius).toBe(0.1);
    expect(solve.ringDir).toBe(0);
    expect(solve.x).toBeCloseTo(CX + 1, 10);
    expect(solve.z).toBeCloseTo(CZ, 10);
    expect(solve.eyeY).toBe(-1 + EYE_HEIGHT);
    expect(solve.centerFloorY).toBeNull();
  });

  it('treats an occluded center as a miss and walks the ring outward', () => {
    // Everything within 3.2m of the center is walled in; ring radii 1, 2 and
    // 3 fall inside, radius 5 clears.
    const blocked = (x: number, _y: number, z: number) =>
      Math.hypot(x - CX, z - CZ) < 3.2;
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(() => 0, blocked));
    expect(solve.source).toBe('ring');
    expect(solve.ringRadius).toBe(0.5);
    expect(solve.ringDir).toBe(0);
    expect(solve.eyeY).toBe(EYE_HEIGHT);
  });

  it('within a ring radius, a blocked direction defers to the next direction', () => {
    const blocked = (x: number, _y: number, z: number) => {
      const dx = x - CX;
      const dz = z - CZ;
      const dist = Math.hypot(dx, dz);
      // Wall disk around the center, plus a pillar blocking the candidate
      // due +x of it at the 5m ring.
      return dist < 3.2 || (dist > 4 && Math.abs(dz) < 0.5 && dx > 0);
    };
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(() => 0, blocked));
    expect(solve.source).toBe('ring');
    expect(solve.ringRadius).toBe(0.5);
    // Direction 0 is due +x (blocked); direction 1 is the +x/+z diagonal.
    expect(solve.ringDir).toBe(1);
    expect(solve.x).toBeCloseTo(CX + 5 * Math.cos(Math.PI / 4), 10);
    expect(solve.z).toBeCloseTo(CZ + 5 * Math.sin(Math.PI / 4), 10);
  });

  it('rejects candidates that have a floor but no clearance, falling back to the sky pose', () => {
    // Floors everywhere, walls everywhere: every candidate fails clearance.
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(() => 0, () => true));
    expect(solve.source).toBe('sky');
    expect(solve.x).toBe(CX);
    expect(solve.z).toBe(CZ);
    expect(solve.eyeY).toBe(MAX_Y + EYE_HEIGHT);
    expect(solve.centerFloorY).toBe(0);
  });

  it('falls back to the sky pose above the model when every candidate misses', () => {
    const solve = solveSpawn(
        CX, CZ, MIN_Y, MAX_Y,
        stubProbe(() => null, () => false));
    expect(solve.source).toBe('sky');
    expect(solve.eyeY).toBe(MAX_Y + EYE_HEIGHT);
    expect(solve.centerFloorY).toBeNull();
  });
});
