package classify

import "testing"

// TestDefaultLineRules_Integrity pins structural invariants of the merged rule
// bank: non-empty, unique IDs, every rule carries a compiled pattern + category
// + a valid severity. A duplicate ID would make DisablePattern ambiguous in the
// scanner; a nil pattern would panic on scan.
func TestDefaultLineRules_Integrity(t *testing.T) {
	rules := DefaultLineRules()
	if len(rules) < 50 {
		t.Fatalf("expected the full rule bank (>=50), got %d", len(rules))
	}
	seen := map[string]bool{}
	for _, r := range rules {
		if r.ID == "" || seen[r.ID] {
			t.Errorf("rule ID empty or duplicated: %q", r.ID)
		}
		seen[r.ID] = true
		if r.Pattern == nil {
			t.Errorf("rule %q has nil pattern", r.ID)
		}
		if r.Category == "" {
			t.Errorf("rule %q has empty category", r.ID)
		}
		switch r.Severity {
		case SeverityError, SeverityWarning, SeverityInfo:
		default:
			t.Errorf("rule %q has invalid severity %q", r.ID, r.Severity)
		}
	}
}

// TestDefaultLineRules_Classification spot-checks representative lines across
// severities and categories — the behaviour the overlay AlertScanner depends on.
func TestDefaultLineRules_Classification(t *testing.T) {
	rules := DefaultLineRules()
	byID := map[string]LineRule{}
	for _, r := range rules {
		byID[r.ID] = r
	}

	cases := []struct {
		ruleID string
		line   string
		sev    Severity
	}{
		{"dotnet-build-error", "Build FAILED.", SeverityError},
		{"connection-refused", "Error: connect ECONNREFUSED 127.0.0.1:5000", SeverityWarning},
		{"go-panic", "panic: runtime error: index out of range", SeverityError},
		{"rebuild-build-success", "build succeeded in 1.2s", SeverityInfo},
		{"npm-err", "npm ERR! code ELIFECYCLE", SeverityError},
	}
	for _, c := range cases {
		r, ok := byID[c.ruleID]
		if !ok {
			t.Errorf("rule %q missing from bank", c.ruleID)
			continue
		}
		if !r.Pattern.MatchString(c.line) {
			t.Errorf("rule %q did not match %q", c.ruleID, c.line)
		}
		if r.Severity != c.sev {
			t.Errorf("rule %q severity = %q, want %q", c.ruleID, r.Severity, c.sev)
		}
	}
}

// TestParseBuildErrors covers the located, tool-declared-severity path shared
// with `proc output` extract: file/line/col/code and the error|warning split.
func TestParseBuildErrors(t *testing.T) {
	lines := []string{
		"src/foo.ts(12,3): error TS2322: Type 'x' is not assignable to 'y'",
		"error[E0308]: mismatched types",
		"  --> src/main.rs:7:9",
		"./pkg/svc.go:42:5: undefined: Bar",
	}
	errs := ParseBuildErrors(lines)
	if len(errs) != 3 {
		t.Fatalf("expected 3 build errors, got %d: %+v", len(errs), errs)
	}

	if errs[0].Tool != "tsc" || errs[0].Severity != "error" || errs[0].Code != "TS2322" ||
		errs[0].File != "src/foo.ts" || errs[0].Line != 12 || errs[0].Col != 3 {
		t.Errorf("tsc parse wrong: %+v", errs[0])
	}
	// Rust header + location fold into one error with file/line/col.
	if errs[1].Tool != "rust" || errs[1].Code != "E0308" || errs[1].File != "src/main.rs" ||
		errs[1].Line != 7 || errs[1].Col != 9 {
		t.Errorf("rust parse wrong: %+v", errs[1])
	}
	if errs[2].Tool != "go" || errs[2].File != "./pkg/svc.go" || errs[2].Line != 42 || errs[2].Col != 5 {
		t.Errorf("go parse wrong: %+v", errs[2])
	}
}

// TestParseBuildErrors_WarningSeverity confirms tool-declared "warning" is
// preserved (eslint surfaces both error and warning).
func TestParseBuildErrors_WarningSeverity(t *testing.T) {
	lines := []string{
		"src/app.ts",
		"  12:3  warning  Unexpected console statement  no-console",
	}
	errs := ParseBuildErrors(lines)
	if len(errs) != 1 || errs[0].Tool != "eslint" || errs[0].Severity != "warning" ||
		errs[0].Rule != "no-console" || errs[0].File != "src/app.ts" {
		t.Fatalf("eslint warning parse wrong: %+v", errs)
	}
}

