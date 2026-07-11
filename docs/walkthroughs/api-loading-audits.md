# Walkthrough: API-efficiency & loading-UX audits

## What it is

`api_audit` and `loading_audit` are two MCP tools that grade how a running page
talks to its backend and how it presents loading state, then hand the coding
agent a compact, fix-oriented report.

- **`api_audit`** reads the in-page fetch/XHR call buffer (`window.__devtool_api`)
  and flags four request-shape problems: `waterfall`, `n-plus-one`,
  `duplicate-call`, and `chatty-load`.
- **`loading_audit`** reads the in-page spinner/loader timeline
  (`window.__devtool_spinners`) and flags two loading-UX problems:
  `spinner-cascade` and `spinner-fragmentation`.

Both run over a proxy started by `proxy {action:"start"}`; the injected client
script records the buffers as you browse. Both return a scored summary
(`score` 0-100 + letter `grade`) plus per-finding detail, and both are
registered into `auditAll`'s weighted overall grade (api weight 1.1, loading
weight 1.1).

Source of truth: `internal/tools/api_audit.go`, `internal/tools/loading_audit.go`,
`internal/proxy/scripts/audit-api.js`, `internal/proxy/scripts/audit-loading.js`.

## Why it's unique

These two audits are **temporal**. Every other agnt audit (DOM, CSS, a11y,
security, SEO) is a pure snapshot: it inspects the DOM as it stands at call
time and is fully reproducible. The API and loading audits instead analyze a
*recording that accumulates while the page loads and while you interact with
it*:

- `api_audit` needs the fetch/XHR buffer to have been populated by an actual
  page load. An audit run against a page that has been sitting idle sees an
  empty buffer.
- `loading_audit` needs to have *witnessed* spinners appear and disappear.
  Spinners that fired before the client script was injected, or on a page you
  never reloaded, are invisible to it.

This is why both tool descriptions say, verbatim, "a fresh page load is
required to fill it." A source-only LLM cannot reason about request ordering or
spinner concurrency from reading code — the decisive evidence is the runtime
timeline, which is exactly what these tools capture.

The other consequence of the temporal nature: it makes the **re-audit score
delta** a real signal. Reload, re-run, and the score moves because the recorded
behavior genuinely changed — not because you edited a static attribute.

## Real-world scenario

A dashboard route feels sluggish. Opening it fires a burst of API calls, three
skeleton blocks flicker in one after another, and the whole thing settles a
beat too late. You want to know *why* before you start editing, and you want a
number you can compare against after the fix.

The offending page:

- fetches `/api/orders/1` … `/api/orders/12` one id at a time (classic N+1),
- chains `/api/user` → `/api/prefs` → `/api/orders` serially when they have no
  data dependency (waterfall),
- fires the same `/api/summary` twice within a second (duplicate),
- issues 25 calls during initial load (chatty),
- and shows a top spinner, then a card spinner after it clears (cascade), plus
  three sibling skeletons under one grid (fragmentation).

## Step by step

### 1. Start the proxy

```
proxy {action:"start", id:"dev", target_url:"http://localhost:3000"}
```

Expected output: a proxy record with a `listen_addr` (e.g.
`http://localhost:41xxx`). Open that URL in a browser — the injected client
script begins recording fetch/XHR calls and the spinner timeline.

### 2. Load the dashboard fresh, then run the API audit

Navigate the browser to the dashboard route and let it fully load. **Then**:

```
api_audit {proxy_id:"dev"}
```

Expected output (compact text, the default):

```
=== API Efficiency Audit: D (58) ===
API efficiency needs attention. (28 calls analyzed). 4 issues to address

n-plus-one (1)
  [warning] .order-list — GET /api/orders/{id} called 12× — batch into one request

waterfall (1)
  [warning] main — 3 serial requests could run in parallel (/api/user → /api/prefs → /api/orders)

duplicate-call (1)
  [info] main — GET /api/summary repeated 2× within 2s

chatty-load (1)
  [warning] document — 25 calls during initial load
```

Notes on the shape (from `audit-api.js` / `formatBufferAuditCompact`):

