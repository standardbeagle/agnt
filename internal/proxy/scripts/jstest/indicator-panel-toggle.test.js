// indicator.js togglePanel: the close transition's deferred work must not
// outlive a reopen or a destroy.
//
// Closing schedules display:none after the 0.2s fade. startDesignMode /
// startResponsiveMode (module missing) close and reopen synchronously, and
// destroy() can land inside the fade.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// Each case evaluates the whole indicator bundle in jsdom (~1s alone); under
// the full parallel tier that can pass vitest's 5s default, so the ceiling is
// a generous liveness bound, not a latency budget.
describe('panel close/reopen', { timeout: 30_000 }, () => {
  it('a reopen inside the close transition leaves the panel shown', async () => {
    rt = await loadIndicator();
    const { ind } = rt;
    ind.togglePanel(true);
    ind.togglePanel(false);
    ind.togglePanel(true);
    await wait(300);
    expect(ind.state.isExpanded).toBe(true);
    expect(ind.state.panel.style.display).toBe('flex');
  });

  it('a close still hides the panel after the transition', async () => {
    rt = await loadIndicator();
    const { ind } = rt;
    ind.togglePanel(true);
    ind.togglePanel(false);
    await wait(300);
    expect(ind.state.panel.style.display).toBe('none');
  });

  it('destroy inside the open/close transition throws nothing from deferred callbacks', async () => {
    rt = await loadIndicator();
    const { win, ind } = rt;
    const errors = [];
    win.addEventListener('error', (e) => errors.push(e.error || e.message));
    ind.togglePanel(true);
    ind.togglePanel(false);
    ind.destroy();
    await wait(300);
    expect(errors).toEqual([]);
  });
});