// TestParseBuildErrors_CRLF: output split on "\n" from a Windows tool keeps a
// trailing "\r" on every line. It must parse exactly like the LF form, and the
// caller's slice must not be modified.
func TestParseBuildErrors_CRLF(t *testing.T) {
	lines := []string{
		"src/app.ts\r",
		"  12:3  error  Unexpected console statement  no-console\r",
		"\r",
		"  13:1  error  Not under a file header  no-undef\r",
		"FAILED tests/test_foo.py::test_bar\r",
	}
	errs := ParseBuildErrors(lines)
	if len(errs) != 2 {
		t.Fatalf("got %d errors, want 2 (eslint row, pytest): %+v", len(errs), errs)
	}
	if errs[0].Tool != "eslint" || errs[0].File != "src/app.ts" || errs[0].Rule != "no-console" {
		t.Errorf("eslint row under a CRLF header parsed wrong: %+v", errs[0])
	}
	if errs[1].Tool != "pytest" || errs[1].File != "tests/test_foo.py" || errs[1].Test != "test_bar" {
		t.Errorf("CRLF pytest line parsed wrong: %+v", errs[1])
	}
	if lines[0] != "src/app.ts\r" {
		t.Errorf("caller's slice was modified: %q", lines[0])
	}
}

// TestRunStructuredLine folds Prisma / DB-auth blocks; TestIsStructuralPrefix
// guards the banner-folding contract.
func TestRunStructuredLine(t *testing.T) {
	se, ok := RunStructuredLine("Authentication failed against database server", nil)
	if !ok || se.Kind != "prisma" || se.Severity != SeverityError {
		t.Fatalf("prisma cause not folded: ok=%v se=%+v", ok, se)
	}
	se2, ok2 := RunStructuredLine(`password authentication failed for user "app"`, nil)
	if !ok2 || se2.Kind != "db-auth" || se2.Category != "database" {
		t.Fatalf("db-auth not folded: ok=%v se=%+v", ok2, se2)
	}
	if _, ok3 := RunStructuredLine("GET /api/users 200 12ms", nil); ok3 {
		t.Error("ordinary log line must not fold as structured")
	}
}

func TestIsStructuralPrefix(t *testing.T) {
	if !IsStructuralPrefix("prisma:error") {
		t.Error("prisma:error must be a structural prefix")
	}
	if IsStructuralPrefix("just a normal line") {
		t.Error("normal line must not be a structural prefix")
	}
}

// TestParseBuildErrors_WindowsDrivePaths pins that every located parser
// accepts an absolute Windows path. The path groups exclude ':' so they
// used to stop at the colon in `C:` and never match a `C:\...` location.
func TestParseBuildErrors_WindowsDrivePaths(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
		tool  string
		file  string
		line  int
		col   int
	}{
		{"tsc colon", []string{`C:\proj\src\foo.ts:12:3 - error TS2322: Type 'x' is not assignable`}, "tsc", `C:\proj\src\foo.ts`, 12, 3},
		{"tsc paren", []string{`C:\proj\src\foo.ts(12,3): error TS2322: Type 'x'`}, "tsc", `C:\proj\src\foo.ts`, 12, 3},
		{"eslint", []string{`C:\proj\src\app.ts`, "  4:7  error  Unexpected console statement  no-console"}, "eslint", `C:\proj\src\app.ts`, 4, 7},
		{"eslint forward slash", []string{`D:/proj/src/app.tsx`, "  4:7  error  Unexpected console statement  no-console"}, "eslint", `D:/proj/src/app.tsx`, 4, 7},
		{"vite", []string{"✘ [ERROR] Expected \";\" but found \"}\"", `    C:\proj\src\main.ts:9:2:`}, "vite", `C:\proj\src\main.ts`, 9, 2},
		{"go compile", []string{`C:\proj\pkg\svc.go:42:5: undefined: Bar`}, "go", `C:\proj\pkg\svc.go`, 42, 5},
		{"rust", []string{"error[E0308]: mismatched types", `  --> C:\proj\src\main.rs:7:9`}, "rust", `C:\proj\src\main.rs`, 7, 9},
		{"pytest", []string{`FAILED C:\proj\tests\test_foo.py::test_bar - AssertionError: boom`}, "pytest", `C:\proj\tests\test_foo.py`, 0, 0},
		{"jest", []string{"  ● suite › works", `      at Object.<anonymous> (C:\proj\src\foo.test.ts:12:3)`}, "jest", `C:\proj\src\foo.test.ts`, 12, 3},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			errs := ParseBuildErrors(c.lines)
			if len(errs) != 1 {
				t.Fatalf("expected 1 build error, got %d: %+v", len(errs), errs)
			}
			e := errs[0]
			if e.Tool != c.tool || e.File != c.file || e.Line != c.line || e.Col != c.col {
				t.Errorf("got tool=%q file=%q line=%d col=%d, want tool=%q file=%q line=%d col=%d",
					e.Tool, e.File, e.Line, e.Col, c.tool, c.file, c.line, c.col)
			}
		})
	}
}

