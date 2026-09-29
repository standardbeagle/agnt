import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { JSDOM } from 'jsdom';
import { describe, expect, it } from 'vitest';

const scriptsDir = join(dirname(fileURLToPath(import.meta.url)), '..');
const shippedBridge = readFileSync(join(scriptsDir, 'indicator-bridge.js'), 'utf8');

// The bridge is the content-side half of the floating indicator: it forwards
// calls made against window.__devtool_indicator in a content frame to the
// real indicator living in the parent chrome shell (frames.js's shellExport).
// A real nested iframe is not needed here — the bridge only ever calls
// frameContext.shellExport(), so stubbing __devtool_context is sufficient and
// keeps this at the DOM-shaped jsdom tier rather than the real-Chrome tier.
function runtime(parentIndicator) {
  const dom = new JSDOM('<!doctype html><body></body>', { runScripts: 'outside-only' });
  dom.window.__devtool_context = {
    isTopLevel: () => false,
    shellExport: (name) => (name === '__devtool_indicator' ? parentIndicator : null),
  };
  dom.window.eval(shippedBridge);
  return dom.window;
}

describe('shipped indicator bridge', () => {
  it('forwards setMessage and switchTab to the parent shell indicator', () => {
    const calls = [];
    const parentIndicator = {
      setMessage(text) { calls.push(['setMessage', text]); return 'ok-message'; },
      switchTab(tab) { calls.push(['switchTab', tab]); return 'ok-tab'; },
    };
    const window = runtime(parentIndicator);

    expect(window.__devtool_indicator.setMessage('hello')).toBe('ok-message');
    expect(window.__devtool_indicator.switchTab('compose')).toBe('ok-tab');
    expect(calls).toEqual([
      ['setMessage', 'hello'],
      ['switchTab', 'compose'],
    ]);
  });

  it('is a safe no-op when no parent indicator is reachable', () => {
    const window = runtime(null);
    expect(() => window.__devtool_indicator.setMessage('hello')).not.toThrow();
    expect(() => window.__devtool_indicator.switchTab('compose')).not.toThrow();
    expect(window.__devtool_indicator.setMessage('hello')).toBeUndefined();
  });
});
