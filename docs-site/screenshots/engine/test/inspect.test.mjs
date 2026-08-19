// Covers the `--inspect` cut-point authoring aid in lib/inspect.mjs:
//   1. the pure contact-sheet / timeline-strip geometry (mark→x-pixel mapping,
//      tile grid) — the criterion "mark labels rendered at correct x-positions",
//   2. the pure ffmpeg-arg construction (tile filter + a drawtext per mark at its
//      computed x), asserted without ever spawning ffmpeg via the injected `run`,
//   3. read-only behavior — inspect writes ONLY under out/inspect, never a
//      mezzanine/output file, and
//   4. a missing take is a loud per-segment message, not a crash.
import {test} from 'node:test';
import assert from 'node:assert/strict';
import path from 'node:path';
import {
  thumbSize, sheetFrameCount, tileGrid, markStripX,
  buildInspectArgs, inspectDemo,
} from '../lib/inspect.mjs';

const VIEW = {width: 1440, height: 900, fps: 25};

// --- geometry: tile grid -----------------------------------------------------

test('thumbSize keeps the viewport aspect ratio', () => {
  const {w, h} = thumbSize(VIEW, 320);
  assert.equal(w, 320);
  assert.equal(h, Math.round(320 * (900 / 1440))); // 200
});

test('sheetFrameCount samples ~1 frame/s and is always >= 1', () => {
  assert.equal(sheetFrameCount(6, 1), 6);
  assert.equal(sheetFrameCount(6.2, 1), 7);   // ceil
  assert.equal(sheetFrameCount(0, 1), 1);     // never zero → always a sheet
  assert.equal(sheetFrameCount(0.1, 1), 1);
});

test('tileGrid derives rows from frame count and columns; shrinks cols on short takes', () => {
  const g = tileGrid(20, 8, 320, 200);
  assert.equal(g.cols, 8);
  assert.equal(g.rows, 3);              // ceil(20/8)
  assert.equal(g.sheetW, 8 * 320);
  assert.equal(g.sheetH, 3 * 200);
  // fewer frames than columns → columns shrink, never an empty trailing column
  const s = tileGrid(3, 8, 320, 200);
  assert.equal(s.cols, 3);
  assert.equal(s.rows, 1);
  assert.equal(s.sheetW, 3 * 320);
});

// --- geometry: THE mark→x-pixel mapping (mutation-guarded) -------------------

test('markStripX maps [0,takeDur] → [0,stripW] at the endpoints and the middle', () => {
  assert.equal(markStripX(0, 10, 1000), 0);      // start → left edge
  assert.equal(markStripX(10, 10, 1000), 1000);  // end → right edge
  assert.equal(markStripX(5, 10, 1000), 500);    // middle
  assert.equal(markStripX(2.5, 10, 1000), 250);  // quarter
});

test('markStripX is monotonic in t (a mutant that ignores t fails here)', () => {
  const xs = [0, 1, 2, 5, 9, 10].map((t) => markStripX(t, 10, 800));
  for (let i = 1; i < xs.length; i++) assert.ok(xs[i] > xs[i - 1], `x must grow with t (${xs})`);
  // and it genuinely depends on t: two different t give two different x
  assert.notEqual(markStripX(3, 10, 800), markStripX(7, 10, 800));
});

test('markStripX clamps a mark at/after the take end onto the right edge, never past it', () => {
  assert.equal(markStripX(12, 10, 1000), 1000); // past end → clamped
  assert.equal(markStripX(-1, 10, 1000), 0);    // before start → clamped
});

test('markStripX handles a zero/negative take duration without dividing by zero', () => {
  assert.equal(markStripX(3, 0, 1000), 0);
});

// --- ffmpeg args: tile filter + a drawtext per mark at its computed x ---------

const MARKS = [
  {name: 'bug-shown', tMs: 1852},
  {name: 'fix-applied', tMs: 4000},
  {name: 'end', tMs: 5457},
];
const build = (dur = 6, marks = MARKS, opts = {}) =>
  buildInspectArgs('/w/seg.webm', '/o/inspect/seg.png', dur, marks, VIEW,
    {fontfile: '/fonts/DejaVuSans.ttf', ...opts});

