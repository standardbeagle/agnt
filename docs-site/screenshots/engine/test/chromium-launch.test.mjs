// Pins the two launch corrections that keep headless Chromium producing
// compositor frames on GPU-less hosts (see CHROMIUM_LAUNCH_OPTIONS in util.mjs).
// Without them a recorded take flatlines at ~24 frames regardless of length
// and Page.captureScreenshot never returns. The engine's own launch sites are
// checked by source so a stray `chromium.launch()` cannot regress it silently.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {CHROMIUM_LAUNCH_OPTIONS} from '../lib/util.mjs';

const screenshotsDir = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const launchSites = ['engine/lib/browser.mjs', 'engine/lib/assemble.mjs',
  'narrate-assemble.mjs', 'record-live.mjs', 'capture.mjs'];

test('launch options drop swiftshader and disable the GPU', () => {
  assert.ok(CHROMIUM_LAUNCH_OPTIONS.ignoreDefaultArgs.includes('--enable-unsafe-swiftshader'));
  assert.ok(CHROMIUM_LAUNCH_OPTIONS.args.includes('--disable-gpu'));
  assert.ok(Object.isFrozen(CHROMIUM_LAUNCH_OPTIONS));
});

test('every chromium.launch in the screenshots tree passes the shared options', () => {
  for (const rel of launchSites) {
    const src = fs.readFileSync(path.join(screenshotsDir, rel), 'utf8');
    const launches = src.match(/chromium\.launch\([^)]*\)/g) || [];
    assert.ok(launches.length > 0, `${rel}: expected a chromium.launch call`);
    for (const call of launches) {
      assert.equal(call, 'chromium.launch(CHROMIUM_LAUNCH_OPTIONS)', `${rel}: ${call}`);
    }
  }
});
