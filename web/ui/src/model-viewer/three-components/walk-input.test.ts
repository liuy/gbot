import {describe, expect, it} from 'vitest';

import {KeyboardState} from './walk-input.js';

describe('KeyboardState', () => {
  it('W is forward (+z), A adds strafe (-x), keyup W drops forward', () => {
    const keys = new KeyboardState();
    keys.attach(window);
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyW'}));
    expect(keys.axis()).toEqual({x: 0, z: 1});
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyA'}));
    expect(keys.axis()).toEqual({x: -1, z: 1});
    window.dispatchEvent(new KeyboardEvent('keyup', {code: 'KeyW'}));
    expect(keys.axis()).toEqual({x: -1, z: 0});
    keys.detach();
  });

  it('opposite keys cancel on both axes', () => {
    const keys = new KeyboardState();
    keys.attach(window);
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyW'}));
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyS'}));
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyA'}));
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyD'}));
    expect(keys.axis()).toEqual({x: 0, z: 0});
    keys.detach();
  });

  it('arrows mirror WASD', () => {
    const keys = new KeyboardState();
    keys.attach(window);
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'ArrowUp'}));
    expect(keys.axis()).toEqual({x: 0, z: 1});
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'ArrowRight'}));
    expect(keys.axis()).toEqual({x: 1, z: 1});
    window.dispatchEvent(new KeyboardEvent('keyup', {code: 'ArrowUp'}));
    window.dispatchEvent(new KeyboardEvent('keyup', {code: 'ArrowRight'}));
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'ArrowDown'}));
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'ArrowLeft'}));
    expect(keys.axis()).toEqual({x: -1, z: -1});
    keys.detach();
  });

  it('keys typed into an editable target are ignored (chat composer)', () => {
    const keys = new KeyboardState();
    keys.attach(window);
    const input = document.createElement('input');
    document.body.appendChild(input);
    input.dispatchEvent(
        new KeyboardEvent('keydown', {code: 'KeyW', bubbles: true}));
    expect(keys.axis()).toEqual({x: 0, z: 0});
    input.remove();
    keys.detach();
  });

  it('detach removes the listeners: later keydowns change nothing', () => {
    const keys = new KeyboardState();
    keys.attach(window);
    keys.detach();
    window.dispatchEvent(new KeyboardEvent('keydown', {code: 'KeyW'}));
    expect(keys.axis()).toEqual({x: 0, z: 0});
  });
});