// TestParseBuildErrors_JestRealBlock feeds the parser what jest actually
// prints for a failed assertion: the header, the matcher message, the
// expected/received block and a code frame, and only then the stack frame,
// fifteen lines below the header. A five-line lookahead never reached it, so
// real jest output yielded a test name with no file. The lookahead must also
// stop at the next test's header rather than lend that test's frame to a test
// whose own frame never came.
func TestParseBuildErrors_JestRealBlock(t *testing.T) {
	block := []string{
		" FAIL  src/sum.test.js",
		"  ● adds 1 + 2 to equal 3",
		"",
		"    expect(received).toBe(expected) // Object.is equality",
		"",
		"    Expected: 4",
		"    Received: 3",
		"",
		"      1 | const sum = require('./sum');",
		"      2 | test('adds 1 + 2 to equal 3', () => {",
		"    > 3 |   expect(sum(1, 2)).toBe(4);",
		"        |                     ^",
		"      4 | });",
		"      5 |",
		"",
		"      at Object.toBe (src/sum.test.js:3:21)",
		"",
		"  ● has no frame",
		"",
		"    thrown: \"Exceeded timeout of 5000 ms for a test.\"",
		"",
		"  ● second failure",
		"",
		"      at Object.toEqual (src/other.test.js:9:7)",
	}
	errs := ParseBuildErrors(block)
	if len(errs) != 3 {
		t.Fatalf("want 3 jest errors, got %d: %+v", len(errs), errs)
	}
	if e := errs[0]; e.Test != "adds 1 + 2 to equal 3" || e.File != "src/sum.test.js" || e.Line != 3 || e.Col != 21 {
		t.Errorf("first test: got test=%q file=%q line=%d col=%d", e.Test, e.File, e.Line, e.Col)
	}
	if e := errs[1]; e.Test != "has no frame" || e.File != "" {
		t.Errorf("a test with no frame must not borrow the next test's: got test=%q file=%q", e.Test, e.File)
	}
	if e := errs[2]; e.Test != "second failure" || e.File != "src/other.test.js" || e.Line != 9 {
		t.Errorf("third test: got test=%q file=%q line=%d", e.Test, e.File, e.Line)
	}
}

// TestParseBuildErrors_DriveNearMisses pins that the drive prefix needs a
// single letter, a colon and a path separator: a URL scheme or a clock time
// is never read as a drive path.
func TestParseBuildErrors_DriveNearMisses(t *testing.T) {
	cases := []struct {
		name  string
		lines []string
	}{
		{"tsc colon url", []string{"http://localhost:5173/src/foo.ts:12:3 - error TS2322: Type 'x'"}},
		{"tsc colon timestamp", []string{"a:12:34:56 - error TS2322: Type 'x'"}},
		{"go compile url", []string{"http://example.com/x.go:1:2: nope"}},
		{"go compile timestamp", []string{"T:12:34.go:1:2: nope"}},
		{"pytest timestamp", []string{"FAILED a:12:34::test_bar"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if errs := ParseBuildErrors(c.lines); len(errs) != 0 {
				t.Fatalf("near-miss parsed as a build error: %+v", errs)
			}
		})
	}

	// A header without a valid drive location still emits, but with no file.
	for _, lines := range [][]string{
		{"error[E0308]: mismatched types", "  --> x:12:34:56"},
		{"✘ [ERROR] bad", "    a:12:34:56:"},
		{"  ● suite › works", "      at fn (a:12:34:56)"},
		{"  ● suite › works", "      at fn (http://localhost/x.js:1:2)"},
	} {
		errs := ParseBuildErrors(lines)
		if len(errs) != 1 || errs[0].File != "" {
			t.Errorf("near-miss location %q must not yield a file: %+v", lines[1], errs)
		}
	}
	// An eslint row under a URL or clock-time header is not attributed.
	for _, hdr := range []string{"http://localhost/app.ts", "12:34"} {
		errs := ParseBuildErrors([]string{hdr, "  4:7  error  Unexpected console statement  no-console"})
		if len(errs) != 0 {
			t.Errorf("eslint header %q must not be accepted: %+v", hdr, errs)
		}
	}
}
