// Covers the event-based injection-settle gate in lib/browser.mjs that replaced
// the old fixed +1200ms sleep. The recorder must not start a browser segment
// until the injected bundle is ACTUALLY live in each frame — per
// .claude/rules/lessons-ssh-transport.md #2, gate on the real runtime signal
// (websocket transport OPEN) plus the subsystem object, never a timer that
// fires earlier than the thing it stands in for.
//
// chromeInjectionReady / contentInjectionReady are written drift-free: the SAME
// function source is serialized into the page by Playwright's waitForFunction
// (where it reads the in-page `window`) and exercised here against a fake window
// (passed as the argument). Testing them here proves the gate keys on the three
// named signals — indicator/toast mounted AND ws connected — not on elapsed time.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {chromeInjectionReady, contentInjectionReady} from '../lib/browser.mjs';

// A window whose ws transport is connected iff `connected` is true.
const win = ({indicator = false, toast = false, connected = false, core = true} = {}) => ({
  __devtool: {
    ...(indicator ? {indicator: {}} : {}),
    ...(toast ? {toast: {show() {}}} : {}),
  },
  ...(core ? {__devtool_core: {isConnected: () => connected}} : {}),
});

// --- chrome frame: indicator mounted AND ws connected ------------------------

test('chromeInjectionReady is false until BOTH indicator mounts and ws connects', () => {
  assert.equal(chromeInjectionReady(win({indicator: false, connected: false})), false);
  assert.equal(chromeInjectionReady(win({indicator: true, connected: false})), false, 'indicator alone is not ready — the old bug: object present, transport not');
  assert.equal(chromeInjectionReady(win({indicator: false, connected: true})), false);
  assert.equal(chromeInjectionReady(win({indicator: true, connected: true})), true);
});

test('chromeInjectionReady does not depend on the toast subsystem (that is the content frame)', () => {
  assert.equal(chromeInjectionReady(win({indicator: true, toast: false, connected: true})), true);
});

// --- content frame: toast subsystem live AND ws connected --------------------

test('contentInjectionReady is false until BOTH toast subsystem is live and ws connects', () => {
  assert.equal(contentInjectionReady(win({toast: false, connected: false})), false);
  assert.equal(contentInjectionReady(win({toast: true, connected: false})), false, 'toast object alone is not ready — transport must be OPEN');
  assert.equal(contentInjectionReady(win({toast: false, connected: true})), false);
  assert.equal(contentInjectionReady(win({toast: true, connected: true})), true);
});

// --- both predicates fail safe on absent / half-installed bundle -------------

test('both predicates are false when the core transport API is absent', () => {
  assert.equal(chromeInjectionReady(win({indicator: true, connected: true, core: false})), false);
  assert.equal(contentInjectionReady(win({toast: true, connected: true, core: false})), false);
});

test('both predicates are false on a null/empty window (never throw)', () => {
  assert.equal(chromeInjectionReady(null), false);
  assert.equal(chromeInjectionReady({}), false);
  assert.equal(contentInjectionReady(null), false);
  assert.equal(contentInjectionReady({}), false);
});
