// __devtool.screenshot and __devtool.snapshot render same-origin iframe content
// themselves: html2canvas paints an <iframe> as an empty box, so each capture
// pre-renders every iframe and swaps it for an <img> in html2canvas's cloned
// document (the onclone hook). The swap is the whole feature — building the
// <img> and not inserting it leaves the capture as blank as before.
//
// html2canvas itself is stubbed: what is under test is the onclone hook the
// shipped code hands it, run against a real cloned document.

import { afterEach, describe, expect, it } from 'vitest';
import { loadBundle } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

const FAKE_PNG = 'data:image/png;base64,iVBORw0KGgo=';

// boot loads the bundle into a page holding one same-origin iframe with
// content, and installs an html2canvas stand-in that records what the onclone
// hook left in the cloned document.
function boot() {
  const clones = [];
  rt = loadBundle({
    html: '<!doctype html><html><head></head><body><h1>page</h1><iframe id="preview"></iframe></body></html>',
    before: (win) => {
      for (const level of ['log', 'warn', 'error']) win.console[level] = () => {};
      win.html2canvas = (element, opts) => {
        if (opts && typeof opts.onclone === 'function') {
          const doc = win.document.implementation.createHTMLDocument('clone');
          doc.body.innerHTML = win.document.body.innerHTML;
          opts.onclone(doc, doc.body);
          clones.push({
            iframes: doc.body.querySelectorAll('iframe').length,
            imgs: Array.from(doc.body.querySelectorAll('img')).map((i) => i.getAttribute('src')),
          });
        }
        return Promise.resolve({ width: 10, height: 10, toDataURL: () => FAKE_PNG });
      };
    },
  });
  expect(rt.errors).toEqual({});
  const win = rt.window;
  // jsdom lays nothing out, so the iframe's body reports no height and the
  // capture would skip it as empty. Give it the height a rendered one has.
  const frameDoc = win.document.getElementById('preview').contentDocument;
  frameDoc.body.innerHTML = '<p>inside the frame</p>';
  Object.defineProperty(frameDoc.body, 'scrollHeight', { configurable: true, get: () => 100 });
  return { win, clones };
}

describe('iframe content in captures', () => {
  it('__devtool.screenshot replaces the iframe with its rendered image', async () => {
    const { win, clones } = boot();
    await win.__devtool.screenshot('with-frame');
    expect(clones).toHaveLength(1);
    expect(clones[0].iframes, 'the iframe must be swapped out of the clone').toBe(0);
    expect(clones[0].imgs).toEqual([FAKE_PNG]);
  });

  it('__devtool.snapshot.captureCurrentPage replaces the iframe the same way', async () => {
    const { win, clones } = boot();
    await win.__devtool.snapshot.captureCurrentPage();
    expect(clones).toHaveLength(1);
    expect(clones[0].iframes).toBe(0);
    expect(clones[0].imgs).toEqual([FAKE_PNG]);
  });
});
