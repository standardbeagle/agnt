# Visual Regression Testing - Usage Guide

## Overview

The visual regression testing feature allows you to capture screenshots as baselines and compare them later to detect unintended visual changes. This is perfect for ensuring your refactors, upgrades, and feature changes only affect what you intended.

## Quick Start

### 1. Capture a Baseline

Before making changes, capture the current state as a baseline:

```javascript
// From the browser (via proxy)
const pages = [
  {
    url: window.location.href,
    viewport: { width: window.innerWidth, height: window.innerHeight },
    screenshot_data: await captureScreenshotAsBase64() // Your screenshot capture
  }
];

// Via MCP tool
await mcp.callTool('snapshot', {
  action: 'baseline',
  name: 'before-header-refactor',
  pages: pages
});
```

**Output:**
```
✓ Baseline 'before-header-refactor' created successfully

Pages captured: 3
Git: feature/header-refactor @ a645d91
```

### 2. Make Your Changes

Refactor code, update CSS, upgrade dependencies - whatever changes you need to make.

### 3. Compare to Baseline

After making changes, capture new screenshots and compare:

```javascript
// Capture current state
const currentPages = [
  {
    url: window.location.href,
    viewport: { width: window.innerWidth, height: window.innerHeight },
    screenshot_data: await captureScreenshotAsBase64()
  }
];

// Compare to baseline
await mcp.callTool('snapshot', {
  action: 'compare',
  baseline: 'before-header-refactor',
  pages: currentPages
});
```

**Output:**
```
Visual Regression Report: before-header-refactor → current
━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━

⚠️  2 of 3 pages changed (3.2% avg diff)

✓ /
   No visual changes detected

❌ /about
   Moderate changes (3.5%)
   Diff: /home/user/.agnt/baselines/before-header-refactor/diff_about.png

❌ /contact
   Significant changes (12.1%)
   Diff: /home/beagle/.agnt/baselines/before-header-refactor/diff_contact.png

━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━
Summary: 2 unexpected changes found
```

## AI Agent Workflow

This is where visual regression really shines with AI-assisted development:

```
User: "Refactor the header component to use modern CSS"

Claude: "I'll capture a baseline before making changes..."
Claude: Uses snapshot tool {action: "baseline", name: "before-header-refactor", pages: [...]}

Claude: *refactors header.css and header.jsx*

Claude: "Let me verify my changes didn't break anything..."
Claude: Uses snapshot tool {action: "compare", baseline: "before-header-refactor", pages: [...]}

Claude: "✓ Changes verified! Only the header layout changed as expected.
         - Desktop: Header height reduced from 80px to 64px ✓
         - Mobile: Navigation adapted correctly ✓
         - No unexpected changes to other pages ✓"
```

## Managing Baselines

### List All Baselines

```javascript
await mcp.callTool('snapshot', {
  action: 'list'
});
```

**Output:**
```
Available baselines (3):

1. before-header-refactor - 3 pages - 2025-12-13 22:30:45 [feature/header-refactor @ a645d91]
2. before-react-upgrade - 5 pages - 2025-12-12 14:15:22 [main @ bfe1f16]
3. pre-css-cleanup - 2 pages - 2025-12-11 09:22:11 [feature/css-cleanup @ 199e427]
```

### Get Baseline Details

```javascript
await mcp.callTool('snapshot', {
  action: 'get',
  name: 'before-header-refactor'
});
```

### Delete a Baseline

```javascript
await mcp.callTool('snapshot', {
  action: 'delete',
  name: 'old-baseline'
});
```

## Storage Structure

Baselines are stored in `~/.agnt/baselines/`:

```
~/.agnt/
├── baselines/
│   ├── before-header-refactor/
│   │   ├── metadata.json
│   │   ├── 0_localhost_3000__a1b2c3d4.png (baseline)
│   │   ├── current_0_localhost_3000__a1b2c3d4.png
│   │   └── diff_0_localhost_3000__a1b2c3d4.png
│   └── before-react-upgrade/
│       └── ...
└── diffs/
    └── before-header-refactor-20251213-223045/
        └── report.json
```

## Configuration

### Diff Threshold

Control sensitivity of change detection (default: 0.01 = 1%):

```javascript
await mcp.callTool('snapshot', {
  action: 'baseline',
  name: 'strict-check',
  pages: pages,
  diff_threshold: 0.001  // 0.1% - very strict
});
```

Lower values = more sensitive to small changes
Higher values = only detect major changes

## Use Cases

### 1. Refactoring Safety Net

```
Before refactor → Baseline
Refactor code → Compare
No changes? Safe to merge!
```

### 2. Dependency Upgrades

```
Before upgrade → Baseline
npm update → Compare
Unexpected styling changes? Rollback or fix
```

### 3. CSS Changes

```
Before CSS update → Baseline
Update styles → Compare
Ripple effects detected? Address them
```

### 4. Responsive Design

```
Capture at 1920x1080 → Desktop baseline
Capture at 375x667 → Mobile baseline
Make changes → Compare both
Ensure responsive behavior maintained
```

### 5. Cross-Browser Testing

```
Capture in Chrome → Chrome baseline
Capture in Firefox → Compare
Different rendering? Fix inconsistencies
```

## Tips

1. **Name baselines clearly**: Use descriptive names like "before-{feature}-{action}"
2. **Capture multiple pages**: Include all affected pages in your baseline
3. **Test multiple viewports**: Capture desktop, tablet, and mobile sizes
4. **Review diff images**: The generated diff images show exactly what changed
5. **Delete old baselines**: Keep your baseline directory clean

## Limitations (MVP)

- Manual screenshot capture (no automated browser control yet)
- Pixel-level diff only (no DOM diffing)
- No ignore regions yet (every pixel is compared)
- No CI/CD integration yet (coming in future phases)
- No AI analysis yet (Claude vision integration coming)

## Next Steps

See `VISUAL_REGRESSION_SPEC.md` for:
- Phase 2: Claude vision integration for intelligent diff analysis
- Phase 3: Multiple viewports, ignore regions, git integration
- Phase 4: CI/CD runners, headless browser, performance regression detection
