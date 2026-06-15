# Replay-Test: Record → Worker-Mock → Replay Front-End Testing

**Date:** 2026-06-15
**Status:** Design (approved sections 1–6, pending written-spec review)
**License:** Pro — gated behind `CapAdvancedTesting`

## Summary

A license-gated front-end testing pipeline built on the agnt reverse proxy.
Record real API traffic and user interactions against any proxied app, generate
an in-page **Web Worker mock** that replays recorded responses fully locally (no
network), then drive the front end with chromedp — both a deterministic seed
replay and a subagent-driven exploratory breadth pass — to catch JavaScript
crashes and DOM regressions at high speed. Fuzz mutators perturb recorded
responses to test front-end resilience.

food-track is the first dogfood target; the feature is generic and works on any
app behind the agnt proxy (whatever `base_url` the proxy fronts). No
target-specific code.

## Goals

- Record once, replay thousands of times locally and deterministically.
- "Comprehensive without manual labor": auto-capture DOM baselines, AI-refine
  for signal.
- Ultra-fast: API resolved in-browser from a Web Worker, no backend, parallel
  isolated contexts.
- Two failure signals (v1): **front-end JS errors/crashes** and **assertion
  failures on expected DOM**. (Visual-diff and spinner-hang detection are out of
  scope for v1.)

## Non-Goals (v1)

- Load testing / throughput benchmarking (this is record-replay, not traffic
  generation).
- Visual/screenshot regression and infinite-spinner detection (deferred; the
  agnt `snapshot` and `loading_audit` tools already cover these separately).
- Service-Worker interception (kept as a fallback if the in-page shim's coverage
  gaps bite — see Approach B).

## Architecture

Three phases:

```
RECORD ──► GENERATE ──► REPLAY
 (proxy)    (worker     (chromedp drive +
            bundle+AI)   worker-mock + asserts)
```

New package `internal/replaytest/`:

| Unit | Responsibility |
|------|----------------|
| `recorder.go` | Subscribe to proxy `TrafficLogger` (HTTP / Response / Interaction / Mutation log types, already emitted). Assemble a **Scenario**: ordered interaction steps + API request/response pairs + per-step DOM snapshots. |
| `scenario.go` | Scenario data model + JSON load/save. On-disk source of truth at `.agnt/replaytests/<name>.json`. |
| `worker_bundle.go` | Pure codegen: emit the main-thread fetch/XHR shim + the Web Worker (recording store, request matcher, fuzz mutator). Recordings embedded. Templated; testable by string output. |
| `refine.go` | One-time AI pass after recording: mask dynamic DOM noise (timestamps, ids), flag high-signal assertions. Writes mask map back into the Scenario. |
| `driver.go` | Replay orchestration: inject worker bundle via proxy, drive chromedp (seed replay + subagent breadth), collect JS errors + assertion results. |
| `report.go` | Pass/fail rollup → incident pipeline + tool output. |

Reuses: proxy `injector`, `chromedp` automation, `get_errors` / incident
pipeline (JS crash capture), `license.Manager.Check`.

**Boundaries:** recorder only writes Scenarios; driver only reads them.
`worker_bundle` is pure (Scenario → string). `refine` and the breadth pass are
the only LLM-touching units, isolated from the deterministic core.

### Interception mechanism (Approach A — chosen)

In-page `window.fetch` + `XMLHttpRequest` shim, injected by the existing proxy
`injector` only when a replay session is active (flag/header gated — never
affects normal browsing). The shim delegates match + mutate to a real **Web
Worker** and constructs a synthetic `Response` / XHR state. Fully local.

Rejected:
- **B — Service Worker (MSW-style):** higher fidelity (full-document
  navigations) but fiddly registration over the proxy, harder per-test reset,
  heavier. Kept as fallback.
- **C — Proxy-level replay:** proxy serves recordings instead of forwarding.
  Not local/parallel, contradicts the web-worker-local goal. Rejected.

## Scenario data model

JSON (LLM-consumed + content data → JSON per project convention). Path:
`.agnt/replaytests/<name>.json`.

