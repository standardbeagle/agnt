# Unified Agent-Inbound Message Queue — Design

Date: 2026-06-04
Status: Approved-in-principle (pre-implementation); supersedes the earlier
"unified incident queue" scope.

## Problem

Everything bound for the AI agent — browser/HTTP errors, UI drawings (sketch
mode), UI comments (floating-panel messages), design-mode events — reaches the
agent through **multiple uncoordinated paths**, none of which respect whether
the agent is mid-task:

- Errors: legacy `AlertHub` (`Deliver` → `MCPAlertSink`/`OverlayAlertSink`) is
  the default delivery path; the incident pipeline runs only when opted in.
- Browser/HTTP errors also auto-forward via
  `cmd/agnt/overlay.go::processAutoForwardEvent` — a parallel write that
  bypasses every gate (the forbidden pattern named in
  `.claude/skills/messaging-queue`).
- UI interactions (panel comments, sketches, design events) reach the agent via
  yet another route (overlay → PTY stdin / proxylog pull).

Two concrete failures:

1. **Spam during active work.** A down dependency throws `5xx` on many distinct
   URLs; each is delivered immediately and separately, interrupting the agent
   mid-response. The same lack of gating means a burst of UI comments or a
   sketch save can land mid-turn.
2. **No consolidation, no drain.** There is no single place that holds messages
   while the agent is busy, collapses duplicates, and releases them with a
   periodic summary the agent can drain when it is free.

## Goal

**One ordered, type-partitioned, activity-gated inbound queue** for all
agent-bound messages. Every message:

1. Enters as a **typed envelope** (`error`, `drawing`, `comment`, extensible).
2. Is **deduped/coalesced** within its type.
3. Is **held while the agent is busy** and **released when the agent is
   available**.
4. Surfaces via a **periodic heartbeat digest** summarizing all pending types,
   which the agent **drains** via a single pull tool (cursor-clear).

Errors are simply the first message type. The existing incident pipeline
(`internal/incident/`) provides the bones (inbox, activity detector, coalescer,
flow control, ping/digest) and is generalized to carry all types.

## Decisions (locked with user)

| Fork | Decision |
|------|----------|
| Queue structure | **One typed queue, type is the primary key.** Top-level lanes by message type: `error` (sub-banded by severity), `drawing` (FIFO), `comment` (FIFO), future types. One activity gate, one digest across all types. Incidents become `type=error`. |
| Availability signal | **Idle gate (base) + turn-boundary force-flush (bonus).** `ActivityDetector` output-idle window is the base gate for ALL agents; when Claude Code hook events are available, the `stop` hook force-flushes the queue at turn end. Falls back cleanly to idle-only for non-hook agents. |
| Storm collapse (error type) | **Coarse root-cause grouping** — a storm fingerprint keyed on `(source, status-class, proxyID)` merges a `5xx` flood from one proxy into one error entry (count + bounded sample URLs). |
| Periodic summary | **Heartbeat digest + cursor-clear.** While any lane has unread entries, one digest fires per interval (~30s, configurable); silent when empty. Drain via the pull tool advances the cursor + marks read; silent again until a new message arrives past the cursor. |
| Legacy cutover | **Split AlertHub.** Delete the alert-delivery path (Job A: `MCPAlertSink`, `OverlayAlertSink`, `Deliver`, `incidentPipeline` flag, drift metrics) and the `processAutoForwardEvent` parallel write. Keep the orthogonal plumbing (Job B: monitor `StreamSink`, hook fan-out, browser toasts, process-output streaming); rename `AlertHub` → `EventHub` for honesty. All agent-inbound delivery routes through the unified queue. |

## Terminology

- **Message** — one item bound for the agent. Carries a `Type` and a
  type-specific payload.
