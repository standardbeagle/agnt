// Covers validateDemoSpec — the pure, dependency-free demo.json validator that
// `make demo-check` runs over every demos/*/demo.json. Each malformation class
// named in the task's acceptance criteria has its own test proving it fails, and
// a well-formed spec proves it passes. No filesystem, no ffmpeg, no chromium.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {validateDemoSpec} from '../lib/schema.mjs';

// A minimal well-formed spec reused (and locally mutated) across the failure cases.
const valid = () => ({
  name: 'sample',
  viewport: {width: 1440, height: 900},
  fps: 25,
  segments: [
    {id: 'card-intro', type: 'card', dur: 4, kicker: 'k', title: 't', sub: 's'},
    {id: 'clip', type: 'browser', script: './segments/clip.mjs',
      keep: [['start', 'mark:shown+1.2'], ['mark:fixed-0.5', 'end']]},
    {id: 'term', type: 'cli', tape: ['Type "ls"', 'Enter']},
  ],
});

test('a well-formed spec passes with no errors', () => {
  const r = validateDemoSpec(valid());
  assert.equal(r.ok, true, JSON.stringify(r.errors));
  assert.deepEqual(r.errors, []);
});

test('unknown segment type fails', () => {
  const s = valid();
  s.segments[1].type = 'teleport';
  const r = validateDemoSpec(s);
  assert.equal(r.ok, false);
  assert.ok(r.errors.some((e) => /unknown segment type/i.test(e) && /teleport/.test(e)),
    `expected an unknown-segment-type error, got ${JSON.stringify(r.errors)}`);
});

test('bad keep endpoint token fails', () => {
  const s = valid();
  s.segments[1].keep = [['start', 'marker:oops']]; // "marker:" is not the "mark:" grammar
  const r = validateDemoSpec(s);
  assert.equal(r.ok, false);
  assert.ok(r.errors.some((e) => /keep endpoint/i.test(e) && /marker:oops/.test(e)),
    `expected a bad-keep-endpoint error, got ${JSON.stringify(r.errors)}`);
});

test('narration anchor referencing a missing segment id fails', () => {
  const s = valid();
  s.narration = {
    voice: 'en-US-AriaNeural',
    segments: [{id: 'n1', at: 'ghost-seg+0.5', text: 'hello'}],
  };
  const r = validateDemoSpec(s);
  assert.equal(r.ok, false);
  assert.ok(r.errors.some((e) => /narration anchor/i.test(e) && /ghost-seg/.test(e)),
    `expected a missing-anchor-segment error, got ${JSON.stringify(r.errors)}`);
});

test('a narration anchor that DOES reference an existing segment passes', () => {
  const s = valid();
  s.narration = {
    voice: 'en-US-AriaNeural',
    segments: [{id: 'n1', at: 'clip+0.5', text: 'hello'}],
  };
  const r = validateDemoSpec(s);
  assert.equal(r.ok, true, JSON.stringify(r.errors));
});

test('bad brand shape fails (missing image)', () => {
  const s = valid();
  s.brand = {position: 'top-right'}; // no image
  const r = validateDemoSpec(s);
  assert.equal(r.ok, false);
  assert.ok(r.errors.some((e) => /brand/i.test(e) && /image/i.test(e)),
    `expected a brand.image error, got ${JSON.stringify(r.errors)}`);
});

test('bad brand shape fails (invalid position enum)', () => {
  const s = valid();
  s.brand = {image: './logo.png', position: 'middle-center'};
  const r = validateDemoSpec(s);
  assert.equal(r.ok, false);
  assert.ok(r.errors.some((e) => /brand\.position/i.test(e) && /middle-center/.test(e)),
    `expected a brand.position error, got ${JSON.stringify(r.errors)}`);
});

test('a well-formed brand passes', () => {
  const s = valid();
  s.brand = {image: './logo.png', position: 'bottom-left', width: 220, opacity: 0.85};
  const r = validateDemoSpec(s);
  assert.equal(r.ok, true, JSON.stringify(r.errors));
});

test('structural failures are collected, not thrown', () => {
  // A grab-bag of shape errors must all be reported at once (no early throw).
  const r = validateDemoSpec({segments: 'not-an-array'});
  assert.equal(r.ok, false);
  assert.ok(r.errors.length >= 2, JSON.stringify(r.errors));
  assert.ok(r.errors.some((e) => /name/.test(e)));
  assert.ok(r.errors.some((e) => /segments/.test(e)));
});

test('null / non-object input fails loud without throwing', () => {
  assert.equal(validateDemoSpec(null).ok, false);
  assert.equal(validateDemoSpec(42).ok, false);
});