```json
{
  "name": "<scenario-name>",
  "version": 1,
  "recorded_at": "2026-06-15T00:00:00Z",
  "base_url": "http://localhost:3000",
  "steps": [
    {
      "index": 0,
      "kind": "navigate",
      "selector": "a[href='/log']",
      "value": "",
      "dom_signature": "blake3:...",
      "assertions": [
        { "selector": "h1", "type": "text", "expect": "Today", "mask": false }
      ]
    }
  ],
  "recordings": [
    {
      "match": { "method": "GET", "path": "/api/items", "query_keys": ["date"] },
      "request_body_sig": "",
      "status": 200,
      "headers": { "content-type": "application/json" },
      "body_ref": "blob:0",
      "hits": 3
    }
  ],
  "blobs": { "blob:0": "<inline json or base64>" }
}
```

- **step.kind:** `navigate | click | input | submit`.
- **Matching key:** `method + normalized path + sorted query_keys + body_sig`.
  Path params (`/items/42`) templated to `/items/:id` at record time so replay
  matches across ids.
- **Ordered queue:** multiple recordings with the same key form a queue; replay
  pops in sequence, respecting `hits`.
- **dom_signature:** normalized DOM (strip masked nodes, collapse whitespace,
  drop volatile attrs) → blake3. Cheap equality gate; `assertions` carry the
  readable detail.
- **Fuzz mutators operate on `recordings[].body` at replay only** — never mutate
  the on-disk Scenario.

## Generate phase

`worker_bundle.go` emits two coordinated scripts, injected only when a replay
session is active:

**1. Main-thread shim (tiny):** override `window.fetch` + `XMLHttpRequest`;
build match key; `postMessage` to the worker; await `MessageChannel` reply;
construct synthetic response. Zero network. Worker spawned from a blob URL with
recordings inlined (no extra HTTP fetch).

**2. Web Worker (the routine):**
- Recording store as `Map<key, queue>`, built once at boot.
- `match(key, body)` → pop next queued recording (respects `hits` order).
- `mutate(rec)` → apply the active fuzz preset before reply.
- Miss → return a tagged `__replay_miss` 599 so the driver flags an unrecorded
  call (silent-failure prohibition: never fabricate a response).

**Why the worker, not the main thread:** matching + mutation + the large store
stay off the main thread, so the render loop is unblocked and the drive runs
fast. Each parallel chromedp context gets its own worker = isolated state, reset
by reload.

**Fuzz presets** (reuse agnt chaos vocabulary): `null_fields`, `empty_array`,
`http_error` (500/403), `truncated_json`, `reordered`, `type_flip`. One preset
per replay run, applied in the worker, deterministic per seed.

`refine.go` runs after recording, before replay: feed captured DOM snapshots to
the LLM once, return a mask map + high-signal assertion picks, written back into
`steps[].assertions[].mask`. One-time cost, not per-replay.

## Replay phase

Two lanes per run, both against the worker-mocked front end.

**Lane 1 — Seed replay (deterministic, no LLM):** inject worker bundle, drive
chromedp through captured `steps` exactly. After each step settles: recompute
DOM signature, run assertions, scrape JS errors from the incident pipeline.
Mismatch or crash → fail. Parallelizable across fuzz presets (N workers, one
preset each, same flow).

**Lane 2 — AI breadth (subagent fan-out):** spin a fleet of
`agnt:browser-debugger` subagents, each owning its own chromedp context +
isolated worker-mock (reload = clean state). Each gets a distinct exploration
seed (start route + click-region partition) so coverage doesn't overlap.
Mandate: drive deeper into the mocked app, hunt JS crashes + dead/blank states
the seed path never reached. Each returns a compact verdict
`{states_visited, crashes:[{route,selector,error}], new_assertions:[]}`.

Dispatch all breadth subagents in one batch (parallel); each writes findings to
its own scratch file; driver merges. New stable states a subagent discovers are
promoted into the Scenario as additional seed assertions — coverage compounds
across runs.

**Fuzz × breadth matrix:** breadth subagents may run with a fuzz preset active —
"does the app survive exploration when every API response is mutated." Highest
bug yield, fully local, cheap to run wide.

