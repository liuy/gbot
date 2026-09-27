// gbot fork addition (not upstream).
// Pure spawn placement: pick a walkable, unoccluded standing spot from
// geometric queries alone. No three imports — the probe is injected, so the
// selection logic is pinned in jsdom against stubs and the real walk-bvh
// samplers plug in at runtime.

import {EYE_HEIGHT} from './walk-math.js';

export interface SpawnProbe {
  // Horizontal surface below fromY at (x, z), either facing; null over a void.
  floorY(x: number, z: number, fromY: number): number | null;
  // True when geometry stands within far of (x, y, z) toward (dirX, dirZ).
  blocked(x: number, y: number, z: number, dirX: number, dirZ: number,
    far: number): boolean;
}

export interface SpawnSolve {
  x: number;
  z: number;
  eyeY: number;
  // Which rule placed the spawn — the failure badge reports it.
  source: 'center' | 'ring' | 'sky';
  // The center-column floor probe result, independent of the source (an
  // occluded center HIT still routes to the ring).
  centerFloorY: number | null;
  // Set only when source === 'ring': radius as a fraction of the bbox span
  // and the winning direction index (0 = +x, counterclockwise).
  ringRadius: number | null;
  ringDir: number | null;
}

// Fractions of the bbox span: near-first, so the closest usable candidate to
// the model center wins.
const RING_RADII = [0.1, 0.2, 0.3, 0.5];
const RING_DIRECTIONS = 8;
// A candidate must have this much open space around the body at eye height —
// half a meter of clearance rejects spawns embedded in walls/columns.
const CLEARANCE_M = 0.5;

export function solveSpawn(
    cx: number, cz: number, minY: number, maxY: number,
    probe: SpawnProbe): SpawnSolve {
  const span = Math.max(maxY - minY, 1e-6);
  // The ground probe starts above the model, never at the eye — an origin
  // inside a slab cannot see the floor below it.
  const fromY = maxY + span * 0.01;

  // candidate: floor hit + full-circle clearance at eye height. A caller
  // that already probed the floor passes knownFloorY to skip the re-raycast.
  const usable = (x: number, z: number,
      knownFloorY?: number | null): number | null => {
    const floorY = knownFloorY === undefined ? probe.floorY(x, z, fromY) :
        knownFloorY;
    if (floorY == null) {
      return null;
    }
    const eyeY = floorY + EYE_HEIGHT;
    for (let d = 0; d < RING_DIRECTIONS; d++) {
      const angle = (d / RING_DIRECTIONS) * 2 * Math.PI;
      if (probe.blocked(x, eyeY, z, Math.cos(angle), Math.sin(angle),
          CLEARANCE_M)) {
        return null;
      }
    }
    return floorY;
  };

  const centerFloorY = probe.floorY(cx, cz, fromY);
  if (centerFloorY != null && usable(cx, cz, centerFloorY) != null) {
    return {
      x: cx, z: cz, eyeY: centerFloorY + EYE_HEIGHT,
      source: 'center', centerFloorY, ringRadius: null, ringDir: null,
    };
  }

  for (const radiusFraction of RING_RADII) {
    const radius = radiusFraction * span;
    for (let d = 0; d < RING_DIRECTIONS; d++) {
      const angle = (d / RING_DIRECTIONS) * 2 * Math.PI;
      const x = cx + Math.cos(angle) * radius;
      const z = cz + Math.sin(angle) * radius;
      const floorY = usable(x, z);
      if (floorY == null) {
        continue;
      }
      return {
        x, z, eyeY: floorY + EYE_HEIGHT,
        source: 'ring', centerFloorY,
        ringRadius: radiusFraction, ringDir: d,
      };
    }
  }

  // Pathological (no floor anywhere reachable): above the model looking at
  // the sky is ugly but can never be occluded black.
  return {
    x: cx, z: cz, eyeY: maxY + EYE_HEIGHT,
    source: 'sky', centerFloorY, ringRadius: null, ringDir: null,
  };
}
