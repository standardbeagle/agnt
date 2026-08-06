// Drag resize: the responsive workbench's edge drag handle, pulled from full
// width down to 320px by real mouse input. Overlay findings light up as the
// layout crosses its breakpoints, then the break goes to the agent.
export default async function run(d) {
  const pg = d.page;
  const cf = d.cf();

  await d.sleep(1400);
  await pg.evaluate(() => window.__devtool.responsive.open());
  await d.sleep(1200);

  // The drag handle is a root-mounted fixed layer on the frame's right edge.
  const handle = await pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot();
    const h = root.querySelector('[title="Drag to resize width"]');
    if (!h) return null;
    const r = h.getBoundingClientRect();
    return {x: r.x + r.width / 2, y: r.y + r.height / 2};
  });
  if (!handle) throw new Error('drag handle not found');
  d.mark('ready');

  // Pull the edge inward in one continuous drag — no presets, no numeric input.
  await pg.mouse.move(handle.x, handle.y);
  await pg.mouse.down();
  const targetX = handle.x - (1440 - 320) * 0.92; // drag most of the way to 320
  await pg.mouse.move(targetX, handle.y, {steps: 90});
  await pg.mouse.up();
  await d.sleep(1600);
  d.mark('dragged');

  const state = await pg.evaluate(() => window.__devtool.responsive.getState());
  const width = state && state.width ? state.width : 320;
  const over = await cf.evaluate(() => {
    const de = document.documentElement;
    return {page: de.scrollWidth, view: window.innerWidth, extra: de.scrollWidth - window.innerWidth};
  });
  await d.toast(
    over.extra > 0
      ? `Dragged to ${width}px: the page renders ${over.page}px wide — ${over.extra}px of sideways scroll`
      : `Dragged to ${width}px: the layout holds`,
    over.extra > 0 ? 'error' : 'success', 5200);
  await d.sleep(3600);

  // Hand the break to the agent the way a user would: Send to agent.
  await pg.evaluate(() => {
    const root = window.__devtoolGetMountRoot();
    const btns = root.querySelectorAll('button');
    for (const b of btns) {
      if (/send to agent/i.test(b.textContent)) { b.click(); return; }
    }
  });
  await d.sleep(1400);
  d.mark('sent');

  await d.agentToast(
    `Break received: at ${width}px the dashboard needs ${over.page}px. Fixed grids, fixed sidebar, min-width table — the usual suspects, all measured.`,
    'info', 5600);
  d.mark('ack');
  await d.sleep(3800);
}
