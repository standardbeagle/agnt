---
description: "Run comprehensive accessibility audit on the current page"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Run a comprehensive accessibility (a11y) audit on the current browser page using agnt's diagnostic tools.

## Steps

1. Run the full accessibility audit:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}
   ```

2. Get the tab order to check keyboard navigation:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.getTabOrder()"}
   ```

3. Take a screenshot for reference:
   ```
   proxy {action: "exec", id: "dev", code: "__devtool.screenshot('a11y-audit')"}
   ```

## What the Audit Checks

The accessibility audit checks for:

### Critical Issues (Errors)
- **Images without alt text** - Screen readers can't describe the image
- **Form inputs without labels** - Users can't understand what to enter
- **Buttons without accessible names** - Users don't know what the button does
- **Empty links** - Links with no text content or aria-label

### Warnings
- **Links without href** - May cause navigation issues

## Interpreting Results

The audit returns:
- `issues`: Array of accessibility problems found
- `count`: Total number of issues
- `errors`: Number of critical issues
- `warnings`: Number of non-critical issues

For each issue:
- `type`: The type of accessibility violation
- `severity`: "error" or "warning"
- `selector`: CSS selector to locate the element
- `message`: Description of the problem

## Additional Diagnostic Tools

For deeper accessibility analysis:

```
// Get detailed accessibility info for a specific element
proxy {action: "exec", id: "dev", code: "__devtool.getA11yInfo('#element')"}

// Check color contrast between foreground and background
proxy {action: "exec", id: "dev", code: "__devtool.getContrast('rgb(0,0,0)', 'rgb(255,255,255)')"}

// Get what a screen reader would announce for an element
proxy {action: "exec", id: "dev", code: "__devtool.getScreenReaderText('#element')"}
```

## WCAG Guidelines Reference

- **4.5:1** contrast ratio required for normal text (AA)
- **3:1** contrast ratio required for large text (AA)
- **7:1** contrast ratio required for enhanced contrast (AAA)
