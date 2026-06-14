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
