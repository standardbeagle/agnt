// variant-engine.js: reconcile behaviour on a page that keeps mutating.
//
// The engine re-syncs on every foreign DOM mutation. These tests drive that
// loop on a real (jsdom) document and count what it does per pass.

import { JSDOM } from 'jsdom';
import { afterEach, describe, expect, it } from 'vitest';
import { shipped } from './load-bundle.js';

let dom = null;
afterEach(() => { if (dom) { dom.window.close(); dom = null; } });

function page(html) {
  dom = new JSDOM(`<!doctype html><html><body>${html}</body></html>`, {
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    url: 'http://localhost:8899/s/token',
  });
  const win = dom.window;
  const warnings = [];
  win.console.warn = (...args) => warnings.push(args.join(' '));
  win.eval(shipped('variant-engine.js'));
  return { win, warnings };
}

// One reconcile pass: let the MutationObserver deliver, then the frame run.
// Tests settle once after apply() before mutating: the engine ignores the
// mutation batch delivered in the same task as its own writes.
const settle = (win) => new Promise((resolve) => {
  win.setTimeout(() => win.requestAnimationFrame(() => win.setTimeout(resolve, 0)), 0);
});

const set = (ops) => ({ version: 'v1', id: 'vs', variants: [{ id: 'a', label: 'A', ops }] });
const MISSING = (w) => w.filter((m) => m.includes('variant targets not found'));

describe('missing-target warning', () => {
  it('is logged once for an unchanged missing set, however often the page mutates', async () => {
    const { win, warnings } = page('<div id="ticker">0</div>');
    const engine = win.__variantEngine.create(
      set([{ op: 'setText', selector: '#nope', value: 'x' }]), null, {});
    engine.apply('a');
    expect(MISSING(warnings)).toHaveLength(1);

    const before = engine.stats().applyCount;
    for (let i = 1; i <= 8; i++) {
      win.document.getElementById('ticker').textContent = String(i);
      await settle(win);
    }
    // The reconcile really ran on those mutations...
    expect(engine.stats().applyCount).toBeGreaterThan(before + 3);
    // ...and stayed quiet about a miss it had already reported.
    expect(MISSING(warnings)).toHaveLength(1);
    expect(engine.missingTargets()).toEqual(['#nope']);
    engine.destroy();
  });

  it('is logged again when the missing set changes', async () => {
    const { win, warnings } = page('<div id="host"></div>');
    const engine = win.__variantEngine.create(set([
      { op: 'setText', selector: '#one', value: 'x' },
      { op: 'setText', selector: '#two', value: 'y' },
    ]), null, {});
    engine.apply('a');
    expect(MISSING(warnings)).toHaveLength(1);
    expect(MISSING(warnings)[0]).toContain('#one, #two');
    await settle(win);

    const one = win.document.createElement('p');
    one.id = 'one';
    win.document.getElementById('host').appendChild(one);
    await settle(win);

    expect(MISSING(warnings)).toHaveLength(2);
    expect(MISSING(warnings)[1]).toContain('#two');
    expect(MISSING(warnings)[1]).not.toContain('#one');
    engine.destroy();
  });

  it('is logged again after a revert and re-apply', () => {
    const { win, warnings } = page('');
    const engine = win.__variantEngine.create(
      set([{ op: 'setText', selector: '#nope', value: 'x' }]), null, {});
    engine.apply('a');
    engine.revert();
    engine.apply('a');
    expect(MISSING(warnings)).toHaveLength(2);
    engine.destroy();
  });

  it('restores the visible marker if the page removes it', async () => {
    const { win } = page('<div id="app"></div>');
    const engine = win.__variantEngine.create(
      set([{ op: 'setText', selector: '#nope', value: 'x' }]), null, {});
    engine.apply('a');
    const marker = () => win.document.getElementById('__variant-engine-missing');
    expect(marker()).toBeTruthy();
    await settle(win);

    marker().remove();
    await settle(win);
    expect(marker(), 'marker must come back on the next reconcile').toBeTruthy();
    engine.destroy();
  });
});

