// CLI segment: render a VHS tape from the demo spec and record it.
//
// Segment shape (in demo.json):
//   {
//     "id": "attempt-1",
//     "type": "cli",
//     "cwd": "./scratch",              // relative to the demo dir
//     "settings": {"FontSize": 22, "Theme": "Catppuccin Mocha", ...},
//     "tape": [ "Type \"claude\"", "Enter", "Sleep 1s", ... ],
//     "loop": 0                         // optional, VHS Set LoopOffset passthroughs live in settings
//   }
// `tape` lines are raw VHS commands — the engine adds Output + Set lines and
// runs `vhs`. Output is normalized to the demo mezzanine by the caller.
import {execFileSync} from 'node:child_process';
import fs from 'node:fs';
import path from 'node:path';

const DEFAULT_SETTINGS = {
  Shell: 'bash',
  FontSize: 22,
  Width: 1440,
  Height: 900,
  Theme: 'Catppuccin Mocha',
  Padding: 24,
  TypingSpeed: '60ms',
  // VHS synthesizes frame timestamps at the nominal framerate; under load the
  // capture loop drops frames and wall-clock time compresses (observed 3.5x
  // speedup at 25fps on a loaded 6-core box). Framerate 10 keeps tape time
  // honest; terminal content stays legible at 10fps.
  Framerate: 10,
  PlaybackSpeed: 1.0,
};

export const recordCLI = async (seg, {demoDir, workDir}) => {
  const out = path.join(workDir, seg.id + '.webm');
  const settings = {...DEFAULT_SETTINGS, ...(seg.settings || {})};
  const lines = [`Output "${out}"`];
  for (const [k, v] of Object.entries(settings)) {
    const val = typeof v === 'string' && /\s/.test(v) ? `"${v}"` : String(v);
    lines.push(`Set ${k} ${val}`);
  }
  if (!seg.tape?.length) throw new Error(`cli segment ${seg.id}: empty tape`);
  lines.push(...seg.tape);

  const tapePath = path.join(workDir, seg.id + '.tape');
  fs.writeFileSync(tapePath, lines.join('\n') + '\n');

  const cwd = seg.cwd ? path.resolve(demoDir, seg.cwd) : demoDir;
  fs.mkdirSync(cwd, {recursive: true});
  execFileSync('vhs', [tapePath], {cwd, stdio: ['ignore', 'inherit', 'inherit'], timeout: 10 * 60 * 1000});
  if (!fs.existsSync(out)) throw new Error(`cli segment ${seg.id}: vhs produced no output at ${out}`);
  return out;
};
