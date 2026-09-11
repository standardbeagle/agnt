// Shared ffmpeg/process helpers for the demo engine.
import {execFileSync, spawn} from 'node:child_process';
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';

// --- content-keyed assembly cache: pure key functions -----------------------
// Fields are NUL-joined before hashing so "a|b","c" and "a","b|c" cannot collide
// on a shared '|' separator.
const sha256 = (parts) =>
  crypto.createHash('sha256').update(parts.join('\0')).digest('hex');

// TTS cache key: a synthesized narration line is fully determined by voice, rate,
// and the spoken text (after the agnt→agent substitution). Any change re-synths.
export const ttsKey = (voice, rate, spoken) => sha256([voice, rate, spoken]);

// Mezzanine cache key: a spliced+normalized take is determined by the take's
// identity plus the cut/encode inputs (keep ranges, trim, viewport w/h, fps).
export const mezzKey = (takeFastKey, seg, view) => sha256([
  takeFastKey,
  JSON.stringify(seg.keep ?? null),
  String(seg.trimSeconds ?? ''),
  String(view.width), String(view.height), String(view.fps),
]);

// Cheap content identity for a take file: size + mtime. Re-recording a take
// changes its bytes and therefore this fast-key, invalidating its mezzanine.
export const fileFastKey = (f) => {
  const s = fs.statSync(f);
  return `${s.size}:${s.mtimeMs}`;
};

// Chromium launch options shared by every Playwright launch in the demo engine.
// Two of Playwright's defaults starve headless Chromium of compositor frames on
// GPU-less hosts (WSL2 measured): its `--enable-unsafe-swiftshader` and the
// absence of `--disable-gpu`. Either one alone stalls Page.captureScreenshot
// past 3 s and leaves a 5 s recordVideo take with 24 frames; with both
// corrected the same take holds 147 frames and screenshots run at 17-20 fps.
// Measured 2026-09-11 against Playwright 1.60 on chromium, headless shell and
// system Chrome alike. Every launch goes through this so the fix cannot drift.
export const CHROMIUM_LAUNCH_OPTIONS = Object.freeze({
  ignoreDefaultArgs: ['--enable-unsafe-swiftshader'],
  args: ['--disable-gpu'],
});

export const ff = (args) =>
  execFileSync('ffmpeg', ['-y', '-v', 'error', ...args], {stdio: ['ignore', 'inherit', 'inherit']});

export const probeDur = (f) =>
  parseFloat(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', f]).toString());

// Re-encode any segment source (vhs webm, playwright webm, card loop) into the
// uniform mezzanine every segment is concatenated from: vp9, fixed size/fps.
export const normalize = (src, dst, {width, height, fps}, trimSeconds) =>
  ff(['-i', src, ...(trimSeconds ? ['-t', String(trimSeconds)] : []), '-an',
    '-vf', `scale=${width}:${height}:force_original_aspect_ratio=decrease,pad=${width}:${height}:(ow-iw)/2:(oh-ih)/2:color=#0f1117`,
    '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5',
    '-row-mt', '1', '-pix_fmt', 'yuv420p', '-r', String(fps), dst]);

export const waitForURL = (url, timeoutMs = 20000) => new Promise((resolve, reject) => {
  const deadline = Date.now() + timeoutMs;
  const attempt = () => http.get(url, (res) => { res.resume(); resolve(); })
    .on('error', (e) => {
      if (Date.now() > deadline) return reject(new Error(`waitFor ${url}: ${e.message}`));
      setTimeout(attempt, 250);
    });
  attempt();
});

// Start a background process (e.g. serve-live.mjs); caller kills it in teardown.
export const spawnLogged = (cmd, args, tag) => {
  const p = spawn(cmd, args, {stdio: ['ignore', 'pipe', 'pipe']});
  p.stdout.on('data', (d) => process.stdout.write(`  [${tag}] ${d}`));
  p.stderr.on('data', (d) => process.stdout.write(`  [${tag}!] ${d}`));
  return p;
};

export const readJSON = (f) => JSON.parse(fs.readFileSync(f, 'utf8'));
export const writeJSON = (f, v) => fs.writeFileSync(f, JSON.stringify(v, null, 2));
