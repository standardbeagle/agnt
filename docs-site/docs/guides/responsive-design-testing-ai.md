---
title: "How to Debug Responsive Design with AI Coding Agents"
description: "Test responsive layouts across screen sizes with AI. Detect fixed-width elements, touch target issues, and viewport breakpoint problems automatically."
keywords: [responsive design testing, mobile layout debugging, viewport testing, AI coding agent, MCP server, touch targets]
sidebar_label: "Responsive Design Testing"
---

# How to Debug Responsive Design with AI Coding Agents

Responsive design testing is one of the most tedious parts of frontend development. A layout that looks perfect on your 1440px monitor can break completely at 768px -- a sidebar that was comfortably beside the main content now overflows horizontally, creating an invisible scrollbar that users on tablets will hit immediately. You would never notice this at your desk. The bug exists only at that specific viewport width, hiding between the breakpoints you thought to check.

The problem compounds with interactive elements. A 32x28px icon button works fine with a mouse cursor, but on a phone it is a frustrating tap target that users miss repeatedly. Text that fits in a card at desktop width gets silently clipped by `overflow: hidden` on narrower screens -- no ellipsis, no visual indicator, just missing content. These are not edge cases. They are the everyday reality of responsive interfaces.

Manual testing means dragging the browser window to different widths and squinting at elements that might be too small. You catch the obvious problems, but the subtle ones -- a fixed-width table that only overflows at exactly 375px, a long product name that gets clipped on one specific card -- slip through every time.

## The Traditional Approach

The standard workflow relies on the DevTools device toolbar. Toggle responsive mode, pick a device preset, visually inspect the page. Pick another device, do it again. For each breakpoint, scroll through looking for horizontal overflow, text truncation, overlapping elements, and undersized buttons.

This process has two fundamental problems. First, it does not scale. A page with 200 elements and 8 common breakpoints means 1,600 potential layout issues to visually verify. You will miss things. Second, it is subjective. "That button looks small enough to cause problems" is a judgment call that varies by person and by how tired you are at 5pm on a Friday.

## The agnt Approach

agnt gives your AI coding agent programmatic layout diagnostics through the `window.__devtool` API. Instead of visually inspecting each breakpoint, the AI runs functions that scan every element on the page and report which ones will break, at which viewport widths, and why.

The key diagnostic functions for responsive testing are:

- **`checkResponsiveRisk()`** -- scans all elements for fixed widths, small touch targets, viewport overflow, and other responsive hazards
- **`checkTextFragility()`** -- finds text that will overflow, get clipped, or truncate at different sizes
- **`findOverflows()`** -- finds elements currently overflowing their containers

The AI calls these through the proxy's JavaScript execution capability:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
```

No manual resizing. No visual guessing. The AI gets a structured report of every responsive issue on the page, with selectors, severity levels, and affected breakpoints.

## Detecting Fixed-Width Elements

Fixed pixel widths are the most common source of responsive layout failures. A `width: 400px` sidebar looks fine on desktop but causes horizontal scroll on any screen narrower than 400px. `checkResponsiveRisk()` finds every one of them:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
```

The response identifies each problematic element, the specific issue, and which breakpoints it affects:

```javascript
{
  issues: [
    {
      selector: ".dashboard-sidebar",
      tagName: "aside",
      issues: [
        {
          type: "fixed-width",
          severity: "warning",
          message: "Fixed width (280px) may cause horizontal scroll on mobile",
          details: {
            width: "280px",
            breakpointsAffected: [320, 375]
          }
        }
      ]
    },
    {
      selector: ".data-table",
      tagName: "table",
      issues: [
        {
          type: "wide-table",
          severity: "error",
          message: "Table wider than viewport",
          details: { width: "920px" }
        },
        {
          type: "table-not-scrollable",
          severity: "warning",
          message: "Wide table without horizontal scroll wrapper"
        }
      ]
    }
  ],
  summary: { total: 5, errors: 1, warnings: 4, elementsAnalyzed: 312 },
  currentViewport: { width: 1440, height: 900 },
  breakpointsTested: [320, 375, 414, 768, 1024, 1280, 1440, 1920]
}
```

