# Unified Incident Queue — Design

Date: 2026-06-04
Status: Approved (pre-implementation)

## Problem

A down dependency (e.g. database) makes a dev server return `5xx` on many
distinct URLs. Today the **legacy `AlertHub` path is the default** (the incident
pipeline is opt-in behind `alerts.incident-pipeline`), so the storm reaches the
AI agent essentially raw: one message per error, no root-cause collapse, no
standing summary, no drain semantics. The agent gets spammed and cannot tell
"the DB is down" from "100 unrelated errors."

Two structural faults cause this:

1. **Dual delivery paths.** `fireGatedFanOut` (`internal/daemon/hub_helpers.go`)
   fans every gated entry to BOTH `alertHub.BroadcastLogEntry` AND the incident
   bus. The legacy `AlertHub` + its three sinks (`MCPAlertSink`,
   `OverlayAlertSink`, `StreamSink`) duplicate — worse, partially bypass — the
   gating the incident pipeline already does. Per
   `.claude/skills/messaging-queue`, parallel writes to the agent surface are
   the canonical spam cause. The legacy path is a bug, not an architecture
   choice.

2. **Per-URL fingerprinting.** `computeFingerprint(source, category,
   canonical_msg, url)` includes the URL, so each distinct-URL `5xx` is a
   distinct fingerprint. Dedup/coalesce never merge them; the inbox error band
   fills with ~100 separate entries even with the pipeline enabled.

## Goal

One ordered queue from raw event → agent input, with:

- **Full removal of legacy `AlertHub`.** The incident pipeline becomes the only
  path, unconditional for every session. No `incident-pipeline` flag, no opt-out.
- **Coarse storm collapse.** A `5xx` flood from one proxy collapses into ONE
  inbox entry (count + sample URLs), so the agent sees the root cause.
- **Heartbeat digest + cursor-clear.** While a backlog of unread incidents
  persists, one digest event fires on a timer (~30s). The agent drains via
  `get_incidents` (advances cursor + marks read); the queue then goes silent
  until a genuinely new incident arrives.

## Decisions (locked)

| Fork | Decision |
|------|----------|
| Storm collapse granularity | **Coarse root-cause grouping** — a "storm fingerprint" keyed on `(source, status-class, proxyID)` merges all same-class errors from one proxy into one incident. Per-URL detail kept as a bounded sample set + available via `get_incidents raw=true`. |
| Periodic summary semantics | **Heartbeat digest + cursor-clear** — timer-driven digest while unread backlog exists; `get_incidents` advances cursor + `MarkRead` = "cleared"; silent until a new fingerprint or escalation appears past the cursor. |
| Legacy cutover | **Full removal, pipeline always-on** — delete `AlertHub` + the three sinks + the flag. `get_errors` becomes a read-only inbox shim; channel mode + PTY injection re-wire onto the Pinger sinks. |
| Digest cadence | 30s default, configurable under `alerts { digest { ... } }`. |
| Storm key scope | Includes `proxyID` so two proxies' storms do not merge into one entry. |

## What stays vs goes

### Stays (already correct, reused)

- The proxy-side **gate chain**: `HealthTracker`, `OutageClassifier`,
  `HoldBuffer` (`proxyBroadcastDecision` / `fireGatedFanOut` /
  `fireToIncidentBus`). This feeds the bus and is the real single-queue front
  end. Only the `alertHub` fan-out *leg* is removed.
- The incident pipeline: `Deduplicator`, `Coalescer` (per-fingerprint
  exponential backoff), `FlowController` (per-severity token buckets), `Inbox`
  (4 bands, cursor, `MarkRead`, `Subscribe`), `PingEmitter` (summary payload +
  3-channel fan-out), `MPSCBus` / `sessionPipeline`.
- `agnt monitor` (`STREAM-EVENTS`) as a user-facing stream — re-pointed at the
  bus/inbox instead of `StreamSink`.

