// Design anti-pattern audit — wraps the vendored Impeccable browser detector
// (Apache-2.0, https://impeccable.style): 59 deterministic rules for the
// design tells AI-generated frontends share (overused fonts, purple-to-blue
// gradients, cards nested in cards, gray text on colored backgrounds,
// side-tab borders, gradient text, low contrast...).
//
// DELAY LOAD: the detector bundle is ~366KB and is NOT part of the injected
// instrumentation. It is served from /__devtool_impeccable and injected only
// on the first auditDesign() call — the same pattern html2canvas-loader uses
// for capture and accessibility.js uses for axe-core. Auto-scan is disabled
// via window.__IMPECCABLE_CONFIG__ before injection so loading the bundle
// never mutates the page or runs work the caller didn't ask for.
//
// ENGINE CHOICE: Impeccable also ships static-HTML, regex, and source-tree
// engines; this audit deliberately binds only the live-DOM browser engine
// (window.impeccableDetect) — an audit answers for the rendered page, not
// for source code.

(function() {
  'use strict';

  function computeFindingID(type, selector, message) {
    return window.__devtool_audit_utils.computeFindingID(type, selector, message);
  }
  function registerFinding(id, selector) {
    window.__devtool_audit_utils.registerFinding(id, selector);
  }
  function calculateGrade(score) {
    return window.__devtool_audit_utils.calculateGrade(score);
  }

  var inflight = null;

  function ensureDetector() {
    if (typeof window.impeccableDetect === 'function') {
      return Promise.resolve();
    }
    if (inflight) {
      return inflight;
    }
    inflight = new Promise(function(resolve, reject) {
      if (typeof window.impeccableDetect === 'function') {
        resolve();
        return;
      }
      // The bundle auto-scans on load unless told not to; an audit must be
      // pull-only, so disable before the script evaluates.
      window.__IMPECCABLE_CONFIG__ = window.__IMPECCABLE_CONFIG__ || {};
      window.__IMPECCABLE_CONFIG__.autoScan = false;

      var existing = document.querySelector('script[data-devtool-impeccable]');
      if (existing) {
        existing.addEventListener('load', function() { resolve(); });
        existing.addEventListener('error', function() { reject(new Error('impeccable detector failed to load')); });
        return;
      }
      var script = document.createElement('script');
      script.src = '/__devtool_impeccable';
      script.setAttribute('data-devtool-impeccable', '1');
      script.onload = function() {
        if (typeof window.impeccableDetect === 'function') {
          resolve();
        } else {
          reject(new Error('impeccable detector loaded but window.impeccableDetect is undefined'));
        }
      };
      script.onerror = function() {
        inflight = null; // allow a retry on the next audit call
        reject(new Error('impeccable detector failed to load from /__devtool_impeccable'));
      };
      (document.head || document.documentElement).appendChild(script);
    });
    return inflight;
  }

  // impeccableDetect() returns per-element results:
  //   {selector, tagName, rect, isPageLevel, isHidden,
  //    findings: [{type, category, severity, advisory, detail, name, description}]}
  // Flatten into the shared audit shape. Advisory findings are surfaced as
  // info and never scored — the detector's own contract is that advisories
  // are not failures.
  function buildReport(elements, raw) {
    var findings = [];
    var findingSelectors = {};
    var score = 100;
    var errorCount = 0, warningCount = 0, infoCount = 0;

    for (var i = 0; i < elements.length; i++) {
      var el = elements[i];
      if (el.isHidden) continue; // hidden elements do not cost the viewer anything
      var fs = el.findings || [];
      for (var j = 0; j < fs.length; j++) {
        var f = fs[j];
        var severity = f.advisory ? 'info' : (f.severity || 'warning');
        var message = (f.name || f.type) + (f.detail ? ' — ' + f.detail : '');
        var id = computeFindingID('design-' + f.type, el.selector, message);
        findingSelectors[id] = el.selector || '';
        registerFinding(id, el.selector || '');
        findings.push({
          id: id,
          type: f.type,
          category: f.category || 'quality',
          severity: severity,
          advisory: f.advisory === true,
          selector: el.selector,
          message: message,
          description: f.description || ''
        });
        if (severity === 'error' || severity === 'critical') { errorCount++; score -= 12; }
        else if (severity === 'warning') { warningCount++; score -= 6; }
        else { infoCount++; }
      }
    }
    if (score < 0) score = 0;
    var grade = calculateGrade(score);

    var actionable = errorCount + warningCount;
    var summaryParts = [];
    if (actionable === 0) {
      summaryParts.push('No design anti-patterns detected');
    } else {
      summaryParts.push(actionable + ' design anti-pattern' + (actionable === 1 ? '' : 's') +
        ' (' + errorCount + ' error, ' + warningCount + ' warning)');
    }
    if (infoCount > 0) summaryParts.push(infoCount + ' advisory');
    summaryParts.push('Impeccable browser engine, live DOM');
    var summary = summaryParts.join('. ');

    if (raw) {
      return {
        audit: 'design',
        score: score,
        grade: grade,
        summary: summary,
        checkedAt: new Date().toISOString(),
        findings: findings,
        findingSelectors: findingSelectors
      };
    }

    var byType = {};
    for (var gi = 0; gi < findings.length; gi++) {
      var gf = findings[gi];
      if (!byType[gf.type]) byType[gf.type] = [];
      if (byType[gf.type].length < 5) {
        byType[gf.type].push({
          id: gf.id,
          severity: gf.severity,
          selector: gf.selector,
          message: gf.message
        });
      }
    }
    return {
      audit: 'design',
      score: score,
      grade: grade,
      summary: summary,
      checkedAt: new Date().toISOString(),
      stats: {
        elementsFlagged: elements.length,
        errors: errorCount,
        warnings: warningCount,
        info: infoCount,
        totalIssues: findings.length
      },
      findingsByType: byType
    };
  }

  function auditDesign(options) {
    options = options || {};
    var raw = options.raw === true;
    return ensureDetector().then(function() {
      var elements = window.impeccableDetect() || [];
      return buildReport(elements, raw);
    });
  }

  window.__devtool_audit_design = {
    auditDesign: auditDesign
  };

  if (!window.__devtool) { window.__devtool = {}; }
  if (!window.__devtool.audit) { window.__devtool.audit = {}; }
  window.__devtool.audit.auditDesign = auditDesign;
})();
