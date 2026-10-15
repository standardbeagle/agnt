// mutation.js: an attribute change on a password field is recorded, never
// its text. Pages that mirror the typed value into the value attribute (React
// controlled inputs) would otherwise put the password in the mutation log.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const flush = () => new Promise((r) => setTimeout(r, 0));

function page(html) {
  rt = loadModules(['frames', 'core', 'utils', 'mutation'], {
    html: `<!doctype html><html><body>${html}</body></html>`,
  });
  expect(rt.errors).toEqual({});
  return rt.window;
}

function valueChanges(win, id) {
  return win.__devtool_mutations.getHistory(500).filter(
    (m) => m.mutation_type === 'attributes' && m.attribute.name === 'value' && m.target.id === id,
  );
}

describe('attribute mutations on password fields', () => {
  it('redacts the old and new value of a password field', async () => {
    const win = page('<input id="pw" type="password" value="hunter1">');
    await flush();
    win.document.getElementById('pw').setAttribute('value', 'hunter2');
    await flush();

    const changes = valueChanges(win, 'pw');
    expect(changes.length).toBe(1);
    expect(changes[0].attribute.old_value).toBe('[redacted]');
    expect(changes[0].attribute.new_value).toBe('[redacted]');
    expect(JSON.stringify(win.__devtool_mutations.getHistory(500))).not.toMatch(/hunter/);
  });

  it('keeps the value of a non-password field', async () => {
    const win = page('<input id="q" type="text" value="old">');
    await flush();
    win.document.getElementById('q').setAttribute('value', 'new');
    await flush();

    const changes = valueChanges(win, 'q');
    expect(changes.length).toBe(1);
    expect(changes[0].attribute.old_value).toBe('old');
    expect(changes[0].attribute.new_value).toBe('new');
  });
});
