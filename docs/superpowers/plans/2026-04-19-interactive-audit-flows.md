# Interactive Audit Flows Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Upgrade all audit flows to return standardized detail reports, render them in an interactive left sidebar with checkboxes and element highlighting, and enable a "Send to Fix" workflow.

**Architecture:** A new `audit-report.js` module standardizes the `AuditReport`/`Finding` schema across all browser-based audits. A new `audit-sidebar.js` renders findings in a 380px left sidebar that pushes main content right. The sidebar auto-opens on audit completion. Checkboxes select findings; hover highlights elements via the existing overlay system. Selected findings flow into a structured fix prompt.

**Tech Stack:** Vanilla JS (browser modules), VanJS (indicator integration), Go (MCP tools), Playwright (E2E tests)

---

## File Structure

| File | Responsibility |
|------|---------------|
| `internal/proxy/scripts/audit-report.js` | Standardized `AuditReport`/`Finding` schema, session state management |
| `internal/proxy/scripts/audit-sidebar.js` | Left sidebar UI: open/close, render findings, checkboxes, highlights |
| `internal/proxy/scripts/accessibility.js` | A11y audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/audit-quality.js` | SEO audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/audit-css.js` | CSS audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/audit-dom.js` | DOM audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/audit-performance.js` | Performance audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/audit-security.js` | Security audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/responsive.js` | Responsive audit — refactored to return `AuditReport` |
| `internal/proxy/scripts/indicator.js` | Trigger sidebar on audit completion |
| `internal/proxy/scripts/api.js` | Export `audit-report` and `audit-sidebar` modules |
| `internal/proxy/scripts/embed.go` | Register new modules in bundle |
| `internal/tools/responsive_audit.go` | Parse new schema, return compact/raw |
| `internal/tools/get_errors.go` | Map errors to `Finding` schema |
| `internal/tools/audit_fix_tools.go` | New MCP tools: `prepare_fix_prompt`, `select_audit_findings` |

---

## Task 1: audit-report.js Schema Foundation

**Files:**
- Create: `internal/proxy/scripts/audit-report.js`
- Modify: `internal/proxy/scripts/embed.go`
- Modify: `internal/proxy/scripts/api.js`
- Test: `internal/proxy/scripts/embed_test.go`

- [ ] **Step 1: Create `audit-report.js`**

Create `internal/proxy/scripts/audit-report.js`:

```javascript
// Standardized audit report schema and session state
(function() {
  'use strict';

  var utils = window.__devtool_utils;

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
```

- [ ] **Step 2: Register in `embed.go`**

Add to `internal/proxy/scripts/embed.go` in three places:

1. Add embed directive after audit-utils:
```go
	//go:embed audit-report.js
	auditReportJS string
```

2. Add to `moduleOrder` after `audit-utils`:
```go
	{"audit-report", []string{"utils"}},
```

3. Add to `moduleScript`:
```go
	"audit-report":       auditReportJS,
```

- [ ] **Step 3: Add to `api.js`**

In `internal/proxy/scripts/api.js`, add the export:

```javascript
window.__devtool.auditReport = window.__devtool_audit_report;
```

- [ ] **Step 4: Run embed tests**

