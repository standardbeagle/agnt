# Dismissable Silent-Failure Notices on Overlay Overview

Date: 2026-06-07

## Problem

When a config-declared resource fails to start, the failure is invisible on the
overlay overview. The triggering case: a proxy with `bind "0.0.0.0"` but no
`allow-external true` fails creation with

```
binding to 0.0.0.0 exposes the proxy to the network; set allow_external: true to confirm
```

Both proxy-creation paths (URL-detection and the 30s fallback) hit the guard.
The `dev` script runs fine, so the overview shows a healthy "running" script and
zero proxies — no signal that the proxy the user declared never came up. The
failure is recorded in the startup-error store but is not surfaced as anything
the developer notices.

This violates the daemon's Silent Failure Prohibition: a config-declared
resource that fails to start must surface a visible signal to the developer, not
only a log entry.

## Goal

Surface silent-failure-prohibition events (config-declared resources that failed
to start) as a dismissable notice banner on the overlay overview panel.

## Decisions

- **Surface**: overlay overview panel (the PTY terminal UI the developer sees).
- **Scope**: all silent-failure-prohibition events — proxies, script-start
  failures, port conflicts, dependency failures.
- **Dismiss**: session-only, in-memory. Lost on daemon/overlay restart. A
  resolved-then-recurring failure re-shows.

## Architecture

Approach C — hybrid:

- **Daemon** computes the authoritative notice list. It owns the
  silent-failure-prohibition contract, the `.agnt.kdl` config authority, and the
  startup-error store that already records both failure and success events. So
  notice *computation* and resolve-tracking live daemon-side.
- **Overlay** owns the ephemeral dismiss set (session-only UI state) and renders
  the banner. Dismissal is pure UI state and belongs above the import boundary
  (overlay cannot import daemon).

No new protocol verb. Notices piggyback on the existing `ALERTS STARTUP-LOG`
response, which the overview already polls each tick and which already carries
both the failure and the resolving success events.

### Data flow

```
startupErrorStore.Query(filter)  →  buildNotices(entries)  →  response {entries, count, notices}
   (daemon, project-scoped)          (pure reduction)            (hubHandleStartupLog)
        →  fetchStartupLog (overlay)  →  Status.Notices  →  dismiss filter  →  banner
```

## Notice model

Daemon-side type, JSON-bridged to an overlay-side mirror (the same pattern as
`StartupLogEntry`, which is defined in both `internal/daemon/startup_errors.go`
and `internal/overlay/overlay.go`). No shared import; bridged via a JSON DTO.

```go
// internal/daemon/notices.go
type Notice struct {
    ID          string    // fingerprint: "<domain>:<process_id>", e.g. "proxy:space-f4a4:dev"
    Domain      string    // "proxy" | "script" | "port"
    Severity    string    // "error" | "warning"
    Resource    string    // "dev"
    Summary     string    // `proxy "dev" not created`
    Detail      string    // full startup-log message
    Remediation string    // actionable hint, may be empty
    EventType   string    // proxy_creation_failed, ...
    Timestamp   time.Time
}
```

## buildNotices: pure reduction

`buildNotices(entries []StartupLogEntry) []Notice` is a pure function over the
project-scoped entries the handler already queried. No daemon state, no mocks —
fully table-testable.

### Classification table

`event_type → {domain, role, severity}`:

| event_type | domain | role | severity |
|------------|--------|------|----------|
| `proxy_creation_failed` | proxy | failure | error |
| `proxy_failed` | proxy | failure | error |
| `proxy_skipped` | proxy | failure | warning |
| `startup_proxy_fallback_failed` | proxy | failure | warning |
| `proxy_event_dropped` | proxy | failure | warning |
| `proxy_started` | proxy | success | — |
| `startup_proxy_fallback_used` | proxy | success | — |
| `failed` | script | failure | error |
| `start_failed` | script | failure | error |
| `started` | script | success | — |
| `script_started` | script | success | — |
| `port_conflict` | port | failure | warning |

Unknown `event_type`s are ignored (not all startup-log events are notices).

### Resolve-pairing

Keyed by `(process_id, domain)`. This split is required because a proxy and its
script share a `process_id` (`space-f4a4:dev`) — a *script* success must not
resolve a *proxy* failure.

