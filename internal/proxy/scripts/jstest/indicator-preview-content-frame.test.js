// indicator.js element attachment preview: under the always-wrap model the
// indicator runs in the chrome shell while the page lives in the content
// frame, so the highlighted element must be looked up in targetWindow()'s
// document, not the shell's.

import { afterEach, describe, expect, it } from 'vitest';
import { loadIndicator } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const RECT = { left: 10, top: 10, right: 50, bottom: 30, width: 40, height: 20 };

function preview(internal, selector) {
  internal.showAttachmentPreview(
    { type: 'element', label: 'div', data: { tag: 'div', selector, text: 'x' } }, RECT);
}

describe('attachment preview from the chrome shell', () => {
  it('highlights the element found in the content frame document', async () => {
    rt = await loadIndicator();
    const { win, internal, root } = rt;
    const iframe = win.document.createElement('iframe');
    win.document.body.appendChild(iframe);
    const contentWin = iframe.contentWindow;
    contentWin.document.body.innerHTML = '<div id="only-in-content">x</div>';
    const ctx = win.__devtool_context;
    ctx.isChrome = () => true;
    ctx.activeContent = () => ({ win: contentWin, frameId: 'f1' });

    preview(internal, '#only-in-content');
    expect(root.querySelector('#__devtool-preview-highlight')).toBeTruthy();
  });

  it('draws no highlight for a selector the content frame does not match', async () => {
    rt = await loadIndicator();
    const { win, internal, root } = rt;
    const iframe = win.document.createElement('iframe');
    win.document.body.appendChild(iframe);
    const contentWin = iframe.contentWindow;
    win.document.body.insertAdjacentHTML('beforeend', '<div id="only-in-shell">x</div>');
    const ctx = win.__devtool_context;
    ctx.isChrome = () => true;
    ctx.activeContent = () => ({ win: contentWin, frameId: 'f1' });

    preview(internal, '#only-in-shell');
    expect(root.querySelector('#__devtool-preview-highlight')).toBeNull();
  });
});
