---
sidebar_position: 7
---

# get_incidents

Cursor-based pull from the always-active incident inbox. This is the authoritative tool for fetching errors and warnings from every signal source — browser JS, HTTP 4xx/5xx, process crashes, proxy diagnostics — normalized, deduplicated, and returned in priority order with remediation hints and suggested next tools.

`alerts.push` selects live, project-isolated interrupt channels; it does not gate inbox recording. The legacy `alerts.incident-pipeline` key is accepted but ignored.

With `detail: "full"`, payload hydration reads only from the caller session's
bounded in-memory blob store. An inbox `PayloadRef` is published only after its
bytes are readable, so scheduler timing cannot turn a resident payload into a
summary-only response. Payloads remain best-effort across real LRU eviction or
session teardown; those cases safely fall back to `summary` and never search
another session's store.

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
| `action` | string | No | `query` | `query`, `pin`, `unpin`, or `clear`. Pins keep an incident alive past band eviction and every retention clear; `clear` retires the session's unpinned incidents |
| `error_id` | string | No | - | Pin/unpin target: the incident fingerprint (or id) from a prior result |
| `tag` | string | No | - | Note stored with a pin, returned on the pinned item |
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
=== Incidents (3) === [inbox: crit=1 err=1 warn=1 info=0 new=3]

[critical:process_crash] panic (2x, 3s ago)
  id: 8b1f42c09ad7e653
  panic: runtime error: index out of range
  at: internal/proxy/server.go:142
  next: proc {action:"output", process_id:"agnt-dev"}

[error:browser_js] TypeError (1x, 8s ago)
  id: 3f9a1c07e2b4d886
  Cannot read property 'map' of undefined
  at: src/components/List.tsx:42:15
  → http://localhost:3000/dashboard

[warning:http_4xx] 404 (1x, 30s ago)
  id: 51d0ae87c2f9b344
  GET /api/old-endpoint

=== Next ===
tool: proc {action:"output", process_id:"agnt-dev"}
```

Each incident renders its fingerprint (`id:` — the pin/unpin target), occurrence count, recency, a first-frame location (`at:`), the page URL (`→`, with `[frame …]` when frame attribution made two same-message incidents distinct), and — where the routing table has a match — a `next:` tool and `skill:` hint. A closing `=== Next ===` section aggregates the dominant remediation across the returned set, and a `!! PARTIAL VIEW` section appears whenever `collection_warnings` is non-empty — bus-dropped events or unhydratable payloads mean the view is missing incidents, and the tool says so rather than staying silent.

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

- [proxylog](/api/proxylog) — raw proxy traffic for drill-down
- [proc](/api/proc) — process output referenced by remediation hints
