// Loads the SHIPPED instrumentation scripts into the test's jsdom window.
//
// The files are evaluated verbatim — no module wrapper, no re-implementation —
// so these tests assert against the exact bytes the proxy injects into a page.

import { readFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const SCRIPTS_DIR = join(dirname(fileURLToPath(import.meta.url)), '..');

// Load order matches the injected bundle: utils, then the shared audit
// helpers, then the audit itself.
const SHIPPED = ['utils.js', 'audit-utils.js', 'audit-css.js'];

export function loadAuditScripts() {
  // Each audit run starts from a clean registry so one test's findings can
  // never satisfy another's assertion.
  delete window.__devtool;
  for (const file of SHIPPED) {
    const src = readFileSync(join(SCRIPTS_DIR, file), 'utf8');
    new Function(src).call(window);
  }
}

// Render a page and audit it. `css` becomes a same-document <style> (an
// accessible stylesheet, which is what the rule walk reads); `html` is
// optional markup for the inline-style pass.
export function auditPage(css, html = '') {
  document.head.innerHTML = css ? `<style>${css}</style>` : '';
  document.body.innerHTML = html;
  loadAuditScripts();
  return window.__devtool_audit_css.auditCSS();
}

// Modern-CSS opportunities of one type, in report order.
export function opportunities(result, type) {
  return result.modernCSS.opportunities.filter((o) => o.type === type);
}

export function types(result) {
  return result.modernCSS.opportunities.map((o) => o.type);
}
