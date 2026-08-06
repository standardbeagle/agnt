---
id: resize-components
title: "Shrink the page. Watch it break."
description: Responsive mode's live width sweep exposes a fixed desktop layout at phone widths — the agent measures the overflow, adds a media query, and re-measures clean.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Shrink the page. Watch it break.

"It works on my viewport" is the mobile-layout bug every agent ships blind.
Responsive mode drags the live page through laptop, tablet, and phone widths
while the overlay flags what's breaking — then the agent measures the actual
overflow, fixes it, and proves the fix at the same width.

<DemoVideo
  src="/video/resize-components.webm"
  poster="/img/resize-components-poster.webp"
  label="Demo: responsive mode sweeps the dashboard down to 414px where it renders 958px wide; the agent adds a phone media query and the re-check fits the frame, verified again at 320px." />

*Silent by design — 0:36. Every beat is legible with the sound off.*

## What happens

**The sweep (0:04).** Responsive mode opens and steps the live dashboard
through 1024px → 768px → 414px. The overlay lights up: fixed three-column
grids, a 220px sidebar, a table with `min-width: 640px` — nothing reflows.

**The measurement (0:15).** Not vibes, a number: at 414px the page renders
**958px wide — 544px of sideways scroll**.

**The fix (0:21).** The agent adds a phone media query: collapse the sidebar,
single-column grids with `minmax(0, 1fr)` (the grid-blowout fix), wrap the
header, and let the table's panel scroll instead of clip.

**The proof (0:26).** Same width, re-measured: **page fits the frame**. Then
one step further — 320px, still fits.

## How it's made

Recorded by the scripted demo engine against the real stack; every number on
screen is measured from the live page during the take. `make demo
NAME=resize-components` re-records it.
