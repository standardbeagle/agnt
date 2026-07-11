# Walkthrough: The Incident Pipeline — intermittent 500s and a TypeError storm

## What it is

An opt-in alert path that replaces direct alert-sink dispatch with a
normalized, deduped, priority-ordered **incident inbox**. Signal sources
(browser JS errors, HTTP 4xx/5xx, transport errors, proxy diagnostics, process
alerts, process crashes, build failures, port conflicts) flow through a bus,
are deduplicated and coalesced, land in a four-band inbox, and are pulled by the
agent through the `get_incidents` MCP tool with a resumable cursor and
remediation hints.

```
Signal sources → Bus → Dedup/Coalesce/FlowControl → Inbox → Pinger → MCP/channel/PTY
```

Enable it per project in `.agnt.kdl`:

```kdl
alerts {
    incident-pipeline true
}
```

Source of truth: `internal/incident/` (bus, dedup, inbox, ping, envelope),
`internal/tools/get_incidents.go` (the MCP tool),
`.claude/rules/daemon-architecture.md` § Incident Pipeline (numbered contracts),
`docs/configuration.md` (config keys).

## Why it is unique

Without the pipeline, a burst of errors reaches the agent as an unordered spam
of near-duplicate alerts — the same TypeError fired 200 times drowns the one
critical process crash. The pipeline fixes this structurally:

- **Priority bands.** Four bands — `critical` / `error` / `warning` / `info` —
  each hard-capped at 100 entries. A crash never gets buried under a warning
  storm.
- **Dedup + coalesce.** Identical signals collapse into one incident with a
  `count`; a coalesce window (default 200ms) batches bursts.
- **Cursor-based resumable pulls.** Each pull returns a `replay_cursor`; the
  next pull with `since:<cursor>` resumes exactly where you left off.
- **Remediation built in.** Each incident carries a `next` tool call and a
  `skill` hint; the tool aggregates a dominant skill and deduped tool set across
  the page.
- **Per-session hard isolation.** Each session gets its own pipeline; events
  from session A never appear in session B's inbox, even for the same project.
  This is why `get_incidents` intentionally has no cross-project `global` flag —
  isolation is a stronger guarantee than project scoping.

## Real-world scenario

A dev server is throwing intermittent HTTP 500s on one API route, and the
frontend — reacting to the empty/blank responses — is spewing a
`TypeError: Cannot read property 'map' of undefined` on every re-render. In the
raw stream these two signals are tangled: hundreds of identical TypeErrors bury
the intermittent 500s that are the actual root cause. You want the crash-class
signal ranked first, the TypeError storm collapsed to one line with a count, and
a pointer to the next tool for each.

## Step by step

### 1. Enable the pipeline

Add to `.agnt.kdl` (config is reconciled live, but the flag is all-or-nothing
*per session* — a session that connected with it `false` uses the legacy path
for its whole lifetime, so start a fresh `agnt run` / session after flipping it):

```kdl
alerts {
    incident-pipeline true
    blob-budget 16777216   // per-session BlobStore cap in bytes (default 16MB)
    ping {
        mcp-notifications true
        pty-injection false
        channel true
    }
}
```

### 2. Pull the inbox

```
get_incidents {}
```

Expected compact output (from `get_incidents.go` `formatIncidentsCompact`):

```
=== Incidents (2) === [inbox: crit=1 err=1 warn=0 info=0 new=2]

[critical:process_crash] panic (2x, 3s ago)
  runtime error: index out of range
  payload: goroutine 1 [running]: main.serve(...)   // only when detail:"full"
  next: proc action=output process_id=agnt-dev
  skill: agnt-process-proxy

[error:browser_js] TypeError (200x, 8s ago)
  Cannot read property 'map' of undefined
  → http://localhost:3000/list
  next: proxy action=exec code=window.__devtool.getElementInfo(selector)
  skill: agnt:browser-debug

=== Next ===
tool: proc action=output process_id=agnt-dev
skill: agnt-process-proxy
replay_cursor: 2026-07-06T01:20:00Z
```

