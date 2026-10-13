# agnt

Gives AI coding agents browser superpowers: a daemon-backed reverse proxy, process manager, and PTY wrapper that bridge the agent and a real browser for debugging, wireframing, and visual feedback.

- Repository: https://github.com/standardbeagle/agnt — version is managed by `scripts/release.sh` (never hand-edit version numbers).
- One real binary, `agnt` (`cmd/agnt`). `agnt-daemon` is a copy used for daemon auto-start because sandboxes forbid self-exec; `devtool-mcp` is a legacy alias.
- The Claude Code plugin lives in a separate marketplace repo; this repo ships the binary and MCP server only.

## Build and test

Run tests through `tman` (or the repo shims) per the global standards.

```bash
make build            # agnt binary
make all              # build + the agnt-daemon copy
make vendor-check     # vendor/ must equal what go.mod resolves to
make install-local    # ~/.local/bin
make test             # default suite (host-safe; excludes procisolation/sshe2e/chromee2e tags)
make test-isolated    # procisolation tests inside a PID namespace (Linux)
make test-ssh         # SSH harness + containerized sshd smoke (loud skip without Docker/Podman)
make test-chrome-e2e  # real-Chrome tests — unloaded machine only, never alongside other runs
make test-js          # vitest + jsdom tier for injected scripts (loud skip without npm)
make cross-compile-check   # GOOS=windows build
make install-hooks    # once per clone: core.hooksPath=.githooks
```

- **Full-suite gate is `go test -p 1 ./...`** (serial packages). Parallel `./...` fails on cross-package port contention, not product bugs. CI runs it with `go vet ./...` and `make vendor-check` on every push and pull request (`.github/workflows/test-linux.yml`).
- The pre-commit hook (`.githooks/pre-commit`) runs gofmt, `go vet`, and `go test -race -p 1` on staged packages and blocks on failure. `--no-verify` is allowed in exactly two cases — see `.claude/rules/testing-parallel-package-flakes.md` § Sanctioned `--no-verify` cases.
- Tests that start real OS processes must not use `t.Parallel()`.
- Daemon tests use `NewForTest`, never `Start()`.

## Architecture

| Layer | Where |
|---|---|
| MCP tools (daemon-aware, IPC only) | `internal/tools/` |
| Daemon: persistent state, socket IPC hub | `internal/daemon/` |
| Text IPC protocol | `internal/protocol/` |
| Business logic: project detection, reverse proxy | `internal/project/`, `internal/proxy/` |
| Process management (vendored module) | `github.com/standardbeagle/go-cli-server/process` — there is no `internal/process` |
| Incident pipeline (the only agent alert path) | `internal/incident/` |
| PTY overlay UI | `internal/overlay/`, `cmd/agnt/overlay*.go` |

Design decisions that everything else follows:

1. **Binary copies, not self-exec** — sandboxes block a binary forking itself.
2. **`agnt run` injects browser events into the agent** — MCP cannot push, so a PTY wrapper writes synthetic stdin: Browser → Proxy → overlay (per-session Unix-domain socket) → PTY stdin → agent.
3. **Agent context injection** — Claude Code via `--append-system-prompt`; other agents via their context file (`docs/agent-adapters.md`).
4. **Daemon state is a cache; the OS is the truth.** Verify processes, ports, and proxies against the OS before reporting them.
5. **Lock-free by default** — `sync.Map` registries, atomic state, `CompareAndSwapState()` transitions.
6. **No silent failures** — anything declared in `.agnt.kdl` either happens or surfaces a visible event to the agent and session log; `debug.Log` alone is not enough.
7. **Session scoping is the default; global is the audited exception** (`scope.Scope`, `resolveProjectScope`).
8. **`internal/overlay` must not be imported by `internal/daemon`** (import cycle) — pass data over IPC, interfaces, or strings.

Detail loads automatically from `.claude/rules/` when you open files in the matching packages.

## Exposure posture (operator decision, 2026-07-31)

Every listener ships **loopback-only** (`127.0.0.1`, never `0.0.0.0`). Only the operator widens exposure; an agent never does.

