// Design steering: the user steers with chat prompts, iterates a second
// round, then applies the chosen alternative to the live DOM. The constraints
// block rides with every chat: scheme axes preserved, UX axes vary, the
// user's words steer.
export default async function run(d) {
  const pg = d.page;
  const cf = d.cf();

  await d.sleep(1500);
  await cf.evaluate(() => {
    window.__devtool.design.start();
    window.__devtool.design.selectElement(document.querySelector('.cards .card'));
  });
  await d.sleep(2500);
  d.mark('selected');

  // Round 1: steer toward a horizontal strip.
  await cf.evaluate(() => window.__devtool.design.chat('make it a horizontal strip — label left, number right'));
  d.mark('chat-1');
  await d.sleep(1800);
  await d.agentToast('steer: "horizontal strip" — preserve: palette, type, spacing · vary: layout, hierarchy, density', 'info', 5600);
  await d.sleep(2600);

  await cf.evaluate(() => {
    var d = window.__devtool.design;
    d.addAlternative('<div style="display:flex;justify-content:space-between;align-items:center"><div><div class="label">Revenue</div><div class="delta up" style="margin-top:4px">▲ 12.4% vs last week</div></div><div class="num" style="font-size:30px">$48,250</div></div>',
      {label: 'variation', note: 'horizontal strip'});
    d.addAlternative('<div style="display:flex;align-items:center;gap:16px"><div class="num" style="font-size:30px">$48,250</div><div><div class="label">Revenue</div><div class="delta up" style="margin-top:4px">▲ 12.4%</div></div></div>',
      {label: 'variation', note: 'strip, number left'});
  });
  d.mark('round-1');
  await d.sleep(1600);
  await cf.evaluate(() => window.__devtool.design.previous());
  await d.sleep(1900);

  // Round 2: iterate — denser, quieter.
  await cf.evaluate(() => window.__devtool.design.chat('tighter — smaller number, drop the arrow'));
  d.mark('chat-2');
  await d.sleep(1800);
  await d.agentToast('steer: "tighter, no arrow" — iteration keeps the same contract; only the direction changes', 'info', 5600);
  await d.sleep(2600);

  await cf.evaluate(() => {
    window.__devtool.design.addAlternative(
      '<div style="display:flex;justify-content:space-between;align-items:baseline"><div class="label">Revenue</div><div style="display:flex;align-items:baseline;gap:10px"><div class="num" style="font-size:22px">$48,250</div><div class="delta up" style="font-size:12px">12.4%</div></div></div>',
      {label: 'variation', note: 'compact strip'}
    );
  });
  d.mark('round-2');
  await d.sleep(2400);

  // Build: the agent ships the winning alternative. Design mode is
  // deliberately preview-only (never writes into the target subtree), so the
  // ship step is the agent's normal path — a source edit + HMR in a real
  // project; on this static page, the direct DOM write is what that produces.
  await cf.evaluate(() => {
    const card = document.querySelector('.cards .card');
    card.innerHTML = '<div style="display:flex;justify-content:space-between;align-items:baseline"><div class="label">Revenue</div><div style="display:flex;align-items:baseline;gap:10px"><div class="num" style="font-size:22px">$48,250</div><div class="delta up" style="font-size:12px">12.4%</div></div></div>';
  });
  d.mark('applied');
  await d.sleep(1600);
  await d.toast('Shipped — the winning alternative lands in the page (in a real project: source edit + HMR)', 'success', 5200);
  await d.sleep(3400);
  d.mark('done');
}