**Capture plumbing:** all lanes read JS errors through the existing
`get_incidents` / incident pipeline. Cross-session isolation already guarantees
per-context separation — each subagent's crashes stay in its own inbox
partition.

`report.go` rolls up: seed pass/fail per preset + breadth crash list + newly
promoted assertions. Emits one incident summary + structured tool output.

## MCP tool surface

**Tool: `replaytest`** — daemon-aware, session-scoped via `resolveProjectScope`,
`global` flag for cross-project `list`. Single tool, action-dispatched (matches
the `proc` / `proxy` pattern).

| Action | Behavior | License |
|--------|----------|---------|
| `record` | Start capture against a running `proxy_id`; assemble Scenario from the TrafficLogger stream until `stop`. | gated |
| `stop` | Finalize + write `.agnt/replaytests/<name>.json`. | gated |
| `refine` | Run the AI mask/assertion pass on a Scenario. | gated |
| `replay` | Run seed lane + fuzz presets; return pass/fail rollup. | gated |
| `explore` | Run subagent breadth fan-out (agent count configurable). | gated |
| `list` | Scenarios for the project (session-scoped). | free (read) |
| `show` | Scenario detail / last report. | free (read) |

**Gating:** every mutating/run action calls `licenseMgr.Check(CapAdvancedTesting)`
at handler entry. Missing/expired → `CallToolResult{IsError:true}` with an
activation hint (NOT a Go error). `list` / `show` stay free so non-Pro users see
what exists.

**Input struct (sketch):** `Action, Name, ProxyID, Preset string; ExploreAgents
int; Global bool`. All jsonschema-tagged. First jsonschema token must not start
with `=` (AddTool panic gotcha).

**On-disk layout:**
```
.agnt/replaytests/
  <name>.json          # Scenario (source of truth)
  <name>.report.json   # last replay rollup
```
Project-relative, gitignorable. JSON throughout. Worker bundle JS is generated
in-memory, never persisted.

**Surfacing:** replay failures + breadth crashes route to the incident pipeline
(existing), landing in `get_incidents` like any other signal. No parallel
delivery path (messaging-queue single-queue rule).

## Testing strategy

| Layer | What | How |
|-------|------|-----|
| Codegen | `worker_bundle.go` Scenario→JS | Pure string output. Table-driven: assert shim/worker JS contains match keys, fuzz hooks, miss-sentinel. No browser. |
| Matcher | request→recording (path templating, query-key sort, body_sig, hits queue) | Pure Go mirror tested directly; JS matcher via one headless chromedp smoke test. |
| Scenario model | load/save round-trip, path-param templating, blob out-of-lining | Pure unit, real JSON, no mocks. |
| Recorder | TrafficLogger events → Scenario | Live proxy + synthetic traffic; assert assembled Scenario. |
| Fuzz mutators | each preset transforms body; never mutates disk Scenario | Pure unit per preset; property sweep (idempotent on disk copy). |
| Driver seed lane | inject + drive + assert + capture crash | Integration: chromedp against a tiny static fixture app behind the proxy with a known Scenario; assert pass on clean replay, fail on injected DOM drift + planted JS throw. |
| Refine / breadth | LLM units | Boundary-isolated; recorded LLM fixtures (snapshot/replay), not live in CI. |
| License gate | each mutating action blocked without `CapAdvancedTesting` | Unit: Manager seeded missing/expired/valid; assert IsError + activation hint; `list`/`show` stay free. |

Density per project test standard: ≥5 asserts/test, `Eventually` not sleeps,
property sweeps on matcher + mutators. Failures asserted via `get_incidents`.
Build-dep tests (`findAgntBinary`) need `make build` after source changes.

## Open / follow-up

- **Proxy "last-started-wins" (separate bug, not part of this feature).**
  `SessionRegistry.FindByDirectory` (`internal/daemon/session.go:303`) does a
  depth-best-match over a `sync.Map` with undefined `Range()` order; on a depth
  tie the last-iterated (newest) session wins, so a proxy can receive the wrong
  session's overlay endpoint (`internal/daemon/proxy_overlay.go:32`,
  `overlayEndpointForProject`). Needs its own Dart task + deterministic repro
  test. Tracked separately from replaytest.
