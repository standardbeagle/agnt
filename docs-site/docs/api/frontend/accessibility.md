---
sidebar_position: 9
---

# Accessibility

Functions for accessibility inspection and auditing.

## getA11yInfo

Get accessibility attributes for an element.

```javascript
window.__devtool.getA11yInfo(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  role: "button",
  ariaLabel: "Close dialog",
  ariaLabelledBy: null,
  ariaDescribedBy: "dialog-description",
  ariaExpanded: "false",
  ariaHidden: null,
  ariaLive: null,
  ariaDisabled: null,
  tabIndex: 0,
  focusable: true,
  accessibleName: "Close dialog",
  accessibleDescription: "Closes the current dialog and discards changes"
}
```

**Example:**
```javascript
window.__devtool.getA11yInfo('#submit-button')
→ {role: "button", ariaLabel: "Submit form", focusable: true, ...}
```

## getContrast

Check color contrast ratio against WCAG guidelines.

```javascript
window.__devtool.getContrast(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  foreground: "#ffffff",
  background: "#2196f3",
  ratio: 4.52,
  passes: {
    AA_normal: true,   // >= 4.5:1
    AA_large: true,    // >= 3:1
    AAA_normal: false, // >= 7:1
    AAA_large: true    // >= 4.5:1
  },
  recommendation: null
}
```

When failing:
```javascript
{
  foreground: "#666666",
  background: "#888888",
  ratio: 1.54,
  passes: {
    AA_normal: false,
    AA_large: false,
    AAA_normal: false,
    AAA_large: false
  },
  recommendation: "Increase contrast. Try #333333 for foreground or #cccccc for background."
}
```

**Limitations:**
- Cannot compute for gradient backgrounds
- Cannot compute for background images
- Returns error for these cases

**Example:**
```javascript
const contrast = window.__devtool.getContrast('.muted-text')
if (!contrast.passes.AA_normal) {
  console.log(`Contrast ratio ${contrast.ratio}:1 is too low`)
  console.log(contrast.recommendation)
}
```

## getTabOrder

Get the document's tab order.

```javascript
window.__devtool.getTabOrder(container)
```

**Parameters:**
- `container` (string, optional): CSS selector for container (default: document)

**Returns:**
```javascript
[
  {
    selector: "a.skip-link",
    element: {tag: "a", classes: ["skip-link"]},
    tabIndex: 0,
    order: 1,
    focusable: true,
    tabbable: true
  },
  {
    selector: "input#search",
    element: {tag: "input", id: "search"},
    tabIndex: 0,
    order: 2,
    focusable: true,
    tabbable: true
  },
  {
    selector: "button#menu",
    element: {tag: "button", id: "menu"},
    tabIndex: 0,
    order: 3,
    focusable: true,
    tabbable: true
  },
  {
    selector: "button.priority",
    element: {tag: "button", classes: ["priority"]},
    tabIndex: 1,  // Positive tabindex - comes first!
    order: 0,
    focusable: true,
    tabbable: true,
    warning: "Positive tabindex may cause unexpected tab order"
  }
]
```

**Notes:**
- Elements with positive tabindex come first
- Then elements in DOM order with tabindex=0
- Elements with tabindex=-1 are focusable but not tabbable

**Example:**
```javascript
const tabOrder = window.__devtool.getTabOrder('form')
console.log('Tab order:')
tabOrder.forEach(el => console.log(`${el.order}: ${el.selector}`))
```

## getScreenReaderText

Get what a screen reader would announce.

```javascript
window.__devtool.getScreenReaderText(selector)
```

**Parameters:**
- `selector` (string): CSS selector

**Returns:**
```javascript
{
  text: "Submit form",
  sources: ["aria-label"],
  role: "button",
  state: "enabled",
  fullAnnouncement: "Submit form, button"
}
```

For complex elements:
```javascript
{
  text: "Search products",
  sources: ["aria-labelledby", "#search-label"],
  role: "searchbox",
  state: "enabled, required",
  fullAnnouncement: "Search products, required, searchbox"
}
```

**Text Source Priority:**
1. `aria-labelledby` (text content of referenced element)
2. `aria-label`
3. `<label>` element (for form controls)
4. `alt` attribute (for images)
5. `title` attribute
6. Text content (for buttons, links)

**Example:**
```javascript
const sr = window.__devtool.getScreenReaderText('button.icon-only')
if (!sr.text) {
  console.log('Icon button has no accessible name!')
}
```

## auditAccessibility

Full page accessibility audit.

```javascript
window.__devtool.auditAccessibility()
```

**Parameters:** None

