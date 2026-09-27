// gbot fork addition (not upstream).
// Walk-mode camera controller, the sibling of SmoothControls: while active it
// owns ModelScene.camera and orbit interaction stays disabled. Never
// constructed under jsdom (Renderer.singleton needs WebGL) — the handshake
// below is covered by code review plus phone gates.
import {Box3, BufferGeometry, Object3D, Quaternion, Vector3} from 'three';

import {$controls} from '../features/controls.js';
import type {ModelViewerElement} from '../model-viewer.js';
import type {ModelScene} from './ModelScene.js';
import {SmoothControls} from './SmoothControls.js';
import {
  buildBVH,
  disposeBVH,
  horizontalBlocked,
  installBVHPatches,
  sampleFloorY,
  sampleGroundY,
} from './walk-bvh.js';
import {KeyboardState} from './walk-input.js';
import {
  EYE_HEIGHT,
  JOYSTICK_RADIUS,
  LOOK_SENSITIVITY,
  clampPitch,
  groundEyeY,
  integrateMove,
  joystickAxis,
} from './walk-math.js';
import {solveSpawn, type SpawnProbe, type SpawnSolve} from './walk-spawn.js';

export interface WalkControlsConfig {
  element: ModelViewerElement;
  scene: ModelScene;
  inputElement: HTMLDivElement;
  onIndexed(): void;
  onError(message: string): void;
}

// The villa's bounds tree (hundreds of MB alongside a 394 MB model) stays in
// RAM while the model stays loaded so re-entry is instant. Flip to false ONLY
// on observed retention OOM (M2/M3 villa validation).
const RETAIN_BVH = true;

const WALK_FOV_DEG = 70;

// groundEyeY converges asymptotically; without an epsilon the tail of the
// damp would queue renders forever on a stationary camera.
const EYE_EPSILON = 1e-4;

interface IndexedGeometry extends BufferGeometry {
  boundsTree?: unknown;
}

// Class names are prefixed so the injected shadow-root style cannot collide
// with upstream template classes.
const JOYSTICK_STYLE = `
.gbot-walk-joy-base, .gbot-walk-joy-nub {
  position: absolute;
  border-radius: 50%;
  pointer-events: none;
  z-index: 2;
}
.gbot-walk-joy-base {
  width: ${2 * JOYSTICK_RADIUS}px;
  height: ${2 * JOYSTICK_RADIUS}px;
  box-sizing: border-box;
  margin: -${JOYSTICK_RADIUS}px 0 0 -${JOYSTICK_RADIUS}px;
  border: 2px solid rgba(255, 255, 255, 0.45);
  background: rgba(0, 0, 0, 0.25);
}
.gbot-walk-joy-nub {
  width: ${JOYSTICK_RADIUS}px;
  height: ${JOYSTICK_RADIUS}px;
  margin: -${JOYSTICK_RADIUS / 2}px 0 0 -${JOYSTICK_RADIUS / 2}px;
  background: rgba(255, 255, 255, 0.55);
}`;

interface TrackedPointer {
  role: 'joystick' | 'look';
  // joystick: press anchor for the dead-zone/clamp math; look: last position.
  baseX: number;
  baseY: number;
  lastX: number;
  lastY: number;
}

export class WalkControls {
  private config: WalkControlsConfig;

  private disposed = false;
  private bvhFrame: number | null = null;
  // The model this controller built a BVH against; forgetModel() (called only
  // by WalkMixin) frees it. Survives exit() — that is the retention.
  private indexedModel: Object3D | null = null;
  // Ground-follow waits for the BVH: sampling before it is built would run an
  // un-accelerated raycast against every vertex, every frame.
  private groundReady = false;

  private savedPosition = new Vector3();
  private savedQuaternion = new Quaternion();
  private savedFov = 0;

  private yaw = 0;
  private pitch = 0;
  // Pixel deltas accumulated since the last update() — look is applied on the
  // frame tick so drag and pointer-lock feed one code path.
  private lookDX = 0;
  private lookDY = 0;
  private joyAxis = {x: 0, y: 0};
  private keys = new KeyboardState();
  private pointers = new Map<number, TrackedPointer>();

  private joyStyle: HTMLStyleElement | null = null;
  private joyBase: HTMLDivElement | null = null;
  private joyNub: HTMLDivElement | null = null;
  // Joystick base in .container-local px; the nub offsets from it.
  private joyBaseX = 0;
  private joyBaseY = 0;

