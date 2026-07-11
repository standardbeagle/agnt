# Walkthrough: `agnt run claude` as Your Daily Driver

## What it is

`agnt run <ai-tool>` wraps an interactive AI coding CLI (Claude Code, and other
agents) in a PTY overlay. The overlay does three jobs at once:

1. **Pushes browser events into the agent's conversation** as synthetic stdin —
   so a runtime error in the browser reaches the agent mid-conversation without
   anyone asking for it.
2. **Renders an interactive overlay UI** — a command palette, a ports & orphans
   panel, a startup splash — layered over the wrapped tool's own output.
3. **Contains everything the session spawns** in one process group, so the next
   session can cleanly reclaim ports and PIDs.

## Why it's unique

MCP servers are request/response — a tool the agent *calls*. They cannot *push*
a notification to the agent. That's a real limitation: when your dev server
throws a runtime error in the browser, nothing in plain MCP can interrupt the
agent to tell it. `agnt run` solves this structurally by owning the terminal:

```
Browser → Proxy → HTTP POST → Overlay (port 19191) → PTY stdin → AI Tool
```

The overlay listens on port 19191 for WebSocket connections from the proxy,
turns the browser event into text, and injects it as if you had typed it. The
agent sees it as part of the conversation — no polling, no "check the errors
tool," no lost context.

## Real-world scenario

You start your day with `agnt run claude` and work normally: ask for a feature,
the agent writes code, you refresh the browser. Something throws. Instead of you
noticing, copying the stack trace, and pasting it back, the error arrives in the
agent's input on its own and it starts diagnosing. Later you background a dev
server, close the session, and open a fresh one — the port is free because the
old session's process group was reaped. This walkthrough covers each of those
moments.

## Step by step

### 1. Launch

```
agnt run claude
```

agnt copies itself to a sidecar binary (sandboxed agents like Claude Code forbid
forking/exec-ing *self*; a separate binary sidesteps that), starts or connects to
the daemon, captures the PTY child's PID as the session's process-group id, and
registers the session. A startup splash confirms wiring before the wrapped tool
takes over the screen.

### 2. Let browser events flow in

Once a proxy is running for your project, any error it captures is POSTed to the
overlay on 19191 and injected as synthetic stdin. You don't call anything. A
JavaScript exception, a failed fetch, a console error — it shows up in the
agent's input stream and it can react immediately, with the surrounding
conversation still in context.

This is the whole reason `agnt run` exists: it turns the browser into a push
source for an agent that otherwise can only pull.

### 3. Drive the command palette

Press `:` or `/` to open the command palette — a filterable list of overlay
actions. It is **not** a shell prompt: you are filtering agnt commands (open the
ports panel, run the doctor, etc.), not typing bash. Type to narrow the list,
Enter to run. (See `docs/overlay-internals.md` for the full command set and the
output-protection chain that keeps the palette from corrupting the wrapped
tool's rendering.)

### 4. Watch ports & orphans

The ports & orphans panel shows which ports your project's processes hold and
flags orphans — ports still bound by processes a previous, crashed session left
behind. This is your at-a-glance answer to "why can't my dev server bind 3000?"

### 5. Understand session containment (the reason cleanup "just works")

Non-interactive bash — the shell the agent runs commands through — does **not**
enable job control. So when the agent runs:

```bash
npm run dev &
```

that backgrounded server does *not* get its own process group. It inherits the
PTY child's group. agnt makes the PTY child a session leader via `setsid` (done
by the pty library), so its PID doubles as the session process-group id and every
descendant — shells, tool calls, backgrounded jobs, their grandchildren —
inherits it.

On session exit, `CleanupSessionResources` kills that entire group (SIGTERM, a
2-second grace window, then SIGKILL to survivors) *before* touching individually
managed processes. So the `npm run dev &` the agent forgot about is reaped, and
the next session claims port 3000 with no manual `kill`.

What's caught: `npm run dev &`, `nohup cmd &` (the group kill uses SIGTERM/SIGKILL,
not SIGHUP), `disown`'d jobs (disown edits the shell's job table, not the pgid),
and all grandchildren.

What intentionally escapes — because it's a conscious "survive the session"
decision you own:

```bash
setsid cmd &        # new session + pgid at exec time
```

plus double-fork daemons, `systemd-run --scope`, and `docker run -d`. agnt does
not try to reap these; a regression test (`TestSessionContainment_SetsidEscapes`)
pins that `setsid` stays escaped, because accidentally reaping a process you
deliberately detached is worse than leaking it.

### 6. Reconcile against OS truth with `agnt doctor`

The daemon's in-memory view of processes and ports is a *cache*, never the
authority. When you want a verified picture — or cleanup — run:

```
agnt doctor
```

or the MCP equivalent `daemon {action: "doctor"}`. Doctor performs a full
reconciliation: it probes each PID against the OS, rescans the socket table for
port ownership, health-checks proxies, and compares all of it to what the daemon
*thinks* is true. Any mismatch is resolved in favor of OS reality, surfaced in a
structured report with offered actions. Use it when the state looks
contradictory, or before trusting the daemon's view for a risky operation.

## Gotchas

- **Port 19191 is the overlay's inbound event port.** If something else holds it,
  browser-event injection won't reach the agent. It's reserved for
  proxy→overlay WebSocket connections.
- **The palette is not a shell.** `:`/`/` filters agnt overlay commands. Typing a
  shell command there won't run it.
- **Containment only covers descendants of the PTY child.** Anything that
  `setsid`s, double-forks, or hands itself to systemd/a container runtime leaves
  the group on purpose and is now *your* cleanup responsibility — that's the
  designed contract, not a bug.
- **Don't trust the daemon cache as authority.** If process/port state looks
  wrong, run doctor — it re-verifies against the OS rather than re-reporting the
  possibly-stale cache.
- **The sidecar binary is intentional.** Seeing an `agnt-daemon`-style second
  binary is the fork-prevention workaround, not a stray copy.
