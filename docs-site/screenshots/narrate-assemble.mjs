// Assemble the narrated debug-to-e2e demo:
//   videos/debug-to-e2e-live.webm  (one recorded take, record-live.mjs)
// + videos/debug-to-e2e-live.json (section-boundary marks)
// + narration.json                (voice, per-line narration)
// → videos/debug-to-e2e-narrated.webm (title cards, VO, burned captions)
//   videos/debug-to-e2e-narrated.vtt  (same captions as WebVTT)
//
// Voice + per-sentence caption timing come from edge-tts (neural TTS); each
// narration line renders to mp3 + srt, and its cues are offset onto the final
// timeline. Early lines in a section are placed at the section start; lines
// anchored "<sec>+end" are right-aligned to the section's end so within-take
// timing drift (live-agent sync latency) can't push them past their footage.
//
// Prereqs: edge-tts on PATH (pip install edge-tts), ffmpeg, playwright.
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {chromium} from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const vidDir = path.join(here, 'videos');
const work = path.join(vidDir, '.narrate');
fs.mkdirSync(work, {recursive: true});
const VIEW = {width: 1440, height: 900};
const FPS = 25;

const ff = (args) => execFileSync('ffmpeg', ['-y', '-v', 'error', ...args], {stdio: ['ignore', 'inherit', 'inherit']});
const probeDur = (f) => parseFloat(execFileSync('ffprobe', ['-v', 'error', '-show_entries', 'format=duration', '-of', 'csv=p=0', f]).toString());

const take = path.join(vidDir, 'debug-to-e2e-live.webm');
const marks = JSON.parse(fs.readFileSync(path.join(vidDir, 'debug-to-e2e-live.json'), 'utf8')).events;
const narration = JSON.parse(fs.readFileSync(path.join(here, 'narration.json'), 'utf8'));
const tOf = (name) => { const e = marks.find(m => m.name === name); if (!e) throw new Error('missing mark ' + name); return e.tMs / 1000; };

// --- 1. Split the take at section boundaries -------------------------------
// The waits on the live agent (panel-sent → reply-1, s3-summary → reply-2)
// are real transport latency; splice them out so the final cut stays tight.
const takeDur = probeDur(take);
const spans = {
  seg1: [[0, tOf('panel-sent') + 2.5], [tOf('reply-1') - 0.5, tOf('s2-start')]],
  seg2: [[tOf('s2-start'), tOf('s3-start')]],
  seg3: [[tOf('s3-start'), tOf('s3-summary') + 1.0], [tOf('reply-2') - 0.5, takeDur]],
};
const cut = [];
for (const [id, parts] of Object.entries(spans)) {
  const pieces = [];
  parts.forEach(([from, to], i) => {
    if (to - from < 0.2) return; // reply landed instantly — nothing to splice
    const p = path.join(work, `${id}-${i}.webm`);
    ff(['-i', take, '-ss', String(from), '-to', String(to), '-an', '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-row-mt', '1', '-r', String(FPS), p]);
    pieces.push(p);
  });
  let file;
  if (pieces.length === 1) {
    file = pieces[0];
  } else {
    file = path.join(work, id + '.webm');
    const lst = path.join(work, id + '-concat.txt');
    fs.writeFileSync(lst, pieces.map(p => `file '${p}'`).join('\n') + '\n');
    ff(['-f', 'concat', '-safe', '0', '-i', lst, '-c', 'copy', file]);
  }
  const c = {id, file, dur: probeDur(file)};
  cut.push(c);
  console.log(id, c.dur.toFixed(2) + 's');
}

