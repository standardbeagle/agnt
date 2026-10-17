// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// walkthrough.js: play() re-arms the current step without leaving the previous
// arming behind, so a stale click listener cannot advance a later step.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

function boot() {
  rt = loadModules(['frames', 'core', 'walkthrough-proxy', 'walkthrough'], {
    html: '<!doctype html><html><head></head><body><button id="a">A</button><button id="b">B</button></body></html>',
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  expect(typeof win.__devtool_walkthrough.start).toBe('function');
  return win;
}

const script = {
  id: 'replay',
  steps: [
    { title: 'one', target: '#a', advance: { type: 'click-target' } },
    { title: 'two', target: '#b', advance: { type: 'click-target' } },
    { title: 'three', advance: { type: 'auto', ms: 60000 } },
  ],
};

describe('walkthrough play()', () => {
  it('does not leave a second click listener on the current target', () => {
    const win = boot();
    const wt = win.__devtool_walkthrough;
    wt.start(script, { mode: 'manual' });
    expect(wt.status().stepIndex).toBe(0);

    // A click-target step is armed even while paused; play() arms it again.
    wt.play();
    wt.play();

    win.document.getElementById('a').click();
    expect(wt.status().stepIndex).toBe(1);

    // Step 1 waits for #b. A click on the earlier target must not advance it.
    win.document.getElementById('a').click();
    expect(wt.status().stepIndex).toBe(1);

    win.document.getElementById('b').click();
    expect(wt.status().stepIndex).toBe(2);
    wt.stop();
  });
});
