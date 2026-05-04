package tools

import (
	"strings"
	"testing"
)

// TestParseBuildErrors_TSC covers the canonical TypeScript compiler error
// formats. tsc emits two shapes depending on flags:
//
//	src/foo.ts(12,3): error TS2322: Type 'string' not assignable to 'number'  // default
//	src/foo.ts:12:3 - error TS2322: Type 'string' not assignable to 'number'  // --pretty
//
// Both must produce {file, line, col, code, message} with identical fields.
func TestParseBuildErrors_TSC(t *testing.T) {
	cases := []struct {
		name string
		line string
	}{
		{"paren-form", "src/foo.ts(12,3): error TS2322: Type 'string' not assignable to 'number'"},
		{"colon-form", "src/foo.ts:12:3 - error TS2322: Type 'string' not assignable to 'number'"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseBuildErrors([]string{tc.line})
			if len(got) != 1 {
				t.Fatalf("want 1 error, got %d: %+v", len(got), got)
			}
			be := got[0]
			if be.Tool != "tsc" {
				t.Errorf("tool=%q, want tsc", be.Tool)
			}
			if be.File != "src/foo.ts" {
				t.Errorf("file=%q, want src/foo.ts", be.File)
			}
			if be.Line != 12 {
				t.Errorf("line=%d, want 12", be.Line)
			}
			if be.Col != 3 {
				t.Errorf("col=%d, want 3", be.Col)
			}
			if be.Code != "TS2322" {
				t.Errorf("code=%q, want TS2322", be.Code)
			}
			if !strings.Contains(be.Message, "not assignable") {
				t.Errorf("message=%q missing 'not assignable'", be.Message)
			}
			if be.RawLine != tc.line {
				t.Errorf("raw_line not preserved")
			}
		})
	}
}

// TestParseBuildErrors_ESLint matches the default stylish formatter output:
//
//	src/foo.ts
//	  12:3  error  Unexpected console statement  no-console
//
// We accept the indented-row variant ESLint emits when grouped under a
// file header; the parser keeps file context across grouped lines.
func TestParseBuildErrors_ESLint(t *testing.T) {
	lines := []string{
		"/abs/path/src/foo.ts",
		"  12:3  error  Unexpected console statement  no-console",
		"  15:7  warning  'x' is assigned a value but never used  no-unused-vars",
	}
	got := parseBuildErrors(lines)
	if len(got) != 2 {
		t.Fatalf("want 2 errors, got %d: %+v", len(got), got)
	}
	first := got[0]
	if first.Tool != "eslint" {
		t.Errorf("tool=%q, want eslint", first.Tool)
	}
	if first.File != "/abs/path/src/foo.ts" {
		t.Errorf("file=%q, want /abs/path/src/foo.ts", first.File)
	}
	if first.Line != 12 || first.Col != 3 {
		t.Errorf("loc=%d:%d, want 12:3", first.Line, first.Col)
	}
	if first.Severity != "error" {
		t.Errorf("severity=%q, want error", first.Severity)
	}
	if first.Rule != "no-console" {
		t.Errorf("rule=%q, want no-console", first.Rule)
	}
	if !strings.Contains(first.Message, "Unexpected console") {
		t.Errorf("message=%q missing 'Unexpected console'", first.Message)
	}
	if got[1].Severity != "warning" {
		t.Errorf("second severity=%q, want warning", got[1].Severity)
	}
	if got[1].Rule != "no-unused-vars" {
		t.Errorf("second rule=%q, want no-unused-vars", got[1].Rule)
	}
}

// TestParseBuildErrors_Vite covers vite/esbuild structured error blocks:
//
//	✗ [ERROR] Could not resolve "missing"
//	    src/foo.ts:12:3:
//	      12 │ import x from "missing"
//	         ╵        ^
//
// We require {file, line, col, message}; the indented squiggle/source line
// is ignored.
func TestParseBuildErrors_Vite(t *testing.T) {
	lines := []string{
		`✗ [ERROR] Could not resolve "missing"`,
		`    src/foo.ts:12:3:`,
		`      12 │ import x from "missing"`,
	}
	got := parseBuildErrors(lines)
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "vite" {
		t.Errorf("tool=%q, want vite", be.Tool)
	}
	if be.File != "src/foo.ts" {
		t.Errorf("file=%q, want src/foo.ts", be.File)
	}
	if be.Line != 12 || be.Col != 3 {
		t.Errorf("loc=%d:%d, want 12:3", be.Line, be.Col)
	}
	if !strings.Contains(be.Message, "Could not resolve") {
		t.Errorf("message=%q missing 'Could not resolve'", be.Message)
	}
}

