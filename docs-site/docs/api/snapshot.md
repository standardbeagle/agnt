---
sidebar_position: 12
---

# snapshot

Capture and compare visual snapshots for regression testing. Take a baseline set of screenshots, make changes, then compare — `snapshot` computes per-page pixel diffs and flags regressions against a threshold.

## Synopsis

```json
snapshot {action: "screenshot", proxy_id: "dev"}
snapshot {action: "baseline", name: "before-refactor", pages: [...]}
snapshot {action: "compare", baseline: "before-refactor", pages: [...]}
snapshot {action: "list"}
```

## Actions

| Action | Description |
|--------|-------------|
| `screenshot` | Capture the current page of a running proxy (writes a PNG, returns the path) |
| `baseline` | Create a named baseline from a set of screenshots |
| `compare` | Compare current screenshots against a baseline and report diffs |
| `list` | List all available baselines |
| `get` | Get details of a specific baseline |
| `delete` | Delete a baseline |

## Parameters

| Parameter | Type | Used by | Description |
|-----------|------|---------|-------------|
| `action` | string | all | One of the actions above |
| `name` | string | baseline / compare / delete / get; optional for screenshot | Baseline or screenshot name |
| `baseline` | string | compare | Baseline name to compare against |
| `pages` | array | baseline / compare | Pages to capture: `[{url, viewport, screenshot_data}]` |
| `diff_threshold` | float | compare | Diff sensitivity 0.0–1.0 (default 0.01) |
| `proxy_id` | string | screenshot | Proxy whose current page to capture (preferred) |
| `id` | string | screenshot | Alias for `proxy_id` |
| `selector` | string | screenshot | CSS selector to capture (default: full viewport) |
| `full_page` | bool | screenshot | Capture the full scrollable page (default: viewport only) |

## Capturing a Screenshot

```json
snapshot {action: "screenshot", proxy_id: "dev"}
snapshot {action: "screenshot", proxy_id: "dev", name: "homepage", full_page: true}
snapshot {action: "screenshot", proxy_id: "dev", selector: ".header"}
```

The `screenshot` action requires daemon mode (it routes through the daemon's proxy session). It writes a PNG and returns its path.

## Baseline / Compare Workflow

```json
// 1. Capture a baseline before changes
snapshot {action: "baseline", name: "before-refactor",
  pages: [{url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "base64..."}]}

// 2. After changes, compare
snapshot {action: "compare", baseline: "before-refactor",
  pages: [{url: "/", viewport: {width: 1920, height: 1080}, screenshot_data: "base64..."}]}
```

Comparison reports per-page `diff_percentage`, a saved diff-image path, and a summary:

```json
{
  "baseline_name": "before-refactor",
  "pages": [
    {"url": "/", "diff_percentage": 2.5, "has_changes": true, "diff_image_path": "...", "description": "Moderate changes (2.50%)"}
  ],
  "summary": {"total_pages": 1, "pages_changed": 1, "average_diff": 2.5, "has_regressions": true}
}
```

A page counts as changed when its diff exceeds `diff_threshold`.

## See Also

- [responsive_audit](/api/responsive_audit) — viewport-size regression checks
- [Visual Regression Testing with AI](/guides/visual-regression-testing-ai) — workflow guide
- [proxy](/api/proxy) — proxy lifecycle the screenshot action drives
