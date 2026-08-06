// Live sketch: the user draws on the running app — a rectangle around the
// Revenue card, an arrow at the invoices table — and sends the sketch to the
// agent over the real proxy channel (sketch.saveAndSend). The agent's reply
// references what was actually drawn.
export default async function run(d) {
  const pg = d.page;

  // Let the dashboard settle, then open sketch mode on the live page.
  await d.sleep(1500);
  await pg.evaluate(() => window.__devtool.sketch.open());
  await d.sleep(900);

  // Default ink is #1e1e1e — invisible on a dark dashboard. Set the stroke
  // color through the panel's own picker (its handler is onchange).
  await pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot();
    const picker = root.querySelector('input[type="color"]');
    if (picker) {
      picker.value = '#ef4444';
      picker.dispatchEvent(new Event('change', {bubbles: true}));
    }
  });
  await d.sleep(300);
  d.mark('sketch-open');

  // Rectangle around the Revenue card.
  await pg.evaluate(() => window.__devtool.sketch.setTool && window.__devtool.sketch.setTool('rectangle'));
  await pg.mouse.move(280, 170);
  await pg.mouse.down();
  await pg.mouse.move(620, 290, {steps: 14});
  await pg.mouse.up();
  await d.sleep(600);

  // Arrow from the invoices table toward the Revenue card.
  await pg.evaluate(() => window.__devtool.sketch.setTool && window.__devtool.sketch.setTool('arrow'));
  await pg.mouse.move(700, 460);
  await pg.mouse.down();
  await pg.mouse.move(500, 260, {steps: 14});
  await pg.mouse.up();
  await d.sleep(800);
  d.mark('drawn');

  // Send it: the drawing + element context travel the real proxy channel.
  await pg.evaluate(() => window.__devtool.sketch.save());
  d.mark('sent');
  await d.sleep(1400);

  await d.agentToast('Got the sketch — rectangle on the Revenue card, arrow up from Recent invoices. You want the numbers that matter on top.', 'info', 5600);
  await d.sleep(3800);
  await d.agentToast('No screenshot to attach, no "the card at the top left" — the drawing came with the elements under it.', 'success', 5600);
  d.mark('ack');
  await d.sleep(3600);
}
