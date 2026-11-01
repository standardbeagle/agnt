// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// indicator-layout.js: the developer panel docks as a left drawer by default,
// pushes the shell's content frame aside while open, detaches into a floating
// window when its header is dragged, re-docks when dropped on a side edge, and
// persists all of it. jsdom's viewport is 1024x768.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const wait = (ms) => new Promise((r) => setTimeout(r, ms));
// pointerDrag applies moves once per animation frame; two frames settle it.
const frames = () => wait(40);

function pointer(win, target, type, x, y) {
  const Ctor = win.PointerEvent || win.MouseEvent;
  target.dispatchEvent(new Ctor(type, { bubbles: true, cancelable: true, button: 0, clientX: x, clientY: y }));
}

async function drag(win, el, from, to) {
  pointer(win, el, 'pointerdown', from[0], from[1]);
  pointer(win, win, 'pointermove', (from[0] + to[0]) / 2, (from[1] + to[1]) / 2);
  await frames();
  pointer(win, win, 'pointermove', to[0], to[1]);
  await frames();
  pointer(win, win, 'pointerup', to[0], to[1]);
}

const header = (root) => root.querySelector('.__devtool-panel-header');
const handle = (root) => root.querySelector('.__devtool-panel-resize');
const dockAttr = (win) => win.document.documentElement.getAttribute('data-agnt-dock');
const savedLayout = (win) => JSON.parse(win.localStorage.getItem('__devtool_panel_layout'));

function seedLayout(layout) {
  return { before: (win) => win.localStorage.setItem('__devtool_panel_layout', JSON.stringify(layout)) };
}