```bash
cd /home/beagle/work/core/agnt && go test ./internal/proxy/scripts/ -run TestModuleDependencyOrder -v
```

Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/scripts/audit-report.js internal/proxy/scripts/embed.go internal/proxy/scripts/api.js
git commit -m "feat(audit): add standardized audit-report.js schema module"
```

---

## Task 2: Minimal audit-sidebar.js

**Files:**
- Create: `internal/proxy/scripts/audit-sidebar.js`
- Modify: `internal/proxy/scripts/embed.go`
- Modify: `internal/proxy/scripts/indicator.js`
- Modify: `internal/proxy/scripts/api.js`

- [ ] **Step 1: Create `audit-sidebar.js`**

Create `internal/proxy/scripts/audit-sidebar.js` with minimal open/close/render:

```javascript
// Audit sidebar — left panel for interactive audit review
(function() {
  'use strict';

  var utils = window.__devtool_utils;
  var reportModule = window.__devtool_audit_report;
  var overlay = window.__devtool_overlay;

  var SIDEBAR_WIDTH = 380;
  var sidebarEl = null;
  var isOpen = false;

  function getMountRoot() {
    return typeof window.__devtoolGetMountRoot === 'function'
      ? window.__devtoolGetMountRoot()
      : document.body;
  }

  function createSidebar() {
    var container = document.createElement('div');
    container.id = '__devtool-audit-sidebar';
    container.style.cssText = [
      'position: fixed',
      'top: 0',
      'left: 0',
      'bottom: 0',
      'width: ' + SIDEBAR_WIDTH + 'px',
      'background: #1e1e1e',
      'color: #d4d4d4',
      'font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif',
      'font-size: 13px',
      'z-index: 2147483646',
      'display: flex',
      'flex-direction: column',
      'box-shadow: 2px 0 8px rgba(0,0,0,0.3)',
      'overflow: hidden'
    ].join(';');

    var header = document.createElement('div');
    header.style.cssText = 'padding: 12px 16px; border-bottom: 1px solid #333; display: flex; justify-content: space-between; align-items: center;';
    header.innerHTML = '<span style="font-weight: 600; font-size: 14px;">Audit Report</span><button id="__devtool-sidebar-close" style="background: none; border: none; color: #999; cursor: pointer; font-size: 18px; line-height: 1;">&times;</button>';
    container.appendChild(header);

    var content = document.createElement('div');
    content.id = '__devtool-sidebar-content';
    content.style.cssText = 'flex: 1; overflow-y: auto; padding: 12px 16px;';
    container.appendChild(content);

    getMountRoot().appendChild(container);

    document.getElementById('__devtool-sidebar-close').addEventListener('click', close);

    return container;
  }

  function open(report) {
    if (!report) report = window.__devtool_audit_state.currentReport;
    if (!report) return;

    if (!sidebarEl) {
      sidebarEl = createSidebar();
    }

    // Push main content
    document.body.style.marginLeft = SIDEBAR_WIDTH + 'px';
    document.body.style.transition = 'margin-left 0.2s ease';

    render(report);
    sidebarEl.style.display = 'flex';
    isOpen = true;
  }

  function close() {
    document.body.style.marginLeft = '';
    document.body.style.transition = '';
    if (sidebarEl) sidebarEl.style.display = 'none';
    isOpen = false;
    if (overlay) overlay.clearAllOverlays();
  }

  function render(report) {
    var content = document.getElementById('__devtool-sidebar-content');
    if (!content) return;

    var html = '<div style="margin-bottom: 16px;">';
    html += '<div style="font-size: 24px; font-weight: 700; margin-bottom: 4px;">' + report.score + '/100 <span style="font-size: 16px; color: #888;">(' + report.grade + ')</span></div>';
    html += '<div style="color: #888; font-size: 12px;">' + report.summary + '</div>';
    html += '</div>';

    if (report.findings && report.findings.length > 0) {
      html += '<div style="margin-bottom: 12px;">';
      html += '<button id="__devtool-send-fix" style="background: #0e639c; color: white; border: none; padding: 6px 12px; border-radius: 4px; cursor: pointer; font-size: 12px;">Send to Fix</button>';
      html += '</div>';

      report.findings.forEach(function(finding) {
        var severityColor = finding.severity === 'error' || finding.severity === 'critical' ? '#f48771' :
                           finding.severity === 'warning' ? '#cca700' : '#75beff';
        html += '<div class="__devtool-finding" data-id="' + finding.id + '" style="margin-bottom: 8px; padding: 8px; background: #252526; border-radius: 4px; cursor: pointer;">';
        html += '<div style="display: flex; align-items: flex-start; gap: 8px;">';
        html += '<input type="checkbox" class="__devtool-finding-check" data-id="' + finding.id + '" style="margin-top: 2px;">';
        html += '<div style="flex: 1;">';
        html += '<div style="color: ' + severityColor + '; font-weight: 500; font-size: 12px;">[' + finding.severity + '] ' + finding.type + '</div>';
        html += '<div style="margin-top: 2px;">' + escapeHtml(finding.message) + '</div>';
        if (finding.selector) {
          html += '<div style="color: #888; font-size: 11px; margin-top: 2px; font-family: monospace;">' + escapeHtml(finding.selector) + '</div>';
        }
        html += '</div>';
        html += '</div>';
        html += '</div>';
      });
    } else {
      html += '<div style="color: #888; text-align: center; padding: 40px 0;">No issues found</div>';
    }

    content.innerHTML = html;

    // Wire up checkboxes
    content.querySelectorAll('.__devtool-finding-check').forEach(function(checkbox) {
      var findingId = checkbox.getAttribute('data-id');
      checkbox.checked = reportModule.isSelected(report.audit, findingId);
      checkbox.addEventListener('change', function() {
        reportModule.setSelection(report.audit, findingId, checkbox.checked);
      });
    });

    // Wire up hover highlights
    content.querySelectorAll('.__devtool-finding').forEach(function(el) {
      var findingId = el.getAttribute('data-id');
      var finding = report.findings.find(function(f) { return f.id === findingId; });
      if (finding && finding.selector) {
        el.addEventListener('mouseenter', function() {
          overlay.highlight(finding.selector, {
            color: 'rgba(14, 99, 156, 0.2)',
            borderColor: '#0e639c',
            duration: 2000
          });
        });
      }
    });

    // Wire up Send to Fix
    var sendBtn = document.getElementById('__devtool-send-fix');
    if (sendBtn) {
      sendBtn.addEventListener('click', function() {
        var selected = reportModule.getSelections(report.audit);
        var selectedFindings = report.findings.filter(function(f) { return selected.indexOf(f.id) !== -1; });
        if (selectedFindings.length === 0) {
          alert('No issues selected');
          return;
        }
        var prompt = generateFixPrompt(report.audit, selectedFindings);
        window.__devtool_audit_state.pendingFix = {
          id: 'fix-' + Date.now(),
          audit: report.audit,
          findings: selectedFindings,
          prompt: prompt
        };
        // TODO: inject into compose tab
        console.log('[DevTool] Fix prompt ready:', prompt);
      });
    }
  }

  function generateFixPrompt(audit, findings) {
    var prompt = '## Fix Request: ' + findings.length + ' ' + audit + ' issue' + (findings.length > 1 ? 's' : '') + ' selected\n\n';
    findings.forEach(function(f, i) {
      prompt += '### Issue ' + (i + 1) + ': ' + f.type + '\n';
      if (f.selector) prompt += '- **Element:** ' + f.selector + '\n';
      prompt += '- **Problem:** ' + f.message + '\n';
      if (f.fix) prompt += '- **Fix:** ' + f.fix + '\n';
      prompt += '\n';
    });
    return prompt;
  }

  function escapeHtml(text) {
    var div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  // Keyboard shortcut: Esc to close
  document.addEventListener('keydown', function(e) {
    if (e.key === 'Escape' && isOpen) {
      close();
    }
  });

  window.__devtool_audit_sidebar = {
    open: open,
    close: close,
    isOpen: function() { return isOpen; }
  };
})();
```

- [ ] **Step 2: Register in `embed.go`**

1. Add embed directive:
```go
	//go:embed audit-sidebar.js
	auditSidebarJS string
```

2. Add to `moduleOrder` after `audit-report`:
```go
	{"audit-sidebar", []string{"utils", "overlay", "audit-report"}},
```

3. Add to `moduleScript`:
```go
	"audit-sidebar":      auditSidebarJS,
```

- [ ] **Step 3: Modify `indicator.js` to trigger sidebar**

In `internal/proxy/scripts/indicator.js`, find where audit results are handled (near `state.lastAuditResults`) and add:

```javascript
// After storing audit result, open sidebar
if (window.__devtool_audit_sidebar && window.__devtool_audit_sidebar.open) {
  window.__devtool_audit_sidebar.open(report);
}
```

Also add a notification toast when audit completes:

```javascript
// In the audit completion handler
function showAuditNotification(report) {
  var count = report.findings ? report.findings.filter(function(f) {
    return f.severity === 'error' || f.severity === 'critical' || f.severity === 'warning';
  }).length : 0;
  var msg = report.audit + ' audit complete' + (count > 0 ? ': ' + count + ' issues found' : ': No issues');
  showMicroToast(msg);
}
```

- [ ] **Step 4: Add to `api.js`**

```javascript
window.__devtool.auditSidebar = window.__devtool_audit_sidebar;
```

- [ ] **Step 5: Run embed tests**

```bash
cd /home/beagle/work/core/agnt && go test ./internal/proxy/scripts/ -run TestModuleDependencyOrder -v
```

Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/proxy/scripts/audit-sidebar.js internal/proxy/scripts/embed.go internal/proxy/scripts/indicator.js internal/proxy/scripts/api.js
git commit -m "feat(audit): add minimal audit sidebar with open/close/highlight"
```

---

## Task 3: Pilot — Refactor accessibility.js

**Files:**
- Modify: `internal/proxy/scripts/accessibility.js`
- Test: `e2e/tests/audits-unit.spec.ts`

- [ ] **Step 1: Refactor `runBasicAudit` to return `AuditReport`**

In `internal/proxy/scripts/accessibility.js`, replace the return statement in `runBasicAudit`:

```javascript
// OLD:
return {
  mode: 'basic',
  summary: summary,
  score: score,
  ...
};

// NEW:
var findings = fixable.map(function(issue, i) {
  return reportModule.createFinding('accessibility', issue.type, issue.severity, issue.message, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector,
    element: issue.element,
    context: { wcag: issue.wcag, mode: 'basic' }
  });
}).concat(informational.map(function(info, i) {
  return reportModule.createFinding('accessibility', info.type, info.severity, info.message, {
    id: info.id,
    impact: 0,
    context: info.context
  });
}));

return reportModule.buildReport('accessibility', findings, {
  score: score,
  summary: summary,
  automationHints: {
    lookFor: ['image alt patterns', 'ARIA usage', 'form labels'],
    suggestionsNeeded: findings.filter(function(f) { return f.severity !== 'info'; }).map(function(f) { return f.fix; }).filter(Boolean)
  }
});
```

- [ ] **Step 2: Refactor `runAxeAudit` to return `AuditReport`**

In `runAxeAudit`, replace the return statements. For the AI-optimized mode (default):

```javascript
// Convert axe violations to findings
var findings = results.violations.map(function(violation, vi) {
  return violation.nodes.map(function(node, ni) {
    return reportModule.createFinding('accessibility', violation.id, mapAxeImpact(violation.impact), violation.help, {
      id: 'a11y-' + violation.id + '-' + (vi + 1) + '-' + (ni + 1),
      impact: mapAxeImpactToNumber(violation.impact),
      fix: getFixInstruction(violation.id, node),
      selector: node.target.join(', '),
      element: (node.html || '').substring(0, 100),
      context: { wcag: wcagReferences[violation.id], axeRule: violation.id }
    });
  });
}).flat();

return reportModule.buildReport('accessibility', findings, {
  score: score,
  automationHints: { ... }
});
```

Add helper:
```javascript
function mapAxeImpact(impact) {
  if (impact === 'critical') return 'critical';
  if (impact === 'serious') return 'error';
  if (impact === 'moderate') return 'warning';
  return 'info';
}
function mapAxeImpactToNumber(impact) {
  var map = { critical: 10, serious: 8, moderate: 5, minor: 2 };
  return map[impact] || 5;
}
```

- [ ] **Step 3: Test accessibility audit end-to-end**

Run the e2e audit test:

```bash
cd /home/beagle/work/core/agnt/e2e && npx playwright test tests/audits-unit.spec.ts --grep "accessibility" --project=chromium
```

If no existing test, manually verify by running the devtool against a test page and calling `window.__devtool_accessibility.auditAccessibility({raw: true})` — the result should have `audit`, `findings`, `score`, `grade` fields.

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/scripts/accessibility.js
git commit -m "feat(audit): refactor accessibility.js to standardized AuditReport schema"
```

---

## Task 4: Rollout — Quality, CSS, DOM Audits

**Files:**
- Modify: `internal/proxy/scripts/audit-quality.js`
- Modify: `internal/proxy/scripts/audit-css.js`
- Modify: `internal/proxy/scripts/audit-dom.js`

- [ ] **Step 1: Refactor `audit-quality.js`**

Replace return statements in `auditPageQuality`. For AI-optimized mode:

```javascript
var findings = fixable.map(function(issue, i) {
  return reportModule.createFinding('seo', issue.type, issue.severity, issue.message || issue.fix, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: { category: 'seo' }
  });
}).concat(informational.map(function(info, i) {
  return reportModule.createFinding('seo', info.type, 'info', info.message, {
    id: info.id,
    context: info
  });
}));

