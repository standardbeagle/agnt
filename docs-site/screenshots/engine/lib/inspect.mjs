// `--inspect` cut-point authoring aid: turn each recorded take in out/.work into
// a contact sheet (one thumbnail per second via ffmpeg's tile filter) with a
// timeline strip beneath it, and overlay every mark from <seg>.json at its
// timestamp so `mark:<name>±<s>` keep-range endpoints can be picked in one look
// instead of by trial-and-error re-assembly.
//
// It is strictly READ-ONLY over the takes: it reads only <seg>.webm + <seg>.json
// and writes only out/inspect/<seg>.png — it never touches a mezzanine or the
// assembled output, and it is a fully independent code path from assembly (so
// the content-keyed assembly cache is untouched). All geometry is pure so the
// mark→x mapping is unit-tested without ffmpeg; ffprobe/ffmpeg are injected
// seams. A missing take is a loud per-segment message, never a crash.
import fs from 'node:fs';
import path from 'node:path';
import {ff, probeDur, readJSON} from './util.mjs';

// Thumbnail width for each contact-sheet cell; height keeps the take's aspect.
export const THUMB_W = 320;
// Height (px) of the timeline strip drawn beneath the tiles (ruler + marks).
export const STRIP_H = 56;
// Contact-sheet sampling rate: one thumbnail per second of take.
export const TILE_FPS = 1;
// Contact-sheet columns (rows follow from the take length).
export const COLS = 8;
// Default label font — DejaVu Sans ships on the CI/dev boxes (see SUB_STYLE in
// assemble.mjs); when absent we fall back to fontconfig resolution (font=...).
export const DEFAULT_FONT = '/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf';

// Thumbnail cell size for a viewport, preserving aspect ratio.
export const thumbSize = (view, thumbW = THUMB_W) => ({
  w: thumbW,
  h: Math.round(thumbW * (view.height / view.width)),
});

// Number of thumbnails a take of `takeDur` seconds yields at `fps` (>= 1 frame,
// so a take shorter than a second still produces a one-cell sheet).
export const sheetFrameCount = (takeDur, fps = TILE_FPS) =>
  Math.max(1, Math.ceil(takeDur * fps));

// Grid layout for `numFrames` cells at `cols` columns, tile size tileW×tileH.
// Columns shrink to numFrames on a short take so there is never a trailing
// empty column; rows follow from the (possibly shrunk) column count.
export const tileGrid = (numFrames, cols, tileW, tileH) => {
  const c = Math.max(1, Math.min(cols, numFrames));
  const rows = Math.ceil(numFrames / c);
  return {cols: c, rows, tileW, tileH, sheetW: c * tileW, sheetH: rows * tileH};
};

// X-pixel of a mark at time `markT` (s) on a full-width timeline strip of width
// `stripW`, mapping [0, takeDur] → [0, stripW]. Clamped so a mark at/after the
// take end lands on the right edge (never past it) and a mark before the start
// lands on the left. THE cut-point placement math — unit-tested + mutation
// guarded. A non-positive duration maps everything to 0 (no divide-by-zero).
export const markStripX = (markT, takeDur, stripW) => {
  if (!(takeDur > 0)) return 0;
  const frac = Math.min(1, Math.max(0, markT / takeDur));
  return Math.round(frac * stripW);
};

// Sanitize a mark name for a single-quoted ffmpeg drawtext `text='...'` token.
// Mark names are already `[A-Za-z0-9_-]+` (+ the synthetic "end"); this is belt
// and braces so a stray char can never break out of the filter string.
const safeLabel = (name) => String(name).replace(/[^A-Za-z0-9_+.-]/g, '');

