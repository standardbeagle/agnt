// Integration check for the final-mux upgrades — exercises the REAL ffmpeg
// invocation buildFinalMuxArgs constructs, on a synthetic offline fixture, and
// verifies the two properties unit tests cannot: the brand logo's pixels land
// in the output, and the narration measures at the EBU R128 target loudness.
//
// Loud-skips (exit 0, prints SKIP) when ffmpeg/ffprobe are absent so it is safe
// to wire into a make target on a machine without them. Run via `make demo-mux-check`.
import {execFileSync, spawnSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {buildFinalMuxArgs} from '../lib/assemble.mjs';

const have = (bin) => spawnSync(bin, ['-version'], {stdio: 'ignore'}).status === 0;
const have2 = (bin) => spawnSync('which', [bin], {stdio: 'ignore'}).status === 0;
if (!have('ffmpeg') || !have('ffprobe')) {
  console.log('SKIP integration-mux: ffmpeg/ffprobe not on PATH (make demo-mux-check needs them)');
  process.exit(0);
}

const ff = (args) => execFileSync('ffmpeg', ['-y', '-v', 'error', ...args], {stdio: ['ignore', 'inherit', 'inherit']});
const work = fs.mkdtempSync(path.join(os.tmpdir(), 'mux-int-'));
const view = {fps: 25};
let failures = 0;
const check = (ok, msg) => { console.log(`${ok ? 'PASS' : 'FAIL'}  ${msg}`); if (!ok) failures++; };

// --- synthetic fixture -------------------------------------------------------
const silent = path.join(work, 'visual.webm');
ff(['-f', 'lavfi', '-i', 'color=c=#0f1117:s=1440x900:r=25', '-t', '6',
  '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-pix_fmt', 'yuv420p', silent]);

// A solid magenta logo (200px square → scaled to 220 wide by the overlay).
const brandPng = path.join(work, 'brand.png');
ff(['-f', 'lavfi', '-i', 'color=c=magenta:s=200x200', '-frames:v', '1', brandPng]);

// VO clips fed the way the pipeline feeds them. The -16 LUFS / ±1.5 target is
// calibrated for speech, so the loudness leg uses real edge-tts VO when present;
// pure tones are a pathological input for single-pass loudnorm and only stand in
// for the (audio-independent) overlay-pixel legs when edge-tts is absent.
const haveTts = have2('edge-tts');
const vo = [];
const lines = [
  ['Native brand overlay folds into the same final encode.', 1.0],
  ['Loudness is normalized to the E B U R one twenty eight target.', 3.2],
];
lines.forEach(([text, at], i) => {
  const mp3 = path.join(work, `vo-${i}.mp3`);
  if (haveTts) {
    execFileSync('edge-tts', ['--voice', 'en-US-AriaNeural', '--text', text, '--write-media', mp3], {stdio: 'ignore'});
  } else {
    ff(['-f', 'lavfi', '-i', `sine=frequency=${220 + i * 110}:duration=1.4`, '-c:a', 'libmp3lame', '-q:a', '4', mp3]);
  }
  vo.push({mp3, at});
});

// --- 1. narrated + brand: overlay pixels + loudness --------------------------
const outNarr = path.join(work, 'narrated.webm');
const rNarr = buildFinalMuxArgs(
  {name: 'n', brand: {image: 'brand.png'}},
  {silent, out: outNarr, srtPath: null}, // no subtitles fixture; graph still overlays on [0:v]-derived frames
  {voiced: vo, totalDur: 6, view, demoDir: work, fileExists: fs.existsSync});
// The real narrated graph burns subtitles; for the fixture we swap that leg for a
// straight relabel so no .srt file is needed, keeping the overlay + loudnorm legs intact.
rNarr.argv = rNarr.argv.map((a) => (typeof a === 'string' ? a.replace(/\[0:v\]subtitles=[^\[]*\[subbed\]/, '[0:v]null[subbed]') : a));
check(rNarr.encodeCount === 1 && rNarr.path === 'narrated-brand', 'narrated+brand builds one-encode graph');
ff(rNarr.argv);

// loudness: measure integrated LUFS of the muxed audio, expect within ±1.5 of -16.
if (haveTts) {
  const ebur = spawnSync('ffmpeg', ['-nostats', '-i', outNarr, '-af', 'ebur128=framelog=verbose', '-f', 'null', '-'],
    {encoding: 'utf8'}).stderr;
  const mI = ebur.match(/Integrated loudness:[\s\S]*?I:\s*(-?[\d.]+)\s*LUFS/);
  const I = mI ? parseFloat(mI[1]) : NaN;
  check(Number.isFinite(I) && Math.abs(I - -16) <= 1.5, `narration integrated loudness ${I} LUFS within -16 ±1.5`);
} else {
  console.log('SKIP  loudness check: edge-tts not on PATH (tones are pathological for single-pass loudnorm)');
}

// --- 2. brand pixels present in the top-right region -------------------------
const sampleColor = (video) => {
  // logo is 220x220 at top-right pad 32 → center near (W-32-110, 32+110)=(1298,142).
  const raw = execFileSync('ffmpeg',
    ['-v', 'error', '-i', video, '-frames:v', '1',
      '-vf', 'crop=20:20:1288:132,scale=1:1', '-f', 'rawvideo', '-pix_fmt', 'rgb24', '-'],
    {maxBuffer: 1 << 20});
  return [raw[0], raw[1], raw[2]];
};
const [r, g, b] = sampleColor(outNarr);
check(r > 180 && g < 80 && b > 180, `narrated brand pixels magenta at top-right (rgb ${r},${g},${b})`);

// --- 3. silent + brand: one encode, overlay pixels ---------------------------
const outSil = path.join(work, 'silent.webm');
const rSil = buildFinalMuxArgs(
  {name: 's', brand: {image: 'brand.png'}},
  {silent, out: outSil, srtPath: null},
  {voiced: [], totalDur: 6, view, demoDir: work, fileExists: fs.existsSync});
check(rSil.encodeCount === 1 && rSil.path === 'silent-brand', 'silent+brand builds one-encode graph');
ff(rSil.argv);
const [sr, sg, sb] = sampleColor(outSil);
check(sr > 180 && sg < 80 && sb > 180, `silent brand pixels magenta at top-right (rgb ${sr},${sg},${sb})`);

fs.rmSync(work, {recursive: true, force: true});
console.log(failures ? `\n${failures} check(s) FAILED` : '\nall integration checks passed');
process.exit(failures ? 1 : 0);
