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
import {toast as daemonToast, proxyStart, proxyStop, ping, chaosAddRule, chaosClear, exec as daemonExec} from './daemon.mjs';
import {spawnLogged, waitForURL, writeJSON} from './util.mjs';

// Generous liveness ceiling for the event-based injection settle. This is NOT a
// latency SLO — it is the "the bundle never came up at all" backstop. On an idle
// machine the gate resolves in well under this; under CPU load it waits exactly
// as long as the real signal takes, instead of the old fixed +1200ms floor.
const INJECT_SETTLE_CEILING_MS = 15000;

// Injection-readiness predicates: the ACTUAL runtime signals the recorder gates
// on, replacing the old fixed 1200ms settle. Each keys on (a) the frame's
// subsystem object being mounted AND (b) the websocket transport being OPEN
// (window.__devtool_core.isConnected()). Per lessons-ssh-transport.md #2: gate
// on the transport actually being live, not on an object merely existing or a
// timer firing earlier than the connection completes.
//
// These are drift-free across the two execution contexts: Playwright serializes
// the function source into the page (where `win` is undefined, so it reads the
// in-page `window`), while the unit test passes a fake window as `win`. One
// source, verified in Node, correct in the browser.
export function chromeInjectionReady(win) {
  win = win || (typeof window !== 'undefined' ? window : null);
  return !!(win && win.__devtool && win.__devtool.indicator &&
    win.__devtool_core && typeof win.__devtool_core.isConnected === 'function' &&
    win.__devtool_core.isConnected());
}
export function contentInjectionReady(win) {
  win = win || (typeof window !== 'undefined' ? window : null);
  return !!(win && win.__devtool && win.__devtool.toast &&
    win.__devtool_core && typeof win.__devtool_core.isConnected === 'function' &&
    win.__devtool_core.isConnected());
}

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

  // Chaos engineering on this demo's proxy (real CHAOS daemon commands).
  chaosAddRule(rule) { return chaosAddRule(this.env.PROXY_ID, rule, this.env.AGNT_SOCKET); }
  chaosClear() { return chaosClear(this.env.PROXY_ID, this.env.AGNT_SOCKET); }

  // The scripted agent acts: real PROXY EXEC in the content frame — the same
  // path MCP proxy exec/navigate take.
  agentExec(code) { return daemonExec(this.env.PROXY_ID, code, this.env.AGNT_SOCKET); }

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
  const waitForFrame = async (timeoutMs = 15000) => {
    const start = Date.now();
    for (;;) {
      try { return cf(); } catch {
        if (Date.now() - start > timeoutMs) throw new Error('content frame never attached');
        await pg.waitForTimeout(200);
      }
    }
  };
  if (seg.raw) {
    // Raw page (no proxy chrome / injected bundle) — for plain-page beats.
    await pg.goto(seg.url || env.UPSTREAM_URL || env.PROXY_URL, {waitUntil: 'load'});
  } else {
    await pg.goto(seg.url || env.PROXY_URL, {waitUntil: 'load'});
    // Event-based injection settle: wait for the injected bundle to be actually
    // live in each frame — chrome indicator mounted + ws OPEN, then content
    // toast subsystem live + ws OPEN — instead of a fixed +1200ms floor.
    const settleStart = Date.now();
    await pg.waitForFunction(chromeInjectionReady, undefined, {timeout: INJECT_SETTLE_CEILING_MS});
    await (await waitForFrame()).waitForFunction(contentInjectionReady, undefined, {timeout: INJECT_SETTLE_CEILING_MS});
    console.log('  injection settle', (Date.now() - settleStart) + 'ms',
      '(event-based, ws-connected gate; ceiling ' + INJECT_SETTLE_CEILING_MS + 'ms)');
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
