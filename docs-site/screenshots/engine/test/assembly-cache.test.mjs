// Covers the content-keyed assembly cache: the pure key functions in
// lib/util.mjs (ttsKey / mezzKey / fileFastKey) and the cached-generation seams
// in lib/assemble.mjs (cachedTTS / cachedMezz). The generation work — edge-tts
// and the splice+normalize ffmpeg encode — is injected as a counting `run` seam,
// so these tests assert "a second identical run does zero work" without ever
// invoking edge-tts or ffmpeg. An in-memory fake fs (a Set of existing paths)
// stands in for the on-disk cache.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import {ttsKey, mezzKey} from '../lib/util.mjs';
import {cachedTTS, cachedMezz} from '../lib/assemble.mjs';

// --- pure key functions: any input change changes the key --------------------

test('ttsKey is stable for identical inputs', () => {
  assert.equal(ttsKey('en-US', '+0%', 'hello'), ttsKey('en-US', '+0%', 'hello'));
});

test('ttsKey changes when voice, rate, or text changes — and covers each', () => {
  const base = ttsKey('en-US', '+0%', 'hello');
  assert.notEqual(base, ttsKey('en-GB', '+0%', 'hello')); // voice
  assert.notEqual(base, ttsKey('en-US', '+5%', 'hello')); // rate
  assert.notEqual(base, ttsKey('en-US', '+0%', 'hey'));   // text
});

test('ttsKey resists field-boundary collisions', () => {
  // A naive `${voice}|${rate}|${text}` join collides here; the NUL-separated
  // hash must not.
  assert.notEqual(ttsKey('a|b', 'c', 'd'), ttsKey('a', 'b|c', 'd'));
});

const VIEW = {width: 1440, height: 900, fps: 25};
const SEG = {id: 's1', keep: [['start', 'end']], trimSeconds: 4};

test('mezzKey is stable for identical inputs', () => {
  assert.equal(mezzKey('100:5', SEG, VIEW), mezzKey('100:5', SEG, VIEW));
});

test('mezzKey covers take identity, keep, trim, and every viewport field', () => {
  const base = mezzKey('100:5', SEG, VIEW);
  assert.notEqual(base, mezzKey('101:5', SEG, VIEW)); // take identity (fast-key)
  assert.notEqual(base, mezzKey('100:5', {...SEG, keep: [['start', 'mark:x']]}, VIEW)); // keep
  assert.notEqual(base, mezzKey('100:5', {...SEG, trimSeconds: 3}, VIEW)); // trim
  assert.notEqual(base, mezzKey('100:5', SEG, {...VIEW, width: 1280}));  // viewport w
  assert.notEqual(base, mezzKey('100:5', SEG, {...VIEW, height: 720}));  // viewport h
  assert.notEqual(base, mezzKey('100:5', SEG, {...VIEW, fps: 30}));      // fps
});

// --- fake fs: a Set of paths that "exist"; copy adds the dst path ------------

const makeFakeFs = () => {
  const files = new Set();
  return {
    files,
    existsSync: (p) => files.has(p),
    copyFile: (src, dst) => {
      if (!files.has(src)) throw new Error('ENOENT ' + src);
      files.add(dst);
    },
  };
};

const CACHE = '/out/.work/cache';

// --- cachedTTS: second identical call performs zero edge-tts invocations -----

test('cachedTTS: first call misses (runs edge-tts), second call hits (zero runs)', () => {
  const fake = makeFakeFs();
  const logs = [];
  let runs = 0;
  const run = (voice, rate, spoken, mp3Out, srtOut) => {
    runs++;
    fake.files.add(mp3Out);
    fake.files.add(srtOut);
  };
  const io = {cacheDir: CACHE, mp3Out: '/w/vo-n1.mp3', srtOut: '/w/vo-n1.srt'};
  const deps = {run, existsSync: fake.existsSync, copyFile: fake.copyFile, log: (m) => logs.push(m)};

  cachedTTS('en-US', '+0%', 'hello world', io, deps);
  assert.equal(runs, 1);
  assert.ok(logs.some((l) => /cache miss/.test(l)));

  cachedTTS('en-US', '+0%', 'hello world', io, deps);
  assert.equal(runs, 1, 'second identical run must not invoke edge-tts');
  assert.ok(logs.some((l) => /cache hit/.test(l)));
});

