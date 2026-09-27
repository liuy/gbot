// gbot fork addition (not upstream).
// three-mesh-bvh integration. All CPU-side, so these functions run (and are
// tested) under jsdom.
import * as THREE from 'three';
import {acceleratedRaycast, computeBoundsTree, disposeBoundsTree} from 'three-mesh-bvh';

const GROUND_RAY_FAR = 200;

// Slopes up to 60° from horizontal still count as floor; vertical faces never
// do.
const FLOOR_NORMAL_MIN_Y = 0.5;

const raycaster = new THREE.Raycaster();
const inverseMatrix = new THREE.Matrix4();
const down = new THREE.Vector3(0, -1, 0);
const origin = new THREE.Vector3();
const direction = new THREE.Vector3();
const worldNormal = new THREE.Vector3();
const normalMatrix = new THREE.Matrix3();

interface BVHGeometry extends THREE.BufferGeometry {
  boundsTree?: unknown;
  computeBoundsTree(): void;
  disposeBoundsTree(): void;
}

// Idempotent prototype patches. They live on the fork bundle's single three
// instance; meshes without a boundsTree fall back to stock raycast, so
// orbit-side hit testing (annotations, recenter) is unchanged.
let patchesInstalled = false;

export function installBVHPatches(): void {
  const geometryProto = THREE.BufferGeometry.prototype as unknown as
      Partial<BVHGeometry>;
  if (geometryProto.computeBoundsTree != null) {
    return;
  }
  geometryProto.computeBoundsTree = computeBoundsTree;
  geometryProto.disposeBoundsTree = disposeBoundsTree;
  (THREE.Mesh.prototype as unknown as {raycast: unknown}).raycast =
      acceleratedRaycast;
  patchesInstalled = true;
}

// Failure-badge input: the patch state is otherwise invisible from outside.
export function bvhPatched(): boolean {
  return patchesInstalled;
}

export function buildBVH(root: THREE.Object3D): void {
  root.traverse((object) => {
    const geometry = (object as THREE.Mesh).geometry as BVHGeometry | undefined;
    if (geometry == null || geometry.attributes.position == null) {
      return;
    }
    // A retained tree means a previous walk session indexed this model —
    // rebuilding it would re-pay the multi-second freeze on every toggle.
    if (geometry.boundsTree != null) {
      return;
    }
    geometry.computeBoundsTree();
  });
}

export function disposeBVH(root: THREE.Object3D): void {
  root.traverse((object) => {
    const geometry = (object as THREE.Mesh).geometry as BVHGeometry | undefined;
    if (geometry != null && geometry.boundsTree != null) {
      geometry.disposeBoundsTree();
    }
  });
}

// Downward ray from (x, fromY, z); first hit's y, or null over a void. An
// origin BELOW the only surface returns null — the ray never looks up.
export function sampleGroundY(
    root: THREE.Object3D, x: number, z: number, fromY: number): number | null {
  raycaster.firstHitOnly = true;
  raycaster.far = GROUND_RAY_FAR;
  raycaster.set(origin.set(x, fromY, z), down);
  const hits = raycaster.intersectObject(root, true);
  return hits.length > 0 ? hits[0].point.y : null;
}

// The LOWEST surface below fromY whose world-space face is horizontal — the
// walkable floor. firstHitOnly must stay off, and nearest-first is the wrong
// order for interiors: under any roof the topmost hits are the ceiling, whose
// solid-slab top face is horizontal just like the floor's, so the ray
// collects every hit and scans from the bottom up.
export function sampleFloorY(
    root: THREE.Object3D, x: number, z: number, fromY: number): number | null {
  raycaster.firstHitOnly = false;
  raycaster.far = GROUND_RAY_FAR;
  raycaster.set(origin.set(x, fromY, z), down);
  const hits = raycaster.intersectObject(root, true);
  for (let i = hits.length - 1; i >= 0; i--) {
    const hit = hits[i]!;
    if (hit.face == null) {
      continue;
    }
    normalMatrix.getNormalMatrix(hit.object.matrixWorld);
    worldNormal.copy(hit.face.normal).applyMatrix3(normalMatrix);
    // glTF chains far from unit scale (the villa mixes 0.0008x and 39x
    // nodes) make the normalMatrix non-unit, and reversed triangle winding
    // (its floor slab: geometric top face with ny = -1) flips the sign — so
    // the raw normal's magnitude AND sign are both untrustworthy. What
    // "floor" means is horizontal-ness: normalize, drop the sign.
    if (Math.abs(worldNormal.normalize().y) > FLOOR_NORMAL_MIN_Y) {
      return hit.point.y;
    }
  }
  return null;
}

