# replaytest

License-gated (`advanced_testing`) **record→worker-mock→replay** pipeline for
deterministic front-end testing.

## Overview

`replaytest` captures live API traffic into a scenario, then replays it by
serving the recorded responses from an **in-page web worker**. Because the
worker answers the app's `fetch`/XHR calls in-process, front-end automation runs
**fully local and deterministic** — no real backend, no network flake. On top of
replay, **fuzz mutators** perturb the recorded responses to probe how the
front-end handles malformed or degenerate data, and an **explore** step
partitions the scenario into seeds so the calling agent can fan out
browser-debugger subagents for breadth coverage.

All record/refine/replay/explore actions require a Pro license with the
`advanced_testing` capability (`agnt activate <key>`). `list` and `show` are
free.

## Actions

| Action | License | Status | Description |
|--------|---------|--------|-------------|
| `list` | free | works | List scenarios in `.agnt/replaytests/`. |
| `show` | free | works | Show a scenario / its last report. |
| `record` | advanced_testing | **PENDING** | Capture proxy traffic into a scenario — not yet wired (see Limitations). |
| `stop` | advanced_testing | **PENDING** | Finalize a recording into a scenario — not yet wired (see Limitations). |
| `refine` | advanced_testing | works (needs key) | LLM-assisted scenario cleanup. Requires `ANTHROPIC_API_KEY` or `CLAUDE_KEY` at runtime. |
| `replay` | advanced_testing | works | Drives headless Chrome, mocks the network in-page from the recorded responses, asserts via DOM signature. |
| `explore` | advanced_testing | works | Returns a seed partition for the calling agent to fan out browser-debugger subagents. |

## Scenario files

Scenarios and reports live under the project:

- `.agnt/replaytests/<name>.json` — the scenario (recorded requests/responses + assertions).
- `.agnt/replaytests/<name>.report.json` — the most recent run report.

## Fuzz presets

`replay` can apply a fuzz preset to mutate recorded responses before serving
them:

| Preset | Effect |
|--------|--------|
| `empty_array` | Replace array payloads with `[]`. |
| `http_error` | Return an HTTP error status instead of the recorded 2xx. |
| `truncated_json` | Cut the JSON body short. |
| `null_fields` | Null out object fields. |
| `reordered` | Reorder array elements / object keys. |
| `type_flip` | Flip value types (string↔number, etc.). |

## Current status / limitations

`record` and `stop` are **not yet wired**. Assembling a scenario from captured
traffic needs full-fidelity `[]proxy.LogEntry` data — response bodies and
headers — but the cross-process `PROXYLOG QUERY` protocol verb returns compacted
entries that drop response bodies. Closing the gap requires a **new daemon
protocol verb that returns full-fidelity `[]proxy.LogEntry` over the wire**;
this is tracked as a follow-up task.

Until then, scenarios must be **hand-authored** (or produced by a future record
path / a process with direct `ProxyManager` access via
`replaytest.AssembleScenario` + `Store.SaveScenario`). `replay`, `explore`, and
`refine` are functional today.

## Dogfood note

Once `record` lands, the intended end-to-end flow against any proxied app
(food-track is the first target) is:

```
start proxy → record → drive the app → stop → refine → replay across presets → explore
```