The AI now knows `.dashboard-sidebar` will break on phones and `.data-table` is already wider than some viewports. It can suggest converting the sidebar to a percentage width or adding a scroll wrapper around the table -- with the exact selectors to target.

## Text Fragility

Text overflow is harder to catch visually than layout overflow because it is often invisible. An `overflow: hidden` container silently swallows content without any visual indicator. `checkTextFragility()` catches three categories of text problems:

- **Clipped** -- text cut off by `overflow: hidden` without ellipsis (silent content loss)
- **Overflow** -- text spilling outside its container (visible but broken)
- **Truncation** -- text shortened with ellipsis (intentional but may hide important information)

```json
proxy {action: "exec", id: "app", code: "window.__devtool.checkTextFragility()"}
```

```javascript
{
  issues: [
    {
      selector: ".product-title",
      text: "Professional Wireless Noise-Cancelling Headph...",
      longestWord: { word: "Noise-Cancelling", length: 16, minWidthPx: 148 },
      issues: [
        {
          type: "clipped",
          severity: "error",
          message: "Text clipped by overflow:hidden without ellipsis - content is silently lost",
          details: {
            scrollWidth: 320,
            clientWidth: 200,
            hiddenContent: "120px",
            suggestion: "Add text-overflow: ellipsis, or allow text to wrap, or increase container width"
          }
        }
      ],
      problematicBreakpoints: [
        { breakpoint: 320, estimatedWidth: 100, requiredWidth: 148, deficit: 48 },
        { breakpoint: 375, estimatedWidth: 120, requiredWidth: 148, deficit: 28 }
      ]
    }
  ],
  summary: { total: 3, errors: 2, warnings: 1, elementsAnalyzed: 189 }
}
```

The `problematicBreakpoints` field tells the AI exactly which screen widths will cause the problem. The `longestWord` analysis predicts overflow from individual words that are too wide for their containers -- something you would never catch by visual inspection alone.

## Touch Target Analysis

Apple's Human Interface Guidelines recommend a minimum 44x44px touch target. Google's Material Design specifies 48x48px. `checkResponsiveRisk()` flags every interactive element that falls below the 44px threshold:

```javascript
{
  selector: "button.icon-close",
  tagName: "button",
  issues: [
    {
      type: "small-touch-target",
      severity: "warning",
      message: "Touch target smaller than 44x44px minimum",
      details: {
        width: "24px",
        height: "24px",
        recommended: "44x44px minimum"
      }
    }
  ]
}
```

A common fix is adding padding without changing the visual size: `padding: 10px` on a 24px icon button brings the tap area to 44px while keeping the icon the same size. The AI can apply this fix directly because it has the exact selector and dimensions, then run `checkResponsiveRisk()` again to confirm the element no longer appears in the results.

## Finding Active Overflow Issues

While `checkResponsiveRisk()` predicts future problems, `findOverflows()` reports elements that are overflowing right now at the current viewport size:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.findOverflows()"}
```

```javascript
[
  {
    selector: ".nav-breadcrumb",
    overflow: "horizontal",
    scrollWidth: 580,
    clientWidth: 320,
    excess: 260,
    overflowStyle: "visible"
  }
]
```

This is useful for confirming that a fix actually resolved the overflow, or for quickly identifying what is causing the horizontal scrollbar a user reported.

## Sweeping Every Viewport at Once

The functions above run against the current viewport. To check the whole breakpoint range in one call, use the [`responsive_audit`](/api/responsive_audit) tool — it loads the page in hidden iframes at each target size and reports every layout, overflow, and accessibility issue per viewport, plus which issues are mobile-only vs cross-viewport:

```json
responsive_audit {proxy_id: "app"}
responsive_audit {proxy_id: "app", checks: ["layout", "overflow"]}
responsive_audit {proxy_id: "app", viewports: [{name: "xs", width: 320, height: 568}]}
```

```
=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 2 issues
  ! [layout] .header - collapsed content, element has text but zero height
  o [overflow] .sidebar - truncated text without title/tooltip