// Build the ffmpeg argv (the args `ff` receives AFTER its `-y -v error` prefix)
// that renders one take into a contact sheet with a labelled timeline strip.
// Pure: given the probed take duration + marks it returns {argv, grid, sheetW,
// sheetH, numFrames, marks:[{name,t,x}]} so tests can assert each mark's x
// without spawning ffmpeg. `opts.fontfile` threads a font path (null → rely on
// fontconfig).
export const buildInspectArgs = (take, outPng, takeDur, marks, view, opts = {}) => {
  const {cols = COLS, fps = TILE_FPS, thumbW = THUMB_W, stripH = STRIP_H, fontfile} = opts;
  const {w: tileW, h: tileH} = thumbSize(view, thumbW);
  const numFrames = sheetFrameCount(takeDur, fps);
  const grid = tileGrid(numFrames, cols, tileW, tileH);
  const {sheetW, sheetH} = grid;
  const stripTop = sheetH;

  const placed = (marks || []).map((m) => ({
    name: m.name,
    t: m.tMs / 1000,
    x: markStripX(m.tMs / 1000, takeDur, sheetW),
  }));

  const fontToken = fontfile ? `fontfile=${fontfile}:` : '';
  const filters = [
    `fps=${fps}`,
    `scale=${tileW}:${tileH}`,
    `tile=${grid.cols}x${grid.rows}`,
    // strip canvas beneath the tiles
    `pad=iw:ih+${stripH}:0:0:color=#0f1117`,
    // ruler baseline across the full strip width
    `drawbox=x=0:y=${stripTop + 1}:w=${sheetW}:h=2:color=#4f8cff@0.6:t=fill`,
  ];
  for (const p of placed) {
    // exact-position tick line at the mark x
    filters.push(`drawbox=x=${p.x}:y=${stripTop}:w=2:h=${stripH}:color=#22c55e:t=fill`);
    // label; x anchored to min(markX, sheetW-tw-2) so a right-edge mark's text
    // box cannot overflow the sheet. The tick above still marks the exact x.
    filters.push(
      `drawtext=${fontToken}text='${safeLabel(p.name)}':` +
      `x=min(${p.x}\\,${sheetW}-tw-2):y=${stripTop + 10}:` +
      `fontsize=13:fontcolor=white:box=1:boxcolor=#111317@0.85:boxborderw=3`);
  }

  const argv = ['-i', take, '-vf', filters.join(','), '-frames:v', '1', '-update', '1', outPng];
  return {argv, grid, sheetW, sheetH, numFrames, marks: placed};
};

// Render one segment's inspect sheet. Reads ONLY the take + its marks; writes
// ONLY outPng (under out/inspect). A missing take is a loud message + null (the
// caller counts it and continues); it never throws. ffprobe/ffmpeg are injected.
export const renderInspectSegment = (seg, {workDir, outDir, view}, deps) => {
  const {
    probe = probeDur, run = ff, existsSync = fs.existsSync,
    readMarks = readJSON, fontfile = DEFAULT_FONT, log = console,
  } = deps;
  const take = path.join(workDir, seg.id + '.webm');
  if (!existsSync(take)) {
    log.error(`inspect: segment '${seg.id}' has no recorded take at ${take} — record it first (skipping)`);
    return null;
  }
  const marksPath = path.join(workDir, seg.id + '.json');
  const marks = existsSync(marksPath) ? (readMarks(marksPath).events || []) : [];
  const takeDur = probe(take);
  const outPng = path.join(outDir, 'inspect', seg.id + '.png');
  const font = fontfile && existsSync(fontfile) ? fontfile : null;
  const {argv, marks: placed} = buildInspectArgs(take, outPng, takeDur, marks, view, {fontfile: font});
  run(argv);
  log.log(`inspect ${seg.id} → ${outPng}  (${marks.length} mark${marks.length === 1 ? '' : 's'}, ${takeDur.toFixed(1)}s)`);
  return {out: outPng, marks: placed, takeDur};
};

// Orchestrate `--inspect` over a demo: emit one contact sheet per browser/cli
// segment (cards have no take, so they are skipped). The only directory ever
// created is out/inspect. `opts.only` limits to a single segment id, loudly.
export const inspectDemo = (spec, {demoDir, workDir, outDir}, opts = {}, deps = {}) => {
  const only = opts.only || null;
  const view = {...spec.viewport, fps: spec.fps || 25};
  const {mkdir = (d) => fs.mkdirSync(d, {recursive: true}), log = console} = deps;

  const inspectDir = path.join(outDir, 'inspect');
  mkdir(inspectDir);

  const targets = spec.segments.filter((s) => s.type === 'browser' || s.type === 'cli');
  if (only && !targets.some((s) => s.id === only)) {
    log.error(`inspect: no browser/cli segment with id '${only}' in this demo (have: ${targets.map((s) => s.id).join(', ') || 'none'})`);
    return {results: [], missing: 0, inspectDir};
  }

  const results = [];
  let missing = 0;
  for (const seg of targets) {
    if (only && seg.id !== only) continue;
    const r = renderInspectSegment(seg, {workDir, outDir, view}, deps);
    if (r) results.push(r); else missing++;
  }
  return {results, missing, inspectDir};
};