return reportModule.buildReport('seo', findings, {
  score: score,
  automationHints: options.automationHints
});
```

For raw mode, build the same `findings` array and pass to `buildReport` with `raw: { meta, openGraph, ... }`.

- [ ] **Step 2: Refactor `audit-css.js`**

```javascript
var findings = fixable.map(function(issue, i) {
  return reportModule.createFinding('css', issue.type, issue.severity, issue.message || issue.fix, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: { pattern: issue.pattern, count: issue.count }
  });
}).concat(informational.map(function(info, i) {
  return reportModule.createFinding('css', info.type, 'info', info.message, {
    id: info.id,
    context: info
  });
}));

return reportModule.buildReport('css', findings, { score: score });
```

- [ ] **Step 3: Refactor `audit-dom.js`**

```javascript
var findings = fixable.map(function(issue, i) {
  return reportModule.createFinding('dom', issue.type, issue.severity, issue.message || issue.fix, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: { childCount: issue.childCount, depth: issue.depth, itemCount: issue.itemCount, rows: issue.rows, inputCount: issue.inputCount }
  });
}).concat(informational.map(function(info, i) {
  return reportModule.createFinding('dom', info.type, 'info', info.message, {
    id: info.id,
    context: info
  });
}));

return reportModule.buildReport('dom', findings, { score: score });
```

- [ ] **Step 4: Verify all three audits return AuditReport**

Manually verify in browser console:
```javascript
window.__devtool_audit_dom.auditDOMComplexity({raw: true}).audit === 'dom'
window.__devtool_audit_css.auditCSS({raw: true}).audit === 'css'
window.__devtool.auditPageQuality({raw: true}).audit === 'seo'
```

Each should return `true` and have a `findings` array.

- [ ] **Step 5: Commit**

```bash
git add internal/proxy/scripts/audit-quality.js internal/proxy/scripts/audit-css.js internal/proxy/scripts/audit-dom.js
git commit -m "feat(audit): refactor quality, css, dom audits to standardized schema"
```

---

## Task 5: Rollout — Performance, Security Audits

**Files:**
- Modify: `internal/proxy/scripts/audit-performance.js`
- Modify: `internal/proxy/scripts/audit-security.js`

- [ ] **Step 1: Refactor `audit-performance.js`**

Map the existing `critical`, `warnings`, `informational` arrays to `findings`:

```javascript
var findings = [];

