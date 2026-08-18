// Demo engine entry point.
//
//   node docs-site/screenshots/engine/demo.mjs demos/<name> [--assemble-only]
//
// A demo is a directory with a demo.json:
//   {
//     "name": "vhs-spiral",
//     "viewport": {"width": 1440, "height": 900},
//     "fps": 25,
//     "env": {"PROXY_ID": "demo", "PROXY_PORT": 18191, "UPSTREAM_PORT": 8021},
//     "setup": {
//       "upstream": "../serve-live.mjs",        // spawned, waited on waitFor
//       "waitFor": "http://127.0.0.1:8021/",
//       "proxy": {"id": "demo", "target": "http://127.0.0.1:8021", "port": 18191}
//     },
//     "segments": [
//       {"id": "card-intro", "type": "card", "dur": 4.5, "kicker": "...", "title": "...", "sub": "..."},
//       {"id": "attempt-1", "type": "cli", "settings": {...}, "tape": ["Type ...", ...]},
//       {"id": "fix", "type": "browser", "script": "./segments/fix.mjs",
//        "keep": [["start", "mark:bug-shown+2"], ["mark:fix-applied-0.5", "end"]]},
//       ...
//     ],
//     "narration": {"voice": "en-US-...", "rate": "+0%",
//                   "segments": [{"id": "n1", "at": "fix+0.5", "text": "..."}]}
//   }
//
// Recording order follows the segments array (cli and browser are recorded;
// cards are rendered at assembly). Output: demos/<name>/out/<name>.webm (+vtt).
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {spawnSync} from 'node:child_process';
import {recordCLI} from './lib/vhs.mjs';
import {recordBrowser, setupLiveStack} from './lib/browser.mjs';
import {assemble} from './lib/assemble.mjs';
import {readJSON} from './lib/util.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const screenshotsDir = path.dirname(here);

// Decide what to do with a spec's narration given whether edge-tts is available.
// Pure and side-effect-free so tests can inject the availability boolean instead
// of depending on the real PATH.
//   assemble   → keep narration (or there is none); proceed normally.
//   warn-drop  → narration.optional:true but edge-tts absent; drop VO, go silent.
//   abort      → narration required but edge-tts absent; caller must fail loud.
export function resolveNarrationMode(spec, edgeTtsAvailable) {
  if (!spec.narration || edgeTtsAvailable) return {mode: 'assemble'};
  if (spec.narration.optional === true) return {mode: 'warn-drop'};
  return {
    mode: 'abort',
    error:
      'edge-tts is not on PATH, but this demo declares narration. ' +
      'Install it with: pip install edge-tts ' +
      '(or set narration.optional:true in demo.json to assemble silent).',
  };
}

const isMain =
  process.argv[1] && path.resolve(process.argv[1]) === fileURLToPath(import.meta.url);
if (!isMain) {
  // Imported (e.g. by the test harness): expose the pure helpers only, run nothing.
} else {

const demoArg = process.argv[2];
const assembleOnly = process.argv.includes('--assemble-only');
const onlyArg = process.argv.find((a) => a.startsWith('--only='));
const only = onlyArg ? new Set(onlyArg.slice(7).split(',')) : null;
if (!demoArg) {
  console.error('usage: node engine/demo.mjs <demo-dir> [--assemble-only] [--only=id1,id2]');
  process.exit(2);
}
const demoDir = path.resolve(screenshotsDir, demoArg);
const spec = readJSON(path.join(demoDir, 'demo.json'));
spec.viewport = spec.viewport || {width: 1440, height: 900};

// Narration needs edge-tts. A demo written WITH narration must fail loud rather
// than silently assemble silent — unless it explicitly opted into that fallback.
const narration = resolveNarrationMode(spec, spawnSync('which', ['edge-tts']).status === 0);
if (narration.mode === 'abort') {
  console.error(narration.error);
  process.exit(1);
} else if (narration.mode === 'warn-drop') {
  console.warn('edge-tts not on PATH — dropping optional narration, assembling silent');
  delete spec.narration;
}

const workDir = path.join(demoDir, 'out', '.work');
const outDir = path.join(demoDir, 'out');
fs.mkdirSync(workDir, {recursive: true});

const env = {
  AGNT_SOCKET: process.env.AGNT_SOCKET,
  ...(spec.env || {}),
};
if (spec.setup?.proxy) {
  env.PROXY_ID = spec.setup.proxy.id;
  env.PROXY_URL = env.PROXY_URL || `http://127.0.0.1:${spec.setup.proxy.port}/`;
}
if (spec.setup?.waitFor) env.UPSTREAM_URL = spec.setup.waitFor;

if (assembleOnly) {
  await assemble(spec, {demoDir, workDir, outDir});
  process.exit(0);
}

let stack = {stop: async () => {}};
try {
  if (spec.setup) stack = await setupLiveStack(demoDir, spec.setup, env);

  for (const seg of spec.segments) {
    if (only && !only.has(seg.id)) continue;
    if (seg.type === 'cli') {
      console.log('segment', seg.id, '(cli/vhs)');
      await recordCLI(seg, {demoDir, workDir});
    } else if (seg.type === 'browser') {
      console.log('segment', seg.id, '(browser)');
      await recordBrowser(seg, {demoDir, workDir, viewport: spec.viewport, env});
    }
  }
} finally {
  await stack.stop();
}

await assemble(spec, {demoDir, workDir, outDir});

}
