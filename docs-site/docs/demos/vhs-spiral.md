---
id: vhs-spiral
title: "The VHS spiral: blind automation vs. seeing the page"
description: A submit button rendered half off-screen. Three rounds of scripted terminal automation fail to fix it — then agnt, with eyes on the live page, fixes it in one pass.
---

import DemoVideo from '@site/src/components/DemoVideo';

# The VHS spiral

A submit button sits half off the screen. The fix is one CSS change — if you
can *see* the page. Watch three rounds of blind terminal automation try and
fail, then the same bug fixed in a single pass once the agent can measure the
live page through the proxy.

<DemoVideo
  src="/video/vhs-spiral.webm"
  poster="/img/vhs-spiral-poster.webp"
  label="Demo: a submit button is half off-screen. Three scripted terminal automation attempts fail — a blind edit, a pasted screenshot description, a pasted DOM dump. Then agnt measures the live page, names the exact clipping in pixels, and fixes it in one pass." />

*Silent by design — 1:40. Every beat is legible with the sound off.*

## What happens, step by step

**The bug (0:04).** The Add-customer form's submit button renders half off the
right edge of the viewport. One line of CSS caused it; finding that line is
the whole game.

**Attempt 1 — blind prompt (0:11).** "The submit button is half off the
screen. Fix it." The agent guesses a margin issue, edits a stylesheet it
cannot see the effect of, and declares victory. The button doesn't move.

**Attempt 2 — paste a screenshot description (0:29).** More context, same
blindness: the agent guesses `position: absolute; left: 40px` — and now the
button is gone entirely.

**Attempt 3 — paste the DOM and computed styles (0:53).** Maximum duct tape.
The agent reverts, tries flexbox, tries again, and lands exactly where it
started: the button is still half off-screen.

**The turn (1:17).** The terminal was blind the whole time.

**One pass with agnt (1:21).** Same bug, same page — but now the agent is
behind the agnt proxy. It measures the button's real box against the real
viewport (`right edge 1456px, viewport 1440px — 16px clipped`), names the root
cause, applies the fix, and re-measures green. No screenshots pasted, no DOM
dumps, no guessing.

## How it's made

Recorded by the scripted demo engine in
[`docs-site/screenshots/engine/`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md):
the terminal rounds are VHS tapes driving a deterministic scripted session,
and the browser beats run against the real stack — a live daemon, the reverse
proxy injecting the `__devtool` bundle, and the agent's replies travelling the
same `PROXY TOAST` transport your own sessions use. `make demo NAME=vhs-spiral`
re-records the whole thing.
