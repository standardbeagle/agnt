// Evaluates the SHIPPED modules in the bundle's real load order inside a fresh
// jsdom window.
//
// The order is read from moduleOrder in embed.go, not copied here, so a module
// added to (or reordered in) the bundle is picked up by these tests without an
// edit. Each file is evaluated verbatim — the same bytes the proxy injects.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';

// AGNT_JSTEST_SCRIPTS_DIR points the loader at another copy of the scripts —
// used to mutation-check a test against an older tree (the test must FAIL
// there). Unset, it is the directory these tests ship beside.
export const SCRIPTS_DIR = process.env.AGNT_JSTEST_SCRIPTS_DIR ||
  join(dirname(fileURLToPath(import.meta.url)), '..');

export function shipped(name) {
  return readFileSync(join(SCRIPTS_DIR, name), 'utf8');
}

// moduleOrder returns the module names in bundle order, parsed from embed.go.
export function moduleOrder() {
  const embed = shipped('embed.go');
  const start = embed.indexOf('var moduleOrder = []moduleEntry{');
  const end = embed.indexOf('\n}\n', start);
  if (start === -1 || end === -1) throw new Error('moduleOrder not found in embed.go');
  const names = [...embed.slice(start, end).matchAll(/^\t\{"([a-z0-9-]+)",/gm)].map((m) => m[1]);
  if (names.length < 40) throw new Error(`moduleOrder parse found only ${names.length} modules`);
  return names;
}

// FakeWebSocket records every socket the page opens and every frame it sends.
// Sockets start CONNECTING; the test drives open()/serverClose() itself.
export function fakeWebSocket() {
  const sockets = [];
  class FakeWebSocket {
    static CONNECTING = 0;
    static OPEN = 1;
    static CLOSING = 2;
    static CLOSED = 3;

    constructor(url) {
      this.url = url;
      this.readyState = FakeWebSocket.CONNECTING;
      this.sent = [];
      sockets.push(this);
    }

    send(payload) {
      this.sent.push(typeof payload === 'string' ? JSON.parse(payload) : payload);
    }

    // close() mirrors a real socket: the close event is delivered to whatever
    // handler is installed, even when the page closed it deliberately.
    close() {
      if (this.readyState === FakeWebSocket.CLOSED) return;
      this.readyState = FakeWebSocket.CLOSED;
      if (typeof this.onclose === 'function') this.onclose({ code: 1000, reason: '', wasClean: true });
    }

    open() {
      this.readyState = FakeWebSocket.OPEN;
      if (typeof this.onopen === 'function') this.onopen({});
    }

    serverClose() {
      if (this.readyState === FakeWebSocket.CLOSED) return;
      this.readyState = FakeWebSocket.CLOSED;
      if (typeof this.onclose === 'function') this.onclose({ code: 1006, reason: '', wasClean: false });
    }
  }
  return { FakeWebSocket, sockets };
}

// loadModules evaluates the named modules (bundle order is the caller's job
// when it passes a subset) and returns the window plus per-module load errors.
export function loadModules(names, opts = {}) {
  const dom = new JSDOM(opts.html || '<!doctype html><html><head></head><body></body></html>', {
    runScripts: 'outside-only',
    pretendToBeVisual: true,
    url: opts.url || 'http://proxy.test/page',
  });
  const { FakeWebSocket, sockets } = fakeWebSocket();
  const win = dom.window;
  win.WebSocket = FakeWebSocket;
  if (opts.role) win.__devtool_role = opts.role;
  if (opts.before) opts.before(win);

  const errors = {};
  for (const name of names) {
    try {
      win.eval(shipped(`${name}.js`));
    } catch (e) {
      errors[name] = e;
    }
  }
  return { window: win, sockets, errors, close: () => win.close() };
}

// loadBundle evaluates every module in the bundle, in bundle order.
export function loadBundle(opts = {}) {
  return loadModules(moduleOrder(), opts);
}
