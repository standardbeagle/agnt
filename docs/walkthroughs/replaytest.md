# Walkthrough: replaytest — deterministic front-end replay testing

## What it is

`replaytest` is an MCP tool that records a browser scenario's network traffic
once, then **replays the page against an in-page mock of that traffic** — no
backend required. The recorded API is served locally by an in-page fetch/XHR
shim backed by a Web Worker, so the app under test cannot tell it is talking to
a mock. On top of the baseline replay it can run fuzz presets (e.g. an
`empty_array` mutation) to see how the UI behaves when the backend returns
degenerate data.

Actions (from `internal/tools/replaytest_tool.go`):

| Action | Gate | What it does |
|--------|------|--------------|
| `list` | free | List saved scenarios |
| `show` | free | Print a saved scenario's JSON |
| `replay` | Pro | Run the baseline seed lane (+ optional fuzz preset) against a scenario, mocking the network in-page; persists and returns a report |
| `explore` | Pro | Compute a breadth-exploration seed partition for the calling agent to fan out to subagents |
| `refine` | Pro | Use an LLM to mask volatile auto-captured assertions |
| `record` / `stop` | Pro | **Alpha — not wired on this build**; returns an explanatory message |

Source of truth: `internal/tools/replaytest_tool.go`, `internal/replaytest/`,
`docs/replaytest.md`.

## Why it's unique

Front-end tests usually force a bad trade: either you stand up the whole
backend (slow, flaky, stateful) or you hand-write mock fixtures (drift from
reality the moment the API changes). `replaytest` takes a third path — it
captures *real* traffic once and replays it deterministically inside the page:

- The mock lives **in the page**, as a fetch/XHR shim plus a Web Worker that
  serves the recorded responses. The app's own code runs unmodified against it.
- Replay is **deterministic**: same recording in, same responses out, every
  run. That makes it safe to run while the backend is down, offline, or
  mid-refactor.
- The Go↔JS request-matching logic is mirrored (`buildKey`/`recKey`) so a
  request matches the same recorded response on both the Go driver side and the
  in-page worker side.
- Fuzz **presets** mutate the replayed responses (latency injection, 5xx
  errors, empty arrays) to probe UI robustness against data the happy-path
  recording never produced.

The result: you can refactor a component with the real network shape pinned,
and separately stress it against malformed data — all without a running server.

## Real-world scenario

You are refactoring a `ProductList` component. The staging backend is down for
maintenance, but you have a previously recorded `product-list` scenario. You
want to (1) confirm the refactor still renders identically against the real
recorded API, and (2) find out whether the component survives an empty product
array — a case the happy-path recording never captured, and a classic source of
`Cannot read properties of undefined (reading 'map')` crashes.

## Step by step

> **Prerequisites**: replaytest's `replay`/`explore`/`refine`/`record`/`stop`
> actions require a Pro license (`advanced_testing` capability). Without it the
> tool returns:
> `advanced_testing requires a Pro license — run \`agnt activate <key>\` to enable replaytest`.
> The tool also assumes daemon mode (it is registered as a daemon tool).

### 1. List available scenarios (free, no license)

```
replaytest {action:"list"}
```

Expected output:

```json
{ "scenarios": ["product-list", "checkout"], "success": true }
```

The scenario store is scoped to the caller's session project directory unless
you pass an explicit `directory`.

### 2. Inspect the scenario (free)

```
replaytest {action:"show", name:"product-list"}
```

Expected output: `report` carries the scenario JSON — its `baseURL`, ordered
`steps` (navigate / interaction steps), and the auto-captured `assertions` with
their `mask` flags.

### 3. Baseline replay against the in-page mock

```
replaytest {action:"replay", name:"product-list"}
```

This drives a real headless browser (chromedp), loads the page against the
in-page fetch/XHR shim + Web Worker serving the recorded API, walks the
scenario's steps, and checks assertions. No daemon proxy or live dev server is
needed for the network — the mock is entirely in-page.

Expected output:

```json
{
  "report": "{\"name\":\"product-list\",\"seeds\":[{\"preset\":\"\",\"passed\":true,...}],\"crashes\":[]}",
  "success": true
}
```

`success` mirrors `report.Passed()`. The baseline lane (`preset:""`) is always
run.

