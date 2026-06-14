# Unified Output Classifier — Design

Status: **implemented (data-level unification)** · 2026-06-09 · author: session work

## What shipped (vs. this design)

Implemented the **single-source-of-truth** core: a new `internal/classify`
package owns the entire rule set — the 54 broad line rules
(`DefaultLineRules`), the Prisma/DB structured parsers (`RunStructuredLine`,
`IsStructuralPrefix`), and the 9 located build parsers (`ParseBuildErrors`,
`BuildError`). Both consumers now source rules from it:

- `internal/overlay/alerts_defaults.go` adapts `classify.DefaultLineRules()` →
  `AlertPattern`; `internal/overlay/structured_parsers.go` is a thin bridge
  (`StructuredError = classify.StructuredError`, wrappers).
- `internal/tools/build_error_parsers.go` aliases `BuildError =
  classify.BuildError` and delegates `parseBuildErrors` →
  `classify.ParseBuildErrors`; only the agent-facing `formatBuildErrorCompact`
  renderer stays tools-local.

The duplicated rule **definitions** are gone — the divergence root the
`alerts_defaults.go` comment warned about. Both consumers' public shapes
(`AlertPattern`, `AlertMatch`, `BuildError`) and behaviour are unchanged; all
existing overlay/tools tests pass and `classify` gets its own direct tests.

**Two deliberate deviations from the original plan below:**

