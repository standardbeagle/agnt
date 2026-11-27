---
id: navigate-defect
title: "The defect is three pages in"
description: The dev reports a wrong invoice total through the floating panel; the agent drives the browser two levels down — real proxy navigation — and measures the defect on arrival.
---

import DemoVideo from '@site/src/components/DemoVideo';

# The defect is three pages in

"The total on invoice INV-1042 looks wrong. Take me there." No repro steps,
no clicking through menus — the agent navigates the browser to the panel and
starts measuring the moment it arrives.

<DemoVideo
  src="/video/navigate-defect.webm"
  poster="/img/navigate-defect-poster.webp"
  label="Demo: the dev types 'The total on invoice INV-1042 looks wrong. Take me there.' into the floating panel; the agent navigates Dashboard → Invoices → INV-1042 through the proxy and confirms the $2,100 total is $450 above the line-item sum." />

*Silent by design — 0:31. Every beat is legible with the sound off.*

## What happens

**The report (0:04).** The dev types the defect into the floating panel —
invoice number, symptom, and "take me there."

**The drive (0:10).** The agent navigates: Dashboard → Invoices → INV-1042.
These are real page loads through the proxy, the same path as
`proxy {action:"navigate", direction:"goto"}` — the bundle re-injects on
every hop.

**The measurement (0:19).** On arrival the agent sums the line items against
the displayed total: $1,200 + $450 = **$1,650**; the total says **$2,100**.
Defect confirmed, $450 discrepancy, no repro steps needed — the investigation
started *on the panel*.

## A fix this demo drove

Recording the two-hop navigation surfaced a real bug: after any page
navigation, `PROXY EXEC` hung permanently — the frame id lives in a URL
marker that `location.assign` drops, so the re-injected page reported no
frame identity and the daemon's exec routing pointed at a ghost. Fixed in
0.15.4 by persisting frame identity across in-frame navigations
(`frames.js`); multi-hop agent navigation works because of it.

## How it's made

Recorded by the scripted demo engine against the real stack — real panel
typing, real daemon navigation, real measurement. `make demo
NAME=navigate-defect` re-records it.
