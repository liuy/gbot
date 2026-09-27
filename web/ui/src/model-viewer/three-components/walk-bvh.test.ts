import { describe, expect, it } from 'vitest'
import { BoxGeometry, DoubleSide, Group, Mesh, MeshBasicMaterial, PlaneGeometry } from 'three'

import {
  buildBVH,
  disposeBVH,
  horizontalBlocked,
  installBVHPatches,
  sampleFloorY,
  sampleGroundY,
} from './walk-bvh'

// A 2-step stair: step tops at y=0 and y=0.2, separated in X so each ray
// lands on exactly one of them; x=5 is the void. updateMatrixWorld mirrors
// production, where the render loop keeps matrices current before any walk
// raycast (three's Raycaster never updates them itself).
function makeStair(): Group {
  const group = new Group()
  const lower = new Mesh(new BoxGeometry(1, 1, 1))
  lower.position.set(0, -0.5, 0)
  const upper = new Mesh(new BoxGeometry(1, 1, 1))
  upper.position.set(2, -0.3, 0)
  group.add(lower, upper)
  group.updateMatrixWorld(true)
  return group
}

// A closed room: floor top at y=0, ceiling slab from 3.0 to 3.5, one wall
// whose +x face stands 0.4m from the origin at eye height.
function makeRoom(): Group {
  const group = new Group()
  const floor = new Mesh(new BoxGeometry(10, 0.5, 10))
  floor.position.set(0, -0.25, 0)
  const ceiling = new Mesh(new BoxGeometry(10, 0.5, 10))
  ceiling.position.set(0, 3.25, 0)
  const wall = new Mesh(new BoxGeometry(0.4, 3, 2))
  wall.position.set(0.6, 1.5, 0)
  group.add(floor, ceiling, wall)
  group.updateMatrixWorld(true)
  return group
}

describe('walk-bvh', () => {
  it('samples each stair top, the void, and stays downward-only', () => {
    installBVHPatches()
    const stair = makeStair()
    buildBVH(stair)

    expect(sampleGroundY(stair, 0, 0, 5)).toBeCloseTo(0, 10)
    expect(sampleGroundY(stair, 2, 0, 5)).toBeCloseTo(0.2, 10)
    expect(sampleGroundY(stair, 5, 0, 5)).toBeNull()
    // Origin below the lower stair's floor (-1): the downward ray can never
    // reach the surface above it.
    expect(sampleGroundY(stair, 0, 0, -2)).toBeNull()
  })

  it('disposeBVH then rebuild yields identical samples', () => {
    installBVHPatches()
    const stair = makeStair()
    buildBVH(stair)
    disposeBVH(stair)
    // Between dispose and rebuild the patched raycast falls back to stock
    // traversal — same answer either way.
    expect(sampleGroundY(stair, 2, 0, 5)).toBeCloseTo(0.2, 10)
    buildBVH(stair)
    expect(sampleGroundY(stair, 2, 0, 5)).toBeCloseTo(0.2, 10)
    expect(sampleGroundY(stair, 5, 0, 5)).toBeNull()
  })

  it('installBVHPatches is idempotent', () => {
    installBVHPatches()
    expect(() => installBVHPatches()).not.toThrow()
  })

  it('sampleFloorY picks the floor beneath a ceiling; first-hit sampling picks the ceiling', () => {
    installBVHPatches()
    const room = makeRoom()
    buildBVH(room)

    // The villa failure mode: the topmost hit under a roof is the ceiling —
    // the floor sampler must skip it and land on the walkable surface.
    expect(sampleFloorY(room, 0, 0, 10)).toBeCloseTo(0, 10)
    expect(sampleGroundY(room, 0, 0, 10)).toBeCloseTo(3.5, 10)
    // Over the void outside the slab: miss.
    expect(sampleFloorY(room, 20, 0, 10)).toBeNull()
    // Stock-raycast path (no bounds tree) answers identically.
    disposeBVH(room)
    expect(sampleFloorY(room, 0, 0, 10)).toBeCloseTo(0, 10)
  })

  it('horizontalBlocked reports walls within the clearance radius at eye height', () => {
    installBVHPatches()
    const room = makeRoom()
    buildBVH(room)

    // The +x wall face is 0.4m away — inside a 0.5m clearance.
    expect(horizontalBlocked(room, 0, 1.6, 0, 1, 0, 0.5)).toBe(true)
    // -x is open; the far side of the room is beyond the clearance.
    expect(horizontalBlocked(room, 0, 1.6, 0, -1, 0, 0.5)).toBe(false)
    // Body height matters: the same ray at ceiling height clears the 3m wall.
    expect(horizontalBlocked(room, 0, 3.2, 0, 1, 0, 0.5)).toBe(false)
  })

  // The villa's floor is a flat surface exported with reversed triangle
  // winding under a positive-scale node: the face the walker stands on
  // carries a world normal pointing DOWN (measured ny = -1 normalized on
  // the real model). A floor test must key on horizontal-ness, not winding
  // sign, or the walkable floor is rejected and the spawn parks at the sky.
  it('sampleFloorY accepts a flipped-winding floor surface', () => {
    installBVHPatches()
    const group = new Group()
    const geometry = new PlaneGeometry(10, 10)
    geometry.rotateX(-Math.PI / 2)
    // Reverse every triangle: the winding, hence the face normal, flips.
    const index = geometry.index!
    for (let i = 0; i < index.count; i += 3) {
      const a = index.getX(i)
      index.setX(i, index.getX(i + 2))
      index.setX(i + 2, a)
    }
    const slab = new Mesh(geometry, new MeshBasicMaterial({side: DoubleSide}))
    group.add(slab)
    group.updateMatrixWorld(true)

    expect(sampleFloorY(group, 0, 0, 10)).toBeCloseTo(0, 10)
  })

  // Chain scales far from 1 make the normalMatrix non-unit: a perfectly
  // horizontal floor under a 39x scale reports worldNormal.y = 1/39 ≈ 0.026
  // (measured on the villa), which the old raw ny > 0.5 test rejected. The
  // comparison must run on the normalized normal.
  it('sampleFloorY accepts a floor under a large chain scale', () => {
    installBVHPatches()
    const group = new Group()
    const chain = new Group()
    chain.scale.setScalar(39)
    const slab = new Mesh(new BoxGeometry(10, 0.5, 10))
    chain.add(slab)
    group.add(chain)
    group.updateMatrixWorld(true)

    // Slab top in world: 0.25 local half-height × 39 chain scale.
    expect(sampleFloorY(group, 0, 0, 10)).toBeCloseTo(0.25 * 39, 10)
  })
})
