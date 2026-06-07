---
sidebar_position: 7
---

# get_incidents

Cursor-based incident inbox pull. When the incident pipeline is enabled (`alerts.incident-pipeline true` in `.agnt.kdl`), this is the authoritative tool for fetching errors and warnings from every signal source — browser JS, HTTP 4xx/5xx, process crashes, proxy diagnostics — normalized, deduplicated, and returned in priority order with remediation hints and suggested next tools.

When the incident pipeline is **off** (the default), use [get_errors](/api/get_errors) instead.

## Synopsis

```json
get_incidents {}
get_incidents {severity: "error", limit: 10}
get_incidents {cursor: "<opaque-cursor>"}
get_incidents {source: "browser_js", raw: true}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `cursor` | string | No | beginning | Opaque cursor from a previous response for incremental pull |
| `limit` | int | No | 25 | Max incidents returned |
| `severity` | string | No | `warning` | Minimum severity: `info`, `warning`, `error`, `critical` |
| `source` | string | No | all | Filter by signal source (e.g. `browser_js`, `http_5xx`, `process_crash`) |
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

The response includes a cursor. Pass it back on the next call to fetch only incidents newer than the last pull:

```json
get_incidents {limit: 25}
// ... process, note the cursor in the response ...
get_incidents {cursor: "<that-cursor>"}
```

## Session Isolation

The incident inbox is **hard-isolated per session** — incidents from one session never appear in another, even for the same project. For this reason `get_incidents` does not take the cross-project `global` flag that the other gated tools expose.

## Enabling the Pipeline

```kdl
alerts {
    incident-pipeline true
}
```

See [Configuration](/agnt-kdl) for the full set of incident-pipeline keys (`blob-budget`, ping channels).

## See Also

- [get_errors](/api/get_errors) — the legacy/daemon-less error view this supersedes
- [proxylog](/api/proxylog) — raw proxy traffic for drill-down
- [proc](/api/proc) — process output referenced by remediation hints