Note the TypeError storm is a *single* incident with `200x` — dedup collapsed it.
The `critical` band is rendered first. The header shows the post-pull band
counts.

### 3. Focus on what matters — filter by severity

```
get_incidents {severity: ["critical","error"]}
```

Drops the `warning`/`info` noise. `sources` filters by origin, e.g. only the
server-side failures:

```
get_incidents {sources: ["http_5xx","process_crash"]}
```

Valid source tokens (from `GetIncidentsInput`): `browser_js`, `http_5xx`,
`http_4xx`, `transport_err`, `proxy_diag`, `process_alert`, `process_crash`,
`build_fail`, `port_conflict`, `shutdown`, `hook_stop_failure`.

### 4. Hydrate the full payload for the crash

The compact view truncates. Pull the full blob for the crash's stack trace:

```
get_incidents {detail: "full", severity: ["critical"]}
```

`detail:"full"` hydrates the payload from the per-session blob store. The
payload now renders in compact mode too (not only under `raw:true`).

### 5. Drain with the cursor

To resume from where the last pull ended and mark those incidents read:

```
get_incidents {since: "2026-07-06T01:20:00Z", mark_read: true}
```

`since` accepts an RFC3339 cursor from a prior pull *or* a duration like `"5m"`
(resolved to an absolute timestamp tool-side before it reaches the hub, which
parses `Since` strictly as RFC3339). `mark_read:true` advances the cursor and
marks the returned incidents read. You must keep polling with the returned
cursor to drain the inbox before a band wraps at 100 entries.

### 6. Full JSON when you need the structured fields

```
get_incidents {raw: true, limit: 50}
```

Returns the `GetIncidentsOutput` JSON: `incidents[]` (with `fingerprint`,
`first_seen`/`last_seen`, `count`, `severity`, `source`, `category`, `summary`,
`context`, `remediation`, `read`), `inbox_after` band counts, `replay_cursor`,
`next_tools`, `next_skills`, `truncated`, and `pipeline_enabled`.

## Gotchas

- **`pipeline_enabled:false` + zero incidents ≠ clean inbox.** It means the
  pipeline is off for this session. The tool distinguishes the two states
  explicitly (compact mode prints "incident pipeline not enabled for this
  session") — do not read an empty result as "all healthy" without checking the
  flag.
- **The flag is all-or-nothing per session.** Flipping `incident-pipeline` mid
  session does nothing for that session; it uses whichever path it connected
  with for its entire lifetime. Start a new session to switch.
- **Bands wrap; the inbox is a cache, not a log.** Each band holds at most 100
  entries and evicts oldest-first. An incident present in the inbox does not mean
  the underlying event is still active — re-fetch from the source subsystem to
  confirm. Poll with the cursor to drain before wrap.
- **Blob store is best-effort.** A `payload`/`BlobRef` may resolve to nothing if
  the blob was evicted before you pulled it (16MB per-session LRU, never
  persisted to disk, gone on daemon restart). `detail:"full"` returning no
  payload is not an error.
- **Bus drops the *newest* on overflow.** Under extreme load (4096-slot channel
  full) the incoming event is dropped, not the oldest — latency stays bounded at
  the cost of the most recent event. The `dropped` count surfaces in
  `inbox_after`.
- **Dedup is per-session, not per-project.** The same fingerprint in two
  sessions produces two incidents. This is deliberate — cross-session dedup
  would violate the isolation contract.
- **`get_incidents` supersedes `get_errors`.** When the pipeline is enabled,
  `get_incidents` is authoritative and `get_errors` becomes a shim. Prefer
  `get_incidents`.
- **The coalesce window is set at construction.** Default 200ms, not
  reconfigurable at runtime — a daemon restart is required to change it.
