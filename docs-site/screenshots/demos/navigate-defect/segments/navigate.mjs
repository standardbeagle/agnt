// Navigate-to-investigate: the dev reports a defect three pages deep; the
// agent drives the browser there (real PROXY EXEC navigation through the
// proxy) and measures the defect on arrival.
export default async function run(d) {
  const pg = d.page;

  await d.sleep(1600);

  // The dev asks through the floating panel (real indicator compose flow).
  await pg.evaluate(() => window.__devtool.indicator.show());
  await d.sleep(800);
  await pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot();
    if (!root.getElementById('__devtool-message')) window.__devtool.indicator.togglePanel(true);
  });
  await d.sleep(900);
  const msg = pg.locator('#__devtool-message');
  await msg.click();
  await msg.type("The total on invoice INV-1042 looks wrong. Take me there.", {delay: 26});
  await d.sleep(700);
  d.mark('typed');

  await d.agentToast('On it — that panel is two levels down. Navigating to Invoices first.', 'info', 4600);
  await d.sleep(2200);

  // Navigate via real PROXY EXEC, retrying: right after a page load the
  // injected bundle may still be reconnecting, and an exec issued in that
  // window drops silently.
  const gotoPage = async (path, selector) => {
    // Deferred-assign form from the daemon's buildNavigateJS: reply returns
    // synchronously, navigation runs after — a sync assign kills the context
    // before the daemon can collect the result and looks like a hang.
    const navJS = `(function(){var h=location.href;setTimeout(function(){location.assign("${path}");},0);return {navigating:true,from:h};})()`;
    for (let attempt = 0; attempt < 3; attempt++) {
      // The bundle must be live in the content frame, or the exec routes to a
      // stale context right after a previous navigation.
      const cf0 = d.cf();
      await cf0.waitForFunction(() => window.__devtool && window.__devtool.indicator, {timeout: 15000});
      const res = await d.agentExec(navJS);
      const arrived = await pg.waitForFunction((p) => {
        const f = document.querySelector('iframe#__devtool_content_frame');
        try { return f && f.contentWindow && f.contentWindow.location.pathname === p; } catch (e) { return false; }
      }, path, {timeout: 8000}).then(() => true).catch(() => false);
      if (arrived) break;
    }
    const cf = d.cf();
    await cf.waitForFunction((sel) => !!document.querySelector(sel), selector, {timeout: 15000});
    await d.sleep(600); // settle for the camera
  };

  // Level 1 → 2: the invoices list. Real navigation through the proxy, the
  // same path as MCP proxy {action:"navigate", direction:"goto"}.
  await gotoPage('/invoices', 'table');
  d.mark('list');

  await d.agentToast('Invoices. INV-1042 is the overdue one — opening it.', 'info', 4200);
  await d.sleep(1800);

  // Level 2 → 3: the invoice detail panel.
  await gotoPage('/invoices/INV-1042', '#line-items');
  d.mark('detail');

  // Investigate on arrival: sum the line items against the displayed total.
  const cf = d.cf();
  const check = await cf.evaluate(() => {
    const cents = (s) => Math.round(parseFloat(s.replace(/[$,]/g, '')) * 100);
    const items = [...document.querySelectorAll('#line-items td.li')].map((td) => cents(td.textContent));
    const sum = items.reduce((a, b) => a + b, 0);
    const total = cents(document.getElementById('total').textContent);
    return {sum: sum / 100, total: total / 100, diff: (total - sum) / 100};
  });
  await cf.evaluate(() => {
    document.getElementById('total').style.outline = '3px solid #ef4444';
    document.getElementById('total').style.outlineOffset = '2px';
  });
  d.mark('measured');

  await d.toast(`Line items: $1,200 + $450 = $${check.sum.toLocaleString()} — the total shows $${check.total.toLocaleString()}`, 'error', 5600);
  await d.sleep(3600);
  await d.agentToast(`Found it: the total is $${check.diff.toLocaleString()} higher than the line items sum to. Defect confirmed, no repro steps needed — we're already on the panel.`, 'success', 6000);
  d.mark('done');
  await d.sleep(4000);
}
