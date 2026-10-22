# opencode-agnt

Pushes agnt incident digests into an opencode session and exposes the inbox as tools.
Design: `docs/plans/2026-08-07-opencode-plugin-integration.md`. No fork of opencode is needed.

## Use

```bash
agnt run opencode          # sets AGNT_SESSION_CODE, which the plugin requires
```

`.opencode/plugins/agnt.ts`, or in `opencode.json`:

```json
{ "plugin": [["opencode-agnt", { "severity": "warning", "resumeOnIdle": true }]] }
```

Without `AGNT_SESSION_CODE` the plugin logs a warning and stays inactive.

## Behaviour

- **Source.** `agnt incidents watch` (NDJSON, reconnects through daemon restarts). It is pinned
  to this agent's session, so two agents in one checkout do not see each other's digests.
- **Gates.** Severity floor, burst coalescing (`coalesceMs`), dedup (`dedupMs`), and activity
  defer. Forwarding-pause and `alerts.push` are applied by the daemon before a digest arrives.
- **Delivery.** While the agent is busy a batch is injected as context only (`noReply`), so the
  running loop sees it with no extra model call. When idle it starts a turn unless
  `resumeOnIdle` is false. The target is the most recently active root session; subagent
  sessions are never targets.
- **Tools.** `agnt_incidents` (preview, never marks read; pages with `since`) and
  `agnt_forwarding` (`pause` / `resume`).
- **Compaction.** The unread incidents and the cursor are added to the compaction context.

## Options

| Option | Default | |
|---|---|---|
| `severity` | `warning` | lowest severity pushed |
| `coalesceMs` | `2000` | burst window |
| `dedupMs` | `30000` | identical batch suppression |
| `resumeOnIdle` | `true` | start a turn on an idle session |
| `bin` | `$AGNT_BIN` or `agnt` | binary |

## Unverified against a live opencode

The hook names and `session.prompt({ noReply })` come from the opencode docs. `promptAsync` and
the `synthetic` part field are not in the SDK docs: the plugin uses `promptAsync` when the SDK
has it and a non-awaited `prompt` otherwise, and sends `synthetic` best-effort. Check both on
the opencode version you run.

## Test

`npm test` runs the queue/delivery unit tests (Node 22.6+ type stripping).
