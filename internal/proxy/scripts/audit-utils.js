// Shared audit utilities
//
// Canonical home for helpers that were previously duplicated across the
// audit modules (accessibility, audit-dom, audit-css, audit-security,
// audit-performance, audit-quality, audit-api, audit-loading, responsive):
//   - computeFindingID: stable FNV-1a 32-bit finding ids (8 hex chars)
//   - registerFinding:  shared highlight registry on
//                       window.__devtool.audit.findingSelectors
//   - calculateGrade:   ONE canonical grade scale (plain A-F)
//   - getSelector / getSelectorPath: compact readable selectors for findings
//
// Modules that load before this one (accessibility) must call these lazily
// via window.__devtool_audit_utils at audit time, never at load time.

(function() {
  'use strict';

  function truncateString(str, maxLength) {
    if (!str || typeof str !== 'string') return str;
    if (str.length <= maxLength) return str;
    return str.substring(0, maxLength) + '...';
  }

  function truncateUrl(url, maxLength) {
    if (!url || typeof url !== 'string') return url;
    if (url.length <= maxLength) return url;
    // Keep protocol + domain + last part of path
    try {
      var u = new URL(url);
      var base = u.protocol + '//' + u.host;
      var remaining = maxLength - base.length - 4; // 4 for "..."
      if (remaining > 10) {
        return base + '/...' + u.pathname.slice(-remaining);
      }
      return base + '/...';
    } catch (e) {
      return truncateString(url, maxLength);
    }
  }

  /**
   * Generate a stable 8-char hex finding ID from type, selector, and message.
   * FNV-1a 32-bit hash — same inputs always produce same output across runs.
   */
  function auditComputeFindingID(type, selector, message) {
    var input = type + '\x00' + (selector || '') + '\x00' + (message || '');
    var h = 0x811c9dc5;
    for (var i = 0; i < input.length; i++) {
      h = h ^ input.charCodeAt(i);
      h = (h + (h << 1) + (h << 4) + (h << 7) + (h << 8) + (h << 24)) >>> 0;
    }
    return ('00000000' + h.toString(16)).slice(-8);
  }

  // Shared highlight registry: finding id -> CSS selector.
  function auditRegisterFinding(id, selector) {
    if (!window.__devtool) { window.__devtool = {}; }
    if (!window.__devtool.audit) { window.__devtool.audit = {}; }
    if (!window.__devtool.audit.findingSelectors) { window.__devtool.audit.findingSelectors = {}; }
    window.__devtool.audit.findingSelectors[id] = selector || '';
  }

  // Canonical grade scale for every audit: plain A-F, no +/- bands, no E.
  function auditCalculateGrade(score) {
    if (score >= 90) return 'A';
    if (score >= 80) return 'B';
    if (score >= 70) return 'C';
    if (score >= 60) return 'D';
    return 'F';
  }

  // Compact readable selector: #id, or tag with up to two classes.
  function auditGetSelector(el) {
    if (!el || !el.tagName) return '';
    if (el.id) return '#' + el.id;
    var sel = el.tagName.toLowerCase();
    if (el.className && typeof el.className === 'string') {
      var classes = el.className.trim().split(/\s+/).slice(0, 2).filter(Boolean);
      if (classes.length) sel += '.' + classes.join('.');
    }
    return sel;
  }

  // Selector path up to 5 ancestor levels, joined with ' > '.
  function auditGetSelectorPath(el) {
    var path = [];
    var current = el;
    var depth = 0;
    while (current && current.tagName && depth < 5) {
      path.unshift(auditGetSelector(current));
      current = current.parentElement;
      depth++;
    }
    return path.join(' > ');
  }

  // Shallow-merge caller-supplied audit thresholds over defaults.
  function mergeThresholds(defaults, overrides) {
    var out = {};
    var k;
    for (k in defaults) {
      if (defaults.hasOwnProperty(k)) out[k] = defaults[k];
    }
    if (overrides && typeof overrides === 'object') {
      for (k in overrides) {
        if (overrides.hasOwnProperty(k) && overrides[k] !== undefined && overrides[k] !== null) {
          out[k] = overrides[k];
        }
      }
    }
    return out;
  }

  window.__devtool_audit_utils = {
    truncateString: truncateString,
    truncateUrl: truncateUrl,
    computeFindingID: auditComputeFindingID,
    registerFinding: auditRegisterFinding,
    calculateGrade: auditCalculateGrade,
    getSelector: auditGetSelector,
    getSelectorPath: auditGetSelectorPath,
    mergeThresholds: mergeThresholds
  };
})();