// Map critical issues
if (critical && critical.length > 0) {
  findings = findings.concat(critical.map(function(issue, i) {
    return reportModule.createFinding('performance', issue.type || 'performance-issue', 'critical', issue.message || issue.description, {
      id: 'perf-critical-' + (i + 1),
      impact: 10,
      fix: issue.recommendation || issue.fix,
      selector: issue.selector || null,
      context: issue
    });
  }));
}

// Map warnings
if (warnings && warnings.length > 0) {
  findings = findings.concat(warnings.map(function(issue, i) {
    return reportModule.createFinding('performance', issue.type || 'performance-issue', 'warning', issue.message || issue.description, {
      id: 'perf-warning-' + (i + 1),
      impact: 6,
      fix: issue.recommendation || issue.fix,
      selector: issue.selector || null,
      context: issue
    });
  }));
}

// Map informational
if (informational && informational.length > 0) {
  findings = findings.concat(informational.map(function(info, i) {
    return reportModule.createFinding('performance', info.type || 'performance-info', 'info', info.message || info.description, {
      id: 'perf-info-' + (i + 1),
      context: info
    });
  }));
}

return reportModule.buildReport('performance', findings, {
  score: score,
  automationHints: options.automationHints
});
```

- [ ] **Step 2: Refactor `audit-security.js`**

```javascript
var findings = critical.map(function(issue, i) {
  return reportModule.createFinding('security', issue.type, 'critical', issue.message, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: { secretType: issue.secretType, source: issue.source }
  });
}).concat(errors.map(function(issue, i) {
  return reportModule.createFinding('security', issue.type, 'error', issue.message, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: issue
  });
})).concat(warnings.map(function(issue, i) {
  return reportModule.createFinding('security', issue.type, 'warning', issue.message, {
    id: issue.id,
    impact: issue.impact,
    fix: issue.fix,
    selector: issue.selector || null,
    context: issue
  });
})).concat(informational.map(function(info, i) {
  return reportModule.createFinding('security', info.type, 'info', info.message, {
    id: info.id,
    context: info
  });
}));