  // Spawn geometry captured at enter(); placeSpawn() consumes it once the
  // BVH exists.
  private spawnCx = 0;
  private spawnCz = 0;
  private spawnMinY = 0;
  private spawnMaxY = 0;

  constructor(config: WalkControlsConfig) {
    this.config = config;
  }

  private get controls(): SmoothControls {
    // $controls is a protected mixin field, reachable outside the subclass
    // chain only through the symbol lookup.
    return (this.config.element as any)[$controls] as SmoothControls;
  }

  enter(): void {
    const {scene, inputElement} = this.config;

    // Settle any in-flight orbit damping first: update() never re-reads the
    // camera, so the saved pose must be one controls considers stationary —
    // otherwise orbit would fight the restore on exit.
    this.controls.jumpToGoal();
    scene.jumpToGoal();

    const camera = scene.camera;
    this.savedPosition.copy(camera.position);
    this.savedQuaternion.copy(camera.quaternion);
    this.savedFov = camera.fov;

    // Orbit listeners off; the inline touch-action overrides the pan-y
    // option while walking.
    this.controls.disableInteraction();
    inputElement.style.touchAction = 'none';

    // The spawn solve needs the BVH: an un-accelerated raycast against a
    // 17.5 M-vertex model would wedge the main thread for minutes. The camera
    // parks at the never-occluded sky pose meanwhile; placeSpawn() takes over
    // the moment buildIndex() lands. near/far stay untouched — updateNearFar's
    // range is wide enough for interiors.
    //
    // scene.boundingBox is in the MODEL'S RAW space: ModelScene centers the
    // rendered model by translating the target group to −center, so raw
    // coordinates are NOT world coordinates. Everything below snapshots raw
    // values; placeSpawn() maps the box through model.matrixWorld into world
    // space, where the raycast pipeline (and the camera) actually live.
    const bbox = scene.boundingBox;
    const center = bbox.getCenter(new Vector3());
    this.spawnCx = center.x;
    this.spawnCz = center.z;
    this.spawnMinY = bbox.min.y;
    this.spawnMaxY = bbox.max.y;
    const model = scene.model;
    const park = new Vector3(
        center.x, bbox.max.y + EYE_HEIGHT, center.z);
    if (model != null) {
      park.applyMatrix4(model.matrixWorld);
    }
    camera.position.copy(park);
    this.yaw = 0;
    this.pitch = 0;
    camera.rotation.order = 'YXZ';
    camera.rotation.set(0, 0, 0);
    camera.fov = WALK_FOV_DEG;
    camera.updateProjectionMatrix();

    this.attachInput();
    this.injectJoystickDom();
    scene.queueRender();

    // Double rAF: the first spinner frame paints before the synchronous
    // multi-second build freezes the main thread.
    this.bvhFrame = requestAnimationFrame(() => {
      this.bvhFrame = requestAnimationFrame(() => this.buildIndex());
    });
  }

  update(_time: number, delta: number): void {
    const {scene} = this.config;
    const camera = scene.camera;
    const dt = delta / 1000;
    let dirty = false;

    if (this.lookDX !== 0 || this.lookDY !== 0) {
      this.yaw -= this.lookDX * LOOK_SENSITIVITY;
      this.pitch = clampPitch(this.pitch - this.lookDY * LOOK_SENSITIVITY);
      this.lookDX = 0;
      this.lookDY = 0;
      camera.rotation.set(this.pitch, this.yaw, 0);
      dirty = true;
    }

    // Joystick y is screen-space (drag up is negative); walk-math's forward
    // axis is +z. The combined axis is clamped per-component, not
    // re-normalized — the diagonal stays up to sqrt(2), as walk-math pins it.
    const keyAxis = this.keys.axis();
    const axisX = Math.max(-1, Math.min(1, keyAxis.x + this.joyAxis.x));
    const axisZ = Math.max(-1, Math.min(1, keyAxis.z - this.joyAxis.y));
    if (dt > 0 && (axisX !== 0 || axisZ !== 0)) {
      const next = integrateMove(
          camera.position.x, camera.position.z, this.yaw, axisX, axisZ, dt);
      camera.position.x = next.x;
      camera.position.z = next.z;
      dirty = true;
    }

    // Eye height tracks the surface under the walker; before the BVH exists
    // the spawn eye height is held instead.
    if (this.groundReady) {
      const model = scene.model;
      if (model != null) {
        const hit = sampleGroundY(
            model, camera.position.x, camera.position.z,
            camera.position.y + 0.5);
        const eyeY = groundEyeY(camera.position.y, hit, dt);
        if (Math.abs(eyeY - camera.position.y) > EYE_EPSILON) {
          camera.position.y = eyeY;
          dirty = true;
        }
      }
    }

    if (dirty) {
      scene.queueRender();
    }
  }

