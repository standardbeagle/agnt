---
sidebar_position: 0
---

# Use Cases Overview

agnt shines in scenarios where traditional debugging tools fall short. This guide covers the most impactful ways to use agnt in your development workflow.

## Why agnt Changes the Game

Traditional AI coding assistants are blind to what happens in the browser. When you say "the button looks wrong," you're spending tokens describing what the AI could just *see*. agnt fixes this by giving your AI agent direct access to:

- **Visual state** - Screenshots, DOM structure, computed styles
- **Runtime errors** - JavaScript exceptions with full stack traces
- **Network traffic** - Every HTTP request and response
- **User interactions** - What was clicked, typed, scrolled
- **Performance data** - Load times, paint metrics, resource timing

This enables entirely new workflows that weren't possible before.

---

## Hard-to-Diagnose Layout Issues

Some layout bugs are notoriously difficult to describe: "the thing is slightly off," "it looks weird on my screen," "sometimes it overlaps." agnt gives your AI the tools to investigate these systematically.

### The Problem

Layout issues often involve:
- Inherited styles from deep in the cascade
- Stacking context conflicts (z-index battles)
- Overflow and clipping from parent containers
- Font rendering differences
- Flexbox/Grid edge cases

### The agnt Approach

```json
// 1. Inspect the problematic element
proxy {action: "exec", id: "app", code: "window.__devtool.inspect('.problem-element')"}
→ Returns: position, box model, computed styles, stacking context, ancestors

// 2. Check for overflow issues
proxy {action: "exec", id: "app", code: "window.__devtool.findOverflows()"}
→ Returns: all elements with content overflowing their bounds

// 3. Analyze stacking contexts
proxy {action: "exec", id: "app", code: "window.__devtool.findStackingContexts()"}
→ Returns: z-index hierarchy and stacking context tree

// 4. Check text fragility (truncation, overflow)
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}
→ Returns: text elements at risk of clipping or overflow
```

### Real Example: "Why is my modal behind the header?"

Instead of describing the problem, let the AI see it:

```json
// Capture the visual state
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('modal-issue')"}

// Analyze stacking
proxy {action: "exec", id: "app", code: `
  const modal = document.querySelector('.modal');
  const header = document.querySelector('header');
  return {
    modal: window.__devtool.getStackingContext(modal),
    header: window.__devtool.getStackingContext(header)
  };
`}
→ AI sees: modal has z-index: 100 but header creates new stacking context with z-index: 1000
```

---

## Responsive Design Testing

Testing responsive layouts traditionally requires manually resizing the browser and describing what you see. agnt automates the diagnosis.

### Viewport Testing Workflow

```json
// 1. Check what might break at different sizes
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
→ Returns: elements with fixed widths, absolute positioning, problematic overflow

// 2. Find elements that might not fit on mobile
proxy {action: "exec", id: "app", code: `
  window.__devtool.findElements({
    selector: '*',
    filter: el => el.offsetWidth > 375  // iPhone SE width
  })
`}

// 3. Check touch target sizes (mobile usability)
proxy {action: "exec", id: "app", code: `
  [...document.querySelectorAll('button, a, input')].filter(el => {
    const rect = el.getBoundingClientRect();
    return rect.width < 44 || rect.height < 44;  // Apple HIG minimum
  }).map(el => ({
    selector: window.__devtool.getSelector(el),
    size: { width: el.offsetWidth, height: el.offsetHeight }
  }))
`}
```

### Testing on Real Devices

Use tunnels to test on actual phones with full instrumentation:

```json
// Start proxy and tunnel
proxy {action: "start", id: "app", target_url: "http://localhost:3000", bind_address: "0.0.0.0"}
tunnel {action: "start", id: "app", provider: "cloudflare", local_port: 45849, proxy_id: "app"}
→ {public_url: "https://random.trycloudflare.com"}

// After testing on phone, check for mobile-specific errors
proxylog {proxy_id: "app", types: ["error"]}
```

See the [Mobile Testing Guide](/use-cases/mobile-testing) for the complete workflow.

---

## Internationalization (i18n) Testing

Text length varies dramatically between languages. German is ~30% longer than English; Chinese may be shorter but taller. agnt helps catch i18n layout issues before they reach production.

### Text Expansion Testing

```json
// Find text that's already at risk of overflow
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}

// Simulate longer text (pseudo-localization)
proxy {action: "exec", id: "app", code: `
  document.querySelectorAll('button, label, h1, h2, h3, p').forEach(el => {
    if (el.childNodes.length === 1 && el.childNodes[0].nodeType === 3) {
      el.textContent = el.textContent + ' [xxxxx]';  // Add ~30% length
    }
  });
  return 'Text expanded - check for overflow';
`}

// Take screenshot of expanded state
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('i18n-expanded')"}

// Check what broke
proxy {action: "exec", id: "app", code: "window.__devtool.findOverflows()"}
```