// TestParseBuildErrors_Webpack covers webpack module build failures:
//
//	ERROR in ./src/foo.ts
//	Module build failed: SyntaxError: Unexpected token (12:3)
//
// At minimum we capture {file, message}; line/col are best-effort.
func TestParseBuildErrors_Webpack(t *testing.T) {
	lines := []string{
		"ERROR in ./src/foo.ts",
		"Module build failed: SyntaxError: Unexpected token (12:3)",
	}
	got := parseBuildErrors(lines)
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "webpack" {
		t.Errorf("tool=%q, want webpack", be.Tool)
	}
	if be.File != "./src/foo.ts" {
		t.Errorf("file=%q, want ./src/foo.ts", be.File)
	}
	if !strings.Contains(be.Message, "Module build failed") {
		t.Errorf("message=%q missing 'Module build failed'", be.Message)
	}
}

// TestParseBuildErrors_Go covers the Go compiler error format:
//
//	./foo.go:12:3: undefined: Bar
//
// Some builds emit the path with a leading "./", others without; both
// must parse.
func TestParseBuildErrors_Go(t *testing.T) {
	cases := []string{
		"./foo.go:12:3: undefined: Bar",
		"foo.go:12:3: undefined: Bar",
		"internal/pkg/foo.go:12:3: cannot use x (type int) as type string",
	}
	for _, line := range cases {
		t.Run(line, func(t *testing.T) {
			got := parseBuildErrors([]string{line})
			if len(got) != 1 {
				t.Fatalf("want 1 error, got %d: %+v", len(got), got)
			}
			be := got[0]
			if be.Tool != "go" {
				t.Errorf("tool=%q, want go", be.Tool)
			}
			if be.Line != 12 || be.Col != 3 {
				t.Errorf("loc=%d:%d, want 12:3", be.Line, be.Col)
			}
			if !strings.HasSuffix(be.File, "foo.go") {
				t.Errorf("file=%q, want suffix foo.go", be.File)
			}
			if be.Message == "" {
				t.Errorf("message empty")
			}
		})
	}
}

// TestParseBuildErrors_Rust covers cargo/rustc errors. The header line and
// the file pointer are on separate lines:
//
//	error[E0308]: mismatched types
//	  --> src/main.rs:12:3
//
// We capture {file, line, col, code, message}.
func TestParseBuildErrors_Rust(t *testing.T) {
	lines := []string{
		"error[E0308]: mismatched types",
		"  --> src/main.rs:12:3",
	}
	got := parseBuildErrors(lines)
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "rust" {
		t.Errorf("tool=%q, want rust", be.Tool)
	}
	if be.Code != "E0308" {
		t.Errorf("code=%q, want E0308", be.Code)
	}
	if be.File != "src/main.rs" {
		t.Errorf("file=%q, want src/main.rs", be.File)
	}
	if be.Line != 12 || be.Col != 3 {
		t.Errorf("loc=%d:%d, want 12:3", be.Line, be.Col)
	}
	if !strings.Contains(be.Message, "mismatched types") {
		t.Errorf("message=%q missing 'mismatched types'", be.Message)
	}
}

// TestParseBuildErrors_Pytest covers pytest summary lines:
//
//	FAILED tests/test_foo.py::test_bar - AssertionError: ...
//
// We capture {file, test, message}.
func TestParseBuildErrors_Pytest(t *testing.T) {
	line := "FAILED tests/test_foo.py::test_bar - AssertionError: assert 1 == 2"
	got := parseBuildErrors([]string{line})
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "pytest" {
		t.Errorf("tool=%q, want pytest", be.Tool)
	}
	if be.File != "tests/test_foo.py" {
		t.Errorf("file=%q, want tests/test_foo.py", be.File)
	}
	if be.Test != "test_bar" {
		t.Errorf("test=%q, want test_bar", be.Test)
	}
	if !strings.Contains(be.Message, "AssertionError") {
		t.Errorf("message=%q missing 'AssertionError'", be.Message)
	}
}

