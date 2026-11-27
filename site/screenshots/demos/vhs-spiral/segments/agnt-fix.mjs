// The resolution: same bug, one pass, with agnt. The scripted agent speaks
// over the real daemon transport (PROXY TOAST) and measures the live page —
// the same calls a real agent makes through the MCP tools.
import {halfOff, measure, fix} from './bugs.mjs';

export default async function run(d) {
  const cf = d.cf();
  await cf.locator('#customer-form').scrollIntoViewIfNeeded();
  await cf.evaluate(halfOff);
  d.mark('bug-live');

  await d.agentToast('I can see the live page. Measuring the submit button…', 'info', 3600);
  await d.sleep(2000);

  const before = await cf.evaluate(measure);
  await cf.evaluate(() => {
    const b = document.getElementById('f-submit');
    b.style.outline = '3px solid #ef4444';
    b.style.outlineOffset = '2px';
  });
  d.mark('measured');
  await d.toast(
    `#f-submit: right edge at ${before.right}px, viewport ${before.viewport}px — ${before.clippedPx}px clipped`,
    'warning', 4600);
  await d.sleep(3400);

  await d.agentToast('Root cause: button positioned past the viewport edge. Fixing.', 'info', 3600);
  await cf.evaluate(fix);
  d.mark('fix-applied');
  await d.sleep(1400);

  const after = await cf.evaluate(measure);
  await d.toast(
    after.clippedPx === 0
      ? `Fixed in one pass — button fully inside the viewport (${after.left}–${after.right}px of ${after.viewport}px)`
      : `Still clipped by ${after.clippedPx}px`,
    after.clippedPx === 0 ? 'success' : 'error', 4800);
  d.mark('verified');
  await d.sleep(3200);
}