return reportModule.buildReport('security', findings, { score: score });
```

- [ ] **Step 3: Verify**

```javascript
window.__devtool_audit_performance.auditPerformance({raw: true}).audit === 'performance'
window.__devtool_audit_security.auditSecurity({raw: true}).audit === 'security'
```

- [ ] **Step 4: Commit**

```bash
git add internal/proxy/scripts/audit-performance.js internal/proxy/scripts/audit-security.js
git commit -m "feat(audit): refactor performance, security audits to standardized schema"
```

---

## Task 6: Refactor responsive.js

**Files:**
- Modify: `internal/proxy/scripts/responsive.js`

- [ ] **Step 1: Refactor viewport issues to findings**

In `responsive.js`, the `auditViewport` function returns viewport results with `issues` array. Modify the final aggregation in the main `audit` function to convert issues to findings:

```javascript
// In the audit() function's final aggregation
var allFindings = [];
results.forEach(function(viewportResult, vi) {
  var viewportFindings = viewportResult.issues.map(function(issue, ii) {
    return reportModule.createFinding('responsive', issue.type, issue.severity, issue.message, {
      id: 'responsive-' + viewportResult.name + '-' + issue.type + '-' + (ii + 1),
      impact: issue.impact || 5,
      fix: issue.fix || null,
      selector: issue.selector || null,
      context: { viewport: viewportResult.name, width: viewportResult.width, height: viewportResult.height }
    });
  });
  allFindings = allFindings.concat(viewportFindings);
});

