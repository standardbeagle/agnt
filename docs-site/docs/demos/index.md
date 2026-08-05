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
| [From bug report to e2e coverage](./debug-to-e2e.md) | Floating-panel bug report → layout diagnostics + CSS audit → site-wide regression check (red → fix → green) → e2e tests for every failure path of a dynamic form | 1:37 |

Shorter, silent feature clips live inline throughout the docs — see the
[frontend API pages](../api/frontend/indicator.md) for the per-tool loops.

## How these are made

The demo harness lives in the repo at `docs-site/screenshots/`: a static demo
app served behind a real proxy, driven by Playwright while a live agent
answers panel messages, then assembled with section title cards, neural TTS
narration, and captions. The pipeline is documented in
[`docs-site/screenshots/README.md`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md),
so demos are re-recordable when the UI changes.
