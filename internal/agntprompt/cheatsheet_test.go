package agntprompt

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/tools"
)

// TestPromotedFunctionsExist guards against drift between the curated
// PromotedFunctions list and the generated DevToolAPIFunctions catalog.
// If a JSDoc rename removes a promoted helper, this fails loudly.
func TestPromotedFunctionsExist(t *testing.T) {
	index := map[string]bool{}
	for _, fn := range tools.DevToolAPIFunctions {
		index[fn.Name] = true
	}
	for _, name := range PromotedFunctions {
		if !index[name] {
			t.Errorf("PromotedFunctions references %q which is not in DevToolAPIFunctions (catalog drift: rename or remove from PromotedFunctions)", name)
		}
	}
}

func TestBuildCheatSheet_ContainsHeader(t *testing.T) {
	out := BuildCheatSheet(tools.DevToolAPIFunctions)
	if !strings.Contains(out, "## Browser debugging helpers") {
		t.Errorf("cheat sheet missing header; got:\n%s", out)
	}
	if !strings.Contains(out, "Prefer __devtool.* helpers over raw document.*") {
		t.Errorf("cheat sheet missing rules block; got:\n%s", out)
	}
	if !strings.Contains(out, "proxy exec search:") {
		t.Errorf("cheat sheet missing search hint; got:\n%s", out)
	}
	if !strings.Contains(out, "proxy exec describe:") {
		t.Errorf("cheat sheet missing describe hint; got:\n%s", out)
	}
}

func TestBuildCheatSheet_ListsPromotedHelpers(t *testing.T) {
	out := BuildCheatSheet(tools.DevToolAPIFunctions)
	// Spot-check one helper from each expected category cluster.
	wants := []string{
		"log(",                 // logging
		"inspect(",             // inspection
		"findOverflows(",       // layout
		"auditAccessibility(",  // accessibility
		"auditPageQuality(",    // audit
		"getLastClickContext(", // interactions
		"highlightRecent(",     // mutations
	}
	for _, w := range wants {
		if !strings.Contains(out, w) {
			t.Errorf("cheat sheet missing helper containing %q; got:\n%s", w, out)
		}
	}
}

func TestBuildCheatSheet_UnderLineBudget(t *testing.T) {
	out := BuildCheatSheet(tools.DevToolAPIFunctions)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	// Budget is ~50 lines per the task spec; assert <55 as a safety
	// margin so incremental helper additions fail loudly before they
	// become a wall of text in every agent's context.
	if len(lines) > 55 {
		t.Errorf("cheat sheet exceeds 55-line budget (got %d lines):\n%s", len(lines), out)
	}
	if len(lines) < 15 {
		t.Errorf("cheat sheet suspiciously short (got %d lines):\n%s", len(lines), out)
	}
}

func TestBuildCheatSheet_GroupsByCategory(t *testing.T) {
	out := BuildCheatSheet(tools.DevToolAPIFunctions)
	// Every category header should appear exactly once — grouping bug
	// would emit the same category twice in insertion order.
	for _, cat := range PromotedCategories(tools.DevToolAPIFunctions) {
		header := "### " + cat + "\n"
		if n := strings.Count(out, header); n != 1 {
			t.Errorf("expected category header %q to appear exactly once, got %d; output:\n%s", header, n, out)
		}
	}
}

func TestBuildCheatSheet_SkipsUnknownNames(t *testing.T) {
	// Simulate a stale promoted name by calling with a reduced catalog.
	reduced := []tools.APIFunction{
		{Name: "log", Category: "logging", Signature: "log(msg)", Description: "Log a message"},
		{Name: "inspect", Category: "inspection", Signature: "inspect(sel)", Description: "Inspect an element"},
	}
	out := BuildCheatSheet(reduced)
	if !strings.Contains(out, "log(msg)") {
		t.Errorf("expected log() line in reduced output:\n%s", out)
	}
	if !strings.Contains(out, "inspect(sel)") {
		t.Errorf("expected inspect() line in reduced output:\n%s", out)
	}
	// Unknown names should silently skip, not blow up.
	if strings.Contains(out, "auditAccessibility") {
		t.Errorf("expected auditAccessibility to be skipped in reduced output:\n%s", out)
	}
}
