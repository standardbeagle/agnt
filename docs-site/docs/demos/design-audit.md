---
id: design-audit
title: "The AI-slop detector"
description: A gorgeous AI-generated landing page gets graded F by auditDesign's 59 Impeccable rules — then fixed live and re-graded A, without anyone looking at a screenshot.
---

import DemoVideo from '@site/src/components/DemoVideo';

# The AI-slop detector

The page looks great. Purple-blue gradient hero, glowing accents, gradient
display type — every current AI-design tell, executed well. `auditDesign`
doesn't care how it *looks* to you: 59 deterministic rules grade the live DOM
an F. Then the agent removes the tells and the re-audit comes back A —
no screenshots, no taste arguments, just rules.

<DemoVideo
  src="/video/design-audit.webm"
  poster="/img/design-audit-poster.webp"
  label="Demo: an AI-slop landing page (gradient text, purple-blue palette, nested cards, eyebrow chip) grades F 10/100 under auditDesign; the agent removes the tells live and the re-audit returns A 100/100." />

*Silent by design — 0:33. Every beat is legible with the sound off.*

## What happens, step by step

**The page (0:04).** A polished AI-generated landing page: gradient hero,
eyebrow chip with pulsing dot, gradient display text, nested feature cards,
glow shadows. To a human eye: fine. Modern, even.

**The verdict (0:14).** The agent runs `auditDesign()` — the vendored
[Impeccable](https://impeccable.style) detector delay-loads from the proxy on
first call — and the grade comes back **F (10/100)**: 14 anti-patterns.
gradient-text ×2, ai-color-palette ×5, dark-glow ×2, icon-tile-stack ×3,
gray-on-color, low-contrast, side-tab, pulsing-dot. The flagged elements get
outlined on the page.

**The fix (0:22).** The agent swaps the stylesheet and flattens the structure:
gradient text becomes solid, the palette goes neutral, nested cards flatten,
chip and kickers come out, buzzword copy is replaced with plain sentences.

**The re-audit (0:27).** **A (100/100)** — every finding cleared, verified by
the same deterministic rules that convicted it.

## Why the fix is a stylesheet swap

One finding from recording this demo is worth knowing: Impeccable's detector
reads every stylesheet *in the DOM* — including disabled ones. Overriding
slop styles with a "fixed" class still grades an F, because the rules are
still there to be read. The honest fix is removing the stylesheet node
entirely (or never shipping it), which is what the demo does.

## How it's made

Recorded by the scripted demo engine
([`docs-site/screenshots/engine/`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md))
against the real stack — live daemon, proxy injecting the bundle, the audit's
366KB detector delay-loaded from `/__devtool_impeccable` exactly as in a real
session. The demo page ships both stylesheets so the before/after is a DOM
operation. `make demo NAME=design-audit` re-records it.
