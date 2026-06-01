# Error formats: Next.js / webpack / Turbopack / RSC

Research spec for the agnt Go pattern/parser banks (task R2 → feeds G3). Goal:
**stable regex anchors keyed on the SIGNAL line, not guesses.** All regexes are
Go `regexp` (RE2) — no lookaround, no backrefs. Each anchor was validated
against the verbatim text quoted in §1.

Two consumer banks (read both before editing — see their package docs):

- **Broad toast** — `AlertPattern{ID, Pattern, Severity, Category, Description}`
  in `internal/overlay/alerts_defaults.go`. One boolean ("this line is an
  error") + category. Browser toast is the consumer.
- **Narrow structured** — `BuildError{Tool, Severity, File, Line, Col, Code,
  Rule, Message, RawLine}` in `internal/tools/build_error_parsers.go`. Naming
  `<tool>_<form>Re`. Multi-line forms consume a header line + a lookahead
  location/trace line and emit one `BuildError`.

**Already present (do NOT duplicate):**
- toast `webpack-error` = `ERROR in`, `webpack-compile-fail` = `Failed to compile`
- toast `nextjs-build-error` = `(?i)Build error`
- toast `node-cannot-find-module` = `Cannot find module`
- parser `webpackHeaderRe` = `^(ERROR|WARNING)\s+in\s+(\S+)` + folds next line as message
- `rebuild-*` info signals (`compiling...`, `rebuilding`)

Deltas proposed below are flagged where they overlap.

---

## 1. Real examples (verbatim)

### 1a. Next.js — Module not found (webpack bundler, App + Pages Router)

Next.js webpack-mode "module not found" is webpack's `Module not found` reframed
with an `Import trace for requested module:` footer. From the Next.js error doc
and real reports (Next 14/15):

```
./node_modules/.pnpm/isolated-vm@4.7.2/node_modules/isolated-vm/isolated-vm.js
Module not found: Can't resolve './out/isolated_vm'

Import trace for requested module:
./node_modules/.pnpm/isolated-vm@4.7.2/node_modules/isolated-vm/isolated-vm.js
./app/page.tsx
```

Node ESM loader variant (also surfaces under `next build` / `next dev`):

```
Error [ERR_MODULE_NOT_FOUND]: Cannot find module 'C:\...\node_modules\next\server' imported from C:\...\node_modules\next-auth\lib\env.js
Did you mean to import "next/server.js"?
```

Source: <https://nextjs.org/docs/messages/module-not-found>,
<https://github.com/vercel/next.js/issues/65564>,
<https://github.com/nextauthjs/next-auth/discussions/10058>

### 1b. Turbopack — Module not found (Next.js 16.0.3, `next dev --turbopack`)

Distinct from webpack: leading `file:line:col` on the **header** line, a code
frame with `>` gutter + caret, and `Import trace:` with an indented
`Server Component:` / `Client Component:` sub-label.

```
./node_modules/.pnpm/thread-stream@3.1.0/node_modules/thread-stream/test/helper.js:33:15
Module not found: Can't resolve 'why-is-node-running'
  31 |
  32 | if (process.env.SKIP_PROCESS_EXIT_CHECK !== 'true') {
  > 33 |   const why = require('why-is-node-running')
     |               ^^^^^^^^^^^^^^^^^^^^^^^^^^^^^^
  34 |   setInterval(why, 10000).unref()
  35 | }
  36 |

Import trace:
  Server Component:
    ./node_modules/.pnpm/thread-stream@3.1.0/node_modules/thread-stream/test/helper.js
    ./node_modules/.pnpm/thread-stream@3.1.0/node_modules/thread-stream/index.js
    ./packages/utils/dist/logger.js
    ./apps/web/app/page.tsx
```

Internal Turbopack failures use a separate tagged class:

```
Error [TurbopackInternalError]: Next.js package not found
```

Source: <https://github.com/vercel/next.js/issues/86458>,
<https://github.com/vercel/next.js/issues/76028>

### 1c. RSC — server/client boundary error (Next 14.2.x, App Router)

The canonical `ReactServerComponentsError:` block. Signal is the
`ReactServerComponentsError:` label line and the `You're importing a component
that needs <hook>.` sentence. Carries a boxed code frame and a
`Maybe one of these should be marked as a client entry with "use client":`
footer listing candidate files.

```
./src\CountriesList.js
ReactServerComponentsError:

You're importing a component that needs useEffect. It only works in a
Client Component but none of its parents are marked with "use client",
so they're Server Components by default.

Learn more: https://nextjs.org/docs/getting-started/react-essentials

╭─[C:\Users\simon\Downloads\Uppgift3\labb3\src\CountriesList.js:1:1]
1 │ import React, { useState, useEffect } from 'react';
  · ─────────
2 │ import fetch from 'isomorphic-unfetch';
╰────

Maybe one of these should be marked as a client entry with "use client":
./src\CountriesList.js
./src\app\page.js
```

The `useState` variant is identical with `needs useState` substituted. Other
boundary errors share the `Unhandled Runtime Error` / `Error:` framing:

```
Unhandled Runtime Error
Error: Event handlers cannot be passed to Client Component props.
```
```
Unhandled Runtime Error
Error: async/await is not yet supported in Client Components, only Server Components.
```

`"use server"` value violations are a separate documented class:

```
Error: A "use server" file can only export async functions, found object.
```

Source: <https://github.com/vercel/next.js/discussions/65665>,
<https://github.com/vercel/next.js/discussions/59857>,
<https://nextjs.org/docs/messages/invalid-use-server-value>,
<https://www.omi.me/blogs/next-js-errors/unhandled-runtime-error-in-next-js-causes-and-how-to-fix>

### 1d. Next.js — Hydration error (runtime, App + Pages)

Exact wording from the Next.js error doc and React (Next 14/15). Two stable
sentence anchors:

```
Hydration failed because the server rendered HTML didn't match the client. As a result this tree will be regenerated on the client.
```
```
Text content does not match server-rendered HTML.
```
Older/React-18 phrasing still seen in the wild:
```
Hydration failed because the initial UI does not match what was rendered on the server.
```

Source: <https://nextjs.org/docs/messages/react-hydration-error>,
<https://github.com/vercel/next.js/discussions/35773>

### 1e. webpack — ERROR in / Module build failed / Module not found

Classic webpack (4/5) terminal blocks. Header is `ERROR in ./path` or
`WARNING in ./path`; body line is `Module build failed (from <loader>):` or
`Module not found: Error: Can't resolve '<x>' in '<dir>'`; resolve trace lines
start with `@ ./src/...`.

`Module build failed` (babel-loader):

```
ERROR in ./src/index.js
Module build failed (from ./node_modules/babel-loader/lib/index.js):
Error: Cannot find module '@babel/core'
```

`Module not found` (warning severity, optional dep):

```
WARNING in ./node_modules/node-fetch/lib/index.mjs
Module not found: Error: Can't resolve 'encoding' in '/home/shan/.../node_modules/node-fetch/lib'
```

`SyntaxError` via babel-loader with code frame + resolve trace:

```
ERROR in ./src/App.js
Module build failed (from ./node_modules/babel-loader/lib/index.js):
SyntaxError: /home/me/app/src/App.js: Unexpected token (12:4)

  10 |   render() {
  11 |     return (
> 12 |     <div>
     |     ^
 @ ./src/index.js 5:0-24 8:13-16
```

Source: <https://github.com/babel/babel/issues/8599>,
<https://github.com/webpack/webpack/issues/8365>,
<https://survivejs.com/books/webpack/appendices/troubleshooting/>

---

## 2. Stable regex anchors (RE2)

Key on the SIGNAL line. Validated against §1 verbatim text.

| # | Area | Anchor (Go RE2) | Keys on |
|---|------|-----------------|---------|
| A | Module not found (webpack & Turbopack body) | `Module not found: (?:Error: )?Can't resolve '([^']+)'` | the resolve target |
| B | Turbopack header w/ loc | `^(\./\S+):(\d+):(\d+)$` (paired w/ next-line A) | file:line:col |
| C | Turbopack/Node internal err | `\[(TurbopackInternalError|ERR_MODULE_NOT_FOUND)\]` | tagged class |
| D | RSC boundary label | `^ReactServerComponentsError:` | the RSC block start |
| E | RSC needs-hook sentence | `You're importing a component that needs (\w+)\.` | hook name; flavour |
| F | RSC client-entry hint | `^Maybe one of these should be marked as a client entry with "use client":` | block footer |
| G | Hydration (current) | `Hydration failed because the (?:server rendered HTML didn't match the client|initial UI does not match)` | hydration mismatch |
| H | Hydration (text) | `^Text content does not match server-rendered HTML\.` | text mismatch |
| I | `use server` value | `A "use server" file can only export async functions` | use-server violation |
| J | Unhandled runtime header | `^Unhandled Runtime Error$` | runtime error block start |
| K | webpack module build failed | `^Module build failed(?: \(from (\S+)\))?:` | loader path (opt) |
| L | webpack resolve trace | `^\s*@\s+(\S+)\s+(\d+:\d+-\d+)` | importing file + range |
| M | Next.js import trace footer | `^Import trace(?: for requested module)?:` | trace block start |

Notes:
- A matches **both** webpack (`Can't resolve 'x'`) and Turbopack
  (`Can't resolve 'x'`, no `Error:`) bodies — `(?:Error: )?` makes the webpack
  `Error:` infix optional. This is the single best cross-bundler anchor.
- Existing parser `webpackHeaderRe` (`^(ERROR|WARNING)\s+in\s+(\S+)`) already
  covers webpack's header line — reuse it; do NOT add a new header regex.
- The Turbopack header (B) does **not** match webpack (webpack header is
  `ERROR in ./path` with no trailing `:line:col`), so B is unambiguous.
- `'` in RE2 needs no escaping; backtick-quote the Go literals to avoid
  escaping the `\` in `\S` / `\d` / `\.`.

---

## 3. Signal-vs-noise field map

| Area | Message line (signal) | file:line:col location | code / rule | Stack / decoration (noise) |
|------|-----------------------|------------------------|-------------|----------------------------|
| webpack | `Module build failed...:` / `Module not found: ...Can't resolve 'x'` | header line `ERROR in ./path` (no line/col); loc only inside `SyntaxError: <file>: ... (L:C)` | none (no error codes); loader path in `(from ...)` | code frame `> 12 \|`, caret line, `@ ./src/...` trace |
| Turbopack | `Module not found: Can't resolve 'x'` (line **after** header) | **header** line `./path:L:C` | none; class tag `[TurbopackInternalError]` for internal | code frame `> 33 \|`/caret, `Import trace:` + indented `Server/Client Component:` |
| Next.js MNF | `Module not found: Can't resolve 'x'` or `Cannot find module '...'` | header `./path` (webpack) / inside `imported from <file>` (Node ESM) | `[ERR_MODULE_NOT_FOUND]` for Node ESM | `Import trace for requested module:` footer |
| RSC boundary | `You're importing a component that needs <hook>.` after `ReactServerComponentsError:` | boxed frame `╭─[<file>:L:C]` | hook name is the discriminant; no numeric code | box-drawing frame, `Learn more:` URL, `Maybe one of these...` footer |
| Hydration | the `Hydration failed...` / `Text content does not match...` sentence | none on the signal line (component stack follows) | none | React component stack, `at <Component>` frames |
| use server | `A "use server" file can only export async functions, found <type>.` | usually none on signal line | none | surrounding `Error:` framing |

Rule of thumb: **for the bundlers the location is the header (Turbopack) or
absent (webpack); for RSC it is the box-frame `╭─[file:L:C]`; for runtime/
hydration there is no location on the signal line** — do not try to scrape one.

---

## 4. Parser proposals

### 4a. Broad toast bank — `internal/overlay/alerts_defaults.go`

Minimal covering set. Category `nextjs` / `webpack` / `turbopack`. Skip
anything already covered (`ERROR in`, `Failed to compile`, `Cannot find
module`, `(?i)Build error`).

```go
// Turbopack (new category)
{
    ID:          "turbopack-internal-error",
    Pattern:     regexp.MustCompile(`\[TurbopackInternalError\]`),
    Severity:    AlertSeverityError,
    Category:    "turbopack",
    Description: "Turbopack internal error",
},
{
    ID:          "turbopack-module-not-found",
    Pattern:     regexp.MustCompile(`Module not found: Can't resolve '`),
    Severity:    AlertSeverityError,
    Category:    "turbopack",
    Description: "Module resolution failure (Turbopack/webpack/Next.js)",
},

// Next.js / RSC
{
    ID:          "nextjs-rsc-boundary",
    Pattern:     regexp.MustCompile(`^ReactServerComponentsError:`),
    Severity:    AlertSeverityError,
    Category:    "nextjs",
    Description: "React Server Components boundary error",
},
{
    ID:          "nextjs-needs-client",
    Pattern:     regexp.MustCompile(`You're importing a component that needs \w+\.`),
    Severity:    AlertSeverityError,
    Category:    "nextjs",
    Description: "Server Component using a client-only hook ('use client' missing)",
},
{
    ID:          "nextjs-hydration",
    Pattern:     regexp.MustCompile(`Hydration failed because the (server rendered HTML didn't match the client|initial UI does not match)`),
    Severity:    AlertSeverityError,
    Category:    "nextjs",
    Description: "React hydration mismatch",
},
{
    ID:          "nextjs-text-content-mismatch",
    Pattern:     regexp.MustCompile(`^Text content does not match server-rendered HTML\.`),
    Severity:    AlertSeverityError,
    Category:    "nextjs",
    Description: "Hydration text-content mismatch",
},
{
    ID:          "nextjs-use-server-value",
    Pattern:     regexp.MustCompile(`A "use server" file can only export async functions`),
    Severity:    AlertSeverityError,
    Category:    "nextjs",
    Description: "Invalid 'use server' export (non-async value)",
},
{
    ID:          "node-err-module-not-found",
    Pattern:     regexp.MustCompile(`\[ERR_MODULE_NOT_FOUND\]`),
    Severity:    AlertSeverityError,
    Category:    "node",
    Description: "Node ESM loader module-not-found",
},
```

Overlap flags:
- `turbopack-module-not-found` (`Module not found: Can't resolve '`) is broader
  than webpack's existing `ERROR in`; it fires on the body line that webpack
  header lacks, and is the only thing Turbopack prints (no `ERROR in`). It is
  **complementary**, not a dup — but it WILL also fire on the webpack body line,
  producing a second toast alongside `webpack-error`. If double-toasting is
  unwanted, dedup by category at the toast layer; do not narrow the regex
  (Turbopack has no header to anchor on).
- `Unhandled Runtime Error` is intentionally **omitted** from toast: it is a
  generic header that precedes many errors and would over-fire; the specific
  body line (hydration, needs-client) carries the signal.

### 4b. Narrow structured bank — `internal/tools/build_error_parsers.go`

Two new multi-line parsers. Reuse the **existing** `webpackHeaderRe` for
webpack — only add the Turbopack and RSC forms.

```go
// Turbopack: header `./path:line:col` then body `Module not found: Can't resolve 'x'`.
// Distinct from webpack (whose header is `ERROR in ./path`, no :line:col).
turbopackHeaderRe = regexp.MustCompile(`^(\.\/\S+):(\d+):(\d+)$`)
turbopackMNFRe    = regexp.MustCompile(`^Module not found: Can't resolve '([^']+)'`)

