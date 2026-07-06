---
sidebar_position: 10
---

import ModeVideo from '@site/src/components/ModeVideo';

# api_audit

<ModeVideo src="/img/api-audit.webm" poster="/img/api-audit-poster.webp" label="API-efficiency audit on a live page — fetch calls (a serial waterfall, an N+1 fan-out over /api/user/1..5, and duplicate /api/config requests) land in the indicator's Network tab as they fire, then the audit grades the traffic and a toast reports the issues found." />

API-efficiency audit over the recorded fetch/XHR call buffer. Detects request patterns that waste time or bandwidth — serial waterfalls, N+1 fan-out, duplicate requests, and chatty page loads — and returns a scored report with concrete fix targets.

The audit reads the always-on in-page call buffer (`window.__devtool_api`). That buffer is populated by browsing, so **a fresh page load is required to fill it**. An empty buffer returns score 100 with a summary noting "no API calls recorded — reload page then re-run".

## Synopsis

```json
api_audit {proxy_id: "dev"}
api_audit {id: "dev"}
api_audit {proxy_id: "dev", raw: true}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `proxy_id` | string | Yes | — | Proxy ID to run the audit on |
| `id` | string | No | — | Alias for `proxy_id` |
| `raw` | bool | No | false | Return full JSON with every finding instead of compact text |

## Detectors

| Type | Severity | What it flags |
|------|----------|---------------|
| `waterfall` | warning / critical | Serial request chains where B starts ≈ when A ends. Reports serial wall-time vs the parallel-possible minimum, i.e. wasted time. |
| `n-plus-one` | warning | A parameterised endpoint (URL template containing an `{id}` segment) hit 5+ times — a batch opportunity. |
| `duplicate-call` | warning | An identical method+URL repeated within a 2-second window. |
| `chatty-load` | warning / critical | Too many calls during the first 3 seconds of page load. |
| `over-fetch-unavailable` | info | The call buffer carries no response-size field, so payload-bloat over-fetch is not measurable. The audit says so explicitly rather than fabricating a size. |

## Scoring

Score starts at 100 and subtracts a weight per finding:

| Finding | Weight |
|---------|--------|
| `waterfall` (critical) | 18 |
| `waterfall` (warning) | 10 |
| `n-plus-one` | 10 |
| `duplicate-call` | 8 |
| `chatty-load` (warning) | 6 |
| `chatty-load` (info) | 3 |
| `over-fetch-unavailable` (note) | 0 |

Grade: A (90+), B (80–89), C (70–79), D (60–69), F (&lt;60).

This is the 7th of the eight scored audits aggregated by [`auditAll`](/api/frontend/quality-auditing) (weight `1.1`).

## Compact Output (default)

```
=== API Efficiency Audit: C (72) ===
API efficiency is moderate. (38 calls analyzed). 3 issues to address

n-plus-one (1)
  [warning] /api/users/{id} — 12× /api/users/{id} → batch

waterfall (2)
  [critical] /api/profile — 4 calls run serially, ~820ms wasted vs parallel
  [warning] /api/cart — 2 calls run serially, ~210ms wasted vs parallel
```

Findings are grouped by type, capped at five examples per type. Each line shows severity, the originating selector/URL, and a human-readable message.

## Raw Output

```json
api_audit {proxy_id: "dev", raw: true}
```

```json
{
  "audit": "api",
  "score": 72,
  "grade": "C",
  "summary": "API efficiency is moderate. (38 calls analyzed). 3 issues to address",
  "checkedAt": "2026-06-06T10:30:00.000Z",
  "findings": [
    {
      "id": "waterfall-a1b2c3",
      "type": "waterfall",
      "severity": "critical",
      "selector": "/api/profile",
      "message": "4 calls run serially, ~820ms wasted vs parallel",
      "chainLength": 4,
      "serialWallMs": 1100,
      "maxSingleMs": 280,
      "wastedMs": 820,
      "urls": ["GET /api/profile", "GET /api/profile/prefs", "GET /api/profile/avatar"]
    }
  ],
  "findingSelectors": ["/api/profile", "/api/cart", "/api/users/{id}"]
}
```

## Empty Buffer

If no calls have been recorded yet:

```
=== API Efficiency Audit: A (100) ===
API efficiency is good. (0 calls analyzed)
No API calls recorded — reload the page then re-run.
```

Reload the page in the proxied browser tab, exercise the flow you care about, then re-run the audit.

## Data Limitation

The fetch/XHR call buffer records timing and URLs but **not response sizes**. Over-fetch by payload size is therefore not measurable — the audit emits the `over-fetch-unavailable` info note instead of guessing. A future content-length capture in the API tracker would unlock real payload-bloat detection.

## Dual Mode Operation

- **Daemon mode**: the audit JS is injected via the daemon's proxy-exec path.
- **Legacy mode** (no daemon): the same JS runs through the proxy's direct JavaScript execution channel (30s timeout).

Both paths surface module/proxy errors as `CallToolResult{IsError: true}`.

## Error Responses

```json
{ "error": "proxy_id required (or `id` alias)" }
{ "error": "proxy not found: dev" }
{ "error": "audit-api module not loaded" }
```

## See Also

- [loading_audit](/api/loading_audit) — loading-UX (spinner) audit, same output shape
- [Quality & Performance Auditing](/api/frontend/quality-auditing) — the `auditAll` aggregate that includes this audit
- [proxylog](/api/proxylog) — raw proxy traffic, including HTTP timing
- [Performance Monitoring](/use-cases/performance-monitoring) — use case guide
