// Covers the narration/edge-tts gating decision in demo.mjs. The decision is
// extracted as a pure function so these tests inject edge-tts availability as a
// boolean and never depend on the real PATH.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {resolveNarrationMode} from '../demo.mjs';

test('narration + edge-tts absent → hard abort naming edge-tts and the install command', () => {
  const r = resolveNarrationMode({narration: {voice: 'en-US'}}, false);
  assert.equal(r.mode, 'abort');
  assert.match(r.error, /edge-tts/);
  assert.match(r.error, /pip install edge-tts/);
});

test('narration.optional:true + edge-tts absent → warn-drop (today\'s silent assembly)', () => {
  const r = resolveNarrationMode({narration: {optional: true, voice: 'en-US'}}, false);
  assert.equal(r.mode, 'warn-drop');
  assert.equal(r.error, undefined);
});

test('narration present + edge-tts available → assemble with VO', () => {
  const r = resolveNarrationMode({narration: {voice: 'en-US'}}, true);
  assert.equal(r.mode, 'assemble');
});

test('no narration → assemble, unaffected by edge-tts availability', () => {
  assert.equal(resolveNarrationMode({}, false).mode, 'assemble');
  assert.equal(resolveNarrationMode({}, true).mode, 'assemble');
});

test('optional must be strictly true — any other value still aborts when edge-tts absent', () => {
  assert.equal(resolveNarrationMode({narration: {optional: false}}, false).mode, 'abort');
  assert.equal(resolveNarrationMode({narration: {optional: 'yes'}}, false).mode, 'abort');
  assert.equal(resolveNarrationMode({narration: {optional: 1}}, false).mode, 'abort');
});
