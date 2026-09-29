import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedFrames = readFileSync(join(scriptsDir, 'frames.js'), 'utf8');
const shippedCore = readFileSync(join(scriptsDir, 'core.js'), 'utf8');

// Reconnect used to give up for good after MAX_RECONNECT_ATTEMPTS=5 (~15s),
// latching the indicator dot "disconnected" until a visitor manually called
// reconnect(). This exercises the fix: unbounded exponential backoff capped
// at a max interval, plus immediate retry on 'online'/'visibilitychange'.
function runtime() {
  const dom = new JSDOM('<!doctype html><body></body>', {
    runScripts: 'outside-only',
    url: 'http://proxy.test/page?__devtool_frame=content-a',
  });
  const sockets = [];

  class FakeWebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor() {
      this.readyState = FakeWebSocket.OPEN;
      sockets.push(this);
    }
    send() {}
    close() { this.readyState = FakeWebSocket.CLOSED; }
  }

  dom.window.WebSocket = FakeWebSocket;
  dom.window.__devtool_role = 'content';
  dom.window.__devtool_frame_id = 'content-a';
  dom.window.eval(shippedFrames);
  dom.window.eval(shippedCore);

  return { window: dom.window, sockets };
}

// dropSocket simulates the current live socket going away: marks it closed
// and fires its onclose handler, the way a real dropped connection would.
function dropSocket(sockets) {
  const s = sockets[sockets.length - 1];
  s.readyState = 3; // CLOSED
  s.onclose({ code: 1006, reason: '', wasClean: false });
}

describe('shipped core.js reconnect backoff', () => {
  beforeEach(() => { vi.useFakeTimers(); });
  afterEach(() => { vi.useRealTimers(); });

  it('never stops scheduling a retry, and the interval is capped rather than unbounded', () => {
    const { sockets } = runtime();
    expect(sockets.length).toBe(1); // initial connect() at init

    // Attempt 1: 1000ms.
    dropSocket(sockets);
    vi.advanceTimersByTime(999);
    expect(sockets.length).toBe(1);
    vi.advanceTimersByTime(1);
    expect(sockets.length).toBe(2);

    // Attempt 2: 2000ms.
    dropSocket(sockets);
    vi.advanceTimersByTime(1999);
    expect(sockets.length).toBe(2);
    vi.advanceTimersByTime(1);
    expect(sockets.length).toBe(3);

    // Drive well past where a MAX_RECONNECT_ATTEMPTS=5 cap would have given
    // up (previously ~15s total, no further sockets ever created again).
    for (let i = 0; i < 10; i++) {
      dropSocket(sockets);
      vi.advanceTimersByTime(30000); // the capped ceiling is always enough
    }
    // Still retrying: every drop produced another socket, none were dropped
    // silently past attempt 5.
    expect(sockets.length).toBe(13);

    // The interval is capped: even at a very high attempt count the wait
    // before the next socket never exceeds 30s.
    dropSocket(sockets);
    vi.advanceTimersByTime(30000);
    expect(sockets.length).toBe(14);
  });

  it('retries immediately on "online" without waiting out the backoff', () => {
    const { window, sockets } = runtime();
    dropSocket(sockets); // arms a long-ish backoff timer
    expect(sockets.length).toBe(1);

    window.dispatchEvent(new window.Event('online'));
    expect(sockets.length).toBe(2); // reconnected immediately, no timer wait
  });

  it('retries immediately on visibilitychange -> visible', () => {
    const { window, sockets } = runtime();
    dropSocket(sockets);
    expect(sockets.length).toBe(1);

    Object.defineProperty(window.document, 'visibilityState', { value: 'visible', configurable: true });
    window.document.dispatchEvent(new window.Event('visibilitychange'));
    expect(sockets.length).toBe(2);
  });

  it('does not reconnect on visibilitychange while still hidden', () => {
    const { window, sockets } = runtime();
    dropSocket(sockets);
    expect(sockets.length).toBe(1);

    Object.defineProperty(window.document, 'visibilityState', { value: 'hidden', configurable: true });
    window.document.dispatchEvent(new window.Event('visibilitychange'));
    expect(sockets.length).toBe(1);
  });

  it('is a no-op while already connected', () => {
    const { window, sockets } = runtime();
    expect(sockets.length).toBe(1); // freshly connected, readyState OPEN

    window.dispatchEvent(new window.Event('online'));
    expect(sockets.length).toBe(1); // no redundant reconnect
  });
});
