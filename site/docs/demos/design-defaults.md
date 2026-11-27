---
id: design-defaults
title: "On-scheme by default"
description: Design mode alternatives stay inside the site's design system without being told to — because the request carries the scheme, the slot geometry, sibling exemplars, and a page thumbnail.
---

import DemoVideo from '@site/src/components/DemoVideo';

# On-scheme by default

Select a card, ask for alternatives, and what comes back already speaks the
site's design language. Not because the prompt said "match the site" — the
request itself carries the design tokens, the parent grid, the sibling
components, and a thumbnail of the whole page. Variation happens on layout,
hierarchy, and density. Palette, type, and radius don't move.

<DemoVideo
  src="/video/design-defaults.webm"
  poster="/img/design-defaults-poster.webp"
  label="Demo: the Revenue card is selected in design mode; three alternatives arrive — split, centered, dense-row layouts — all in the site's own palette, typography, and radii, cycled in the preview dock." />

*Silent by design — 0:31. Every beat is legible with the sound off.*

## What happens

**Select (0:04).** The Revenue card is selected. The `design_state` event
carries: the extracted scheme (8 palette colors, type scale, spacing, radii,
CSS vars), the slot (parent grid: three 377px tracks, 16px gap), two sibling
cards as exemplars, and a whole-page JPEG thumbnail.

**Generate (0:11).** Three alternatives land in the preview dock: a split
layout (label left, delta right), a centered number-first layout, a dense row
with a divider. Three structures — zero new colors.

**Cycle (0:16).** `previous()` walks the options side-by-side with the real
card. Every preview renders in an iframe carrying the page's own stylesheets
and theme — what you see is what the page would look like.

## How it's made

Recorded by the scripted demo engine against the real stack on the 0.15.2+
payload. `make demo NAME=design-defaults` re-records it.
