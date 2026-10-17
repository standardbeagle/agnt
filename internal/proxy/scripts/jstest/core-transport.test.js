// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// core.js control transport: reconnect schedule, stale-socket handling, the
// online / visibility re-arm, the transport-diagnostics global, and what the
// console override captures.

import { afterEach, describe, expect, it, vi } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => {
  if (rt) { rt.close(); rt = null; }
  vi.useRealTimers();
});

// The page's timers are replaced with a recorder so the backoff schedule is
// asserted as a list of requested delays — nothing here sleeps.
function boot(modules = ['frames', 'core']) {
  const timers = [];
  rt = loadModules(modules, {
    before: (win) => {
      // The page console is silenced so captured test output stays readable;
      // core.js wraps whatever console it finds, so capture is unaffected.
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      win.setTimeout = (fn, ms) => { const t = { fn, ms, cleared: false }; timers.push(t); return t; };
      win.clearTimeout = (t) => { if (t) t.cleared = true; };
    },
  });
  expect(rt.errors).toEqual({});
  const fire = (t) => { expect(t.cleared).toBe(false); t.cleared = true; t.fn(); };
  // Only the reconnect timers: frames.js also arms short (≤3s) one-shots.
  const reconnects = () => timers.filter((t) => !t.cleared && t.ms >= 1000 && t.ms !== 3000);
  return { win: rt.window, sockets: rt.sockets, timers, fire, reconnects };
}

describe('reconnect', () => {
  it('backs off exponentially, caps the delay, and never gives up', () => {
    const { sockets, timers, fire } = boot();
    expect(sockets).toHaveLength(1);
    sockets[0].open();

    const delays = [];
    for (let i = 0; i < 12; i++) {
      const before = timers.length;
      sockets.at(-1).serverClose();
      const armed = timers.slice(before).filter((t) => !t.cleared);
      expect(armed, `attempt ${i + 1} must arm exactly one retry`).toHaveLength(1);
      delays.push(armed[0].ms);
      const socketsBefore = sockets.length;
      fire(armed[0]);
      expect(sockets.length).toBe(socketsBefore + 1);
    }
    expect(delays.slice(0, 6)).toEqual([1000, 2000, 4000, 8000, 16000, 30000]);
    expect(delays.slice(6)).toEqual([30000, 30000, 30000, 30000, 30000, 30000]);
  });

  it('resets the schedule after a successful connect', () => {
    const { sockets, timers, fire } = boot();
    sockets[0].open();
    sockets[0].serverClose();
    fire(timers.at(-1));
    sockets.at(-1).serverClose();
    expect(timers.at(-1).ms).toBe(2000);
    fire(timers.at(-1));

    sockets.at(-1).open();
    sockets.at(-1).serverClose();
    expect(timers.at(-1).ms).toBe(1000);
  });

  it('ignores the close event of a socket it already replaced', () => {
    const { win, sockets, timers } = boot();
    const first = sockets[0];
    first.open();

    const timersBefore = timers.length;
    win.__devtool_core.reconnect();
    expect(sockets).toHaveLength(2);
    const second = sockets[1];
    second.open();

    // The retired socket reports its close late, exactly as a real one does.
    first.readyState = 3;
    if (typeof first.onclose === 'function') first.onclose({ code: 1000, wasClean: true });

    expect(timers.slice(timersBefore).filter((t) => !t.cleared && t.ms >= 1000 && t.ms !== 3000)).toEqual([]);
    expect(sockets).toHaveLength(2);
    expect(win.__devtool_core.isConnected()).toBe(true);
    expect(win.__devtool_core.ws()).toBe(second);
  });

  it('reconnects at once when the browser comes back online', () => {
    const { win, sockets, timers } = boot();
    sockets[0].open();
    sockets[0].serverClose();
    sockets[0].serverClose();
    const pending = timers.at(-1);
    expect(pending.cleared).toBe(false);

    win.dispatchEvent(new win.Event('online'));

    expect(sockets).toHaveLength(2);
    expect(pending.cleared, 'the pending backoff timer is superseded').toBe(true);
  });

  it('reconnects when the tab becomes visible, and not while a socket is live', () => {
    const { win, sockets } = boot();
    sockets[0].open();

    win.document.dispatchEvent(new win.Event('visibilitychange'));
    expect(sockets, 'a live socket is left alone').toHaveLength(1);

    sockets[0].serverClose();
    win.document.dispatchEvent(new win.Event('visibilitychange'));
    expect(sockets).toHaveLength(2);

    // Still connecting: a second signal must not open a third socket.
    win.dispatchEvent(new win.Event('online'));
    expect(sockets).toHaveLength(2);
  });

  it('reports "connecting", never "disconnected", during an outage', () => {
    const { win, sockets } = boot();
    sockets[0].open();
    expect(win.__devtool_core.connectionState()).toBe('connected');
    sockets[0].serverClose();
    expect(win.__devtool_core.connectionState()).toBe('connecting');
  });
});

