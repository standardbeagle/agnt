// Browser segment driver: runs a demo's browser scene against the REAL agnt
// stack (upstream + daemon proxy injecting the bundle) and records it with
// Playwright. Extracts the reusable plumbing from record-live.mjs and adds a
// scripted agent: instead of a human/live agent answering SYNC_DIR markers,
// the segment script fires agent replies itself through the daemon socket
// (PROXY TOAST) — deterministic, CI-regenerable.
//
// A browser segment is an .mjs module in the demo dir exporting:
//   export default async function run(d) { ... }
// where `d` is the driver below. Marks are written to <out>.json for
// keep-range splicing at assembly.
import fs from 'node:fs';
import path from 'node:path';
import {chromium} from 'playwright';
import {toast as daemonToast, proxyStart, proxyStop, ping} from './daemon.mjs';
import {spawnLogged, waitForURL, writeJSON} from './util.mjs';

export class BrowserDriver {
  constructor(pg, cf, mark, env) {
    this.page = pg;
    this.cf = cf;           // () => content frame (inside the proxy chrome)
    this.mark = mark;       // (name) => timestamped mark for keep-ranges
    this.env = env;         // demo env (PROXY_URL, PROXY_ID, UPSTREAM_URL, ...)
    this.viewport = pg.viewportSize();
  }

  // Toast rendered by the page's own injected bundle (narration-style beats).
  toast(message, type = 'info', duration = 4200) {
    return this.page.evaluate((t) => window.__devtool.toast.show(t), {message, type, duration});
  }

  // The scripted agent speaks: a real PROXY TOAST over the daemon socket,
  // indistinguishable on screen from a live agent's reply.
  agentToast(message, type = 'info', duration = 4200, title = 'agent') {
    return daemonToast(this.env.PROXY_ID, {message, type, title, duration}, this.env.AGNT_SOCKET);
  }

  sleep(ms) { return this.page.waitForTimeout(ms); }
}

// Bring up the live stack per demo setup config. Returns {proxyURL, stop()}.
export const setupLiveStack = async (demoDir, setup, env) => {
  const children = [];
  const stop = async () => {
    if (setup.proxy?.stop !== false && env.PROXY_ID) {
      try { await proxyStop(env.PROXY_ID, env.AGNT_SOCKET); } catch { /* already gone */ }
    }
    for (const c of children) c.kill('SIGTERM');
  };

  if (setup.upstream) {
    const up = spawnLogged('node', [path.resolve(demoDir, setup.upstream)], 'upstream');
    children.push(up);
    await waitForURL(setup.waitFor, 20000);
  }

  if (setup.proxy) {
    await ping(env.AGNT_SOCKET).catch(() => {
      throw new Error('agnt daemon socket not reachable — start the daemon first (agnt daemon start)');
    });
    const res = await proxyStart({id: setup.proxy.id, target: setup.proxy.target, port: setup.proxy.port}, env.AGNT_SOCKET);
    console.log('  proxy:', res);
  }
  return {stop};
};

// Record one browser segment; returns {video, marks}.
export const recordBrowser = async (seg, {demoDir, workDir, viewport, env}) => {
  const scriptPath = path.resolve(demoDir, seg.script);
  const mod = await import(scriptPath);
  if (typeof mod.default !== 'function') throw new Error(`${seg.script}: must export default async function`);

  const browser = await chromium.launch();
  const ctx = await browser.newContext({viewport, recordVideo: {dir: workDir, size: viewport}});
  const t0 = Date.now();
  const events = [];
  const mark = (name) => { events.push({name, tMs: Date.now() - t0}); console.log('  mark', name, Date.now() - t0); };

  const pg = await ctx.newPage();
  pg.on('pageerror', (e) => console.log('  pageerror:', e.message));
  const cf = () => {
    const f = pg.frames().find((fr) => fr.name() === '__devtool_content_frame');
    if (!f) throw new Error('content frame not found');
    return f;
  };
  if (seg.raw) {
    // Raw page (no proxy chrome / injected bundle) — for plain-page beats.
    await pg.goto(seg.url || env.UPSTREAM_URL || env.PROXY_URL, {waitUntil: 'load'});
  } else {
    await pg.goto(seg.url || env.PROXY_URL, {waitUntil: 'load'});
    await pg.waitForFunction(() => window.__devtool && window.__devtool.indicator, {timeout: 15000});
    await cf().waitForFunction(() => window.__devtool && window.__devtool.toast, {timeout: 15000});
    await pg.waitForTimeout(1200); // injection settle, matches record-live
  }

  const d = new BrowserDriver(pg, cf, mark, env);
  try {
    await mod.default(d, seg.args || {});
  } finally {
    mark('end');
    const video = pg.video();
    await ctx.close();
    await browser.close();
    const out = path.join(workDir, seg.id + '.webm');
    if (video) fs.renameSync(await video.path(), out);
    writeJSON(path.join(workDir, seg.id + '.json'), {events});
    console.log('  recorded', seg.id + '.webm');
  }
  return {video: path.join(workDir, seg.id + '.webm'), marks: events};
};