- **Lane** — the per-type partition in the queue. `error` lane is severity-banded
  (reusing today's 4 bands); `drawing`/`comment` lanes are bounded FIFO.
- **Availability gate** — the single decision point that holds or releases
  messages based on whether the agent is busy.
- **Digest** — the periodic compact notification summarizing pending messages
  across all lanes.
- **Drain** — the agent pulling pending messages via the tool, which advances
  the cursor and marks them read ("cleared").

## Message types (initial set)

| Type | Source(s) today | Lane shape | Notes |
|------|-----------------|-----------|-------|
| `error` | browser JS, http_4xx/5xx, transport, process crash/alert, build fail, etc. (the 11 incident adapters) | severity-banded (critical/error/warning/info) | Storm-collapsed by coarse fingerprint. |
| `drawing` | sketch mode (`LogTypeSketch`, `LogTypeSketchCapture`) | FIFO, bounded | A saved wireframe the agent should act on. |
| `comment` | floating panel (`LogTypePanelMessage`) | FIFO, bounded | A user message/question from the browser overlay. |
| `design` | design mode (`LogTypeDesignState/Request/Chat`) | FIFO, bounded | Optional in phase 1; may fold into `comment` initially. |

The set is open: adding a type means adding a lane config + an adapter, not new
gate/digest machinery.

## Architecture

### Pipeline (single path for every type)

```
signal (error adapter | UI interaction adapter)
   → typed MessageEnvelope { Type, Severity?, Fingerprint, Payload, Ctx }
   → Bus.Publish
   → sessionPipeline:
        dedup (per type+fingerprint)
        → Inbox lane insert/merge (error→severity band, others→FIFO)
   → Inbox.Subscribe delta
        → AvailabilityGate
             busy  → hold (stay in inbox, no push)
             avail → release
        → DigestEmitter (periodic heartbeat while unread, all lanes)
   → delivery sinks: MCP Log | claude/channel | PTY stdin
                     (whichever the session uses)
agent → get_inbox (pull) → advance cursor + mark read = drain
```

The incident pipeline's existing components map directly:

- `Inbox` → generalized to typed lanes (error band logic preserved; new FIFO
  lanes added).
- `ActivityDetector` → the AvailabilityGate base signal (already exists, already
  wired to `PingEmitter.ForceFlushCoalesce` on idle).
- `Coalescer` / `FlowController` → per-type coalesce + rate limit.
- `PingEmitter` → `DigestEmitter`: payload becomes a per-lane summary; add the
  timer-driven heartbeat (see below).
- `MPSCBus` / `sessionPipeline` → unchanged transport; envelope gains `Type`.

### Typed envelope

`IncidentEvent` generalizes (or is wrapped by) a `MessageEnvelope`:

```
MessageEnvelope {
    Type        MessageType   // "error" | "drawing" | "comment" | ...
    Severity    Severity      // meaningful for type=error only
    Fingerprint string        // dedup key, type-scoped
    Summary     string        // ≤200 bytes
    PayloadRef  *BlobRef      // large payloads in BlobStore
    Ctx         Context
    ReceivedAt  time.Time
}
```

`error`-type envelopes are produced by the existing 11 adapters (minimal change:
stamp `Type=error`). `drawing`/`comment` envelopes are produced by a new UI
interaction adapter fed from the proxy log types (`LogTypeSketch`,
`LogTypePanelMessage`, ...).

### Availability gate — idle base + turn-boundary flush

- **Base (all agents):** `ActivityDetector` marks the agent busy while PTY output
  is flowing / within an idle window (existing 500ms→3s params). While busy, the
  gate holds releases (messages remain inboxed; no push, no PTY injection). On
  idle, the gate releases and triggers a digest.
- **Bonus (Claude Code):** the `stop` hook (already drained in
  `internal/daemon/hub_hook.go`) force-flushes the queue at turn end — a precise
  "agent is between turns" signal. `user-prompt-submit` marks busy. When hooks
  are absent, only the idle base applies.
- Critical-severity errors keep today's bypass (immediate emit) — a crash should
  not wait for idle. All other types respect the gate.

### Storm collapse (error lane)

`adapter_http.go::FromHTTPEntry`: for 4xx/5xx compute a **storm fingerprint** from
`(source, status-class, proxyID)` — drop the URL from both the canonical message
and the location, and fold `proxyID` in explicitly (today `computeFingerprint`
does not include proxyID). All same-class errors from one proxy merge into one
`error` entry; `Count` accumulates via the existing dedup/inbox merge. Keep a
**bounded distinct-URL sample set** (cap ~10 + `DistinctCount`) on the entry for
rendering `47x across 12 URLs`; full list via `get_inbox ... raw=true`.

### Heartbeat digest + cursor-clear

- `DigestEmitter` owns a `time.Ticker` at the configured interval. On each tick,
  if any lane has unread entries, emit ONE digest (compact per-lane summary:
  `pending: 3 errors, 1 drawing, 2 comments`); if nothing unread, emit nothing.
- The digest supplements event-driven coalesced pushes (a new message still
  pushes immediately when the agent is available). The heartbeat is the standing
  "still pending" beat.
- Drain: `get_inbox` advances the cursor + marks pulled messages read. After a
  full drain the digest goes silent until a new message (new fingerprint, new
  FIFO item, or escalation) arrives past the cursor. Backlog alone never
  re-spams.

### Tools

- `get_inbox` — unified pull (supersedes `get_incidents`). Params: `type`
  filter, `severity` (error lane), `cursor`, `limit`, `raw`. Advances cursor +
  marks read. Compact output groups by type.
- `get_incidents` — kept as a thin alias/shim over `get_inbox type=error` for
  back-compat.
- `get_errors` — read-only shim over the error lane (legacy/daemon-less mode
  retained).

### Legacy split (AlertHub → EventHub)

**Delete (Job A — the alert-delivery bug):**
- `MCPAlertSink`, `OverlayAlertSink`, `AddMCPSink`/`RemoveMCPSink`,
  `SetOverlaySink`, `Deliver`, `incidentPipeline` flag, `SetIncidentPipeline`,
  drift metrics (`oldPathCount`/`newPathCount`/`IncrNewPath`/`DriftMetricsSnapshot`).
- `config.AlertsConfig.IncidentPipeline` + `IncidentPipelineEnabled()` + the
  `tools` mirror `incidentPipelineConfig`.
- `cmd/agnt/overlay.go::processAutoForwardEvent` and `lastForwardNs` (the
  parallel write); browser/HTTP errors now flow only through the queue.
- `internal/proxy/ws_handler.go::NotifyBrowserError` parallel overlay write
  (re-point to the bus adapter).

**Keep, rename `AlertHub` → `EventHub` (Job B — orthogonal plumbing):**
- `streamSinks` + `AddStreamSink`/`RemoveStreamSink`/`BroadcastLogEntry`/
  `BroadcastProcessOutput` → `agnt monitor`.
- `hookSinks` + `BroadcastHookEvent`/`EmitHookEvent` → `agnt hook` fan-out.
- `proxyBroadcaster` + `BroadcastToast` → outbound browser toasts
  (`channel_reply`, stop/notification hooks).
- `RegisterProxyPath`/`proxyPaths` → stream routing.

**Synchronous warnings** currently sent via `Deliver` (port-conflict in
`port_preflight.go`, shutdown messages in `daemon_shutdown.go`) re-home as
`error`-type envelopes published to the bus (or a minimal direct toast) — they
must not silently vanish (Silent Failure Prohibition).

### Cross-process delivery note

The daemon owns the inbox; the agent reaches it through one of three session
surfaces, each already represented as a Pinger sink param in
`MPSCBus.AddSession(sessionID, mcpNotify, channelNotify, ptyInject)`:

- `agnt mcp` process → MCP `session.Log()` notifications (`mcpNotify`).
- channel mode → `claude/channel` notification (`channelNotify`).
- `agnt run` → PTY stdin injection (`ptyInject`).

Today `AddSession` is called with `nil` sinks in `hub_session.go` (pull-only).
This work wires the real sinks per session surface so the periodic digest is
actually pushed. Pull (`get_inbox`) remains the authoritative drain.

## Config

```kdl
alerts {
    // REMOVED: incident-pipeline (queue is unconditional)
    // RETAINED unchanged: push { ... }, ping { ... }, blob-budget, outage-hold

    digest {
        enabled true        // heartbeat digest on/off (default true)
        interval 30000      // ms between digests while any lane unread (default 30s)
    }

    lanes {                 // optional per-type tuning; sensible defaults if omitted
        drawing { capacity 50 }
        comment { capacity 50 }
    }
}
```

Unknown legacy keys (`incident-pipeline`) must be tolerated on parse (ignored +
one-line warning), not fatal, so existing `.agnt.kdl` files keep loading
(`config-contracts.md`: validate, but don't break back-compat for a removed
flag).

## Error handling

- Bus overflow: unchanged (drop-newest at 4096, `OverflowCount`).
- Error band overflow: unchanged (LRU per band, `Stats.Dropped`).
- FIFO lane overflow: bounded; drop-oldest with a dropped counter (a flood of
  comments/sketches must not OOM).
- Storm sample-set overflow: increment `DistinctCount`, never grow past cap.
- Digest tick during teardown: guarded by `stopCh`; no emit after
  `RemoveSession`.
- All delivery sinks non-blocking (channel-send-with-default); a slow consumer
  never stalls the inbox drain or the digest ticker.

## Testing

- **Type partition:** error/drawing/comment envelopes land in distinct lanes;
  `get_inbox type=comment` returns only comments.
- **Storm collapse:** 200 distinct-URL 5xx on one proxy → one error entry,
  `Count==200`, sample ≤ cap, `DistinctCount` correct; storms on two proxies →
  two entries.
- **Availability hold:** messages arriving while `ActivityDetector` is busy are
  held (no push); released on idle. With a `stop` hook, queue force-flushes at
  turn end.
- **Critical bypass:** a `critical` error emits immediately even while busy.
- **Heartbeat:** persistent unread backlog → one digest per interval, not N;
  silent after `get_inbox` drains; re-arms only on a new message.
- **Single delivery:** ten identical events in 100ms → one delivery.
- **No legacy path:** build/grep proves `MCPAlertSink`/`OverlayAlertSink`/
  `Deliver`/`processAutoForwardEvent` are gone; no caller references removed
  symbols.
- **Monitor parity:** `agnt monitor --types ... --severity ...` unchanged
  pre/post `EventHub` rename.
- **Toast + hook parity:** `channel_reply`, stop/notification toasts, and
  `agnt hook` fan-out still work.
- **Config back-compat:** `.agnt.kdl` with `incident-pipeline true` still loads.

## Out of scope (phase 1)

- New signal sources beyond the existing 11 error adapters + the sketch/comment
  UI adapter.
- Inbox persistence across daemon restarts (best-effort, unchanged).
- Outage-suppression mode (we collapse, not suppress).
- Rich design-mode lane (folds into `comment` initially).

## Migration / blast radius (ordered by risk)

1. **AvailabilityGate generalization** — holding ALL types (not just non-critical
   errors) on the idle/turn signal; correctness of the hold/release is the core.
2. **`StreamSink` / monitor** under the `EventHub` rename (parity test first).
3. **Channel-mode + PTY sink wiring** in `AddSession` (forked go-sdk `Notify`).
4. **UI interaction adapter** — routing sketch/panel events into the queue
   instead of the old overlay→PTY path.
5. **Storm fingerprint + sample set** (smallest, self-contained).

Each risky step lands behind a parity/behavior test before the corresponding
legacy code is deleted.
