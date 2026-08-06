---
id: incident-inbox
title: "The bug that reports itself"
description: Real failures stream through the proxy into the daemon event feed — watched live with agnt monitor, triaged by the agent with zero log-pasting.
---

import DemoVideo from '@site/src/components/DemoVideo';

# The bug that reports itself

Two failing API calls, fired through the proxy with `curl`. No browser open,
no one watching a log. The daemon's event stream catches both, `agnt monitor`
shows them landing live, and the agent triages the inbox with full request
context — before anyone pastes a single log line.

<DemoVideo
  src="/video/incident-inbox.webm"
  poster="/img/incident-inbox-poster.webp"
  label="Demo: agnt monitor streams two real failures (HTTP 500 and 409 on POST /api/customer) as they happen; the agent then triages both incidents in the browser overlay with root-cause hints." />

*Silent by design — 1:11. Every beat is legible with the sound off.*

## What happens, step by step

**The stream (0:04).** `agnt monitor --proxy demo-inc` tails the daemon event
feed in the background. Two `curl` calls go through the proxy: one triggers a
server-side 500, one a 409 duplicate-company conflict. Both responses come
back as ordinary JSON — nothing screams.

**The reveal (0:32).** `cat` of the monitor log: both failures were captured
as structured events — `[http:500] POST /api/customer → …`,
`[http:409] POST /api/customer → …` — with method, path, status, and payload.

**The triage (0:47).** In the browser, the agent pulls the same incidents from
the inbox and reads them back with remediation hints: the 500 is server-side
("payload is valid, the handler is not"), the 409 means the client should
pre-check. Nobody pasted anything.

## How it's made

Recorded by the scripted demo engine
([`docs-site/screenshots/engine/`](https://github.com/standardbeagle/agnt/blob/main/docs-site/screenshots/README.md)).
The terminal segment is a VHS tape running the real `agnt monitor` CLI and
real `curl` calls against a live proxy; the triage segment runs against the
same proxy in a real browser, with the agent's toasts travelling the actual
`PROXY TOAST` daemon transport. `make demo NAME=incident-inbox` re-records it.
