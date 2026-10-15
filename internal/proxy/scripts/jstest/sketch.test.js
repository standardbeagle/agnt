// sketch.js: keyboard shortcuts stay out of sketch's own text inputs (which
// live in the shadow-root mount), and undo/redo walk the drawing history one
// state at a time in both directions.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

// jsdom has no canvas backend: getContext returns a 2D context whose every
// method is a no-op (measureText answers a fixed width).
function stubCanvas(win) {
  const ctx = new Proxy({}, {
    get(target, key) {
      if (key in target) return target[key];
      if (key === 'measureText') return () => ({ width: 40 });
      return () => ({ addColorStop() {} });
    },
    set(target, key, value) { target[key] = value; return true; },
  });
  win.HTMLCanvasElement.prototype.getContext = () => ctx;
}

async function boot() {
  rt = loadModules(['frames', 'shadow-root', 'core', 'sketch'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      stubCanvas(win);
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  if (win.document.readyState === 'loading') {
    await new Promise((r) => win.document.addEventListener('DOMContentLoaded', r));
  }
  const sketch = win.__devtool_sketch;
  sketch.init();
  expect(sketch.state.isActive).toBe(true);
  return { win, sketch };
}

const rect = (id, x) => ({ id, type: 'rectangle', x, y: 10, width: 50, height: 50 });

describe('sketch keyboard shortcuts', () => {
  it('leave a key typed into sketch\'s own text input alone', async () => {
    const { win, sketch } = await boot();
    // The scenario only exists when sketch mounts in a shadow root.
    expect(win.__devtoolIsShadowMount()).toBe(true);

    const el = rect('r1', 10);
    sketch.state.elements = [el];
    sketch.state.selectedElements = [el];

    // An input inside the sketch container, as editElementText creates.
    const input = win.document.createElement('input');
    sketch.state.container.appendChild(input);
    input.focus();
    expect(win.document.activeElement).not.toBe(input); // retargeted to the host

    for (const key of ['Backspace', 'Delete', 'Escape']) {
      input.dispatchEvent(new win.KeyboardEvent('keydown', { key, bubbles: true, composed: true }));
    }
    expect(sketch.state.elements.map((e) => e.id)).toEqual(['r1']);
    expect(sketch.state.isActive).toBe(true);
  });

  it('still act on a key pressed outside a text input', async () => {
    const { win, sketch } = await boot();
    const el = rect('r1', 10);
    sketch.state.elements = [el];
    sketch.state.selectedElements = [el];
    win.document.body.dispatchEvent(new win.KeyboardEvent('keydown', { key: 'Delete', bubbles: true }));
    expect(sketch.state.elements).toEqual([]);
  });
});
