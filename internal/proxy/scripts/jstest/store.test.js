// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// store.js: a store request that is never sent rejects at once instead of
// waiting out the 10s response timeout (the contract session.js keeps).

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

// Timers are recorded, never run, so a pending 10s response timeout is visible
// as an uncleared 10000ms entry and nothing here sleeps.
function boot() {
  const timers = [];
  rt = loadModules(['frames', 'core', 'store'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      win.setTimeout = (fn, ms) => { const t = { fn, ms, cleared: false }; timers.push(t); return t; };
      win.clearTimeout = (t) => { if (t) t.cleared = true; };
    },
  });
  expect(rt.errors).toEqual({});
  const pendingTimeouts = () => timers.filter((t) => !t.cleared && t.ms === 10000);
  const storeFrames = () => rt.sockets.flatMap((s) => s.sent).filter((m) => m.type === 'store_request');
  return { win: rt.window, sockets: rt.sockets, pendingTimeouts, storeFrames };
}

// settled reports a promise's state after the current microtasks drain.
async function settled(p) {
  let state = 'pending';
  p.then(() => { state = 'resolved'; }, (e) => { state = e.message; });
  await Promise.resolve();
  await Promise.resolve();
  return state;
}

describe('store requests', () => {
  it('rejects at once when the socket is not connected', async () => {
    const { win, pendingTimeouts, storeFrames } = boot();
    // The socket is still CONNECTING.
    const state = await settled(win.__devtool_store.get('k'));
    expect(state).toBe('Store request failed: DevTool socket not connected');
    expect(pendingTimeouts()).toEqual([]);
    expect(storeFrames()).toEqual([]);
  });

  it('rejects at once when core.send does not write the frame', async () => {
    const { win, sockets, pendingTimeouts, storeFrames } = boot();
    sockets[0].open();
    // A BigInt does not serialise, so core.send reports the frame unsent.
    const state = await settled(win.__devtool_store.set('k', 1n));
    expect(state).toBe('Store request failed: DevTool socket not connected');
    expect(pendingTimeouts()).toEqual([]);
    expect(storeFrames()).toEqual([]);
  });

  it('sends the request and waits for the response when connected', async () => {
    const { win, sockets, pendingTimeouts, storeFrames } = boot();
    sockets[0].open();
    const p = win.__devtool_store.set('k', 'v');
    expect(await settled(p)).toBe('pending');
    expect(pendingTimeouts()).toHaveLength(1);
    expect(storeFrames()).toHaveLength(1);
    expect(storeFrames()[0].data).toMatchObject({ action: 'set', params: { key: 'k', value: 'v' } });
  });
});
