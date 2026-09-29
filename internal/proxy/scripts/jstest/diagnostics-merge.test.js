import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedDiagnostics = readFileSync(join(scriptsDir, 'diagnostics.js'), 'utf8');

// core.js pre-installs window.__devtool_diagnostics with a buffer-scoped API
// (getHistory, reconnect, ...) before diagnostics.js loads in the content
// frame (embed.go moduleOrder: diagnostics depends on core). diagnostics.js
// used to overwrite that object wholesale, silently dropping core's API
// (notably reconnect() and the proxy_diagnostic buffer) the moment it loaded.
// This stubs core's half of the contract directly rather than loading all of
// core.js, so the test stays focused on the merge behaviour diagnostics.js
// itself owns.
function runtime(withCoreDiagnostics) {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  dom.window.__devtool_utils = { generateSelector: () => '', resolveElement: () => null, getRect: () => null };
  if (withCoreDiagnostics) {
    dom.window.__devtool_diagnostics = {
      getHistory: () => ['core-history'],
      getConnectionState: () => ({ connected: true }),
      reconnect: () => 'core-reconnect-called',
      clear: () => 'core-buffer-cleared',
    };
  }
  dom.window.eval(shippedDiagnostics);
  return dom.window;
}

describe('shipped diagnostics module merge onto core.js', () => {
  it('keeps core.js buffer methods (getHistory, getConnectionState, reconnect) alongside its own', () => {
    const window = runtime(true);
    const d = window.__devtool_diagnostics;

    expect(d.getHistory()).toEqual(['core-history']);
    expect(d.getConnectionState()).toEqual({ connected: true });
    expect(d.reconnect()).toBe('core-reconnect-called');

    // diagnostics.js's own surface is still present.
    expect(typeof d.outlineAll).toBe('function');
    expect(typeof d.diagnoseClick).toBe('function');
  });

  it('renames the colliding core.js clear() to clearHistory instead of dropping it', () => {
    const window = runtime(true);
    const d = window.__devtool_diagnostics;

    // The name "clear" now belongs to diagnostics.js's own overlay-mode clear
    // (clear(mode)), never core's buffer clear.
    expect(d.clearHistory()).toBe('core-buffer-cleared');
    expect(d.clear).not.toBe(undefined);
    // diagnostics.js's clear(mode) returns a {success, cleared} shape, not the
    // string core's clear() returned.
    expect(d.clear('outline')).toMatchObject({ success: true });
  });

  it('works even when core.js has not installed a __devtool_diagnostics yet', () => {
    const window = runtime(false);
    const d = window.__devtool_diagnostics;

    expect(typeof d.outlineAll).toBe('function');
    expect(d.clearHistory).toBe(undefined);
    expect(d.clear('outline')).toMatchObject({ success: true });
  });
});
