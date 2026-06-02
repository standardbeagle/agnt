# Channel Mode (Beta — Claude Code only)

> **Beta / Experimental**: Channel mode uses `github.com/standardbeagle/go-sdk` (a fork of `modelcontextprotocol/go-sdk` that adds `ServerSession.Notify`) and the `--dangerously-load-development-channels` flag in Claude Code. Protocol, schema, tool shapes may change before stabilization.

Push-based event forwarding via MCP `claude/channel` protocol. When enabled, daemon streams browser errors, diagnostics, user interactions directly into Claude's context as `<channel>` events -- no PTY wrapper or `agnt run` required.

## When to use channel mode vs `agnt run`

| | Channel mode | `agnt run` |
|--|-------------|------------|
| Works with | Claude Code v2.1.80+ | Any terminal agent |
| Event delivery | Push (real-time XML tags in context) | Pull (poll `get_errors`, `proxylog`) or PTY stdin injection |
| Setup | Add `channel { enabled true }` to `.agnt.kdl` | Wrap agent: `agnt run claude` |
| Browser overlay | Yes (via `channel_reply` tool) | Yes (via PTY indicator) |
| Login requirement | claude.ai account (Console/API key not supported) | None |

## Enabling

1. Add `channel` block to `.agnt.kdl`:

```kdl
channel {
    enabled true              // required to activate
    events "error" "diagnostic" "interaction"  // allowlist; omit for all types
    severity "warning"        // minimum severity to forward
    dedupe-window 2000        // per-event deduplication window (ms)
    reply-tool true           // register channel_reply MCP tool
}
```

| Field | KDL key | Type | Default | Description |
|-------|---------|------|---------|-------------|
| Enabled | `enabled` | bool | `false` | Activate channel event forwarding |
| Events | `events` | string list | (all) | Allowlist of event types: `error`, `diagnostic`, `interaction`, `http`, `custom`, `panel_message`, `responsive_request` |
| Severity | `severity` | string | `"warning"` | Minimum severity: `trace`, `debug`, `info`, `warning`, `error` |
| DedupeWindow | `dedupe-window` | int | `2000` | Per-event dedup window in ms; `0` disables |
| ReplyTool | `reply-tool` | bool | `true` | Register the `channel_reply` MCP tool |

2. During research preview, Claude Code must launch with development-channels flag:

```bash
claude --dangerously-load-development-channels server:agnt
```

Normal MCP entry (`claude mcp add agnt -s user -- agnt mcp`) unchanged. `--dangerously-load-development-channels` flag tells Claude Code to process `claude/channel` capability and render `<channel>` events in context.

## Event shape

Events arrive as XML-like tags injected into Claude's context:

```xml
<channel source="agnt" type="error" proxy="dev" severity="error">
TypeError: Cannot read property 'map' of undefined
  at ProductList (src/components/List.tsx:42:15)
</channel>
```

| Meta key | Description |
|----------|-------------|
| `source` | Always `"agnt"` |
| `type` | Event type: `error`, `diagnostic`, `interaction`, `process`, `panel_message`, `sketch`, `design`, `responsive_request` |
| `proxy` | Agnt proxy ID (stable per dev server) |
| `severity` | `trace`, `debug`, `info`, `warning`, `error` |

## `channel_reply` tool

When `reply-tool` enabled (default), `channel_reply` MCP tool registered for sending messages from Claude to developer's browser overlay:

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | yes | Message body (markdown OK) |
| `title` | string | no | Toast title |
| `severity` | string | no | Toast style: `info` (default), `warning`, `error` |
| `proxy_id` | string | no | Target specific proxy; omit to fan out to all active proxies |

Returns `{ "delivered": N, "message": "..." }` with count of proxies that received toast.

```json
channel_reply {content: "Build succeeded, opening preview..."}
channel_reply {content: "Which layout?", title: "Choose", severity: "warning", proxy_id: "dev"}
```

## Forked go-sdk

Channel mode uses `ServerSession.Notify(ctx, method, params)` from `github.com/standardbeagle/go-sdk` (fork of `modelcontextprotocol/go-sdk`). Fork adds this method, pending upstream PR #898. When upstream merges and releases, swap imports back to `modelcontextprotocol/go-sdk` and bump version.
