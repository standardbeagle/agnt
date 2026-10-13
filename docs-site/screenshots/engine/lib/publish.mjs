// `--publish`: promote a rendered demo into the docs-site as committed assets.
//
// Reads the assembled demo at demos/<dir>/out/<name>.webm (the gitignored
// output of a record/assemble run) and writes the assets <ModeVideo/> references:
//
//   docs-site/static/video/<dir>.webm        — the clip, copied verbatim
//   docs-site/static/video/<dir>.vtt         — captions, only when a sidecar exists
//   docs-site/static/img/<dir>-poster.webp   — 960-wide still from t=2s
//   docs-site/static/img/<dir>-demo.webp     — 8fps 720-wide looping animation
//
// The source is checked FIRST: a missing webm or a missing ffmpeg throws before
// anything is written. Every artifact — the captions included — is staged in a
// temp dir and installed only after all renders succeed, so a mid-run ffmpeg
// failure never leaves a partial poster/webp behind and a re-run overwrites
// deterministically.
//
// The install itself is all-or-nothing (installSet): each asset it replaces is
// set aside first, and a failure part-way through puts every one back, so
// docs-site never holds a new clip beside an old poster. A demo republished
// WITHOUT captions has its previously published .vtt removed in that same set —
// otherwise the old captions would keep playing over the new clip.
//
// Path construction and ffmpeg argv are pure; the filesystem and ffmpeg are
// injected (`deps`) so the ordering guarantees are unit-testable without either.
import {execFileSync, spawnSync} from 'node:child_process';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';

// A demo is addressed by its directory name, which becomes four file names
// under docs-site/static. Anything that is not a plain name could write outside
// those directories, so it is refused here rather than trusted from the caller.
export const DEMO_NAME_RE = /^[A-Za-z0-9][A-Za-z0-9._-]*$/;

export const assertDemoName = (name) => {
  if (typeof name !== 'string' || !DEMO_NAME_RE.test(name) || name.includes('..')) {
    throw new Error(`publish: invalid demo name ${JSON.stringify(name)} (letters, digits, '.', '_' and '-' only)`);
  }
  return name;
};

// Every path a publish reads or writes. `dirName` is the demo's directory (the
// published asset name); `specName` is demo.json's name, which is what assembly
// names its output after. They are normally identical.
export const publishPaths = ({demoDir, docsSiteDir, dirName, specName}) => {
  assertDemoName(dirName);
  const srcName = assertDemoName(specName || dirName);
  const srcDir = path.join(demoDir, 'out');
  const videoDir = path.join(docsSiteDir, 'static', 'video');
  const imgDir = path.join(docsSiteDir, 'static', 'img');
  return {
    srcWebm: path.join(srcDir, srcName + '.webm'),
    srcVtt: path.join(srcDir, srcName + '.vtt'),
    videoDir,
    imgDir,
    destWebm: path.join(videoDir, dirName + '.webm'),
    destVtt: path.join(videoDir, dirName + '.vtt'),
    destPoster: path.join(imgDir, dirName + '-poster.webp'),
    destAnim: path.join(imgDir, dirName + '-demo.webp'),
  };
};

// Poster: single frame at t=2s, scaled to 960 wide (height auto, even).
export const buildPosterArgs = (src, dst) =>
  ['-nostdin', '-y', '-v', 'error', '-ss', '2', '-i', src, '-frames:v', '1', '-vf', 'scale=960:-2', dst];

// Animated webp for README/GitHub: 8fps, 720 wide, infinite loop, no audio.
export const buildAnimArgs = (src, dst) =>
  ['-nostdin', '-y', '-v', 'error', '-i', src, '-vf', 'fps=8,scale=720:-2:flags=lanczos',
    '-loop', '0', '-an', '-c:v', 'libwebp', '-q:v', '70', dst];

export const embedSnippet = (dirName) =>
  `<ModeVideo src="/video/${dirName}.webm" poster="/img/${dirName}-poster.webp" label="..." />`;

const realDeps = {
  existsSync: fs.existsSync,
  mkdir: (d) => fs.mkdirSync(d, {recursive: true}),
  mkdtemp: () => fs.mkdtempSync(path.join(os.tmpdir(), 'demo-publish-')),
  copyFile: fs.copyFileSync,
  // rename fails across volumes (temp dir vs. repo checkout); fall back to copy.
  move: (from, to) => {
    try {
      fs.renameSync(from, to);
    } catch (e) {
      if (e.code !== 'EXDEV') throw e;
      fs.copyFileSync(from, to);
      fs.rmSync(from, {force: true});
    }
  },
  rmTree: (d) => fs.rmSync(d, {recursive: true, force: true}),
  haveFfmpeg: () => spawnSync('ffmpeg', ['-version'], {stdio: 'ignore'}).status === 0,
  run: (argv) => execFileSync('ffmpeg', argv, {stdio: ['ignore', 'inherit', 'inherit']}),
  log: console,
};

