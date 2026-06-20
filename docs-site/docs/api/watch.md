---
sidebar_position: 14
---

# watch

Returns a ready-to-run `agnt monitor` command string for streaming daemon events to your terminal in real time. MCP clients know the daemon socket path; this tool bridges them to the [`agnt monitor`](#the-agnt-monitor-cli) CLI so you can tail errors, interactions, or process output live.

`watch` does not stream events itself — it hands you the exact command to run.

## Synopsis

```json
watch {}
watch {target: "errors", proxy_id: "dev"}
watch {target: "process", process_id: "app"}
```

## Parameters

| Parameter | Type | Required | Default | Description |
|-----------|------|----------|---------|-------------|
| `target` | string | No | `all` | What to watch: `errors`, `interactions`, `process`, `all` |
| `proxy_id` | string | No | none | Filter to a specific proxy |
| `process_id` | string | No | none | Filter to a specific process (**required** for `target: "process"`) |

## Targets

| Target | Streams | Required params |
|--------|---------|-----------------|
| `errors` | Error and diagnostic events | optional `proxy_id` |
| `interactions` | User interactions — panel messages, clicks, sketch | optional `proxy_id` |
| `process` | A process's output stream (follow mode) | `process_id` |
| `all` | All daemon events (default) | none |

## Output

Returns a `command` string and a human-readable `description`:

```json
{
  "command": "agnt monitor --socket /tmp/devtool-mcp-1000.sock --types error,diagnostic --format compact",
  "description": "Stream error and diagnostic events from all proxies"
}
```

Run the `command` in a terminal to start the live stream.

## The `agnt monitor` CLI

```bash
agnt monitor                           # All events
agnt monitor --types error,diagnostic  # Errors only
agnt monitor --proxy dev --format json # NDJSON for a specific proxy
agnt monitor --process app             # Process output, follow mode
```

Flags: `--types`, `--proxy`, `--process`, `--severity`, `--format` (compact/json), `--socket`. The stream auto-reconnects on daemon restart and exits cleanly on SIGINT/SIGTERM.

## See Also

- [get_errors](/api/get_errors) — pull-based error snapshot (vs this push stream)
- [proxylog](/api/proxylog) — query the historical traffic log
- [proc](/api/proc) — process management