**Returns:**
```javascript
{
  score: 85,  // 0-100
  errors: [
    {
      type: "missing-alt",
      severity: "error",
      selector: "img.hero-image",
      element: {tag: "img", classes: ["hero-image"]},
      message: "Image missing alt text",
      wcag: "1.1.1",
      fix: "Add alt attribute describing the image content"
    },
    {
      type: "missing-label",
      severity: "error",
      selector: "input#email",
      element: {tag: "input", id: "email"},
      message: "Form input has no associated label",
      wcag: "1.3.1",
      fix: "Add <label for='email'> or aria-label"
    }
  ],
  warnings: [
    {
      type: "low-contrast",
      severity: "warning",
      selector: ".muted-text",
      element: {tag: "p", classes: ["muted-text"]},
      message: "Contrast ratio 3.2:1 below AA threshold (4.5:1)",
      wcag: "1.4.3",
      fix: "Use darker text color or lighter background"
    },
    {
      type: "positive-tabindex",
      severity: "warning",
      selector: "button.priority",
      message: "Positive tabindex disrupts natural tab order",
      fix: "Use tabindex='0' and DOM order instead"
    }
  ],
  passes: 42,
  total: 50,
  summary: {
    images: {checked: 10, issues: 2},
    forms: {checked: 5, issues: 1},
    links: {checked: 20, issues: 0},
    headings: {checked: 8, issues: 0},
    contrast: {checked: 7, issues: 1}
  }
}
```

**Checks Performed:**
- Missing alt text on images
- Unlabeled form controls
- Empty links and buttons
- Missing document language
- Color contrast issues
- Positive tabindex values
- Missing heading hierarchy
- Missing landmark regions
- Duplicate IDs
- Empty headings

**Example:**
```javascript
const audit = window.__devtool.auditAccessibility()
console.log(`Accessibility score: ${audit.score}/100`)

if (audit.errors.length > 0) {
  console.log('\nErrors (must fix):')
  audit.errors.forEach(e => {
    console.log(`  ${e.selector}: ${e.message}`)
    console.log(`    Fix: ${e.fix}`)
  })
}

if (audit.warnings.length > 0) {
  console.log('\nWarnings:')
  audit.warnings.forEach(w => {
    console.log(`  ${w.selector}: ${w.message}`)
  })
}
```

## Common Patterns

### Check All Images

```javascript
document.querySelectorAll('img').forEach((img, i) => {
  const a11y = window.__devtool.getA11yInfo(`img:nth-of-type(${i + 1})`)
  if (!a11y.accessibleName) {
    console.log('Missing alt:', img.src)
    window.__devtool.highlight(`img:nth-of-type(${i + 1})`, {color: 'red'})
  }
})
```

### Contrast Check All Text

```javascript
document.querySelectorAll('p, span, a, button, label, h1, h2, h3, h4, h5, h6')
  .forEach((el, i) => {
    const contrast = window.__devtool.getContrast(el.tagName.toLowerCase())
    if (contrast.ratio && !contrast.passes.AA_normal) {
      console.log(`Low contrast (${contrast.ratio}):`, el.textContent.slice(0, 50))
    }
  })
```

### Form Accessibility Check

```javascript
function auditForm(formSelector) {
  const issues = []

  document.querySelectorAll(`${formSelector} input, ${formSelector} select, ${formSelector} textarea`)
    .forEach((input, i) => {
      const a11y = window.__devtool.getA11yInfo(`${formSelector} :nth-child(${i + 1})`)

      if (!a11y.accessibleName) {
        issues.push({
          element: input,
          issue: 'No accessible name',
          fix: 'Add label or aria-label'
        })
      }
    })

  return issues
}

const formIssues = auditForm('#signup-form')
console.log('Form accessibility issues:', formIssues)
```

### Keyboard Navigation Test

```javascript
const tabOrder = window.__devtool.getTabOrder()

// Check for positive tabindex (anti-pattern)
const positiveTabindex = tabOrder.filter(el => el.tabIndex > 0)
if (positiveTabindex.length > 0) {
  console.warn('Avoid positive tabindex:', positiveTabindex)
}

// Visualize tab order
tabOrder.forEach((el, i) => {
  window.__devtool.highlight(el.selector, {
    label: `Tab ${i + 1}`,
    duration: 0
  })
})
```

### Screen Reader Preview

```javascript
function previewScreenReader(selector) {
  const elements = document.querySelectorAll(selector)
  const announcements = []

  elements.forEach((el, i) => {
    const sr = window.__devtool.getScreenReaderText(`${selector}:nth-of-type(${i + 1})`)
    announcements.push(sr.fullAnnouncement)
  })

  return announcements.join('. ')
}

console.log('Nav will read as:', previewScreenReader('nav a'))
```

## WCAG Reference

| Code | Guideline |
|------|-----------|
| 1.1.1 | Non-text Content (images need alt) |
| 1.3.1 | Info and Relationships (labels) |
| 1.4.3 | Contrast (Minimum) 4.5:1 |
| 1.4.6 | Contrast (Enhanced) 7:1 |
| 2.1.1 | Keyboard accessible |
| 2.4.1 | Skip links |
| 2.4.4 | Link purpose |
| 2.4.6 | Headings and labels |

## See Also

- [Use Case: Accessibility Auditing](/use-cases/accessibility-auditing)
- [Element Inspection](/api/frontend/element-inspection)