### 4. Fuzz with the `empty_array` preset — expose the unguarded `.map`

```
replaytest {action:"replay", name:"product-list", preset:"empty_array"}
```

`replay` always runs the baseline **and** the requested preset, so you get both
lanes in one report. The `empty_array` mutation rewrites the recorded
list-endpoint responses to `[]`. If `ProductList` does
`products.map(...)` without guarding an empty/undefined array, the fuzz lane
crashes:

```json
{
  "report": "{\"name\":\"product-list\",\"seeds\":[
     {\"preset\":\"\",\"passed\":true},
     {\"preset\":\"empty_array\",\"passed\":false}],
   \"crashes\":[{\"preset\":\"empty_array\",
     \"message\":\"TypeError: Cannot read properties of undefined (reading 'map')\"}]}",
  "success": false
}
```

`success:false` + a populated `crashes` array is the actionable signal: the
happy path is fine, but the component is not defensive against an empty list.
(Unknown preset names are rejected with the available list, sourced from
`replaytest.PresetNames()`.)

### 5. Explore — fan out breadth coverage

```
replaytest {action:"explore", name:"product-list", explore_agents:4}
```

`explore` does **not** run browsers itself — a Go MCP server cannot spawn Claude
subagents in-process. It computes a seed partition from the scenario's navigate
steps and returns it for *you* (the agent) to fan out:

```json
{
  "seeds": [
    {"index":0, "route":"/products"},
    {"index":1, "route":"/products/1"},
    {"index":2, "route":"/products"},
    {"index":3, "route":"/products/1"}
  ],
  "success": true,
  "message": "computed 4 exploration seed(s); dispatch one browser-debugger subagent per seed, then feed crashes/new assertions back via refine"
}
```

Dispatch one `browser-debugger` subagent per seed, collect the crashes/new
assertions they find, and feed them back through `refine`.

### 6. Refine — mask volatile assertions with an LLM

```
replaytest {action:"refine", name:"product-list"}
```

`refine` asks an LLM to set `mask:true` on auto-captured assertions whose
expected value is volatile across runs (timestamps, counts, ids, dates) and keep
the stable, high-signal ones. It uses the in-process Anthropic provider, which
reads `ANTHROPIC_API_KEY` or `CLAUDE_KEY` from the **daemon's** environment.

Without a key, it returns honest guidance rather than failing:

```json
{
  "message": "refine needs an LLM API key — set ANTHROPIC_API_KEY or CLAUDE_KEY in the daemon's environment, then re-run. ...",
  "success": false
}
```

With a key present, it rewrites and re-saves the scenario, returning the refined
JSON in `report`.

## Gotchas

- **`record` / `stop` are alpha and not wired on this build.** They return an
  explanatory message, not a captured scenario. The full-fidelity transport
  they need (`ProxyLogQueryFull` → typed `[]proxy.LogEntry` with response bodies
  + headers) now exists, but the end-to-end capture→`AssembleScenario` wiring
  lives on branch `feat/replaytest-record-stop` and is not merged here. Until it
  lands, assemble scenarios from a process with direct `ProxyManager` access
  (`replaytest.AssembleScenario` + `Store.SaveScenario`).
- **`replay`/`explore`/`refine`/`record`/`stop` are Pro-gated**
  (`advanced_testing`); `list`/`show` are free. Gated actions check the license
  before doing any work.
- **`replay` drives a real headless browser** even though it needs no server —
  the network is mocked in-page, but chromedp still renders the app. It is not a
  pure-in-memory unit test.
- **`explore` never runs anything.** It only computes and returns seeds; the
  fan-out to subagents is the calling agent's job. Treat its output as a work
  list, not a result.
- **`refine`'s API key must be in the daemon's environment**, not just your
  shell — the provider runs in the daemon process. A missing key yields a
  `success:false` guidance message, not an error.
- **The Go↔JS matcher parity is load-bearing.** `buildKey` (Go) and `recKey`
  (worker) must stay mirrored; if request matching diverges between the driver
  and the in-page worker, replays silently miss recorded responses. Keep them in
  sync when touching either side.
- **Scenario store is session-project-scoped by default.** Pass `directory` to
  target another project's store.
