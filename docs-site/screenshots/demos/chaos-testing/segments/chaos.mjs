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

  // Rule 1: latency floor on /api/report.
  await d.agentToast('Chaos rule 1: /api/report now answers in 2.5–3.5s. Same page, same code.', 'warning', 5200);
  await d.chaosAddRule({
    id: 'slow-report', type: 'latency', url_pattern: '/api/report',
    min_latency_ms: 2500, max_latency_ms: 3500, probability: 1.0,
  });
  d.mark('latency-rule');
  await cf.evaluate(() => window.__demoLoadingCascade());
  await d.sleep(3000);
  await d.toast('Cascade running under the latency rule — spinners crawl', 'warning', 4200);
  await d.sleep(4200);

  // Rule 2: hard 500s. The page swallows them; the inbox doesn't.
  await d.agentToast('Chaos rule 2: every /api/report call now fails with a 500. Watch what the page tells you — nothing.', 'warning', 5600);
  await d.chaosClear();
  await d.chaosAddRule({
    id: 'fail-report', type: 'http_error', url_pattern: '/api/report',
    error_codes: [500], error_message: 'chaos: injected failure', probability: 1.0,
  });
  d.mark('error-rule');
  await cf.evaluate(() => window.__demoLoadingCascade());
  await d.sleep(3600);
  await d.agentToast('The page stayed quiet — but every 500 landed in the incident inbox with the request attached.', 'error', 5600);
  await d.sleep(3800);

  // Clear and re-run: fast again.
  await d.chaosClear();
  d.mark('cleared');
  await cf.evaluate(() => window.__demoLoadingCascade());
  await d.sleep(2600);
  await d.toast('Rules cleared: cascade back to mock latency', 'success', 4800);
  d.mark('recovered');
  await d.sleep(3200);
}