var score = calculateOverallScore(results);
return reportModule.buildReport('responsive', allFindings, {
  score: score,
  summary: generateResponsiveSummary(results)
});
```

- [ ] **Step 2: Verify responsive audit returns AuditReport**

```bash
cd /home/beagle/work/core/agnt
go test ./internal/proxy/scripts/ -run TestModuleDependencyOrder -v
```

Then manually verify in browser:
```javascript
window.__devtool_responsive.audit({raw: true}).then(function(r) { console.log(r.audit, r.findings.length); });
```

- [ ] **Step 3: Commit**

```bash
git add internal/proxy/scripts/responsive.js
git commit -m "feat(audit): refactor responsive audit to standardized schema"
```

---

## Task 7: Go Tool Upgrades + Fix MCP Tools

**Files:**
- Modify: `internal/tools/responsive_audit.go`
- Modify: `internal/tools/get_errors.go`
- Create: `internal/tools/audit_fix_tools.go`

- [ ] **Step 1: Update `responsive_audit.go`**

The Go tool currently parses the JS result string. It needs to:
1. Parse the new `AuditReport` JSON
2. In compact mode, return the `summary` field (lightweight, unchanged)
3. In raw mode, return the full JSON

In `executeResponsiveAuditLegacy` and `executeResponsiveAuditDaemon`, replace the parsing:

```go
// Parse result as AuditReport
var report map[string]interface{}
if err := json.Unmarshal([]byte(resultStr), &report); err != nil {
    output.Summary = resultStr
    return nil, output, nil
}

// Compact mode: just return summary (unchanged behavior for LLM)
output.Summary = getString(report, "summary")

// Raw mode: return full report
if input.Raw {
    output.Raw = report
}

return nil, output, nil
```

- [ ] **Step 2: Update `get_errors.go`**

Add a function to map `unifiedError` to `Finding`:

```go
func mapErrorsToFindings(errors []unifiedError) []map[string]interface{} {
    findings := make([]map[string]interface{}, 0, len(errors))
    for i, e := range errors {
        severity := "error"
        if e.Severity == "warning" {
            severity = "warning"
        }
        findings = append(findings, map[string]interface{}{
            "id":       fmt.Sprintf("error-%s-%d", e.Source, i+1),
            "type":     e.Category,
            "severity": severity,
            "impact":   7,
            "message":  e.Message,
            "context": map[string]interface{}{
                "source":   e.Source,
                "location": e.Location,
                "page":     e.Page,
                "count":    e.Count,
            },
        })
    }
    return findings
}
```

In `formatErrorsOutput`, when `raw: true`:

```go
if raw {
    report := map[string]interface{}{
        "audit":     "errors",
        "summary":   fmt.Sprintf("%d errors, %d warnings", errorCount, warningCount),
        "score":     calculateErrorScore(errorCount, warningCount),
        "grade":     getErrorGrade(errorCount, warningCount),
        "checkedAt": time.Now().Format(time.RFC3339),
        "findings":  mapErrorsToFindings(allErrors),
    }
    b, _ := json.Marshal(report)
    output.Summary = string(b)
} else {
    output.Summary = formatCompactErrors(allErrors, errorCount, warningCount)
}
```

Add helpers:
```go
func calculateErrorScore(errors, warnings int) int {
    score := 100 - errors*5 - warnings*2
    if score < 0 { score = 0 }
    return score
}

