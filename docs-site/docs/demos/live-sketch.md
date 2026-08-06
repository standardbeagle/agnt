---
id: live-sketch
title: "Draw it, don't describe it"
description: Sketch mode on the live app — a rectangle around the Revenue card, an arrow from the invoices table — and the drawing travels to the agent with the elements under it.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Draw it, don't describe it

The slowest, most lossy part of working with an agent on a UI is describing
what you mean: "the card at the top left, no the other one." Sketch mode cuts
the description out — draw directly on the running app, and the agent gets
the drawing *plus* the element context underneath it.

<DemoVideo
  src="/video/live-sketch.webm"
  poster="/img/live-sketch-poster.webp"
  label="Demo: sketch mode opens on the live dashboard; the user draws a red rectangle around the Revenue card and an arrow from Recent invoices, sends the sketch, and the agent acknowledges exactly what was drawn." />

*Silent by design — 0:32. Every beat is legible with the sound off.*

## What happens

**Open (0:04).** Sketch mode opens over the live dashboard — full toolbar,
style panel, dot grid. The app keeps running underneath.

**Draw (0:10).** A rectangle around the Revenue card; an arrow from Recent
invoices up to it. (Recording note: the default ink is `#1e1e1e` — invisible
on a dark UI. The stroke color is set through the panel's own picker, which
listens on `change`, not `input`.)

**Send (0:17).** `sketch.save()` pushes the drawing over the real proxy
channel. The agent's reply names what was drawn and where — no screenshot to
attach, no prose to parse.

## How it's made

Recorded by the scripted demo engine
([`docs-site/screenshots/engine/`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md))
against the real stack; the drawing is real mouse input on the actual sketch
canvas, and the agent's toasts travel the real `PROXY TOAST` transport.
`make demo NAME=live-sketch` re-records it.
