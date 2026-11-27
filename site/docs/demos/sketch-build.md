---
id: sketch-build
title: "Sketch the layout. The agent builds it."
description: A new stat-card row wireframed and annotated on the live dashboard with sketch mode — then built by the agent as real DOM in the app's own design language.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Sketch the layout. The agent builds it.

Sketch mode isn't just red ink for bug reports. Here it's a construction
tool: draw three rectangles where a new stat-card row should live, label it,
send it — and the agent builds the row for real, in the dashboard's own card
style, positioned where you drew it.

<DemoVideo
  src="/video/sketch-build.webm"
  poster="/img/sketch-build-poster.webp"
  label="Demo: three rectangles and a '3 stat cards here' label sketched on the live dashboard are sent to the agent, which builds the row as real DOM cards matching the app's design language." />

*Silent by design — 0:39. Every beat is legible with the sound off.*

## What happens

**Construct (0:05).** Sketch mode opens on the live dashboard. Three
rectangles in a row, drawn over the invoices area — the shape of a new
stat-card row.

**Annotate (0:16).** A text label: *"3 stat cards here."* The sketch now
carries both the layout and the intent.

**Send (0:22).** `sketch.save()` pushes the drawing over the real proxy
channel. The agent reads it back: "a row of three stat cards under the
metrics. Building it now."

**Build (0:27).** Three new cards land in the page — Refunds, Chargebacks,
Net revenue — real DOM, real house style (`var(--panel)`, the dashboard's own
card markup), inserted exactly where the rectangles were drawn.

## How it's made

Recorded by the scripted demo engine against the real stack — real mouse
input on the actual sketch canvas (including the text tool's type-and-blur
commit), real `PROXY TOAST` agent replies. `make demo NAME=sketch-build`
re-records it.