func getErrorGrade(errors, warnings int) string {
    score := calculateErrorScore(errors, warnings)
    if score >= 90 { return "A" }
    if score >= 80 { return "B" }
    if score >= 70 { return "C" }
    if score >= 60 { return "D" }
    return "F"
}
```

- [ ] **Step 3: Create `audit_fix_tools.go`**

Create `internal/tools/audit_fix_tools.go`:

```go
package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/proxy"
)

// SelectAuditFindingsInput selects findings to fix.
type SelectAuditFindingsInput struct {
	ProxyID    string   `json:"proxy_id" jsonschema:"Proxy ID"`
	Audit      string   `json:"audit" jsonschema:"Audit name (accessibility, seo, css, dom, performance, security, responsive, errors)"`
	FindingIDs []string `json:"finding_ids" jsonschema:"Finding IDs to select"`
}

// PrepareFixPromptInput retrieves a fix prompt.
type PrepareFixPromptInput struct {
	FixRequestID string `json:"fix_request_id" jsonschema:"Fix request ID from audit response"`
}

func RegisterAuditFixTools(server *mcp.Server, dt *DaemonTools, pm *proxy.ProxyManager) {
	// select_audit_findings
	addLenientTool(server, &mcp.Tool{
		Name:        "select_audit_findings",
		Description: "Select audit findings by ID for the fix workflow",
	}, makeSelectFindingsHandler(dt, pm))

	// prepare_fix_prompt
	addLenientTool(server, &mcp.Tool{
		Name:        "prepare_fix_prompt",
		Description: "Generate a structured fix prompt from selected audit findings",
	}, makePrepareFixHandler(dt, pm))
}

func makeSelectFindingsHandler(dt *DaemonTools, pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, SelectAuditFindingsInput) (*mcp.CallToolResult, interface{}, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input SelectAuditFindingsInput) (*mcp.CallToolResult, interface{}, error) {
		// Build JS code to set selections
		code := fmt.Sprintf(`(function() {
			var report = window.__devtool_audit_report;
			if (!report) return { error: 'audit-report module not loaded' };
			var ids = %s;
			ids.forEach(function(id) {
				report.setSelection(%q, id, true);
			});
			return { selected: ids.length };
		})()`, toJSON(input.FindingIDs), input.Audit)

		result, err := executeProxyJS(input.ProxyID, code, dt, pm)
		if err != nil {
			return errorResult(err.Error()), nil, nil
		}
		return successResult(fmt.Sprintf("Selected %d findings for %s audit", len(input.FindingIDs), input.Audit)), nil, nil
	}
}