1. **No `ClassifyLine`/`ClassifyBlock` unified-orchestration entrypoints.** The
   per-surface orchestration (overlay `ProcessLine`'s dedup/batch/recent-ring
   sequence; tools' extract rendering) was left in place. Routing both through a
   single function would have meant either dead code or a risky rewrite of two
   well-tested pipelines for no behavioural gain once the rule data is shared. A
   follow-up can consolidate orchestration if desired.

2. **ASP.NET log-level precedence dropped.** The original plan had explicit
   `warn:`/`info:` levels override the keyword heuristic. Per maintainer
   direction, the GraphQL-validation lines *should* surface as **error** (they
   are real client/schema mismatches), so the catch-all keeps its error
   severity and no downgrade was added.

---

## Original design (for reference)


## Problem

agnt classifies process output through **two independent banks** that disagree
on the same line:

1. **`internal/overlay/alerts_defaults.go`** — 55 broad `AlertPattern` regexes
   plus the `unparsedErrorRe` keyword catch-all. Per-line, heuristic. Feeds the
   overlay popup/toasts and (via `internal/incident/adapter_alert.go`) the
   incident pipeline. Output shape: `AlertPattern{Severity, Category,
   Description}`.

2. **`internal/tools/build_error_parsers.go`** — 9 precise, multi-line,
   location-aware structured parsers (tsc / eslint / vite / webpack / go / rust
   / pytest / jest / gotest). Reads each tool's **declared** level. Sole
   consumer: `internal/tools/daemon_proc_signals.go` (the `proc output`
   `extract:["error"|"warning"]` compact rendering). Output shape:
   `BuildError{Tool, Severity, File, Line, Col, Code, Message}`.

### The divergence (observed)

BifrostQL (.NET) logs GraphQL validation failures via the default ILogger
console formatter:

```
warn: BifrostQL.Server.Logging.BifrostLoggingModule[0]
      GraphQL Error: Cannot query field 'category_id' on type 'categories_paged'. ...
```

- Bank 1: the indented message trips `unparsedErrorRe` (word "Error") → surfaced
  as **error** in the popup/incident inbox. (The `warn:` level on the preceding
  header line is ignored — line-based scan.)
- Bank 2: matches **no** structured parser → **absent** from `proc output`
  extract.

Same signal, two verdicts: an agent gets an error push but sees nothing when it
queries `proc output --extract error`. Two banks = two maintenance points, two
severity policies, silent drift.

> Note: the GraphQL case *should* be an error (a real client/schema mismatch).
> The defect is not the severity value here — it is that **two code paths decide
> it independently**.

## Goal

One classifier is the **single source of truth** for "given a process-output
line (with recent context), what is its severity, category, and any structured
fields." Both consumers call it. Removing one bank is not the goal — the two
banks extract *different* things (alert severity vs. located build error); the
goal is one decision surface they both defer to.

## Target Design

New low-level package **`internal/classify`** (imported by both `overlay` and
`tools`; neither imports the other today, so a shared dependency avoids a
cycle).

```go
package classify

type Severity string // "error" | "warning" | "info"

// Classification is the unified verdict for one logical output unit (a line,
// or a multi-line block folded by a structured parser).
type Classification struct {
    Matched     bool
    Severity    Severity
    Category    string   // "dotnet", "tsc", "generic", "unparsed", ...
    Description string   // human label for the matching rule
    RuleID      string   // stable id (migrated from AlertPattern.ID / parser tool)

    // Structured fields — populated when a precise parser recognised the unit.
    // Zero values when only a heuristic/broad rule matched.
    Tool string
    File string
    Line int
    Col  int
    Code string
    Msg  string
}

// ClassifyLine applies the merged rule set to a single line, given recent
// preceding lines for context (ASP.NET header inheritance, structured-parser
// cause lookback). Returns Matched=false when nothing fires.
func ClassifyLine(line string, recent []string) Classification

// ClassifyBlock runs the structured (multi-line, location-aware) parsers over a
// slice of lines, returning one Classification per recognised block. Used by
// `proc output` extract.
func ClassifyBlock(lines []string) []Classification
```

Rule precedence inside `classify` (highest wins):
1. **Structured parsers** (former bank 2) — most precise; carry location +
   tool-declared severity (authoritative: tsc/rust/eslint state error|warning).
2. **Specific broad patterns** (former bank 1, the 54 non-catch-all entries).
3. **Explicit log-level prefix** — ASP.NET ILogger (`trce|dbug|info|warn|fail|
   crit|error:`) and similar: when present, the app's declared level wins over
   the keyword heuristic. (Resolves the line-vs-header context loss; GraphQL
   stays the app's level.)
4. **`unparsedErrorRe` keyword catch-all** — last resort, severity error.

### Consumer adaptation

- **`overlay.AlertScanner`** keeps its dedup/batch/activity-defer/queue
  machinery untouched. `ProcessLine` replaces its inline pattern loop +
  structured-parser call + catch-all with one `classify.ClassifyLine(line,
  recentSnapshot())`, then maps `Classification` → `AlertMatch`/`AlertPattern`.
  `internal/overlay/structured_parsers.go` folds into `classify`.
- **`tools/daemon_proc_signals.go`** replaces `parseBuildErrors(lines)` with
  `classify.ClassifyBlock(lines)`, mapping `Classification` (structured subset)
  → `BuildError`. `extract` severity filter reads `Classification.Severity`.
- **`incident/adapter_alert.go`** unchanged — still maps `AlertMatch.Severity`.

### Back-compat

- `AlertPattern`, `AlertMatch`, `BuildError` public shapes preserved at the
  consumer boundary (thin mappers), so `get_errors`, `get_incidents`, overlay
  rendering, and `proc output` JSON contracts do not change.
- `unparsedPatternID` / `Category=="unparsed"` semantics preserved.

## Migration Steps (TDD)

1. Create `internal/classify` with `Classification`, `Severity`, and the merged
   rule tables (move regexes + structured parsers verbatim first, no behaviour
   change). Port existing `alerts_test.go` pattern assertions + the
   `build_error_parsers` parser tests into `classify` package tests. **Green.**
2. Add `ClassifyLine` / `ClassifyBlock` with the precedence above. New tests:
   the bifrost GraphQL case (one severity, both entrypoints agree), tsc/rust
   located errors, ASP.NET level inheritance, catch-all fallback.
3. Rewire `overlay.AlertScanner.ProcessLine` to `ClassifyLine`; delete the
   overlay pattern bank + `structured_parsers.go`. Run full overlay suite.
4. Rewire `tools/daemon_proc_signals.go` to `ClassifyBlock`; delete
   `build_error_parsers.go`. Run tools suite.
5. Cross-check: a table test asserting representative lines get **identical
   severity** from `ClassifyLine` and (where applicable) `ClassifyBlock`.
6. Full `go test ./... -race` (daemon suite `-p 1`), `go vet`, `go build ./...`.

## Risks

- **Two large tested subsystems** rewired — mitigated by step 1 (verbatim move,
  green) before any behaviour change, and by preserving public shapes.
- **Precedence changes severity for edge lines** — e.g. a line currently
  catch-all "error" that an ASP.NET `warn:` now downgrades. This is the intended
  correction but must be enumerated in tests so the delta is explicit, not
  silent.
- **Multi-line context**: overlay scans line-by-line (3-line recent ring);
  `ClassifyBlock` sees a whole slice. The structured parsers needing lookback
  must work from both — keep the recent-ring contract for `ClassifyLine`.

## Out of Scope

- Changing what severity any specific rule yields (beyond the explicit-level
  precedence correction).
- The incident pipeline band model, dedup, or delivery.
- Removing either public type (`AlertMatch` / `BuildError`).
