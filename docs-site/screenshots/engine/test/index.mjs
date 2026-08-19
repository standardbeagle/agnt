// Entry point so `node --test docs-site/screenshots/engine/test/` (which resolves
// a directory to its package main) loads the whole suite. `node --test` from this
// directory with no path argument also discovers the *.test.mjs files directly.
import './schema.test.mjs';
import './narration-gating.test.mjs';
import './final-mux.test.mjs';
import './assembly-cache.test.mjs';
import './inspect.test.mjs';
import './injection-settle.test.mjs';
