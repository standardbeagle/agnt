// Standardized audit report schema and session state
(function() {
  'use strict';

  // Session state
  window.__devtool_audit_state = {
    currentReport: null,
    selections: {},
    highlights: {},
    pendingFix: null
  };

  function generateFindingId(audit, type, index) {
    return audit + '-' + type + '-' + (index + 1);
  }

  function createFinding(audit, type, severity, message, options) {
    options = options || {};
    var index = options.index || 0;
    var finding = {
      id: options.id || generateFindingId(audit, type, index),
      type: type,
      severity: severity,
      impact: options.impact || 5,
      message: message,
      fix: options.fix || null,
      selector: options.selector || null,
      element: options.element || null,
      bounds: options.bounds || null,
      context: options.context || null
    };
    return finding;
  }

  function buildReport(audit, findings, options) {
    options = options || {};
    var score = options.score !== undefined ? options.score : 100;
    var grade = options.grade || getGrade(score);
    var summary = options.summary || generateSummary(findings);

    var report = {
      audit: audit,
      summary: summary,
      score: score,
      grade: grade,
      checkedAt: new Date().toISOString(),
      findings: findings || [],
      automationHints: options.automationHints || null
    };

    // Store for sidebar access
    window.__devtool_audit_state.currentReport = report;

    return report;
  }

  function getGrade(score) {
    if (score >= 90) return 'A';
    if (score >= 80) return 'B';
    if (score >= 70) return 'C';
    if (score >= 60) return 'D';
    return 'F';
  }

  function generateSummary(findings) {
    if (!findings) return 'No issues found';
    var errors = findings.filter(function(f) { return f.severity === 'error' || f.severity === 'critical'; }).length;
    var warnings = findings.filter(function(f) { return f.severity === 'warning'; }).length;
    var info = findings.filter(function(f) { return f.severity === 'info'; }).length;
    var parts = [];
    if (errors > 0) parts.push(errors + ' error' + (errors > 1 ? 's' : ''));
    if (warnings > 0) parts.push(warnings + ' warning' + (warnings > 1 ? 's' : ''));
    if (info > 0) parts.push(info + ' info');
    if (parts.length === 0) return 'No issues found';
    return parts.join(', ');
  }

  function setSelection(audit, findingId, selected) {
    var key = audit + '::' + window.location.href;
    if (!window.__devtool_audit_state.selections[key]) {
      window.__devtool_audit_state.selections[key] = [];
    }
    var selections = window.__devtool_audit_state.selections[key];
    var idx = selections.indexOf(findingId);
    if (selected && idx === -1) {
      selections.push(findingId);
    } else if (!selected && idx !== -1) {
      selections.splice(idx, 1);
    }
  }

  function getSelections(audit) {
    var key = audit + '::' + window.location.href;
    return window.__devtool_audit_state.selections[key] || [];
  }

  function isSelected(audit, findingId) {
    return getSelections(audit).indexOf(findingId) !== -1;
  }

  function clearSelections(audit) {
    var key = audit + '::' + window.location.href;
    window.__devtool_audit_state.selections[key] = [];
  }

  window.__devtool_audit_report = {
    createFinding: createFinding,
    buildReport: buildReport,
    getGrade: getGrade,
    generateSummary: generateSummary,
    setSelection: setSelection,
    getSelections: getSelections,
    isSelected: isSelected,
    clearSelections: clearSelections
  };
})();