// TestParseBuildErrors_Jest covers Jest/vitest failure entries. The test
// name appears on a `●` header and the failing location follows in an
// indented `at` frame:
//
//	● ProductList › renders empty state
//	  at Object.<anonymous> (src/components/List.test.tsx:42:15)
func TestParseBuildErrors_Jest(t *testing.T) {
	lines := []string{
		"  ● ProductList › renders empty state",
		"    at Object.<anonymous> (src/components/List.test.tsx:42:15)",
	}
	got := parseBuildErrors(lines)
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "jest" {
		t.Errorf("tool=%q, want jest", be.Tool)
	}
	if be.Test == "" || !strings.Contains(be.Test, "renders empty state") {
		t.Errorf("test=%q missing 'renders empty state'", be.Test)
	}
	if be.File != "src/components/List.test.tsx" {
		t.Errorf("file=%q, want src/components/List.test.tsx", be.File)
	}
	if be.Line != 42 {
		t.Errorf("line=%d, want 42", be.Line)
	}
}

// TestParseBuildErrors_GoTest covers `go test` failure format:
//
//	--- FAIL: TestFoo (0.01s)
//	    foo_test.go:12: assertion failed: want 1, got 2
//
// We capture {file, line, test, message}.
func TestParseBuildErrors_GoTest(t *testing.T) {
	lines := []string{
		"--- FAIL: TestFoo (0.01s)",
		"    foo_test.go:12: assertion failed: want 1, got 2",
	}
	got := parseBuildErrors(lines)
	if len(got) != 1 {
		t.Fatalf("want 1 error, got %d: %+v", len(got), got)
	}
	be := got[0]
	if be.Tool != "gotest" {
		t.Errorf("tool=%q, want gotest", be.Tool)
	}
	if be.Test != "TestFoo" {
		t.Errorf("test=%q, want TestFoo", be.Test)
	}
	if be.File != "foo_test.go" {
		t.Errorf("file=%q, want foo_test.go", be.File)
	}
	if be.Line != 12 {
		t.Errorf("line=%d, want 12", be.Line)
	}
	if !strings.Contains(be.Message, "assertion failed") {
		t.Errorf("message=%q missing 'assertion failed'", be.Message)
	}
}

// TestParseBuildErrors_UnknownFormat verifies the parser silently skips
// lines that don't match any known format. No panic, no error, just an
// empty slice — the caller's existing line-level errors:[...] array
// continues to surface them via the legacy path.
func TestParseBuildErrors_UnknownFormat(t *testing.T) {
	lines := []string{
		"this is just a normal log line",
		"another line with no structure",
		"random output: things are happening",
	}
	got := parseBuildErrors(lines)
	if len(got) != 0 {
		t.Errorf("want 0 errors for unknown format, got %d: %+v", len(got), got)
	}
}

// TestParseBuildErrors_MixedTools confirms multiple parsers can fire in
// the same line buffer (e.g., a CI log that runs tsc + go test).
func TestParseBuildErrors_MixedTools(t *testing.T) {
	lines := []string{
		"src/foo.ts(12,3): error TS2322: Type mismatch",
		"./foo.go:12:3: undefined: Bar",
		"FAILED tests/test_x.py::test_a - AssertionError",
	}
	got := parseBuildErrors(lines)
	if len(got) != 3 {
		t.Fatalf("want 3 errors, got %d: %+v", len(got), got)
	}
	tools := map[string]bool{}
	for _, be := range got {
		tools[be.Tool] = true
	}
	for _, want := range []string{"tsc", "go", "pytest"} {
		if !tools[want] {
			t.Errorf("missing tool %q in result", want)
		}
	}
}

