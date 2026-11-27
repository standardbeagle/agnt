---
title: "AI-Powered Accessibility Auditing for Web Applications"
description: "Run WCAG accessibility audits with AI. Automatic axe-core testing, contrast checking, focus indicators, and actionable fix suggestions."
keywords: [accessibility audit, WCAG testing, a11y automation, AI coding agent, MCP server, axe-core]
sidebar_label: "Accessibility Auditing"
---

import ModeVideo from '@site/src/components/ModeVideo';

# AI-Powered Accessibility Auditing for Web Applications

Running an **accessibility audit** on a web application is one of those tasks everyone agrees is important and almost nobody does thoroughly. An **AI coding agent** with agnt can run WCAG compliance checks directly against a rendered page, interpret the results, and generate code fixes -- all without you opening a browser tab.

<ModeVideo src="/img/accessibility-audit.webm" poster="/img/accessibility-audit-poster.webp" label="auditAccessibility running axe-core over a live page — a toast reports the WCAG AA grade with critical and serious issue counts, and every flagged element (image without alt text, low-contrast buttons, unlabeled input) is outlined in red." />

## The Problem

Accessibility is usually the last thing checked before a release, if it gets checked at all. The reason is straightforward: manual testing is slow and tedious. Tab through every interactive element on the page. Inspect each image for alt text. Verify every color combination meets contrast ratios. Check that ARIA attributes are used correctly, that headings follow a logical hierarchy, that form inputs have labels. A single page can have dozens of checkpoints, and most applications have dozens of pages.

Automated tools exist, but they create their own friction. You install a browser extension, run a scan, export the results, then copy those results into your AI assistant and ask it to help you fix things. The AI reads a wall of violation codes, asks you which framework you are using, and you spend another five minutes providing context it should already have. If you fix three issues and introduce a fourth, you have to run the whole cycle again.

The gap is between seeing the problem and fixing it. Traditional tools can find accessibility violations. AI assistants can suggest fixes. But connecting the two requires you to be the middleware -- copying data between browser extensions and chat windows, translating tool output into context the AI can act on.

## The Traditional Approach

A typical accessibility testing workflow looks like this:

1. Open your application in Chrome
2. Run Lighthouse or install the axe DevTools extension
3. Wait for the scan to complete
4. Read through a report of 15-40 violations
5. Copy a violation into your AI assistant
6. Explain which component, which framework, which CSS approach
7. Get a fix suggestion, apply it
8. Re-run the scan to verify
9. Repeat for the next violation

Each violation takes 3-5 minutes to address through this copy-paste loop. A page with 20 violations is an hour of mechanical work. Multiply that across your application and accessibility testing becomes a multi-day effort that gets deferred to "next sprint" indefinitely.

## The agnt Approach

agnt sits between your browser and your dev server as a reverse proxy. It injects diagnostic JavaScript into every HTML response, giving your AI coding agent direct access to accessibility auditing functions through `window.__devtool`. The AI runs audits, reads results in an optimized format, and writes fixes -- all in a single conversation without switching tools.

```json
// Start the proxy in front of your dev server
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

// Run a full accessibility audit
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('standard')"}
```

The audit results go directly to the AI in a format designed for machine consumption -- grouped by issue type, deduplicated, with selectors that map back to your source code. The AI reads the violations, identifies the affected components, and generates fixes without you touching the browser console.

## Four Audit Modes

agnt provides four accessibility audit modes, each optimized for different stages of development.

### Standard (axe-core)

The default mode. Uses the axe-core engine to evaluate WCAG 2.1 Level AA compliance across 90+ rules. This is the same engine behind Deque's browser extension, running through agnt's proxy instead.

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('standard')"}
```

- **Rules**: 90+ WCAG 2.1 Level AA checks
- **Speed**: ~100-300ms
- **Best for**: Pre-commit checks, PR reviews, general compliance

### Fast

Focuses on the two most commonly missed categories: focus indicators and color scheme preferences. Useful during active development when you want quick feedback without waiting for a full scan.

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('fast')"}
```

- **Rules**: Focus visibility, `prefers-color-scheme` support, `prefers-reduced-motion` handling
- **Speed**: ~50-100ms
- **Best for**: Iterating on interactive components, checking dark mode support

### Comprehensive

Extends the standard audit with state-specific contrast checks (hover, focus, active states) and responsive breakpoint testing. This mode simulates viewport changes and interaction states to catch issues that only appear under specific conditions.

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('comprehensive')"}
```

- **Rules**: Everything in standard, plus state-specific contrast, responsive layout checks
- **Speed**: ~500-2000ms
- **Best for**: Final review before release, compliance certification

### Basic

A lightweight fallback that runs minimal checks without external dependencies. Useful when axe-core loading fails or when you need the fastest possible feedback loop.

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('basic')"}
```

- **Rules**: Missing alt text, empty links, missing labels, document language
- **Speed**: ~10-50ms
- **Best for**: Smoke tests, environments where axe-core is unavailable

## Running an Audit

A full audit workflow starts with the proxy and takes three tool calls.

```json
// 1. Start your dev server through agnt
run {script_name: "dev"}

// 2. Start the proxy
proxy {action: "start", id: "app", target_url: "http://localhost:3000"}

// 3. Run the audit
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('standard')"}
```

To audit a specific element instead of the full page, use `getA11yInfo` or `getContrast` with a CSS selector:

