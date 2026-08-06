// The Impeccable confession: a gorgeous AI-slop landing page gets graded by
// auditDesign (59 deterministic anti-pattern rules), then fixed live and
// re-graded. One browser segment; the keep-ranges in demo.json trim the
// audit latency and scroll pauses.
export default async function run(d) {
  const cf = d.cf();

  // Show off the page first — it genuinely looks good at a glance.
  await d.sleep(1600);
  await cf.evaluate(() => document.querySelector('.cards').scrollIntoView({behavior: 'smooth', block: 'center'}));
  await d.sleep(1800);
  await cf.evaluate(() => document.querySelector('.quote').scrollIntoView({behavior: 'smooth', block: 'center'}));
  await d.sleep(1500);
  await cf.evaluate(() => window.scrollTo({top: 0, behavior: 'smooth'}));
  await d.sleep(1400);
  d.mark('shown');

  await d.agentToast('Looks great, right? Running the Impeccable design audit — 59 rules for AI-design tells…', 'info', 4600);
  await d.sleep(2200);

  // First auditDesign() call delay-loads the 366KB detector from the proxy.
  const first = await cf.evaluate(() => window.__devtool_audit_design.auditDesign());
  d.mark('first-audit');

  const top = Object.entries(first.findingsByType || {}).slice(0, 4)
    .map(([type, list]) => `${type} ×${list.length}`).join(' · ');
  await d.toast(`auditDesign: grade ${first.grade} (${first.score}/100) — ${first.summary}`, 'error', 5600);
  await d.sleep(3600);
  await d.toast(`Tells: ${top}`, 'warning', 5200);

  // Outline a few flagged elements so the verdict is visible, not just text.
  await cf.evaluate(() => {
    const seen = new Set();
    for (const el of document.querySelectorAll('h1, .chip, .card-inner, .sub')) {
      if (seen.size >= 5) break;
      const r = el.getBoundingClientRect();
      if (r.width === 0) continue;
      seen.add(el);
      el.style.outline = '3px solid #ef4444';
      el.style.outlineOffset = '3px';
    }
  });
  await d.sleep(3000);
  d.mark('verdict-shown');

  await d.agentToast('Every flag is a deterministic tell — gradient text, purple-blue palette, nested cards, gray-on-color. Fixing.', 'info', 4800);
  await cf.evaluate(() => {
    for (const el of document.querySelectorAll('[style*="outline"]')) {
      el.style.outline = '';
      el.style.outlineOffset = '';
    }
    // Swap the whole stylesheet: the detector reads every stylesheet in the
    // DOM — even disabled ones — so the slop sheet must be removed outright.
    document.getElementById('slop').remove();
    document.getElementById('clean').disabled = false;
    // Structural + copy tells a stylesheet can't fix: chip/kicker scaffolding,
    // nested card shells, icon tiles, buzzword copy, em-dash cadence.
    document.querySelector('.chip')?.remove();
    for (const k of document.querySelectorAll('.kicker')) k.remove();
    for (const t of document.querySelectorAll('.icon-tile')) t.remove();
    for (const inner of document.querySelectorAll('.card-inner')) {
      while (inner.firstChild) inner.parentNode.insertBefore(inner.firstChild, inner);
      inner.remove();
    }
    document.querySelector('h1').textContent = 'Project tracking for teams that ship';
    document.querySelector('.sub').textContent = 'Plan, track, and ship work in one place. Free for teams of five or fewer.';
    document.querySelector('.cta').textContent = 'Start a free trial';
    const titles = ['Fast to set up', 'Easy to read', 'Secure by default'];
    const bodies = [
      'Import your existing issues and start planning in minutes.',
      'Every screen uses plain language and visible system status.',
      'SOC2 controls and audit logs are included on every plan.',
    ];
    document.querySelectorAll('.card h3').forEach((h, i) => { h.textContent = titles[i]; });
    document.querySelectorAll('.card p').forEach((p, i) => { p.textContent = bodies[i]; });
    const q = document.querySelector('.quote');
    if (q) q.childNodes[0].textContent = '“Nimbusly cut our weekly planning meeting from an hour to fifteen minutes.” ';
    const f = document.querySelector('footer.page');
    if (f) f.textContent = '© 2026 Nimbusly Inc.';
  });
  d.mark('fixed');
  await d.sleep(1800);

  const second = await cf.evaluate(() => window.__devtool_audit_design.auditDesign());
  d.mark('second-audit');
  const passed = second.grade !== 'F' && second.grade !== 'D';
  await d.toast(
    `Re-audit: grade ${second.grade} (${second.score}/100) — was ${first.grade} (${first.score}/100)`,
    passed ? 'success' : 'warning', 5600);
  await d.agentToast(
    passed
      ? `${first.grade} (${first.score}) → ${second.grade} (${second.score}). The tells are gone — and the agent never had to look at a screenshot.`
      : `${first.grade} (${first.score}) → ${second.grade} (${second.score}): ${second.summary}`,
    passed ? 'success' : 'warning', 5200);
  d.mark('done');
  await d.sleep(3600);
}
