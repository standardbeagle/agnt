// Integration check for --inspect — exercises the REAL ffmpeg invocation
// buildInspectArgs constructs, on a synthetic offline fixture, and verifies the
// property the unit tests cannot: a contact-sheet PNG is actually produced, at
// the geometry the pure math predicts, from a genuine take + marks json, and
// the run is read-only over everything except out/inspect.
//
// Loud-skips (exit 0, prints SKIP) when ffmpeg/ffprobe are absent so it is safe
// to wire into a make target on a machine without them. Run via `make demo-inspect-check`.
import {execFileSync, spawnSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {inspectDemo, buildInspectArgs, thumbSize, tileGrid, sheetFrameCount} from '../lib/inspect.mjs';

const have = (bin) => spawnSync(bin, ['-version'], {stdio: 'ignore'}).status === 0;
if (!have('ffmpeg') || !have('ffprobe')) {
  console.log('SKIP integration-inspect: ffmpeg/ffprobe not on PATH (make demo-inspect-check needs them)');
  process.exit(0);
}

const ff = (args) => execFileSync('ffmpeg', ['-y', '-v', 'error', ...args], {stdio: ['ignore', 'inherit', 'inherit']});
const probe = (f) => parseFloat(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', f]).toString());
const pngSize = (f) => execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'stream=width,height', '-of', 'csv=p=0', f]).toString().trim();

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'inspect-int-'));
const demoDir = path.join(root, 'demo');
const workDir = path.join(demoDir, 'out', '.work');
const outDir = path.join(demoDir, 'out');
fs.mkdirSync(workDir, {recursive: true});
let failures = 0;
const check = (ok, msg) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${msg}`); if (!ok) failures++; };

const view = {width: 1440, height: 900};
const DUR = 6;

// --- synthetic fixture: one 6s take + a marks json, plus a decoy mezzanine ---
const take = path.join(workDir, 'seg.webm');
ff(['-f', 'lavfi', '-i', 'testsrc=size=1440x900:rate=25', '-t', String(DUR),
  '-c:v', 'libvpx-vp9', '-crf', '40', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-pix_fmt', 'yuv420p', take]);
fs.writeFileSync(path.join(workDir, 'seg.json'), JSON.stringify({
  events: [{name: 'start-mark', tMs: 0}, {name: 'mid', tMs: 3000}, {name: 'end', tMs: 6000}],
}));
// a decoy mezzanine + assembled output the read-only run must not touch
const mezz = path.join(workDir, 'seg-mezz.webm');
fs.copyFileSync(take, mezz);
const assembled = path.join(outDir, 'demo.webm');
fs.copyFileSync(take, assembled);

const spec = {name: 'demo', viewport: view, fps: 25, segments: [
  {id: 'card', type: 'card'}, {id: 'seg', type: 'browser', script: './x.mjs'},
]};

// --- snapshot everything except out/inspect for the read-only assertion ------
const snap = () => {
  const out = {};
  for (const f of fs.readdirSync(workDir)) out[f] = fs.statSync(path.join(workDir, f)).mtimeMs;
  out['../demo.webm'] = fs.statSync(assembled).mtimeMs;
  return out;
};
const before = snap();

// --- run the real inspect over the fixture -----------------------------------
const {results, missing} = inspectDemo(spec, {demoDir, workDir, outDir}, {}, {probe, run: ff});
check(results.length === 1 && missing === 0, 'one sheet produced for the browser segment, card skipped');

const outPng = path.join(outDir, 'inspect', 'seg.png');
check(fs.existsSync(outPng), 'contact-sheet PNG exists at out/inspect/seg.png');

// geometry the pure math predicts for a 6s take
const {w: tileW, h: tileH} = thumbSize(view, 320);
const grid = tileGrid(sheetFrameCount(DUR, 1), 8, tileW, tileH);
const expected = `${grid.sheetW},${grid.sheetH + 56}`; // + STRIP_H
check(pngSize(outPng) === expected, `PNG dimensions ${pngSize(outPng)} match predicted ${expected}`);

// read-only: nothing outside out/inspect changed
const after = snap();
const changed = Object.keys(before).filter((k) => before[k] !== after[k])
  .concat(Object.keys(after).filter((k) => !(k in before)));
check(changed.length === 0, `read-only: no mezzanine/output file created or modified (${changed.join(', ') || 'none'})`);

// --- missing take is loud, not a crash ---------------------------------------
let threw = false;
const errs = [];
const spec2 = {name: 'demo', viewport: view, fps: 25, segments: [{id: 'absent', type: 'browser', script: './x.mjs'}]};
try {
  const r = inspectDemo(spec2, {demoDir, workDir, outDir}, {}, {probe, run: ff, log: {log: () => {}, error: (m) => errs.push(m)}});
  check(r.missing === 1 && r.results.length === 0, 'missing take → counted, not rendered');
} catch { threw = true; }
check(!threw, 'missing take does not throw');
check(errs.some((m) => /absent/.test(m) && /absent\.webm/.test(m)), 'missing take logs a loud per-segment message naming the take path');

fs.rmSync(root, {recursive: true, force: true});
console.log(failures ? `\n${failures} check(s) FAILED` : '\nall integration checks passed');
process.exit(failures ? 1 : 0);