### RTL (Right-to-Left) Testing

```json
// Switch to RTL mode
proxy {action: "exec", id: "app", code: `
  document.documentElement.dir = 'rtl';
  document.documentElement.lang = 'ar';
  return 'Switched to RTL';
`}

// Screenshot the RTL layout
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('rtl-layout')"}

// Check for layout issues
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
```

### Mobile Typography Without Word Breaks

Many clients prefer clean text layouts without forced word breaks (`word-break: break-word`). The text fragility audit helps ensure your mobile typography supports a realistic vocabulary without requiring breaks:

```json
// Check if text elements can handle their longest words at mobile sizes
proxy {action: "exec", id: "app", code: `
  const fragility = window.__devtool.checkTextFragility();

  // Find elements that will need word breaks on mobile
  const needsBreaks = fragility.issues.filter(el =>
    el.problematicBreakpoints.some(bp => bp.breakpoint <= 414) // Mobile widths
  );

  return needsBreaks.map(el => ({
    selector: el.selector,
    longestWord: el.longestWord.word,
    minWidthNeeded: el.longestWord.minWidthPx + 'px',
    breaksAt: el.problematicBreakpoints
      .filter(bp => bp.breakpoint <= 414)
      .map(bp => bp.breakpoint + 'px')
  }));
`}
```

**What this tells you:**
- The **longest word** in each text element (e.g., "internationalization")
- The **minimum pixel width** needed to display that word without breaks
- Which **mobile breakpoints** (320px, 375px, 414px) would require word breaks

**Design implications:**
- If an element's longest word needs 185px but only gets 140px on iPhone SE (320px), you need:
  - Smaller font size at that breakpoint
  - Wider container
  - Or accept word breaks for that element
- Elements with only short words (< 15 chars) typically fit fine on mobile

---

## Writing Better Frontend Tests

agnt helps you write more targeted tests by showing exactly what to assert.

### Discovering What to Test

```json
// See the actual structure of a component
proxy {action: "exec", id: "app", code: `
  window.__devtool.captureDOM('.user-profile', {
    includeStyles: true,
    includeAccessibility: true
  })
`}
→ Returns: HTML structure, ARIA attributes, visible text, computed styles

// Get accessibility info for assertion
proxy {action: "exec", id: "app", code: `
  window.__devtool.getA11yInfo('.submit-button')
`}
→ Returns: role, name, description, keyboard focusable, etc.
```

### Generating Test Selectors

```json
// Get a stable selector for an element
proxy {action: "exec", id: "app", code: `
  window.__devtool.selectElement()  // User clicks the element
`}
→ Returns: {selector: "[data-testid='submit-btn']", xpath: "//button[@type='submit']"}
```

### Capturing Expected State

```json
// Capture network requests for mocking
proxylog {proxy_id: "app", types: ["http"], url_pattern: "/api"}
→ Use response bodies as mock data in tests

// Capture DOM state for snapshot testing
proxy {action: "exec", id: "app", code: `
  window.__devtool.captureDOM('.results-list')
`}
```

---

## Creating Documentation with Screenshots

agnt makes it easy to capture annotated screenshots for documentation, bug reports, and PRs.

### Capturing UI States

```json
// Screenshot the current state
proxy {action: "exec", id: "app", code: "window.__devtool.screenshot('feature-overview')"}

// Highlight a specific element before capturing
proxy {action: "exec", id: "app", code: `
  window.__devtool.highlight('.new-feature');
  setTimeout(() => window.__devtool.screenshot('new-feature-highlighted'), 100);
`}

// Capture multiple states
proxy {action: "exec", id: "app", code: `
  // Empty state
  window.__devtool.screenshot('dashboard-empty');
`}
// ... add data ...
proxy {action: "exec", id: "app", code: `
  // Populated state
  window.__devtool.screenshot('dashboard-populated');
`}
```

### Annotating with Sketch Mode

Use sketch mode to add annotations directly on screenshots:

```json
// Open sketch mode from indicator or programmatically
proxy {action: "exec", id: "app", code: "window.__devtool.sketch.open()"}

// User draws annotations...
// Then save the sketch
proxy {action: "exec", id: "app", code: "window.__devtool.sketch.save()"}
→ Sketch saved to proxylog with PNG image
```

### Documenting Bugs

```json
// Capture complete context for a bug report
proxy {action: "exec", id: "app", code: `
  return {
    screenshot: window.__devtool.screenshot('bug-state'),
    errors: window.__devtool_errors || [],
    url: window.location.href,
    viewport: { width: window.innerWidth, height: window.innerHeight },
    userAgent: navigator.userAgent,
    localStorage: Object.keys(localStorage),
    lastClick: window.__devtool.interactions.getLastClickContext()
  };
`}
```

---

## Design Iteration Flow

agnt enables a new design workflow where you can iterate on UI directly with AI assistance.

### The Design Mode Workflow

