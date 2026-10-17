// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// indicator.js compose tab: re-rendering it (every panel open, every switch to
// the tab) must not accumulate nodes outside the tab content.
//
// The audit menu popover and the textarea measurement element are mounted in
// the mount root, not the tab content, so clearing the content leaves them.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const frame = (win) => new Promise((r) => win.requestAnimationFrame(() => r()));

// Each case evaluates the whole indicator bundle in jsdom (~1s alone); under
// the full parallel tier that can pass vitest's 5s default, so the ceiling is
// a generous liveness bound, not a latency budget.
describe('compose re-render', { timeout: 30_000 }, () => {
  it('keeps exactly one audit menu popover, the one the button targets', async () => {
    rt = await loadIndicator({
      // Force the native-popover path (jsdom has no Popover API): the menu is
      // then mounted in the mount root rather than inside the tab content.
      before: (win) => { win.HTMLElement.prototype.popover = null; },
    });
    const { ind, root } = rt;
    for (let i = 0; i < 3; i++) {
      ind.togglePanel(true);
      ind.togglePanel(false);
    }
    ind.togglePanel(true);
    const menus = root.querySelectorAll('#__devtool-audit-menu');
    expect(menus.length).toBe(1);
    const btns = root.querySelectorAll('#__devtool-audit-btn');
    expect(btns.length).toBe(1);
    expect(btns[0].getAttribute('popovertarget')).toBe('__devtool-audit-menu');
    expect(menus[0].querySelector('button'), 'menu carries its items').toBeTruthy();
  });

  it('keeps one textarea measurement element across renders', async () => {
    rt = await loadIndicator();
    const { win, ind, root } = rt;
    const type = async () => {
      const ta = root.querySelector('#__devtool-message');
      ta.value = 'line\nline';
      ta.dispatchEvent(new win.Event('input'));
      await frame(win);
    };
    ind.togglePanel(true);
    await type();
    ind.switchTab('compose');
    await type();
    ind.switchTab('compose');
    await type();
    const measures = [...root.querySelectorAll('div')]
      .filter((d) => d.style.left === '-9999px' && d.style.visibility === 'hidden');
    expect(measures).toHaveLength(1);
  });
});
