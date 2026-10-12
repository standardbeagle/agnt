// Covers `--publish` in lib/publish.mjs without spawning ffmpeg or touching the
// repo: the filesystem and the ffmpeg runner are injected.
//   1. paths and ffmpeg argv are the documented ones,
//   2. a missing source or missing ffmpeg writes nothing,
//   3. nothing lands under docs-site until every render has succeeded, and a
//      failed render leaves no partial asset behind,
//   4. the .vtt sidecar is copied only when it exists,
//   5. a name that could escape the asset directories is refused.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import {
  publishPaths, buildPosterArgs, buildAnimArgs, embedSnippet, publishDemo, assertDemoName,
} from '../lib/publish.mjs';

const DIRS = {
  demoDir: path.join('/repo', 'docs-site', 'screenshots', 'demos', 'vhs-spiral'),
  docsSiteDir: path.join('/repo', 'docs-site'),
  dirName: 'vhs-spiral',
  specName: 'vhs-spiral',
};
const P = publishPaths(DIRS);
const TMP = path.join('/tmp', 'demo-publish-x');

// A fake filesystem + ffmpeg that records every operation in order.
const harness = ({files = [P.srcWebm], ffmpeg = true, failOn = null, produce = true} = {}) => {
  const present = new Set(files);
  const ops = [];
  const deps = {
    existsSync: (f) => present.has(f),
    mkdir: (d) => ops.push(['mkdir', d]),
    mkdtemp: () => { ops.push(['mkdtemp', TMP]); return TMP; },
    copyFile: (a, b) => { ops.push(['copy', a, b]); present.add(b); },
    move: (a, b) => { ops.push(['move', a, b]); present.delete(a); present.add(b); },
    rmTree: (d) => ops.push(['rm', d]),
    haveFfmpeg: () => ffmpeg,
    run: (argv) => {
      const out = argv[argv.length - 1];
      ops.push(['ffmpeg', out]);
      if (failOn && out.endsWith(failOn)) throw new Error('ffmpeg exploded');
      if (produce) present.add(out);
    },
    log: {log: (m) => ops.push(['log', m])},
  };
  const wroteUnderDocsSite = () => ops.filter(([op, , dst]) =>
    (op === 'copy' || op === 'move') && dst.startsWith(path.join(DIRS.docsSiteDir, 'static')));
  return {deps, ops, present, wroteUnderDocsSite};
};

test('publishPaths maps a demo onto the four committed asset paths', () => {
  assert.equal(P.srcWebm, path.join(DIRS.demoDir, 'out', 'vhs-spiral.webm'));
  assert.equal(P.destWebm, path.join(DIRS.docsSiteDir, 'static', 'video', 'vhs-spiral.webm'));
  assert.equal(P.destVtt, path.join(DIRS.docsSiteDir, 'static', 'video', 'vhs-spiral.vtt'));
  assert.equal(P.destPoster, path.join(DIRS.docsSiteDir, 'static', 'img', 'vhs-spiral-poster.webp'));
  assert.equal(P.destAnim, path.join(DIRS.docsSiteDir, 'static', 'img', 'vhs-spiral-demo.webp'));
});

test('the source is named after demo.json, the published assets after the directory', () => {
  const p = publishPaths({...DIRS, specName: 'spiral'});
  assert.equal(p.srcWebm, path.join(DIRS.demoDir, 'out', 'spiral.webm'));
  assert.equal(p.destWebm, path.join(DIRS.docsSiteDir, 'static', 'video', 'vhs-spiral.webm'));
});

test('ffmpeg argv: poster is one 960-wide frame at 2s, animation is 8fps 720-wide looping webp', () => {
  const poster = buildPosterArgs('in.webm', 'p.webp');
  assert.deepEqual(poster.slice(poster.indexOf('-ss')), ['-ss', '2', '-i', 'in.webm', '-frames:v', '1', '-vf', 'scale=960:-2', 'p.webp']);
  const anim = buildAnimArgs('in.webm', 'a.webp');
  assert.ok(anim.includes('fps=8,scale=720:-2:flags=lanczos'));
  assert.deepEqual(anim.slice(anim.indexOf('-loop')), ['-loop', '0', '-an', '-c:v', 'libwebp', '-q:v', '70', 'a.webp']);
  // never blocks on a terminal, always overwrites its temp target
  for (const argv of [poster, anim]) {
    assert.ok(argv.includes('-nostdin') && argv.includes('-y'));
  }
});

