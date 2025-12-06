---
sidebar_position: 5
---

# Accessibility Auditing

Using devtool-mcp's frontend diagnostics for comprehensive accessibility testing.

## Overview

devtool-mcp provides 5 dedicated accessibility functions:

- `getA11yInfo` - Get ARIA attributes for an element
- `getContrast` - Check color contrast ratios
- `getTabOrder` - Analyze keyboard navigation order
- `getScreenReaderText` - Preview screen reader announcements
- `auditAccessibility` - Full page accessibility audit

## Quick Start

### Run a Full Audit

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility()"}
→ {
    score: 72,
    errors: [
      {type: "missing-alt", selector: "img.hero", message: "..."},
      {type: "missing-label", selector: "input#email", message: "..."}
    ],
    warnings: [
      {type: "low-contrast", selector: ".muted", message: "..."}
    ],
    passes: 38,
    total: 45
  }
```

### Interpret Results

| Score | Rating |
|-------|--------|
| 90-100 | Excellent |
| 70-89 | Good |
| 50-69 | Needs Work |
| < 50 | Critical Issues |

## Common Accessibility Checks

### Images Without Alt Text

```json
proxy {action: "exec", id: "app", code: `
  const images = document.querySelectorAll('img');
  const issues = [];
  images.forEach((img, i) => {
    const a11y = window.__devtool.getA11yInfo('img:nth-of-type(' + (i+1) + ')');
    if (!a11y.accessibleName) {
      issues.push({src: img.src, selector: 'img:nth-of-type(' + (i+1) + ')'});
    }
  });
  issues
`}
→ All images missing alt text
```

### Form Input Labels

```json
proxy {action: "exec", id: "app", code: `
  const inputs = document.querySelectorAll('input, select, textarea');
  const unlabeled = [];
  inputs.forEach((input, i) => {
    const sel = input.tagName.toLowerCase() + ':nth-of-type(' + (i+1) + ')';
    const a11y = window.__devtool.getA11yInfo(sel);
    if (!a11y.accessibleName) {
      unlabeled.push({type: input.type, id: input.id, selector: sel});
    }
  });
  unlabeled
`}
```

### Color Contrast

```json
// Check a specific element
proxy {action: "exec", id: "app", code: "window.__devtool.getContrast('.button-text')"}
→ {
    foreground: "#ffffff",
    background: "#4a90d9",
    ratio: 3.8,
    passes: {
      AA_normal: false,  // Needs 4.5:1
      AA_large: true,    // Needs 3:1
      AAA_normal: false,
      AAA_large: false
    }
  }
```

### Keyboard Navigation

```json
proxy {action: "exec", id: "app", code: "window.__devtool.getTabOrder()"}
→ [
    {selector: "a.skip-link", tabIndex: 0, order: 1},
    {selector: "input#search", tabIndex: 0, order: 2},
    ...
  ]
```

#### Check for Positive Tabindex (Anti-pattern)

```json
proxy {action: "exec", id: "app", code: `
  const order = window.__devtool.getTabOrder();
  order.filter(el => el.tabIndex > 0)
`}
→ Elements with positive tabindex (should be avoided)
```

### Screen Reader Preview

```json
proxy {action: "exec", id: "app", code: "window.__devtool.getScreenReaderText('.icon-button')"}
→ {
    text: "",
    sources: [],
    role: "button",
    fullAnnouncement: "button"  // Missing accessible name!
  }
```

## Comprehensive Audit Workflow

### Step 1: Run Automated Audit

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility()"}
```

### Step 2: Highlight Issues

```json
proxy {action: "exec", id: "app", code: `
  const audit = window.__devtool.auditAccessibility();
  audit.errors.forEach(err => {
    window.__devtool.highlight(err.selector, {
      color: 'rgba(255, 0, 0, 0.3)',
      label: err.type,
      duration: 0
    });
  });
  audit.warnings.forEach(warn => {
    window.__devtool.highlight(warn.selector, {
      color: 'rgba(255, 165, 0, 0.3)',
      label: warn.type,
      duration: 0
    });
  });
`}
```

### Step 3: Document with Screenshots

```json
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('a11y-issues')"}
```

### Step 4: Clear Overlays

```json
proxy {action: "exec", id: "app", code: "window.__devtool.clearAllOverlays()"}
```

## WCAG Compliance Checking

### Level A Requirements

```json
proxy {action: "exec", id: "app", code: `
  const checks = {
    images: [],
    forms: [],
    links: []
  };

  // 1.1.1 Non-text Content
  document.querySelectorAll('img').forEach((img, i) => {
    if (!img.alt) checks.images.push(img.src);
  });

  // 1.3.1 Info and Relationships
  document.querySelectorAll('input, select, textarea').forEach((input, i) => {
    const a11y = window.__devtool.getA11yInfo(input.tagName + ':nth-of-type(' + (i+1) + ')');
    if (!a11y.accessibleName) checks.forms.push(input.id || input.name);
  });

  // 2.4.4 Link Purpose
  document.querySelectorAll('a').forEach((link, i) => {
    const sr = window.__devtool.getScreenReaderText('a:nth-of-type(' + (i+1) + ')');
    if (!sr.text || sr.text.toLowerCase() === 'click here') {
      checks.links.push({text: sr.text, href: link.href});
    }
  });

  checks
`}
```