describe('undo records', () => {
  it('do not accumulate for elements the page has discarded', async () => {
    const { win } = page('<ul id="list"></ul>');
    const list = win.document.getElementById('list');
    const engine = win.__variantEngine.create(
      set([{ op: 'addClass', selector: 'li', value: 'promo' }]), null, {});
    engine.apply('a');
    await settle(win);

    // A re-rendering list: each pass replaces every row with fresh nodes.
    for (let pass = 0; pass < 12; pass++) {
      while (list.firstChild) list.removeChild(list.firstChild);
      for (let i = 0; i < 100; i++) list.appendChild(win.document.createElement('li'));
      await settle(win);
    }

    expect(list.querySelectorAll('li.promo')).toHaveLength(100);
    // 1200 rows were decorated in total; only the live ones (plus at most one
    // pass awaiting the next prune) may still be pinned.
    expect(engine.stats().writeCount).toBe(1200);
    expect(engine.stats().undoCount).toBeLessThanOrEqual(356);
    engine.destroy();
  });

  it('are all kept on a page that stays under the prune threshold', () => {
    const { win } = page('<p class="t">a</p><p class="t">b</p>');
    const engine = win.__variantEngine.create(
      set([{ op: 'setText', selector: '.t', value: 'changed' }]), null, {});
    engine.apply('a');
    expect(engine.stats().undoCount).toBe(2);
    engine.revert();
    expect([...win.document.querySelectorAll('.t')].map((p) => p.textContent)).toEqual(['a', 'b']);
    engine.destroy();
  });
});

// INV-14: the public bundle's renderer has no authored-script path. A variant
// set carrying addScript can only reach a browser by tampering (publish refuses
// the op, and the store refuses to load a stored revision that carries it), so
// what the engine must do with one is refuse it by name and render nothing for
// it — while the legitimate ops beside it still apply.
describe('addScript', () => {
  const REFUSAL = 'addScript is not permitted on a published walkthrough';

  it('is refused by name in every form, before any field is read', () => {
    const { win } = page('');
    const forms = [
      { op: 'addScript', code: 'window.__ran = 1' },
      { op: 'addScript', src: 'https://cdn.example.com/a.js' },
      { op: 'addScript', src: 'https://cdn.example.com/a.js', code: 'x=1' },
      { op: 'addScript' },
      { op: 'addScript', selector: '.x', code: 'x=1' },
      { op: 'addScript', css: '.a{}' },
    ];
    const reasons = forms.map((op) => win.__variantEngine.validateOp(op));
    for (const res of reasons) {
      expect(res.ok).toBe(false);
      expect(res.reason).toContain(REFUSAL);
      expect(res.reason).toContain('INV-14');
    }
    // One refusal, not one per shape.
    expect(new Set(reasons.map((r) => r.reason)).size).toBe(1);
    // Near miss: the neighbouring root op still validates.
    expect(win.__variantEngine.validateOp({ op: 'addStyle', css: '.a{color:red}' }).ok).toBe(true);
  });

  it('creates no script element and runs nothing, while sibling ops still apply', () => {
    const { win, warnings } = page('<p id="t">before</p>');
    const scriptsBefore = win.document.querySelectorAll('script').length;
    const engine = win.__variantEngine.create(set([
      { op: 'addScript', code: 'window.__authoredRan = 1' },
      { op: 'setText', selector: '#t', value: 'after' },
      { op: 'addStyle', css: '#t{color:red}' },
    ]), null, {});
    engine.apply('a');

    // The refusal is reported, with the rule's own reason...
    const refused = engine.refusedOps();
    expect(refused).toHaveLength(1);
    expect(refused[0].op.op).toBe('addScript');
    expect(refused[0].reason).toContain(REFUSAL);
    expect(warnings.some((m) => m.includes(REFUSAL))).toBe(true);

    // ...and the dangerous side effect never happened: no script element
    // anywhere in the document (the variant root included), nothing ran.
    expect(win.document.querySelectorAll('script')).toHaveLength(scriptsBefore);
    const root = win.document.getElementById('__variant-engine-root');
    expect(root, 'premise: addStyle created the variant root').toBeTruthy();
    expect(root.querySelector('script')).toBeNull();
    expect(win.__authoredRan).toBeUndefined();

    // Near miss: the ops beside it were not swallowed by the refusal.
    expect(win.document.getElementById('t').textContent).toBe('after');
    expect(root.querySelectorAll('style')).toHaveLength(1);

    engine.revert();
    expect(win.document.getElementById('t').textContent).toBe('before');
    engine.destroy();
  });

  it('an unknown op is still its own, different refusal', () => {
    const { win } = page('');
    const res = win.__variantEngine.validateOp({ op: 'runCode', selector: '.x', value: 'x' });
    expect(res.ok).toBe(false);
    expect(res.reason).toContain('unknown op type');
    expect(res.reason).not.toContain(REFUSAL);
  });
});
