---
sidebar_position: 8
title: Replay Testing
description: Record live API traffic into a scenario, then replay it from an in-page web worker for deterministic front-end testing, with fuzz presets for error-path coverage.
---

# Replay Testing

`replaytest` captures live API traffic into a scenario file, then replays it by
serving the recorded responses from an **in-page web worker**. Because the worker
answers the app's own `fetch` and `XHR` calls in-process, front-end runs are fully
local and deterministic — no real backend, no network flake, no mock server to
keep in sync by hand.

On top of plain replay, **fuzz mutators** perturb the recorded responses so you
can see how the UI handles malformed or degenerate data, and **explore** splits a
scenario into seeds so the calling agent can fan out subagents for breadth.

:::info Pro feature
Record, refine, replay, and explore require a Pro license with the
`advanced_testing` capability (`agnt activate <key>`). `list` and `show` are free.
Recording is currently alpha. See [Licensing](#licensing) below.
:::

## The loop

```
start proxy → record → drive the app → stop → refine → replay across presets → explore
```

```javascript
replaytest {action: "record", name: "checkout", proxy_id: "app"}
// …click through the flow in your browser…
replaytest {action: "stop", name: "checkout"}
replaytest {action: "replay", name: "checkout"}
replaytest {action: "replay", name: "checkout", fuzz: "empty_array"}
```

## Actions

| Action | License | Description |
|--------|---------|-------------|
| `list` | free | List scenarios in `.agnt/replaytests/` |
| `show` | free | Show a scenario or its last run report |
| `record` | Pro | Capture API traffic + interactions against a running proxy (requires daemon mode) |
| `stop` | Pro | Finalize the recording into `.agnt/replaytests/<name>.json` |
| `refine` | Pro | LLM-assisted scenario cleanup (needs `ANTHROPIC_API_KEY` or `CLAUDE_KEY`) |
| `replay` | Pro | Drive headless Chrome, mock the network in-page, assert via DOM signature |
| `explore` | Pro | Return a seed partition for fanning out browser-debugger subagents |

## Scenario files

Everything lives under the project, so scenarios are reviewable and committable:

- `.agnt/replaytests/<name>.json` — the scenario (recorded requests/responses plus assertions)
- `.agnt/replaytests/<name>.report.json` — the most recent run report

## Fuzz presets

`replay` can mutate the recorded responses before serving them, which is the
cheapest way to find the error paths nobody wrote a fixture for:

| Preset | Effect |
|--------|--------|
| `empty_array` | Replace array payloads with `[]` |
| `http_error` | Return an HTTP error status instead of the recorded 2xx |
| `truncated_json` | Cut the JSON body short |
| `null_fields` | Null out object fields |
| `reordered` | Reorder array elements and object keys |
| `type_flip` | Flip value types (string ↔ number, etc.) |

## Licensing

agnt Pro uses **self-hosted, offline license validation** — the binary embeds only
a public key and verifies the signature locally. There is no runtime phone-home.

```bash
agnt activate <key>        # validate + install a license blob, offline
agnt license status        # state, email, expiry, days left, capabilities
agnt license deactivate    # remove the installed license
```

The dividing line is **breadth of operation**, not a fixed tool list: single-page
operations (responsive/API/loading/accessibility audits, snapshots, browser
debugging, process and proxy management) are free; whole-site, multi-page, and
application-wide work is Pro. A license that has passed its expiry keeps working
for a 14-day grace period, with a renewal warning, before Pro capabilities stop.

The license blob is stored per-user at `~/.local/state/agnt/license.lk`
(`$XDG_STATE_HOME/agnt/license.lk`), written atomically at mode 600.

## Current limitations

- Recording is alpha and requires daemon mode; the non-daemon path returns a clear
  "requires daemon mode" message rather than half-recording.
- `refine` needs `ANTHROPIC_API_KEY` or `CLAUDE_KEY` at runtime.
- Orphaned blobs from coalesced duplicate recordings are retained in the scenario
  JSON. Harmless, but a future GC pass will prune them.