test('buildInspectArgs emits a tile filter sized to the grid and one PNG output', () => {
  const r = build();
  const vf = r.argv[r.argv.indexOf('-vf') + 1];
  assert.match(vf, /fps=1/);
  assert.match(vf, new RegExp(`tile=${r.grid.cols}x${r.grid.rows}`));
  assert.match(vf, /scale=320:200/);
  assert.equal(r.argv[r.argv.length - 1], '/o/inspect/seg.png'); // single output
  assert.ok(r.argv.includes('-frames:v') || r.argv.includes('-frames'));
});

test('buildInspectArgs draws one label per mark, each at its markStripX position', () => {
  const r = build();
  assert.equal(r.marks.length, MARKS.length);
  for (const m of r.marks) {
    const expected = markStripX(m.t, 6, r.sheetW);
    assert.equal(m.x, expected);
    // a tick drawbox sits at the exact x, and a labelled drawtext references it
    const vf = r.argv[r.argv.indexOf('-vf') + 1];
    assert.match(vf, new RegExp(`drawbox=x=${expected}:`), `tick at x=${expected} for ${m.name}`);
    assert.ok(vf.includes(`text='${m.name}'`), `label for ${m.name}`);
  }
});

test('buildInspectArgs converts mark tMs (ms) into seconds for the x mapping', () => {
  const r = build();
  const bug = r.marks.find((m) => m.name === 'bug-shown');
  assert.equal(bug.t, 1.852);
  assert.equal(bug.x, markStripX(1.852, 6, r.sheetW));
});

test('buildInspectArgs anchors the label so a right-edge mark cannot overflow the sheet', () => {
  const r = build();
  const last = r.marks[r.marks.length - 1]; // "end" near the right edge
  const vf = r.argv[r.argv.indexOf('-vf') + 1];
  // label x is min(markX, sheetW-tw-<pad>) so the text box stays on the sheet
  assert.match(vf, new RegExp(`x=min\\(${last.x}\\\\,${r.sheetW}-tw`));
});

test('buildInspectArgs threads a fontfile when given, and omits it otherwise', () => {
  const withFont = build();
  assert.ok(withFont.argv[withFont.argv.indexOf('-vf') + 1].includes('fontfile=/fonts/DejaVuSans.ttf'));
  const noFont = buildInspectArgs('/w/seg.webm', '/o/inspect/seg.png', 6, MARKS, VIEW, {fontfile: null});
  assert.ok(!noFont.argv[noFont.argv.indexOf('-vf') + 1].includes('fontfile='));
});

test('buildInspectArgs tolerates a take with no marks (bare contact sheet)', () => {
  const r = buildInspectArgs('/w/seg.webm', '/o/inspect/seg.png', 6, [], VIEW, {fontfile: null});
  assert.equal(r.marks.length, 0);
  const vf = r.argv[r.argv.indexOf('-vf') + 1];
  assert.match(vf, /tile=/);
  assert.ok(!vf.includes('drawtext'));
});

// --- orchestration: read-only + missing-take, via injected seams -------------

const SPEC = {
  name: 'demo',
  viewport: {width: 1440, height: 900},
  fps: 25,
  segments: [
    {id: 'card-intro', type: 'card'},               // no take — must be skipped
    {id: 'show-bug', type: 'browser', script: './x.mjs'},
    {id: 'attempt-1', type: 'cli'},
  ],
};
const DIRS = {demoDir: '/demo', workDir: '/demo/out/.work', outDir: '/demo/out'};
const INSPECT_DIR = path.join(DIRS.outDir, 'inspect');

// A recording harness: existing paths in a Set, and recorders for every write.
const harness = (existing) => {
  const files = new Set(existing);
  const runs = [];      // ffmpeg argv arrays
  const mkdirs = [];     // directories created
  const reads = [];      // mark files read
  const logs = {info: [], err: []};
  const deps = {
    probe: () => 6.0,
    run: (argv) => { runs.push(argv); },
    existsSync: (p) => files.has(p),
    readMarks: (p) => { reads.push(p); return {events: MARKS}; },
    mkdir: (d) => { mkdirs.push(d); },
    log: {log: (m) => logs.info.push(m), error: (m) => logs.err.push(m)},
  };
  return {files, runs, mkdirs, reads, logs, deps};
};

