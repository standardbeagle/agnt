---
name: daemon-debug
description: Diagnose autostart, proxy, and process issues — why things aren't starting, connecting, or recovering
---

# Daemon Debugging

Use this skill when diagnosing why processes aren't starting, proxies are missing, ports are occupied, or the autostart flow isn't working.

## Diagnostic Checklist

Work through these in order. Each step either finds the problem or rules out a category.

### 1. Verify Daemon State
```bash
# Is the daemon running?
agnt daemon status

# What processes does the daemon know about?
printf 'PROC LIST\n;;\n' | socat - UNIX-CONNECT:/run/user/$(id -u)/devtool-mcp.sock

# What proxies exist?
printf 'PROXY LIST\n;;\n' | socat - UNIX-CONNECT:/run/user/$(id -u)/devtool-mcp.sock
```

Decode base64 JSON responses. Check:
- Are expected processes listed?
- What state are they in? (running / failed / etc.)
- Do running processes have `urls` populated?
- Are any warnings present (rogue processes)?

### 2. Check Config
```bash
# Read the project config
cat /path/to/project/.agnt.kdl
```

For each proxy with `script` set:
- Does the linked script exist in `scripts {}`?
- Does the script have `url-matchers`? Do they match actual output format?
- Does the proxy have `fallback-port`? (safety net if URL detection fails)

### 3. Verify URL Detection

URL detection is the #1 failure point for script-linked proxies.

**Common URL matcher mismatches:**

| Dev server | Actual output | Correct matcher |
|-----------|--------------|----------------|
| dotnet | `Now listening on: http://localhost:6111` | `"Now listening on: {url}"` |
| Vite | `Local:   http://localhost:5173/` | `"Local:\\s+{url}"` |
| Next.js | `- Local: http://localhost:3000` | `"Local:\\s+{url}"` |
| Flask | `Running on http://127.0.0.1:5000` | `"Running on {url}"` |

**How to test**: Check process output for the URL line, then verify the matcher pattern (with `{url}` removed) matches as a regex substring.

### 4. Check Port Bindings
```bash
# Linux: what's listening on expected ports?
ss -tlnp | grep -E ':(6111|6112|3000|5173)\b'

# Compare against daemon's tracked processes
# If port is occupied by a PID the daemon doesn't own → rogue process
```

### 5. Check Event System

If URL was detected but proxy wasn't created:
- Was the `proxyEvents` channel full? (Check daemon debug log for "channel full" warnings)
- Did `handleURLDetected` find the matching proxy config? (Check for "Proxy X matches script Y" in debug log)
- Was the proxy already created under a different ID? (Event-driven IDs include host-port suffix)

### 6. Check Session Log

The session log (the startup output the developer sees) should show:
- `[starting]` / `[started]` for each script
- `[proxy_starting]` / `[proxy_started]` for each proxy
- `[dependency_ready]` or `[dependency_wait]` for dependencies

Missing entries indicate the step was silently skipped.

## Common Failure Modes

### Proxy Never Created
**Symptoms**: Process running, no proxy in `PROXY LIST`
**Causes**:
1. URL matcher doesn't match actual output → fix matcher in `.agnt.kdl`
2. No `fallback-port` configured → add it as safety net
3. Event channel was full → health check should catch this
4. Process started but URL was in first output batch that was scanned before URLTracker was wired up

### Rogue Process on Port
**Symptoms**: Process shows as "failed" but port is in use by unmanaged PID
**Causes**:
1. Previous run wasn't cleaned up (daemon restart, crash)
2. Another tool started on the same port
**Fix**: `proc {action: "restart", process_id: "..."}` kills rogue and restarts

### Process "Failed" But Actually Compiling
**Symptoms**: Process killed during long compilation
**Root cause**: Stall detection misidentified compilation output as stall
**Fix**: Error output IS activity — AlertScanner classifications must reset stall timer

### Stale Status Bar Indicators
**Symptoms**: Status bar shows indicators for scripts that were removed from `.agnt.kdl`
**Root cause**: Script registry entries survived session disconnect
**How it happens**:
1. Session 1 starts 3 scripts → 3 registry entries
2. User exits (session disconnects)
3. `CleanupSessionResources` must remove ALL registry entries for the project
4. If it doesn't, next session's `Register` returns stale entries via `LoadOrStore`
5. Status bar renders indicators for all registry entries, not just current config

**Diagnosis**:
```bash
# Check script registry vs config
printf 'SCRIPT LIST\n;;\n' | socat - UNIX-CONNECT:/run/user/$(id -u)/devtool-mcp.sock
# Compare count against scripts in .agnt.kdl
```

**Fix**: The registry is ephemeral — rebuilt from `.agnt.kdl` each session. `CleanupSessionResources` removes entries when the last session disconnects. If stale entries appear, check that cleanup is running (look for "cleared script registry" in debug log).

### Multiple Autostart Cycles
**Symptoms**: Session log shows autostart running twice for same project
**Causes**:
1. Session reconnection triggered re-autostart
2. Multiple clients connecting to same daemon
**Impact**: Second cycle may fail if ports already bound by first cycle's processes

## Cross-Platform Notes

When debugging on WSL:
- `runtime.GOOS` is `"linux"` — use `platform.IsWSL()` instead
- Processes may be Linux-side or Windows-side depending on project path
- Port checks may need both `ss` (Linux) and `netstat.exe` (Windows)
- Use `platform.ShouldUseWindowsShell(path)` to determine which side owns the process
