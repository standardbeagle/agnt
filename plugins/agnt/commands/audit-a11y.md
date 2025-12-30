---
description: "Run comprehensive accessibility audit on the current page"
allowed-tools: ["mcp__agnt__proxy", "mcp__agnt__proxylog"]
---

Run a comprehensive accessibility (a11y) audit on the current browser page using agnt's diagnostic tools powered by axe-core.

## Steps

### 1. Choose Your Audit Mode

**Standard Mode (Default)** - Industry-standard axe-core audit:
```
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility()"}
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'standard'})"}
```

**Fast Mode** - Quick wins beyond axe-core:
```
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'fast'})"}
```

**Comprehensive Mode** - Full CSS state validation (may be slower):
```
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'comprehensive'})"}
```

### 2. Configure Audit Level

Run different WCAG conformance levels:

```javascript
// WCAG 2.1 Level A (minimum)
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({level: 'a'})"}

// WCAG 2.1 Level AA (recommended)
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({level: 'aa'})"}

// WCAG 2.1 Level AAA (enhanced)
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({level: 'aaa'})"}
```

### 3. Audit Specific Elements

```javascript
// Audit only a specific section
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({selector: '#main-content'})"}
```

### 4. Use Basic Audit (Fallback Mode)

```javascript
// Skip axe-core and use basic audit
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({useBasic: true})"}
```

### 5. Comprehensive Mode Options

```javascript
// Configure breakpoints to test
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'comprehensive', breakpoints: [320, 768, 1024, 1440]})"}

// Configure color schemes to test
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'comprehensive', colorSchemes: ['light', 'dark']})"}

// Combine with WCAG level
proxy {action: "exec", id: "dev", code: "__devtool.auditAccessibility({mode: 'comprehensive', level: 'aaa', breakpoints: [375, 768, 1920]})"}
```

### 6. Additional Diagnostics

```javascript
// Get tab order for keyboard navigation
proxy {action: "exec", id: "dev", code: "__devtool.getTabOrder()"}

// Take screenshot for reference
proxy {action: "exec", id: "dev", code: "__devtool.screenshot('a11y-audit')"}
```

## What Each Mode Checks

### Standard Mode (Default - axe-core)
Industry-standard comprehensive WCAG 2.1 testing including:

- **Perceivable**: Images, audio/video, color contrast, text alternatives
- **Operable**: Keyboard navigation, focus management, timing
- **Understandable**: Language, labels, error identification
- **Robust**: Valid HTML, ARIA usage, compatibility

**Performance**: Fast (~100-300ms for typical pages)
**Coverage**: 90+ WCAG rules

### Fast Mode (Quick Wins)
Additional checks beyond axe-core that run quickly:

- **Focus indicator visibility** - Checks if focusable elements have visible focus styles
- **Hidden on focus** - Detects elements that disappear when focused (display:none, visibility:hidden, opacity:0)
- **Missing focus indicators** - Elements that may lack visible focus indicators
- **Color scheme support** - Validates presence of prefers-color-scheme media queries

**Performance**: Very fast (~50-100ms)
**Coverage**: Focus management + color scheme detection

### Comprehensive Mode (CSS Analysis & Test Enumeration)
Intelligent CSS analysis that discovers testing requirements:

- **CSS rule indexing** - Builds reverse index of classes/selectors → media queries
- **Cross-origin detection** - Flags stylesheets that cannot be accessed
- **Media query discovery** - Automatically finds all breakpoints and color schemes in CSS
- **Element categorization** - Tracks which media queries affect each element (walks inheritance tree)
- **State-specific contrast** - Tests color contrast in default and focus states
- **Focus outline contrast** - Validates focus indicators meet 3:1 minimum contrast
- **Untested state warnings** - Reports elements affected by inactive media queries
- **Test recommendations** - Enumerates exact viewport sizes and color schemes to test

**Performance**: Slower (~500-2000ms depending on CSS complexity)
**Coverage**: Current state testing + comprehensive analysis of what else needs testing

### Basic Mode (Fallback)
Essential checks when axe-core is unavailable:

- **Images without alt text** - Screen readers can't describe the image
- **Form inputs without labels** - Users can't understand what to enter (checks explicit and implicit labels)
- **Buttons without accessible names** - Users don't know what the button does
- **Empty links** - Links with no text content or aria-label
- **Links without href** - May cause navigation issues

**Performance**: Very fast (~10-50ms)
**Coverage**: Critical issues only

## Interpreting Results

### Standard Mode Results (axe-core)

When using standard mode (default), the audit returns:

- `mode`: "axe-core" (indicates full audit was used)
- `version`: Axe-core version (e.g., "4.8.3")
- `level`: WCAG level tested ("a", "aa", or "aaa")
- `count`: Total number of violations found
- `errors`: Number of critical/serious issues
- `warnings`: Number of moderate/minor issues
- `summary`: Breakdown by impact level (critical, serious, moderate, minor)
- `violations`: Full axe-core violation details
- `passes`: Rules that passed
- `incomplete`: Rules that need manual review
- `inapplicable`: Rules that don't apply to this page

For each issue in `issues` array:
- `type`: Axe rule ID (e.g., "color-contrast", "label")
- `severity`: "error" (critical/serious) or "warning" (moderate/minor)
- `impact`: "critical", "serious", "moderate", or "minor"
- `message`: Human-readable description
- `helpUrl`: Link to detailed documentation
- `selector`: CSS selector to locate the element
- `html`: HTML snippet of the violating element
- `wcagTags`: Relevant WCAG success criteria

### Fast Mode Results

When using fast mode, the audit returns:

- `mode`: "fast"
- `count`: Total number of issues found
- `errors`: Number of critical issues
- `warnings`: Number of non-critical issues
- `categories`: Breakdown by category (focus-management, color-scheme)

For each issue:
- `type`: Issue type (e.g., "hidden-on-focus", "no-focus-indicator", "no-color-scheme")
- `severity`: "error" or "warning"
- `selector`: CSS selector to locate the element (when applicable)
- `message`: Description of the problem
- `category`: Category of the issue

### Comprehensive Mode Results

When using comprehensive mode, the audit returns:

- `mode`: "comprehensive"
- `level`: WCAG level tested ("a", "aa", or "aaa")
- `count`: Total number of issues found
- `errors`: Number of critical contrast violations
- `warnings`: Number of potential issues
- `info`: Number of informational items (untested states)
- `categories`: Breakdown by category (contrast, focus-indicator, responsive, color-scheme)
- `summary`: Current test context (testedStates, currentBreakpoint, currentColorScheme)

For each issue:
- `type`: Issue type (e.g., "color-contrast-state", "focus-outline-contrast", "untested-breakpoint")
- `severity`: "error", "warning", or "info"
- `selector`: CSS selector to locate the element
- `state`: Which state triggered the issue (e.g., "default", "focus")
- `message`: Description of the problem
- `contrast`: Actual contrast ratio (for contrast issues)
- `required`: Required contrast ratio (for contrast issues)
- `foreground`: Foreground color (for contrast issues)
- `background`: Background color (for contrast issues)
- `category`: Category of the issue

### Basic Mode Results (Fallback)

When using basic mode (fallback), the audit returns:

- `mode`: "basic"
- `fallback`: true (if axe-core failed to load)
- `fallbackReason`: Error message explaining why
- `count`: Total number of issues
- `errors`: Number of critical issues
- `warnings`: Number of non-critical issues

For each issue:
- `type`: Issue type (e.g., "missing-label")
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
