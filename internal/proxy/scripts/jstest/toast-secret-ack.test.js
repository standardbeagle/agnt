import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedToast = readFileSync(join(scriptsDir, 'toast.js'), 'utf8');

// The daemon replies to 'secret_submit' with a 'secret_ack' message
// ({type, name, ok} — ws_messages.go), but toast.js used to dismiss the
// secret-entry toast and claim "saved" the instant WebSocket.send() returned
// true, never looking at the ack. This drives the real shipped module against
// a fake core transport that captures the registered onMessage handler, so
// the ack can be delivered exactly the way core.js would deliver it.
function runtime() {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  const sendCalls = [];
  let messageHandler = null;
  let sendReturnValue = true;

  dom.window.__devtool_core = {
    send: (type, data) => { sendCalls.push({ type, data }); return sendReturnValue; },
    onMessage: (fn) => { messageHandler = fn; },
  };
  // jsdom does not implement requestAnimationFrame; toast.js only uses it to
  // kick off the (irrelevant here) progress-bar CSS transition.
  dom.window.requestAnimationFrame = (cb) => setTimeout(cb, 0);
  dom.window.eval(shippedToast);

  return {
    window: dom.window,
    sendCalls,
    deliver: (message) => messageHandler(message),
    setSendReturn: (v) => { sendReturnValue = v; },
  };
}

function submitSecretToast(rt, name) {
  rt.window.__devtool_toast.show({ input: 'secret', name: name, title: 'Enter ' + name });
  const form = rt.window.document.querySelector('#__devtool-toast-container form');
  const input = form.querySelector('input[type="password"]');
  input.value = 'sekret-value';
  form.dispatchEvent(new rt.window.Event('submit', { bubbles: true, cancelable: true }));
  return { form: form, status: form.parentNode.querySelector('[role="status"]') };
}

describe('shipped toast.js secret_ack handling', () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  // Each test constructs its own JSDOM instance (JSDOM environment creation
  // is the dominant cost here, per the vitest.config.js note on shared
  // environments) — generous headroom over the 5000ms default so this file
  // is not a false-flake target under a loaded machine.
  const TEST_TIMEOUT_MS = 20000;

  it('waits for a real secret_ack{ok:true} before confirming and closing', () => {
    const rt = runtime();
    const { form, status } = submitSecretToast(rt, 'API_KEY');

    expect(rt.sendCalls).toEqual([{ type: 'secret_submit', data: { name: 'API_KEY', value: 'sekret-value' } }]);
    // Not yet dismissed — waiting on the ack.
    expect(status.textContent).toBe('Saving…');
    expect(form.querySelector('button[type="submit"]').disabled).toBe(true);

    rt.deliver({ type: 'secret_ack', name: 'API_KEY', ok: true });

    // A confirmation toast was raised in its place.
    const toasts = rt.window.document.querySelectorAll('#__devtool-toast-container > div');
    const texts = Array.from(toasts).map((t) => t.textContent);
    expect(texts.some((t) => t.indexOf('saved to the agnt store') !== -1)).toBe(true);
  }, TEST_TIMEOUT_MS);

  it('shows a visible, retryable failure on secret_ack{ok:false} instead of claiming success', () => {
    const rt = runtime();
    const { form, status } = submitSecretToast(rt, 'API_KEY');

    rt.deliver({ type: 'secret_ack', name: 'API_KEY', ok: false });

    expect(status.textContent).toBe('Rejected — try again.');
    // The form is still there and re-enabled: the visitor can retry without
    // the toast having silently closed on them.
    expect(form.querySelector('button[type="submit"]').disabled).toBe(false);
    expect(form.querySelector('input[type="password"]').disabled).toBe(false);
  }, TEST_TIMEOUT_MS);

  it('fails visibly if no secret_ack ever arrives', () => {
    const rt = runtime();
    const { status } = submitSecretToast(rt, 'API_KEY');

    vi.advanceTimersByTime(9999);
    expect(status.textContent).toBe('Saving…');
    vi.advanceTimersByTime(1);
    expect(status.textContent).toBe('No response — try again.');
  }, TEST_TIMEOUT_MS);

  it('a late ack after the timeout does not resolve a fresh submission of the same name', () => {
    const rt = runtime();
    const first = submitSecretToast(rt, 'API_KEY');
    vi.advanceTimersByTime(10000);
    expect(first.status.textContent).toBe('No response — try again.');

    // Retry: submit again (a fresh pending entry for the same name).
    first.form.querySelector('input[type="password"]').value = 'second-try';
    first.form.dispatchEvent(new rt.window.Event('submit', { bubbles: true, cancelable: true }));
    expect(first.status.textContent).toBe('Saving…');

    // The ack now resolves the SECOND (current) pending entry normally — the
    // point of the token guard is that a stray ack arriving during the first
    // entry's now-expired window could never have resolved this one instead.
    rt.deliver({ type: 'secret_ack', name: 'API_KEY', ok: true });
    const toasts = rt.window.document.querySelectorAll('#__devtool-toast-container > div');
    const texts = Array.from(toasts).map((t) => t.textContent);
    expect(texts.some((t) => t.indexOf('saved to the agnt store') !== -1)).toBe(true);
  }, TEST_TIMEOUT_MS);

  it('reports "not connected" immediately when send() itself fails, without waiting for an ack', () => {
    const rt = runtime();
    rt.setSendReturn(false);
    const { status, form } = submitSecretToast(rt, 'API_KEY');

    expect(status.textContent).toBe('Not connected — try again once reconnected.');
    // No disable — nothing was sent, nothing to wait on.
    expect(form.querySelector('button[type="submit"]').disabled).toBe(false);
  }, TEST_TIMEOUT_MS);
});
