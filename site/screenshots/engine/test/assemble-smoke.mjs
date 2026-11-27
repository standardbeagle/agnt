// Assembly smoke: stage the committed ci-fixture takes, run the REAL
// `demo.mjs ... --assemble-only` CLI, and assert it produced a playable video
// of the expected duration. This is the regression guard for the whole
// splice → normalize → concat → mux pipeline, and it runs with ffmpeg + node
// only — no Chrome, no VHS, no edge-tts, no daemon (the fixture is card-less
// and declares no narration or setup).
//
// Loud-skips (exit 0, prints SKIP) when ffmpeg/ffprobe are absent so it is safe
// to wire into a make target anywhere. Run via `make demo-assemble-check`.
import {execFileSync, spawnSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';

const here = path.dirname(fileURLToPath(import.meta.url));
const engineDir = path.dirname(here);
const screenshotsDir = path.dirname(engineDir);
const demoDir = path.join(screenshotsDir, 'demos', 'ci-fixture');

const have = (bin) => spawnSync(bin, ['-version'], {stdio: 'ignore'}).status === 0;
if (!have('ffmpeg') || !have('ffprobe')) {
  console.log('SKIP assemble-smoke: ffmpeg/ffprobe not on PATH (make demo-assemble-check needs them)');
  process.exit(0);
}

// Expected total: clip-a (3.0s straight) + clip-b (keep-spliced 1.5s + 1.0s =
// 2.5s) ≈ 5.5s. Tolerance absorbs vp9 keyframe/duration rounding across the
// splice + concat, but is tight enough that a broken pipeline (empty output,
// dropped segment, un-spliced take) falls outside it.
const EXPECTED = 5.5;
const TOLERANCE = 0.75;

let failures = 0;
const check = (ok, msg) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${msg}`); if (!ok) failures++; };

// --- stage committed takes into the (gitignored) work dir --------------------
// out/ is gitignored, so the takes live tracked under takes/ and are copied to
// the names assembly reads (out/.work/<seg-id>.{webm,json}).
const workDir = path.join(demoDir, 'out', '.work');
fs.rmSync(path.join(demoDir, 'out'), {recursive: true, force: true});
fs.mkdirSync(workDir, {recursive: true});
for (const f of fs.readdirSync(path.join(demoDir, 'takes'))) {
  fs.copyFileSync(path.join(demoDir, 'takes', f), path.join(workDir, f));
}

// --- run the real CLI --------------------------------------------------------
const run = spawnSync('node',
  [path.join(engineDir, 'demo.mjs'), 'demos/ci-fixture', '--assemble-only'],
  {cwd: screenshotsDir, encoding: 'utf8'});
if (run.stdout) process.stdout.write(run.stdout);
if (run.stderr) process.stderr.write(run.stderr);
check(run.status === 0, `--assemble-only exits 0 (got ${run.status})`);

// --- assert a playable output of the expected length -------------------------
const out = path.join(demoDir, 'out', 'ci-fixture.webm');
const exists = fs.existsSync(out) && fs.statSync(out).size > 0;
check(exists, `output exists and is non-empty: ${path.relative(screenshotsDir, out)}`);

if (exists) {
  const codec = execFileSync('ffprobe',
    ['-v', 'error', '-select_streams', 'v:0', '-show_entries', 'stream=codec_name',
      '-of', 'csv=p=0', out], {encoding: 'utf8'}).trim();
  check(codec.length > 0, `output carries a decodable video stream (codec=${codec || 'none'})`);

  const dur = parseFloat(execFileSync('ffprobe',
    ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', out], {encoding: 'utf8'}).trim());
  check(Math.abs(dur - EXPECTED) <= TOLERANCE,
    `duration ${dur.toFixed(2)}s within ${EXPECTED}±${TOLERANCE}s`);
}

if (failures > 0) {
  console.error(`\nassemble-smoke: ${failures} check(s) failed`);
  process.exit(1);
}
console.log('\nassemble-smoke: ok');
