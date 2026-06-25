# Verb-driven setup gate, cwd-scoped config, and live `.agnt.kdl` reload

Date: 2026-06-24
Status: Approved (decomposition + sequencing approved by owner)

Progress: A ✅ done (cwd-only + `--config`, validation/gate honor it).
B ✅ done (verb-driven gate on `run` all-agents + universal fallback; `ai`
interactive setup, one-shot protected).
C ✅ done for the D-critical paths: proxy restart now preserves its bound port
(`BoundPort` + `StrictListenPort`), waits for port release instead of a fixed
sleep, and `Stop` drains WebSocket conns + idle backend conns. Proc restart
already had EADDRINUSE preflight recovery + retry. **Deferred (tracked):** the
auto-restart-on-crash loop in `runServer` reassigns `ps.httpServer` without the
mutex (latent data race with a concurrent `Stop`) and rebinds the listener on
the crash path; these affect only the crash auto-restart path, not the explicit
stop→recreate path that D's reconcile uses, so they are out of D's critical path.
D ✅ done: pure signature-based reconcile diff (`reconcile.go`, table-tested) +
`ReconcileProjectConfig` apply (`reconcile_apply.go`) that stops removed/changed
scripts and delegates starts to the tested autostart path, wired to
`AUTOSTART RECONCILE` + `Client.AutostartReconcile`, verified end-to-end (live
remove + live change of a running script, `e2e_reconcile_test.go`). Per the
owner's framing D = C's teardown fixes + live `.agnt.kdl` edit; both delivered,
so config changes apply without a daemon/session restart.

**Note on the PTY first-run relaunch:** `agnt run`'s two-phase setup→relaunch is
structural (the setup child exits before the coding child starts) and is left in
place. The "no restart" goal is met for the live-edit case via reconcile; the
`ai` interactive REPL opens in setup mode in the same session. Fully unifying the
PTY run model onto reconcile (no relaunch) is a larger restructure left as a
follow-up.

## Problem

`agnt run claude` in a new project with no `.agnt.kdl` did not enter the
first-run setup flow. Two root causes plus a design gap:

1. **Walk-up config lookup.** `FindAgntConfigFile` walks *up* the directory
   tree. A new project nested under a directory that already has `.agnt.kdl`
   (e.g. anything under `work/core/`) is seen as "already configured", so the
   setup gate is skipped. The owner's verdict: walk-up was a mistake for this
   product — config scope should be the cwd of execution, with an explicit
   `--config` override when a different file is needed.

2. **Adapter-coupled gate.** The setup gate fires only when
   `adapter.Name() == "claude"` and only on the PTY (`agnt run`) path. `agnt ai`
   (a fully interactive REPL) bypasses it entirely. The owner wants the *verb*
   (`run`, `ai`) to dictate behavior, with the bare minimum of agent-specific
   workarounds — the adapter should carry only injection *mechanics*.

3. **Relaunch crutch.** The setup flow is two-phase (setup → relaunch into a
   clean coding session) because changed config isn't applied live. The owner
   wants the relaunch gone: support live `.agnt.kdl` edits in all cases, which
   first requires fixing the proxy/proc teardown-restart bugs that make live
   re-apply unreliable today.

## Design principles

- **Verb drives behavior.** The decision *whether* to run setup belongs to the
  command verb, not to which agent was launched.
- **Thin adapter.** Adapters describe only *how* to inject a system prompt
  (flag-based vs stdin-after-delay). An unrecognized command falls back to the
  universal stdin injection — it still gets the gate.
- **cwd-scoped config.** `<cwd>/.agnt.kdl` only. No walk-up. `--config <path>`
  overrides the resolved file.
- **No relaunch.** Setup writes config; the daemon reconciles live; the session
  continues.

## Decomposition and sequencing

Four separable pieces, built `A → B → C → D`. Each ships standalone value.

### A — Config scope: cwd-only + `--config`

- `FindAgntConfigFile(dir)` stops walking up: it checks `<dir>/.agnt.kdl` and
  returns it or `""`.
- New `ResolveConfigPath(dir, override)`: if `override != ""` use it (error/empty
  if missing); else `<dir>/.agnt.kdl` or `""`.
- `--config <path>` persistent flag on `run`, `ai`, `init`. Threaded into the
  first-run gate and config validation client-side, and propagated to the daemon
  via session metadata so autostart loads the same file.
- Blast radius: all ten `FindAgntConfigFile` callers re-checked. Daemon-side
  callers (autostart, doctor, hookrules) keyed by project path keep using the
  cwd-derived path unless an override is supplied.

