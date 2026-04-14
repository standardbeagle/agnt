// Package proxy — readiness gate for script-dependency waiting.
//
// A proxy linked to a front-end script (e.g. Vite) becomes visible the
// moment its linked script emits a detectable URL. In multi-service
// projects the front end often depends on a back end that has not yet
// bound its port. Without a gate, requests forwarded through the proxy
// reach the front end, which proxies API calls to the not-yet-listening
// back end, producing `ECONNREFUSED` errors in `get_errors` for the full
// duration of the startup race.
//
// The readiness gate holds the proxy in a "waiting for dependencies"
// state: it is running and bound (visible to `proxy list`), but every
// incoming request receives a 503 with the `agnt_proxy_not_ready`
// sentinel body. `get_errors` filters the sentinel so it never reaches
// the AI agent. When every dependency in the wait-list signals ready,
// the gate opens atomically and normal forwarding resumes.
//
// The gate is a readiness gate, not a build gate. Per the
// Running With Errors rule in .claude/rules/daemon-lifecycle.md, a
// process that is bound and producing output (including compile
// errors) is still "running" from the daemon's perspective. The daemon
// signals dependency readiness via the ReadySignaler as soon as the
// dependency's port binds, regardless of whether the process is in a
// clean or errored state.

package proxy

import (
	"encoding/json"
	"net/http"
	"sort"
	"sync"
	"sync/atomic"
)

// ReadinessSentinel is the stable error code in the 503 JSON body
// emitted by the readiness gate. `get_errors` matches this string to
// drop gate-generated responses from its error feed.
const ReadinessSentinel = "agnt_proxy_not_ready"

// ReadinessRetryAfterMs is the retry interval suggested to clients in
// the readiness 503 body. The browser will retry automatically; the
// value is informational only.
const ReadinessRetryAfterMs = 500

// ReadinessBody is the JSON payload returned when the gate is closed.
// Kept as a struct so the shape is self-documenting and so tests can
// unmarshal it without resorting to map[string]interface{}.
type ReadinessBody struct {
	Error        string   `json:"error"`
	Message      string   `json:"message"`
	Pending      []string `json:"pending"`
	RetryAfterMs int      `json:"retry_after_ms"`
}

// readinessGate tracks the set of dependencies a proxy is waiting on.
// The hot path (checking whether the gate is open) is a single atomic
// load. The slow path (SetDependencies / MarkDependencyReady) takes a
// mutex so the pending-list snapshot stays consistent.
//
// State machine:
//
//	open (no deps, or all deps marked ready)  → forward requests
//	closed (len(pending) > 0)                 → return 503 sentinel
//
// Once a dependency is removed from `pending` it is never re-added.
// Flipping the gate from closed to open is a one-way transition for
// the lifetime of the ProxyServer; if the proxy is re-created after a
// crash, a fresh gate is constructed from config.
type readinessGate struct {
	mu      sync.Mutex
	pending map[string]struct{}

	// open is an atomic snapshot of whether the gate allows forwarding.
	// The hot path reads this lock-free; the mutex is only taken on the
	// slow paths (dependency transitions and status queries).
	open atomic.Bool

	// pendingSnapshot holds a sorted copy of the pending dependency
	// names for fast lock-free reads by the 503 handler and status
	// reporting. Replaced atomically whenever `pending` changes.
	pendingSnapshot atomic.Pointer[[]string]
}

// newReadinessGate returns a gate that is open by default. Callers
// pass an empty slice (or nil) when the proxy has no `wait-for`
// configured — this is the common case and must be zero-cost on the
// forwarding hot path.
func newReadinessGate() *readinessGate {
	g := &readinessGate{}
	g.open.Store(true)
	empty := []string{}
	g.pendingSnapshot.Store(&empty)
	return g
}

