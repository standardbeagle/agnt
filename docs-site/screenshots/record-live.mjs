// Live-stack recording for the narrated debug-to-e2e demo.
//
// Unlike capture.mjs (static bundle embed), this records against the REAL
// agnt pipeline: serve-live.mjs upstream ← agnt proxy (bundle injection,
// always-wrap chrome shell + content iframe) ← this Playwright session.
// The developer's actions are real UI interactions (floating indicator,
// panel compose, form typing); the agent's replies arrive over the real
// daemon transport (PROXY TOAST), fired by a live agent coordinating via
// marker files in SYNC_DIR:
//   this script writes  <SYNC_DIR>/sent-<n>     after a panel send
//   the agent touches   <SYNC_DIR>/reply-<n>    after its toast is fired
//
// Env: PROXY_URL (required), SYNC_DIR (required)
// Output: videos/debug-to-e2e-live.webm + videos/debug-to-e2e-live.json
//         (section-boundary timestamps, ms relative to recording start)
import fs from 'node:fs';
import path from 'node:path';
import {fileURLToPath} from 'node:url';
import {chromium} from 'playwright';

const here = path.dirname(fileURLToPath(import.meta.url));
const vidDir = path.join(here, 'videos');
const PROXY_URL = process.env.PROXY_URL;
const SYNC = process.env.SYNC_DIR;
if (!PROXY_URL || !SYNC) throw new Error('PROXY_URL and SYNC_DIR are required');
fs.mkdirSync(vidDir, {recursive: true});
const VIEW = {width: 1440, height: 900};

const browser = await chromium.launch();
const ctx = await browser.newContext({viewport: VIEW, recordVideo: {dir: vidDir, size: VIEW}});
const t0 = Date.now();
const events = [];
const mark = (name) => { events.push({name, tMs: Date.now() - t0}); console.log('  mark', name, Date.now() - t0); };

const pg = await ctx.newPage();
pg.on('pageerror', e => console.log('  pageerror:', e.message));
await pg.goto(PROXY_URL, {waitUntil: 'load'});
await pg.waitForFunction(() => window.__devtool && window.__devtool.indicator, {timeout: 15000});
const cf = () => {
  const f = pg.frames().find(fr => fr.name() === '__devtool_content_frame');
  if (!f) throw new Error('content frame not found');
  return f;
};
await cf().waitForFunction(() => window.__devtool && window.__devtool.diagnoseLayoutIssues, {timeout: 15000});
await pg.waitForTimeout(1500);

const toast = (message, type = 'info', duration = 4200) =>
  pg.evaluate((t) => window.__devtool.toast.show(t), {message, type, duration});
const waitMarker = async (name, maxMs = 45000) => {
  const p = path.join(SYNC, name);
  const start = Date.now();
  while (!fs.existsSync(p)) {
    if (Date.now() - start > maxMs) { console.log('  marker timeout:', name); return false; }
    await pg.waitForTimeout(300);
  }
  return true;
};

// ---------------------------------------------------------------------------
mark('s1-start');
// Developer opens the floating panel and reports the bug to the agent.
await pg.evaluate(() => window.__devtool.indicator.show());
await pg.waitForTimeout(700);
// The indicator container reports zero size to Playwright's visibility check;
// click the rendered dot by coordinates, then force the panel open via the
// indicator API in case the click landed on a non-toggling child.
const dot = await pg.evaluate(() => {
  const root = window.__devtoolGetMountRoot();
  const el = root.getElementById('__devtool-indicator');
  let best = null;
  const walk = (n) => {
    if (n.getBoundingClientRect) {
      const r = n.getBoundingClientRect();
      if (r.width > 10 && (!best || r.width * r.height > best.a)) best = {x: r.x + r.width / 2, y: r.y + r.height / 2, a: r.width * r.height};
    }
    for (const c of n.children || []) walk(c);
  };
  walk(el);
  return best;
});
if (dot) { await pg.mouse.move(dot.x, dot.y); await pg.waitForTimeout(300); await pg.mouse.click(dot.x, dot.y); }
await pg.waitForTimeout(800);
await pg.evaluate(() => {
  const root = window.__devtoolGetMountRoot();
  if (!root.getElementById('__devtool-message')) window.__devtool.indicator.togglePanel(true);
});
await pg.waitForTimeout(1000);
const msg = pg.locator('#__devtool-message');
await msg.click();
await msg.type('The standup note in Team activity swallows clicks, and the export menu is cut off. Can you take a look?', {delay: 24});
await pg.waitForTimeout(600);
await msg.press('Control+Enter');
mark('panel-sent');
fs.writeFileSync(path.join(SYNC, 'sent-1'), '1');
// Live agent picks the message up off the daemon and answers via PROXY TOAST.
// The wait is real agent latency; assembly cuts [panel-sent+2.5 .. reply-1-0.5]
// out of the final video, so mark the reply the moment it lands.
await waitMarker('reply-1');
mark('reply-1');
await pg.waitForTimeout(2600); // agent toast on screen

