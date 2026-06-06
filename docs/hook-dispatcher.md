# Hook Dispatcher (`agnt hook`) — Telemetry Forwarder

The `agnt hook` subcommand pushes Claude Code (or any agent) hook events into the
daemon's lock-free ring buffer for fan-out. This is the telemetry-forward path.
For the Bash-interceptor side (`agnt hook check-bash` / `check-prompt`) see
[hook-rules.md](hook-rules.md).

Dispatcher = fire-and-forget alias designed to install in `~/.claude/settings.json` hook entries: must never break agent loop, must complete in single-digit ms even when daemon wedged, must always exit 0 on any transient failure (daemon down, deadline exceeded, payload errors). Only non-zero exit = `--message`/arg validation.

**Cost contract**: p99 cold exit ≤5ms, measured ~270µs in `cmd/agnt/hook_bench_test.go`. Dispatcher uses 50ms hard deadline on daemon round-trip, opens dedicated short-lived client (no shared state), writes entire enqueue path daemon-side as single mutex push into 1024-slot ring buffer.

## Supported events (Claude Code hook nomenclature)

| Event | When fired | Drain-side behavior |
|-------|-----------|---------------------|
| `pre-tool-use` | Before each tool call | StreamSink + heartbeat |
| `post-tool-use` | After each tool call | StreamSink + heartbeat |
| `notification` | On `notify`-style messages | StreamSink + heartbeat + per-proxy `BroadcastToast` (payload type/title/message) |
| `stop` | When agent finishes responding | StreamSink + heartbeat + per-proxy `BroadcastToast` (`success`/"Claude Finished"/`last_assistant_message`, suppressed when `stop_hook_active=true`) |
| `stop-failure` | When turn ends due to API error (Claude Code's `StopFailure` event) | StreamSink + heartbeat + per-proxy `BroadcastToast` (`error`/"Claude Error"/`error` + `error_details`) |
| `subagent-stop` | When subagent stops | StreamSink + heartbeat |
| `user-prompt-submit` | On user prompt submission | StreamSink + heartbeat |
| `session-start` | Session start | StreamSink + heartbeat |
| `session-end` | Session end | StreamSink + heartbeat |
| `pre-compact` | Before context compaction | StreamSink + heartbeat |

Any other event name enqueued and fanned out same way; table above = canonical Claude Code set, not whitelist. Custom event names work transparently.

## Drain fan-out (cheapest-first order — see `drainHooks` → `fanOutHookEvent` in `internal/daemon/hub_hook.go`)

1. Session heartbeat (in-memory `LastSeen` bump on SessionRegistry — hook traffic counts as proof-of-life for parent `agnt run` session)
2. StreamSink fan-out as synthetic `LogEntry{Type: hook}` so `agnt monitor --types hook` streams events live
3. If event = `notification`, decode payload as `{type, title, message, duration}` and call `BroadcastToast` on every active proxy (back-compat for legacy `agnt notify` path)
4. Typed `HookEventSink` fan-out via `BroadcastHookEvent` for direct subscribers (overlay panel, future MCP push)

Drain goroutine never blocks on slow consumer: `BroadcastLogEntry` uses channel-send-with-default, `BroadcastToast` errors swallowed per-proxy, any malformed payload short-circuits at decode step. If consumer stalls hard enough to wedge fan-out, ring buffer overflow kicks in and `hookRing.OverflowCount()` surfaces pressure.

## Sample `~/.claude/settings.json`

```json
{
  "hooks": {
    "preToolUse": [
      { "type": "command", "command": "agnt hook pre-tool-use --session-id $CLAUDE_SESSION_ID --project-path $PWD" }
    ],
    "postToolUse": [
      { "type": "command", "command": "agnt hook post-tool-use --session-id $CLAUDE_SESSION_ID --project-path $PWD" }
    ],
    "notification": [
      { "type": "command", "command": "agnt hook notification --session-id $CLAUDE_SESSION_ID" }
    ],
    "stop": [
      { "type": "command", "command": "agnt hook stop --session-id $CLAUDE_SESSION_ID" }
    ]
  }
}
```

## Streaming hook events live

```bash
agnt monitor --types hook                # All hook events, compact
agnt monitor --types hook --format json  # NDJSON for jq pipelines
```
`--severity` filter no-op for hook events; type filter = active discriminator. Provenance hints (session ID, agent name, project path) included in `Location` field of JSON output so jq pipelines correlate events without unwrapping payload.

## `agnt notify` compatibility

`agnt notify --message "hi"` preserved as thin alias for `agnt hook notification`. Marshals `{type, title, message}` and calls `HookSend("notification", ...)`. Daemon-side drain handles per-proxy `BroadcastToast` loop, so browser surface identical to legacy impl. Client-side per-proxy iteration that lived in `cmd/agnt/notify.go` removed in phase 3.