// Install a set of staged files as one unit. `installs` is [from, to] pairs;
// `removals` is destinations that must not exist afterwards. Anything already at
// a destination is moved into `backupDir` first. If any step throws, every file
// this call placed is removed and every file it set aside is moved back, then
// the error is rethrown — the destinations end as they started. `backupDir` is
// the caller's temp dir, so the set-aside copies are cleaned up with it.
export const installSet = (d, backupDir, installs, removals = []) => {
  const placed = [];
  const setAside = [];
  const aside = (to) => {
    if (!d.existsSync(to)) return;
    const bak = path.join(backupDir, `replaced-${setAside.length}-${path.basename(to)}`);
    d.move(to, bak);
    setAside.push([bak, to]);
  };
  try {
    for (const [from, to] of installs) {
      aside(to);
      d.move(from, to);
      placed.push(to);
    }
    for (const to of removals) aside(to);
  } catch (e) {
    for (const to of placed.reverse()) d.rmTree(to);
    for (const [bak, to] of setAside.reverse()) d.move(bak, to);
    throw e;
  }
};

// Publish one demo. Throws (leaving docs-site as it was) when the source is
// missing, ffmpeg is unavailable, a render fails, or an install step fails.
export const publishDemo = ({demoDir, docsSiteDir, dirName, specName}, deps = {}) => {
  const d = {...realDeps, ...deps};
  const p = publishPaths({demoDir, docsSiteDir, dirName, specName});

  if (!d.existsSync(p.srcWebm)) {
    throw new Error(
      `publish: source not found: ${p.srcWebm}\n` +
      `         record or assemble the demo first (make demo NAME=${dirName})`);
  }
  if (!d.haveFfmpeg()) {
    throw new Error('publish: ffmpeg not found on PATH (required for the poster and the animated webp)');
  }

  const hasVtt = d.existsSync(p.srcVtt);
  const tmp = d.mkdtemp();
  try {
    const tmpWebm = path.join(tmp, dirName + '.webm');
    const tmpPoster = path.join(tmp, dirName + '-poster.webp');
    const tmpAnim = path.join(tmp, dirName + '-demo.webp');

    const tmpVtt = path.join(tmp, dirName + '.vtt');

    d.copyFile(p.srcWebm, tmpWebm);
    // .vtt sidecar: staged with the rest when present, so it is installed (or
    // not) together with the clip it captions.
    if (hasVtt) d.copyFile(p.srcVtt, tmpVtt);
    d.run(buildPosterArgs(p.srcWebm, tmpPoster));
    d.run(buildAnimArgs(p.srcWebm, tmpAnim));
    // A render that exits 0 without producing its file (a take shorter than the
    // 2s poster seek, say) must not publish a clip with no poster.
    for (const f of [tmpPoster, tmpAnim]) {
      if (!d.existsSync(f)) throw new Error(`publish: ffmpeg produced no ${path.basename(f)} from ${p.srcWebm}`);
    }

    // All renders succeeded — only now touch docs-site.
    d.mkdir(p.videoDir);
    d.mkdir(p.imgDir);
    const installs = [[tmpWebm, p.destWebm], [tmpPoster, p.destPoster], [tmpAnim, p.destAnim]];
    if (hasVtt) installs.push([tmpVtt, p.destVtt]);
    // A silent demo must not keep captions published by an earlier, narrated run.
    installSet(d, tmp, installs, hasVtt ? [] : [p.destVtt]);
  } finally {
    d.rmTree(tmp);
  }

  const vtt = hasVtt ? p.destVtt : null;

  const embed = embedSnippet(dirName);
  d.log.log(`demo-publish: ${dirName}`);
  d.log.log(`  video   ${p.destWebm}${vtt ? ' + ' + vtt : ''}`);
  d.log.log(`  poster  ${p.destPoster}`);
  d.log.log(`  demo    ${p.destAnim}`);
  d.log.log('');
  d.log.log('Embed in docs with:');
  d.log.log('  ' + embed);
  return {video: p.destWebm, vtt, poster: p.destPoster, anim: p.destAnim, embed};
};
