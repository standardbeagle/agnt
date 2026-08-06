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
2.5–3.5s. Same page, same code — the spinners crawl.

**Error rule (0:22).** Rules swapped: every `/api/report` call now fails with
a 500. The page tells you *nothing* — the demo code ignores failed fetches.
The incident inbox tells you everything: each 500 landed with the request
attached.

**Clear (0:34).** `CHAOS CLEAR` and the cascade is back to mock latency. The
unhappy path is a switch you can flip, not a deployment you have to wait for.

## How it's made

Recorded by the scripted demo engine against the real stack. The chaos rules
are real `CHAOS` daemon commands issued mid-take through the engine's daemon
client — the same engine your MCP `chaos` tool drives. `make demo
NAME=chaos-testing` re-records it.
