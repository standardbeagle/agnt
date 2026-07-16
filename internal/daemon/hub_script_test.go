package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/config"
)

// TestSCRIPTLIST_IncludesExplicitProxies verifies that SCRIPT LIST
// returns proxy-kind entries for explicit proxies started via
// handleExplicitStart. This is what drives the overlay status bar's
// indicator count and the admin screen's script list — without it,
// explicit proxies are invisible to both surfaces.
//
// Tests the merge contract through buildScriptListSummaries, which
// hubHandleScriptList delegates to. Exercising the full hub command
// path would require a real socket connection; the merge logic is
// the testable unit.
func TestSCRIPTLIST_IncludesExplicitProxies(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// Register a process-kind script first.
	if _, err := daemon.scriptRegistry.Register(
		"api",
		tmpDir,
		scriptConfigToEntry(&config.ScriptConfig{Run: "true"}),
	); err != nil {
		t.Fatalf("scriptRegistry.Register: %v", err)
	}

	// Start an explicit proxy — this should add a proxy-kind admin entry.
	proxyID := makeProcessID(tmpDir, "dev-proxy")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65020"},
		Path:    tmpDir,
	})

	summaries := daemon.buildScriptListSummaries(tmpDir)
	if len(summaries) != 2 {
		t.Fatalf("expected 2 SCRIPT LIST rows (1 process + 1 proxy), got %d: %+v", len(summaries), summaries)
	}

	// Classify rows by kind.
	var sawProcess, sawProxy bool
	for _, s := range summaries {
		switch s["kind"] {
		case string(ScriptKindProcess):
			sawProcess = true
			if s["name"] != "api" {
				t.Errorf("process row name: got %v, want %q", s["name"], "api")
			}
		case string(ScriptKindProxy):
			sawProxy = true
			if s["name"] != "dev-proxy" {
				t.Errorf("proxy row name: got %v, want %q", s["name"], "dev-proxy")
			}
			if s["process_id"] != proxyID {
				t.Errorf("proxy row process_id: got %v, want %q", s["process_id"], proxyID)
			}
		default:
			t.Errorf("unexpected row kind: %v in %+v", s["kind"], s)
		}
	}
	if !sawProcess {
		t.Errorf("expected a process-kind row for script %q", "api")
	}
	if !sawProxy {
		t.Errorf("expected a proxy-kind row for proxy %q", "dev-proxy")
	}
}

// TestSCRIPTLIST_EmptyProject_NoPanic guards against nil-dereference
// on a project with no scripts and no proxies. Covers the
// empty-config boot path (fresh `.agnt.kdl` with no autostart items)
// so the admin surface renders "no scripts" cleanly rather than
// crashing.
func TestSCRIPTLIST_EmptyProject_NoPanic(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	summaries := daemon.buildScriptListSummaries(tmpDir)
	if len(summaries) != 0 {
		t.Errorf("expected 0 rows for empty project, got %d: %+v", len(summaries), summaries)
	}
}

// TestCleanupSessionResources_ClearsExplicitProxyEntry pins the T5
// cleanup contract for proxy-kind admin entries: when the last session
// for a project disconnects, every proxyScriptEntry registered under
// that project must be removed from the daemon-level proxyEntryStore.
// Otherwise the overlay status bar on the next session renders a
// phantom indicator for a proxy that no longer exists — the same
// stale-indicator class of bug the script-registry cleanup in
// daemon_test.go:TestCleanupSessionResources_ClearsScriptRegistry
// already guards against, but for the proxy-kind surface added in T2.
func TestCleanupSessionResources_ClearsExplicitProxyEntry(t *testing.T) {
	t.Parallel()
	daemon, tmpDir := newFallbackTestDaemon(t)

	// Register a session owning this project so doCleanupExact takes the
	// "last session" branch.
	session := &Session{
		Code:        "t5-cleanup",
		ProjectPath: tmpDir,
		StartedAt:   time.Now(),
		LastSeen:    time.Now(),
		Status:      SessionStatusActive,
	}
	if err := daemon.sessionRegistry.Register(session); err != nil {
		t.Fatalf("sessionRegistry.Register: %v", err)
	}

	proxyID := makeProcessID(tmpDir, "leaking")
	daemon.handleExplicitStart(ProxyEvent{
		Type:    ExplicitStart,
		ProxyID: proxyID,
		Config:  &config.ProxyConfig{URL: "http://127.0.0.1:65030"},
		Path:    tmpDir,
	})

	if got := len(daemon.proxyEntries.List(tmpDir)); got != 1 {
		t.Fatalf("precondition: expected 1 proxy-kind entry, got %d", got)
	}

	// Run cleanup. T5 wires proxyEntries.Remove into this path — the
	// admin entry must be gone after the last session disconnects.
	daemon.CleanupSessionResources(session.Code)

	if remaining := len(daemon.proxyEntries.List(tmpDir)); remaining != 0 {
		t.Errorf("proxy-kind entries after cleanup: got %d, want 0", remaining)
	}
}