  exit(): void {
    this.cancelIndexSchedule();
    const {element, scene, inputElement} = this.config;

    // Stationary controls would leave the walk pose in place — the restore
    // must be explicit.
    const camera = scene.camera;
    camera.position.copy(this.savedPosition);
    camera.quaternion.copy(this.savedQuaternion);
    camera.fov = this.savedFov;
    camera.updateProjectionMatrix();

    inputElement.style.touchAction = '';
    if (element.cameraControls) {
      this.controls.enableInteraction();
    }
    scene.queueRender();

    if (!RETAIN_BVH) {
      this.forgetModel();
    }
  }

  forgetModel(): void {
    this.cancelIndexSchedule();
    if (this.indexedModel != null) {
      disposeBVH(this.indexedModel);
      this.indexedModel = null;
    }
  }

  // Frees listeners and timers ONLY: a disposed controller must not free the
  // retained BVH — forgetModel() owns that, and the mixin calls it only on
  // model change / element disconnect.
  dispose(): void {
    this.cancelIndexSchedule();
    if (this.disposed) {
      return;
    }
    this.disposed = true;
    this.detachInput();
    this.removeJoystickDom();
  }

  private attachInput(): void {
    const {inputElement} = this.config;
    inputElement.addEventListener('pointerdown', this.onPointerDown);
    inputElement.addEventListener('pointermove', this.onPointerMove);
    inputElement.addEventListener('pointerup', this.onPointerUp);
    inputElement.addEventListener('pointercancel', this.onPointerUp);
    this.keys.attach(window);
  }

  private detachInput(): void {
    const {inputElement} = this.config;
    inputElement.removeEventListener('pointerdown', this.onPointerDown);
    inputElement.removeEventListener('pointermove', this.onPointerMove);
    inputElement.removeEventListener('pointerup', this.onPointerUp);
    inputElement.removeEventListener('pointercancel', this.onPointerUp);
    this.keys.detach();
    this.pointers.clear();
  }

  private onPointerDown = (event: PointerEvent): void => {
    const {inputElement} = this.config;
    const rect = inputElement.getBoundingClientRect();
    // Touch splits the screen: left half is the joystick, right half is look.
    // A mouse is always a look drag — WASD owns desktop movement, and pointer
    // lock would hide the cursor behind the browser's Esc banner.
    const role: TrackedPointer['role'] =
        event.pointerType === 'mouse' ? 'look' :
        event.clientX - rect.left >= rect.width / 2 ? 'look' : 'joystick';
    inputElement.setPointerCapture(event.pointerId);
    this.pointers.set(event.pointerId, {
      role,
      baseX: event.clientX,
      baseY: event.clientY,
      lastX: event.clientX,
      lastY: event.clientY,
    });
    if (role === 'joystick') {
      this.showJoystick(event.clientX, event.clientY);
    }
  };

  private onPointerMove = (event: PointerEvent): void => {
    const pointer = this.pointers.get(event.pointerId);
    if (pointer == null) {
      return;
    }
    if (pointer.role === 'look') {
      this.lookDX += event.clientX - pointer.lastX;
      this.lookDY += event.clientY - pointer.lastY;
      pointer.lastX = event.clientX;
      pointer.lastY = event.clientY;
      return;
    }
    const dx = event.clientX - pointer.baseX;
    const dy = event.clientY - pointer.baseY;
    this.joyAxis = joystickAxis(dx, dy);
    this.moveJoystickVisual(dx, dy);
  };

  private onPointerUp = (event: PointerEvent): void => {
    const pointer = this.pointers.get(event.pointerId);
    if (pointer == null) {
      return;
    }
    this.pointers.delete(event.pointerId);
    if (pointer.role === 'joystick') {
      this.joyAxis = {x: 0, y: 0};
      this.hideJoystick();
    }
  };

  // The joystick visuals are ordinary shadow-DOM overlays: .container is the
  // template parent of .userInput, so absolute children stack above the
  // canvas without joining the input surface (pointer-events: none).
  private injectJoystickDom(): void {
    const inputElement = this.config.inputElement;
    const container = inputElement.parentElement;
    const root = inputElement.getRootNode();
    if (container == null || !(root instanceof ShadowRoot)) {
      return;
    }
    const style = document.createElement('style');
    style.textContent = JOYSTICK_STYLE;
    root.appendChild(style);
    const base = document.createElement('div');
    base.className = 'gbot-walk-joy-base';
    const nub = document.createElement('div');
    nub.className = 'gbot-walk-joy-nub';
    base.style.display = 'none';
    nub.style.display = 'none';
    container.append(base, nub);
    this.joyStyle = style;
    this.joyBase = base;
    this.joyNub = nub;
  }

