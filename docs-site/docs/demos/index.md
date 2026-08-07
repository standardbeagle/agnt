---
id: index
title: Demos
description: Narrated, real-stack demo videos of agnt workflows — recorded against a live daemon, proxy, and agent.
---

# Demos

Narrated walkthroughs of complete agnt workflows. Every demo here is recorded
against the **real stack** — a live `agnt` daemon, the reverse proxy injecting
the `__devtool` bundle into a running app, and a live AI agent answering over
the daemon channel. Nothing is mocked up in an editor: the toasts, diagnostics,
and agent replies you see travelled the same transport your own sessions use.

| Demo | What it covers | Length |
|------|----------------|--------|
| [The VHS spiral](./vhs-spiral.md) | A button half off-screen: three rounds of blind terminal automation fail, then one measured pass through the proxy fixes it | 1:40 |
| [The bug that reports itself](./incident-inbox.md) | Two failing API calls caught by `agnt monitor` live, then triaged from the incident inbox with remediation hints | 1:11 |
| [The AI-slop detector](./design-audit.md) | `auditDesign` grades a gorgeous AI-generated landing page F (10/100), the agent removes the tells live, re-audit returns A (100/100) | 0:33 |
| [On-scheme by default](./design-defaults.md) | Design-mode alternatives stay in the site's design system unasked — scheme, slot, exemplars, and a page thumbnail ride the request | 0:31 |
| [Steer it. Iterate. Then build.](./design-steer.md) | Chat prompts steer alternatives via the preserve/vary/steer contract; the winner ships to the live page | 0:34 |
| [Draw it, don't describe it](./live-sketch.md) | Sketch mode: draw on the live app, the agent gets the drawing plus the elements under it | 0:32 |
| [Sketch the layout. The agent builds it.](./sketch-build.md) | A stat-card row wireframed and annotated on the live app, then built as real DOM in the app's own style | 0:39 |
| [Shrink the page. Watch it break.](./resize-components.md) | Responsive mode sweep exposes 544px of sideways scroll at 414px; the agent's media query fixes it, verified at 320px | 0:36 |
| [Drag until it breaks](./drag-resize.md) | The workbench's edge drag handle in one continuous pull — findings bloom live, then Send-to-agent hands off the measured break | 0:29 |
| [Break your API on purpose](./chaos-testing.md) | Chaos latency + 500 rules at the proxy; the page swallows the failures, the incident inbox doesn't | 0:48 |
| [The defect is three pages in](./navigate-defect.md) | "Take me there" — the agent drives the browser to a panel two levels down and measures the defect on arrival | 0:31 |
| [From bug report to e2e coverage](./debug-to-e2e.md) | Floating-panel bug report → layout diagnostics + CSS audit → site-wide regression check (red → fix → green) → e2e tests for every failure path of a dynamic form | 1:37 |

Shorter, silent feature clips live inline throughout the docs — see the
[frontend API pages](../api/frontend/indicator.md) for the per-tool loops.

## How these are made

The demo engine lives in the repo at `docs-site/screenshots/engine/`: each
demo is a `demo.json` composing VHS-recorded CLI segments with Playwright
browser segments running against the real stack — a live daemon, the proxy
injecting the bundle, and agent replies travelling the real daemon transport —
then assembled with title cards, optional neural TTS narration, and captions.
`make demo NAME=<demo>` regenerates a demo when the UI changes. The pipeline is
documented in
[`docs-site/screenshots/README.md`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md).
