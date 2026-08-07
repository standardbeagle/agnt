---
id: chaos-testing
title: "Break your API on purpose"
description: Chaos rules inject 3s latency and hard 500s at the proxy — the page swallows the failures, the incident inbox doesn't. Rules cleared, the page is fast again.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Break your API on purpose

Your app talks to a real network. Chaos engineering at the proxy lets you
answer "what happens when it's slow / down / lying" without touching code or
rewriting mocks — and because the proxy sees everything, the failures your
page *swallows* still land in the incident inbox.

<DemoVideo
  src="/video/chaos-testing.webm"
  poster="/img/chaos-testing-poster.webp"
  label="Demo: a report-loading cascade runs at mock latency, then a chaos latency rule makes spinners crawl, then an http_error rule makes every call fail with a 500 that the page ignores but the inbox catches; rules cleared, the cascade is fast again." />

*Silent by design — 0:48. Every beat is legible with the sound off.*

## What happens

**Baseline (0:04).** The dashboard's report cascade (three cards + a status
sequence) completes in about two seconds at mock latency.

**Latency rule (0:12).** `CHAOS ADD-RULE`: `/api/report` now answers in
2.5–3.5s. A labeled spinner — *"Loading revenue report… (waiting on
/api/report)"* — tracks a real awaited fetch, and when it outstays the 0.4s
baseline the page puts up a **"Things are taking longer than usual"** banner.
The wait on screen is the actual response time.

**Error rule (0:22).** Rules swapped: every `/api/report` call now fails with
a 500. The cards that never got data are labeled in place — **"HTTP 500 — no
data"** — and each swallowed failure surfaces as a toast *with its fix*:
"the report handler is throwing; payload valid, handler not."

**Clear (0:38).** `CHAOS CLEAR` and the cascade is back to mock latency, real
values replacing the labels. The unhappy path is a switch you can flip, not a
deployment you have to wait for.

## How it's made

Recorded by the scripted demo engine against the real stack. The chaos rules
are real `CHAOS` daemon commands issued mid-take through the engine's daemon
client — the same engine your MCP `chaos` tool drives. `make demo
NAME=chaos-testing` re-records it.