// RSC boundary: label line, then the needs-<hook> sentence, then a boxed
// frame `╭─[<file>:line:col]` carrying the location.
rscLabelRe    = regexp.MustCompile(`^ReactServerComponentsError:`)
rscNeedsRe    = regexp.MustCompile(`You're importing a component that needs (\w+)\.`)
rscLocationRe = regexp.MustCompile(`^╭─\[(.+):(\d+):(\d+)\]$`)
```

Parser logic (mirrors the existing rust/vite header→location lookahead):

- **Turbopack** (`Tool: "turbopack"`): on a `turbopackHeaderRe` match, capture
  `File/Line/Col`; scan the next non-empty line for `turbopackMNFRe` →
  `Message: "Module not found: Can't resolve '<x>'"`, `Code: ""`. Emit one
  `BuildError`. Guard: only treat as Turbopack if the body line within the next
  ~2 lines matches `turbopackMNFRe` (otherwise a bare `./path:1:1` could be
  noise).
- **RSC** (`Tool: "rsc"`): on `rscLabelRe`, look ahead (≤8 lines, mirroring the
  jest 5-line window) for `rscNeedsRe` → `Message: "needs <hook>"`,
  `Severity: "error"`; continue scanning for `rscLocationRe` → `File/Line/Col`
  from the box frame. Emit one `BuildError`. If no location frame, still emit
  with the message + raw line (mirrors jest header-without-frame behaviour).

Compact render (existing `formatBuildErrorCompact`, no change needed):
```
[turbopack:error] ./node_modules/.../helper.js:33:15 — Module not found: Can't resolve 'why-is-node-running'
[rsc:error] src/CountriesList.js:1:1 — needs useEffect (mark a parent 'use client')
```

webpack delta: **none.** `webpackHeaderRe` + its next-line message fold already
cover `ERROR in ./path` → `Module build failed: ...`. The only gap is that the
folded message stops at one line, so it misses the `SyntaxError: <file> ...
(L:C)` location buried in the babel-loader frame. Optional low-priority delta:
add `webpackSyntaxLocRe = regexp.MustCompile(`^(?:Syntax)?Error: (\S+): .* \((\d+):(\d+)\)`)`
and, when the webpack folded message matches it, backfill `File/Line/Col`. Flag
as optional — not in the minimal covering set.

---

## Sources

- <https://nextjs.org/docs/messages/module-not-found>
- <https://nextjs.org/docs/messages/react-hydration-error>
- <https://nextjs.org/docs/messages/invalid-use-server-value>
- <https://github.com/vercel/next.js/issues/86458> (Turbopack MNF, Next 16.0.3)
- <https://github.com/vercel/next.js/issues/76028> (TurbopackInternalError)
- <https://github.com/vercel/next.js/issues/65564> (App Router MNF)
- <https://github.com/vercel/next.js/discussions/65665> (RSC needs useState, Next 14.2.3)
- <https://github.com/vercel/next.js/discussions/59857> (RSC needs useEffect, full block)
- <https://github.com/vercel/next.js/discussions/35773> (hydration phrasing)
- <https://github.com/babel/babel/issues/8599> (webpack ERROR in / Module build failed)
- <https://github.com/webpack/webpack/issues/8365> (webpack Module not found warning)
- <https://survivejs.com/books/webpack/appendices/troubleshooting/> (webpack error taxonomy)
- <https://github.com/nextauthjs/next-auth/discussions/10058> (ERR_MODULE_NOT_FOUND)
