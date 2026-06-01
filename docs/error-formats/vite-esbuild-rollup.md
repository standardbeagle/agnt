# Error formats: Vite, esbuild, Rollup

Survey of REAL, CURRENT error-message output for the three bundlers, to feed the
two Go pattern banks:

- Toast classifier: `internal/overlay/alerts_defaults.go` (`AlertPattern` — boolean "is error" + category).
- Structured parser: `internal/tools/build_error_parsers.go` (`BuildError` — file/line/col/code/message).

All regex is Go `regexp` (RE2) syntax — no backreferences, no lookaround.
Versions surveyed: **Vite 5.x/6.x**, **esbuild 0.19–0.25**, **Rollup 4.x**
(Vite delegates production build + import resolution to Rollup, and transform to
esbuild, so all three forms co-occur in a single Vite session).

> **Already covered, do NOT duplicate.** `build_error_parsers.go` already has
> `viteHeaderRe = ^[✗✘]\s+\[(ERROR|WARNING)\]\s+(.+)$` paired with
> `viteLocationRe = ^\s+([^\s:]+):(\d+):(\d+):?\s*$`. That is the **esbuild
> code-frame form** (Vite re-emits esbuild transform errors through it). The
> proposals below are DELTAS only: esbuild's trailing `[category]` tag + note
> lines, Vite's CLI `Plugin:`/`File:` block, and Rollup's `[!] (plugin …)` CLI
> form. The toast bank already has `vite-hmr-fail`, `webpack-error`,
> `rebuild-vite`; deltas avoid those too.

---

## 1. Real examples (verbatim)

### 1a. esbuild — error with code frame
esbuild 0.19+. Source: esbuild CHANGELOG (evanw/esbuild).

```
✘ [ERROR] The character ">" is not valid inside a JSX element

    example.tsx:2:14:
      2 │   return <div>></div>;
        │               ^
        ╵               {'>'}

  Did you mean to escape it as "{'>'}" instead?
```

### 1b. esbuild — warning with trailing `[category]` tag + secondary location
esbuild attaches a bracketed category id at the END of the message text, and may
emit a *second* indented `file:line:col:` block as a "note" pointing elsewhere.

```
▲ [WARNING] "import.meta" is not available with the "cjs" output format and will be empty [empty-import-meta]

    ../../node_modules/yargs/lib/platform-shims/esm.mjs:20:28:
      20 │ __dirname = fileURLToPath(import.meta.url);
         ╵                           ~~~~~~~~~~~

  You need to set the output format to "esm" for "import.meta" to work correctly.
```

```
▲ [WARNING] The CommonJS "module" variable is treated as a global variable in an ECMAScript module and may not work as expected

    example.ts:2:0:
      2 │ module.exports.b = 1
        ╵ ~~~~~~

  This file is considered to be an ECMAScript module because of the "export" keyword here:

    example.ts:1:0:
      1 │ export let a = 1
        ╵ ~~~~~~
```

> Note the SECOND `example.ts:1:0:` location block: the "note" reuses the exact
> `viteLocationRe` shape. Existing parser already swallows it as the location of
> the *header*; for esbuild the first location after the header is the real one,
> the rest are notes. Existing behaviour (take next line only) is correct — the
> delta is the `[category]` tag extraction, not the note handling.

### 1c. esbuild — unresolved import (CLI, no frame on the resolve message line)

```
✘ [ERROR] Could not resolve "some-module"

    src/index.js:3:19:
      3 │ import something from "some-module";
        ╵                       ~~~~~~~~~~~~~
```

---

### 1d. Vite — dev server `Internal server error` via `vite:import-analysis`
Vite 5.x/6.x. This is the CLI terminal form (the daemon scans terminal output).
NOT the esbuild `✘` form — it is a plain `[vite] Internal server error:` header,
then a `Plugin:` line, then a `File:` line carrying `path:line:col`. Optional
leading timestamp (`H:MM:SS` or `H:MM:SS AM/PM`). Source: vitejs/vite issues
#17471, #17501.

```
3:23:41 PM [vite] Internal server error: Failed to resolve import "@/styles/reset.less" from "src/main.tsx". Does the file exist?
  Plugin: vite:import-analysis
  File: /home/bluven/Workspace/web/hydra/src/main.tsx:3:7
  3 | import "@/styles/reset.less";
```

