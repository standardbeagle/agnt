// interaction.js: what is recorded for a user event, and what is not.
//
// Evaluates the shipped frames/core/utils/interaction modules and dispatches
// real DOM events at them.

import { afterEach, describe, expect, it } from 'vitest';
import { loadModules } from './load-bundle.js';

const MODULES = ['frames', 'core', 'utils', 'interaction'];

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

function page(html) {
  rt = loadModules(MODULES, { html: `<!doctype html><html><body>${html}</body></html>` });
  expect(rt.errors).toEqual({});
  return rt.window;
}

function keydown(win, el, init) {
  el.dispatchEvent(new win.KeyboardEvent('keydown', { bubbles: true, cancelable: true, ...init }));
}

function lastOf(win, type) {
  const all = win.__devtool_interactions.getHistory(500).filter((i) => i.event_type === type);
  return all.at(-1);
}

describe('keystroke capture', () => {
  it('records nothing about a key typed into a password field', () => {
    const win = page('<input id="pw" type="password">');
    const pw = win.document.getElementById('pw');
    for (const ch of ['h', 'u', 'n', 't', 'e', 'r', '2']) {
      keydown(win, pw, { key: ch, code: `Key${ch.toUpperCase()}` });
    }
    // Non-printing keys and modifier combos are withheld too: in a password
    // field even the editing pattern is not ours to keep.
    keydown(win, pw, { key: 'Backspace', code: 'Backspace' });
    keydown(win, pw, { key: 'v', code: 'KeyV', ctrlKey: true });

    const keys = win.__devtool_interactions.getHistory(500).filter((i) => i.event_type === 'keydown');
    expect(keys).toHaveLength(9);
    for (const k of keys) {
      expect(k.key).toEqual({ key: '', code: '', masked: true });
    }
    expect(JSON.stringify(keys)).not.toMatch(/hunter|KeyH|Backspace|KeyV/);
  });

  it('masks characters typed into a text input and a textarea', () => {
    const win = page('<input id="t" type="text"><textarea id="ta"></textarea><input id="plain">');
    for (const id of ['t', 'ta', 'plain']) {
      const el = win.document.getElementById(id);
      keydown(win, el, { key: 's', code: 'KeyS' });
      expect(lastOf(win, 'keydown').key).toEqual({ key: '', code: '', masked: true });
      keydown(win, el, { key: 'S', code: 'KeyS', shiftKey: true });
      expect(lastOf(win, 'keydown').key).toEqual({ key: '', code: '', masked: true });
    }
  });

  it('masks characters typed into contenteditable, including a nested child', () => {
    const win = page('<div id="ce" contenteditable="true"><p id="inner">hello</p></div>');
    keydown(win, win.document.getElementById('ce'), { key: 'x', code: 'KeyX' });
    expect(lastOf(win, 'keydown').key.masked).toBe(true);
    keydown(win, win.document.getElementById('inner'), { key: 'y', code: 'KeyY' });
    expect(lastOf(win, 'keydown').key).toEqual({ key: '', code: '', masked: true });
  });

  it('keeps non-printing keys and modifier combos in an editable target', () => {
    const win = page('<input id="t" type="text">');
    const t = win.document.getElementById('t');

    keydown(win, t, { key: 'Enter', code: 'Enter' });
    expect(lastOf(win, 'keydown').key).toMatchObject({ key: 'Enter', code: 'Enter' });
    expect(lastOf(win, 'keydown').key.masked).toBeUndefined();

    keydown(win, t, { key: 'Escape', code: 'Escape' });
    expect(lastOf(win, 'keydown').key.key).toBe('Escape');

    keydown(win, t, { key: 's', code: 'KeyS', ctrlKey: true });
    expect(lastOf(win, 'keydown').key).toMatchObject({ key: 's', code: 'KeyS', ctrl: true });
  });

  it('records a plain-document shortcut as typed', () => {
    const win = page('<main id="app"><button id="b">Go</button></main>');
    keydown(win, win.document.body, { key: 'k', code: 'KeyK', metaKey: true });
    expect(lastOf(win, 'keydown').key).toMatchObject({ key: 'k', code: 'KeyK', meta: true });

    // An unmodified key outside any editable target is a shortcut too ("?",
    // "j"/"k" navigation) and is what a dead-shortcut diagnosis needs.
    keydown(win, win.document.getElementById('b'), { key: '?', code: 'Slash', shiftKey: true });
    expect(lastOf(win, 'keydown').key).toMatchObject({ key: '?', code: 'Slash', shift: true });
  });

  it('treats a button-type input as a non-editable target', () => {
    const win = page('<input id="c" type="checkbox">');
    keydown(win, win.document.getElementById('c'), { key: ' ', code: 'Space' });
    expect(lastOf(win, 'keydown').key).toMatchObject({ key: ' ', code: 'Space' });
  });
});

