---
sidebar_position: 7
---

# get_incidents

Cursor-based pull from the always-active incident inbox. This is the authoritative tool for fetching errors and warnings from every signal source — browser JS, HTTP 4xx/5xx, process crashes, proxy diagnostics — normalized, deduplicated, and returned in priority order with remediation hints and suggested next tools.

`alerts.push` selects live, project-isolated interrupt channels; it does not gate inbox recording. The legacy `alerts.incident-pipeline` key is accepted but ignored.

## Synopsis

```json
get_incidents {}
get_incidents {severity: ["error", "critical"], limit: 10}
get_incidents {since: "<cursor-from-prior-pull>"}
get_incidents {sources: ["browser_js"], raw: true}
get_incidents {since: "5m", detail: "full"}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `severity` | string[] | No | all | Filter by band: `critical`, `error`, `warning`, `info` |
| `since` | string | No | beginning | Cursor from a prior pull (RFC3339 timestamp) **or** a duration like `5m` |
| `sources` | string[] | No | all | Filter by signal source: `browser_js`, `http_5xx`, `http_4xx`, `transport_err`, `proxy_diag`, `process_alert`, `process_crash`, `build_fail`, `port_conflict`, `shutdown`, `hook_stop_failure` |
| `fingerprints` | string[] | No | - | Retrieve specific incidents by fingerprint |
| `proxy_id` | string | No | - | Filter to a specific proxy |
| `process_id` | string | No | - | Filter to a specific process |
| `detail` | string | No | `summary` | `summary` or `full` (hydrates the full payload from the blob store) |
| `mark_read` | bool | No | false | Advance the cursor and mark returned incidents as read |
| `limit` | int | No | 20 | Max incidents returned (max 100) |
| `raw` | bool | No | false | Return full JSON instead of compact text |

## Priority Ordering

Incidents are returned in band order: **critical → error → warning → info**. The inbox is partitioned into four priority bands, each capped at 100 entries; oldest entries in a band are evicted as new ones arrive. Poll with the returned cursor to drain the inbox before it wraps.

## Compact Output (default)

```
=== Incidents (3) ===

[critical] process_crash — agnt-dev (2x, latest 3s ago)
  panic: runtime error: index out of range
  → internal/proxy/server.go:142
  remediation: proc {action:"output", process_id:"agnt-dev"} → get_incidents
  next_tools: proc, get_incidents

[error] browser_js — TypeError (1x, 8s ago)
  Cannot read property 'map' of undefined
  → src/components/List.tsx:42:15

[warning] http_4xx — GET /api/old-endpoint (1x, 30s ago)
```

Each incident carries occurrence count, recency, a first-frame location, and — where the routing table has a match — a **remediation** hint and **next_tools** suggestion.

## Cursor Pull

The response includes a cursor. Pass it back as `since` on the next call to fetch only incidents newer than the last pull:

```json
get_incidents {limit: 20}
// ... process, note the cursor in the response ...
get_incidents {since: "<that-cursor>"}
```

Add `mark_read: true` to advance the cursor server-side and mark the returned incidents as read in one call.

## Session Isolation

The incident inbox is **hard-isolated per session** — incidents from one session never appear in another, even for the same project. For this reason `get_incidents` does not take the cross-project `global` flag that the other gated tools expose.

## Push Delivery (Optional)

```kdl
alerts {
    preset "claude-code" // digest only; "universal" adds PTY injection
}
```

See [Configuration](/agnt-kdl) for `alerts.push` and preset details.

## See Also

- [get_errors](/api/get_errors) — the legacy/daemon-less error view this supersedes
- [proxylog](/api/proxylog) — raw proxy traffic for drill-down
- [proc](/api/proc) — process output referenced by remediation hints