| Listener | Default | Widened only by |
|---|---|---|
| Dev proxy (`internal/proxy/server.go`) | loopback | `tunnel`, or `bind` / `cloudflare-tunnel` in `.agnt.kdl` |
| Public walkthrough plane (`internal/daemon/publish_public.go`) | off | `AGNT_PUBLIC_ADDR` |
| `agnt publish serve` (`cmd/agnt/publish_serve.go`) | loopback | `--tunnel` |
| Overlay (`cmd/agnt/overlay.go`, `ai_overlay.go`) | per-session Unix-domain socket, no TCP port | nothing |
| Daemon control socket / named pipe | uid-scoped | nothing |

Grades: `tunnel cloudflare`/`ngrok` are public and unauthenticated; a `cloudflare-tunnel` block is public behind Cloudflare Access; `tunnel tailscale` and `bind "tailscale"` are tailnet-private. A non-loopback, non-tailnet `bind` requires `allow-external`; the tailnet exemption is checked on the resolved address (`platform.IsTailnetAddress`), never the token.

Reaching a listener is not the same as driving the agent. The dev proxy's control WebSocket classifies each connection once, at upgrade (`controlCallerTrusted`, `internal/proxy/ws_handler.go`): agent-input, secret, session/store and voice frames are accepted only from a Cloudflare Access-verified caller, an identified tailnet peer, or the local browser (loopback peer, loopback `Host` and `Origin` on the proxy's port, no reverse-proxy headers). An unauthenticated tunnel visitor is refused them. The local-browser rule is applied only while no public URL is bound to the proxy, or the bound one was set by a tunnel agnt itself started of a kind captured live (a Cloudflare quick tunnel or `tailscale serve`), or is the proxy's own named tunnel; the tunnel's kind decides, never the URL's hostname. With ngrok, or a `public-url` bound any other way, the local browser is refused too.

Page telemetry from an untrusted connection is recorded marked `untrusted` (`proxy.LogEntry.Untrusted`): it is never pushed over the channel, an untrusted browser error is a separately fingerprinted `browser_js_untrusted` incident whose text is labelled, and `proxy exec` is sent to and answered by trusted connections only. Residual: on pull surfaces the marker is a label, not a barrier — an agent that reads `proxylog` or `currentpage` on a publicly tunnelled proxy is reading visitor-authored text.

The first three listeners are hardened to public standard in every posture: read-header/read/write/idle timeouts, `MaxHeaderBytes`, a concurrent-connection cap, and a request-rate cap. Unbounded is a defect. Every share route is rate-capped. Known residuals: upstream fetches are not cached (the outbound bounds are a per-origin rate cap and a plane-wide concurrency ceiling), and per-client limits are keyed on the socket peer except behind a tunnel `agnt publish serve --tunnel` started itself (client address from the tunnel's forwarding header; captured live for Cloudflare and tailscale, not for ngrok), so behind an operator's own reverse proxy, and on the daemon's `AGNT_PUBLIC_ADDR` listener, every viewer shares that hop's address.

## Conventions

- KDL for app config (`.agnt.kdl`); JSON for content data, API contracts, and LLM-facing formats.
- MCP tool errors are `CallToolResult{IsError: true}`, never Go errors.
- `github.com/standardbeagle/go-sdk` is a fork of the MCP go-sdk adding `ServerSession.Notify`; used without a `replace` so `go install` works. Swap back when upstream PR #898 merges.
- `vendor/` is a plain copy of the module versions in `go.mod`; never edit a file under it. A fix to `go-cli-server` (or any dependency) goes upstream and arrives as a version bump, because a patch that lives only in `vendor/` is dropped by the next `go mod vendor` and never reaches `go install` builds. `make vendor-check` enforces this in CI and in the release build.

## Where things are documented

| Topic | Location |
|---|---|
| Per-tool params, `__devtool` API, `agnt monitor` | `docs/mcp-tools.md` |
| `.agnt.kdl` reference | `docs/configuration.md` |
| Remote SSH | `docs/remote-ssh.md` |
| Overlay internals | `docs/overlay-internals.md` |
| Hooks, channel mode | `docs/hook-dispatcher.md`, `docs/hook-rules.md`, `docs/channel-mode.md` |
| Public walkthroughs | `docs/public-walkthroughs.md` |
| Flake registry | `docs/testing-flake-registry.md` |
| Doc index | `docs/README.md` |
| Codebase map | `search` skill |
