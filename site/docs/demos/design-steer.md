---
id: design-steer
title: "Steer it. Iterate. Then build."
description: Design mode steered by chat prompts — a horizontal strip, then tighter — with the scheme preserved by contract, then the winner shipped to the page.
---

import DemoVideo from '@site/src/components/DemoVideo';

# Steer it. Iterate. Then build.

Defaults keep you on-scheme; prompts set the direction. Every chat message
rides with an explicit contract — `preserve: palette, type, spacing · vary:
layout, hierarchy, density · steer: your words` — so iteration changes the
design without ever drifting off-system. When a round wins, the agent ships
it.

<DemoVideo
  src="/video/design-steer.webm"
  poster="/img/design-steer-poster.webp"
  label="Demo: the Revenue card is steered via chat to a horizontal strip, iterated tighter in a second round, and the winning alternative lands in the live page." />

*Silent by design — 0:34. Every beat is legible with the sound off.*

## What happens

**Round 1 — steer (0:07).** *"make it a horizontal strip — label left, number
right."* The alternatives arrive as strips: structure changed, palette and
type untouched.

**Round 2 — iterate (0:17).** *"tighter — smaller number, drop the arrow."*
Same contract, new direction: a compact strip lands.

**Build (0:26).** The winner ships. Design mode is deliberately preview-only —
it never writes into the target subtree — so the ship step is the agent's
normal path: a source edit and an HMR reload, shown here as the direct DOM
change it produces.

## How it's made

Recorded by the scripted demo engine against the real stack; every chat
message is a real `design_chat` event with the full constraints block.
`make demo NAME=design-steer` re-records it.
