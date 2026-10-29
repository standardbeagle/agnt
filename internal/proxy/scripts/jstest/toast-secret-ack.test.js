// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// toast.js secret-entry field: what the user is told after Save.
//
// The value goes out as secret_submit, and the server answers secret_ack
// {name, ok, error} once the store has taken it — or refused it (a store
// failure, or a connection not allowed to write secrets). A frame that merely
// went out is not a saved secret, so "saved" may only follow ok:true. Until
// then the prompt stays open with an inline status line, so a refusal or a
// missing ack can be retried in place. Driven through the real core.js
// transport, so the ack reaches toast.js the way the server's frame would.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const ACK_TIMEOUT_MS = 10000;

function boot({ connected = true } = {}) {
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
  if (connected) sock.open();

  const container = () => win.document.getElementById('__devtool-toast-container');
  const toasts = () => (container() ? container().textContent : '');
  const submit = (name, value) => {
    win.__devtool_toast.show({ input: 'secret', name });
    const form = container().querySelector('form');
    const input = form.querySelector('input[type=password]');
    input.value = value;
    form.dispatchEvent(new win.Event('submit', { cancelable: true }));
    return {
      form,
      input,
      button: form.querySelector('button[type=submit]'),
      status: form.parentNode.querySelector('[role=status]'),
      resubmit: (v) => { input.value = v; form.dispatchEvent(new win.Event('submit', { cancelable: true })); },
    };
  };
  const serverSends = (msg) => sock.onmessage({ data: JSON.stringify(msg) });
  // Fires every live ack timeout, as if ACK_TIMEOUT_MS had passed.
  const expireAckTimers = () => {
    for (const t of timers.filter((x) => !x.cleared && x.ms === ACK_TIMEOUT_MS)) { t.cleared = true; t.fn(); }
  };
  const submits = () => sock.sent.filter((m) => m.type === 'secret_submit');
  return { win, toasts, submit, serverSends, expireAckTimers, submits };
}

describe('secret-entry outcome', () => {
  it('sends the value as secret_submit and waits, without claiming it is saved', () => {
    const { toasts, submit, submits } = boot();
    const { status, button, input } = submit('API_KEY', 'hunter2');
    expect(submits().map((m) => m.data)).toEqual([{ name: 'API_KEY', value: 'hunter2' }]);
    expect(status.textContent).toBe('Saving…');
    expect(button.disabled).toBe(true);
    expect(input.disabled).toBe(true);
    expect(input.value, 'the typed value is not left in the field').toBe('');
    expect(toasts()).not.toContain('saved to the agnt store');
  });

  it('confirms the save only on secret_ack ok:true for that name', () => {
    const { toasts, submit, serverSends } = boot();
    submit('API_KEY', 'hunter2');
    serverSends({ type: 'secret_ack', name: 'OTHER', ok: true });
    expect(toasts(), 'an ack for another secret settles nothing').not.toContain('saved to the agnt store');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: true });
    expect(toasts()).toContain('API_KEY saved to the agnt store');
  });

  it('shows the server refusal reason inline and leaves the prompt open to retry', () => {
    const { toasts, submit, serverSends } = boot();
    const { status, button, input } = submit('API_KEY', 'hunter2');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: false, error: 'store write failed' });
    expect(status.textContent).toBe('Not saved: store write failed — try again.');
    expect(button.disabled).toBe(false);
    expect(input.disabled).toBe(false);
    expect(toasts()).not.toContain('saved to the agnt store');
  });

  it('says "rejected" when the refusal carries no reason', () => {
    const { submit, serverSends } = boot();
    const { status } = submit('API_KEY', 'hunter2');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: false });
    expect(status.textContent).toBe('Rejected — try again.');
  });

  it('fails visibly when no ack arrives, and a retry is resolved normally', () => {
    const { toasts, submit, serverSends, expireAckTimers, submits } = boot();
    const first = submit('API_KEY', 'hunter2');
    expireAckTimers();
    expect(first.status.textContent).toBe('No response — try again.');
    expect(toasts()).not.toContain('saved to the agnt store');

    first.resubmit('second-try');
    expect(submits()).toHaveLength(2);
    expect(first.status.textContent).toBe('Saving…');
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: true });
    expect(toasts()).toContain('API_KEY saved to the agnt store');
  });

  it('reports "not connected" at once when nothing could be sent', () => {
    const { submit, submits } = boot({ connected: false });
    const { status, button } = submit('API_KEY', 'hunter2');
    expect(submits()).toHaveLength(0);
    expect(status.textContent).toBe('Not connected — try again once reconnected.');
    expect(button.disabled, 'nothing is pending, so nothing is locked').toBe(false);
  });

  it('raises a toast for a refusal that arrives after the prompt was closed', () => {
    const { toasts, submit, serverSends } = boot();
    const { form } = submit('API_KEY', 'hunter2');
    form.parentNode.parentNode.querySelector('button[aria-label="Dismiss notification"]').click();
    serverSends({ type: 'secret_ack', name: 'API_KEY', ok: false, error: 'store write failed' });
    expect(toasts()).toContain('Secret not saved');
    expect(toasts()).toContain('store write failed');
  });
});