test('inspectDemo writes ONLY under out/inspect — never a mezzanine/output file', () => {
  const h = harness([
    '/demo/out/.work/show-bug.webm', '/demo/out/.work/show-bug.json',
    '/demo/out/.work/attempt-1.webm',
  ]);
  inspectDemo(SPEC, DIRS, {}, h.deps);
  // one PNG per browser/cli take (card skipped)
  assert.equal(h.runs.length, 2);
  for (const argv of h.runs) {
    const out = argv[argv.length - 1];
    assert.ok(out.startsWith(INSPECT_DIR + path.sep), `output ${out} must live under ${INSPECT_DIR}`);
    assert.match(out, /\.png$/);
  }
  // the only directory ever created is out/inspect
  assert.deepEqual([...new Set(h.mkdirs)], [INSPECT_DIR]);
  // marks are read only from the .work take-mark json, never a mezz/output file
  for (const p of h.reads) assert.match(p, /\.work\/[^/]+\.json$/);
});

test('inspectDemo skips card segments (they have no take)', () => {
  const h = harness([
    '/demo/out/.work/show-bug.webm', '/demo/out/.work/show-bug.json',
    '/demo/out/.work/attempt-1.webm',
  ]);
  const r = inspectDemo(SPEC, DIRS, {}, h.deps);
  const outs = h.runs.map((a) => a[a.length - 1]);
  assert.ok(!outs.some((o) => o.includes('card-intro')));
  assert.equal(r.results.length, 2);
});

test('inspectDemo: a missing take is a loud per-segment message, not a crash', () => {
  const h = harness([
    // show-bug take is ABSENT; attempt-1 present
    '/demo/out/.work/attempt-1.webm',
  ]);
  let r;
  assert.doesNotThrow(() => { r = inspectDemo(SPEC, DIRS, {}, h.deps); });
  assert.equal(r.missing, 1);
  assert.equal(r.results.length, 1);              // attempt-1 still produced
  // loud message names the segment AND the missing take path
  assert.ok(h.logs.err.some((m) => /show-bug/.test(m) && /show-bug\.webm/.test(m)),
    `expected loud missing-take message, got ${JSON.stringify(h.logs.err)}`);
  // the present segment still ran
  assert.equal(h.runs.length, 1);
  assert.ok(h.runs[0][h.runs[0].length - 1].includes('attempt-1'));
});

test('inspectDemo with only=<seg-id> renders exactly that segment', () => {
  const h = harness([
    '/demo/out/.work/show-bug.webm', '/demo/out/.work/show-bug.json',
    '/demo/out/.work/attempt-1.webm',
  ]);
  const r = inspectDemo(SPEC, DIRS, {only: 'attempt-1'}, h.deps);
  assert.equal(r.results.length, 1);
  assert.equal(h.runs.length, 1);
  assert.ok(h.runs[0][h.runs[0].length - 1].includes('attempt-1'));
});

test('inspectDemo with only=<unknown> is loud, not silent', () => {
  const h = harness(['/demo/out/.work/show-bug.webm']);
  inspectDemo(SPEC, DIRS, {only: 'nope'}, h.deps);
  assert.ok(h.logs.err.some((m) => /nope/.test(m)));
  assert.equal(h.runs.length, 0);
});

test('inspectDemo tolerates a take that has no marks json (empty marks)', () => {
  const h = harness(['/demo/out/.work/attempt-1.webm']); // no .json for it
  const r = inspectDemo({...SPEC, segments: [{id: 'attempt-1', type: 'cli'}]}, DIRS, {}, h.deps);
  assert.equal(r.results.length, 1);
  assert.equal(h.reads.length, 0);                // never tried to read a missing json
  assert.equal(r.results[0].marks.length, 0);
});
