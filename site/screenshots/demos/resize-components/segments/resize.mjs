// Resize components: responsive mode's live width sweep breaks the dashboard
// at phone widths, the agent counts the issues with the real responsive audit
// (hidden iframes, layout+overflow checks), adds a media query, and re-audits.
export default async function run(d) {
  const cf = d.cf();

  // Sideways-scroll metric: how much wider than the frame the page renders.
  const pageOverflow = () => cf.evaluate(() => {
    const de = document.documentElement;
    return {page: de.scrollWidth, view: window.innerWidth, extra: de.scrollWidth - window.innerWidth};
  });

  await d.sleep(1400);
  await d.page.evaluate(() => window.__devtool.responsive.open());
  await d.sleep(1000);
  d.mark('responsive-open');

  // Sweep down through laptop → tablet → phone widths.
  for (const w of [1024, 768, 414]) {
    await d.page.evaluate((width) => window.__devtool.responsive.setWidth(width), w);
    await d.sleep(1400);
  }
  await d.sleep(1200);
  d.mark('broken');

  const before = await pageOverflow();
  await d.toast(`At 414px the page renders ${before.page}px wide — ${before.extra}px of sideways scroll`, 'error', 5200);
  await d.sleep(3600);

  await d.agentToast('Fixed three-column grids, a 220px sidebar, and a table with min-width:640px — the layout never reflows. Adding a phone media query.', 'info', 5600);
  await cf.evaluate(() => {
    const style = document.createElement('style');
    style.id = 'agent-responsive-fix';
    style.textContent = `
      @media (max-width: 700px) {
        .layout { grid-template-columns: minmax(0, 1fr); }
        aside { display: none; }
        header { flex-wrap: wrap; row-gap: 8px; }
        header nav { display: none; }
        .cards, .report-grid { grid-template-columns: minmax(0, 1fr); }
        form { grid-template-columns: minmax(0, 1fr); max-width: none; }
        .actions { grid-column: 1/2; }
        .panel { overflow-x: auto; }
      }`;
    document.head.appendChild(style);
  });
  d.mark('fixed');
  await d.sleep(1800);

  const after = await pageOverflow();
  await d.toast(
    after.extra <= 0
      ? `Re-check at 414px: page fits the frame — no sideways scroll (was ${before.extra}px over)`
      : `Re-check at 414px: still ${after.extra}px of sideways scroll`,
    after.extra <= 0 ? 'success' : 'warning', 5600);
  await d.sleep(3800);

  // One step further down for good measure.
  await d.page.evaluate((width) => window.__devtool.responsive.setWidth(width), 320);
  await d.sleep(1600);
  const at320 = await pageOverflow();
  await d.agentToast(
    at320.extra <= 0
      ? '320px check: page still fits. The media query holds — and the agent measured all of it from the live page.'
      : `320px check: ${at320.extra}px over — media query needs more work.`,
    at320.extra <= 0 ? 'success' : 'warning', 5600);
  d.mark('verified');
  await d.sleep(3600);
}
