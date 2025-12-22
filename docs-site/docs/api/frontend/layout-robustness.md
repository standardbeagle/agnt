---
sidebar_position: 11
---

# Layout Robustness & Fragility Detection

Functions for detecting layout fragility, text overflow issues, responsive risks, and performance problems.

## checkTextFragility

Detect text overflow, truncation, and layout shift risks. Analyzes all text elements on the page for issues that may cause content loss or layout problems at different viewport sizes.

```javascript
window.__devtool.checkTextFragility()
```

**Returns:**
```javascript
{
  issues: [
    {
      selector: ".card-title",
      text: "This is the visible text content...",
      longestWord: {
        word: "internationalization",
        length: 20,
        minWidthPx: 185
      },
      issues: [
        {
          type: "horizontal-overflow",
          severity: "error",
          message: "Text overflows container horizontally",
          details: {
            scrollWidth: 250,
            clientWidth: 150,
            overflow: "100px"
          }
        }
      ],
      problematicBreakpoints: [
        {
          breakpoint: 320,
          estimatedWidth: 120,
          requiredWidth: 185,
          deficit: 65
        },
        {
          breakpoint: 375,
          estimatedWidth: 140,
          requiredWidth: 185,
          deficit: 45
        }
      ]
    }
  ],
  summary: {
    total: 4,
    errors: 2,
    warnings: 2,
    elementsAnalyzed: 156
  },
  breakpointsTested: [320, 375, 414, 768, 1024, 1280, 1440, 1920]
}
```

**Issue Types:**
| Type | Severity | Description |
|------|----------|-------------|
| `truncated` | warning | Text is truncated with ellipsis |
| `horizontal-overflow` | error | Text overflows container horizontally |
| `vertical-overflow` | error | Text overflows container vertically |
| `multi-line-auto-height` | warning | Multi-line text with auto height - may cause layout shift |
| `long-word-no-break` | warning | Long word (>15 chars) without word-break may overflow |

**Key Features:**
- Reports the **longest word** in each element and its minimum pixel width
- Predicts which **breakpoints** will cause overflow issues
- Detects both horizontal and vertical overflow
- Identifies layout shift risks from dynamic content

**Example:**
```javascript
const fragility = window.__devtool.checkTextFragility()

if (fragility.summary.errors > 0) {
  console.log('Critical text issues found!')
  fragility.issues
    .filter(i => i.issues.some(issue => issue.severity === 'error'))
    .forEach(i => {
      console.log(`${i.selector}: ${i.text}`)
      console.log(`  Longest word: "${i.longestWord.word}" needs ${i.longestWord.minWidthPx}px`)
      if (i.problematicBreakpoints.length > 0) {
        console.log(`  Breaks at: ${i.problematicBreakpoints.map(b => b.breakpoint + 'px').join(', ')}`)
      }
    })
}
```

---

## checkResponsiveRisk

Detect elements at risk of layout issues at different viewport sizes. Checks for fixed dimensions, small touch targets, horizontal scroll issues, and more.

```javascript
window.__devtool.checkResponsiveRisk()
```

**Returns:**
```javascript
{
  issues: [
    {
      selector: ".product-card",
      tagName: "div",
      issues: [
        {
          type: "fixed-width",
          severity: "warning",
          message: "Fixed width (400px) may cause horizontal scroll on mobile",
          details: {
            width: "400px",
            breakpointsAffected: [320, 375]
          }
        }
      ]
    },
    {
      selector: "button.small-btn",
      tagName: "button",
      issues: [
        {
          type: "small-touch-target",
          severity: "warning",
          message: "Touch target smaller than 44x44px minimum",
          details: {
            width: "32px",
            height: "28px",
            recommended: "44x44px minimum"
          }
        }
      ]
    }
  ],
  summary: {
    total: 5,
    errors: 1,
    warnings: 4,
    elementsAnalyzed: 245
  },
  currentViewport: {
    width: 1440,
    height: 900
  },
  breakpointsTested: [320, 375, 414, 768, 1024, 1280, 1440, 1920]
}
```

**Issue Types:**
| Type | Severity | Description |
|------|----------|-------------|
| `fixed-width` | warning/error | Fixed pixel width may cause horizontal scroll |
| `min-width-too-large` | warning/error | min-width larger than mobile viewports |
| `exceeds-viewport` | error | Element currently exceeds viewport width |
| `small-touch-target` | warning | Interactive element smaller than 44x44px (Apple HIG) |
| `unintended-horizontal-scroll` | error | Element causes horizontal scroll without overflow-x setting |
| `positioned-offscreen-right` | warning | Absolute/fixed element extends past right edge |
| `large-fixed-element` | warning | Large fixed element may obscure content on mobile |
| `small-font` | warning | Font size below 12px may be hard to read |
| `extreme-font-size` | warning | Very large or small font may cause issues |
| `wide-table` | error | Table wider than viewport |
| `table-not-scrollable` | warning | Wide table without horizontal scroll wrapper |

