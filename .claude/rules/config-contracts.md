---
paths:
  - "internal/config/**"
---

# Config Contracts

## Parsing Authority

`.agnt.kdl` is the single source of truth for expected project state. The config parser (`internal/config/agnt.go`) must:

1. Parse all declared fields completely — no field should be parsed but unused
2. Validate at parse time — invalid configs fail loudly, not silently at runtime
3. Return sensible defaults via `DefaultAgntConfig()` — callers should not need to check for nil

## Config Fields That Must Be Honored

Every config field that is parsed must have a consumer. If a field exists in the struct but nothing reads it, that's a bug. Key fields and their consumers:

| Field | Config path | Consumer | Purpose |
|-------|------------|----------|---------|
| `fallback-port` | `proxies.<name>.fallback-port` | Proxy creation fallback, port preflight | Target port when URL detection fails |
| `depends-on` | `scripts.<name>.depends-on` | Autostart ordering, ready signaler | Script launch ordering |
| `url-matchers` | `scripts.<name>.url-matchers` | URLTracker | Pattern to detect dev server URLs in output |
| `port-conflict` | `project.port-conflict` | Port preflight | Policy for handling port conflicts |
| `autostart` | `scripts.<name>.autostart` | RunAutostart | Whether to launch on session connect |

## URL Matcher Validation

URL matchers (`url-matchers` field) are patterns like `"Now listening on: {url}"` that the URLTracker uses to find dev server URLs in process output. Common failure: the pattern doesn't match the actual output format of the dev server.

When implementing or modifying URL matcher logic:
- The `{url}` placeholder is removed and the remaining text is used as a regex
- The regex does substring matching (not full-line matching)
- ANSI escape codes are stripped before matching
- Test matchers against real output from common dev servers (dotnet, vite, next, flask, etc.)

## Cross-Platform Config

The `platform.ShouldUseWindowsShell(path)` check determines shell resolution. Config parsing itself is platform-independent, but fields like `run` (shell commands) and `cwd` (paths) are interpreted differently:
- WSL + Windows filesystem path → `cmd.exe /c`
- Everything else → `sh -c`
