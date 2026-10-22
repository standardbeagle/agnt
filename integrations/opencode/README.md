# opencode-agnt

Shows agnt incident pings in opencode, exposes the inbox as tools, and keeps unread incidents across compaction.
Design: `docs/plans/2026-08-07-opencode-plugin-integration.md`. No fork of opencode is needed.

The plugin never injects text into a session. agnt sends each ping down the one connector the
session was started with: under `agnt run opencode` the daemon types it to the agent. The plugin
is the display and the pull side.

## Use

```bash
agnt run opencode          # sets AGNT_SESSION_CODE, which the plugin requires
```

`.opencode/plugins/agnt.ts`, or in `opencode.json`:

```json
{ "plugin": [["opencode-agnt", { "severity": "warning" }]] }
```

Without `AGNT_SESSION_CODE` the plugin logs a warning and stays inactive.

## Behaviour

- **Toasts.** Each digest at or above `severity` from `agnt incidents watch` (NDJSON, reconnects
  through daemon restarts) is shown as a toast. It is pinned to this agent's session, so two agents
  in one checkout do not see each other's digests. Forwarding-pause and `alerts.push` are applied by
  the daemon first.
- **Tools.** `agnt_incidents` (preview, never marks read; pages with `since`) and
  `agnt_forwarding` (`pause` / `resume`).
- **Compaction.** The unread incidents and the cursor are added to the compaction context.

## Options

| Option | Default | |
|---|---|---|
| `severity` | `warning` | lowest severity shown |
| `bin` | `$AGNT_BIN` or `agnt` | binary |

## Unverified against a live opencode

The hook names (`experimental.session.compacting`) and `tui.showToast` come from the opencode
docs; check them on the opencode version you run.

## Test

`npm test` runs the helper unit tests (Node 22.6+ type stripping).
