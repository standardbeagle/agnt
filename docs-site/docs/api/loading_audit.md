---
sidebar_position: 11
---

import ModeVideo from '@site/src/components/ModeVideo';

# loading_audit

<ModeVideo src="/img/loading-audit.webm" poster="/img/loading-audit-poster.webp" label="Loading-UX audit on a live page — three report cards flicker concurrent spinners while a status row runs loaders one after another; the audit reads the recorded spinner timeline and a toast reports the cascade and fragmentation findings with a grade." />

Loading-UX audit over a spinner timeline. Detects two failure modes that make a page feel slow even when the network isn't: **spinner cascades** (loaders that fire one after another instead of together) and **spinner fragmentation** (many small loaders flickering at once where one master loader would do).

A self-contained spinner observer records loading indicators as they appear and disappear, correlating each spinner's active window with overlapping API calls. The timeline is **populated by browsing**, so a fresh page load is required to fill it. An empty timeline returns score 100 with a "reload page then re-run" summary.

## Synopsis

```json
loading_audit {proxy_id: "dev"}
loading_audit {id: "dev"}
loading_audit {proxy_id: "dev", raw: true}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `proxy_id` | string | Yes | — | Proxy ID to run the audit on |
| `id` | string | No | — | Alias for `proxy_id` |
| `raw` | bool | No | false | Return full JSON with every finding instead of compact text |

## What Counts as a Spinner

The recorder detects loading indicators via:

- `aria-busy="true"`
- `role="progressbar"` / `role="status"`
- `<progress>` elements
- class / id / aria text matching `spin`, `load`, `skeleton`, `shimmer`, `pending`, `placeholder`
- a spin-like CSS `animationName`

For each indicator it records `{id, selector, ancestorPath, appearedAt, disappearedAt, pendingAPI}` (appear/disappear paired via a WeakMap).

## Detectors

| Type | Severity | What it flags |
|------|----------|---------------|
| `spinner-cascade` | warning / critical | Loader B appears within 400ms of loader A disappearing, B sits in A's DOM region, and A had a resolved API call → a serial chain of depth ≥2. Reports serial span vs the parallel-possible minimum. |
| `spinner-fragmentation` | warning | 3+ spinners active simultaneously under one common ancestor → consolidate to a single master loader. |

## Scoring

Score starts at 100 and subtracts a weight per finding:

| Finding | Weight |
|---------|--------|
| `spinner-cascade` | `8 + 4 × (depth − 2)`, plus `6` if critical |
| `spinner-fragmentation` | `6 + (count − 3)` |

Grade: A (90+), B (80–89), C (70–79), D (60–69), F (&lt;60).

This is the 8th of the eight scored audits aggregated by [`auditAll`](/api/frontend/quality-auditing) (weight `1.1`).

## Compact Output (default)

```
=== Loading UX Audit: B (84) ===
Loading UX is moderate. (7 loaders analyzed). 2 issues to address

spinner-cascade (1)
  [warning] .profile-card — 3 loaders fire serially (avatar → bio → stats), ~640ms span

spinner-fragmentation (1)
  [warning] .dashboard-grid — 4 spinners active at once under one container
```

Findings are grouped by type, capped at five examples per type. Each line shows severity, the originating selector, and a human-readable message.

## Raw Output

```json
loading_audit {proxy_id: "dev", raw: true}
```

```json
{
  "audit": "loading",
  "score": 84,
  "grade": "B",
  "summary": "Loading UX is moderate. (7 loaders analyzed). 2 issues to address",
  "checkedAt": "2026-06-06T10:30:00.000Z",
  "findings": [
    {
      "id": "spinner-cascade-d4e5f6",
      "type": "spinner-cascade",
      "severity": "warning",
      "selector": ".profile-card",
      "message": "3 loaders fire serially (avatar → bio → stats), ~640ms span",
      "depth": 3
    }
  ],
  "findingSelectors": [".profile-card", ".dashboard-grid"]
}
```

## Empty Timeline

If no spinners have been recorded yet:

```
=== Loading UX Audit: A (100) ===
Loading UX is good. (0 loaders analyzed)
No loaders recorded — reload the page then re-run.
```

Reload the page in the proxied browser tab, let the loading states play out, then re-run the audit.

## Dual Mode Operation

- **Daemon mode**: the audit JS is injected via the daemon's proxy-exec path.
- **Legacy mode** (no daemon): the same JS runs through the proxy's direct JavaScript execution channel (30s timeout).

Both paths surface module/proxy errors as `CallToolResult{IsError: true}`.

## Error Responses

```json
{ "error": "proxy_id required (or `id` alias)" }
{ "error": "proxy not found: dev" }
{ "error": "audit-loading module not loaded" }
```

## See Also

- [api_audit](/api/api_audit) — API-efficiency audit, same output shape
- [Quality & Performance Auditing](/api/frontend/quality-auditing) — the `auditAll` aggregate that includes this audit
- [Performance Monitoring](/use-cases/performance-monitoring) — use case guide
