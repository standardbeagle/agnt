# agnt Walkthroughs

Task-oriented, developer-facing walkthroughs. Each one follows the same shape —
*What it is / Why it's unique / Real-world scenario / Step by step / Gotchas* —
and drives real agnt tools against a running app rather than describing them in
the abstract.

New to agnt? Start with [`agnt run` as your daily driver](agnt-run.md) — it's the
harness the rest of these live inside.

## Debug

Find and fix what's actually broken in the running page.

- **[Layout diagnostics](layout-diagnostics.md)** — one pass turns a mystery
  ("why is this element unclickable / clipped / behind everything?") into a
  named cause, the offending ancestor, and the fix.
- **[Responsive audit](responsive.md)** — a marketing page that looks perfect on
  desktop and breaks on mobile: sweep viewports for overflow, sub-44px touch
  targets, and iOS zoom triggers, then fix and re-verify.
- **[Frame model & auth breakout](frame-model-auth-breakout.md)** — how the
  always-wrap chrome/content iframe model works, and how OAuth popups escape the
  content frame instead of dying inside it.
- **[Incident pipeline](incident-pipeline.md)** — one deduped, priority-ordered
  inbox for every signal source, with remediation hints, replacing the scramble
  across scattered error surfaces.
- **[API & loading audits](api-loading-audits.md)** — catch N+1 waterfalls,
  duplicate fetches, chatty loads, and spinner cascades over the recorded
  request/spinner timeline.

## Test

Prove behavior — and prove it stays fixed.

- **[Chaos injection](chaos.md)** — harden a dashboard's error handling by
  deliberately injecting latency, 500s, and drops with a deterministic seed;
  catch the errors the UI swallows silently, then replay the same seed to verify.
- **[Replaytest](replaytest.md)** — record real API traffic, serve it back from
  an in-page worker mock, and replay front-end tests deterministically with fuzz
  and subagent breadth.

## Collaborate

Close the loop between the browser, the human, and the agent.

- **[Visual collaboration](visual-collaboration.md)** — sketch-mode wireframes,
  design iteration, and floating-indicator messaging that push what the human
  points at straight into the agent's context.

## Run

The harness everything else runs inside.

- **[`agnt run` daily driver](agnt-run.md)** — synthetic-stdin push of browser
  events into the agent, the command palette, the ports & orphans panel, session
  process-group containment, and `agnt doctor` OS-truth reconciliation.
