// gbot fork addition (not upstream).
// DOM-only keyboard axis (no three imports) so jsdom can pin the contracts.

const FORWARD = ['KeyW', 'ArrowUp'];
const BACK = ['KeyS', 'ArrowDown'];
const LEFT = ['KeyA', 'ArrowLeft'];
const RIGHT = ['KeyD', 'ArrowRight'];

// The chat composer can sit behind the artifact sheet — typing into it must
// not walk the camera.
const isEditable = (target: EventTarget | null): boolean => {
  const element = target as HTMLElement | null;
  return element != null &&
      (element.tagName === 'INPUT' || element.tagName === 'TEXTAREA' ||
       element.isContentEditable === true);
};

export class KeyboardState {
  private target: Window | null = null;
  private down = new Set<string>();

  attach(target: Window): void {
    this.target = target;
    target.addEventListener('keydown', this.onKey);
    target.addEventListener('keyup', this.onKey);
  }

  detach(): void {
    if (this.target == null) return;
    this.target.removeEventListener('keydown', this.onKey);
    this.target.removeEventListener('keyup', this.onKey);
    this.target = null;
    this.down.clear();
  }

  // +z is forward (walk-math's forward axis); opposite keys cancel.
  axis(): {x: number, z: number} {
    const held = (codes: string[]) => codes.some((code) => this.down.has(code));
    return {
      x: (held(RIGHT) ? 1 : 0) - (held(LEFT) ? 1 : 0),
      z: (held(FORWARD) ? 1 : 0) - (held(BACK) ? 1 : 0),
    };
  }

  private onKey = (event: KeyboardEvent) => {
    if (isEditable(event.target)) return;
    if (event.type === 'keydown') {
      this.down.add(event.code);
    } else {
      this.down.delete(event.code);
    }
  };
}