```
1:49:08 [vite] Internal server error: Failed to resolve import "../Test2/Test/Messages/en.ts" from "src/Intl/en.ts". Does the file exist?
  Plugin: vite:import-analysis
  File: /home/projects/vitejs-vite-adxel4/src/Intl/en.ts:1:74
  1 | import * as __vite_glob_0_0 from "../Test/Messages/en.ts";...
```

### 1e. Vite — transform failure (esbuild relayed through Vite header)
Vite wraps an esbuild transform error in a single-line `Internal server error`
when it has no frame. Distinct from 1d (no `Plugin:`/`File:` block).

```
[vite] Internal server error: Transform failed with 1 error:
C:/Users/name/project/src/main.tsx:6:2: ERROR: Expected ";" but found "as"
```

### 1f. Vite — client-side pre-transform error (terminal)
Emitted with a `(client)` or `(ssr)` environment tag in recent Vite. Source:
vitejs/vite-plugin-react #349 and Vite docs.

```
Pre-transform error: Failed to resolve import "some-package" from "src/main.ts"
[vite] (client) Pre-transform error: `super()` is only valid inside a class constructor of a subclass
```

---

### 1g. Rollup — CLI plugin error
Rollup 4.x. The CLI renderer (`cli/logging.ts`) prints `[!]` + bold
`(plugin NAME) ErrorName: message`. When a `loc` is present it adds a
`file (line:column)` line; a `frame` snippet follows, then the stack (dimmed).
Source: rollup/rollup `cli/logging.ts`, issue #5166.

```
[!] (plugin my-plugin) Error: Failed to run transformers
my-file.coolext
```

```
[!] RollupError: Could not resolve "./missing.js" from "src/main.js"
src/main.js (1:15)
```

### 1h. Rollup — unresolved export / "is not exported by"
Common `RollupError` with `code`. The non-plugin form omits the `(plugin …)`
parenthetical.

```
[!] RollupError: "foo" is not exported by "bar.js", imported by "baz.js".
https://rollupjs.org/troubleshooting/#error-name-is-not-exported-by-module
src/baz.js (2:9)
2: import { foo } from './bar.js';
            ^
```

### 1i. Rollup — parse error with frame
```
[!] RollupError: Unexpected token (Note that you need plugins to import files that are not JavaScript)
src/styles.css (1:0)
1: .body { color: red; }
   ^
```

> Rollup location line shape: `path (line:column)` with parentheses and a SPACE
> before the paren — NOT the `path:line:col:` colon shape used by esbuild/Vite.
> This is the single most load-bearing disambiguator between the banks.

---

## 2. Stable regex anchors (RE2)

| # | Tool / form | Signal line | RE2 regex |
|---|-------------|-------------|-----------|
| A1 | esbuild error/warn header (existing `viteHeaderRe`, extend) | `✘ [ERROR] …` / `▲ [WARNING] …` | `^[✗✘▲]\s+\[(ERROR\|WARNING)\]\s+(.+)$` |
| A2 | esbuild trailing category tag (extract from header msg) | `… [empty-import-meta]` | `\s\[([a-z][a-z0-9-]+)\]$` |
| A3 | esbuild/Vite location (existing `viteLocationRe`) | `    file:line:col:` | `^\s+([^\s:]+):(\d+):(\d+):?\s*$` |
| V1 | Vite ISE header | `… [vite] Internal server error: …` | `\[vite\]\s+(?:\((?:client\|ssr)\)\s+)?Internal server error:\s+(.+)$` |
| V2 | Vite pre-transform error | `Pre-transform error: …` | `(?:\[vite\]\s+(?:\((?:client\|ssr)\)\s+)?)?Pre-transform error:\s+(.+)$` |
| V3 | Vite plugin line | `  Plugin: vite:import-analysis` | `^\s*Plugin:\s+(\S+)\s*$` |
| V4 | Vite file line | `  File: /abs/path.tsx:3:7` | `^\s*File:\s+(.+?):(\d+):(\d+)\s*$` |
| R1 | Rollup CLI error header | `[!] (plugin x) Error: …` / `[!] RollupError: …` | `^\[!\]\s+(?:\(plugin\s+([^)]+)\)\s+)?(\w+(?:Error)?):\s+(.+)$` |
| R2 | Rollup location line | `src/baz.js (2:9)` | `^(\S.*?)\s+\((\d+):(\d+)\)\s*$` |

Validation notes:
- **A1** adds `▲` (U+25B2) and keeps `✗`/`✘`. RE2 handles multibyte UTF-8 in a
  char class fine.
- **A2** anchors the tag to end-of-line (`$`) so it never collides with a
  bracketed term mid-message; esbuild always trails its category id.
