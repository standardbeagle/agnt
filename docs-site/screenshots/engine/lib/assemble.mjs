// Assembly: uniform mezzanine segments (+ optional keep-range splicing for
// browser takes) → title cards → concat → optional edge-tts narration with
// burned + WebVTT captions. Generalized from narrate-assemble.mjs; that
// one-shot script stays untouched.
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import {chromium} from 'playwright';
import {ff, probeDur, normalize, readJSON, writeJSON, ttsKey, mezzKey, fileFastKey} from './util.mjs';

// --- content-keyed assembly cache seams -------------------------------------
// Both generate expensive artifacts (edge-tts audio, splice+normalize encodes)
// only on a miss. The actual generation is an injected `run` seam so unit tests
// can count invocations without ever spawning edge-tts or ffmpeg. A corrupt or
// unreadable cache entry falls through to a miss (regenerate); it is never a
// hard error. Cache writes are best-effort. Cache is per-demo + unbounded — out/
// is gitignored and hand-cleanable, so there is no eviction machinery.

// Synthesize one narration line (mp3 + srt) with a hit skipping edge-tts.
export const cachedTTS = (voice, rate, spoken, {cacheDir, mp3Out, srtOut}, deps) => {
  const {run, existsSync = fs.existsSync, copyFile = fs.copyFileSync, log = console.log} = deps;
  const key = ttsKey(voice, rate, spoken);
  const short = key.slice(0, 12);
  const cMp3 = path.join(cacheDir, key + '.mp3');
  const cSrt = path.join(cacheDir, key + '.srt');
  if (existsSync(cMp3) && existsSync(cSrt)) {
    try {
      copyFile(cMp3, mp3Out);
      copyFile(cSrt, srtOut);
      log(`  cache hit  tts ${short}`);
      return {mp3: mp3Out, srt: srtOut};
    } catch { /* corrupt/unreadable cache → fall through and regenerate */ }
  }
  log(`  cache miss tts ${short}`);
  run(voice, rate, spoken, mp3Out, srtOut);
  try { copyFile(mp3Out, cMp3); copyFile(srtOut, cSrt); } catch { /* cache write best-effort */ }
  return {mp3: mp3Out, srt: srtOut};
};

// Splice+normalize one take into its mezzanine with a hit skipping the encode.
export const cachedMezz = (takeFastKey, seg, view, {cacheDir, mezzOut}, deps) => {
  const {run, existsSync = fs.existsSync, copyFile = fs.copyFileSync, log = console.log} = deps;
  const key = mezzKey(takeFastKey, seg, view);
  const short = key.slice(0, 12);
  const cMezz = path.join(cacheDir, key + '.webm');
  if (existsSync(cMezz)) {
    try {
      copyFile(cMezz, mezzOut);
      log(`  cache hit  mezz ${short}`);
      return mezzOut;
    } catch { /* corrupt/unreadable cache → fall through and regenerate */ }
  }
  log(`  cache miss mezz ${short}`);
  run(mezzOut);
  try { copyFile(mezzOut, cMezz); } catch { /* cache write best-effort */ }
  return mezzOut;
};

const cardHTML = (kicker, title, sub, view) => `<!DOCTYPE html><html><head><style>
  body { margin:0; width:${view.width}px; height:${view.height}px; background:#0f1117; color:#e6e9ef;
         font:15px -apple-system,'Segoe UI',Roboto,sans-serif; display:flex; align-items:center; }
  .wrap { padding-left:120px; max-width:1000px; }
  .kicker { color:#4f8cff; font-size:22px; font-weight:700; letter-spacing:.14em; text-transform:uppercase; margin-bottom:18px; }
  h1 { font-size:64px; line-height:1.12; margin:0 0 22px; font-weight:800; letter-spacing:-.01em; }
  .sub { color:#9aa3b2; font-size:26px; line-height:1.45; }
  .bar { position:fixed; left:0; top:0; bottom:0; width:10px; background:linear-gradient(#4f8cff,#22c55e); }
  .brand { position:fixed; right:56px; bottom:44px; color:#9aa3b2; font-size:20px; }
  .brand b { color:#e6e9ef; } .brand span { color:#4f8cff; }
</style></head><body><div class="bar"></div><div class="wrap">
  ${kicker ? `<div class="kicker">${kicker}</div>` : ''}<h1>${title}</h1><div class="sub">${sub}</div>
</div><div class="brand"><b>agnt</b><span>·</span>dev</div></body></html>`;