// Each case evaluates the whole indicator bundle in jsdom (~1s alone); under
// the full parallel tier that can pass vitest's 5s default, so the ceiling is
// a generous liveness bound, not a latency budget.
describe('panel layout', { timeout: 30_000 }, () => {
  it('defaults to a full-height left drawer that pushes the content frame while open', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind } = rt;
    const panel = ind.state.panel;
    expect(ind.panelLayout()).toEqual({ mode: 'left', dockWidth: 420, float: null });
    expect(panel.getAttribute('data-dock')).toBe('left');
    expect([panel.style.left, panel.style.top, panel.style.bottom, panel.style.width]).toEqual(['0px', '0px', '0px', '420px']);
    expect(dockAttr(win)).toBeNull();

    ind.togglePanel(true);
    expect(dockAttr(win)).toBe('left');
    expect(win.document.documentElement.style.getPropertyValue('--agnt-dock-width')).toBe('420px');
    expect(win.document.getElementById('__devtool-dock-inset-style')).toBeTruthy();
    // The drawer covers the bug's default bottom-left corner.
    expect(ind.state.bug.style.visibility).toBe('hidden');

    ind.togglePanel(false);
    expect(dockAttr(win)).toBeNull();
    expect(ind.state.bug.style.visibility).toBe('');
  });

  it('overlays instead of pushing outside the chrome shell', async () => {
    rt = await loadIndicator();
    const { win, ind } = rt;
    ind.togglePanel(true);
    expect(ind.panelLayout().mode).toBe('left');
    expect(dockAttr(win)).toBeNull();
    expect(win.document.getElementById('__devtool-dock-inset-style')).toBeNull();
  });

  it('a header drag detaches the drawer into a float window that follows the pointer', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    await drag(win, header(root), [100, 20], [400, 120]);
    const layout = ind.panelLayout();
    expect(layout.mode).toBe('float');
    expect(layout.float.w).toBe(420);
    // jsdom rects are all zero, so the grab offset is the pointer itself.
    expect([layout.float.x, layout.float.y]).toEqual([300, 100]);
    expect(ind.state.panel.getAttribute('data-dock')).toBe('float');
    expect(ind.state.panel.style.left).toBe('300px');
    expect(dockAttr(win)).toBeNull();
    expect(ind.state.bug.style.visibility).toBe('');
    expect(savedLayout(win).mode).toBe('float');
  });

  it('a drag shorter than the detach threshold leaves the drawer docked', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    await drag(win, header(root), [100, 20], [104, 23]);
    expect(ind.panelLayout().mode).toBe('left');
    expect(dockAttr(win)).toBe('left');
    expect(win.localStorage.getItem('__devtool_panel_layout')).toBeNull();
  });

  it('dropping a float window on the right edge docks it right; mid-screen does not', async () => {
    rt = await loadIndicator({ role: 'chrome', ...seedLayout({ mode: 'float', dockWidth: 400, float: { x: 200, y: 100, w: 400, h: 400 } }) });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    await drag(win, header(root), [300, 110], [990, 110]);
    expect(ind.panelLayout().mode).toBe('float');

    await drag(win, header(root), [300, 110], [1020, 110]);
    expect(ind.panelLayout().mode).toBe('right');
    expect(dockAttr(win)).toBe('right');
    expect(ind.state.panel.style.right).toBe('0px');
    expect(savedLayout(win).mode).toBe('right');
  });

  it('a drag starting on a header button does not move the panel', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    const closeBtn = header(root).querySelector('button[aria-label="Close panel"]');
    await drag(win, closeBtn, [400, 20], [600, 300]);
    expect(ind.panelLayout().mode).toBe('left');
  });

  it('the resize handle widens a docked drawer within its bounds and persists it', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    await drag(win, handle(root), [420, 300], [520, 300]);
    expect(ind.panelLayout().dockWidth).toBe(520);
    expect(win.document.documentElement.style.getPropertyValue('--agnt-dock-width')).toBe('520px');
    expect(savedLayout(win).dockWidth).toBe(520);

    // 1024 viewport keeps 320px of app: the drawer stops at 704.
    await drag(win, handle(root), [520, 300], [1000, 300]);
    expect(ind.panelLayout().dockWidth).toBe(704);
    // and never narrower than its 320px minimum.
    await drag(win, handle(root), [704, 300], [10, 300]);
    expect(ind.panelLayout().dockWidth).toBe(320);
  });

  it('the resize handle grows a float window in both directions, clamped to the viewport', async () => {
    rt = await loadIndicator({ role: 'chrome', ...seedLayout({ mode: 'float', dockWidth: 420, float: { x: 100, y: 100, w: 400, h: 300 } }) });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    await drag(win, handle(root), [500, 400], [600, 450]);
    expect(ind.panelLayout().float).toEqual({ x: 100, y: 100, w: 500, h: 350 });
    await drag(win, handle(root), [600, 450], [5000, 5000]);
    const f = ind.panelLayout().float;
    expect(f.w).toBe(1024 - 16);
    expect(f.h).toBe(768 - 16);
    expect([f.x, f.y]).toEqual([8, 8]);
  });

  it('the header buttons switch mode and mark the current one pressed', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind, root } = rt;
    ind.togglePanel(true);
    const btn = (label) => header(root).querySelector(`button[aria-label="${label}"]`);
    expect(btn('Dock left').getAttribute('aria-pressed')).toBe('true');

    btn('Float (detach)').click();
    expect(ind.panelLayout().mode).toBe('float');
    expect(btn('Float (detach)').getAttribute('aria-pressed')).toBe('true');
    expect(btn('Dock left').getAttribute('aria-pressed')).toBe('false');
    expect(dockAttr(win)).toBeNull();

    btn('Dock right').click();
    expect(dockAttr(win)).toBe('right');
    // A right drawer does not cover the bug's bottom-left corner.
    expect(ind.state.bug.style.visibility).toBe('');
  });

  it('a viewport too narrow to leave 320px of app overlays the drawer', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind } = rt;
    ind.togglePanel(true);
    expect(dockAttr(win)).toBe('left');
    Object.defineProperty(win, 'innerWidth', { configurable: true, value: 500 });
    win.dispatchEvent(new win.Event('resize'));
    await frames();
    expect(ind.panelLayout().dockWidth).toBe(320);
    expect(dockAttr(win)).toBeNull();
  });

  it('hide() and destroy() release the content-frame inset', async () => {
    rt = await loadIndicator({ role: 'chrome' });
    const { win, ind } = rt;
    ind.togglePanel(true);
    ind.hide();
    expect(dockAttr(win)).toBeNull();
    ind.show();
    expect(dockAttr(win)).toBe('left');
    ind.destroy();
    expect(dockAttr(win)).toBeNull();
  });

  it('ignores malformed saved layout fields', async () => {
    rt = await loadIndicator({ role: 'chrome', ...seedLayout({ mode: 'sideways', dockWidth: 'wide', float: { x: 1 } }) });
    expect(rt.ind.panelLayout()).toEqual({ mode: 'left', dockWidth: 420, float: null });
  });
});
