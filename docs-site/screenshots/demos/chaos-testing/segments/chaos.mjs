// Chaos testing: the loading cascade runs clean, then real CHAOS rules hit
// /api/report — 3s latency, then hard 500s — and the incident stream catches
// what the page swallows. Rules cleared, the cascade is fast again.
export default async function run(d) {
  const cf = d.cf();

  // Baseline: cascade at mock latency (~200-400ms per call).
  await cf.locator('#report-grid').scrollIntoViewIfNeeded();
  await d.sleep(800);
  await cf.evaluate(() => window.__demoLoadingCascade());
  await d.sleep(2600);
  d.mark('baseline');

  await d.toast('Baseline: report cascade completes in ~2s at mock latency', 'info', 4200);
  await d.sleep(2400);

  // Rule 1: latency floor on /api/report. The demo cascade is schedule-driven
  // (fixed sleeps), so it can't show slowness — this phase uses a real awaited
  // fetch with a labeled spinner, so the wait on screen IS the response time.
  await d.agentToast('Chaos rule 1: /api/report now answers in 2.5–3.5s. Same page, same code.', 'warning', 5200);
  await d.chaosAddRule({
    id: 'slow-report', type: 'latency', url_pattern: '/api/report',
    min_latency_ms: 2500, max_latency_ms: 3500, probability: 1.0,
  });
  d.mark('latency-rule');
  await cf.evaluate(() => {
    const status = document.getElementById('report-status');
    status.innerHTML = '';
    const step = document.createElement('span');
    step.className = 'step';
    step.id = 'latency-probe';
    step.innerHTML = '<span class="spinner"></span>&nbsp;Loading revenue report… <span style="font-size:11px;color:var(--mut)">(waiting on /api/report)</span>';
    status.appendChild(step);
    // Resolve with the true response seconds — the number on screen is measured.
    window.__latencyProbe = (function () {
      const start = performance.now();
      return fetch('/api/report?kind=revenue').then((r) => r.json())
        .then(() => Math.round((performance.now() - start) / 100) / 10);
    })();
  });
  await d.sleep(1600);

  // Still pending well past baseline (420ms mock latency) — the page surfaces
  // it: a "running slow" banner that only appears because the fetch is
  // genuinely still open.
  const pending = await cf.evaluate(() => !!document.getElementById('latency-probe'));
  if (pending) {
    await cf.evaluate(() => {
      const banner = document.createElement('div');
      banner.id = 'slow-banner';
      banner.style.cssText = 'position:sticky;top:0;z-index:50;background:#7c2d12;color:#fdba74;padding:8px 14px;font-size:13px;text-align:center;border-radius:8px;margin-bottom:14px';
      banner.textContent = 'Things are taking longer than usual — still loading your reports…';
      const main = document.querySelector('main');
      main.insertBefore(banner, main.firstChild);
    });
  }
  d.mark('banner');
  await d.sleep(1800);

  // Wait for the actual response, then resolve the spinner with the real time.
  const elapsed = await cf.evaluate(() => window.__latencyProbe);
  await cf.evaluate((secs) => {
    const el = document.getElementById('latency-probe');
    if (el) el.outerHTML = '<span style="font-size:13px;color:#4ade80">Revenue report loaded in ' + secs + 's (mock baseline: 0.4s)</span>';
  }, elapsed);
  await d.toast('Latency rule: /api/report answered in ' + elapsed + 's — the spinner and banner tracked the real wait', 'warning', 5200);
  await d.sleep(3200);

  // Rule 2: hard 500s. The page swallows them; the inbox doesn't.
  await d.agentToast('Chaos rule 2: every /api/report call now fails with a 500. Watch what the page tells you — nothing.', 'warning', 5600);
  await d.chaosClear();
  await cf.evaluate(() => document.getElementById('slow-banner')?.remove());
  await d.chaosAddRule({
    id: 'fail-report', type: 'http_error', url_pattern: '/api/report',
    error_codes: [500], error_message: 'chaos: injected failure', probability: 1.0,
  });
  d.mark('error-rule');
  await cf.evaluate(() => window.__demoLoadingCascade());
  await d.sleep(3600);

  // Label the damage in place: cards that never got data carry the reason.
  await cf.evaluate(() => {
    for (const id of ['rg-a', 'rg-b', 'rg-c']) {
      const el = document.getElementById(id);
      if (el && !el.textContent.trim()) {
        el.innerHTML = '<span style="font-size:12px;color:#f87171;font-weight:400">HTTP 500 — no data</span>';
      }
    }
    const status = document.getElementById('report-status');
    if (status) {
      status.innerHTML = '<span style="font-size:12.5px;color:#f87171">Failed to load reports — HTTP 500 on /api/report</span>';
    }
  });
  d.mark('labeled');

  // Each swallowed 500 surfaces as a toast with its fix — report + remediation,
  // the way the incident inbox hands them to the agent.
  await d.toast('500 ×3 on /api/report?kind=* — fix: the report handler is throwing; payload valid, handler not', 'error', 5600);
  await d.sleep(3400);
  await d.toast('500 ×3 on /api/report?step=* — same handler, sequential path; one fix covers both', 'error', 5200);
  await d.sleep(3200);
  await d.agentToast('The page stayed quiet — but every 500 landed in the incident inbox with the request attached, and each got a remediation hint.', 'info', 5600);
  await d.sleep(3800);

  // Clear and re-run: labels replaced by real values, fast again.
  await d.chaosClear();
  d.mark('cleared');
  await cf.evaluate(() => {
    const status = document.getElementById('report-status');
    if (status) status.innerHTML = '';
    return window.__demoLoadingCascade();
  });
  await d.sleep(2600);
  await d.toast('Rules cleared: cascade back to mock latency', 'success', 4800);
  d.mark('recovered');
  await d.sleep(3200);
}