### B — Verb-driven setup gate

- Hoist the gate from the claude-only branch in `firstRunOrCoding` to a
  verb-level chokepoint shared by `run` and `ai`.
- Gate fires for any auto-run command; adapter resolves only the injection
  mechanism. `nil` adapter → universal stdin injection.
- Interim: keep today's PTY relaunch mechanic; `ai` injects the setup prompt
  into its single interactive session. D later retires the relaunch.

### C — Teardown/restart hardening (prerequisite for D)

Fix the proxy/proc fragilities that block reliable live re-apply:

1. Proxy restart must preserve its listen port (today `hub_proxy.go` recreates
   with `ListenPort: 0`).
2. Close the old listener before rebinding; avoid TIME_WAIT collisions (replace
   the fixed 100ms sleep with wait-until-free polling).
3. Drain/close WebSocket conns and flush the transport pool on `Stop`.
4. Resolve the `Stop` vs `runServer` restart-loop race.
5. Reset per-proxy restart counters / lastError on recreate.
6. Proc restart: ensure the old process group is fully reaped before the new
   one binds.

### D — Live `.agnt.kdl` reconcile

- Config-diff: compare freshly loaded config against running scripts/proxies;
  stop removed/changed, start new, leave unchanged untouched.
- Trigger: an explicit reconcile path (new protocol verb or reuse
  `AUTOSTART RUN`) invoked when setup writes config.
- Retire the setup→relaunch two-phase: setup writes `.agnt.kdl`, daemon
  reconciles, the interactive session keeps running.

## Testing

- A: table tests for `FindAgntConfigFile` (no walk-up) and `ResolveConfigPath`
  (override present/missing, cwd present/absent). Gate-decision tests updated.
- B: gate fires for `run` and `ai`, claude and non-claude and unknown commands;
  adapter selection verified; `--config`/marker/TTL interplay preserved.
- C: stop→start-same-port succeeds repeatedly; no goroutine/listener leak
  (goleak); restart preserves port; rapid restart no "address already in use".
- D: reconcile diff stops removed, starts added, leaves unchanged; changed
  script/proxy re-applies on the same port without manual stop.

## E — Deterministic auto-config (added 2026-06-24, after A–D)

Owner insight: setup over-delegated to the LLM. A simple project (package.json
web app, dotnet site, Go/Python repo) is fully determinable — only complicated
configs need detailed LLM instructions. Transcript evidence: the LLM hit the
setup prompt and replied "what's the task?" instead of acting.

- **dotnet detection** added to `internal/project` (`.csproj`/`.sln`/`.fsproj`
  → `dotnet watch run` / `dotnet test` / `dotnet build` / `dotnet format`),
  checked last so a Node project with a stray `.csproj` still wins.
- **`internal/autoconfig.Generate(p) (kdl, ok)`** — pure generator: dev server
  as an autostart script with a linked reverse proxy; lint/test/build as
  on-demand scripts (overlay queue). Conservative server detection (Node dev/
  start script present, dotnet, wails — not a bare `go run`). `ok=false` for
  unknown/complex → LLM fallback. Output is validated against `ParseAgntConfig`.
- **`tryAutoConfig` wired into `firstRunOrCoding`** before the gate: a confident
  project gets `.agnt.kdl` written deterministically (write + inform, editable),
  then the existing gate sees a config and goes straight to coding — no LLM
  round-trip. `agnt init` short-circuits when auto-config wrote it (a
  pre-existing config still reconfigures via setup). Apply mode = write+inform;
  edits apply live via piece D.
- **Setup prompt tightened** (`setup_prompt.go`): only fires for projects
  auto-config could not handle, and is now imperative ("this IS your task; do
  not ask what to work on") to fix the passive-LLM failure.

Tests: `internal/autoconfig/autoconfig_test.go` (Node app/library, dotnet, Go,
wails, unknown — every generated config re-parsed), `internal/project` dotnet
detection, `cmd/agnt` `TestFirstRunOrCoding_AutoConfigSkipsSetup`.

Known limitation (unchanged): a wrapper command (e.g. `cdsp`) that does not
PATH-resolve to `claude` hits the Universal stdin adapter, so a complex-project
setup prompt arrives as a stdin message. Auto-config sidesteps this for the
common simple case.

## Out of scope

- fsnotify auto-watch of `.agnt.kdl` (reconcile is explicitly triggered).
- Cross-project config inheritance (deliberately removed with walk-up).
