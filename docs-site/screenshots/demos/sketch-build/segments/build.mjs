// Sketch → build: the user wireframes a new stat-card row directly on the
// live dashboard (three rectangles + a text annotation), sends the sketch,
// and the agent builds it — real DOM, real styles, inserted into the page.
export default async function run(d) {
  const pg = d.page;
  const cf = d.cf();

  await d.sleep(1500);
  await pg.evaluate(() => window.__devtool.sketch.open());
  await d.sleep(900);
  d.mark('sketch-open');

  // Construct the layout: three rectangles in a row over the invoices area.
  await pg.evaluate(() => window.__devtool.sketch.setTool && window.__devtool.sketch.setTool('rectangle'));
  const rects = [[300, 340, 570, 470], [610, 340, 880, 470], [920, 340, 1190, 470]];
  for (const [x1, y1, x2, y2] of rects) {
    await pg.mouse.move(x1, y1);
    await pg.mouse.down();
    await pg.mouse.move(x2, y2, {steps: 10});
    await pg.mouse.up();
    await d.sleep(400);
  }

  // Annotate: text label under the row.
  await pg.evaluate(() => window.__devtool.sketch.setTool && window.__devtool.sketch.setTool('text'));
  await pg.mouse.move(300, 495);
  await pg.mouse.click(300, 495);
  await d.sleep(500);
  await pg.keyboard.type('3 stat cards here', {delay: 45});
  await pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot();
    const input = root.querySelector('#__devtool-sketch input[type="text"]') || root.querySelector('input[type="text"]');
    if (input) input.blur();
  });
  await d.sleep(700);
  d.mark('drawn');

  // Send it over the real channel.
  await pg.evaluate(() => window.__devtool.sketch.save());
  d.mark('sent');
  await d.sleep(1400);

  await d.agentToast('From your sketch: a row of three stat cards under the metrics. Building it now.', 'info', 5200);
  await d.sleep(3200);

  // The build: real cards in the dashboard's own design language, inserted
  // after the stats row.
  await cf.evaluate(() => {
    const stats = [
      {label: 'Refunds', value: '312', delta: '8.2% vs last week', down: true},
      {label: 'Chargebacks', value: '4', delta: '2 resolved', down: false},
      {label: 'Net revenue', value: '$47,910', delta: '11.8% vs last week', down: false},
    ];
    const wrap = document.createElement('div');
    wrap.className = 'cards';
    wrap.id = 'sketch-built-row';
    for (const s of stats) {
      const card = document.createElement('div');
      card.className = 'card';
      card.innerHTML = '<div style="color:var(--mut);font-size:13px">' + s.label + '</div>' +
        '<div style="font-size:28px;font-weight:700;margin:6px 0">' + s.value + '</div>' +
        '<div style="font-size:12.5px;color:' + (s.down ? '#f87171' : '#4ade80') + '">' + (s.down ? '▼ ' : '▲ ') + s.delta + '</div>';
      wrap.appendChild(card);
    }
    const anchor = document.querySelector('.cards');
    anchor.parentNode.insertBefore(wrap, anchor.nextSibling);
    wrap.scrollIntoView({behavior: 'smooth', block: 'center'});
  });
  d.mark('built');
  await d.sleep(2000);

  await d.toast('Built from the sketch: 3 new stat cards, live DOM, house style', 'success', 5200);
  await d.agentToast('Wireframe in, working UI out — positioned where you drew it, styled like its neighbors.', 'success', 5600);
  d.mark('done');
  await d.sleep(3600);
}