// SetDependencies replaces the pending set with the supplied list.
// Passing an empty or nil slice opens the gate immediately. Duplicate
// names in the input are coalesced. The call is idempotent: setting
// the same dependencies twice leaves the gate in the same state.
//
// The typical caller is the daemon's proxy-creation path, which
// resolves the proxy's `wait-for` config into process IDs before
// calling this method.
func (g *readinessGate) SetDependencies(deps []string) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if len(deps) == 0 {
		g.pending = nil
		g.open.Store(true)
		empty := []string{}
		g.pendingSnapshot.Store(&empty)
		return
	}

	g.pending = make(map[string]struct{}, len(deps))
	for _, d := range deps {
		if d == "" {
			continue
		}
		g.pending[d] = struct{}{}
	}

	if len(g.pending) == 0 {
		g.open.Store(true)
		empty := []string{}
		g.pendingSnapshot.Store(&empty)
		return
	}

	g.open.Store(false)
	g.refreshSnapshotLocked()
}

// MarkDependencyReady removes dep from the pending set. When the
// pending set becomes empty, the gate flips to open. Returns true if
// the gate transitioned from closed to open as a result of this call,
// so the caller can emit exactly one `proxy_ready` event per proxy.
//
// Unknown dependency names are ignored — the caller may mark deps
// that were never registered (e.g. if SetDependencies was called with
// an empty list) without triggering an error.
func (g *readinessGate) MarkDependencyReady(dep string) (openedNow bool) {
	g.mu.Lock()
	defer g.mu.Unlock()

	if _, ok := g.pending[dep]; !ok {
		return false
	}
	delete(g.pending, dep)
	if len(g.pending) == 0 {
		// Transition closed → open exactly once.
		wasOpen := g.open.Swap(true)
		empty := []string{}
		g.pendingSnapshot.Store(&empty)
		return !wasOpen
	}
	g.refreshSnapshotLocked()
	return false
}

// IsReady returns true when the gate is open. This is the hot-path
// check consulted on every incoming request and is lock-free.
func (g *readinessGate) IsReady() bool {
	return g.open.Load()
}

// PendingDependencies returns a sorted snapshot of the dependencies
// still being waited on. Returns an empty slice when the gate is open.
// The returned slice is a fresh copy; callers may modify it freely.
func (g *readinessGate) PendingDependencies() []string {
	snap := g.pendingSnapshot.Load()
	if snap == nil {
		return nil
	}
	// Defensive copy — the snapshot is shared across callers.
	out := make([]string, len(*snap))
	copy(out, *snap)
	return out
}

// refreshSnapshotLocked rebuilds the sorted pending list under the
// mutex. Called from SetDependencies / MarkDependencyReady whenever
// the pending map changes but the gate remains closed.
func (g *readinessGate) refreshSnapshotLocked() {
	snap := make([]string, 0, len(g.pending))
	for d := range g.pending {
		snap = append(snap, d)
	}
	sort.Strings(snap)
	g.pendingSnapshot.Store(&snap)
}

// writeReadinessNotReady writes the 503 response body for a gated
// proxy. Split into a helper so handleProxy and tests can share the
// exact same serialization.
func writeReadinessNotReady(w http.ResponseWriter, pending []string) {
	body := ReadinessBody{
		Error:        ReadinessSentinel,
		Message:      formatReadinessMessage(pending),
		Pending:      pending,
		RetryAfterMs: ReadinessRetryAfterMs,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		// Marshalling a plain struct cannot fail in practice; if it
		// somehow does, fall back to a bare sentinel so `get_errors`
		// can still filter the response.
		payload = []byte(`{"error":"` + ReadinessSentinel + `"}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "1")
	w.Header().Set("X-Agnt-Readiness", "waiting-for-dependencies")
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write(payload)
}

// formatReadinessMessage composes the human-readable message embedded
// in the 503 body. The format is stable for log-scraping consumers.
func formatReadinessMessage(pending []string) string {
	if len(pending) == 0 {
		return "Proxy is ready"
	}
	return "Waiting on: " + joinSorted(pending)
}

// joinSorted joins sorted dependency names with comma separators. The
// input is assumed to be pre-sorted by PendingDependencies; we re-sort
// defensively in case a caller passes an unsorted slice.
func joinSorted(names []string) string {
	if len(names) == 0 {
		return ""
	}
	sorted := make([]string, len(names))
	copy(sorted, names)
	sort.Strings(sorted)
	out := sorted[0]
	for _, n := range sorted[1:] {
		out += ", " + n
	}
	return out
}