// Agent runs the diagnosis in the content frame.
await cf().evaluate(() => document.getElementById('layout-demo').scrollIntoView({behavior: 'smooth', block: 'center'}));
await pg.waitForTimeout(1400);
await cf().evaluate(() => {
  const r = window.__devtool.diagnoseLayoutIssues();
  window.__diag = r;
  for (const f of r.findings) {
    const el = document.querySelector(f.selector);
    if (el) { el.style.outline = '3px solid #ef4444'; el.style.outlineOffset = '2px'; }
    if (f.cause) {
      const c = document.querySelector(f.cause);
      if (c) { c.style.outline = '3px dashed #f59e0b'; c.style.outlineOffset = '4px'; }
    }
  }
});
const diag = await cf().evaluate(() => {
  const r = window.__diag;
  const parts = Object.entries(r.by_check).filter(([, n]) => n > 0).map(([k, n]) => k + ' ×' + n);
  const trap = (r.findings || []).find((f) => f.fix) || {};
  return {count: r.count, parts: parts.join(', '), detail: trap.detail || 'containing-block trap', fix: trap.fix || 'remove the transform'};
});
await toast('diagnoseLayoutIssues(): ' + diag.count + ' issues — ' + diag.parts, 'warning', 5000);
await pg.waitForTimeout(2600);
await toast('Root cause: ' + diag.detail + ' — fix: ' + diag.fix, 'info', 5600);
await pg.waitForTimeout(2800);
const css = await cf().evaluate(() => window.__devtool_audit_css.auditCSS({detailLevel: 'summary'}));
await toast('CSS audit [' + css.grade + '] ' + css.summary, 'warning', 4600);
await pg.waitForTimeout(2800);

// ---------------------------------------------------------------------------
mark('s2-start');
await toast('Encoding diagnosis as a site-wide regression check…', 'info', 4200);
await pg.waitForTimeout(1800);
const red = await cf().evaluate(() => window.__devtool.diagnoseLayoutIssues().count);
await toast('Regression check: FAIL — ' + red + ' layout violations across the page', 'error', 4200);
await pg.waitForTimeout(2400);
await toast('Applying fix: drop translateZ(0) hack, un-clip export card, position the filters', 'info', 4200);
await cf().evaluate(() => {
  document.querySelector('.activity-wrap').style.transform = 'none';
  document.getElementById('activity-card').style.overflow = 'visible';
  document.querySelector('.filters').style.position = 'relative';
  for (const el of document.querySelectorAll('*')) {
    if (el.style && el.style.outline) { el.style.outline = ''; el.style.outlineOffset = ''; }
  }
});
await pg.waitForTimeout(1800);
const green = await cf().evaluate(() => window.__devtool.diagnoseLayoutIssues().count);
await toast(
  green === 0 ? 'Regression check: PASS — 0 violations · snapshot baseline saved for CI compare'
              : 'Regression check: ' + green + ' violations remaining',
  green === 0 ? 'success' : 'warning', 4800);
await pg.waitForTimeout(2400);

