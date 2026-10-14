// The ancestor walks in utils.js are bounded. A walk that hits the bound has
// not seen the whole ancestry, and must not answer as if it had: a selector
// cut short of the root is relative, and "no stacking context found" is not
// "the root is the stacking context".

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { beforeEach, describe, expect, it } from 'vitest';

const SCRIPTS_DIR = join(dirname(fileURLToPath(import.meta.url)), '..');
const DEPTH = 1100; // beyond MAX_ANCESTOR_WALK (1024)

function loadUtils() {
  delete window.__devtool_utils;
  new Function(readFileSync(join(SCRIPTS_DIR, 'utils.js'), 'utf8')).call(window);
  return window.__devtool_utils;
}

// deepChain appends DEPTH nested divs under parent and returns a span at the
// bottom.
function deepChain(parent) {
  let node = parent;
  for (let i = 0; i < DEPTH; i++) {
    const div = document.createElement('div');
    node.appendChild(div);
    node = div;
  }
  const span = document.createElement('span');
  node.appendChild(span);
  return span;
}

// jsdom's selector engine and getComputedStyle are slow over a 1100-deep tree.
describe('utils.js bounded ancestor walks', { timeout: 120_000 }, () => {
  let utils;
  beforeEach(() => {
    document.body.innerHTML = '';
    utils = loadUtils();
  });

  it('keeps a truncated selector only when it still names the element alone', () => {
    const span = deepChain(document.body);
    const sel = utils.generateSelector(span);
    expect(sel).not.toBe('');
    expect(document.querySelectorAll(sel)).toHaveLength(1);
    expect(document.querySelector(sel)).toBe(span);
  });

  it('returns no selector when the truncated path is ambiguous', () => {
    const first = deepChain(document.body);
    deepChain(document.body); // an identical branch the relative path also matches
    expect(utils.generateSelector(first)).toBe('');
  });

  it('reports the stacking context as unknown, not the root', () => {
    const span = deepChain(document.body);
    expect(utils.getStackingContext(span)).toBeNull();
    const chain = utils.getStackingChain(span);
    const last = chain[chain.length - 1];
    expect(last.truncated).toBe(true);
    expect(chain.some((c) => c.selector === ':root')).toBe(false);
  });

  it('still answers normally for an ordinary depth', () => {
    document.body.innerHTML = '<main><section><p><span id="x-1">t</span><b>u</b></p></section></main>';
    const b = document.querySelector('b');
    expect(document.querySelector(utils.generateSelector(b))).toBe(b);
    expect(utils.getStackingContext(b)).toBe(document.documentElement);
    expect(utils.getStackingChain(b).at(-1).selector).toBe(':root');
  });
});
