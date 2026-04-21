package daemon

import (
	"strings"
	"sync"
	"time"

	"github.com/standardbeagle/go-cli-server/script"
)

// ScriptKind classifies admin-surface entries returned by SCRIPT LIST.
//
// The script registry in go-cli-server is strictly process-kind (backed by
// a real child process), but the overlay status bar and the `SCRIPT LIST`
// admin screen are also the surface for explicit proxies — proxies started
// without a linked script (MCP tool path or `autostart true` on a
// standalone proxy config). To keep the admin surface unified without
// modifying the vendored script package, proxy-kind entries live in a
// parallel daemon-level map and are merged into SCRIPT LIST responses.
//
// ScriptKindProcess is the default / implicit kind for entries returned
// directly from scriptRegistry.List (the vendored registry has no Kind
// field; the merge path tags every script.Entry as ScriptKindProcess).
type ScriptKind string

const (
	ScriptKindProcess ScriptKind = "process"
	ScriptKindProxy   ScriptKind = "proxy"
)

// proxyScriptEntry is the daemon-owned admin-surface record for an
// explicit proxy. It is intentionally minimal — enough to render a
// status-bar indicator and a SCRIPT LIST row, no more. The proxy's
// real state lives in proxym (the ProxyManager); this entry is a
// read-only projection for the admin surface.
type proxyScriptEntry struct {
	name        string
	projectPath string
	proxyID     string
	listenAddr  string
	createdAt   time.Time
}

// Name returns the display name used in SCRIPT LIST rows.
func (e *proxyScriptEntry) Name() string { return e.name }

// ProjectPath returns the project path this entry belongs to.
func (e *proxyScriptEntry) ProjectPath() string { return e.projectPath }

// ProxyID returns the underlying proxy ID in the ProxyManager.
func (e *proxyScriptEntry) ProxyID() string { return e.proxyID }

// ListenAddr returns the proxy's bind address (e.g. "127.0.0.1:19191").
// Empty string if the proxy hadn't completed binding at entry time.
func (e *proxyScriptEntry) ListenAddr() string { return e.listenAddr }

// CreatedAt returns when the entry was registered.
func (e *proxyScriptEntry) CreatedAt() time.Time { return e.createdAt }

// proxyEntryStore is a lock-free-ish keyed store for proxy-kind
// admin entries. Shape mirrors script.Registry: keyed by
// (projectPath, name), List filters by projectPath prefix.
type proxyEntryStore struct {
	entries sync.Map // map[string]*proxyScriptEntry, key = projectPath + "\x00" + name
}

func newProxyEntryStore() *proxyEntryStore {
	return &proxyEntryStore{}
}

func proxyEntryKey(projectPath, name string) string {
	return projectPath + "\x00" + name
}

// Register adds a proxy entry if no entry exists for (projectPath, name).
// Idempotent: returns (existing, true) when a prior entry is present so
// callers can detect the "URL-detection-then-fallback" ordering case
// without accidentally overwriting fields set by the first path.
func (s *proxyEntryStore) Register(entry *proxyScriptEntry) (*proxyScriptEntry, bool) {
	if entry == nil || entry.name == "" || entry.projectPath == "" {
		return nil, false
	}
	key := proxyEntryKey(entry.projectPath, entry.name)
	if existing, loaded := s.entries.LoadOrStore(key, entry); loaded {
		return existing.(*proxyScriptEntry), true
	}
	return entry, false
}

// Get returns the entry for (projectPath, name), or (nil, false).
func (s *proxyEntryStore) Get(projectPath, name string) (*proxyScriptEntry, bool) {
	val, ok := s.entries.Load(proxyEntryKey(projectPath, name))
	if !ok {
		return nil, false
	}
	return val.(*proxyScriptEntry), true
}

// Remove deletes the entry for (projectPath, name). Returns true if the
// entry was present.
func (s *proxyEntryStore) Remove(projectPath, name string) bool {
	_, existed := s.entries.LoadAndDelete(proxyEntryKey(projectPath, name))
	return existed
}

// List returns all entries for the given project path, in non-deterministic
// order (sync.Map iteration).
func (s *proxyEntryStore) List(projectPath string) []*proxyScriptEntry {
	prefix := projectPath + "\x00"
	var result []*proxyScriptEntry
	s.entries.Range(func(key, value interface{}) bool {
		k := key.(string)
		if strings.HasPrefix(k, prefix) {
			result = append(result, value.(*proxyScriptEntry))
		}
		return true
	})
	return result
}

// registerExplicitProxyEntry is the handleExplicitStart integration
// point. Derives the display name from the proxy ID (stripping the
// project-hash prefix added by makeProcessID) and registers a
// proxy-kind entry in d.proxyEntries.
//
// Idempotency rules:
//
//  1. If a script.Entry already exists under the derived name (script-linked
//     proxy case — autostartScript registered a process-kind entry first,
//     and URL detection later created this proxy), skip. The script
//     registry entry owns the admin row; a parallel proxy entry would
//     duplicate it.
//  2. If a proxyScriptEntry already exists under (projectPath, name),
//     skip. The first registration wins; later calls (e.g. when the MCP
//     tool path is refactored to flow through handleExplicitStart) must
//     not overwrite the entry.
//
// Returns true if a new entry was registered, false if skipped.
func (d *Daemon) registerExplicitProxyEntry(projectPath, proxyID, listenAddr string) bool {
	if d.proxyEntries == nil {
		return false
	}
	if projectPath == "" || proxyID == "" {
		return false
	}
	name := stripProcessPrefix(proxyID)
	if name == "" {
		return false
	}

	// Rule 1: script-linked proxy case — defer to the existing script.Entry.
	if d.scriptRegistry != nil {
		if _, ok := d.scriptRegistry.Get(name, projectPath); ok {
			return false
		}
	}

	// Rule 2: proxy entry already exists — first registration wins.
	if _, existed := d.proxyEntries.Register(&proxyScriptEntry{
		name:        name,
		projectPath: projectPath,
		proxyID:     proxyID,
		listenAddr:  listenAddr,
		createdAt:   time.Now(),
	}); existed {
		return false
	}
	return true
}

// proxyEntryToSummary projects a proxyScriptEntry into the
// SCRIPT LIST JSON summary shape, mirroring scriptEntryToSummary in
// hub_script.go. Kept shallow on purpose: proxy-kind rows don't have
// start/fail counts, output history, or state machine semantics —
// just a kind tag, a display name, and a listen address link-out.
// Omitting fields (rather than zeroing them) keeps the JSON tight
// and lets the admin surface tell process-kind from proxy-kind rows
// structurally rather than by convention.
func proxyEntryToSummary(entry *proxyScriptEntry) map[string]interface{} {
	if entry == nil {
		return nil
	}
	summary := map[string]interface{}{
		"name":       entry.name,
		"kind":       string(ScriptKindProxy),
		"state":      script.StateRunning.String(),
		"process_id": entry.proxyID,
	}
	if entry.listenAddr != "" {
		summary["listen_addr"] = entry.listenAddr
	}
	if !entry.createdAt.IsZero() {
		summary["last_started"] = entry.createdAt.Format(time.RFC3339)
	}
	return summary
}