describe('transport diagnostics global', () => {
  it('is exported on its own global with reconnect reachable', () => {
    const { win } = boot();
    expect(win.__devtool_diagnostics).toBeUndefined();
    const diag = win.__devtool_transport_diag;
    expect(typeof diag.getHistory).toBe('function');
    expect(typeof diag.reconnect).toBe('function');
    expect(diag.getConnectionState().maxReconnectAttempts).toBeNull();
    expect(typeof win.__devtool_core.reconnect).toBe('function');
  });

  it('survives diagnostics.js loading after it', () => {
    const { win } = boot(['frames', 'core', 'utils', 'diagnostics']);
    expect(typeof win.__devtool_transport_diag.getConnectionState).toBe('function');
    expect(typeof win.__devtool_diagnostics.outlineAll).toBe('function');
    expect(win.__devtool_diagnostics.getConnectionState).toBeUndefined();
  });
});

describe('console capture', () => {
  it('records an Error argument as its message and stack, not "{}"', () => {
    const { win } = boot();
    const err = new win.TypeError('cannot save draft');
    win.console.error('Save failed', err);

    const [entry] = win.__devtool_errors.getConsoleErrors().slice(-1);
    expect(entry.message).toContain('Save failed');
    expect(entry.message).toContain('TypeError: cannot save draft');
    expect(entry.message).not.toContain('{}');
    expect(entry.stack).toContain('cannot save draft');
  });

  it('records an Error nested inside an object argument', () => {
    const { win } = boot();
    win.console.error({ op: 'save', cause: new win.Error('disk full') });
    const [entry] = win.__devtool_errors.getConsoleErrors().slice(-1);
    expect(entry.message).toContain('disk full');
    expect(entry.message).toContain('"op":"save"');
  });

  it('truncates one oversized argument and keeps the others', () => {
    const { win } = boot();
    win.console.warn('x'.repeat(50000), 'tail-marker');
    const [entry] = win.__devtool_errors.getConsoleWarnings().slice(-1);
    expect(entry.message.length).toBeLessThan(2200);
    expect(entry.message).toContain('[truncated 48000 chars]');
    expect(entry.message).toContain('tail-marker');
  });

  it('bounds the work spent serialising a large object graph', () => {
    const { win } = boot();
    const big = {};
    for (let i = 0; i < 5000; i++) big[`k${i}`] = { id: i, nested: { deep: i } };
    win.console.error(big);
    const [entry] = win.__devtool_errors.getConsoleErrors().slice(-1);
    expect(entry.message.length).toBeLessThan(2200);
    // The truncation note reports how much was produced before the cut. With
    // the node budget, everything past it collapses to a short placeholder, so
    // far less text is built than a full serialisation would build.
    const produced = Number(/truncated (\d+) chars/.exec(entry.message)[1]) + 2000;
    expect(produced).toBeLessThan(JSON.stringify(big).length * 0.6);
  });

  it('does not route its own connection errors into the page error buffer', () => {
    const { win, sockets } = boot();
    sockets[0].open();
    for (let i = 0; i < 3; i++) {
      const s = sockets.at(-1);
      if (typeof s.onerror === 'function') s.onerror({ type: 'error' });
      s.serverClose();
      win.__devtool_core.reconnect();
    }
    expect(win.__devtool_errors.getConsoleErrors()).toEqual([]);
  });
});