**Example:**
```javascript
const risks = window.__devtool.checkResponsiveRisk()

// Check for critical issues
if (risks.summary.errors > 0) {
  console.log('Critical responsive issues:')
  risks.issues.forEach(el => {
    el.issues
      .filter(i => i.severity === 'error')
      .forEach(issue => {
        console.log(`${el.selector}: ${issue.message}`)
      })
  })
}

// Find small touch targets
const smallTargets = risks.issues.filter(el =>
  el.issues.some(i => i.type === 'small-touch-target')
)
console.log(`${smallTargets.length} elements have small touch targets`)
```

---

## capturePerformanceMetrics

Capture comprehensive performance metrics including CLS, long tasks, and resource timing.

```javascript
window.__devtool.capturePerformanceMetrics()
```

**Returns:**
```javascript
{
  cls: {
    score: 0.15,
    rating: "needs-improvement",  // "good" | "needs-improvement" | "poor"
    shifts: [
      {
        value: 0.08,
        startTime: 1523,
        sources: [".ad-banner", ".hero-image"]
      }
    ]
  },
  longTasks: [
    { duration: 85, startTime: 234, name: "self" }
  ],
  resources: {
    byType: {
      script: { count: 12, totalSize: 450000, totalDuration: 1200 },
      img: { count: 25, totalSize: 1200000, totalDuration: 2500 },
      css: { count: 5, totalSize: 85000, totalDuration: 300 }
    },
    largest: [
      { url: "/images/hero.jpg", type: "img", size: 450000, duration: 800 }
    ],
    slowest: [
      { url: "/api/data", type: "fetch", size: 5000, duration: 1200 }
    ],
    renderBlocking: [
      { url: "/styles.css", type: "css", size: 45000, duration: 200 }
    ]
  },
  paint: {
    firstPaint: 450,
    firstContentfulPaint: 620
  },
  totals: {
    pageWeight: 1855000,
    resourceCount: 46,
    loadTime: 2800,
    domContentLoaded: 1200
  },
  timestamp: 1699999999999
}
```

**CLS Rating Thresholds:**
- `good`: < 0.1
- `needs-improvement`: 0.1 - 0.25
- `poor`: > 0.25

**Example:**
```javascript
const perf = window.__devtool.capturePerformanceMetrics()

console.log(`Page weight: ${(perf.totals.pageWeight / 1024 / 1024).toFixed(2)} MB`)
console.log(`Load time: ${perf.totals.loadTime}ms`)

if (perf.cls && perf.cls.rating !== 'good') {
  console.log('CLS issues:', perf.cls.shifts)
}
```

---

## E2E Testing Examples

The agnt audit system is tested using Playwright. Here are examples from the test suite:

### Running Audits via the Indicator UI

```typescript
import { test, expect, Page } from '@playwright/test';

// Wait for the __devtool API to be available
async function waitForDevtool(page: Page): Promise<void> {
  await page.waitForFunction(
    () => {
      return (
        typeof window.__devtool !== 'undefined' &&
        window.__devtool !== null &&
        typeof window.__devtool.indicator !== 'undefined'
      );
    },
    { timeout: 15000 }
  );
}

// Run an audit and check results
test('Text Fragility audit detects overflow issues', async ({ page }) => {
  await page.goto('http://localhost:12345/layout-issues.html');
  await waitForDevtool(page);

  // Open the indicator panel
  await page.evaluate(() => {
    window.__devtool.indicator.togglePanel(true);
  });

  // Click the Audit dropdown
  const auditBtn = page.locator('#__devtool-indicator button').filter({
    hasText: 'Audit',
  }).first();
  await auditBtn.click({ force: true });

  // Select Text Fragility audit
  await page.waitForSelector('#__devtool-audit-menu');
  const menuItem = page.locator('#__devtool-audit-menu button').filter({
    hasText: 'Text Fragility',
  });
  await menuItem.click({ force: true });

  // Wait for audit to complete and check results
  await page.waitForTimeout(2000);
  const result = await page.evaluate(() => {
    return window.__devtool.checkTextFragility();
  });

  expect(result.summary.elementsAnalyzed).toBeGreaterThan(0);
  expect(result.breakpointsTested).toContain(320);
});
```

### Test Fixtures for Layout Issues

Create test pages with known issues to validate audits:

```html
<!-- layout-issues.html -->
<!DOCTYPE html>
<html>
<head>
  <style>
    /* Fixed width that breaks on mobile */
    .fixed-width-card {
      width: 500px;
      border: 1px solid #ccc;
    }

    /* Text that will overflow */
    .overflow-text {
      width: 100px;
      white-space: nowrap;
      overflow: visible;
    }

    /* Small touch target */
    .tiny-button {
      width: 20px;
      height: 20px;
      padding: 0;
    }

    /* Long word without word-break */
    .long-word-container {
      width: 150px;
    }
  </style>
</head>
<body>
  <div class="fixed-width-card">
    <p>This card has a fixed width that will cause issues on mobile.</p>
  </div>

  <div class="overflow-text">
    This text will overflow its container because of nowrap.
  </div>

  <button class="tiny-button">X</button>

  <div class="long-word-container">
    <p>Contains supercalifragilisticexpialidocious which is very long.</p>
  </div>
</body>
</html>
```

### Smoke Test for All Audits

```typescript
test('All audits complete without errors', async ({ page }) => {
  await page.goto('http://localhost:12345/clean-baseline.html');
  await waitForDevtool(page);

  const audits = [
    'Full Page Audit',
    'Accessibility',
    'Security',
    'SEO / Meta',
    'Layout Issues',
    'Text Fragility',
    'Responsive Risk',
    'Last Click Context',
    'Recent DOM Changes',
    'Browser State',
    'Network/Resources',
    'DOM Complexity',
    'CSS Quality',
  ];

  for (const label of audits) {
    const result = await runAudit(page, label);
    expect.soft(result.success, `${label} should appear in results`).toBe(true);
    expect.soft(result.hasError, `${label} should not error`).toBe(false);
  }
});
```

---

## Common Patterns

### Pre-Deploy Quality Check

```javascript
async function preDeployCheck() {
  const textFragility = window.__devtool.checkTextFragility()
  const responsiveRisk = window.__devtool.checkResponsiveRisk()

  const issues = []

  // No text overflow errors
  if (textFragility.summary.errors > 0) {
    issues.push(`${textFragility.summary.errors} text overflow errors`)
  }

  // No viewport overflow
  const viewportOverflows = responsiveRisk.issues.filter(el =>
    el.issues.some(i => i.type === 'exceeds-viewport')
  )
  if (viewportOverflows.length > 0) {
    issues.push(`${viewportOverflows.length} elements overflow viewport`)
  }

  // No critical touch target issues
  const tinyTargets = responsiveRisk.issues.filter(el =>
    el.issues.some(i => i.type === 'small-touch-target')
  )
  if (tinyTargets.length > 5) {
    issues.push(`${tinyTargets.length} small touch targets`)
  }

  return {
    pass: issues.length === 0,
    issues: issues,
    textFragility: textFragility.summary,
    responsiveRisk: responsiveRisk.summary
  }
}
```

### Mobile Compatibility Check

```javascript
function checkMobileReady() {
  const risks = window.__devtool.checkResponsiveRisk()

  // Find issues that affect mobile breakpoints (320-414px)
  const mobileIssues = risks.issues.filter(el =>
    el.issues.some(i =>
      i.details?.breakpointsAffected?.some(bp => bp <= 414)
    )
  )

  console.log(`Mobile issues: ${mobileIssues.length}`)
  mobileIssues.forEach(el => {
    console.log(`  ${el.selector}:`)
    el.issues.forEach(i => console.log(`    - ${i.message}`))
  })

  return {
    mobileReady: mobileIssues.length === 0,
    issues: mobileIssues
  }
}
```

### Content Loss Detection

```javascript
function findContentLoss() {
  const fragility = window.__devtool.checkTextFragility()

  // Find elements losing content due to truncation or overflow
  const contentLoss = fragility.issues.filter(el =>
    el.issues.some(i =>
      i.type === 'truncated' ||
      i.type === 'horizontal-overflow' ||
      i.type === 'vertical-overflow'
    )
  )

  contentLoss.forEach(el => {
    console.log(`Content lost at: ${el.selector}`)
    console.log(`  Text: "${el.text}"`)
    console.log(`  Longest word: "${el.longestWord.word}" (${el.longestWord.length} chars)`)
    console.log(`  Min width needed: ${el.longestWord.minWidthPx}px`)
    if (el.problematicBreakpoints.length > 0) {
      console.log(`  Problematic at: ${el.problematicBreakpoints.map(b => b.breakpoint + 'px').join(', ')}`)
    }
  })

  return contentLoss
}
```

---

## Performance Notes

- `checkTextFragility` and `checkResponsiveRisk` scan all elements - may be slow on large pages
- Both functions are synchronous and block the main thread
- Use for diagnostic purposes, not in production hot paths
- Consider running audits after page load completes
- The indicator UI runs audits asynchronously and displays results as attachment chips

## See Also

- [Quality Auditing](/api/frontend/quality-auditing) - Frame rate, memory, and Core Web Vitals
- [Layout Diagnostics](/api/frontend/layout-diagnostics) - Basic overflow and stacking detection
- [Accessibility](/api/frontend/accessibility) - Built-in a11y checks
- [Performance Monitoring](/use-cases/performance-monitoring) - Performance use cases
