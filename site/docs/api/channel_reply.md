---
sidebar_position: 15
---

# channel_reply

Send a message from the AI agent to the developer's browser overlay as a toast. Part of [Channel Mode](#channel-mode-beta) — it lets the agent talk back to the human in the browser without a PTY indicator.

Registered only when channel mode is enabled with `reply-tool true` (the default once `channel.enabled true` is set).

## Synopsis

```json
channel_reply {content: "Build succeeded, opening preview..."}
channel_reply {content: "Which layout?", title: "Choose", severity: "warning", proxy_id: "dev"}
```

## Parameters

| Parameter | Type | Required | Description |
|-----------|------|----------|-------------|
| `content` | string | Yes | Message body (markdown OK) |
| `title` | string | No | Toast title |
| `severity` | string | No | Toast style: `info` (default), `warning`, `error` |
| `proxy_id` | string | No | Target a specific proxy; omit to fan out to all active proxies |

## Output

```json
{ "delivered": 2, "message": "..." }
```

`delivered` is the count of proxies that received the toast.

## Channel Mode (Beta)

> Channel mode is experimental and Claude Code only. It uses a forked go-sdk and the `--dangerously-load-development-channels` flag. Protocol and tool shapes may change.

Channel mode pushes browser errors, diagnostics, and interactions directly into Claude's context as `<channel>` events — no `agnt run` PTY wrapper or polling required. `channel_reply` is the return path. Enable it in `.agnt.kdl`:

```kdl
channel {
    enabled true
    reply-tool true   // register channel_reply (default)
}
```

Then launch Claude Code with the development-channels flag:

```bash
claude --dangerously-load-development-channels server:agnt
```

## See Also

- [get_incidents](/api/get_incidents) — incident inbox the channel forwards from
- [proxy](/api/proxy) — proxy whose overlay receives the toast
