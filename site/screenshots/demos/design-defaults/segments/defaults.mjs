// Design defaults: select a card, and the alternatives that come back stay
// inside the site's design — because the request carried the scheme tokens,
// the slot geometry, sibling exemplars, and a page thumbnail. Variation is
// layout and hierarchy; palette, type, and radius don't move.
export default async function run(d) {
  const pg = d.page;
  const cf = d.cf();

  await d.sleep(1500);

  // Select the Revenue card through the real design-mode API.
  await cf.evaluate(() => {
    window.__devtool.design.start();
    window.__devtool.design.selectElement(document.querySelector('.cards .card'));
  });
  await d.sleep(2500);
  d.mark('selected');

  await d.agentToast('Selected: Revenue card. The request carries the scheme tokens, the 3-track grid slot, two sibling cards, and a page thumbnail.', 'info', 5600);
  await d.sleep(4000);

  // The alternatives: same classes, same tokens, three different layouts.
  // (The public API is addAlternative, one call each.)
  await cf.evaluate(() => {
    var d = window.__devtool.design;
    d.addAlternative('<div style="display:flex;justify-content:space-between;align-items:baseline"><div class="label">Revenue</div><div class="delta up">▲ 12.4%</div></div><div class="num" style="margin-top:10px">$48,250</div>',
      {label: 'variation', note: 'split — label left, delta right'});
    d.addAlternative('<div style="text-align:center"><div class="num" style="font-size:34px">$48,250</div><div class="label" style="margin-top:4px">Revenue</div><div class="delta up" style="margin-top:6px">▲ 12.4% vs last week</div></div>',
      {label: 'variation', note: 'centered, number-first'});
    d.addAlternative('<div style="display:flex;align-items:center;gap:14px"><div class="num" style="font-size:24px">$48,250</div><div style="width:1px;align-self:stretch;background:var(--line)"></div><div><div class="label">Revenue</div><div class="delta up" style="margin-top:4px">▲ 12.4% vs last week</div></div></div>',
      {label: 'variation', note: 'dense row with divider'});
  });
  d.mark('alternatives');
  await d.sleep(1200);

  await d.agentToast('Three options, zero new colors. Layout, hierarchy, and density vary — palette, type, and radius stay on-scheme.', 'success', 5600);

  // Cycle through them in the preview dock.
  for (let i = 0; i < 3; i++) {
    await cf.evaluate(() => window.__devtool.design.previous());
    await d.sleep(1900);
  }
  d.mark('cycled');

  await d.toast('Defaults: on-scheme by construction — the agent never had to be told the house rules', 'info', 5200);
  await d.sleep(3400);
  d.mark('done');
}
