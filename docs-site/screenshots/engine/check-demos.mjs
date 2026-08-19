// `make demo-check`: validate EVERY demos/*/demo.json against the demo-engine
// schema (lib/schema.mjs). Prints one line per demo and exits non-zero, naming
// the offending file and each error, if any demo is malformed. Pure node — no
// ffmpeg, no chromium, no daemon — so it is trivial to run anywhere and in CI.
//
//   node docs-site/screenshots/engine/check-demos.mjs
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {validateDemoSpec} from './lib/schema.mjs';

const here = path.dirname(fileURLToPath(import.meta.url));
const screenshotsDir = path.dirname(here);
const demosDir = path.join(screenshotsDir, 'demos');

const specs = fs.readdirSync(demosDir, {withFileTypes: true})
  .filter((d) => d.isDirectory())
  .map((d) => path.join(demosDir, d.name, 'demo.json'))
  .filter((p) => fs.existsSync(p))
  .sort();

if (specs.length === 0) {
  console.error(`demo-check: no demos/*/demo.json found under ${demosDir}`);
  process.exit(1);
}

let failed = 0;
for (const specPath of specs) {
  const rel = path.relative(screenshotsDir, specPath);
  let spec;
  try {
    spec = JSON.parse(fs.readFileSync(specPath, 'utf8'));
  } catch (e) {
    console.error(`FAIL  ${rel}\n        not valid JSON: ${e.message}`);
    failed++;
    continue;
  }
  const {ok, errors} = validateDemoSpec(spec);
  if (ok) {
    console.log(`ok    ${rel}`);
  } else {
    console.error(`FAIL  ${rel}`);
    for (const err of errors) console.error(`        ${err}`);
    failed++;
  }
}

if (failed > 0) {
  console.error(`\ndemo-check: ${failed} of ${specs.length} demo(s) failed the schema`);
  process.exit(1);
}
console.log(`\ndemo-check: all ${specs.length} demos pass the schema`);
