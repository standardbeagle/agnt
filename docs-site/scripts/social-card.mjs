// Regenerates static/img/agnt-social-card.png — the Open Graph card Slack,
// Discord, X, and LinkedIn render when someone shares a docs link.
//
//   npm run social-card
//
// Edit scripts/social-card.html, re-run, commit both files. The 1200x630 size
// is pinned in docusaurus.config.ts (og:image:width/height) — keep them in sync.
import {chromium} from 'playwright';
import {fileURLToPath} from 'node:url';
import path from 'node:path';

const here = path.dirname(fileURLToPath(import.meta.url));
const source = path.join(here, 'social-card.html');
const out = path.join(here, '..', 'static', 'img', 'agnt-social-card.png');

// Prefer Playwright's own browser; fall back to a system Chrome so this works
// without `npx playwright install`.
async function launch() {
  try {
    return await chromium.launch();
  } catch (err) {
    for (const channel of ['chrome', 'chromium', 'msedge']) {
      try {
        return await chromium.launch({channel});
      } catch {}
    }
    throw err;
  }
}

const browser = await launch();
const page = await browser.newPage({viewport: {width: 1200, height: 630}, deviceScaleFactor: 1});
await page.goto(`file://${source}`);
await page.screenshot({path: out});
await browser.close();

console.log(`wrote ${out}`);
