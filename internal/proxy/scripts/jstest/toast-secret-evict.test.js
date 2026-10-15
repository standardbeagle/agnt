// toast.js maxVisible eviction: an open secret-entry prompt persists until the
// user submits or dismisses it, so a burst of ordinary toasts must evict the
// oldest ordinary toast instead — never the prompt the user may be typing in.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

function boot() {
  rt = loadModules(['frames', 'core', 'ui-tokens', 'toast'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      // Run the animate-in frame at once, so a shown toast is opacity 1 and a
      // dismissed one (fading out, removed 300ms later) is opacity 0.
      win.requestAnimationFrame = (fn) => { fn(0); return 0; };
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  const live = () => [...win.document.querySelectorAll('#__devtool-toast-container > *')]
    .filter((el) => el.style.opacity !== '0');
  const secretForms = () => live().filter((el) => el.querySelector('input[type=password]'));
  return { win, toast: win.__devtool_toast, live, secretForms };
}

describe('maxVisible eviction', () => {
  it('evicts ordinary toasts, never an open secret prompt', () => {
    const { toast, live, secretForms } = boot();
    toast.show({ input: 'secret', name: 'API_KEY' });
    for (let i = 0; i < 5; i++) toast.show({ type: 'info', message: 'event ' + i });
    expect(secretForms()).toHaveLength(1);
    expect(live()).toHaveLength(3);
    expect(live().map((el) => el.textContent).join('|')).toContain('event 4');
  });

  it('still evicts the oldest ordinary toast when no prompt is open', () => {
    const { toast, live } = boot();
    for (let i = 0; i < 4; i++) toast.show({ type: 'info', message: 'event ' + i });
    const text = live().map((el) => el.textContent).join('|');
    expect(live()).toHaveLength(3);
    expect(text).not.toContain('event 0');
    expect(text).toContain('event 3');
  });

  it('keeps every prompt when prompts alone fill the stack', () => {
    const { toast, secretForms } = boot();
    for (const name of ['A', 'B', 'C', 'D']) toast.show({ input: 'secret', name });
    expect(secretForms()).toHaveLength(4);
  });
});
