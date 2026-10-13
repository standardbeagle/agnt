// Integration check for --publish — exercises the REAL ffmpeg invocations
// buildPosterArgs / buildAnimArgs construct, on a synthetic clip, publishing
// into a temp docs-site (never the repo's). It verifies what the unit tests
// cannot: the poster and the animated webp are actually produced at the
// documented widths, the clip is copied byte-for-byte, the .vtt sidecar rides
// along, and a publish that cannot succeed leaves the destination untouched.
//
// Loud-skips (exit 0, prints SKIP) when ffmpeg/ffprobe are absent so it is safe
// to wire into a make target on a machine without them. Run via `make demo-publish-check`.
import {execFileSync, spawnSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {publishDemo, publishPaths} from '../lib/publish.mjs';

const have = (bin) => spawnSync(bin, ['-version'], {stdio: 'ignore'}).status === 0;
if (!have('ffmpeg') || !have('ffprobe')) {
  console.log('SKIP integration-publish: ffmpeg/ffprobe not on PATH (make demo-publish-check needs them)');
  process.exit(0);
}

const ff = (args) => execFileSync('ffmpeg', ['-y', '-v', 'error', ...args], {stdio: ['ignore', 'inherit', 'inherit']});
// ffmpeg's webp decoder cannot read an ANIMATED webp ("image data not found"),
// so the animation is measured from its RIFF container instead: the VP8X chunk
// carries the canvas size, and each frame is one ANMF chunk.
const webpAnim = (f) => {
  const b = fs.readFileSync(f);
  if (b.toString('latin1', 0, 4) !== 'RIFF' || b.toString('latin1', 8, 12) !== 'WEBP') return null;
  let width = 0, height = 0, frames = 0;
  for (let o = 12; o + 8 <= b.length;) {
    const id = b.toString('latin1', o, o + 4);
    const size = b.readUInt32LE(o + 4);
    if (id === 'VP8X') {
      width = b.readUIntLE(o + 12, 3) + 1;
      height = b.readUIntLE(o + 15, 3) + 1;
    }
    if (id === 'ANMF') frames++;
    o += 8 + size + (size & 1);
  }
  return {width, height, frames};
};

const dims = (f) => execFileSync('ffprobe', ['-v', 'error', '-select_streams', 'v:0',
  '-show_entries', 'stream=width,height', '-of', 'csv=p=0', f]).toString().trim().split('\n')[0];

const root = fs.mkdtempSync(path.join(os.tmpdir(), 'publish-int-'));
const docsSiteDir = path.join(root, 'docs-site');
const demoDir = path.join(docsSiteDir, 'screenshots', 'demos', 'fixture');
const outDir = path.join(demoDir, 'out');
fs.mkdirSync(outDir, {recursive: true});
let failures = 0;
const check = (ok, msg) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${msg}`); if (!ok) failures++; };
const quiet = {log: () => {}, error: () => {}};
const listStatic = () => {
  const dir = path.join(docsSiteDir, 'static');
  return fs.existsSync(dir) ? fs.readdirSync(dir, {recursive: true}).map(String).sort() : [];
};

const dirs = {demoDir, docsSiteDir, dirName: 'fixture', specName: 'fixture'};
const p = publishPaths(dirs);

// --- a publish with no rendered demo writes nothing --------------------------
let err = null;
try { publishDemo(dirs, {log: quiet}); } catch (e) { err = e; }
check(err && /source not found/.test(err.message), 'missing source throws a run-first message');
check(listStatic().length === 0, 'missing source wrote nothing under docs-site/static');

// --- synthetic 4s 1440x900 clip (long enough for the t=2s poster seek) -------
ff(['-f', 'lavfi', '-i', 'testsrc=size=1440x900:rate=25', '-t', '4',
  '-c:v', 'libvpx-vp9', '-crf', '40', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-pix_fmt', 'yuv420p', p.srcWebm]);

// --- silent demo: clip + poster + animation, no captions ---------------------
const r = publishDemo(dirs, {log: quiet});
check(fs.readFileSync(p.destWebm).equals(fs.readFileSync(p.srcWebm)), 'clip is copied byte-for-byte');
check(fs.existsSync(p.destPoster) && dims(p.destPoster) === '960,600', `poster is 960 wide (${fs.existsSync(p.destPoster) ? dims(p.destPoster) : 'absent'})`);
const anim = fs.existsSync(p.destAnim) ? webpAnim(p.destAnim) : null;
check(anim && anim.width === 720 && anim.height === 450, `animation is 720 wide (${anim ? anim.width + ',' + anim.height : 'absent'})`);
check(anim && anim.frames > 1, `animation carries more than one frame (${anim ? anim.frames : 0})`);
check(r.vtt === null && !fs.existsSync(p.destVtt), 'silent demo publishes no .vtt');
check(r.embed.includes('/video/fixture.webm') && r.embed.includes('/img/fixture-poster.webp'), 'embed snippet names the published paths');

// --- captioned demo: the sidecar rides along, and a re-run overwrites --------
fs.writeFileSync(p.srcVtt, 'WEBVTT\n\n00:00.000 --> 00:01.000\nhello\n');
const r2 = publishDemo(dirs, {log: quiet});
check(r2.vtt === p.destVtt && fs.readFileSync(p.destVtt, 'utf8').includes('hello'), 'captioned demo publishes its .vtt');

// --- a render failure leaves the previously published assets untouched -------
const before = Object.fromEntries([p.destWebm, p.destPoster, p.destAnim].map((f) => [f, fs.statSync(f).mtimeMs]));
fs.writeFileSync(p.srcWebm, 'not a video');
err = null;
try { publishDemo(dirs, {log: quiet, run: (argv) => execFileSync('ffmpeg', argv, {stdio: 'ignore'})}); } catch (e) { err = e; }
check(err !== null, 'an unreadable source fails the publish');
const unchanged = Object.keys(before).every((f) => fs.statSync(f).mtimeMs === before[f]);
check(unchanged, 'a failed publish leaves every previously published asset untouched');
check(!fs.readFileSync(p.destWebm).equals(fs.readFileSync(p.srcWebm)), 'the broken clip was not copied over the good one');

fs.rmSync(root, {recursive: true, force: true});
console.log(failures ? `\n${failures} check(s) FAILED` : '\nall integration checks passed');
process.exit(failures ? 1 : 0);