- Headline line is `=== <headline>: <grade> (<score>) ===`.
- Findings are grouped by type in alphabetical order; each line is
  `[<severity>] <selector> — <message>` (selector omitted when absent).
- The **N+1 detection uses URL-template normalization**: `templateOf()`
  rewrites id-like path segments to `{id}`, groups calls by
  `method + template`, and flags a template hit `NPLUSONE_MIN` (5) or more
  times *only when the template was actually parameterized* (contained an id
  segment). So `/api/orders/1`…`/api/orders/12` collapse to the single template
  `GET /api/orders/{id}` with count 12.
- Thresholds live at the top of `audit-api.js`:
  `DUPLICATE_WINDOW_MS = 2000`, `DUPLICATE_MIN = 2`, `CHATTY_THRESHOLD = 20`,
  `CHATTY_SEVERE = 40` (chatty above 40 becomes a warning rather than info).

For the full machine-readable object (every finding + `stats`):

```
api_audit {proxy_id:"dev", raw:true}
```

Raw shape:

```json
{
  "score": 58,
  "grade": "D",
  "summary": "API efficiency needs attention. (28 calls analyzed). 4 issues to address",
  "stats": { "total": 28, "errors": 0, "warnings": 3, "info": 1, "totalIssues": 4 },
  "findingsByType": { "n-plus-one": [ ... ], "waterfall": [ ... ], ... }
}
```

### 3. Run the loading audit against the same load

```
loading_audit {proxy_id:"dev"}
```

Expected output:

```
=== Loading UX Audit: C (72) ===
Loading UX needs attention. 2 issues to address

spinner-cascade (1)
  [warning] #dashboard — 2 loaders fire serially (#top-bar → .card), 900ms total

spinner-fragmentation (1)
  [info] .grid — 3 concurrent sub-loaders under one ancestor should be one master loader
```

Notes (from `audit-loading.js`):

- **Cascade** = a serial chain of depth ≥ 2: loader B appears at/after loader A
  disappears, within `CASCADE_GAP_MS` (400ms), and they share a region. Total
  serial span above `CASCADE_CRITICAL_MS` (1000ms) escalates to critical; chains
  are capped at `CASCADE_MAX_CHAIN` (12).
- **Fragmentation** = `FRAGMENT_MIN` (3) or more sub-spinners active
  simultaneously under one common ancestor. The finding's selector is the
  deepest ancestor token shared by all members.

### 4. Fix, reload, re-audit — watch the delta

Batch the N+1 into `GET /api/orders?ids=1..12`, run the three independent
fetches with `Promise.all`, dedupe `/api/summary`, and consolidate the three
grid skeletons under one master loader. Then **reload the page** (this is
mandatory — the audits read a fresh recording, not the DOM) and re-run:

```
api_audit {proxy_id:"dev"}
loading_audit {proxy_id:"dev"}
```

Expected: `=== API Efficiency Audit: A (96) ===` and
`=== Loading UX Audit: A (94) ===`. The score delta (58 → 96, 72 → 94) is the
verification that the fix landed — and it moved only because the recorded
runtime behavior changed.

## Gotchas

- **You must reload between fix and re-audit.** The buffers are populated by
  page load. Editing code and re-running the audit without a reload compares
  against the *old* recording and shows no improvement. This is the single most
  common mistake.
- **An idle or never-loaded page returns score 100 with an empty summary.** If
  the buffer/timeline is empty the audit is not "perfect" — it saw nothing. The
  summary says "no API calls recorded — reload page then re-run" (api) or "No
  loading indicators recorded — reload page then re-run" (loading). Treat a
  bare 100 with zero findings as "go reload," not "done."
- **Audits target the inner content frame by default.** agnt wraps the app in a
  chrome shell + content iframe. Both tools default to `target:"inner"` (the
  active page content frame), which is what you want. `target:"outer"` audits
  the chrome shell and is almost never useful.
- **Score weighting differs from raw finding count.** `audit-api.js` weights
  waterfall / n+1 / duplicate heavier than chatty/info, so a single N+1 can
  outweigh several info-level notes. Read the grouped findings, not just the
  number.
- **`errors_only`-style filtering does not apply here** — these are dedicated
  audit tools, not `proxylog` queries. Use `raw:true` when you need every field.
