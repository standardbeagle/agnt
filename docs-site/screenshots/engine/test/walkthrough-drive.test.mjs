// Walkthrough driving: the snippet the demo engine ships over PROXY EXEC must
// be the SAME one the MCP `walkthrough` tool builds (internal/tools/
// walkthrough_tools.go buildWalkthroughExec), the in-page step/panel predicates
// must be verifiable in Node (they are serialized into the browser by
// Playwright), and the mark/anchor grammars must accept the `step:<n>` marks the
// driver drops.
import test from 'node:test';
import assert from 'node:assert/strict';

import {buildWalkthroughCall} from '../lib/daemon.mjs';
import {walkthroughAtStep, walkthroughPanelVisible} from '../lib/browser.mjs';
import {endpointSec, narrationAnchorSec} from '../lib/assemble.mjs';
import {validateDemoSpec} from '../lib/schema.mjs';

test('buildWalkthroughCall mirrors the MCP tool exec snippet', () => {
  const script = {id: 'tour', title: 'T', steps: [{title: 'a', advance: {type: 'auto', ms: 1000}}]};
  const start = buildWalkthroughCall('start', {script});
  // Same guard + same entry point as buildWalkthroughExec in Go.
  assert.match(start, /window\.__devtool && window\.__devtool\.walkthrough/);
  assert.match(start, /return JSON\.stringify\(w\.start\(/);
  assert.match(start, /\{mode:"auto"\}/);
  assert.ok(start.includes(JSON.stringify(script)));

  assert.match(buildWalkthroughCall('start', {scriptId: 'tour', mode: 'manual'}),
    /w\.start\("tour", \{mode:"manual"\}\)/);
  for (const a of ['stop', 'next', 'prev', 'play', 'pause', 'status', 'list']) {
    assert.match(buildWalkthroughCall(a), new RegExp(`w\\.${a}\\(\\)`));
  }
});

test('buildWalkthroughCall fails loud on bad input', () => {
  assert.throws(() => buildWalkthroughCall('start', {}), /script or scriptId/);
  assert.throws(() => buildWalkthroughCall('start', {scriptId: 'x', mode: 'weird'}), /invalid mode/);
  assert.throws(() => buildWalkthroughCall('load', {}), /script required/);
  assert.throws(() => buildWalkthroughCall('teleport'), /unknown action/);
});

test('walkthroughAtStep reads the host-frame walkthrough status', () => {
  const win = (status) => ({__devtool_walkthrough_host: {status: () => status}});
  assert.equal(walkthroughAtStep(0, win({running: true, stepIndex: 0})), true);
  assert.equal(walkthroughAtStep(1, win({running: true, stepIndex: 0})), false);
  assert.equal(walkthroughAtStep(0, win({running: false, registered: []})), false);
  assert.equal(walkthroughAtStep(0, {}), false);
  assert.equal(walkthroughAtStep(0, null), false);
});

test('walkthroughPanelVisible requires a rendered, non-empty panel', () => {
  const mk = (panel) => ({__devtoolGetMountRoot: () => ({querySelector: (s) => (s === '#__wt_panel' ? panel : null)})});
  assert.equal(walkthroughPanelVisible(mk({textContent: 'Step 1/3'})), true);
  assert.equal(walkthroughPanelVisible(mk({textContent: '   '})), false);
  assert.equal(walkthroughPanelVisible(mk(null)), false);
  assert.equal(walkthroughPanelVisible(null), false);
});

test('keep endpoints accept step:<n> marks', () => {
  const marks = [{name: 'step:1', tMs: 1000}, {name: 'step:2', tMs: 4500}, {name: 'end', tMs: 9000}];
  assert.equal(endpointSec('mark:step:1', marks, 9), 1);
  assert.equal(endpointSec('mark:step:2+0.5', marks, 9), 5);
  assert.equal(endpointSec('mark:step:2-0.5', marks, 9), 4);
  assert.throws(() => endpointSec('mark:step:9', marks, 9), /missing mark/);
  assert.throws(() => endpointSec('mark:', marks, 9), /bad keep endpoint/);
});

test('narration anchors resolve against a step mark', () => {
  const seg = {start: 10, dur: 8};
  const marks = [{name: 'step:2', tMs: 3000}];
  const specSeg = {id: 'tour', type: 'browser'};
  assert.equal(narrationAnchorSec('tour+mark:step:2', 2, seg, specSeg, marks), 13);
  assert.equal(narrationAnchorSec('tour+mark:step:2+0.5', 2, seg, specSeg, marks), 13.5);
  // Existing grammar keeps working.
  assert.equal(narrationAnchorSec('tour', 2, seg, specSeg, marks), 10.4);
  assert.equal(narrationAnchorSec('tour+1.5', 2, seg, specSeg, marks), 11.5);
  assert.equal(narrationAnchorSec('tour+end', 2, seg, specSeg, marks), 15);
  // Fail loud rather than silently misalign: mark times are take-relative, so a
  // spliced or trimmed segment has no honest mapping.
  assert.throws(() => narrationAnchorSec('tour+mark:step:2', 2, seg, {...specSeg, keep: [['start', 'end']]}, marks),
    /keep/);
  assert.throws(() => narrationAnchorSec('tour+mark:step:2', 2, seg, {...specSeg, trimSeconds: 3}, marks),
    /trimSeconds/);
  assert.throws(() => narrationAnchorSec('tour+mark:step:9', 2, seg, specSeg, marks), /missing mark/);
});

test('schema accepts step-mark keep endpoints and narration anchors', () => {
  const spec = {
    name: 'wt',
    segments: [{id: 'tour', type: 'browser', script: './s.mjs', keep: [['mark:step:1', 'mark:step:3+1.0']]}],
    narration: {segments: [{id: 'n1', at: 'tour+mark:step:2', text: 'hi'}]},
  };
  assert.deepEqual(validateDemoSpec(spec), {ok: true, errors: []});

  const bad = {...spec, narration: {segments: [{id: 'n1', at: 'nope+mark:step:2', text: 'hi'}]}};
  assert.equal(validateDemoSpec(bad).ok, false);
});
