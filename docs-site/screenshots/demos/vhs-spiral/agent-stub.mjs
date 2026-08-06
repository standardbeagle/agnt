// Scripted agent stand-in for the vhs-spiral demo's failing attempts.
//
// The three "automate Claude Code with VHS" attempts must be deterministic so
// the demo regenerates in CI — a real agent session can't be replayed. This
// stub plays back a canned agent session (prompt echo, streamed "work", final
// verdict) from a JSON script, at human-ish cadence, for VHS to record.
// Swap for the real thing later: point the tape at `claude` instead.
//
//   node agent-stub.mjs attempts/1.json
//
// Script shape: {"lines": [{"text": "...", "kind": "in"|"out"|"edit"|"verdict", "delayMs": n}]}
// kind "in" is typed character-by-character (user/agent input); everything
// else is printed as streamed output.
import fs from 'node:fs';

const script = JSON.parse(fs.readFileSync(process.argv[2], 'utf8'));
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const write = (s) => process.stdout.write(s);

const ESC = '\u001b';
const COLORS = {
  reset: `${ESC}[0m`, dim: `${ESC}[2m`, bold: `${ESC}[1m`,
  blue: `${ESC}[34m`, green: `${ESC}[32m`, yellow: `${ESC}[33m`, red: `${ESC}[31m`,
};
const c = (k, s) => COLORS[k] + s + COLORS.reset;

for (const line of script.lines) {
  await sleep(line.delayMs ?? 400);
  if (line.kind === 'in') {
    write(c('blue', '❯ '));
    for (const ch of line.text) { write(ch); await sleep(45); }
    write('\n');
  } else if (line.kind === 'edit') {
    write(c('yellow', '✎ ' + line.text) + '\n');
  } else if (line.kind === 'verdict') {
    write('\n' + c('green', c('bold', line.text)) + '\n');
  } else {
    write(c('dim', line.text) + '\n');
  }
}
write('\n');