test('a successful publish writes all three assets and returns the embed snippet', () => {
  const h = harness();
  const r = publishDemo(DIRS, h.deps);
  assert.deepEqual(h.wroteUnderDocsSite().map(([, , dst]) => dst), [P.destWebm, P.destPoster, P.destAnim]);
  assert.equal(r.video, P.destWebm);
  assert.equal(r.vtt, null);
  assert.equal(r.embed, embedSnippet('vhs-spiral'));
  assert.ok(r.embed.includes('src="/video/vhs-spiral.webm"') && r.embed.includes('poster="/img/vhs-spiral-poster.webp"'));
  assert.ok(h.ops.some(([op, d]) => op === 'rm' && d === TMP), 'temp dir is removed');
});

test('nothing reaches docs-site until both renders have run', () => {
  const h = harness();
  publishDemo(DIRS, h.deps);
  const lastRender = h.ops.map(([op]) => op).lastIndexOf('ffmpeg');
  const firstPublish = h.ops.findIndex(([op, , dst]) => op === 'move' && dst === P.destWebm);
  assert.ok(lastRender >= 0 && firstPublish > lastRender, 'publish moves must follow every render');
});

test('a missing source is loud and writes nothing — not even a temp dir', () => {
  const h = harness({files: []});
  assert.throws(() => publishDemo(DIRS, h.deps), /source not found.*vhs-spiral\.webm[\s\S]*make demo NAME=vhs-spiral/);
  assert.deepEqual(h.ops, []);
});

test('missing ffmpeg is loud and writes nothing', () => {
  const h = harness({ffmpeg: false});
  assert.throws(() => publishDemo(DIRS, h.deps), /ffmpeg not found on PATH/);
  assert.deepEqual(h.ops, []);
});

test('a failed render leaves no partial asset under docs-site and cleans its temp dir', () => {
  for (const failOn of ['-poster.webp', '-demo.webp']) {
    const h = harness({failOn});
    assert.throws(() => publishDemo(DIRS, h.deps), /ffmpeg exploded/);
    assert.deepEqual(h.wroteUnderDocsSite(), [], `no asset may be published when ${failOn} fails`);
    assert.ok(h.ops.some(([op, d]) => op === 'rm' && d === TMP));
  }
});

test('a render that exits 0 without output does not publish a clip with no poster', () => {
  const h = harness({produce: false});
  assert.throws(() => publishDemo(DIRS, h.deps), /ffmpeg produced no vhs-spiral-poster\.webp/);
  assert.deepEqual(h.wroteUnderDocsSite(), []);
});

test('the .vtt sidecar is copied only when the demo has one', () => {
  const h = harness({files: [P.srcWebm, P.srcVtt]});
  const r = publishDemo(DIRS, h.deps);
  assert.equal(r.vtt, P.destVtt);
  assert.ok(h.ops.some(([op, a, b]) => op === 'copy' && a === P.srcVtt && b === P.destVtt));
  const silent = harness();
  publishDemo(DIRS, silent.deps);
  assert.ok(!silent.ops.some(([, , dst]) => dst === P.destVtt));
});

test('names that could escape the asset directories are refused', () => {
  for (const bad of ['', '..', '../x', 'a/b', 'a\\b', '.hidden', 'a..b', 'x y']) {
    assert.throws(() => assertDemoName(bad), /invalid demo name/, `should refuse ${JSON.stringify(bad)}`);
  }
  for (const ok of ['vhs-spiral', 'ci_fixture', 'demo.v2', 'A1']) assert.equal(assertDemoName(ok), ok);
  assert.throws(() => publishPaths({...DIRS, specName: '../../etc/passwd'}), /invalid demo name/);
});