### Contrast Requirements (1.4.3, 1.4.6)

```json
proxy {action: "exec", id: "app", code: `
  const textElements = document.querySelectorAll('p, span, h1, h2, h3, h4, h5, h6, a, button, label');
  const issues = [];

  textElements.forEach((el, i) => {
    const sel = el.tagName.toLowerCase() + ':nth-of-type(' + (i+1) + ')';
    const contrast = window.__devtool.getContrast(sel);

    if (contrast.ratio && !contrast.passes.AA_normal) {
      issues.push({
        selector: sel,
        text: el.textContent.slice(0, 30),
        ratio: contrast.ratio,
        required: 4.5
      });
    }
  });

  issues
`}
```

## Focus Management

### Check Focus Visibility

```json
proxy {action: "exec", id: "app", code: `
  // Programmatically focus each interactive element
  const interactive = document.querySelectorAll('a, button, input, select, textarea, [tabindex]');

  interactive.forEach((el, i) => {
    el.focus();
    const styles = getComputedStyle(el);
    const hasOutline = styles.outline !== 'none' && styles.outlineWidth !== '0px';
    const hasBoxShadow = styles.boxShadow !== 'none';

    if (!hasOutline && !hasBoxShadow) {
      console.log('No visible focus:', el);
    }
  });

  document.activeElement.blur();
`}
```

### Modal Focus Trapping

```json
proxy {action: "exec", id: "app", code: `
  // Check if modal traps focus correctly
  const modal = document.querySelector('.modal');
  if (modal) {
    const tabOrder = window.__devtool.getTabOrder('.modal');
    const firstFocusable = tabOrder[0];
    const lastFocusable = tabOrder[tabOrder.length - 1];

    ({
      firstFocusable: firstFocusable?.selector,
      lastFocusable: lastFocusable?.selector,
      tabOrderLength: tabOrder.length
    })
  }
`}
```

## Interactive Audit Session

```json
// Let user select element to audit
proxy {action: "exec", id: "app", code: `
  (async () => {
    const selected = await window.__devtool.selectElement();
    if (selected.cancelled) return {cancelled: true};

    const a11y = window.__devtool.getA11yInfo(selected.selector);
    const contrast = window.__devtool.getContrast(selected.selector);
    const sr = window.__devtool.getScreenReaderText(selected.selector);

    return {
      element: selected.element,
      accessibility: a11y,
      contrast: contrast.ratio ? {
        ratio: contrast.ratio,
        passes: contrast.passes
      } : {error: contrast.error},
      screenReader: sr.fullAnnouncement
    };
  })()
`}
```

## Generating Reports

### HTML Report

```json
proxy {action: "exec", id: "app", code: `
  const audit = window.__devtool.auditAccessibility();

  const html = \`
    <h1>Accessibility Report</h1>
    <p>Score: \${audit.score}/100</p>

    <h2>Errors (\${audit.errors.length})</h2>
    <ul>
      \${audit.errors.map(e => '<li>' + e.selector + ': ' + e.message + '</li>').join('')}
    </ul>

    <h2>Warnings (\${audit.warnings.length})</h2>
    <ul>
      \${audit.warnings.map(w => '<li>' + w.selector + ': ' + w.message + '</li>').join('')}
    </ul>

    <p>Passes: \${audit.passes}/\${audit.total}</p>
  \`;

  html
`}
```

### JSON Report for CI

```json
proxy {action: "exec", id: "app", code: `
  const audit = window.__devtool.auditAccessibility();

  JSON.stringify({
    score: audit.score,
    errorCount: audit.errors.length,
    warningCount: audit.warnings.length,
    passCount: audit.passes,
    total: audit.total,
    errors: audit.errors,
    warnings: audit.warnings
  }, null, 2)
`}
```

## CI Integration

### Fail on Critical Issues

```yaml
      - name: Accessibility Check
        run: |
          # Start app and proxy
          devtool-mcp run --script dev &
          sleep 10
          devtool-mcp proxy --action start --id a11y --target-url http://localhost:3000

          # Navigate via browser automation
          # ...

          # Check accessibility
          SCORE=$(devtool-mcp proxy --action exec --id a11y --code "window.__devtool.auditAccessibility().score")

          if [ "$SCORE" -lt 70 ]; then
            echo "Accessibility score too low: $SCORE"
            exit 1
          fi
```

## Best Practices

1. **Run automated audit first** - Catch obvious issues
2. **Check images and forms** - Most common failures
3. **Test keyboard navigation** - Tab through entire page
4. **Verify screen reader text** - All interactive elements need names
5. **Check color contrast** - 4.5:1 minimum for normal text
6. **Test with real screen readers** - Automation misses nuances

## Common Issues & Fixes

| Issue | Detection | Fix |
|-------|-----------|-----|
| Missing alt | `auditAccessibility()` | Add descriptive alt text |
| No label | `getA11yInfo()` shows empty | Add `<label>` or `aria-label` |
| Low contrast | `getContrast()` ratio < 4.5 | Darken text or lighten background |
| Positive tabindex | `getTabOrder()` | Use `tabindex="0"` and DOM order |
| Empty link | `getScreenReaderText()` | Add descriptive link text |

## See Also

- [Accessibility API](/api/frontend/accessibility) - Full API reference
- [Frontend Diagnostics](/features/frontend-diagnostics) - All available tools