  private removeJoystickDom(): void {
    this.joyBase?.remove();
    this.joyNub?.remove();
    this.joyStyle?.remove();
    this.joyBase = null;
    this.joyNub = null;
    this.joyStyle = null;
  }

  private showJoystick(clientX: number, clientY: number): void {
    const base = this.joyBase;
    const nub = this.joyNub;
    if (base == null || nub == null) {
      return;
    }
    const rect = base.parentElement!.getBoundingClientRect();
    this.joyBaseX = clientX - rect.left;
    this.joyBaseY = clientY - rect.top;
    base.style.left = `${this.joyBaseX}px`;
    base.style.top = `${this.joyBaseY}px`;
    nub.style.left = `${this.joyBaseX}px`;
    nub.style.top = `${this.joyBaseY}px`;
    base.style.display = '';
    nub.style.display = '';
  }

  // The nub mirrors joystickAxis's unit-circle clamp in px.
  private moveJoystickVisual(dx: number, dy: number): void {
    if (this.joyNub == null) {
      return;
    }
    const magnitude = Math.hypot(dx, dy);
    const scale =
        magnitude > JOYSTICK_RADIUS ? JOYSTICK_RADIUS / magnitude : 1;
    this.joyNub.style.left = `${this.joyBaseX + dx * scale}px`;
    this.joyNub.style.top = `${this.joyBaseY + dy * scale}px`;
  }

  private hideJoystick(): void {
    if (this.joyBase == null || this.joyNub == null) {
      return;
    }
    this.joyBase.style.display = 'none';
    this.joyNub.style.display = 'none';
  }

  private cancelIndexSchedule(): void {
    if (this.bvhFrame != null) {
      cancelAnimationFrame(this.bvhFrame);
      this.bvhFrame = null;
    }
  }

  private buildIndex(): void {
    this.bvhFrame = null;
    // Exit during the wait cancels the rAF ids; exit cannot interrupt the
    // synchronous build itself, so the post-build checks suppress the
    // callbacks instead.
    if (this.disposed) {
      return;
    }
    installBVHPatches();
    const model = this.config.scene.model;
    try {
      if (model != null) {
        buildBVH(model);
        this.indexedModel = model;
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      if (!this.disposed) {
        this.config.onError(message);
      }
      return;
    }
    if (!this.disposed) {
      this.groundReady = true;
      this.placeSpawn(model);
      if (!this.disposed) {
        this.config.onIndexed();
      }
    }
  }

  // Runs once the BVH exists: maps the raw-space box into world space through
  // the model's live matrixWorld (ModelScene's target-group centering makes
  // raw ≠ world), then solves the standing spawn — refusing the mode entirely
  // when no standable floor exists.
  private placeSpawn(model: Object3D | null): void {
    if (model == null) {
      return;
    }
    const worldBox = new Box3();
    worldBox.copy(this.config.scene.boundingBox);
    worldBox.applyMatrix4(model.matrixWorld);
    const worldCenter = worldBox.getCenter(new Vector3());
    const probe: SpawnProbe = {
      floorY: (x, z, y) => sampleFloorY(model, x, z, y),
      blocked: (x, y, z, dirX, dirZ, far) =>
          horizontalBlocked(model, x, y, z, dirX, dirZ, far),
    };
    const solve = solveSpawn(
        worldCenter.x, worldCenter.z, worldBox.min.y, worldBox.max.y, probe);
    // A walkable space must contain the standing eye: smooth closed objects
    // (helmet, pipe, sphere) always present an up-facing top surface to the
    // probe — their "floor" is their own top, putting the eye above the whole
    // model. Without this check they enter into a blank view.
    if (solve.source === 'sky' || solve.eyeY > worldBox.max.y) {
      // Nothing standable anywhere — the honest answer is to refuse the mode
      // (a helmet is not a building). The artifact layer exits and shows the
      // no-floor copy; the camera never stays in the sky pose.
      this.config.onError('no-walkable-floor');
      this.config.scene.queueRender();
      return;
    }
    this.config.scene.camera.position.set(solve.x, solve.eyeY, solve.z);
    this.config.scene.queueRender();
  }

}
