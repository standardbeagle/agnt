---
paths:
  - "internal/tools/**"
---

# MCP tools

Per-tool parameters and output formats: `docs/mcp-tools.md`. Session scoping of each verb: `daemon-architecture.md` § Tool session-scoping.

## Handler contract

- Input/Output structs carry JSON schema tags; handlers return `(*mcp.CallToolResult, OutputStruct, error)`.
- Report failures as `CallToolResult{IsError: true}`, never as a Go error.
- Tool names match `^[a-zA-Z0-9_-]{1,128}$`. Transport is stdio only, so log to stderr (or `debug.Log`) and never to stdout.
- A `jsonschema` tag is the whole description. Its first token must not contain `=`, or `AddTool` panics and takes down the MCP server at startup.
- Every tool talks to the daemon over IPC (`DaemonTools`). There is no in-process fallback path.
- Query and list tools default to the caller's project. Gated tools expose the same optional `global *bool` (pinned by `TestGatedMCPTools_ExposeGlobalFlagUniformly`).
- When the daemon already knows the valid values (sessions, ids), return them as candidates instead of an error that sends the caller off to find them.

## Tool inventory

`detect`, `run`, `proc`, `proxy`, `proxylog`, `tunnel`, `currentpage`, `get_incidents` (the only incident pull surface; `get_errors` is gone), `diagnose`, `verify_change`, `release_qa`, `responsive_audit`, `api_audit`, `loading_audit`, `snapshot`, `replaytest` (Pro), `daemon`, `session`, `watch`, `channel_reply` (only when channel mode and its reply tool are enabled), `automation`, `browser`, `error_queue`, `store`, `walkthrough`, `publish`, `devauth`, `demo`.

Registration lives in `cmd/agnt/serve.go` (`runDaemonClient`) and `RegisterDaemonTools`. Adding a tool means updating this list, the catalog in `docs/mcp-tools.md`, and the `Instructions` string in `serve.go` by hand.

`replaytest replay` is the one handler that does work in-process (it drives headless Chrome from the MCP server) rather than over daemon IPC.
