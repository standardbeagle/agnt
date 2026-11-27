---
sidebar_position: 9
---

import ModeVideo from '@site/src/components/ModeVideo';

# responsive_audit

Headless responsive-design audit across multiple viewport sizes. Loads the page in hidden iframes at each target size and reports layout issues, content overflows, and viewport-specific accessibility problems — in one shot, no manual resizing.

For an interactive loop where you dial into a specific break and hand it to the agent, see [Responsive Mode](/api/frontend/responsive-mode); Auto-sweep there runs this same audit.

<ModeVideo src="/img/responsive-audit.webm" poster="/img/responsive-audit-poster.webp" label="The same responsive checks driven interactively — the page is stepped through desktop, tablet, and phone widths, and at 375px the fixed-minimum-width invoices table visibly overflows its panel, the kind of break the audit reports per viewport." />

## Synopsis

```json
responsive_audit {proxy_id: "dev"}
responsive_audit {proxy_id: "dev", checks: ["layout", "overflow"]}
responsive_audit {proxy_id: "dev", viewports: [{name: "xs", width: 320, height: 568}]}
responsive_audit {proxy_id: "dev", raw: true}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `proxy_id` | string | Yes | — | Proxy ID to run the audit on |
| `viewports` | array | No | mobile / tablet / desktop | Custom viewports `[{name, width, height}]` |
| `checks` | array | No | all | Subset of `["layout", "overflow", "a11y"]` |
| `timeout` | int | No | 10000 | Load timeout per viewport (ms) |
| `raw` | bool | No | false | Return full JSON instead of compact text |

## Default Viewports

| Name | Size | Device |
|------|------|--------|
| Mobile | 375 × 667 | iPhone SE |
| Tablet | 768 × 1024 | iPad |
| Desktop | 1440 × 900 | — |

## Checks

| Check | Detects |
|-------|---------|
| `layout` | Collapsed content (has text, zero height), fixed elements covering large viewport fractions, margin/padding squeeze |
| `overflow` | Horizontal scroll, clipped content, truncated text without tooltip, squeezed images |
| `a11y` | Touch targets too small (mobile), iOS zoom-triggering font sizes, readability issues |

## Issue Severities

- **critical** — horizontal scroll, collapsed content (breaks layout)
- **warning** — touch targets too small, fixed elements covering 25–40% of viewport
- **info** — truncated text without tooltip, small font sizes on mobile

## Compact Output (default)

```
=== Responsive Audit: 3 viewports ===

MOBILE (375px) - 2 issues
  ! [layout] .header - collapsed content, element has text but zero height
  o [overflow] .sidebar - truncated text without title/tooltip

TABLET (768px) - 0 issues

DESKTOP (1440px) - 1 issues
  ! [layout] .fixed-nav - fixed element covers 45% of viewport

SUMMARY: 3 issues (1 critical, 2 minor)
PATTERNS: 1 mobile-only, 0 tablet-only, 1 cross-viewport
```

## Raw Output

```json
responsive_audit {proxy_id: "dev", raw: true}
```

```json
{
  "viewports": {
    "mobile": {
      "width": 375,
      "issues": [
        {"type": "layout", "severity": "critical", "selector": ".header", "message": "..."}
      ]
    }
  },
  "summary": {"total": 3, "critical": 1, "minor": 2},
  "patterns": {"mobileOnly": 1, "tabletOnly": 0, "crossViewport": 1}
}
```

## Pattern Detection

- `mobileOnly` — issues appearing only on the mobile viewport
- `tabletOnly` — issues appearing only on the tablet viewport
- `crossViewport` — issues appearing across all viewports

## See Also

- [Responsive Mode](/api/frontend/responsive-mode) — interactive width-driving workbench
- [Responsive Design Testing with AI](/guides/responsive-design-testing-ai) — workflow guide
- [Mobile Testing](/use-cases/mobile-testing) — use case guide