DESKTOP (1440px) - 1 issues
  ! [layout] .fixed-nav - fixed element covers 45% of viewport

SUMMARY: 3 issues (1 critical, 2 minor)
PATTERNS: 1 mobile-only, 0 tablet-only, 1 cross-viewport
```

## Dialing Into a Break Interactively

When you want to *find* the exact width where a layout breaks — not just confirm a known break — open [Responsive Mode](/api/frontend/responsive-mode), the interactive workbench. It hosts a live iframe of the page at a width you control with a slider, numeric input, preset chips, or an edge drag handle. As you change width, the same detectors run against the framed page and overlay severity-colored boxes on the elements that break at that width.

The human dials into the break and clicks **Send to agent**, which hands off `{width, shifts, selectors}`. The agent fixes the CSS, then drives the width back to verify:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.responsive.setWidth(414)"}
proxy {action: "exec", id: "app", code: "window.__devtool.responsive.getState()"}
```

`getState()` returns the current width and the remaining `shifts` — an empty `shifts` array confirms the fix. **Auto-sweep** in the panel runs the full `responsive_audit` to check for regressions at the standard viewports.

## Testing with Real Devices

Simulated viewport widths do not catch everything. Real phones have different font rendering, default zoom levels, and touch behavior. agnt's tunnel integration exposes your proxied dev server to real devices:

```json
// Start the proxy with external access
proxy {
  action: "start",
  id: "app",
  target_url: "http://localhost:3000",
  bind_address: "0.0.0.0"
}

// Create a public URL
tunnel {
  action: "start",
  id: "app",
  provider: "cloudflare",
  local_port: 45849,
  proxy_id: "app"
}
```

The tunnel response includes a public HTTPS URL you can open on any phone. The full `window.__devtool` API works through the tunnel, so your AI can run `checkResponsiveRisk()` on the page as rendered on a real device. Errors from mobile browsers are captured in the same proxy log as desktop errors.

For LAN-only testing (faster, no tunnel setup), `bind_address: "0.0.0.0"` lets you access the proxy directly from any device on your local network using your machine's IP address.

## Real-World Example: Dashboard Sidebar on Tablets

A React dashboard works fine on desktop (1440px) and phones (375px, where the sidebar collapses into a hamburger menu). But on an iPad in portrait mode (768px), the sidebar and main content area together exceed the viewport width, creating a horizontal scrollbar.

The AI runs the diagnostic:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.checkResponsiveRisk()"}
```

The result pinpoints the issue:

```javascript
{
  selector: ".dashboard-layout",
  issues: [
    {
      type: "fixed-width",
      severity: "warning",
      message: "Fixed width (280px) may cause horizontal scroll on mobile",
      details: { width: "280px", breakpointsAffected: [320, 375, 414, 768] }
    }
  ]
}
```

The sidebar has `width: 280px`. The main content has `min-width: 600px`. Together, 280 + 600 = 880px, which exceeds the 768px iPad viewport. The media query that collapses the sidebar triggers at `max-width: 640px`, missing the 768px tablet breakpoint entirely.

The AI adjusts the media query breakpoint to `max-width: 900px` and changes the sidebar width to `width: min(280px, 30vw)`. Running `checkResponsiveRisk()` again confirms the fixed-width warning is gone. Running `findOverflows()` confirms no horizontal overflow at 768px.

The entire diagnosis and fix took seconds. Finding this manually would have required noticing the scrollbar on an actual iPad -- or methodically dragging the browser window pixel by pixel through the 640-768px range.

## See Also

- [Mobile Device Testing](/use-cases/mobile-testing) -- tunnel setup, BrowserStack integration, and real device workflows
- [Layout Robustness API](/api/frontend/layout-robustness) -- full reference for `checkResponsiveRisk()`, `checkTextFragility()`, and `capturePerformanceMetrics()`
- [Layout Diagnostics API](/api/frontend/layout-diagnostics) -- reference for `findOverflows()`, `findOffscreen()`, and `findStackingContexts()`