// Keep-range endpoint grammar: "start" | "end" | "mark:<name>[+|-<seconds>]".
// A keep range [a, b] keeps footage from a to b; everything else in the take
// (e.g. scripted-agent latency) is spliced out.
const endpointSec = (tok, marks, takeDur) => {
  if (tok === 'start') return 0;
  if (tok === 'end') return takeDur;
  const m = tok.match(/^mark:([A-Za-z0-9_-]+)([+-][\d.]+)?$/);
  if (!m) throw new Error(`bad keep endpoint: ${tok}`);
  const e = marks.find((x) => x.name === m[1]);
  if (!e) throw new Error(`missing mark ${m[1]}`);
  return e.tMs / 1000 + (m[2] ? parseFloat(m[2]) : 0);
};

// Splice a recorded take down to its keep ranges → one mezzanine file.
const spliceTake = (seg, marks, workDir, view) => {
  const take = path.join(workDir, seg.id + '.webm');
  const takeDur = probeDur(take);
  if (!seg.keep?.length) return take;
  const pieces = [];
  seg.keep.forEach(([from, to], i) => {
    const a = endpointSec(from, marks, takeDur);
    const b = endpointSec(to, marks, takeDur);
    if (b - a < 0.2) return;
    const p = path.join(workDir, `${seg.id}-keep${i}.webm`);
    ff(['-i', take, '-ss', String(a), '-to', String(b), '-an',
      '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5',
      '-row-mt', '1', '-pix_fmt', 'yuv420p', '-r', String(view.fps), p]);
    pieces.push(p);
  });
  if (pieces.length === 1) return pieces[0];
  const lst = path.join(workDir, seg.id + '-keep.txt');
  fs.writeFileSync(lst, pieces.map((p) => `file '${p}'`).join('\n') + '\n');
  const joined = path.join(workDir, seg.id + '-spliced.webm');
  ff(['-f', 'concat', '-safe', '0', '-i', lst, '-c', 'copy', joined]);
  return joined;
};

