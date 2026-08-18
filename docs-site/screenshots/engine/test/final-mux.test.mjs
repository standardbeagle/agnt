// Covers buildFinalMuxArgs — the pure final-mux filter-graph constructor in
// lib/assemble.mjs. It returns the ffmpeg argv (the args `ff` gets after its
// `-y -v error` prefix) so we assert the constructed command without ever
// spawning ffmpeg. The brand overlay (Part A) and the EBU R128 loudnorm
// (Part B) are both filters folded into the single final invocation.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {buildFinalMuxArgs} from '../lib/assemble.mjs';

const fc = (r) => r.argv[r.argv.indexOf('-filter_complex') + 1];
const alwaysExists = () => true;
const NARR = [{mp3: '/w/vo-n1.mp3', at: 1.2}, {mp3: '/w/vo-n2.mp3', at: 5.0}];

// --- refactor parity: no-brand paths reproduce today's exact behavior --------

test('no brand + silent path → byte-identical stream-copy (no encode)', () => {
  const r = buildFinalMuxArgs(
    {name: 'd'},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo'});
  assert.deepEqual(r.argv, ['-i', '/w/visual.webm', '-c', 'copy', '/o/d.webm']);
  assert.equal(r.encodeCount, 0);
  assert.equal(r.path, 'silent');
});

test('no brand + narrated path → exactly one video encode, one output file', () => {
  const r = buildFinalMuxArgs(
    {name: 'd'},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: '/w/captions.srt'},
    {voiced: NARR, totalDur: 12.5, view: {fps: 25}, demoDir: '/demo'});
  assert.equal(r.encodeCount, 1);
  // one video-encode codec directive, and it is not a copy
  assert.equal(r.argv.filter((a) => a === '-c:v').length, 1);
  assert.ok(!r.argv.includes('copy'));
  // one output file = the last arg; no second output pass
  assert.equal(r.argv[r.argv.length - 1], '/o/d.webm');
  assert.equal(r.argv.filter((a) => a === '/o/d.webm').length, 1);
  // subtitle burn preserved, audio still delayed + amixed
  assert.match(fc(r), /subtitles=\/w\/captions\.srt/);
  assert.match(fc(r), /amix=inputs=2:normalize=0/);
});

// --- Part B: EBU R128 loudnorm on the narration chain, silent path untouched --

test('narrated filter graph applies EBU R128 loudnorm AFTER amix', () => {
  const r = buildFinalMuxArgs(
    {name: 'd'},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: '/w/captions.srt'},
    {voiced: NARR, totalDur: 12.5, view: {fps: 25}, demoDir: '/demo'});
  const g = fc(r);
  assert.match(g, /loudnorm=I=-16:TP=-1\.5:LRA=11/);
  // loudnorm must sit downstream of amix on the [voa] chain, not before it
  assert.ok(g.indexOf('amix=') < g.indexOf('loudnorm='), 'loudnorm should follow amix');
  assert.match(g, /amix=inputs=2:normalize=0,loudnorm=I=-16:TP=-1\.5:LRA=11\[voa\]/);
});

test('loudnorm never touches the silent (no-narration) path', () => {
  const r = buildFinalMuxArgs(
    {name: 'd'},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo'});
  assert.ok(!r.argv.join(' ').includes('loudnorm'));
});

// --- Part A: brand overlay folded into the SAME final invocation -------------

test('brand + narrated → overlay in the same graph, still exactly one encode', () => {
  const r = buildFinalMuxArgs(
    {name: 'd', brand: {image: 'brand.png'}},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: '/w/captions.srt'},
    {voiced: NARR, totalDur: 12.5, view: {fps: 25}, demoDir: '/demo', fileExists: alwaysExists});
  assert.equal(r.encodeCount, 1);
  assert.equal(r.path, 'narrated-brand');
  const g = fc(r);
  assert.match(g, /overlay=/);
  // loudnorm survives alongside the overlay
  assert.match(g, /loudnorm=I=-16/);
  // brand input is appended AFTER the mp3s so audio indices stay [1:a],[2:a]
  assert.match(g, /\[1:a\]adelay/);
  assert.match(g, /\[2:a\]adelay/);
  assert.match(g, /\[3:v\]scale=220/); // input 3 = brand (0 silent, 1-2 mp3)
  // overlay consumes the subtitle-burned frames, not the raw [0:v]
  assert.match(g, /subtitles=.*\[subbed\]/);
  assert.match(g, /\[subbed\]\[logo\]overlay=/);
  // still one output, one -c:v
  assert.equal(r.argv.filter((a) => a === '-c:v').length, 1);
  assert.equal(r.argv[r.argv.length - 1], '/o/d.webm');
});

test('brand + silent → one necessary encode with overlay (no more stream-copy)', () => {
  const r = buildFinalMuxArgs(
    {name: 'd', brand: {image: 'brand.png'}},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo', fileExists: alwaysExists});
  assert.equal(r.encodeCount, 1);
  assert.equal(r.path, 'silent-brand');
  assert.ok(!r.argv.includes('copy'));
  const g = fc(r);
  assert.match(g, /\[1:v\]scale=220/); // input 1 = brand (0 silent, no mp3)
  assert.match(g, /\[0:v\]\[logo\]overlay=/);
  assert.equal(r.argv.filter((a) => a === '-c:v').length, 1);
});

test('brand defaults: width 220, opacity 0.85, top-right corner', () => {
  const r = buildFinalMuxArgs(
    {name: 'd', brand: {image: 'brand.png'}},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo', fileExists: alwaysExists});
  const g = fc(r);
  assert.match(g, /scale=220:-1/);
  assert.match(g, /colorchannelmixer=aa=0\.85/);
  assert.match(g, /overlay=main_w-overlay_w-\d+:\d+/); // top-right
});

test('brand overrides: width/opacity/position honored', () => {
  const r = buildFinalMuxArgs(
    {name: 'd', brand: {image: 'logo.png', width: 320, opacity: 0.5, position: 'bottom-left'}},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo', fileExists: alwaysExists});
  const g = fc(r);
  assert.match(g, /scale=320:-1/);
  assert.match(g, /colorchannelmixer=aa=0\.5/);
  assert.match(g, /overlay=\d+:main_h-overlay_h-\d+/); // bottom-left
});

test('brand.image resolves relative to the demo dir', () => {
  const r = buildFinalMuxArgs(
    {name: 'd', brand: {image: 'assets/brand.png'}},
    {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
    {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo/mine', fileExists: alwaysExists});
  assert.ok(r.argv.includes('/demo/mine/assets/brand.png'));
});

test('brand.image missing on disk → hard error naming the resolved path', () => {
  assert.throws(
    () => buildFinalMuxArgs(
      {name: 'd', brand: {image: 'nope.png'}},
      {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
      {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo', fileExists: () => false}),
    /\/demo\/nope\.png/);
});

test('invalid brand.position → hard error listing the valid corners', () => {
  assert.throws(
    () => buildFinalMuxArgs(
      {name: 'd', brand: {image: 'brand.png', position: 'middle'}},
      {silent: '/w/visual.webm', out: '/o/d.webm', srtPath: null},
      {voiced: [], totalDur: 10, view: {fps: 25}, demoDir: '/demo', fileExists: alwaysExists}),
    /position/);
});
