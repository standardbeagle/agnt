# agnt × opencode integration — plugin architecture design

**Date:** 2026-08-07
**Status:** design (no code yet)
**Verdict:** opencode's existing plugin system + local server API support tight
integration of agnt's proxy-UI messaging and incident queues **today — no fork
required**. The only gap is arbitrary custom SSE event types, which this design
does not need.

## What opencode already provides

| Need | Mechanism | Evidence (opencode @ HEAD) |
|---|---|---|
| Subscribe to every session event | plugin `event` hook | `packages/plugin/src/index.ts:224` |
| Register custom tools | plugin `tool` hook + `tool()` helper | `packages/plugin/src/tool.ts:39-49` |
| Background loops (socket client, retries) | async plugin init + `dispose` hook | `packages/plugin/src/index.ts:263-276` |
| Inject agent input mid-run | `POST /session/:id/prompt_async`, picked up next loop iteration | `packages/opencode/src/session/prompt.ts:1499-1521`, exit condition at :1088-1127 |
| Silent/context-only injection | `noReply: true`, parts with `synthetic: true` | prompt.ts:1504, `packages/schema/src/v1/session.ts:397-410` |
| Detect agent idle (safe push points) | `session.idle` bus event | schema `session.ts:572-657` |
| TUI surface (toasts, dialogs, routes, slots) | TUI plugin API | `packages/plugin/src/tui.ts:581-634` |
| Server→TUI push | `POST /tui/show-toast`, `/tui/publish` (4-type union) | `packages/opencode/src/server/groups/tui.ts:29-50` |
| Plugin discovery | `plugin` array in opencode.json, or `{plugin,plugins}/*.{ts,js}` auto-glob; npm or file path specs | `packages/opencode/src/config/plugin.ts:21` |

**Only fork-required item:** publishing *arbitrary* SSE event types to external
subscribers. Not needed here — agnt's daemon socket is already the event
source of truth; opencode is a *consumer*, not a relay.

## Architecture: `opencode-agnt` plugin

One plugin package (npm `opencode-agnt` or project-local `plugins/agnt.ts`),
running inside opencode's process, bridging to the agnt daemon over its unix
socket (`/tmp/devtool-mcp-<uid>/devtool-mcp.sock`, the engine's client
protocol — see `docs-site/screenshots/engine/lib/daemon.mjs`).

```
agnt daemon ──STREAM-EVENTS──▶ opencode-agnt plugin ──prompt_async──▶ opencode session
            ◀──PROXY TOAST────                    ◀──agnt_reply tool── agent
```

### 1. Incident → session pipeline (inbound)

- Plugin init opens a `STREAM-EVENTS` subscription on the daemon socket with
  `severity≥warning` (+ optional type/grep filters from config), handling the
  30s keepalive and reconnect.
- Incoming incident → **queue, don't inject immediately.** Respect agnt's
  single-queue discipline (`.claude/skills/messaging-queue`): dedup by
  signature, coalesce bursts into one digest, and **activity-defer**:
  - if the session is mid-run, inject with `noReply: true` + `synthetic: true`
    — the running loop sees it next iteration without a new LLM round-trip;
  - on `session.idle`, flush the digest as a normal async prompt if the queue
    has been waiting, so work resumes unattended.
- Payload shape: the incident pipeline's compact ping (priority, source, count,
  remediation hint) — NOT raw logs. Same content policy as the PTY injector.

### 2. Agent → browser messaging (outbound, replaces `channel_reply`)

Custom tools via the `tool` hook, each a thin daemon-socket call:

- `agnt_reply(message, type, duration)` → `PROXY TOAST <proxy>;;{...}` —
  agent speaks into the browser overlay (this is the opencode-native
  equivalent of channel mode's `channel_reply`).
- `agnt_incidents(cursor, detail)` → `INCIDENTS` pull — the priority inbox,
  for when the agent wants the full queue rather than the pushed digest.
- `agnt_exec(code)` → `PROXY EXEC` — browser control (post-0.15.4 this
  survives navigation).

### 3. TUI surface

TUI plugin entry: on incident flush, `show-toast` ("3 new incidents — proxy
dev"); optional dialog/slot listing the live inbox. Keep minimal — the value
is the session injection, not chrome.

### 4. Config & scoping

- Plugin options via opencode.json `["opencode-agnt", {...}]`: socket override,
  severity floor, filters, `push: "digest" | "off"`.
- Project scoping aligns naturally: the plugin loads per project directory and
  agnt's incident policy is keyed by normalized project path — concurrent
  projects can't cross sinks (same invariant as agnt's session scoping).

## What this replaces

| Today | With the plugin |
|---|---|
| `agnt run` PTY wrapper + synthetic-stdin injection | `prompt_async` injection with `noReply`/`synthetic` — native, no wrapper |
| Channel mode (`claude/channel`, Claude-only) | Same semantics on opencode via the bus + server API |
| `channel_reply` MCP tool | `agnt_reply` plugin tool |
| `watch`/`agnt monitor` for agent visibility | plugin's own STREAM-EVENTS subscription |

`agnt run` and channel mode stay supported for other agents; opencode gets the
tightest integration of the three.

## Phased implementation

1. **Bridge + digest push** — daemon socket client (reuse the engine's client
   protocol), STREAM-EVENTS subscribe, queue/dedup/defer, `prompt_async`
   injection. This alone is the integration's core value.
2. **Outbound tools** — `agnt_reply`, `agnt_incidents`, `agnt_exec`.
3. **TUI toast** + optional inbox dialog.
4. **npm package + docs** — `opencode.json` snippet, scoping notes.

## Risks / open questions

- **Spam discipline is the whole game.** The plugin must implement agnt's
  messaging-queue gates faithfully (dedup, batch, activity-defer); a naive
  forwarder will spam the session. Port the gate logic, don't reinvent it.
- **Loopback only:** opencode server defaults to 127.0.0.1 with optional Basic
  auth; the plugin talks to the daemon socket locally — no new listeners, no
  posture change. (agnt exposure rules apply unchanged.)
- **`noReply` + mid-run pickup** semantics are the load-bearing assumption;
  verify on opencode HEAD before building phase 1's flush logic (the exit
  condition at prompt.ts:1088-1127 is the contract).