// --- 2. Title cards --------------------------------------------------------
const cardHTML = (kicker, title, sub) => `<!DOCTYPE html><html><head><style>
  body { margin:0; width:1440px; height:900px; background:#0f1117; color:#e6e9ef;
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

const cards = [
  {id: 'card-intro', dur: 5.0, kicker: 'agnt demo', title: 'From bug report to e2e coverage', sub: 'One CSS bug — diagnosed, locked in, and test-covered through the floating proxy UI, with a live agent on the other side.'},
  {id: 'card-s1', dur: 3.2, kicker: 'Part 1 · Diagnose', title: 'Audit tools find the CSS trap', sub: 'Panel report → layout diagnostics → root cause → CSS audit'},
  {id: 'card-s2', dur: 3.2, kicker: 'Part 2 · Lock it in', title: 'Site-wide regression check', sub: 'Red on the broken page · fix applied · green — baseline saved'},
  {id: 'card-s3', dur: 3.2, kicker: 'Part 3 · Cover it', title: 'e2e tests for every failure path', sub: 'Seven cases driven through the dynamic form, observed and verified'},
  {id: 'card-outro', dur: 4.5, kicker: '', title: 'Give your agent the browser', sub: 'github.com/standardbeagle/agnt'},
];
{
  const browser = await chromium.launch();
  const pg = await browser.newPage({viewport: VIEW});
  for (const c of cards) {
    await pg.setContent(cardHTML(c.kicker, c.title, c.sub));
    await pg.waitForTimeout(120);
    c.png = path.join(work, c.id + '.png');
    await pg.screenshot({path: c.png});
    c.file = path.join(work, c.id + '.webm');
    ff(['-loop', '1', '-framerate', String(FPS), '-i', c.png, '-t', String(c.dur), '-an',
        '-c:v', 'libvpx-vp9', '-crf', '34', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-row-mt', '1', '-pix_fmt', 'yuv420p', c.file]);
  }
  await browser.close();
}

// --- 3. Final visual timeline ---------------------------------------------
const timeline = [
  cards[0],            // intro
  cards[1],            // part 1 card
  cut[0],              // diagnose
  cards[2],            // part 2 card
  cut[1],              // regression
  cards[3],            // part 3 card
  cut[2],              // form e2e
  cards[4],            // outro
];
let acc = 0;
for (const t of timeline) { t.start = acc; acc += t.dur ?? probeDur(t.file); }
const totalDur = acc;
console.log('final timeline', timeline.map(t => `${t.id}@${t.start.toFixed(1)}`).join(' '), 'total', totalDur.toFixed(1) + 's');
const segStart = (id) => timeline.find(t => t.id === id).start;
const segEnd = (id) => { const t = timeline.find(x => x.id === id); return t.start + (t.dur ?? probeDur(t.file)); };

// --- 4. TTS narration + caption cues --------------------------------------
// anchor grammar: "<timeline-id>" (start of that block) or "<timeline-id>+end"
// (right-aligned so the line ENDS ~1s before the block does).
const anchors = {
  'intro-card': () => segStart('card-intro') + 0.4,
  's1': () => segStart('seg1') + 0.5,
  's1+16': (dur) => Math.max(segStart('seg1') + 12, segEnd('seg1') - dur - 1.0),
  's2-card': () => segStart('card-s2') + 0.3,
  's3-card': () => segStart('card-s3') + 0.3,
  's3+24': (dur) => Math.max(segStart('seg3') + 18, segEnd('seg3') - dur - 1.5),
  'outro-card': () => segStart('card-outro') + 0.5,
};

const parseSRT = (file) => {
  const txt = fs.readFileSync(file, 'utf8').replace(/\r/g, '');
  const cues = [];
  for (const block of txt.split('\n\n')) {
    const m = block.match(/(\d+):(\d+):([\d.,]+)\s+-->\s+(\d+):(\d+):([\d.,]+)\n([\s\S]+)/);
    if (!m) continue;
    const s = (+m[1]) * 3600 + (+m[2]) * 60 + parseFloat(m[3].replace(',', '.'));
    const e = (+m[4]) * 3600 + (+m[5]) * 60 + parseFloat(m[6].replace(',', '.'));
    cues.push({start: s, end: e, text: m[7].trim().replace(/\n/g, ' ')});
  }
  return cues;
};

const voiced = [];
for (const seg of narration.segments) {
  const mp3 = path.join(work, seg.id + '.mp3');
  const srt = path.join(work, seg.id + '.srt');
  // "agnt" is pronounced "agent"; captions keep the product spelling, only
  // the spoken text is substituted (cue text is rewritten back below).
  const spoken = seg.text.replace(/\bagnt\b/gi, 'agent');
  execFileSync('edge-tts', ['--voice', narration.voice, '--rate', narration.rate,
    '--text', spoken, '--write-media', mp3, '--write-subtitles', srt]);
  const dur = probeDur(mp3);
  const at = anchors[seg.at](dur);
  // Rewrite cue text back to the original spelling: the substitution is
  // word-for-word, so map each cue's word span onto the original text.
  let cues = parseSRT(srt);
  const orig = seg.text.split(/\s+/);
  const cueWords = cues.reduce((s, c) => s + c.text.split(/\s+/).length, 0);
  if (cueWords === orig.length) {
    let i = 0;
    cues = cues.map(c => {
      const n = c.text.split(/\s+/).length;
      const text = orig.slice(i, i + n).join(' ');
      i += n;
      return {...c, text};
    });
  }
  voiced.push({...seg, mp3, dur, at, cues});
  console.log('VO', seg.id, dur.toFixed(1) + 's @', at.toFixed(1) + 's');
}

// --- 5. Captions (merged, offset onto the final timeline) ------------------
const fmtVTT = (t) => {
  const h = Math.floor(t / 3600), m = Math.floor((t % 3600) / 60), s = (t % 60).toFixed(3).padStart(6, '0');
  return `${String(h).padStart(2, '0')}:${String(m).padStart(2, '0')}:${s}`;
};
const fmtSRT = (t) => fmtVTT(t).replace('.', ',');
let vtt = 'WEBVTT\n\n', srtOut = '', idx = 0;
for (const v of voiced) {
  for (const c of v.cues) {
    idx++;
    const s = v.at + c.start, e = v.at + c.end;
    vtt += `${fmtVTT(s)} --> ${fmtVTT(e)}\n${c.text}\n\n`;
    srtOut += `${idx}\n${fmtSRT(s)} --> ${fmtSRT(e)}\n${c.text}\n\n`;
  }
}
fs.writeFileSync(path.join(vidDir, 'debug-to-e2e-narrated.vtt'), vtt);
const srtPath = path.join(work, 'captions.srt');
fs.writeFileSync(srtPath, srtOut);

// --- 6. Concat video, mix VO track, burn captions --------------------------
const concatList = path.join(work, 'concat.txt');
fs.writeFileSync(concatList, timeline.map(t => `file '${t.file}'`).join('\n') + '\n');
const silent = path.join(work, 'visual.webm');
ff(['-f', 'concat', '-safe', '0', '-i', concatList, '-c', 'copy', silent]);

const audioIn = [];
const delayFilters = [];
voiced.forEach((v, i) => {
  audioIn.push('-i', v.mp3);
  delayFilters.push(`[${i + 1}:a]adelay=${Math.round(v.at * 1000)}|${Math.round(v.at * 1000)}[a${i}]`);
});
const mix = `${delayFilters.join(';')};${voiced.map((_, i) => `[a${i}]`).join('')}amix=inputs=${voiced.length}:normalize=0[voa]`;
const subStyle = 'FontName=DejaVu Sans,FontSize=9,PrimaryColour=&H00FFFFFF,BorderStyle=4,BackColour=&HA0101317,Outline=0,Shadow=0,MarginV=22';
ff(['-i', silent, ...audioIn,
  '-filter_complex', `${mix};[0:v]subtitles=${srtPath}:force_style='${subStyle}'[vout]`,
  '-map', '[vout]', '-map', '[voa]',
  '-c:v', 'libvpx-vp9', '-crf', '33', '-b:v', '0', '-deadline', 'realtime', '-cpu-used', '5', '-row-mt', '1', '-r', String(FPS),
  '-c:a', 'libopus', '-b:a', '96k',
  '-t', String(totalDur),
  path.join(vidDir, 'debug-to-e2e-narrated.webm')]);
console.log('assembled debug-to-e2e-narrated.webm', probeDur(path.join(vidDir, 'debug-to-e2e-narrated.webm')).toFixed(1) + 's');