// Failure-badge probe: one ray, three numbers — raw hit count, how many pass
// the horizontal filter, and what sampleFloorY returns. Splits "the ray
// misses" from "the filter rejects" in a single screenshot.
export function debugFloorProbe(
    root: THREE.Object3D, x: number, z: number,
    fromY: number): {raw: number; horiz: number; y: number | null;
      firstOnly: number | null} {
  raycaster.firstHitOnly = false;
  raycaster.far = GROUND_RAY_FAR;
  raycaster.set(origin.set(x, fromY, z), down);
  const hits = raycaster.intersectObject(root, true);
  let horiz = 0;
  let y: number | null = null;
  for (let i = hits.length - 1; i >= 0; i--) {
    const hit = hits[i]!;
    if (hit.face == null) {
      continue;
    }
    normalMatrix.getNormalMatrix(hit.object.matrixWorld);
    worldNormal.copy(hit.face.normal).applyMatrix3(normalMatrix);
    if (Math.abs(worldNormal.normalize().y) > FLOOR_NORMAL_MIN_Y) {
      horiz++;
      if (y == null) {
        y = hit.point.y;
      }
    }
  }
  raycaster.firstHitOnly = true;
  const first = raycaster.intersectObject(root, true);
  return {
    raw: hits.length,
    horiz,
    y,
    firstOnly: first.length > 0 ? first[0]!.point.y : null,
  };
}

// Failure-badge probe: finds the mesh with the largest world XZ footprint
// (the floor/terrain candidate), raycasts it ALONE, and reports the ray
// origin transformed into ITS local frame — the exact numbers the BVH box
// tests consume. A world box that fails to contain the ray xz, or a local
// origin wildly far from the mesh, localizes a space/transform fault.
export function debugLargestMeshProbe(
    root: THREE.Object3D, x: number, z: number, fromY: number):
    {name: string; box: string; raw: number; localOrigin: string} {
  let best: THREE.Mesh | null = null;
  let bestArea = -1;
  let bestBox: string = 'none';
  const localBox = new THREE.Box3();
  const worldBox = new THREE.Box3();
  const corner = new THREE.Vector3();
  const makeWorldBox = (mesh: THREE.Mesh): string => {
    const geometry = mesh.geometry as THREE.BufferGeometry;
    if (geometry.boundingBox == null) {
      geometry.computeBoundingBox();
    }
    localBox.copy(geometry.boundingBox!);
    worldBox.makeEmpty();
    for (let i = 0; i < 8; i++) {
      corner.set(
          i & 1 ? localBox.max.x : localBox.min.x,
          i & 2 ? localBox.max.y : localBox.min.y,
          i & 4 ? localBox.max.z : localBox.min.z);
      corner.applyMatrix4(mesh.matrixWorld);
      worldBox.expandByPoint(corner);
    }
    return `(${worldBox.min.x},${worldBox.min.y},${worldBox.min.z})…` +
        `(${worldBox.max.x},${worldBox.max.y},${worldBox.max.z})`;
  };
  root.traverse((object) => {
    const mesh = object as THREE.Mesh;
    if (mesh.geometry == null || mesh.geometry.attributes.position == null) {
      return;
    }
    const box = makeWorldBox(mesh);
    const area = (worldBox.max.x - worldBox.min.x) *
        (worldBox.max.z - worldBox.min.z);
    if (area > bestArea) {
      bestArea = area;
      best = mesh;
      bestBox = box;
    }
  });
  if (best == null) {
    return {name: 'none', box: 'none', raw: 0, localOrigin: 'none'};
  }
  raycaster.firstHitOnly = false;
  raycaster.far = GROUND_RAY_FAR;
  raycaster.set(origin.set(x, fromY, z), down);
  const raw = raycaster.intersectObject(best, true).length;
  inverseMatrix.copy(best.matrixWorld).invert();
  const localOrigin = origin.clone().applyMatrix4(inverseMatrix);
  return {
    name: best.name === '' ? '<unnamed>' : best.name.slice(0, 28),
    box: bestBox,
    raw,
    localOrigin: `(${localOrigin.x},${localOrigin.y},${localOrigin.z})`,
  };
}

// True when any geometry stands within far of (x, y, z) toward the unit-ish
// horizontal (dirX, dirZ) — spawn-clearance probing at eye height.
export function horizontalBlocked(
    root: THREE.Object3D, x: number, y: number, z: number, dirX: number,
    dirZ: number, far: number): boolean {
  raycaster.firstHitOnly = true;
  raycaster.far = far;
  raycaster.set(origin.set(x, y, z), direction.set(dirX, 0, dirZ).normalize());
  return raycaster.intersectObject(root, true).length > 0;
}
