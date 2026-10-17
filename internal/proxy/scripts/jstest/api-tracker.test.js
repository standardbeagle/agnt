// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// api-tracker.js: the fetch / XHR interceptors observe and never interfere.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

// nativeFetch stands in for the browser's fetch: it records the exact
// arguments it was called with and settles the way the test asks.
function boot(nativeFetch, url = 'http://proxy.test/app/list/') {
  const calls = [];
  rt = loadModules(['frames', 'api-tracker'], {
    url,
    before: (win) => {
      if (nativeFetch !== null) {
        win.fetch = function (...args) { calls.push(args); return nativeFetch(...args); };
      }
      win.Request = class Request {
        constructor(u, init = {}) { this.url = new URL(u, url).href; this.method = init.method || 'GET'; }
      };
    },
  });
  expect(rt.errors).toEqual({});
  return { win: rt.window, calls, recorded: () => rt.window.__devtool_api.getCalls() };
}

const ok = () => Promise.resolve({ status: 200, ok: true, statusText: 'OK' });
const flush = () => new Promise((r) => setTimeout(r, 0));

describe('fetch interception', () => {
  it('records the URL of a URL object, not ".../undefined"', async () => {
    const { win, recorded } = boot(ok);
    await win.fetch(new win.URL('http://proxy.test/api/items?page=2'));
    await flush();
    expect(recorded().map((c) => c.url)).toEqual(['http://proxy.test/api/items?page=2']);
  });

  it('records the URL and method of a Request object', async () => {
    const { win, recorded } = boot(ok);
    await win.fetch(new win.Request('/api/save', { method: 'post' }));
    await flush();
    expect(recorded()[0]).toMatchObject({ url: 'http://proxy.test/api/save', method: 'POST' });
  });

  it('resolves a relative URL against the document URL, not the origin', async () => {
    const { win, recorded } = boot(ok);
    await win.fetch('items.json');
    await flush();
    expect(recorded()[0].url).toBe('http://proxy.test/app/list/items.json');
  });

  it('keeps two different URL-object requests distinct', async () => {
    const { win } = boot(ok);
    for (let i = 0; i < 3; i++) {
      await win.fetch(new win.URL(`http://proxy.test/api/thing/${i}`));
    }
    await flush();
    expect(win.__devtool_api.getRepeatedCalls(30000, 3)).toEqual([]);
  });

  it('passes the page\'s own arguments through untouched', async () => {
    const { win, calls } = boot(ok);
    const target = new win.URL('http://proxy.test/api/x');
    const init = { method: 'PUT' };
    await win.fetch(target, init);
    expect(calls[0][0]).toBe(target);
    expect(calls[0][1]).toBe(init);
  });

  it('returns the native promise itself', () => {
    const native = Promise.resolve({ status: 204, ok: true, statusText: '' });
    const { win } = boot(() => native);
    expect(win.fetch('/x')).toBe(native);
  });

  it('lets a bad input fail the way native fetch fails it', async () => {
    const boom = new TypeError('Failed to parse URL');
    const { win, recorded } = boot(() => Promise.reject(boom));

    // No argument, and a method that is not a string: both used to throw
    // synchronously from the interceptor's own bookkeeping.
    let p;
    expect(() => { p = win.fetch(); }).not.toThrow();
    await expect(p).rejects.toBe(boom);

    expect(() => { p = win.fetch('/x', { method: 42 }); }).not.toThrow();
    await expect(p).rejects.toBe(boom);

    await flush();
    expect(recorded().every((c) => c.status === 0 && c.ok === false)).toBe(true);
  });

  it('records a failed response without consuming the page\'s body', async () => {
    const response = {
      status: 500, ok: false, statusText: 'Server Error',
      clone: () => ({ text: () => Promise.resolve('{"error":"db down"}') }),
    };
    const { win, recorded } = boot(() => Promise.resolve(response));
    const got = await win.fetch('/api/fail');
    expect(got).toBe(response);
    await flush();
    expect(recorded()[0]).toMatchObject({ status: 500, ok: false, errorMessage: 'db down' });
  });

  it('leaves window.fetch alone when the platform has none', () => {
    const { win } = boot(null);
    expect(win.fetch).toBeUndefined();
    expect(typeof win.__devtool_api.getCalls).toBe('function');
  });
});

describe('XHR interception', () => {
  it('does not throw from open() on a non-string method', () => {
    const { win } = boot(ok);
    const xhr = new win.XMLHttpRequest();
    // The native open() decides what an odd method means; ours must not throw
    // a TypeError of its own first.
    let thrown = null;
    try { xhr.open({ toString() { return 'GET'; } }, '/api/x'); } catch (e) { thrown = e; }
    expect(thrown).toBeNull();
  });

  // A page may reuse one XHR object for several requests. Each request must
  // be recorded exactly once, with its own URL.
  it('records each request on a reused XHR exactly once', () => {
    const { win } = bootXHR();
    const xhr = new win.XMLHttpRequest();
    for (const [i, status] of [[1, 200], [2, 404], [3, 200]]) {
      xhr.open('GET', `/api/page/${i}`);
      xhr.send();
      xhr.finish(status);
    }
    expect(win.__devtool_api.getCalls().map((c) => [c.url, c.status])).toEqual([
      ['http://proxy.test/api/page/1', 200],
      ['http://proxy.test/api/page/2', 404],
      ['http://proxy.test/api/page/3', 200],
    ]);
  });
});

// bootXHR replaces the platform XHR with a stand-in the test finishes by hand
// (jsdom's would hit the network). It is installed before api-tracker loads,
// so the tracker wraps it exactly as it wraps the browser's.
function bootXHR() {
  rt = loadModules(['frames', 'api-tracker'], {
    url: 'http://proxy.test/app/',
    before: (win) => {
      class FakeXHR extends win.EventTarget {
        open(method, url) { this.status = 0; this.statusText = ''; this.url = url; }
        send() {}
        finish(status) {
          this.status = status;
          this.responseText = '';
          this.dispatchEvent(new win.Event('loadend'));
        }
      }
      win.XMLHttpRequest = FakeXHR;
    },
  });
  expect(rt.errors).toEqual({});
  return { win: rt.window };
}
