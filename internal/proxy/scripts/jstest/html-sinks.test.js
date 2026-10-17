// @vitest-environment node
// The test owns its JSDOM window (load-bundle.js), so the default jsdom
// environment would only build a second, unused DOM per file.
// Page-derived strings in the indicator attachment previews and the
// diagnostics panels are rendered as text, never as markup.
//
// Each case feeds a string that is harmless as text and destructive as markup
// (an element whose textContent is literally "<img ...>", a font-family name
// containing angle brackets) and asserts on the DOM that results.

import { afterEach, describe, expect, it } from 'vitest';
import { loadBundle, loadModules } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const PAYLOAD = '<img src=x onerror="window.__pwned=1"><b id="injected">bold</b>';
const RECT = { left: 100, top: 100, right: 140, bottom: 120, width: 40, height: 20 };

function preview(attachment) {
  rt = loadBundle();
  const win = rt.window;
  win.__devtool_indicator_internal.showAttachmentPreview(attachment, RECT);
  const popup = win.__devtoolGetMountRoot().querySelector('#__devtool-attachment-preview');
  expect(popup, 'preview popup must be mounted').toBeTruthy();
  return { win, popup };
}

function expectInert(popup, win) {
  expect(popup.querySelector('img')).toBeNull();
  expect(popup.querySelector('#injected')).toBeNull();
  expect(win.__pwned).toBeUndefined();
}

describe('indicator attachment previews', () => {
  it('element preview shows page text, selector and tag as text', () => {
    const { win, popup } = preview({
      type: 'element',
      label: 'code',
      data: { tag: PAYLOAD, selector: PAYLOAD, text: PAYLOAD },
    });
    expectInert(popup, win);
    expect(popup.textContent).toContain('<img src=x');
    // Our own markup still renders: the tag is the one <strong>.
    expect(popup.querySelectorAll('strong')).toHaveLength(1);
  });

  it('style-edit preview shows selector, property and values as text', () => {
    const { win, popup } = preview({
      type: 'style-edit',
      label: 'style',
      data: {
        selector: PAYLOAD,
        changes: [{ property: PAYLOAD, original: PAYLOAD, current: 'url("a") </div><b id="injected">x</b>' }],
      },
    });
    expectInert(popup, win);
    expect(popup.textContent).toContain('url("a")');
  });

  it('sketch and audit previews show the summary as text', () => {
    for (const attachment of [
      { type: 'sketch', label: 's', summary: PAYLOAD, data: { elements: [] } },
      { type: 'audit', label: 'a', summary: PAYLOAD, data: { auditType: PAYLOAD } },
    ]) {
      const { win, popup } = preview(attachment);
      expectInert(popup, win);
      expect(popup.textContent).toContain('<img src=x');
      rt.close();
      rt = null;
    }
  });
});

describe('diagnostics panels', () => {
  function diagnostics(html) {
    rt = loadModules(['frames', 'core', 'ui-tokens', 'shadow-root', 'utils', 'diagnostics'], {
      html: `<!doctype html><html><body>${html}</body></html>`,
    });
    expect(rt.errors).toEqual({});
    return rt.window;
  }

  function allNodes(win) {
    const roots = [win.document];
    try { roots.push(win.__devtoolGetMountRoot()); } catch (e) { /* no shadow root */ }
    return roots.flatMap((r) => [...r.querySelectorAll('*')]);
  }

  it('typography panel renders a font-family that contains markup as text', () => {
    // The panel shows the first family with its quotes stripped, so a quoted
    // family name is the one page-controlled string that reaches the markup
    // with angle brackets intact.
    const win = diagnostics(
      '<p style=\'font-family: "A<b id=injected>B", serif; font-size: 18px\'>Body copy here</p>' +
      '<p style="font-family: &quot;Helvetica Neue&quot;, Arial; font-size: 20px">More copy</p>');
    expect(win.getComputedStyle(win.document.querySelector('p')).fontFamily).toContain('<b id=injected>');

    win.__devtool_diagnostics.showTypographyPanel();

    const nodes = allNodes(win);
    expect(nodes.find((n) => n.id === 'injected')).toBeUndefined();

    // Control: an ordinary quoted family still styles its sample line.
    const samples = nodes.filter((n) => n.textContent === 'The quick brown fox jumps over the lazy dog');
    expect(samples.length).toBeGreaterThanOrEqual(2);
    expect(samples.some((n) => n.style.fontFamily.includes('Helvetica Neue'))).toBe(true);
  });

  it('DOM diff panel shows element paths and changes as text', () => {
    const win = diagnostics('<div id="root"><span id="a">one</span></div>');
    const d = win.__devtool_diagnostics;
    // Real snapshots of a real change: an element whose id is markup, and an
    // attribute whose NAME is reported in the change list.
    const baseline = d.captureDOMSnapshot();
    const em = win.document.createElement('em');
    em.setAttribute('id', PAYLOAD);
    win.document.getElementById('root').appendChild(em);
    win.document.getElementById('a').setAttribute('data-x', PAYLOAD);
    const current = d.captureDOMSnapshot();
    expect(d.compareDOMSnapshots(baseline, current).summary.totalChanges).toBeGreaterThan(0);
    d.showDOMDiff(baseline, current);

    const nodes = allNodes(win);
    expect(nodes.find((n) => n.id === 'injected')).toBeUndefined();
    expect(nodes.filter((n) => n.tagName === 'IMG')).toEqual([]);
    expect(win.__pwned).toBeUndefined();
  });
});