- **V1/V2** are deliberately NOT `^`-anchored on the bracket because Vite
  prefixes an optional timestamp (`3:23:41 PM `). They match mid-line.
- **R2** requires `\s+\(` (space before paren) so it cannot match an esbuild/Vite
  `path:line:col:` line (those have no space-paren). The `\S.*?` non-greedy head
  tolerates paths with spaces up to the location paren.
- **R1** `(\w+(?:Error)?)` captures both `Error` and `RollupError`/`SyntaxError`
  as the error-name field; the `(plugin …)` group is optional.

---

## 3. Signal-vs-noise field map

| Field | esbuild | Vite ISE (1d) | Vite transform (1e) | Rollup CLI |
|-------|---------|---------------|---------------------|------------|
| Severity signal | `[ERROR]`/`[WARNING]` (A1) | `Internal server error` ⇒ error | header ⇒ error | `[!]` ⇒ error |
| Message | A1 grp 2 (strip A2 tag) | V1 grp 1 | V1 grp 1 | R1 grp 3 |
| File | A3 grp 1 | V4 grp 1 | inline `path:line:col:` after header | R2 grp 1 |
| Line:Col | A3 grp 2/3 | V4 grp 2/3 | inline | R2 grp 2/3 |
| Plugin/code | A2 category tag | V3 grp 1 (`vite:import-analysis`) | n/a | R1 grp 1 (plugin) + grp 2 (errname) |
| Decoration (noise) | `│ ~ ╵ ^` frame rows, `Did you mean…` note, secondary location | `N \| <code>` echo row, timestamp prefix | none | `1: <code>` echo + `^` caret rows, dimmed stack, `https://rollupjs.org/…` doc URL |

Decoration lines to SKIP (never emit as a `BuildError`):
- esbuild frame rows: `^\s+\d+ │`, `^\s+[│╵] `, lines starting with `~`/`^`/`╵`.
- Vite/Rollup code echo: `^\s*\d+ \| ` (Vite) / `^\d+: ` (Rollup) and the lone `^` caret line.
- Rollup doc URL: `^https://rollupjs\.org/`.

---

## 4. Parser proposals (DELTAS only)

### 4a. Toast bank — `internal/overlay/alerts_defaults.go`

Add to the `// Vite` group and a new `// Rollup` / `// esbuild` group. These are
broad boolean classifiers (`AlertPattern{ID, Pattern, Severity, Category, Description}`).
None duplicate existing `vite-hmr-fail`, `webpack-error`, `rebuild-vite`.

```go
// Vite (delta)
{
    ID:          "vite-internal-server-error",
    Pattern:     regexp.MustCompile(`\[vite\] (?:\((?:client|ssr)\) )?Internal server error:`),
    Severity:    AlertSeverityError,
    Category:    "vite",
    Description: "Vite dev server internal/transform/import-analysis error",
},
{
    ID:          "vite-pre-transform-error",
    Pattern:     regexp.MustCompile(`Pre-transform error:`),
    Severity:    AlertSeverityError,
    Category:    "vite",
    Description: "Vite pre-transform (import resolution / parse) error",
},
{
    ID:          "vite-import-analysis-plugin",
    Pattern:     regexp.MustCompile(`Plugin: vite:import-analysis`),
    Severity:    AlertSeverityError,
    Category:    "vite",
    Description: "Vite import-analysis resolution failure",
},

// esbuild (delta — toast did not classify the ✘/▲ header before)
{
    ID:          "esbuild-error",
    Pattern:     regexp.MustCompile(`^[✗✘]\s+\[ERROR\]`),
    Severity:    AlertSeverityError,
    Category:    "esbuild",
    Description: "esbuild compile/transform error",
},
{
    ID:          "esbuild-warning",
    Pattern:     regexp.MustCompile(`^▲\s+\[WARNING\]`),
    Severity:    AlertSeverityWarning,
    Category:    "esbuild",
    Description: "esbuild warning",
},

// Rollup (delta — no Rollup coverage existed)
{
    ID:          "rollup-error",
    Pattern:     regexp.MustCompile(`^\[!\]\s+(?:\(plugin\s+[^)]+\)\s+)?\w*(?:RollupError|Error):`),
    Severity:    AlertSeverityError,
    Category:    "rollup",
    Description: "Rollup CLI build/plugin error",
},
```

Toast count: **6 new** (3 vite, 2 esbuild, 1 rollup).

