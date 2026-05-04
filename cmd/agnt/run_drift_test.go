package main

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

// Per-file LOC budgets for the per-platform run entrypoints. These are
// intentionally tight: every line over budget is a candidate for
// extraction into pty_common.go (genuinely-shared) or the helper files
// (signals_*.go, resolve_*.go, browser_helper_windows.go,
// exec_*.go). See task wkDDSC2FgNc6.
//
// Touching this constant must be justified in the commit message — most
// drift comes from copy-paste landings, and the budget is the trigger
// for code review to push back. Iter-9 baseline was run.go=164,
// run_windows.go=182; we set the budget at 200 (the original task
// acceptance bar) so adding ~20 lines for a genuinely platform-specific
// reason (e.g. a new ConPTY quirk) is fine, but doubling either file
// would fail this test and force the contributor to either extract or
// argue for a budget bump.
const (
	maxRunGoLines        = 200
	maxRunWindowsGoLines = 200
)

// TestRunFilesUnderBudget enforces the per-platform line budget. This
// test is build-tag-neutral on purpose — both files exist on every
// platform's source tree even if only one compiles. Reading them as
// plain files keeps the guard active on every CI run regardless of GOOS.
func TestRunFilesUnderBudget(t *testing.T) {
	files := []struct {
		name   string
		budget int
	}{
		{"run.go", maxRunGoLines},
		{"run_windows.go", maxRunWindowsGoLines},
	}
	for _, f := range files {
		t.Run(f.name, func(t *testing.T) {
			b, err := os.ReadFile(f.name)
			if err != nil {
				t.Fatalf("read %s: %v", f.name, err)
			}
			lines := strings.Count(string(b), "\n")
			if lines > f.budget {
				t.Errorf("%s has %d lines (budget %d) — extract shared code into pty_common.go or platform helpers (signals_*.go, resolve_*.go, browser_helper_windows.go); see task wkDDSC2FgNc6", f.name, lines, f.budget)
			}
		})
	}
}

// TestRunPlatformPTYParity checks the two per-platform entrypoints
// expose the same top-level symbol (`runPlatformPTY`) so the dispatcher
// in pty_common.go's runCommand has a callable target on every GOOS.
// Drift guard: if one platform deletes the entrypoint or renames it
// without updating the other, this test fails before runtime catches it.
func TestRunPlatformPTYParity(t *testing.T) {
	want := map[string]bool{"run.go": false, "run_windows.go": false}
	fset := token.NewFileSet()
	for name := range want {
		path, err := filepath.Abs(name)
		if err != nil {
			t.Fatalf("abs %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil {
				continue
			}
			if fn.Name.Name == "runPlatformPTY" {
				want[name] = true
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("%s does not declare func runPlatformPTY — the dispatcher in pty_common.go's runCommand requires both files to declare this symbol", name)
		}
	}
}

// TestRunPlatformPTYSignatureMatch checks that runPlatformPTY in both
// files takes the same parameter list. This is a drift guard for the
// dispatcher contract — if Unix grows a parameter Windows doesn't, the
// two will silently call into incompatible implementations under their
// build tags. Reads the files directly (build-tag-neutral) so a single
// test pass covers both platforms.
func TestRunPlatformPTYSignatureMatch(t *testing.T) {
	sigs := map[string]string{}
	fset := token.NewFileSet()
	for _, name := range []string{"run.go", "run_windows.go"} {
		path, err := filepath.Abs(name)
		if err != nil {
			t.Fatalf("abs %s: %v", name, err)
		}
		f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, decl := range f.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || fn.Name.Name != "runPlatformPTY" {
				continue
			}
			sigs[name] = renderFuncSignature(fn)
		}
	}
	if len(sigs) < 2 {
		t.Skipf("expected runPlatformPTY in both files, got: %v (sister test TestRunPlatformPTYParity will fail loudly)", keysOf(sigs))
		return
	}
	if sigs["run.go"] != sigs["run_windows.go"] {
		t.Errorf("runPlatformPTY signature drift:\n  run.go:         %s\n  run_windows.go: %s", sigs["run.go"], sigs["run_windows.go"])
	}
}

// renderFuncSignature builds a stable string of a func decl's parameter
// and result types. Names are dropped (we want to compare types, not
// bikeshed parameter names like ctx vs context). Type expressions are
// rendered via their underlying token positions so we don't pull in
// go/printer.
func renderFuncSignature(fn *ast.FuncDecl) string {
	var sb strings.Builder
	sb.WriteString("(")
	if fn.Type.Params != nil {
		for i, field := range fn.Type.Params.List {
			if i > 0 {
				sb.WriteString(", ")
			}
			// Repeat the type once per name (Go grouping like
			// `a, b string` represents two parameters of type string).
			n := len(field.Names)
			if n == 0 {
				n = 1
			}
			for j := 0; j < n; j++ {
				if j > 0 {
					sb.WriteString(", ")
				}
				sb.WriteString(exprString(field.Type))
			}
		}
	}
	sb.WriteString(")")
	if fn.Type.Results != nil && len(fn.Type.Results.List) > 0 {
		sb.WriteString(" ")
		if len(fn.Type.Results.List) > 1 {
			sb.WriteString("(")
		}
		for i, field := range fn.Type.Results.List {
			if i > 0 {
				sb.WriteString(", ")
			}
			sb.WriteString(exprString(field.Type))
		}
		if len(fn.Type.Results.List) > 1 {
			sb.WriteString(")")
		}
	}
	return sb.String()
}

// exprString stringifies the type expressions we actually use in the
// runPlatformPTY signature. The list is intentionally narrow — adding
// new constructors here is fine when the signature genuinely needs to
// grow, but the goal is to keep the diff visible in code review.
func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	case *ast.StarExpr:
		return "*" + exprString(v.X)
	case *ast.ArrayType:
		return "[]" + exprString(v.Elt)
	case *ast.Ellipsis:
		return "..." + exprString(v.Elt)
	case *ast.MapType:
		return "map[" + exprString(v.Key) + "]" + exprString(v.Value)
	case *ast.ChanType:
		return "chan " + exprString(v.Value)
	case *ast.InterfaceType:
		return "interface{}"
	default:
		return "<unknown>"
	}
}

func keysOf(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// init touches runtime so the import is real (we may add runtime checks
// later — currently the test is platform-neutral by design).
var _ = runtime.GOOS
