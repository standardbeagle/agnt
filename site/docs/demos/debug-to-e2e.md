---
id: debug-to-e2e
title: "From bug report to e2e coverage"
description: A live agent diagnoses a CSS containing-block trap through the floating proxy UI, locks the fix in with a site-wide regression check, and generates e2e tests for every failure path of a dynamic form.
---

import DemoVideo from '@site/src/components/DemoVideo';

# From bug report to e2e coverage

One CSS bug, taken all the way: reported in the floating panel, diagnosed with
the audit tools, locked in with a site-wide regression check, and covered with
e2e tests for every failure condition — with a live agent on the other side of
the proxy the whole time.

<DemoVideo
  src="/video/debug-to-e2e-narrated.webm"
  poster="/img/debug-to-e2e-narrated-poster.webp"
  captions="/video/debug-to-e2e-narrated.vtt"
  label="Narrated demo: a live agent diagnoses a CSS containing-block trap through the floating proxy UI, fixes it, proves the fix with a site-wide regression check, and generates e2e tests for all seven failure conditions of a dynamic form." />

*Narration and captions included — 1:37. Captions are burned in; the CC toggle
carries the same text for assistive tech.*

## What happens, step by step

**Part 1 — Diagnose (0:05).** The developer types the bug report into the
floating panel — *"the standup note swallows clicks and the export menu is cut
off"* — and it travels over the proxy's channel to the agent, which answers in
the same overlay. The agent runs
[`diagnoseLayoutIssues()`](../api/frontend/layout-diagnostics.md): five issues,
each outlined on the page (symptom in red, offending ancestor in dashed
amber). Root cause: a `translateZ(0)` hack creating a containing block that
traps a `position:fixed` note — plus a clipped dropdown and a dead `z-index`.
A [CSS audit](../api/frontend/quality-auditing.md) grades the stylesheet.

**Part 2 — Lock it in (0:36).** The diagnosis is re-run as a site-wide
regression check: **FAIL, 5 violations**. The agent applies the fix — drop the
transform, un-clip the card, position the filters — and the same check runs
**PASS, 0 violations**, with a [snapshot baseline](../api/snapshot.md) saved
for CI. Then the fix is proven by hand: the previously-clipped export menu
renders in full and takes clicks, and the previously-trapped note answers.

**Part 3 — Cover it (0:54).** The agent drives the *Add customer* form through
all seven of its failure conditions — a passing baseline, empty required
fields, an invalid email, seats out of range, a duplicate company (HTTP 409),
an Enterprise seat-rule violation (HTTP 422), and a raw server 500 — each one
observed against the real mock API and recorded as a
[replaytest](../features/replay-testing.md) e2e case.

## Why this is a real recording

The take runs against a live `agnt` daemon: the proxy injects the bundle into
the demo app exactly as it does for your dev server, the panel message is a
real `panel_message` over the metrics WebSocket, and both agent replies are
real `PROXY TOAST` pushes sent by a live agent while the recording ran. The
only editing is splicing out agent think-time and adding title cards,
narration, and captions.
