package tools

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestAPIDocsNoDrift fails loudly when internal/tools/apidocs_gen.go is out
// of date relative to the JSDoc blocks in internal/proxy/scripts/*.js. If
// this test fails, run `make generate` (or `go generate ./internal/tools/...`)
// and commit the regenerated file.
//
// The test invokes scripts/gen-apidocs.go with -check, which re-parses the
// JSDoc source of truth and compares against the committed file.
func TestAPIDocsNoDrift(t *testing.T) {
	repoRoot := findRepoRoot(t)

	cmd := exec.Command("go", "run", "./scripts/gen-apidocs.go",
		"-scripts", "internal/proxy/scripts",
		"-out", "internal/tools/apidocs_gen.go",
		"-check",
	)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		t.Fatalf("apidocs drift detected — the JSDoc in internal/proxy/scripts/*.js\n"+
			"no longer matches internal/tools/apidocs_gen.go.\n\n"+
			"Fix: run `make generate` and commit the regenerated file.\n\n"+
			"gen-apidocs stderr:\n%s\n\ngen-apidocs stdout:\n%s\n\nrun error: %v",
			stderr.String(), stdout.String(), err)
	}
}

// TestAPIDocsGeneratorHasFunctions guards against a silent-empty catalog:
// if the generator runs but finds zero @devtool blocks, that's a regression
// (either a scanner bug or an accidental JSDoc wipeout).
func TestAPIDocsGeneratorHasFunctions(t *testing.T) {
	if len(DevToolAPIFunctions) == 0 {
		t.Fatalf("DevToolAPIFunctions is empty — gen-apidocs did not emit any functions")
	}
	// Sanity check a well-known entry so a broken parser surfaces early.
	found := false
	for _, fn := range DevToolAPIFunctions {
		if fn.Name == "log" && fn.Category == "logging" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected `log` function in logging category, not present in %d entries", len(DevToolAPIFunctions))
	}
}

// findRepoRoot walks up from this test file's directory until it finds a
// go.mod. Returns that directory.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found walking up from %s", filepath.Dir(thisFile))
		}
		dir = parent
	}
}

// Ensure gen-apidocs lives where we expect — a quick smoke check for CI
// environments that may have lost the generator.
func TestGeneratorExists(t *testing.T) {
	root := findRepoRoot(t)
	path := filepath.Join(root, "scripts", "gen-apidocs.go")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("gen-apidocs.go missing at %s: %v", path, err)
	}
	if !strings.Contains(string(data), "DevToolAPIFunctions") {
		t.Fatalf("gen-apidocs.go at %s does not reference DevToolAPIFunctions", path)
	}
}
