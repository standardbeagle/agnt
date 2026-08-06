---
id: drag-resize
title: "Drag until it breaks"
description: The responsive workbench's edge drag handle, pulled in one continuous motion — findings light up as the layout crosses its breakpoints, then the break goes to the agent.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Drag until it breaks

No presets, no numeric input: grab the edge of the live page and pull. The
responsive workbench re-renders at every pixel of the drag and flags each
element as it breaks — and when you stop, "Send to agent" hands the exact
width and the measured overflow over in one click.

<DemoVideo
  src="/video/drag-resize.webm"
  poster="/img/drag-resize-poster.webp"
  label="Demo: the responsive workbench's edge handle is dragged from full width down to 320px in one motion, overflow findings lighting up as the layout breaks, then Send to agent hands the break off with measurements." />

*Silent by design — 0:29. Every beat is legible with the sound off.*

## What happens

**The drag (0:04).** One continuous pull on the frame's edge handle, 1440px
down to 320px. The overlay recomputes live — watch the overflow flags bloom
as the dashboard crosses each breakpoint.

**The measurement (0:15).** The page needs 958px inside a 320px frame. Not
"looks cramped" — a number, measured from the live DOM.

**The handoff (0:19).** *Send to agent* ships the break with its context.
The agent's reply names the causes: fixed three-column grids, a fixed sidebar,
a `min-width: 640px` table.

For the fix half of this loop, see [Shrink the page. Watch it
break.](./resize-components.md) — same workbench, driven to a verified fix.

## How it's made

Recorded by the scripted demo engine against the real stack; the drag is real
mouse input on the actual drag handle. `make demo NAME=drag-resize`
re-records it.