// TestFormatBuildError_Compact verifies the single-line render the agent
// sees in compact output. Acceptance: ~120 chars max for typical errors.
func TestFormatBuildError_Compact(t *testing.T) {
	be := BuildError{
		Tool:     "tsc",
		Severity: "error",
		File:     "src/components/Foo.tsx",
		Line:     42,
		Col:      7,
		Code:     "TS2345",
		Message:  "Argument of type 'string' not assignable to 'number'",
	}
	got := formatBuildErrorCompact(be)
	if !strings.Contains(got, "src/components/Foo.tsx:42:7") {
		t.Errorf("missing file:line:col in %q", got)
	}
	if !strings.Contains(got, "TS2345") {
		t.Errorf("missing code in %q", got)
	}
	if !strings.Contains(got, "not assignable") {
		t.Errorf("missing message in %q", got)
	}
	if len(got) > 130 {
		t.Errorf("compact line too long (%d > 130 chars): %q", len(got), got)
	}
}

// TestFormatBuildError_Compact_LongMessage verifies the format helper
// truncates pathological compiler messages so a single error can't blow
// past the agent's reasonable line budget. Hard ceiling is 200 chars.
func TestFormatBuildError_Compact_LongMessage(t *testing.T) {
	long := strings.Repeat("very long type name and ", 30) // ~700 chars
	be := BuildError{
		Tool: "tsc", Severity: "error", File: "src/foo.ts",
		Line: 1, Col: 1, Code: "TS2345", Message: long,
	}
	got := formatBuildErrorCompact(be)
	if len(got) > 200 {
		t.Errorf("compact line not truncated (%d chars): %q", len(got), got)
	}
	if !strings.HasSuffix(got, "…") {
		t.Errorf("expected ellipsis suffix on truncation: %q", got)
	}
}

// TestFormatBuildError_Compact_NoCol handles the case where col is unknown
// (e.g., webpack file-only errors). Format must not print "::0".
func TestFormatBuildError_Compact_NoCol(t *testing.T) {
	be := BuildError{
		Tool:    "webpack",
		File:    "./src/foo.ts",
		Message: "Module build failed",
	}
	got := formatBuildErrorCompact(be)
	if strings.Contains(got, ":0:") || strings.Contains(got, ":0 ") {
		t.Errorf("should not print zero line/col: %q", got)
	}
	if !strings.Contains(got, "./src/foo.ts") {
		t.Errorf("missing file in %q", got)
	}
}

// TestExtractSignals_StructuredErrors verifies the extract path returns
// structured BuildError objects in the SignalSet. The legacy errors[]
// slice continues to surface the *broad* error markers (ERROR/panic/
// fatal/error TS####/error[E####]/Traceback) — it is the no-regression
// fallback for unknown structured formats. Lines that match a structured
// parser but not the legacy regex (e.g. `./foo.go:12:3: undefined: Bar`
// has no broad keyword) appear in BuildErrors only — that's the whole
// point of the parser bank.
func TestExtractSignals_StructuredErrors(t *testing.T) {
	lines := []string{
		"src/foo.ts(12,3): error TS2322: Type mismatch",
		"unrelated log line",
		"./foo.go:12:3: undefined: Bar",
	}
	got := extractSignals(lines, []string{"error"})
	// Legacy errors[] still populated for the tsc line (matches `error TS\d+`).
	if len(got.Errors) < 1 {
		t.Errorf("want >=1 raw error (tsc TS line), got %d: %v", len(got.Errors), got.Errors)
	}
	// Both structured errors populated regardless.
	if len(got.BuildErrors) != 2 {
		t.Fatalf("want 2 structured errors, got %d: %+v", len(got.BuildErrors), got.BuildErrors)
	}
	tools := map[string]bool{}
	for _, be := range got.BuildErrors {
		tools[be.Tool] = true
	}
	if !tools["tsc"] || !tools["go"] {
		t.Errorf("missing tsc/go in tools: %v", tools)
	}
}

// TestExtractSignals_StructuredErrors_NoRegression confirms the unknown-format
// fallback: lines that don't match any parser still surface in the legacy
// Errors slice when they hit the broad regex. BuildErrors is empty.
func TestExtractSignals_StructuredErrors_NoRegression(t *testing.T) {
	lines := []string{
		"ERROR something exploded",
		"panic: runtime error: index out of range",
	}
	got := extractSignals(lines, []string{"error"})
	if len(got.Errors) < 2 {
		t.Errorf("legacy errors[] regression: want >=2, got %d: %v", len(got.Errors), got.Errors)
	}
	if len(got.BuildErrors) != 0 {
		t.Errorf("unknown formats should not produce BuildErrors, got %+v", got.BuildErrors)
	}
}

