// Shared ffmpeg/process helpers for the demo engine.
import {execFileSync, spawn} from 'node:child_process';
import fs from 'node:fs';
import http from 'node:http';

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
