import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shipped = readFileSync(join(scriptsDir, 'persona-chip.js'), 'utf8');

const STATE = {
  persona: 'standard',
  personas: [
    { name: 'standard', email: 'std@example.com', display_name: 'Standard', roles: ['user'] },
    { name: 'admin', email: 'admin@example.com', display_name: '<img src=x onerror=alert(1)>', roles: ['admin', 'user'] },
  ],
};

// shell builds a chrome-shell window with the shipped chip loaded. `state` is
// what /state answers (null = the app's own HTML, as on a proxy without
// dev-oidc); `withFrame` adds the content iframe the frame-context adapter
// hands out.
function shell({ state, withFrame = true }) {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only', url: 'http://localhost:4000/' });
  const w = dom.window;
  let frame = null;
  if (withFrame) {
    frame = w.document.createElement('iframe');
    w.document.body.appendChild(frame);
  }
  w.__devtool_context = { contentFrame: () => frame };
  const fetched = [];
  w.fetch = (url, opts) => {
    fetched.push({ url, opts });
    const json = state !== null;
    return Promise.resolve({
      ok: json,
      headers: { get: () => (json ? 'application/json' : 'text/html') },
      json: () => Promise.resolve(state),
    });
  };
  const submitted = [];
  w.HTMLFormElement.prototype.submit = function () {
    submitted.push({
      action: this.getAttribute('action'),
      method: this.method,
      target: this.target,
      fields: Object.fromEntries(new w.FormData(this)),
    });
  };
  w.eval(shipped);
  return { w, frame, fetched, submitted };
}

async function mounted(opts) {
  const env = shell(opts);
  const chip = env.w.__devtool_personaChip.create();
  env.w.document.body.appendChild(chip);
  await chip.__devtoolRefresh();
  return { ...env, chip };
}

describe('persona chip', () => {
  it('stays hidden when the proxy has no dev-oidc issuer', async () => {
    const { chip, fetched } = await mounted({ state: null });
    expect(fetched[0].url).toBe('/__agnt/oidc/state');
    expect(chip.style.display).toBe('none');
    expect(chip.querySelectorAll('[role=menuitemradio]')).toHaveLength(0);
  });

  it('shows the current persona and lists every persona', async () => {
    const { chip } = await mounted({ state: STATE });
    const btn = chip.querySelector('.__devtool-persona-button');
    expect(chip.style.display).toBe('flex');
    expect(btn.textContent).toBe('as: Standard');
    expect(btn.title).toBe('std@example.com · user');
    const items = [...chip.querySelectorAll('[role=menuitemradio]')];
    expect(items.map((i) => i.dataset.persona)).toEqual(['standard', 'admin']);
    expect(items.map((i) => i.getAttribute('aria-checked'))).toEqual(['true', 'false']);
  });

  it('renders persona text as text, never markup', async () => {
    const { chip } = await mounted({ state: STATE });
    expect(chip.querySelector('img')).toBeNull();
    expect(chip.textContent).toContain('<img src=x onerror=alert(1)>');
  });

  it('opens on click and submits the switch form into the content frame', async () => {
    const { chip, frame, submitted } = await mounted({ state: STATE });
    const btn = chip.querySelector('.__devtool-persona-button');
    const menu = chip.querySelector('[role=menu]');
    expect(menu.hidden).toBe(true);
    btn.click();
    expect(menu.hidden).toBe(false);
    expect(btn.getAttribute('aria-expanded')).toBe('true');

    chip.querySelector('[data-persona=admin]').click();
    expect(menu.hidden).toBe(true);
    expect(submitted).toHaveLength(1);
    expect(submitted[0]).toMatchObject({ action: '/__agnt/oidc/switch', method: 'post', fields: { persona: 'admin' } });
    expect(frame.name).toBe('__agnt_content');
    expect(submitted[0].target).toBe('__agnt_content');
  });

  it('keeps an existing frame name as the form target', async () => {
    const env = shell({ state: STATE });
    env.frame.name = 'app-frame';
    const form = env.w.__devtool_personaChip.switchTo('admin');
    expect(env.frame.name).toBe('app-frame');
    expect(env.submitted[0].target).toBe('app-frame');
    expect(form.isConnected).toBe(false);
  });

  it('targets its own window outside the wrapped shell', async () => {
    const env = shell({ state: STATE, withFrame: false });
    env.w.__devtool_personaChip.switchTo('admin');
    expect(env.submitted[0].target).toBe('');
  });

  it('refreshes when the content frame finishes loading', async () => {
    const { frame, fetched } = await mounted({ state: STATE });
    const before = fetched.length;
    frame.dispatchEvent(new frame.ownerDocument.defaultView.Event('load'));
    await new Promise((r) => setTimeout(r, 0));
    expect(fetched.length).toBe(before + 1);
  });
});