### 4b. Structured bank — `internal/tools/build_error_parsers.go`

Existing `viteHeaderRe` + `viteLocationRe` already handle the esbuild code-frame
form (1a/1b/1c). Add the regexes below; wire each in `parseBuildErrors`.

```go
// esbuild category tag — extracted from the header message captured by
// viteHeaderRe group 2; trailing "[category-id]". Apply to m[2] before storing.
var esbuildTagRe = regexp.MustCompile(`\s\[([a-z][a-z0-9-]+)\]$`)
//   In the existing viteHeader branch, after capturing Message=m[2]:
//   if t := esbuildTagRe.FindStringSubmatch(be.Message); t != nil {
//       be.Code = t[1]; be.Message = strings.TrimSuffix(be.Message, t[0])
//       be.Tool = "esbuild"   // distinguish from Vite-relayed
//   }

// Vite dev-server Internal Server Error: header line, then "Plugin:" then
// "File: path:line:col". Header may carry a leading timestamp + optional
// (client)/(ssr) env tag, so it is NOT ^-anchored on the bracket.
var viteISEHeaderRe = regexp.MustCompile(`\[vite\]\s+(?:\((?:client|ssr)\)\s+)?Internal server error:\s+(.+)$`)
var vitePluginRe    = regexp.MustCompile(`^\s*Plugin:\s+(\S+)\s*$`)
var viteFileRe      = regexp.MustCompile(`^\s*File:\s+(.+?):(\d+):(\d+)\s*$`)
//   Emit BuildError{Tool:"vite", Severity:"error", Message:m[1], Code:<plugin>,
//   File/Line/Col from viteFileRe}. Look ahead up to 3 lines for Plugin:/File:.

// Vite pre-transform error (single line, no File block in many cases).
var vitePreTransformRe = regexp.MustCompile(`(?:\[vite\]\s+(?:\((?:client|ssr)\)\s+)?)?Pre-transform error:\s+(.+)$`)
//   Emit BuildError{Tool:"vite", Severity:"error", Message:m[1]}.

// Rollup CLI error header → location pair.
//   group1 = optional plugin name, group2 = error name, group3 = message.
var rollupHeaderRe   = regexp.MustCompile(`^\[!\]\s+(?:\(plugin\s+([^)]+)\)\s+)?(\w+(?:Error)?):\s+(.+)$`)
//   Location line: "path (line:col)" — SPACE before paren (vs esbuild's colon form).
var rollupLocationRe = regexp.MustCompile(`^(\S.*?)\s+\((\d+):(\d+)\)\s*$`)
//   Emit BuildError{Tool:"rollup", Severity:"error", Code:<errname or plugin>,
//   Message:m[3], File/Line/Col from rollupLocationRe on the next non-frame line}.
```

Ordering / collision notes for `parseBuildErrors`:
- `rollupLocationRe` must only be tried as the *follow-up* to `rollupHeaderRe`
  (like `rustLocationRe`), never standalone — a bare `foo (1:2)` could otherwise
  shadow other formats. Gate it behind a pending-rollup-header flag.
- `viteISEHeaderRe` / `vitePreTransformRe` are mid-line matches; try them BEFORE
  the generic `node-error` `^Error:` toast logic won't apply here (different bank).
- esbuild tag extraction lives INSIDE the existing `viteHeaderRe` branch — it is
  a post-process on `m[2]`, not a new top-level branch. Set `Tool="esbuild"`
  when the `▲` glyph or a category tag is present; otherwise keep `Tool="vite"`.

Structured count: **1 tag extractor + 5 new regexes** → 4 new BuildError emitters
(esbuild-tag augmentation, vite-ISE, vite-pre-transform, rollup).

---

## Sources
- esbuild output format & code frames — esbuild CHANGELOG (evanw/esbuild) and API docs: https://esbuild.github.io/api/ , https://raw.githubusercontent.com/evanw/esbuild/b31c5c4b5a91168708aabb95aee2ab93f9e94eb8/CHANGELOG.md
- esbuild `[WARNING]` + `[category]` tag — angular/angular-cli#28851, evanw/esbuild#2723, #2842
- Vite Internal server error / import-analysis — vitejs/vite#17471, #17501, #15784, #1882
- Vite pre-transform error — vitejs/vite-plugin-react#349, https://fixdevs.com/blog/vite-failed-to-resolve-import/
- Rollup CLI error rendering — rollup/rollup `cli/logging.ts` (master), rollup/rollup#5166, https://rollupjs.org/troubleshooting/
