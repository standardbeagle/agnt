// snapshot-helper.js: createBaseline / compareToBaseline settle only on the
// proxy's snapshot_response. They used to resolve {success: true} the moment
// the frame was sent, though nothing on the Go side handled it.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const PNG_B64 = 'iVBORw0KGgoAAAANSUhEUg==';

// Timers are recorded, never run: the response timeout is visible as a pending
// 60000ms entry and a test fires it by hand. html2canvas is stubbed to a canvas
// whose data URL the test chooses.
function boot(dataURL = 'data:image/png;base64,' + PNG_B64) {
  const timers = [];
  let captures = 0;
  rt = loadModules(['frames', 'core', 'snapshot-helper'], {
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      win.setTimeout = (fn, ms) => { const t = { fn, ms, cleared: false }; timers.push(t); return t; };
      win.clearTimeout = (t) => { if (t) t.cleared = true; };
      win.html2canvas = () => { captures++; return Promise.resolve({ toDataURL: () => dataURL }); };
    },
  });
  expect(rt.errors).toEqual({});
  const sock = rt.sockets[0];
  return {
    snap: rt.window.__devtool_snapshot,
    sock,
    captures: () => captures,
    responseTimers: () => timers.filter((t) => !t.cleared && t.ms === 60000),
    requests: () => sock.sent.filter((m) => m.type === 'snapshot_request'),
    reply: (msg) => sock.onmessage({ data: JSON.stringify({ type: 'snapshot_response', ...msg }) }),
  };
}

// settle drains the capture's promise chain and reports the promise's state.
async function settle(p) {
  let state = 'pending';
  p.then((v) => { state = { value: v }; }, (e) => { state = { error: e.message }; });
  for (let i = 0; i < 5; i++) await new Promise((r) => setTimeout(r, 0));
  return state;
}

describe('snapshot baseline/compare', () => {
  it('waits for the proxy reply and resolves with the saved baseline', async () => {
    const b = boot();
    b.sock.open();
    const p = b.snap.createBaseline('home-v1');
    expect(await settle(p)).toBe('pending');
    expect(b.responseTimers()).toHaveLength(1);
    expect(b.requests()).toHaveLength(1);
    const req = b.requests()[0].data;
    expect(req).toMatchObject({ action: 'baseline', name: 'home-v1', page: { url: '/page', screenshot_data: PNG_B64 } });

    const saved = { name: 'home-v1', pages: [{ url: '/page', screenshot: 'page_x.png' }] };
    b.reply({ request_id: req.request_id, result: saved });
    expect(await settle(p)).toEqual({ value: saved });
    expect(b.responseTimers()).toEqual([]);
  });

  it('resolves compare with the CompareResult', async () => {
    const b = boot();
    b.sock.open();
    const p = b.snap.compare('home-v1');
    await settle(p);
    const req = b.requests()[0].data;
    expect(req.action).toBe('compare');
    const result = { baseline_name: 'home-v1', summary: { has_regressions: true, average_diff: 12.5 } };
    b.reply({ request_id: req.request_id, result });
    expect(await settle(p)).toEqual({ value: result });
  });

  it('rejects with the proxy error', async () => {
    const b = boot();
    b.sock.open();
    const p = b.snap.compareToBaseline('nope');
    await settle(p);
    b.reply({ request_id: b.requests()[0].data.request_id, error: 'failed to compare to baseline "nope": load baseline: not found' });
    expect(await settle(p)).toEqual({ error: 'failed to compare to baseline "nope": load baseline: not found' });
    expect(b.responseTimers()).toEqual([]);
  });

  it('ignores a reply to another request', async () => {
    const b = boot();
    b.sock.open();
    const p = b.snap.createBaseline('v1');
    await settle(p);
    b.reply({ request_id: 'someone-else', result: { name: 'v1' } });
    expect(await settle(p)).toBe('pending');
  });

  it('rejects when no reply arrives in time', async () => {
    const b = boot();
    b.sock.open();
    const p = b.snap.createBaseline('v1');
    await settle(p);
    const [timer] = b.responseTimers();
    timer.fn();
    expect(await settle(p)).toEqual({ error: 'Baseline "v1" failed: no reply from the proxy within 60s' });
    // A late reply is dropped, not double-settled.
    b.reply({ request_id: b.requests()[0].data.request_id, result: { name: 'v1' } });
  });

  it('rejects at once, before capturing, when the socket is not connected', async () => {
    const b = boot();
    // Still CONNECTING.
    expect(await settle(b.snap.createBaseline('v1'))).toEqual({ error: 'Baseline "v1" failed: DevTool socket not connected' });
    expect(b.captures()).toBe(0);
    expect(b.requests()).toEqual([]);
    expect(b.responseTimers()).toEqual([]);
  });

  it('rejects an oversize capture without sending it', async () => {
    const big = 'data:image/png;base64,' + PNG_B64 + 'A'.repeat(15 * 1024 * 1024);
    const b = boot(big);
    b.sock.open();
    const state = await settle(b.snap.createBaseline('v1'));
    expect(state.error).toMatch(/^Baseline "v1" failed: the capture is \d+ bytes of base64, over the 15728640 byte limit/);
    expect(b.requests()).toEqual([]);
    expect(b.responseTimers()).toEqual([]);
  });

  it('requires a name', async () => {
    const b = boot();
    b.sock.open();
    expect(await settle(b.snap.createBaseline(''))).toEqual({ error: 'A baseline name is required' });
    expect(b.captures()).toBe(0);
  });
});