For each `(process_id, domain)` group:
- Find the latest failure entry `F` (by timestamp) among failure roles.
- Find the latest success entry `S` among success roles.
- Emit a notice from `F` only if `S` is absent or `S.timestamp < F.timestamp`.

Consequences:
- Dedup is inherent: when both the URL-detection path and the fallback fail for
  the same proxy, two failure entries collapse to one notice (latest wins).
- Auto-resolve is inherent: once the resource later succeeds, the notice
  disappears from the daemon's output.

### Remediation derivation

From `event_type` + message substring:
- message contains `allow_external` → `Add allow-external true to the proxy block in .agnt.kdl, or change bind to localhost`
- `proxy_skipped` → `Fix the upstream script that failed`
- `port_conflict` → `Free the port or run :kill-port <port>`
- otherwise → empty (the banner shows `Detail` alone)

### Notice ID

`"<domain>:<process_id>"`. Stable per resource+domain, so a re-firing identical
failure keeps the same ID (stays dismissed); a resolve removes it from the
active set; a later recurrence reuses the ID, and the overlay's prune-on-resolve
rule re-shows it.

## Overlay: dismiss state

- `Overlay.dismissedNotices map[string]bool`, guarded by the existing overlay
  lock.
- Each render: visible notices are those with `!dismissed[ID]`. Prune dismissed
  IDs that are absent from the current active set — so a resolved-then-recurring
  failure (same ID) re-shows, and the map does not grow unbounded.
- Dismiss is overlay-local — no daemon round-trip.

## Overlay: rendering

A banner block at the top of `drawOverviewContent` (`internal/overlay/render.go`),
above the ports/scripts sections. Collapses to nothing when there are no visible
notices.

```
⚠ 1 issue · :dismiss <n> · :dismiss-all
 [1] proxy "dev" not created
     ↳ bind 0.0.0.0 needs allow-external true in .agnt.kdl
```

- Error severity → red icon; warning → yellow.
- Index `[n]` matches the `:dismiss <n>` argument.
- Cap at 3 visible notices; surplus shown as `+k more`.
- Width and row count clamped to the panel, consistent with the existing
  overview sections.

## Overlay: input

Add `dismiss <n>` and `dismiss-all` to `paletteCommands`
(`internal/overlay/command_palette.go`). `InputRouter.dispatchPaletteCommand`
(`internal/overlay/input.go`) handles both locally — they mutate
`dismissedNotices` and need no `ScriptController` / daemon call. `<n>` is the
1-based index shown in the banner.

## Testing

- `internal/daemon/notices_test.go` (pure, dense asserts, no sleeps):
  - failure with no success → one notice;
  - failure then later success → no notice;
  - proxy-vs-script domain isolation on a shared `process_id`;
  - remediation mapping for each derivation branch;
  - severity per event_type;
  - latest-failure dedup (two proxy failures → one notice);
  - empty input → empty output;
  - unknown event_type ignored.
- overlay tests:
  - dismiss filter hides a notice by ID;
  - prune-on-resolve lets a recurring ID re-show;
  - dismiss-all clears all visible;
  - render includes the banner when notices present, omits it when empty.

## Files

New:
- `internal/daemon/notices.go`
- `internal/daemon/notices_test.go`

Edited:
- `internal/daemon/hub_alerts.go` — add `notices` to the startup-log response
- `internal/overlay/overlay.go` — `NoticeInfo` type, `Status.Notices`,
  `dismissedNotices` map + methods
- `internal/overlay/status.go` — decode `notices` in `fetchStartupLog`
- `internal/overlay/render.go` — notice banner in `drawOverviewContent`
- `internal/overlay/command_palette.go` + `input.go` — `dismiss` / `dismiss-all`
- overlay tests
- `docs/overlay-internals.md` — document the notice banner

## Scope / YAGNI

- No persistence of dismissals.
- No new protocol verb — piggyback the startup-log response.
- No modal UI — reuse the command palette.
- Notices are a *view* over existing startup-log data; nothing new is logged.

## Out of scope

- Browser-injected notices (the proxy failed, so there is no page to inject into).
- Cross-session or persisted dismissal.
- Surfacing notices through MCP tools or the incident pipeline.