1. **Select an element** - Click any element you want to redesign
2. **AI generates alternatives** - Multiple design variations appear
3. **Preview and compare** - Navigate through alternatives live
4. **Refine with chat** - Describe adjustments in natural language
5. **Apply the winner** - Copy the final HTML/CSS

```json
// Start design mode
proxy {action: "exec", id: "app", code: "window.__devtool.design.start()"}

// User clicks element to select it
// → AI receives design_state event with element context

// AI generates alternatives
proxy {action: "exec", id: "app", code: `
  window.__devtool_design.addAlternative('<button class="btn-primary">...</button>');
  window.__devtool_design.addAlternative('<button class="btn-outline">...</button>');
`}

// User navigates alternatives and provides feedback
// → AI receives design_chat events

// Get the final chosen design
proxy {action: "exec", id: "app", code: "window.__devtool.design.getState()"}
```

### Sketch-to-Code Workflow

1. **Sketch a wireframe** - Draw directly on the page
2. **AI interprets the sketch** - Understands layout intent
3. **Generate implementation** - Creates matching HTML/CSS
4. **Iterate** - Refine with more sketches or chat

```json
// Open sketch mode
proxy {action: "exec", id: "app", code: "window.__devtool.sketch.open()"}

// User draws wireframe...
// Save and send to AI
proxy {action: "exec", id: "app", code: "window.__devtool.sketch.save()"}
→ Sketch with image sent to AI for interpretation
```

---

## Chaos Testing & Resilience

Most frontend bugs happen under conditions developers never test: slow networks, flaky APIs, race conditions. agnt's built-in chaos engineering makes it easy to simulate these.

### Testing Network Conditions

```json
// Simulate 3G mobile network
proxy {action: "chaos", id: "app", preset: "mobile-3g"}

// Now use the app - do loading states appear? Is the UI responsive during loads?

// Check what errors occurred
proxylog {proxy_id: "app", types: ["error"]}
```

### Testing API Failures

```json
// Random 500 errors, timeouts, variable latency
proxy {action: "chaos", id: "app", preset: "flaky-api"}

// Verify: Do error messages appear? Can users retry? Is state consistent?
```

### Exposing Race Conditions

```json
// Responses arrive in random order
proxy {action: "chaos", id: "app", preset: "race-condition"}

// Type in a search box rapidly - does the UI show stale results?
// Click a button twice - does the handler guard against double-submit?
```

### Testing Token Expiry

```json
// Return 401 for all API calls
proxy {
  action: "chaos",
  id: "app",
  rules: [{
    "type": "http_error",
    "url_pattern": "/api/.*",
    "error_codes": [401],
    "probability": 1.0
  }]
}

// Does the app redirect to login? Is state preserved for after re-auth?
```

### Custom Chaos Rules

```json
// Slow only the checkout API
proxy {
  action: "chaos",
  id: "app",
  rules: [
    {
      "id": "slow-checkout",
      "type": "latency",
      "url_pattern": "/api/checkout",
      "min_latency_ms": 3000,
      "max_latency_ms": 8000,
      "probability": 1.0
    }
  ]
}
```

See the [Chaos Engineering Guide](/features/chaos-engineering) for complete documentation.

---

## Quick Reference: Use Case → Tools

| Use Case | Primary Tools |
|----------|---------------|
| Layout debugging | `inspect`, `findOverflows`, `findStackingContexts` |
| Responsive testing | `checkResponsiveRisk`, `checkTextFragility`, tunnels |
| i18n testing | `checkTextFragility`, `findOverflows`, manual text expansion |
| Writing tests | `captureDOM`, `getA11yInfo`, `selectElement`, `proxylog` |
| Documentation | `screenshot`, `highlight`, sketch mode |
| Design iteration | design mode, sketch mode, `addAlternative` |
| Chaos testing | `proxy chaos`, presets: `flaky-api`, `mobile-3g`, `race-condition` |
| Error debugging | `proxylog {types: ["error"]}`, `captureState` |
| Performance | `proxylog {types: ["performance"]}`, resource timing |
| Accessibility | `auditAccessibility`, `getA11yInfo`, `getContrast` |

---

## Detailed Guides

For in-depth coverage of specific workflows:

- [Chaos Engineering](/features/chaos-engineering) - Network failures, API errors, race conditions
- [Debugging Web Apps](/use-cases/debugging-web-apps) - Complete debugging workflow
- [Frontend Error Tracking](/use-cases/frontend-error-tracking) - Capturing and analyzing JS errors
- [Performance Monitoring](/use-cases/performance-monitoring) - Load times and resource optimization
- [Accessibility Auditing](/use-cases/accessibility-auditing) - WCAG compliance checking
- [Mobile Testing](/use-cases/mobile-testing) - Real device testing with tunnels
- [Automated Testing](/use-cases/automated-testing) - Integration with test frameworks
- [CI/CD Integration](/use-cases/ci-cd-integration) - Automation pipelines