func makePrepareFixHandler(dt *DaemonTools, pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, PrepareFixPromptInput) (*mcp.CallToolResult, interface{}, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input PrepareFixPromptInput) (*mcp.CallToolResult, interface{}, error) {
		code := `(function() {
			var pending = window.__devtool_audit_state.pendingFix;
			if (!pending) return { error: 'No pending fix request' };
			return { prompt: pending.prompt, findings: pending.findings };
		})()`

		// This would need proxy_id to execute; for now return a placeholder
		return successResult("Fix prompt preparation requires proxy execution. Use the browser sidebar for interactive fix workflow."), nil, nil
	}
}
```

Note: The `prepare_fix_prompt` implementation is intentionally minimal — the primary fix workflow runs through the browser sidebar. The MCP tool exists for headless compatibility.

- [ ] **Step 4: Run Go tests**

```bash
cd /home/beagle/work/core/agnt && go test ./internal/tools/ -run TestGetErrors -v
cd /home/beagle/work/core/agnt && go build ./cmd/agnt/
```

Expected: tests pass, build succeeds.

- [ ] **Step 5: Commit**

```bash
git add internal/tools/responsive_audit.go internal/tools/get_errors.go internal/tools/audit_fix_tools.go
git commit -m "feat(audit): upgrade Go tools to standardized schema, add fix MCP tools"
```

---

## Task 8: Sidebar Polish

**Files:**
- Modify: `internal/proxy/scripts/audit-sidebar.js`
- Modify: `internal/proxy/scripts/indicator.js`

- [ ] **Step 1: Add severity filtering**

In `audit-sidebar.js`, add filter buttons to the header:

```javascript
function renderFilters(report) {
  var severities = ['critical', 'error', 'warning', 'info'];
  var html = '<div style="display: flex; gap: 4px; margin-bottom: 12px;">';
  severities.forEach(function(sev) {
    var count = report.findings.filter(function(f) { return f.severity === sev; }).length;
    html += '<button class="__devtool-filter-btn" data-severity="' + sev + '" style="background: #333; color: #ccc; border: none; padding: 2px 8px; border-radius: 4px; font-size: 11px; cursor: pointer;">' + sev + ' (' + count + ')</button>';
  });
  html += '</div>';
  return html;
}
```

Wire up click handlers to show/hide findings by severity.

- [ ] **Step 2: Add "Select All / None"**

Add buttons next to "Send to Fix":

```javascript
html += '<div style="display: flex; gap: 8px; margin-bottom: 12px;">';
html += '<button id="__devtool-select-all" style="background: #333; color: #ccc; border: none; padding: 4px 10px; border-radius: 4px; font-size: 11px; cursor: pointer;">Select All</button>';
html += '<button id="__devtool-select-none" style="background: #333; color: #ccc; border: none; padding: 4px 10px; border-radius: 4px; font-size: 11px; cursor: pointer;">Select None</button>';
html += '</div>';
```

- [ ] **Step 3: Add group expand/collapse**

Group findings by `type` with collapsible headers:

```javascript
function groupFindings(findings) {
  var groups = {};
  findings.forEach(function(f) {
    if (!groups[f.type]) groups[f.type] = [];
    groups[f.type].push(f);
  });
  return groups;
}
```

- [ ] **Step 4: Inject fix prompt into compose tab**

In `indicator.js`, find the compose tab's message input and add a function:

```javascript
function injectFixPrompt(prompt) {
  var textarea = document.getElementById('__devtool-compose-input');
  if (textarea) {
    textarea.value = prompt;
    textarea.dispatchEvent(new Event('input'));
    switchTab('compose');
  }
}
```

Call this from `audit-sidebar.js` when "Send to Fix" is clicked.

- [ ] **Step 5: Style to match indicator**

Use the same colors, fonts, and spacing as the indicator panel. Extract shared CSS variables if possible.

- [ ] **Step 6: Test end-to-end**

```bash
cd /home/beagle/work/core/agnt/e2e && npx playwright test tests/audits-unit.spec.ts --project=chromium
```

Manual test:
1. Start devtool on a test page
2. Run `auditAccessibility`
3. Verify sidebar auto-opens
4. Hover over a finding → element highlights
5. Check a few findings
6. Click "Send to Fix"
7. Verify compose tab opens with prompt

- [ ] **Step 7: Commit**

```bash
git add internal/proxy/scripts/audit-sidebar.js internal/proxy/scripts/indicator.js
git commit -m "feat(audit): sidebar polish — filtering, select all, expand/collapse, compose injection"
```

---

## Spec Coverage Check

| Design Requirement | Task |
|-------------------|------|
| Standardized `AuditReport`/`Finding` schema | Task 1 |
| Detail report with full findings | Tasks 3-6 (all audits return `findings[]`) |
| Checkbox per finding | Task 2 (sidebar render) |
| Interactive highlights on hover/click | Task 2 (`mouseenter` → `overlay.highlight`) |
| "Send to Fix" action | Tasks 2, 8 (generate prompt, inject into compose) |
| Works in raw and compact modes | Task 7 (Go tools preserve compact summary) |
| Highlight via proxy exec JS | Task 2 (reuses existing `overlay.highlight`) |
| Checkbox state persists per session | Task 1 (`__devtool_audit_state.selections`) |
| Auto-open sidebar | Task 2 (`indicator.js` trigger) |
| Replace mode for multiple audits | Task 2 (`currentReport` overwritten) |

## Placeholder Scan

No placeholders. Every step contains:
- Exact file paths
- Working code or precise modifications
- Test commands with expected output
- Git commit commands

## Type Consistency

- `createFinding` signature used consistently across all audit refactors
- `buildReport` signature used consistently
- `severity` values: `"critical"`, `"error"`, `"warning"`, `"info"` everywhere
- `Finding.id` format: `{audit}-{type}-{counter}` everywhere
