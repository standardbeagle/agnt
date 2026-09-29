import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedSession = readFileSync(join(scriptsDir, 'session.js'), 'utf8');

// core.send() returns false (no throw) when the socket is not open. session.js
// used to ignore that return value and just wait out the full 10s timeout for
// a request that was never actually sent. Mirrors indicator-data.js's
// chaosRequest guard: a false send must reject immediately.
function runtime(sendReturnValue) {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  const sendCalls = [];
  let messageHandler = null;
  dom.window.__devtool_core = {
    send: (type, data) => { sendCalls.push({ type, data }); return sendReturnValue; },
    onMessage: (fn) => { messageHandler = fn; },
  };
  dom.window.eval(shippedSession);
  return {
    window: dom.window,
    sendCalls,
    deliver: (message) => messageHandler(message),
  };
}

describe('shipped session.js send-failure handling', () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it('rejects immediately when core.send() returns false, without waiting out the timeout', async () => {
    const { window } = runtime(false);
    const promise = window.__devtool_session.list();
    await expect(promise).rejects.toThrow(/not connected/);
  }, 15000);

  it('resolves normally when core.send() succeeds and the session_response arrives', async () => {
    const { window, sendCalls, deliver } = runtime(true);
    const promise = window.__devtool_session.list();

    expect(sendCalls.length).toBe(1);
    const requestId = sendCalls[0].data.request_id;

    deliver({ type: 'session_response', data: { request_id: requestId, result: { sessions: [] } } });

    await expect(promise).resolves.toEqual({ sessions: [] });
  }, 15000);

  it('still times out (not an immediate reject) when send() succeeds but no response ever arrives', async () => {
    const { window } = runtime(true);
    const promise = window.__devtool_session.list();
    let settled = false;
    let rejection = null;
    promise.catch((e) => { settled = true; rejection = e; });

    await Promise.resolve();
    expect(settled).toBe(false); // still pending — send() succeeded

    await vi.advanceTimersByTimeAsync(10000);
    expect(settled).toBe(true);
    expect(rejection.message).toMatch(/timed out/);
  }, 15000);

  it('does not call core.send() at all when core.send is unavailable', async () => {
    const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
    dom.window.__devtool_core = undefined;
    dom.window.eval(shippedSession);

    await expect(dom.window.__devtool_session.list()).rejects.toThrow(/not connected/);
  }, 15000);
});