```json
// Check accessibility attributes on a specific button
proxy {action: "exec", id: "app", code: "window.__devtool.getA11yInfo('#submit-btn')"}

// Check contrast ratio on muted text
proxy {action: "exec", id: "app", code: "window.__devtool.getContrast('.text-muted')"}
```

## Interpreting Results

Audit output comes in two formats.

**Default (AI-optimized)**: Issues are grouped by type with limited examples per group. This format minimizes token usage while giving the AI enough context to generate fixes. It is the format returned by default.

```json
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('standard')"}
```

```
Score: 72/100

Errors (3):
  missing-alt (2): img.hero-image, img.team-photo
    WCAG 1.1.1 — Add alt attribute describing image content
  missing-label (1): input#email
    WCAG 1.3.1 — Add <label for="email"> or aria-label

Warnings (2):
  low-contrast (1): .muted-text — ratio 3.2:1, needs 4.5:1
    WCAG 1.4.3 — Darken text or lighten background
  positive-tabindex (1): button.priority — tabindex="5"
    Use tabindex="0" and DOM order instead

Passes: 38/45
```

**Raw (verbose)**: Returns full JSON with every violation, every passing check, element details, and fix suggestions. Use this when the AI needs to do deeper analysis or when you want a complete record.

```json
proxy {action: "exec", id: "app", code: `
  window.__devtool.auditAccessibility('standard', {raw: true})
`}
```

The raw format includes selectors, element metadata, WCAG criterion references, and suggested fixes for each violation. The AI-optimized format omits redundant details and caps examples to keep responses focused.

## Common Issues and Fixes

These are the five violations that account for the majority of accessibility failures. Each includes what the AI sees in the audit output and what it generates as a fix.

### Missing Alt Text on Images

**Audit output**: `missing-alt: img.hero-image — WCAG 1.1.1`

**Fix**:
```html
<!-- Before -->
<img class="hero-image" src="/hero.jpg" />

<!-- After -->
<img class="hero-image" src="/hero.jpg" alt="Team collaborating around a whiteboard" />
```

For decorative images that carry no meaning, use an empty alt attribute so screen readers skip them: `alt=""`.

### Low Contrast Text

**Audit output**: `low-contrast: .muted-text — ratio 3.2:1, needs 4.5:1`

```json
proxy {action: "exec", id: "app", code: "window.__devtool.getContrast('.muted-text')"}
```

**Fix**:
```css
/* Before: #999 on #fff = 2.85:1 */
.muted-text { color: #999; }

/* After: #595959 on #fff = 7.0:1 (passes AAA) */
.muted-text { color: #595959; }
```

### Unlabeled Form Inputs

**Audit output**: `missing-label: input#email — WCAG 1.3.1`

**Fix**:
```html
<!-- Before -->
<input id="email" type="email" placeholder="Email address" />

<!-- After: explicit label (preferred) -->
<label for="email">Email address</label>
<input id="email" type="email" placeholder="Email address" />

<!-- Or: aria-label for visually hidden labels -->
<input id="email" type="email" aria-label="Email address" placeholder="Email address" />
```

Placeholder text is not a substitute for labels. Screen readers may not announce it, and it disappears when the user starts typing.

### Incorrect ARIA Usage

**Audit output**: `aria-required-attr: div[role="checkbox"] — missing aria-checked`

**Fix**:
```html
<!-- Before: role without required attributes -->
<div role="checkbox" class="custom-checkbox">Accept terms</div>

<!-- After: all required ARIA attributes present -->
<div role="checkbox" aria-checked="false" tabindex="0" class="custom-checkbox">
  Accept terms
</div>
```

When possible, prefer native HTML elements over ARIA roles. A `<input type="checkbox">` handles keyboard interaction and state management automatically.

### Keyboard Traps

Focus enters a component but cannot leave via keyboard. The audit flags this, but verification requires checking tab order:

```json
proxy {action: "exec", id: "app", code: "window.__devtool.getTabOrder('.modal')"}
```

**Fix**: Ensure modals have a close mechanism that works with keyboard (Escape key) and that focus returns to the triggering element when the modal closes. The last focusable element in the modal should tab back to the first, creating a focus loop within the modal only while it is open.

## Continuous Accessibility Testing

The most effective approach is to run audits after every code change, not as a separate testing phase. When the AI modifies a component, it can immediately verify the change did not introduce regressions:

```json
// AI makes a change to the navigation component
// ... edits Nav.tsx ...

// Immediately verify accessibility
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('fast')"}
```

For thorough coverage during development, pair the fast mode for interactive work with a standard audit before committing:

```json
// Quick check during iteration
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('fast')"}

// Full check before commit
proxy {action: "exec", id: "app", code: "window.__devtool.auditAccessibility('standard')"}
```

This turns accessibility from a gate at the end of the pipeline into a continuous feedback loop. Issues are caught and fixed within the same AI conversation that introduced the code, while the context is still fresh.

## See Also

- [Accessibility Auditing Use Case](/use-cases/accessibility-auditing) -- workflow patterns and CI integration
- [Accessibility API Reference](/api/frontend/accessibility) -- full parameter docs for `getA11yInfo`, `getContrast`, `getTabOrder`, `getScreenReaderText`, and `auditAccessibility`
- [Debug Browser Errors with AI](/guides/debug-browser-errors-ai) -- capture JavaScript errors and HTTP failures alongside accessibility issues
