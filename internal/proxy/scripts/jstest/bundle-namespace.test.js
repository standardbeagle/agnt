// The assembled bundle, evaluated in real module order.
//
// api.js is the last module and replaces window.__devtool. Earlier modules
// register onto that object before it runs — the audit modules hang their entry
// points off window.__devtool.audit, frames.js adds role/frameId. A source scan
// cannot see that those registrations were wiped by the replacement; only
// evaluating the modules in order can.

import { afterEach, describe, expect, it } from 'vitest';
import { loadBundle, moduleOrder } from './load-bundle.js';

let rt = null;
afterEach(() => { if (rt) { rt.close(); rt = null; } });

describe('assembled bundle namespace', () => {
  it('ends with api.js, the module that replaces window.__devtool', () => {
    expect(moduleOrder().at(-1)).toBe('api');
  });

  it('evaluates every module without throwing', () => {
    rt = loadBundle();
    expect(Object.keys(rt.errors)).toEqual([]);
  });

  it('keeps the audit entry points registered before api.js', () => {
    rt = loadBundle();
    const audit = rt.window.__devtool.audit;
    expect(audit, '__devtool.audit must survive api.js').toBeTruthy();
    for (const name of ['auditAnimations', 'auditDesign', 'auditAPIEfficiency', 'auditLoading', 'highlight']) {
      expect(typeof audit[name], `__devtool.audit.${name}`).toBe('function');
    }
  });

  it('the surviving audit entry points are callable', async () => {
    rt = loadBundle();
    const audit = rt.window.__devtool.audit;

    const animations = audit.auditAnimations();
    expect(animations).toBeTruthy();
    expect(animations.error).toBeUndefined();

    const api = audit.auditAPIEfficiency();
    expect(api).toBeTruthy();

    // auditDesign lazy-loads its detector with a <script src>, which jsdom
    // does not fetch. Stand in for the loaded detector: what is under test is
    // that the entry point is still reachable and produces a report.
    rt.window.impeccableDetect = () => [];
    const design = await audit.auditDesign();
    expect(design.audit).toBe('design');

    // highlight of an unknown finding reports, it does not throw.
    expect(() => audit.highlight('no-such-finding')).not.toThrow();
  });

  it('keeps the frames.js fields', () => {
    rt = loadBundle();
    const dt = rt.window.__devtool;
    expect(typeof dt.role).toBe('function');
    expect(dt.role()).toBe('content');
    expect(typeof dt.frameId).toBe('function');
    expect(dt.FRAME_PARAM).toBe('__devtool_frame');
  });

  it('api.js definitions win over an earlier registration of the same key', () => {
    rt = loadBundle({ before: (win) => { win.__devtool = { log: 'stale' }; } });
    expect(typeof rt.window.__devtool.log).toBe('function');
  });
});