### Goes (deleted)

- `internal/daemon/alert_hub.go` (the `AlertHub`, `MCPAlertSink`,
  `OverlayAlertSink`, `StreamSink` definitions and fan-out).
- `DriftMetricsSnapshot` / `OldPathCount` / `NewPathCount` / `IncrNewPath`
  (dual-path drift counters — meaningless once single-path).
- `AlertHub.SetIncidentPipeline`.
- `config.AlertsConfig.IncidentPipeline` field + `IncidentPipelineEnabled()`,
  and the `tools` mirror `incidentPipelineConfig`.

## Architecture

### Pipeline (single path, unconditional)

```
proxy/process signal
   → adapter (FromHTTPEntry / FromProxyDiagnostic / FromFrontendError / ...)
   → proxyBroadcastDecision gate (health / outage / hold)
   → fireToIncidentBus → MPSCBus.Publish
   → sessionPipeline: Deduplicator → Inbox (band insert/merge)
   → Inbox.Subscribe delta
       ├→ PingEmitter (event-driven, flow-gated, coalesced)  [existing]
       └→ DigestTicker (timer-driven heartbeat while unread)  [new]
   → sinks: MCP Log / claude-channel / PTY stdin
```

For every session the bus has a pipeline (today some sessions get a no-op bus;
after removal `AddSession` is called for all). `get_incidents` and `get_errors`
both read `bus.QuerySession`.

### Storm collapse — `(source, status-class, proxyID)` fingerprint

`adapter_http.go::FromHTTPEntry` currently builds
`msg = "METHOD URL → STATUS"` and lets `NewIncidentEvent` fingerprint on
`(source, category=STATUS, canonical(msg), url)`. Change for 4xx/5xx:

- Compute a **storm fingerprint** directly from
  `(source, statusClass, proxyID)` where `statusClass` is `"5xx"` / `"4xx"`
  (not the exact code) — so `500`, `502`, `503` on the same proxy merge.
- Drop URL from both the canonical message and the location input so the
  fingerprint is URL-independent.
- Keep a human sample message ("502 Bad Gateway — upstream unreachable") on the
  event `Summary`.

Because dedup/inbox already key on `Fingerprint` and merge with `Count++`, no
new merge machinery is needed — the coarse key makes the existing merge collapse
the storm.

> Implementation note: `computeFingerprint` does not currently fold in
> `proxyID`. The storm path must include it explicitly (either a dedicated
> `computeStormFingerprint` or by passing `proxyID` into the location slot).
> Pin this so two proxies' storms stay distinct.

### Bounded sample set on merge

So a collapsed entry can render `47x across 12 URLs` with examples:

- Extend the merge in `Deduplicator.Ingest` / `Inbox.Ingest` to accumulate a
  **bounded distinct-sample set** per entry: a small ordered set of distinct
  URLs (cap ~10) plus a `DistinctCount`. Cap is hard; overflow increments
  `DistinctCount` without growing memory.
