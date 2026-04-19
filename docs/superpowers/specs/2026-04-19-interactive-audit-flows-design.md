# Interactive Audit Flows Design

**Date:** 2026-04-19
**Status:** Design Complete, Ready for Implementation
**Task:** [Upgrade audit flows: interactive detail reports with checkboxes and fix selection](https://app.dartai.com/t/i9Wj5kigYmIA)
**Estimated Effort:** ~5 days (Large)

## Overview

Upgrade all audit flows to produce detail reports with full findings, enable interactive element highlighting, and provide a "Send to Fix" workflow. Audits currently return either compact AI-optimized summaries or raw JSON, but neither format supports interactive selection of individual issues for remediation.

## Problem Statement

Current audit outputs have these limitations:

1. **No granular selection** — AI-optimized mode groups issues by type; users cannot pick individual findings to fix
2. **No visual correlation** — Findings mention selectors but there's no way to see which element on the page is affected
3. **No fix workflow** — After identifying issues, users must manually translate findings into action items
4. **Inconsistent raw output** — Each audit uses slightly different schema for its `fixable`/`informational` arrays
5. **No session persistence** — Re-running an audit loses any mental context about which issues were prioritized

## Decision Log

| Question | Decision | Rationale |
|----------|----------|-----------|
| Checkbox UI | Browser indicator panel + MCP output with IDs | Panel gives rich visual interaction; MCP IDs enable headless/programmatic use |
| Fix workflow | Generate structured fix prompt for AI agent | Leverages existing AI chat flow; no new backend infrastructure needed |
| Highlight mechanism | Reuse `__devtool_overlay.highlight()` via selector | Already implemented, supports both selector and bounding box |
| Implementation order | Pilot a11y → responsive → proxy exec audits → get_errors | A11y has the richest `fixable` array; get_errors has no DOM context |
| Session persistence | Store selected IDs in `__devtool_audit_state` keyed by `auditName + url` | Simple, survives re-audit, cleared on navigation |

## Architecture

### System Diagram

```
┌─────────────────────────────────────────────────────────────────────┐
│  Browser Page                                                        │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  DevTool Indicator Panel (VanJS) — New "Audits" tab          │  │
│  │                                                               │  │
│  │  ┌─────────────────────────────────────────────────────────┐ │  │
│  │  │  Audit Detail View                                      │ │  │
│  │  │  ┌─────────────────────────────────────────────────┐   │ │  │
│  │  │  │ Score: 72/100 (C)  [Re-run] [Clear]            │   │ │  │
│  │  │  ├─────────────────────────────────────────────────┤   │ │  │
│  │  │  │ ☑ Missing alt text (3)                         │   │ │  │
│  │  │  │   ☐ img.logo — "Add alt attribute"            │   │ │  │
│  │  │  │   ☑ img.hero — "Add alt attribute"            │   │ │  │
│  │  │  │   ☐ img.nav-icon — "Add alt attribute"        │   │ │  │
│  │  │  ├─────────────────────────────────────────────────┤   │ │  │
│  │  │  │ ☑ Color contrast (2)                           │   │ │  │
│  │  │  │   ☑ .btn-primary — "Increase contrast ratio"  │   │ │  │
│  │  │  │   ☑ .text-muted — "Increase contrast ratio"   │   │ │  │
│  │  │  ├─────────────────────────────────────────────────┤   │ │  │
│  │  │  │ [Send 3 selected to Fix]                       │   │ │  │
│  │  │  └─────────────────────────────────────────────────┘   │ │  │
│  │  └─────────────────────────────────────────────────────────┘ │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  Overlay Layer (pointer-events: none)                         │  │
│  │  ┌─────────────────────────────────────────┐                  │  │
│  │  │  Highlight box around affected element  │ ◄── hover/click │  │
│  │  └─────────────────────────────────────────┘                  │  │
│  └───────────────────────────────────────────────────────────────┘  │
│                                                                      │
│  ┌───────────────────────────────────────────────────────────────┐  │
│  │  Audit State (window.__devtool_audit_state)                   │  │
│  │  {                                                            │  │
│  │    "a11y::https://example.com/": ["a11y-alt-1", "a11y-contrast-2"],│  │
│  │    "responsive::https://example.com/": ["responsive-layout-3"] │  │
│  │  }                                                            │  │
│  └───────────────────────────────────────────────────────────────┘  │
└─────────────────────────────────────────────────────────────────────┘
                              │
                              ▼
┌─────────────────────────────────────────────────────────────────────┐
│  MCP Tool Response (raw: true)                                       │
│  {                                                                   │
│    "audit": "accessibility",                                         │
│    "summary": "3 errors, 2 warnings",                                │
│    "score": 72,                                                      │
│    "findings": [                                                     │
│      { "id": "a11y-alt-1", "type": "missing-alt", ... },             │
│      { "id": "a11y-contrast-2", "type": "color-contrast", ... }      │
│    ]                                                                 │
│  }                                                                   │
└─────────────────────────────────────────────────────────────────────┘
```

### Components

1. **`audit-report.js`** — New browser module that standardizes audit output schema and manages session state
2. **`audit-sidebar.js`** — New browser module that renders the left sidebar with findings, checkboxes, and actions
3. **Upgraded audit scripts** — `accessibility.js`, `audit-quality.js`, `audit-css.js`, `audit-dom.js`, `audit-performance.js`, `audit-security.js`, `responsive.js` all return consistent `AuditReport`
4. **Upgraded Go tools** — `responsive_audit.go` and `get_errors.go` return detail reports in both modes

## Standardized Audit Output Schema

All audits return a consistent top-level shape:

```typescript
interface AuditReport {
  audit: string;           // "accessibility" | "seo" | "css" | "dom" | "performance" | "security" | "responsive" | "errors"
  summary: string;         // Human-readable summary
  score: number;           // 0-100
  grade: string;           // "A" | "B" | "C" | "D" | "F"
  checkedAt: string;       // ISO timestamp
  
  // Standardized findings array (replaces fixable/informational/actions)
  findings: Finding[];
  
  // AI-optimized hints (preserved for compact mode consumers)
  automationHints?: {
    lookFor: string[];
    suggestionsNeeded: string[];
  };
  
  // Audit-specific raw data (only in raw mode)
  raw?: any;
}

interface Finding {
  id: string;              // Globally unique: "{audit}-{type}-{counter}"
  type: string;            // Issue category: "missing-alt", "color-contrast", etc.
  severity: "critical" | "error" | "warning" | "info";
  impact: number;          // 1-10
  message: string;         // Human-readable description
  fix?: string;            // Suggested fix
  
  // Element location (omitted for non-DOM findings)
  selector?: string;       // CSS selector for affected element
  element?: string;        // Truncated HTML snippet
  bounds?: {               // Bounding box for highlight
    x: number;
    y: number;
    width: number;
    height: number;
  };
  
  // Contextual data (audit-specific)
  context?: Record<string, any>;
}
```

### Schema Migration by Audit

| Audit | Current arrays | Migration |
|-------|---------------|-----------|
| accessibility | `fixable`, `informational`, `actions` | → `findings` with `severity` mapping |
| seo (quality) | `fixable`, `informational`, `actions` | → `findings` with `severity` mapping |
| css | `fixable`, `informational`, `actions` | → `findings` with `severity` mapping |
| dom | `fixable`, `informational`, `actions` | → `findings` with `severity` mapping |
| performance | `issues` (critical/warnings arrays) | → `findings` with severity mapping |
| security | `critical`, `errors`, `warnings`, `informational` | → `findings` with severity mapping |
| responsive | `issues` per viewport | → `findings` with `context.viewport` |
| errors (get_errors) | `unifiedError[]` | → `findings` without `selector`/`bounds` |

## Browser Panel Design

### Audit Sidebar (Left Panel)

Instead of cramming audit details into the indicator panel's small tab area, the audit report renders in a **dedicated left sidebar** that pushes the main page content to the right.

```
┌──────────────┬──────────────────────────────────────┐
│ Audit Sidebar│  Main Page (shifted right)           │
│              │                                      │
│ Score: 72/100│  ┌────────────────────────────────┐  │
│ (C)          │  │                                │  │
│              │  │  Content shifted by            │  │
│ ☑ Missing alt│  │  sidebar width                 │  │
│   ☐ img.logo │  │                                │  │
│   ☑ img.hero │  │                                │  │
│              │  │                                │  │
│ ☑ Contrast   │  │                                │  │
│   ☑ .btn-pri │  │                                │  │
│              │  └────────────────────────────────┘  │
│ [Send 3 to   │                                      │
│  Fix]        │                                      │
│              │  ┌──┐ ← indicator bug (floating)    │
│ [×]          │  └──┘                                │
└──────────────┴──────────────────────────────────────┘
```

**Behavior:**
- **Triggered by:** Audit completion, clicking an audit notification in the indicator, or calling `__devtool_audit_sidebar.show(report)`
- **Width:** 380px fixed (wide enough for readable findings, narrow enough to leave room for content)
- **Push behavior:** Adds `margin-left: 380px` to `<body>` or a wrapper element; restores on close
- **Close:** `×` button or `Escape` key; body margin restored
- **Coexists with indicator:** The floating indicator bug remains for status/notifications

### Sidebar States

1. **Empty state** — "No audits run yet. Use `responsive_audit`, `auditAccessibility`, or other audit tools to get started."
2. **List state** — Shows all audits run in this session with score/grade; click one to expand
3. **Detail state** — Full view of one audit with grouped findings, checkboxes, and actions

### Detail View

```
┌─ Accessibility Audit ───────────────────────────┐
│ Score: 72/100 (C)              [Re-run] [Clear] │
│                                                 │
│ ☑ Missing alt text                        (3)   │
│   ☐ img.logo      "Add alt attribute"      👁   │
│   ☑ img.hero      "Add alt attribute"      👁   │
│   ☐ img.nav-icon  "Add alt attribute"      👁   │
│                                                 │
│ ☑ Color contrast issues                   (2)   │
│   ☑ .btn-primary  "Increase contrast"      👁   │
│   ☑ .text-muted   "Increase contrast"      👁   │
│                                                 │
│ ☐ Heading hierarchy                       (1)   │
│   ☐ h3.page-title "Fix heading order"      👁   │
│                                                 │
│ [Send 3 selected to Fix]    [Select All]        │
└─────────────────────────────────────────────────┘
```

- **Group checkbox** — toggles all findings in the group
- **Individual checkbox** — toggles one finding; state persisted in `__devtool_audit_state`
- **Eye icon (👁)** — hover to highlight element in the main page; click to persist highlight
- **"Send to Fix"** — formats selected findings into fix prompt

### Sidebar Implementation

```javascript
var sidebarState = {
  container: null,      // The sidebar DOM element
  isOpen: false,
  width: 380,
  originalBodyMargin: ''  // Saved to restore on close
};

function openSidebar(report) {
  // Save original body margin
  sidebarState.originalBodyMargin = document.body.style.marginLeft;
  
  // Create or update sidebar
  var sidebar = getOrCreateSidebar();
  renderReport(sidebar, report);
  
  // Push main content
  document.body.style.marginLeft = sidebarState.width + 'px';
  document.body.style.transition = 'margin-left 0.2s ease';
  
  sidebarState.isOpen = true;
}

function closeSidebar() {
  document.body.style.marginLeft = sidebarState.originalBodyMargin;
  var sidebar = document.getElementById('__devtool-audit-sidebar');
  if (sidebar) sidebar.style.display = 'none';
  sidebarState.isOpen = false;
  
  // Clear any persisted highlights
  window.__devtool_overlay.clearAllOverlays();
}
```

**Style notes:**
- Sidebar is `position: fixed; left: 0; top: 0; bottom: 0; width: 380px`
- `z-index: 2147483646` (just below overlay layer at 2147483647)
- Shadow DOM mount for style isolation (same as indicator)
- Scrollable content area with sticky header for score/actions

### Highlight Interaction

```javascript
// On finding hover
function onFindingHover(finding) {
  if (finding.selector) {
    window.__devtool_overlay.highlight(finding.selector, {
      color: severityColor(finding.severity),
      borderColor: severityBorder(finding.severity),
      duration: 3000  // Auto-remove after 3s unless clicked
    });
  }
}

// On finding click — persist highlight
function onFindingClick(finding) {
  if (finding.selector) {
    window.__devtool_overlay.highlight(finding.selector, {
      color: severityColor(finding.severity),
      borderColor: severityBorder(finding.severity)
      // No duration = persistent until cleared
    });
  }
}
```

### Fix Workflow

When user clicks "Send to Fix":

1. Collect all selected findings from `__devtool_audit_state`
2. Generate structured fix prompt:

```markdown
## Fix Request: 3 accessibility issues selected

### Issue 1: Missing alt text
- **Element:** img.hero
- **Problem:** Image missing alt attribute
- **Fix:** Add alt="[description of image]" attribute

### Issue 2: Color contrast
- **Element:** .btn-primary
- **Problem:** Insufficient color contrast: 2.1:1 (requires 4.5:1)
- **Fix:** Increase contrast between text and background

### Issue 3: Color contrast
- **Element:** .text-muted
- **Problem:** Insufficient color contrast: 3.2:1 (requires 4.5:1)
- **Fix:** Increase contrast between text and background
```

3. Store the fix prompt in `__devtool_audit_state.pendingFix` with a unique ID
4. Inject prompt into the compose tab's message input (for human users)
5. Show micro-toast: "3 issues sent to fix. Check the Compose tab."
6. The next MCP tool response for this audit can include: `"fixRequestId": "fix-abc123"` hinting that a fix is ready

### AI Agent Fix Workflow

For AI agents operating via MCP (not the browser panel), the fix workflow is:

1. AI runs audit via MCP tool
2. AI sees `fixRequestId` in response (if user selected issues in panel)
3. AI calls new MCP tool `prepare_fix_prompt({fix_request_id})` to retrieve the structured fix prompt
4. AI uses the prompt to generate fixes

If operating headless (no panel), the AI can also select findings by ID:
```json
// MCP tool: select_audit_findings
{
  "proxy_id": "dev",
  "audit": "accessibility",
  "finding_ids": ["a11y-alt-1", "a11y-contrast-2"]
}
```
This stores the selection in `__devtool_audit_state` just like the panel would.

## Session Persistence

```javascript
window.__devtool_audit_state = {
  // Key format: "{auditName}::{pageUrl}"
  // Value: array of selected finding IDs
  selections: {
    "accessibility::https://example.com/": ["a11y-alt-1", "a11y-contrast-2"],
    "responsive::https://example.com/": ["responsive-layout-3"]
  },
  
  // Cached full reports (last run per audit per page)
  reports: {
    "accessibility::https://example.com/": { /* full AuditReport */ },
    "responsive::https://example.com/": { /* full AuditReport */ }
  },
  
  // Currently persisted highlights
  highlights: {
    "a11y-alt-1": "highlight-42",
    "a11y-contrast-2": "highlight-43"
  }
};
```

Persistence rules:
- **Survives re-audit** — selections are keyed by audit+URL, not by run
- **Survives navigation** — cleared when URL changes (new key)
- **Survives panel close/open** — state is in `window`, not panel local state
- **Manual clear** — "Clear" button in panel resets selections for current audit

## MCP Tool Response Changes

### Compact Mode (Default)

Compact mode now includes the full findings list formatted as structured text. This is a **change from current behavior** — previously compact mode returned only a summary. The new compact output includes:

1. A brief summary header
2. The full findings list grouped by type, with severity icons
3. A footer noting how many issues are selected (if any)

```
=== Accessibility Audit: 72/100 (C) ===

ERRORS (3):
  ✗ [missing-alt] img.logo — Image missing alt attribute
    → Fix: Add alt="[description of image]" attribute
  ✗ [missing-alt] img.hero — Image missing alt attribute
    → Fix: Add alt="[description of image]" attribute
  ✗ [color-contrast] .btn-primary — Insufficient contrast: 2.1:1
    → Fix: Increase contrast between text and background

WARNINGS (2):
  ⚠ [heading-skip] h3.page-title — Heading level skipped: h1 to h3
    → Fix: Change to h2 or add intermediate headings

INFO (1):
  ○ [focusable-count] 24 focusable elements found

3 issues selected for fixing. Use the Audits panel or call
prepare_fix_prompt to generate a fix workflow.
```

The JS audit functions store the full `AuditReport` in `window.__devtool_audit_state.reports` so the panel can access structured data regardless of output mode.

### Raw Mode (`raw: true`)

Returns the full `AuditReport` JSON with the standardized `findings` array:

```json
{
  "audit": "accessibility",
  "summary": "3 errors, 2 warnings found across 10 checks",
  "score": 72,
  "grade": "C",
  "checkedAt": "2026-04-19T17:30:00Z",
  "findings": [
    {
      "id": "a11y-alt-1",
      "type": "missing-alt",
      "severity": "error",
      "impact": 9,
      "message": "Image missing alt attribute",
      "fix": "Add alt=\"[description of image]\" attribute",
      "selector": "img.hero",
      "element": "<img class=\"hero\" src=\"/hero.jpg\">"
    }
  ]
}
```

## Implementation Plan

### Phase 1: Foundation — Standardized Schema (Day 1)

**Goal:** Define and implement the `AuditReport`/`Finding` schema in JS.

**Files:**
- `internal/proxy/scripts/audit-report.js` (new)

**Tasks:**
1. Create `audit-report.js` module with:
   - `createFinding(audit, type, severity, message, options)` helper
   - `buildReport(audit, findings, options)` — standardizes any audit's output
   - `persistReport(report)` — stores in `__devtool_audit_state`
   - `getSelections(audit)` / `setSelection(audit, findingId, selected)` — session persistence
2. Add to `scripts/embed.go` bundle
3. Add module reference in `api.js`

### Phase 2: Pilot — Accessibility Audit (Day 1-2)

**Goal:** Upgrade one audit end-to-end to validate the pattern.

**Files:**
- `internal/proxy/scripts/accessibility.js` (modify)
- `internal/proxy/scripts/audit-sidebar.js` (new, minimal)
- `internal/proxy/scripts/indicator.js` (modify — add open-sidebar button/notification)

**Tasks:**
1. Refactor `accessibility.js` to use `audit-report.js` helpers
   - Replace `fixable`/`informational`/`actions` with unified `findings[]`
   - Ensure every finding has a stable `id`, `selector`, `message`, `fix`
   - Return `AuditReport` shape in both raw and compact modes
2. Create minimal `audit-sidebar.js`:
   - Render findings list with checkboxes in a left sidebar
   - Push main content with `body.style.marginLeft`
   - Wire up hover → `__devtool_overlay.highlight()`
   - Wire up "Send to Fix" → inject prompt into compose tab
3. Add audit-complete notification to indicator with "View Details" button that opens sidebar
4. Test: run `auditAccessibility`, verify panel shows findings, verify highlight works

### Phase 3: Rollout — All Browser Audits (Day 2-3)

**Goal:** Apply the standardized schema to all proxy exec audits.

**Files:**
- `internal/proxy/scripts/audit-quality.js` (modify)
- `internal/proxy/scripts/audit-css.js` (modify)
- `internal/proxy/scripts/audit-dom.js` (modify)
- `internal/proxy/scripts/audit-performance.js` (modify)
- `internal/proxy/scripts/audit-security.js` (modify)
- `internal/proxy/scripts/responsive.js` (modify)

**Tasks:**
1. Refactor each audit to return `AuditReport` via `audit-report.js`
2. Ensure all DOM-related findings include `selector`
3. Ensure responsive audit findings include `context.viewport`
4. Update `audit-panel.js` to group findings by `context.viewport` for responsive audits
5. Test each audit individually

### Phase 4: Go Tool Upgrades (Day 3-4)

**Goal:** Upgrade `responsive_audit` and `get_errors` Go tools; add fix workflow MCP tools.

**Files:**
- `internal/tools/responsive_audit.go` (modify)
- `internal/tools/get_errors.go` (modify)
- `internal/tools/audit_fix_tools.go` (new)
- `internal/proxy/scripts/responsive.js` (modify — already in Phase 3)

**Tasks:**
1. `responsive_audit.go`:
   - Parse the new `AuditReport` JSON from JS
   - Compact mode: formatted text with full findings + store report in proxy state
   - Raw mode: return full `AuditReport` JSON
   - Include `fixRequestId` in response if pending fixes exist
2. `get_errors.go`:
   - Map `unifiedError` to `Finding` schema
   - No `selector`/`bounds` (no DOM context)
   - Add `context.source` and `context.location` to findings
   - Return `AuditReport` shape in both modes
3. `audit_fix_tools.go`:
   - `prepare_fix_prompt` — takes `fix_request_id`, returns structured fix prompt from `__devtool_audit_state`
   - `select_audit_findings` — takes `proxy_id`, `audit`, `finding_ids`, stores selection in audit state

### Phase 5: Panel Polish & Integration (Day 4-5)

**Goal:** Complete the interactive experience.

**Files:**
- `internal/proxy/scripts/audit-sidebar.js` (complete)
- `internal/proxy/scripts/indicator.js` (modify)

**Tasks:**
1. Add severity filtering (show/hide info/warning/error)
2. Add "Select All" / "Select None" buttons
3. Add group expand/collapse
4. Add audit run history (list of past audits in session)
5. Style sidebar to match existing indicator design language
6. Add keyboard shortcuts (`Esc` to close sidebar, `H` to clear highlights)
7. Make sidebar resizable (drag edge to resize)
8. Test end-to-end: run audit → sidebar opens → select findings → highlight elements → send to fix → verify prompt

## Testing Strategy

1. **Unit tests** for `audit-report.js` helpers
2. **Integration tests** for each upgraded audit (verify `findings[]` schema)
3. **E2E tests** for panel interactions (hover highlight, checkbox state, send to fix)
4. **Regression tests** — ensure compact text output remains similar for AI consumption

## Files Modified/Created

### New Files
| File | Purpose |
|------|---------|
| `internal/proxy/scripts/audit-report.js` | Standardized schema and session state |
| `internal/proxy/scripts/audit-sidebar.js` | Left sidebar UI for audit details |
| `internal/tools/audit_fix_tools.go` | MCP tools for fix workflow |
| `docs/superpowers/specs/2026-04-19-interactive-audit-flows-design.md` | This design doc |

### Modified Files
| File | Change |
|------|--------|
| `internal/proxy/scripts/accessibility.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/audit-quality.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/audit-css.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/audit-dom.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/audit-performance.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/audit-security.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/responsive.js` | Return `AuditReport` with `findings[]` |
| `internal/proxy/scripts/indicator.js` | Add "Audits" tab, wire up panel |
| `internal/proxy/scripts/api.js` | Export `audit-report` module |
| `internal/proxy/scripts/embed.go` | Bundle `audit-report.js` and `audit-panel.js` |
| `internal/tools/responsive_audit.go` | Parse new schema, store report |
| `internal/tools/get_errors.go` | Map errors to `Finding` schema |
| `internal/tools/audit_fix_tools.go` | New MCP tools: `prepare_fix_prompt`, `select_audit_findings` |

## Not Included

1. **Screenshot capture per finding** — Out of scope; would require significant new infrastructure
2. **Excalidraw overlay arrows** — CSS outline via existing overlay is sufficient
3. **TUI/terminal checkboxes** — Browser panel is the primary UI; MCP output with IDs supports headless
4. **Cross-session persistence** — Only per-page-session; no localStorage/database
5. **Auto-fix application** — Only generates fix prompts; does not apply fixes automatically
6. **Real-time audit updates** — No watch mode; manual re-run only

## Success Criteria

1. All 6 audit flows return standardized `AuditReport` with `findings[]`
2. Browser panel displays findings with checkboxes for all browser-based audits
3. Hovering a finding highlights the affected element via overlay
4. Clicking "Send to Fix" injects a structured prompt into the compose tab
5. Checkbox selections persist across re-audits of the same page
6. Raw mode returns full detail reports; compact mode remains AI-consumable
7. No regressions in existing audit functionality
