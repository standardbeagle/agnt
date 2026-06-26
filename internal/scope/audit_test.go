package scope_test

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// allowedUnscoped is the reviewed allowlist of every production call site that
// constructs an Unscoped (cross-project) scope. Unscoped is the deliberate
// escape hatch from session-scoped-by-default delivery; each entry here is a
// place we have decided global access is correct. Keyed by "<relpath>:<reason>".
//
// Adding a new Unscoped call to non-test code fails TestUnscopedCallSites until
// the call site is added here with a justification in code review. This keeps
// global access rare, visible, and intentional. Test files are exempt — tests
// may scope however they need.
var allowedUnscoped = map[string]string{
	"internal/daemon/doctor.go:doctor: health check across all projects":                                              "doctor run with no project filter audits every proxy",
	"internal/daemon/daemon_port_kill_report.go:port-kill attribution: find proxies whose backend was just reclaimed": "port conflicts are inherently cross-project; attribution must scan every project's proxies to notify the victim",
	"internal/daemon/hub_helpers.go:explicit global flag on hub query":                                                "user passed global:true on a gated query (documented C6 override)",
	"internal/daemon/hub_lifecycle.go:STOP-ALL: count every proxy":                                                    "STOP-ALL is an admin-wide lifecycle command",
	"internal/daemon/hub_lifecycle.go:RESTART-ALL: restart every proxy":                                               "RESTART-ALL is an admin-wide lifecycle command",
	"internal/daemon/event_hub.go:alert toast fan-out to all browser overlays":                                        "alert toasts are browser-facing, not agent-bound delivery",
}

var unscopedCallRe = regexp.MustCompile(`scope\.Unscoped\(\s*"([^"]*)"`)

// TestUnscopedCallSites pins the exact set of production Unscoped() call sites.
// It walks the repository, collects every scope.Unscoped("…") in non-test Go
// files, and diffs against allowedUnscoped. New, removed, or reason-changed
// global access trips the test.
func TestUnscopedCallSites(t *testing.T) {
	root := repoRoot(t)

	found := map[string]string{} // "relpath:reason" -> relpath:line for diagnostics

	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			base := d.Name()
			if base == "vendor" || base == ".git" || strings.HasPrefix(base, ".") && base != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		// The scope package itself defines Unscoped; skip its definition file.
		if rel == "internal/scope/scope.go" {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(data), "\n") {
			for _, m := range unscopedCallRe.FindAllStringSubmatch(line, -1) {
				reason := m[1]
				key := rel + ":" + reason
				found[key] = rel + ":" + itoa(i+1)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk failed: %v", err)
	}

	// New / unexpected call sites.
	var added []string
	for key, loc := range found {
		if _, ok := allowedUnscoped[key]; !ok {
			added = append(added, loc+"  ("+key+")")
		}
	}
	// Removed call sites (allowlist entries no longer present).
	var removed []string
	for key := range allowedUnscoped {
		if _, ok := found[key]; !ok {
			removed = append(removed, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)

	if len(added) > 0 {
		t.Errorf("new Unscoped() call site(s) not in allowlist — global access must be reviewed and added to allowedUnscoped:\n  %s",
			strings.Join(added, "\n  "))
	}
	if len(removed) > 0 {
		t.Errorf("allowlisted Unscoped() call site(s) no longer found — remove stale entries from allowedUnscoped:\n  %s",
			strings.Join(removed, "\n  "))
	}
}

// repoRoot walks up from the test's working directory until it finds go.mod.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found walking up from test dir")
		}
		dir = parent
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
