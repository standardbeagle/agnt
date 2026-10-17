// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// toast.js secret-entry field: what the user is told after Save.
//
// The value goes out as secret_submit, and the server answers secret_ack
// {name, ok, error} once the store has taken it — or refused it (a store
// failure, or a connection not allowed to write secrets). A frame that merely
// went out is not a saved secret, so "saved" may only follow ok:true.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const ACK_TIMEOUT_MS = 10000;

function boot() {
  const timers = [];
  rt = loadModules(['frames', 'core', 'ui-tokens', 'toast'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      win.setTimeout = (fn, ms) => { const t = { fn, ms, cleared: false }; timers.push(t); return t; };
      win.clearTimeout = (t) => { if (t) t.cleared = true; };
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  const sock = rt.sockets[0];
  sock.open();

  const toasts = () => win.document.getElementById('__devtool-toast-container').textContent;
  const submit = (name, value) => {
    win.__devtool_toast.show({ input: 'secret', name });
    const form = win.document.querySelector('#__devtool-toast-container form');
    form.querySelector('input[type=password]').value = value;
    form.dispatchEvent(new win.Event('submit', { cancelable: true }));
  };
  const serverSends = (msg) => sock.onmessage({ data: JSON.stringify(msg) });
  const ackTimer = () => timers.filter((t) => !t.cleared && t.ms === ACK_TIMEOUT_MS);
  return { win, sock, toasts, submit, serverSends, ackTimer };
}

describe('secret-entry outcome', () => {
  it('sends the value as secret_submit and does not claim it is saved yet', () => {
    const { sock, toasts, submit } = boot();
    submit('API_KEY', 'hunter2');
    expect(sock.sent.filter((m) => m.type === 'secret_submit').map((m) => m.data))
      .toEqual([{ name: 'API_KEY', value: 'hunter2' }]);
    expect(toasts()).not.toContain('saved to the agnt store');
  });

  it('reports the server refusal from secret_ack ok:false', () => {
    const { toasts, submit, serverSends } = boot();
    submit('API_KEY', 'hunter2');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: false, error: 'store write failed' });
    expect(toasts()).toContain('store write failed');
    expect(toasts()).not.toContain('saved to the agnt store');
  });

  it('confirms the save only on secret_ack ok:true', () => {
    const { toasts, submit, serverSends } = boot();
    submit('API_KEY', 'hunter2');
    serverSends({ type: 'secret_ack', name: 'OTHER', ok: true });
    expect(toasts(), 'an ack for another secret settles nothing').not.toContain('saved to the agnt store');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: true });
    expect(toasts()).toContain('API_KEY saved to the agnt store');
  });

  it('says the outcome is unknown when no ack arrives', () => {
    const { toasts, submit, ackTimer } = boot();
    submit('API_KEY', 'hunter2');
    const armed = ackTimer().filter((t) => t.fn.toString().includes('No confirmation'));
    expect(armed).toHaveLength(1);
    armed[0].cleared = true;
    armed[0].fn();
    expect(toasts()).toContain('No confirmation that API_KEY was saved');
    expect(toasts()).not.toContain('saved to the agnt store');
  });
});