- Only storm-source entries populate the sample set (non-storm entries keep
  today's single `Sample`).
- Full per-URL list is NOT held in the inbox; `get_incidents raw=true` retrieves
  detail from the proxy traffic log / BlobStore.

### Heartbeat digest + cursor-clear

- Add a `DigestTicker` owned by `sessionPipeline` (or `PingEmitter`): a
  `time.Ticker` at the configured interval.
- On each tick: if `Inbox.Stats().*` shows any **unread** entries, call the
  existing `PingEmitter.emit()` (summary payload of the standing backlog).
  If zero unread, emit nothing (silent).
- The digest is a SUPPLEMENT to the existing event-driven coalesced pings: new
  incidents still ping immediately (flow-gated); the heartbeat is the "still
  broken, standing summary" beat so the agent isn't left guessing after the
  first burst.
- "Clear": `get_incidents` advances the inbox cursor and marks pulled
  fingerprints read. After a full drain, `Stats` reports zero unread → digest
  silent. A new fingerprint, a new escalation, or a count past the cursor is
  what re-arms it. Backlog alone never re-spams.

### get_errors → read-only shim

`get_errors` becomes a thin formatter over `bus.QuerySession` (it is already
partly a shim when the pipeline is enabled). Daemon-less/legacy mode: returns
proxy errors directly as today. The tool keeps its current schema for
back-compat.

### Monitor / channel / PTY re-wire

- `STREAM-EVENTS` hub handler (`hub_stream.go`) subscribes to the bus/inbox
  delta stream (or a thin adapter) instead of registering a `StreamSink`.
  Existing `--types` / `--proxy` / `--severity` / grep filters must be
  preserved; parity test required.
- Channel mode (`channel_sink.go`) supplies `channelNotify` to `AddSession`;
  PTY injection supplies `ptyInject`. Both already exist as Pinger sink params —
  the work is wiring the existing implementations into `AddSession` instead of
  into `AlertHub` sinks.

## Config changes

```kdl
alerts {
    // REMOVED: incident-pipeline (pipeline is now unconditional)
    // RETAINED unchanged: push { mcp-notifications / pty-injection },
    //   ping { mcp-notifications / pty-injection / channel }, blob-budget.
    //   Only the incident-pipeline on/off gate disappears.

    digest {
        enabled true        // heartbeat digest on/off (default true)
        interval 30000      // ms between digests while backlog unread (default 30s)
    }
    // push channels (mcp-notifications / pty-injection) and ping config retained
}
```

Removing `incident-pipeline` is a breaking config change: parsing an unknown
`incident-pipeline` key must be tolerated (ignored with a one-line warning), not
fatal, so existing `.agnt.kdl` files keep loading.

## Error handling

- Bus overflow: unchanged (drop-newest at 4096, `bus.OverflowCount()`).
- Inbox band overflow: unchanged (LRU evict per band, `Stats.Dropped`).
- Storm sample-set overflow: increment `DistinctCount`, never grow past cap.
- Digest tick while session tearing down: guarded by `stopCh`; no emit after
  `RemoveSession`.
- Pinger sinks remain non-blocking (channel-send-with-default); a slow consumer
  never stalls the inbox drain or the digest ticker.

## Testing

- **Storm collapse:** 200 distinct-URL 5xx on one proxy → exactly one inbox
  error entry, `Count == 200`, sample set ≤ cap, `DistinctCount` correct.
- **Cross-proxy isolation:** storms on proxy A and proxy B → two distinct
  entries.
- **Single delivery:** ten identical events in 100ms → one delivery (existing
  contract, re-assert post-removal).
- **Heartbeat:** persistent unread backlog → one digest per interval, not N;
  goes silent after `get_incidents` drains; re-arms only on a new fingerprint.
- **No legacy path:** grep/build proves `AlertHub` and the three sinks are gone;
  no caller references the removed symbols.
- **Monitor parity:** `agnt monitor --types ... --severity ...` produces the
  same event set pre/post re-wire.
- **Channel + PTY parity:** channel mode and `agnt run` PTY injection still
  deliver pings.
- **Config back-compat:** a `.agnt.kdl` containing `incident-pipeline true`
  still loads (key ignored, warned).

## Out of scope

- New signal sources beyond the existing 11 adapters.
- Persisting the inbox across daemon restarts (still best-effort).
- Outage-suppression mode for storms (we collapse, not suppress — the rejected
  alternative). Can be layered later on top of the storm fingerprint.

## Migration / blast radius

Single large diff. Highest-risk pieces, in order:

1. `STREAM-EVENTS` re-wire (monitor semantics).
2. Channel-mode re-wire (forked go-sdk `Notify`).
3. PTY-injection re-wire (`agnt run` overlay).

Each gets a parity test before the corresponding legacy code is deleted.