// Burned-caption ASS style, shared by the final mux and its tests.
const SUB_STYLE = 'FontName=DejaVu Sans,FontSize=9,PrimaryColour=&H00FFFFFF,BorderStyle=4,BackColour=&HA0101317,Outline=0,Shadow=0,MarginV=22';
// The single vp9 video-encode flag set the final mux uses whenever it re-encodes.
const VP9_VIDEO = ['-c:v', 'libvpx-vp9', '-crf', '33', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-row-mt', '1'];
// EBU R128 single-pass loudness normalization for VO (standard web target).
const LOUDNORM = 'loudnorm=I=-16:TP=-1.5:LRA=11';
// Brand overlay defaults (Part A): top-right (bottom is captions + card brand),
// ~220px wide, 0.85 opacity — matching the create-demo-video skill recipe.
const BRAND_PAD = 32;
const BRAND_POS = {
  'top-right': `main_w-overlay_w-${BRAND_PAD}:${BRAND_PAD}`,
  'top-left': `${BRAND_PAD}:${BRAND_PAD}`,
  'bottom-right': `main_w-overlay_w-${BRAND_PAD}:main_h-overlay_h-${BRAND_PAD}`,
  'bottom-left': `${BRAND_PAD}:main_h-overlay_h-${BRAND_PAD}`,
};

// Resolve spec.brand into the pieces the overlay needs, or null when absent.
// A declared brand.image that is missing on disk is a hard error (no silent
// unbranded output). fileExists is injected so the check is unit-testable.
const resolveBrand = (brand, demoDir, fileExists) => {
  if (!brand) return null;
  const image = path.resolve(demoDir, brand.image);
  if (!fileExists(image)) throw new Error(`brand.image not found on disk: ${image}`);
  const position = brand.position || 'top-right';
  const xy = BRAND_POS[position];
  if (!xy) throw new Error(`brand.position invalid: '${position}' (use ${Object.keys(BRAND_POS).join(', ')})`);
  return {image, xy, width: brand.width || 220, opacity: brand.opacity ?? 0.85};
};

// Scale the logo to width, keep aspect, apply opacity → a [logo] link, ready to
// overlay onto whatever base link the caller names. `idx` is the brand input's
// ffmpeg input index (brand is always appended AFTER the audio inputs so the
// [1:a],[2:a],… narration indices are never disturbed).
const logoChain = (idx, brand) =>
  `[${idx}:v]scale=${brand.width}:-1,format=rgba,colorchannelmixer=aa=${brand.opacity}[logo]`;

// Final-mux filter-graph construction, extracted pure (args in → ffmpeg argv out)
// so it is unit-testable without ever spawning ffmpeg. Both the brand overlay
// (Part A) and the EBU R128 loudnorm (Part B) are filters folded in HERE, into
// the ONE final invocation — never a second encode pass.
//
// Returns {argv, encodeCount, path}:
//   - argv:        the args passed to `ff` (i.e. after its `-y -v error` prefix).
//   - encodeCount: video encodes this invocation performs (0 = stream-copy).
//   - path:        'silent' | 'silent-brand' | 'narrated' | 'narrated-brand'.
export const buildFinalMuxArgs = (spec, {silent, out, srtPath}, opts) => {
  const {voiced, totalDur, view, demoDir, fileExists = fs.existsSync} = opts;
  const brand = resolveBrand(spec.brand, demoDir, fileExists);

  if (!voiced.length) {
    // Silent path. Without brand it stays a byte-identical stream-copy; with a
    // brand it gains its one necessary encode to burn the overlay in.
    if (!brand) {
      return {argv: ['-i', silent, '-c', 'copy', out], encodeCount: 0, path: 'silent'};
    }
    const graph = `${logoChain(1, brand)};[0:v][logo]overlay=${brand.xy}[vout]`;
    return {
      argv: ['-i', silent, '-i', brand.image,
        '-filter_complex', graph, '-map', '[vout]',
        ...VP9_VIDEO, '-r', String(view.fps),
        '-t', String(totalDur), out],
      encodeCount: 1,
      path: 'silent-brand',
    };
  }

  // Narrated path already re-encodes to burn subtitles — overlay + loudnorm join
  // that same filter_complex for zero extra encodes.
  const audioIn = [], delays = [];
  voiced.forEach((v, i) => {
    audioIn.push('-i', v.mp3);
    delays.push(`[${i + 1}:a]adelay=${Math.round(v.at * 1000)}|${Math.round(v.at * 1000)}[a${i}]`);
  });
  const mix = `${delays.join(';')};${voiced.map((_, i) => `[a${i}]`).join('')}amix=inputs=${voiced.length}:normalize=0,${LOUDNORM}[voa]`;

  const brandInput = brand ? ['-i', brand.image] : [];
  const brandIdx = 1 + voiced.length; // 0 = silent video, 1..N = mp3s, N+1 = brand
  const videoGraph = brand
    ? `[0:v]subtitles=${srtPath}:force_style='${SUB_STYLE}'[subbed];${logoChain(brandIdx, brand)};[subbed][logo]overlay=${brand.xy}[vout]`
    : `[0:v]subtitles=${srtPath}:force_style='${SUB_STYLE}'[vout]`;

  const argv = ['-i', silent, ...audioIn, ...brandInput,
    '-filter_complex', `${mix};${videoGraph}`,
    '-map', '[vout]', '-map', '[voa]',
    ...VP9_VIDEO, '-r', String(view.fps),
    '-c:a', 'libopus', '-b:a', '96k',
    '-t', String(totalDur), out];
  return {argv, encodeCount: 1, path: brand ? 'narrated-brand' : 'narrated'};
};

export const assemble = async (spec, {demoDir, workDir, outDir}) => {
  const view = {...spec.viewport, fps: spec.fps || 25};
  const cacheDir = path.join(workDir, 'cache');
  fs.mkdirSync(cacheDir, {recursive: true});

  // --- 1. Mezzanine: cards rendered, takes spliced + normalized ------------
  const browser = await chromium.launch();
  const pg = await browser.newPage({viewport: {width: view.width, height: view.height}});
  const timeline = [];
  for (const seg of spec.segments) {
    let file;
    if (seg.type === 'card') {
      const png = path.join(workDir, seg.id + '.png');
      await pg.setContent(cardHTML(seg.kicker, seg.title, seg.sub, view));
      await pg.waitForTimeout(120);
      await pg.screenshot({path: png});
      file = path.join(workDir, seg.id + '.webm');
      ff(['-loop', '1', '-framerate', String(view.fps), '-i', png, '-t', String(seg.dur || 3.5), '-an',
        '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5',
        '-row-mt', '1', '-pix_fmt', 'yuv420p', file]);
    } else {
      const marksPath = path.join(workDir, seg.id + '.json');
      const marks = fs.existsSync(marksPath) ? readJSON(marksPath).events : [];
      const take = path.join(workDir, seg.id + '.webm');
      file = path.join(workDir, seg.id + '-mezz.webm');
      cachedMezz(fileFastKey(take), seg, view, {cacheDir, mezzOut: file}, {
        run: (mezzOut) => normalize(spliceTake(seg, marks, workDir, view), mezzOut, view, seg.trimSeconds),
      });
    }
    timeline.push({id: seg.id, file});
    console.log('  seg', seg.id, probeDur(file).toFixed(2) + 's');
  }
  await browser.close();

  let acc = 0;
  for (const t of timeline) { t.start = acc; t.dur = probeDur(t.file); acc += t.dur; }
  const totalDur = acc;
  const at = (id) => timeline.find((t) => t.id === id);
  console.log('  timeline', timeline.map((t) => `${t.id}@${t.start.toFixed(1)}`).join(' '), 'total', totalDur.toFixed(1) + 's');

  // --- 2. Narration (optional) ----------------------------------------------
  // anchor grammar in narration.json: "<seg-id>" (start), "<seg-id>+<sec>",
  // "<seg-id>+end" (right-aligned: line ENDS ~1s before the segment does).
  const voiced = [];
  if (spec.narration?.segments?.length) {
    const anchorSec = (tok, dur) => {
      const m = tok.match(/^([A-Za-z0-9_-]+)(?:\+(end|[\d.]+))?$/);
      if (!m) throw new Error(`bad narration anchor: ${tok}`);
      const seg = at(m[1]);
      if (!seg) throw new Error(`narration anchor ${m[1]} not a segment`);
      if (!m[2]) return seg.start + 0.4;
      if (m[2] === 'end') return Math.max(seg.start + 0.4, seg.start + seg.dur - dur - 1.0);
      return seg.start + parseFloat(m[2]);
    };
    const parseSRT = (file) => {
      const txt = fs.readFileSync(file, 'utf8').replace(/\r/g, '');
      const cues = [];
      for (const block of txt.split('\n\n')) {
        const m = block.match(/(\d+):(\d+):([\d.,]+)\s+-->\s+(\d+):(\d+):([\d.,]+)\n([\s\S]+)/);
        if (!m) continue;
        cues.push({
          start: (+m[1]) * 3600 + (+m[2]) * 60 + parseFloat(m[3].replace(',', '.')),
          end: (+m[4]) * 3600 + (+m[5]) * 60 + parseFloat(m[6].replace(',', '.')),
          text: m[7].trim().replace(/\n/g, ' '),
        });
      }
      return cues;
    };
    for (const n of spec.narration.segments) {
      const mp3 = path.join(workDir, 'vo-' + n.id + '.mp3');
      const srt = path.join(workDir, 'vo-' + n.id + '.srt');
      const spoken = n.text.replace(/\bagnt\b/gi, 'agent');
      cachedTTS(spec.narration.voice, spec.narration.rate, spoken, {cacheDir, mp3Out: mp3, srtOut: srt}, {
        run: (voice, rate, sp, mp3Out, srtOut) => execFileSync('edge-tts',
          ['--voice', voice, '--rate', rate, '--text', sp, '--write-media', mp3Out, '--write-subtitles', srtOut]),
      });
      const dur = probeDur(mp3);
      let cues = parseSRT(srt);
      // Rewrite cue text back to the original spelling ("agnt" ≠ "agent").
      const orig = n.text.split(/\s+/);
      if (cues.reduce((s, c) => s + c.text.split(/\s+/).length, 0) === orig.length) {
        let i = 0;
        cues = cues.map((c) => {
          const k = c.text.split(/\s+/).length;
          const text = orig.slice(i, i + k).join(' ');
          i += k;
          return {...c, text};
        });
      }
      voiced.push({...n, mp3, dur, at: anchorSec(n.at, dur), cues});
    }
  }

  // --- 3. Captions -----------------------------------------------------------
  const fmt = (t, sep = '.') => {
    const h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), s = (t % 60).toFixed(3).padStart(6, '0');
    return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${s.replace('.', sep)}`;
  };
  let vtt = 'WEBVTT\n\n', srtOut = '', idx = 0;
  for (const v of voiced) {
    for (const c of v.cues) {
      idx++;
      vtt += `${fmt(v.at + c.start)} --> ${fmt(v.at + c.end)}\n${c.text}\n\n`;
      srtOut += `${idx}\n${fmt(v.at + c.start, ',')} --> ${fmt(v.at + c.end, ',')}\n${c.text}\n\n`;
    }
  }

  // --- 4. Concat + mix + burn -------------------------------------------------
  const lst = path.join(workDir, 'concat.txt');
  fs.writeFileSync(lst, timeline.map((t) => `file '${t.file}'`).join('\n') + '\n');
  const silent = path.join(workDir, 'visual.webm');
  ff(['-f', 'concat', '-safe', '0', '-i', lst, '-c', 'copy', silent]);

  const out = path.join(outDir, spec.name + '.webm');
  let srtPath = null;
  if (voiced.length) {
    srtPath = path.join(workDir, 'captions.srt');
    fs.writeFileSync(srtPath, srtOut);
    fs.writeFileSync(path.join(outDir, spec.name + '.vtt'), vtt);
  }
  const {argv} = buildFinalMuxArgs(spec, {silent, out, srtPath}, {voiced, totalDur, view, demoDir});
  ff(argv);
  console.log('assembled', out, totalDur.toFixed(1) + 's' + (voiced.length ? '' : ' (silent)'));
  return {out, vtt: voiced.length ? path.join(outDir, spec.name + '.vtt') : null};
};