describe('click targets', () => {
  it('records a click that lands on an SVG icon, resolved to its button', () => {
    const win = page(
      '<button id="save" aria-label="Save">' +
      '<svg id="icon" viewBox="0 0 10 10"><path id="glyph" d="M0 0h10v10z"/></svg>' +
      '</button>');
    const glyph = win.document.getElementById('glyph');
    expect(glyph instanceof win.HTMLElement).toBe(false);

    glyph.dispatchEvent(new win.MouseEvent('click', { bubbles: true, clientX: 5, clientY: 6 }));

    const click = win.__devtool_interactions.getLastClick();
    expect(click, 'a click on an SVG child must be recorded').toBeTruthy();
    expect(click.target.tag).toBe('button');
    expect(click.target.id).toBe('save');
    expect(click.target.resolved_from).toBe('path');
    expect(click.position).toMatchObject({ client_x: 5, client_y: 6 });
  });

  it('records an ordinary HTML click without a resolved_from marker', () => {
    const win = page('<button id="b">Go</button>');
    win.document.getElementById('b').dispatchEvent(new win.MouseEvent('click', { bubbles: true }));
    const click = win.__devtool_interactions.getLastClick();
    expect(click.target.id).toBe('b');
    expect(click.target.resolved_from).toBeUndefined();
    expect(click.target.text).toBe('Go');
  });
});

describe('text capture', () => {
  it('never reads innerText', () => {
    const win = page('<div id="d">hello <b>world</b></div>');
    let reads = 0;
    Object.defineProperty(win.HTMLElement.prototype, 'innerText', {
      configurable: true,
      get() { reads++; return 'from innerText'; },
    });
    win.document.getElementById('d').dispatchEvent(new win.MouseEvent('click', { bubbles: true }));
    win.document.dispatchEvent(new win.Event('scroll'));
    win.document.body.dispatchEvent(new win.MouseEvent('mousemove', { bubbles: true, clientX: 1, clientY: 1 }));

    expect(reads).toBe(0);
    expect(win.__devtool_interactions.getLastClick().target.text).toBe('hello world');
  });

  it('bounds the captured text and skips script/style bodies', () => {
    const long = 'x'.repeat(5000);
    const win = page(`<div id="d"><script>var secret = 1;</script><style>.a{}</style><p>${long}</p></div>`);
    win.document.getElementById('d').dispatchEvent(new win.MouseEvent('click', { bubbles: true }));
    const text = win.__devtool_interactions.getLastClick().target.text;
    expect(text).toHaveLength(100);
    expect(text).not.toContain('secret');
  });

  it('records no text for scroll events', () => {
    const win = page('<p>some page text</p>');
    win.document.dispatchEvent(new win.Event('scroll'));
    const scroll = lastOf(win, 'scroll');
    expect(scroll).toBeTruthy();
    expect(scroll.target.text).toBeUndefined();
  });
});

describe('pending send queue', () => {
  it('is capped, dropping the oldest events', () => {
    const win = page('<button id="b">Go</button>');
    const b = win.document.getElementById('b');
    for (let i = 0; i < 450; i++) {
      b.dispatchEvent(new win.MouseEvent('click', { bubbles: true, clientX: i }));
    }
    const stats = win.__devtool_interactions.getQueueStats();
    expect(stats.max).toBe(300);
    expect(stats.pending).toBe(300);
    expect(stats.dropped).toBe(150);
  });
});