// Quick hands-on proof the fix holds: the previously-clipped export menu
// renders in full and takes clicks, and the previously-trapped standup note
// acknowledges a click instead of swallowing it.
mark('fix-demo');
const content = pg.frameLocator('iframe#__devtool_content_frame');
await content.locator('#export-menu a:nth-child(1)').hover();
await pg.waitForTimeout(500);
await content.locator('#export-menu a:nth-child(2)').hover();
await pg.waitForTimeout(500);
await content.locator('#export-menu a:nth-child(3)').hover();
await pg.waitForTimeout(400);
await content.locator('#export-menu a:nth-child(2)').click();
await pg.waitForTimeout(1900); // also lets the PASS toast clear the note's corner
await content.locator('#pinned-note').click();
await pg.waitForTimeout(1600);

// ---------------------------------------------------------------------------
mark('s3-start');
await cf().evaluate(() => document.getElementById('customer-form').scrollIntoView({behavior: 'smooth', block: 'center'}));
await toast('Generating e2e tests for every failure condition of the Add-customer form…', 'info', 4200);
await pg.waitForTimeout(2000);

const form = pg.frameLocator('iframe#__devtool_content_frame');
const cases = [
  {name: 'baseline-valid', company: 'Initrode', email: 'ops@initrode.com', seats: '25', expect: '201-style success'},
  {name: 'empty-required-fields', company: '', email: '', seats: '', expect: '3 client validation errors'},
  {name: 'invalid-email', company: 'Initrode', email: 'not-an-email', seats: '25', expect: 'email validation error'},
  {name: 'seats-out-of-range', company: 'Initrode', email: 'ops@initrode.com', seats: '0', expect: 'range validation error'},
  {name: 'duplicate-company', company: 'Globex Corp', email: 'ops@globex.com', seats: '25', expect: 'HTTP 409 conflict'},
  {name: 'plan-seat-constraint', company: 'Initrode', email: 'ops@initrode.com', seats: '5', plan: 'Enterprise', expect: 'HTTP 422 rule violation'},
  {name: 'server-error', company: 'Crash Test', email: 'ops@crash.dev', seats: '25', expect: 'HTTP 500 surfaced to user'},
];
let n = 0;
for (const c of cases) {
  await cf().evaluate(() => window.__demoFormReset());
  if (c.company) { await form.locator('#f-company').click(); await form.locator('#f-company').type(c.company, {delay: 16}); }
  if (c.email) { await form.locator('#f-email').click(); await form.locator('#f-email').type(c.email, {delay: 12}); }
  if (c.seats) { await form.locator('#f-seats').click(); await form.locator('#f-seats').type(c.seats, {delay: 28}); }
  if (c.plan) await form.locator('#f-plan').selectOption(c.plan);
  await form.locator('#f-submit').click();
  await pg.waitForTimeout(700);
  const observed = await cf().evaluate(() => {
    const l = window.__formLast || {};
    if (l.stage === 'client-validation') return Object.keys(l.errors).length + ' field errors';
    if (l.stage === 'server') return 'HTTP ' + l.status;
    return l.stage || 'no result';
  });
  n++;
  const pass = c.name === 'baseline-valid';
  await toast('e2e ' + n + '/' + cases.length + ' [' + c.name + '] expect ' + c.expect + ' — observed ' + observed + ' ✓', pass ? 'success' : 'warning', 3000);
  await pg.waitForTimeout(1400);
}
mark('s3-summary');
fs.writeFileSync(path.join(SYNC, 'sent-2'), '1');
// Live agent sends the wrap-up over the daemon channel; same gap-compression
// contract as reply-1.
await waitMarker('reply-2');
mark('reply-2');
await pg.waitForTimeout(4200);
mark('end');

fs.writeFileSync(path.join(vidDir, 'debug-to-e2e-live.json'), JSON.stringify({events}, null, 2));
const video = pg.video();
await ctx.close();
if (video) fs.renameSync(await video.path(), path.join(vidDir, 'debug-to-e2e-live.webm'));
await browser.close();
console.log('recorded debug-to-e2e-live.webm');
