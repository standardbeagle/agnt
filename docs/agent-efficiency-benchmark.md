# Agent Efficiency Benchmark (agentbench)

`internal/agentbench` scores a JSON trace of one AGNT debugging session
against six efficiency metrics, and pins a **documented-workflow baseline**
for eight canonical debugging scenarios.

## What the baseline is — and is not

The baseline traces under `internal/agentbench/testdata/baseline/` are the
**call sequence today's shipped guidance prescribes**, transcribed from:

- the marketplace browser-debug skill
  `standardbeagle-tools/plugins/agnt/skills/browser-debug/SKILL.md`
  (quick-start: screenshot → check errors → `__devtool.diagnoseLayout()`; and
  the per-problem workflows: dead-click, horizontal overflow, z-index,
  mutation-rate debugging), and
- the remediation route primaries in `internal/incident/remediation.go`
  (e.g. `SourceHTTP5xx` → `proxylog` with 5xx status filter, fallback
  `proc output`).

Every trace file carries a `provenance` field naming those source file and
line ranges.

The baseline is **not a live-model recording**. None exists: the agnt hook
ring buffer in `internal/daemon/hub_hook.go` is a transient toast feed, not a
persisted trace. The baseline is recorded as-is; this slice makes **no claim
of improvement** over it. It is the fixed reference a future trace-replay
gate (S6) diffs live traces against.

## Trace schema

```json
{
  "scenario": "dead_click",
  "provenance": "documented-workflow baseline; transcribed from ...",
  "steps": [
    {
      "tool": "proxy",
      "action": "exec",
      "args": {"id": "dev"},
      "code": "__devtool.interactions.getLastClick()",
      "response_bytes": 1400,
      "kind": "exec",
      "useful": true,
      "finding_kind": "layout"
    }
  ]
}
```

- `tool` (required), `action`, `args`, `code` — the call as issued.
- `response_bytes` (required, ≥ 0) — size of the tool response returned to
  the agent.
- `kind` (required) — one of `state`, `incidents`, `audit`, `composite`,
  `exec`, `screenshot`, `other`.
- `useful` — the step that produced a real finding toward the root cause.
- `finding_kind` — classifies a useful step; `layout` / `responsive` /
  `visual` anchor the screenshot metric.

Malformed traces (missing `tool`, negative `response_bytes`, unknown `kind`)
are rejected wholesale: `Score` returns an error and no partial report.

## Metrics

| Metric | Definition |
|---|---|
| `calls_to_first_finding` | 1-based index of the first step flagged `useful=true`; 0 if none. |
| `returned_bytes` | Sum of `response_bytes` across all steps. |
| `tokens_estimate` | `returned_bytes / BytesPerToken` (default 4). |
| `repeated_state_reads` | `currentpage` / `proxy status` calls beyond the first with identical args. |
| `raw_js_steps` | `kind=exec` steps whose code does not begin with `__devtool.`, `window.__devtool`, or `await __devtool` — i.e. raw page JS bypassing the shipped helpers. |
| `broad_audit_count` | Audit steps with no `selector`/`area` and a full (or default) profile. The second and later occurrences are the repeated waste `MaxBroadAudits` policies against. |
| `screenshot_before_visual_hypothesis` | True when a screenshot step precedes the first `layout`/`responsive`/`visual` finding — the SKILL.md step-one screenshot pattern. |

All numeric policy lives in one exported struct, `agentbench.Thresholds`,
with `DefaultThresholds()`. Tests assert against the struct; no numeric
threshold literal appears inside a test body.

## API

```go
tr, err := agentbench.LoadScenario("dead_click") // testdata/baseline/dead_click.json
rep, err := agentbench.Score(tr)                 // DefaultThresholds
rep, err := agentbench.ScoreWithThresholds(tr, th)
```

`Score` and `LoadScenario` are the only entry points. The scenario catalogue
is `internal/agentbench/scenarios.go`; names must match
`testdata/baseline/<name>.json` 1:1 (pinned by
`TestScenarioCatalogueMatchesTestdata`).

## Scenarios

`blank_page`, `dead_click`, `mobile_overflow`, `zindex_positioning`,
`api_failure`, `loading_flicker`, `a11y_failure`, `release_qa`.

## Viewing the baseline table

```
go test -run TestAgentBenchBaselineReport -v ./internal/agentbench/
```

prints one compact row per scenario (steps, first-finding index, bytes,
tokens, repeated state reads, raw-JS steps, broad audits, screenshot flag).

## Adding a scenario

1. Add the name and blurb to `scenarios` in
   `internal/agentbench/scenarios.go`.
2. Write `internal/agentbench/testdata/baseline/<name>.json` transcribing the
   documented workflow for that problem; set `provenance` to the source file
   and line ranges you transcribed from.
3. Mark the step(s) that actually find the root cause with `useful: true` and
   a `finding_kind`.
4. Run `go test ./internal/agentbench/` — the catalogue↔testdata 1:1 pin and
   the load-and-score test cover the new scenario automatically.
