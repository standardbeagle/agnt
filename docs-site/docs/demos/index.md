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
