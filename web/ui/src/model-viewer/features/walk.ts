// gbot fork addition (not upstream).
import {property} from 'lit/decorators.js';

import ModelViewerElementBase, {$onModelLoad, $scene, $tick, $userInputElement} from '../model-viewer-base.js';
import {Constructor} from '../utilities.js';
import {WalkControls} from '../three-components/WalkControls.js';
import {$walkActive, $walkController} from './walk-symbols.js';

// Aliased: the mixin factory's parameter shadows the element type name.
import type {ModelViewerElement as FullModelViewerElement} from '../model-viewer.js';

export declare interface WalkInterface {
  walk: boolean;
}

const $enterWalk = Symbol('enterWalk');
const $exitWalk = Symbol('exitWalk');

export const WalkMixin = <T extends Constructor<ModelViewerElementBase>>(
    ModelViewerElement: T): Constructor<WalkInterface>&T => {
  class WalkModelViewerElement extends ModelViewerElement {
    @property({type: Boolean}) walk = false;

    [$walkActive] = false;
    [$walkController]: WalkControls | null = null;

    updated(changedProperties: Map<string | number | symbol, unknown>) {
      super.updated(changedProperties);

      if (changedProperties.has('walk')) {
        if (this.walk) {
          this[$enterWalk]();
        } else {
          this[$exitWalk]();
        }
      }
    }

    disconnectedCallback() {
      // The BVH dies with the element; window listeners must not outlive it.
      if (this[$walkController] != null) {
        this[$walkController]!.forgetModel();
        this[$exitWalk]();
      }
      super.disconnectedCallback();
    }

    [$tick](time: number, delta: number) {
      super[$tick](time, delta);
      this[$walkController]?.update(time, delta);
    }

    [$onModelLoad]() {
      super[$onModelLoad]();

      if (!this.walk) {
        return;
      }
      // Re-init on a fresh model: new spawn pose, new BVH schedule. The old
      // model's retained BVH is released here — never on a plain exit.
      if (this[$walkController] != null) {
        this[$walkController]!.forgetModel();
        this[$exitWalk]();
      }
      this[$enterWalk]();
    }

    // Entering is synchronous (no event): wui flips dataset.walk to 'active'
    // only when the fork reports walk-indexed.
    private[$enterWalk]() {
      // The spawn pose needs a loaded model; [$onModelLoad] re-runs the
      // enter for an attribute that arrived before the load finished.
      if (!this.loaded || this[$walkController] != null) {
        return;
      }
      const controller = new WalkControls({
        element: this as unknown as FullModelViewerElement,
        scene: this[$scene],
        inputElement: this[$userInputElement],
        onIndexed: () =>
            this.dispatchEvent(new CustomEvent('walk-indexed')),
        onError: (message: string) => this.dispatchEvent(
            new CustomEvent('walk-error', {detail: {message}})),
      });
      this[$walkController] = controller;
      controller.enter();
      this[$walkActive] = true;
    }

    private[$exitWalk]() {
      const controller = this[$walkController];
      if (controller == null) {
        return;
      }
      this[$walkController] = null;
      this[$walkActive] = false;
      // exit() restores the pre-walk camera; dispose() frees timers and
      // listeners but deliberately keeps the retained BVH.
      controller.exit();
      controller.dispose();
    }
  }

  return WalkModelViewerElement;
};
