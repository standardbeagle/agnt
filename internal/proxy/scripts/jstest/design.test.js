// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js).
// design.js: selecting an element swaps on-scheme options into the page in
// place at once (no agent round trip), the agent message leaves in the same
// tick, and agent alternatives with off-scheme colours or fonts are refused.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const PAGE = `<!doctype html><html><head><style>
  :root { --brand: #2563eb; }
  body { color: rgb(17, 24, 39); background-color: rgb(255, 255, 255); font-family: Inter, sans-serif; }
  .card { padding: 16px; border-radius: 8px; background-color: rgb(255, 255, 255); }
  .btn { background-color: rgb(37, 99, 235); color: rgb(255, 255, 255); padding: 8px; border-radius: 6px;
         box-shadow: rgba(0, 0, 0, 0.1) 0px 1px 3px 0px; }
  .tight { padding: 4px; }
  .roomy { padding: 24px; }
</style></head><body>
  <div class="list">
    <div class="card" id="target"><h3>Plan</h3><a class="btn" href="#buy">Buy</a></div>
    <div class="card"><h3>Other</h3></div>
  </div>
  <p class="tight">a</p><p class="roomy">b</p>
</body></html>`;

async function boot() {
  rt = loadModules(['frames', 'core', 'ui-tokens', 'shadow-root', 'utils', 'toast', 'override-store', 'design'], {
    html: PAGE,
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  if (win.document.readyState === 'loading') {
    await new Promise((r) => win.document.addEventListener('DOMContentLoaded', r));
  }
  for (const s of rt.sockets) s.open();
  return win;
}

function designFrames(type) {
  return rt.sockets.flatMap((s) => s.sent).filter((m) => m && m.type === type);
}

function swapped(win) {
  return win.document.querySelector('[data-devtool-design-swap]');
}

describe('design mode', () => {
  it('swaps instant on-scheme options into the page and sends the agent message in the same tick', async () => {
    const win = await boot();
    const design = win.__devtool_design;
    const target = win.document.getElementById('target');

    design.start();
    design.selectElement(target);

    // Agent message left synchronously: nothing (rasterising, screenshots) waits in front of it.
    const states = designFrames('design_state');
    expect(states).toHaveLength(1);
    const data = states[0].data;
    expect(data.instantVariants.length).toBeGreaterThanOrEqual(2);
    expect(data.pageThumb).toBeUndefined();

    // First instant option is in the page, in the element's place, with its classes.
    const st = design.getState();
    expect(st.currentIndex).toBe(1);
    expect(st.alternativesCount).toBe(1 + data.instantVariants.length);
    const clone = swapped(win);
    expect(clone).not.toBeNull();
    expect(clone.classList.contains('card')).toBe(true);
    expect(clone.id).toBe('');
    expect(clone.previousElementSibling).toBe(target);
    expect(target.style.getPropertyValue('display')).toBe('none');
    expect(target.style.getPropertyPriority('display')).toBe('important');
    expect(clone.innerHTML).toBe(target.innerHTML);

    // Every instant option only uses values the page already has.
    for (const note of data.instantVariants) {
      expect(note).toMatch(/^(Compact|Spacious|Emphasis|Elevated) — /);
    }
    expect(data.instantVariants.some((n) => n.startsWith('Compact — 8px'))).toBe(true);
    expect(data.instantVariants.some((n) => n.startsWith('Spacious — 24px'))).toBe(true);

    // Stepping back to the original removes the swap entirely.
    design.applyAlternative(0);
    expect(swapped(win)).toBeNull();
    expect(target.style.getPropertyValue('display')).toBe('');
  });

  it('refuses agent alternatives with off-scheme colours or fonts and swaps in on-scheme ones', async () => {
    const win = await boot();
    const design = win.__devtool_design;
    const target = win.document.getElementById('target');
    design.start();
    design.selectElement(target);
    const before = design.getState().alternativesCount;

    const bad = design.addAlternative('<h3 style="color:#ff00ff">Plan</h3>', { label: 'variation', note: 'loud' });
    expect(bad.error).toBe('off-scheme');
    expect(bad.clashes).toEqual(['colour #ff00ff']);
    const badFont = design.addAlternative('<h3 style="font-family: \'Comic Sans MS\'">Plan</h3>');
    expect(badFont.clashes).toEqual(['font comic sans ms']);
    const badWrapper = design.addAlternative('<h3>Plan</h3>', { style: 'background: hsl(120, 100%, 50%)' });
    expect(badWrapper.clashes).toEqual(['colour hsl(120, 100%, 50%)']);
    expect(design.getState().alternativesCount).toBe(before);

    // Near-palette (rounding), var() tokens, generic fonts and anchor hrefs all pass.
    const res = design.addAlternatives([
      { html: '<h3 style="color:#2563ea;font-family:var(--font), sans-serif">Plan</h3><a href="#add">Buy</a>', note: 'stack' },
      { html: '<h3 style="color:var(--brand)">Plan</h3>', style: 'background-color:#fff', note: 'airy' },
      { html: '<h3 style="color:rgb(250,0,0)">Plan</h3>', note: 'red' },
    ]);
    expect(res.added).toBe(2);
    expect(res.rejected).toEqual([{ index: 2, clashes: ['colour rgb(250,0,0)'] }]);
    expect(res.hint).toMatch(/page palette/);
    expect(design.getState().alternativesCount).toBe(before + 2);
    const clone = swapped(win);
    expect(clone.innerHTML).toBe('<h3 style="color:var(--brand)">Plan</h3>');
    expect(clone.getAttribute('style')).toContain('background-color:#fff');
  });

  it('puts the swap back when the framework re-renders the slot, and stop() restores the page', async () => {
    const win = await boot();
    const design = win.__devtool_design;
    const target = win.document.getElementById('target');
    target.style.setProperty('display', 'flex');
    design.start();
    design.selectElement(target);
    expect(swapped(win)).not.toBeNull();

    // A re-render that rewrites the target's inline style is undone.
    target.style.display = 'block';
    await new Promise((r) => setTimeout(r, 0));
    expect(target.style.getPropertyValue('display')).toBe('none');

    // A re-render that drops our foreign node.
    swapped(win).remove();
    await new Promise((r) => setTimeout(r, 0));
    const back = swapped(win);
    expect(back).not.toBeNull();
    expect(back.previousElementSibling).toBe(target);
    expect(win.document.querySelectorAll('[data-devtool-design-swap]')).toHaveLength(1);

    design.stop();
    expect(swapped(win)).toBeNull();
    expect(target.style.getPropertyValue('display')).toBe('flex');
    expect(target.style.getPropertyPriority('display')).toBe('');
  });
});