test('cachedTTS: corrupt/unreadable cache entry falls through to a miss, never throws', () => {
  const fake = makeFakeFs();
  const key = ttsKey('en-US', '+0%', 'hello');
  // Cache files "exist" but reading them fails (corrupt entry).
  fake.files.add(`${CACHE}/${key}.mp3`);
  fake.files.add(`${CACHE}/${key}.srt`);
  let runs = 0;
  const copyFile = (src, dst) => {
    if (src.startsWith(CACHE)) throw new Error('corrupt');
    fake.files.add(dst);
  };
  const io = {cacheDir: CACHE, mp3Out: '/w/vo-n1.mp3', srtOut: '/w/vo-n1.srt'};
  assert.doesNotThrow(() =>
    cachedTTS('en-US', '+0%', 'hello', io,
      {run: () => { runs++; }, existsSync: fake.existsSync, copyFile, log: () => {}}));
  assert.equal(runs, 1, 'corrupt cache must re-generate');
});

test('cachedTTS: changing one line re-synthesizes ONLY that line', () => {
  const fake = makeFakeFs();
  const ran = [];
  const run = (voice, rate, spoken, mp3Out, srtOut) => {
    ran.push(spoken);
    fake.files.add(mp3Out);
    fake.files.add(srtOut);
  };
  const deps = {run, existsSync: fake.existsSync, copyFile: fake.copyFile, log: () => {}};
  const line = (spoken, id) =>
    cachedTTS('en-US', '+0%', spoken,
      {cacheDir: CACHE, mp3Out: `/w/vo-${id}.mp3`, srtOut: `/w/vo-${id}.srt`}, deps);

  // Pass 1: both lines cold → both synthesized.
  line('first line', 'n1');
  line('second line', 'n2');
  assert.deepEqual(ran, ['first line', 'second line']);

  // Pass 2: n1 unchanged, n2 text changed → only n2 re-synthesized.
  ran.length = 0;
  line('first line', 'n1');
  line('second line EDITED', 'n2');
  assert.deepEqual(ran, ['second line EDITED']);
});

// --- cachedMezz: second identical call performs zero normalize encodes -------

test('cachedMezz: first call misses (encodes), second call hits (zero encodes)', () => {
  const fake = makeFakeFs();
  const logs = [];
  let encodes = 0;
  const run = (mezzOut) => { encodes++; fake.files.add(mezzOut); };
  const io = {cacheDir: CACHE, mezzOut: '/w/s1-mezz.webm'};
  const deps = {run, existsSync: fake.existsSync, copyFile: fake.copyFile, log: (m) => logs.push(m)};

  cachedMezz('100:5', SEG, VIEW, io, deps);
  assert.equal(encodes, 1);
  assert.ok(logs.some((l) => /cache miss/.test(l)));

  cachedMezz('100:5', SEG, VIEW, io, deps);
  assert.equal(encodes, 1, 'second identical run must not re-encode');
  assert.ok(logs.some((l) => /cache hit/.test(l)));
});

test('cachedMezz: corrupt cache entry falls through to a miss, never throws', () => {
  const fake = makeFakeFs();
  const key = mezzKey('100:5', SEG, VIEW);
  fake.files.add(`${CACHE}/${key}.webm`);
  let encodes = 0;
  const copyFile = (src, dst) => {
    if (src.startsWith(CACHE)) throw new Error('corrupt');
    fake.files.add(dst);
  };
  assert.doesNotThrow(() =>
    cachedMezz('100:5', SEG, VIEW, {cacheDir: CACHE, mezzOut: '/w/s1-mezz.webm'},
      {run: () => { encodes++; }, existsSync: fake.existsSync, copyFile, log: () => {}}));
  assert.equal(encodes, 1);
});

test('cachedMezz: re-cutting one segment re-encodes ONLY that segment', () => {
  const fake = makeFakeFs();
  const ran = [];
  const deps = {
    run: (mezzOut) => { ran.push(mezzOut); fake.files.add(mezzOut); },
    existsSync: fake.existsSync, copyFile: fake.copyFile, log: () => {},
  };
  const s1 = {id: 's1', keep: [['start', 'end']]};
  const s2 = {id: 's2', keep: [['start', 'mark:x']]};
  const cut = (seg) => cachedMezz(`take-${seg.id}`, seg, VIEW,
    {cacheDir: CACHE, mezzOut: `/w/${seg.id}-mezz.webm`}, deps);

  cut(s1); cut(s2);
  assert.deepEqual(ran, ['/w/s1-mezz.webm', '/w/s2-mezz.webm']);

  // s1 unchanged, s2's keep ranges edited → only s2 re-cut.
  ran.length = 0;
  cut(s1);
  cut({...s2, keep: [['start', 'mark:x+1']]});
  assert.deepEqual(ran, ['/w/s2-mezz.webm']);
});