// TestAppendSingleStreamBuildErrors_AppendsBlock verifies the single-
// stream compact path tacks a "=== Build Errors (N) ===" block onto the
// raw output when parsers hit. Empty errs means no block.
func TestAppendSingleStreamBuildErrors_AppendsBlock(t *testing.T) {
	raw := "compiling...\nsrc/foo.ts(12,3): error TS2322: Type mismatch\n"
	errs := []BuildError{{
		Tool: "tsc", Severity: "error", File: "src/foo.ts",
		Line: 12, Col: 3, Code: "TS2322", Message: "Type mismatch",
	}}
	out := appendSingleStreamBuildErrors(raw, errs)
	if !strings.Contains(out, "=== Build Errors (1) ===") {
		t.Errorf("missing summary header: %q", out)
	}
	if !strings.Contains(out, "[tsc:error] src/foo.ts:12:3 — TS2322:") {
		t.Errorf("missing compact line: %q", out)
	}
	// Raw lines preserved.
	if !strings.Contains(out, "compiling...") {
		t.Errorf("raw lines lost: %q", out)
	}
}

func TestAppendSingleStreamBuildErrors_EmptyErrs(t *testing.T) {
	raw := "just some output\n"
	got := appendSingleStreamBuildErrors(raw, nil)
	if got != raw {
		t.Errorf("empty errs should return raw unchanged, got %q", got)
	}
}

// TestAssembleMultiStreamOutput_BuildErrors verifies the multi-stream
// compact output emits a per-stream "[id] [tool:sev] file:line ..." line
// in the build-errors block when extract was requested.
func TestAssembleMultiStreamOutput_BuildErrors(t *testing.T) {
	streams := []processStream{
		{ProcessID: "build", Lines: []string{
			"src/foo.ts(12,3): error TS2322: Type mismatch",
		}},
		{ProcessID: "tests", Lines: []string{
			"--- FAIL: TestFoo (0.01s)",
			"    foo_test.go:12: assertion failed",
		}},
	}
	out := assembleMultiStreamOutput(streams, []string{"error"}, false)
	if !strings.Contains(out.Output, "=== Build Errors (2) ===") {
		t.Errorf("missing summary header: %q", out.Output)
	}
	if !strings.Contains(out.Output, "[build] [tsc:error] src/foo.ts:12:3") {
		t.Errorf("missing per-stream tsc compact line: %q", out.Output)
	}
	if !strings.Contains(out.Output, "[tests] [gotest:error] foo_test.go:12") {
		t.Errorf("missing per-stream gotest compact line: %q", out.Output)
	}
}

// TestExtractSignals_StructuredErrors_WarningSeverityFilter verifies that
// asking only for "warning" surfaces only warning-severity BuildErrors,
// and asking only for "error" surfaces only error-severity ones.
func TestExtractSignals_StructuredErrors_WarningSeverityFilter(t *testing.T) {
	lines := []string{
		"/abs/path/src/foo.ts",
		"  12:3  error  Unexpected console statement  no-console",
		"  15:7  warning  unused var  no-unused-vars",
	}
	gotErr := extractSignals(lines, []string{"error"})
	gotWarn := extractSignals(lines, []string{"warning"})
	gotBoth := extractSignals(lines, []string{"error", "warning"})

	if len(gotErr.BuildErrors) != 1 || gotErr.BuildErrors[0].Severity != "error" {
		t.Errorf("error-only: want 1 error BuildError, got %+v", gotErr.BuildErrors)
	}
	if len(gotWarn.BuildErrors) != 1 || gotWarn.BuildErrors[0].Severity != "warning" {
		t.Errorf("warning-only: want 1 warning BuildError, got %+v", gotWarn.BuildErrors)
	}
	if len(gotBoth.BuildErrors) != 2 {
		t.Errorf("both: want 2 BuildErrors, got %+v", gotBoth.BuildErrors)
	}
}
