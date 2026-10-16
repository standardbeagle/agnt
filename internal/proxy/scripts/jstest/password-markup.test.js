// Markup and attribute maps that leave the page carry a password input's value
// attribute only with the operator's record-passwords opt-in
// (window.__devtool_record_passwords). Pages that mirror the typed value into
// the attribute (React controlled inputs) would otherwise put the password in
// the mutation log, DOM captures and element inspection.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const flush = () => new Promise((r) => setTimeout(r, 0));

const FORM = '<form id="login"><input id="user" value="alice"><div><input id="pw" type="Password" value="hunter2"></div></form>';

function page(html, opts = {}) {
  rt = loadModules(['frames', 'core', 'ui-tokens', 'shadow-root', 'utils', 'inspection', 'capture', 'mutation', 'diagnostics'], {
    html: `<!doctype html><html><body>${html}</body></html>`,
    before: (win) => { if (opts.recordPasswords) win.__devtool_record_passwords = true; },
  });
  expect(rt.errors).toEqual({});
  return rt.window;
}

async function addForm(win) {
  await flush();
  const holder = win.document.createElement('section');
  holder.id = 'holder';
  holder.innerHTML = FORM;
  win.document.body.appendChild(holder);
  await flush();
  return win.__devtool_mutations.getHistory(500).find(
    (m) => m.mutation_type === 'added' && m.added[0].id === 'holder');
}

describe('mutation markup of added and removed subtrees', () => {
  it('redacts a nested password value by default, with no marker', async () => {
    const win = page('<main id="app"></main>');
    const rec = await addForm(win);
    expect(rec, 'premise: the added subtree is recorded').toBeTruthy();
    expect(rec.added[0].html).toContain('value="[redacted]"');
    expect(rec.added[0].html).toContain('value="alice"');
    expect(rec.added[0].password).toBeUndefined();
    expect(win.document.getElementById('pw').getAttribute('value')).toBe('hunter2'); // live page untouched

    win.document.getElementById('holder').remove();
    await flush();
    const removed = win.__devtool_mutations.getHistory(500).find((m) => m.mutation_type === 'removed');
    expect(removed.removed[0].html).not.toContain('hunter2');
    expect(JSON.stringify(win.__devtool_mutations.getHistory(500))).not.toMatch(/hunter2/);
  });

  it('keeps it with record-passwords, marked for the proxy', async () => {
    const win = page('<main id="app"></main>', { recordPasswords: true });
    const rec = await addForm(win);
    expect(rec.added[0].html).toContain('value="hunter2"');
    expect(rec.added[0].password).toBe(true);
  });
});

describe('other markup and attribute snapshots', () => {
  it('redacts the value in DOM capture, DOM snapshots and element info by default', () => {
    const win = page(FORM);
    expect(win.__devtool_capture.captureDOM().snapshot).not.toContain('hunter2');
    expect(win.__devtool_capture.captureDOM().snapshot).toContain('value="alice"');
    expect(JSON.stringify(win.__devtool_diagnostics.captureDOMSnapshot())).not.toContain('hunter2');
    expect(win.__devtool_inspection.getElementInfo('#pw').attributes.value).toBe('[redacted]');
    expect(win.__devtool_inspection.getElementInfo('#user').attributes.value).toBe('alice');
    expect(win.__devtool_utils.passwordSafeOuterHTML(win.document.getElementById('login')).html).not.toContain('hunter2');
  });

  it('keeps it with record-passwords', () => {
    const win = page(FORM, { recordPasswords: true });
    expect(win.__devtool_capture.captureDOM().snapshot).toContain('hunter2');
    expect(win.__devtool_inspection.getElementInfo('#pw').attributes.value).toBe('hunter2');
    expect(win.__devtool_utils.passwordSafeOuterHTML(win.document.getElementById('pw')))
      .toEqual({ html: '<input id="pw" type="Password" value="hunter2">', password: true });
  });
});
