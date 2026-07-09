package tools

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildSnapshot_BasicAssembly verifies the unified snapshot is
// assembled correctly from raw daemon-IPC maps. This is the unit-level
// contract for the wire format documented in the task spec.
func TestBuildSnapshot_BasicAssembly(t *testing.T) {
	procEntries := []map[string]interface{}{
		{
			"id":         "web",
			"state":      "running",
			"runtime_ms": float64(720000), // 12m
			"command":    "next dev",
			"urls":       []interface{}{"http://localhost:3000"},
		},
		{
			"id":         "api",
			"state":      "running",
			"runtime_ms": float64(480000), // 8m
			"command":    "go run ./cmd/api",
			"urls":       []interface{}{"http://localhost:8080"},
		},
		{
			"id":         "db",
			"state":      "running",
			"runtime_ms": float64(720000),
			"command":    "postgres",
		},
	}
	proxyEntries := []map[string]interface{}{
		{
			"id":             "dev",
			"target_url":     "http://localhost:3000",
			"listen_addr":    ":12345",
			"running":        true,
			"total_requests": float64(42),
			"status":         "running",
		},
	}
	errors := []unifiedError{
		{
			Source:   "process:api",
			Severity: "error",
			Category: "COMPILE ERROR",
			Message:  "undefined: UserCache",
			Location: "src/handlers/user.go:88:12",
			Count:    1,
			LastSeen: time.Now(),
		},
		{
			Source:   "process:api",
			Severity: "error",
			Category: "COMPILE ERROR",
			Message:  "cannot use nil as type Cache",
			Location: "src/handlers/user.go:91:5",
			Count:    1,
			LastSeen: time.Now(),
		},
	}

	snap := buildSnapshot(procEntries, proxyEntries, errors)

	require.Len(t, snap.Processes, 3, "snapshot must include all three processes")
	require.Len(t, snap.Proxies, 1)
	require.Len(t, snap.Errors, 2)

	// Process state and uptime conversion.
	web := findProcess(snap.Processes, "web")
	require.NotNil(t, web)
	assert.Equal(t, "running", web.State)
	assert.Equal(t, int64(720000), web.UptimeMs)
	assert.Equal(t, []string{"http://localhost:3000"}, web.URLs)
	assert.Equal(t, 0, web.ErrorCount, "web has no errors")

	// Per-process error count attribution (acceptance criterion #2).
	api := findProcess(snap.Processes, "api")
	require.NotNil(t, api)
	assert.Equal(t, 2, api.ErrorCount, "api has two attributed errors")
	assert.NotEmpty(t, api.LastError, "api should surface a last_error")

	// db has no errors and no URL.
	db := findProcess(snap.Processes, "db")
	require.NotNil(t, db)
	assert.Equal(t, 0, db.ErrorCount)
	assert.Empty(t, db.URLs)

	// Proxy basic shape.
	assert.Equal(t, "dev", snap.Proxies[0].ID)
	assert.Equal(t, "http://localhost:3000", snap.Proxies[0].Target)
	assert.Equal(t, ":12345", snap.Proxies[0].ListenAddr)
	assert.Equal(t, int64(42), snap.Proxies[0].RequestCount)
	assert.True(t, snap.Proxies[0].Running)

	// Error rows carry parsed file/line.
	assert.Equal(t, "src/handlers/user.go", snap.Errors[0].File)
	assert.Equal(t, "88", snap.Errors[0].Line)
	assert.Equal(t, "api", snap.Errors[0].ProcessID)

	// URL flattening.
	require.Len(t, snap.URLs, 2)
	assert.Equal(t, "api", snap.URLs[0].ProcessID, "URLs sorted by process_id")
	assert.Equal(t, "web", snap.URLs[1].ProcessID)

	// Suggested next includes a proc output / extract suggestion when
	// any process has errors (acceptance criterion #3).
	require.NotEmpty(t, snap.SuggestedNext)
	foundOutputSuggestion := false
	for _, s := range snap.SuggestedNext {
		if strings.Contains(s, `proc {action:"output"`) && strings.Contains(s, `process_id:"api"`) {
			foundOutputSuggestion = true
		}
	}
	assert.True(t, foundOutputSuggestion,
		"suggested_next must include a proc output command for the process with errors; got: %v",
		snap.SuggestedNext)
}

// TestBuildSnapshot_AllHealthy_SuggestsWatch verifies the "all healthy"
// branch of the suggested-next logic.
func TestBuildSnapshot_AllHealthy_SuggestsWatch(t *testing.T) {
	procs := []map[string]interface{}{
		{"id": "web", "state": "running", "runtime_ms": float64(60000)},
	}
	proxies := []map[string]interface{}{
		{"id": "dev", "target_url": "http://localhost:3000", "listen_addr": ":10000", "running": true},
	}
	snap := buildSnapshot(procs, proxies, nil)
	require.Len(t, snap.SuggestedNext, 1, "all healthy should produce exactly one suggestion")
	assert.Contains(t, snap.SuggestedNext[0], `watch {target:"all"}`,
		"all-healthy branch should suggest watch")
}

// TestBuildSnapshot_FailedProcess_SuggestsRestart verifies that
// failed/stopped processes produce a restart suggestion.
func TestBuildSnapshot_FailedProcess_SuggestsRestart(t *testing.T) {
	procs := []map[string]interface{}{
		{"id": "broken", "state": "failed", "runtime_ms": float64(0)},
	}
	snap := buildSnapshot(procs, nil, nil)
	foundRestart := false
	for _, s := range snap.SuggestedNext {
		if strings.Contains(s, `proc {action:"restart"`) && strings.Contains(s, `process_id:"broken"`) {
			foundRestart = true
		}
	}
	assert.True(t, foundRestart, "failed process must trigger a restart suggestion; got: %v", snap.SuggestedNext)
}

// TestBuildSnapshot_NoProxyButWebProcess_SuggestsProxy verifies the
// "web process without a proxy" branch.
func TestBuildSnapshot_NoProxyButWebProcess_SuggestsProxy(t *testing.T) {
	procs := []map[string]interface{}{
		{
			"id":         "web",
			"state":      "running",
			"runtime_ms": float64(60000),
			"urls":       []interface{}{"http://localhost:3000"},
		},
	}
	snap := buildSnapshot(procs, nil, nil)
	foundProxyStart := false
	for _, s := range snap.SuggestedNext {
		if strings.Contains(s, `proxy {action:"start"`) && strings.Contains(s, "http://localhost:3000") {
			foundProxyStart = true
		}
	}
	assert.True(t, foundProxyStart,
		"web process with HTTP URL but no proxy must trigger a proxy start suggestion; got: %v",
		snap.SuggestedNext)
}

// TestBuildSnapshot_ProxyCoversWebProcess_NoStartSuggestion verifies
// that a running proxy targeting a process URL suppresses the
// "start a proxy" suggestion. Only uncovered URLs should trigger it.
func TestBuildSnapshot_ProxyCoversWebProcess_NoStartSuggestion(t *testing.T) {
	procs := []map[string]interface{}{
		{
			"id":         "web",
			"state":      "running",
			"runtime_ms": float64(60000),
			"urls":       []interface{}{"http://localhost:3000"},
		},
	}
	proxies := []map[string]interface{}{
		{
			"id":          "dev",
			"target_url":  "http://localhost:3000",
			"listen_addr": ":12345",
			"running":     true,
		},
	}
	snap := buildSnapshot(procs, proxies, nil)
	for _, s := range snap.SuggestedNext {
		assert.NotContains(t, s, `proxy {action:"start"`,
			"covered web URL must not trigger a proxy start suggestion; got: %v", snap.SuggestedNext)
	}
}

// TestBuildSnapshot_StoppedProxyDoesNotCover verifies that a stopped
// proxy doesn't count as "covering" the web process — the agent should
// see the failed-process suggestion (or could choose to restart the
// proxy via the proxy tool).
func TestBuildSnapshot_StoppedProxyDoesNotCover(t *testing.T) {
	procs := []map[string]interface{}{
		{
			"id":         "web",
			"state":      "running",
			"runtime_ms": float64(60000),
			"urls":       []interface{}{"http://localhost:3000"},
		},
	}
	proxies := []map[string]interface{}{
		{
			"id":          "dev",
			"target_url":  "http://localhost:3000",
			"listen_addr": ":12345",
			"running":     false, // stopped
		},
	}
	snap := buildSnapshot(procs, proxies, nil)
	foundProxyStart := false
	for _, s := range snap.SuggestedNext {
		if strings.Contains(s, `proxy {action:"start"`) {
			foundProxyStart = true
		}
	}
	assert.True(t, foundProxyStart,
		"stopped proxy should not count as covering the web URL; got: %v", snap.SuggestedNext)
}

// TestBuildSnapshot_ErrorsTakePriority verifies that errors are surfaced
// before the failed-process or proxy-start suggestions when both apply.
// This matches the documented order in the task spec.
func TestBuildSnapshot_ErrorsTakePriority(t *testing.T) {
	procs := []map[string]interface{}{
		{
			"id":         "api",
			"state":      "running",
			"runtime_ms": float64(30000),
		},
		{
			"id":         "broken",
			"state":      "failed",
			"runtime_ms": float64(0),
		},
	}
	errors := []unifiedError{
		{Source: "process:api", Severity: "error", Category: "ERR", Message: "boom", Count: 1, LastSeen: time.Now()},
	}
	snap := buildSnapshot(procs, nil, errors)
	require.NotEmpty(t, snap.SuggestedNext)
	// First suggestion should be the error-output one for api.
	assert.Contains(t, snap.SuggestedNext[0], `process_id:"api"`,
		"errors take priority over failed processes; got: %v", snap.SuggestedNext)
}

// TestFormatSnapshot_FitsThirtyLineBudget verifies the compact text
// rendering for a typical 3-process stack stays within the ~30-line
// budget specified in the acceptance criteria.
func TestFormatSnapshot_FitsThirtyLineBudget(t *testing.T) {
	snap := SnapshotData{
		Processes: []SnapshotProcess{
			{ID: "web", State: "running", UptimeMs: 720000, URLs: []string{"http://localhost:3000"}, ErrorCount: 0},
			{ID: "api", State: "running", UptimeMs: 480000, URLs: []string{"http://localhost:8080"}, ErrorCount: 2, LastError: "undefined: UserCache"},
			{ID: "db", State: "running", UptimeMs: 720000, ErrorCount: 0},
		},
		Proxies: []SnapshotProxy{
			{ID: "dev", Target: "http://localhost:3000", ListenAddr: ":12345", RequestCount: 42, ErrorCount: 0, Running: true},
		},
		Errors: []SnapshotError{
			{Source: "process:api", ProcessID: "api", Severity: "error", Message: "undefined: UserCache", File: "src/handlers/user.go", Line: "88"},
			{Source: "process:api", ProcessID: "api", Severity: "error", Message: "cannot use nil as type Cache", File: "src/handlers/user.go", Line: "91"},
		},
		URLs: []SnapshotURL{
			{ProcessID: "web", URL: "http://localhost:3000"},
			{ProcessID: "api", URL: "http://localhost:8080"},
		},
		SuggestedNext: []string{
			`proc {action:"output", process_id:"api", grep:"error|warn"}  ← 2 errors need review`,
		},
	}
	out := formatSnapshot(&snap)
	lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
	assert.LessOrEqual(t, len(lines), 30,
		"compact snapshot must fit in ~30 lines for a typical 3-process stack; got %d lines:\n%s",
		len(lines), out)

	// Sanity: the header and section names appear so the output is
	// recognisable and not a stray empty render.
	assert.Contains(t, out, "Dev Environment Snapshot")
	assert.Contains(t, out, "PROCESSES")
	assert.Contains(t, out, "PROXIES")
	assert.Contains(t, out, "RECENT ERRORS")
	assert.Contains(t, out, "URLs DISCOVERED")
	assert.Contains(t, out, "SUGGESTED NEXT")
}

// TestFormatSnapshot_EmptyState renders cleanly with no processes,
// proxies, or errors. The agent should still see a meaningful
// suggested-next entry rather than an empty result.
func TestFormatSnapshot_EmptyState(t *testing.T) {
	snap := buildSnapshot(nil, nil, nil)
	out := formatSnapshot(&snap)
	assert.Contains(t, out, "PROCESSES (0 running)")
	assert.Contains(t, out, "(none)")
	require.NotEmpty(t, snap.SuggestedNext)
	assert.Contains(t, snap.SuggestedNext[0], "watch",
		"empty environment is treated as 'all healthy'; suggest monitoring")
}

// TestProcessIDFromSource verifies the parser used to attribute errors
// back to processes.
func TestProcessIDFromSource(t *testing.T) {
	cases := []struct {
		source, want string
	}{
		{"process:api", "api"},
		{"process:web-server", "web-server"},
		{"process:", ""},
		{"browser:js", ""},
		{"proxy:http", ""},
		{"", ""},
	}
	for _, c := range cases {
		assert.Equal(t, c.want, processIDFromSource(c.source), "source=%q", c.source)
	}
}

// TestSplitFileLine verifies location parsing — used to fill the file
// and line fields of a SnapshotError.
func TestSplitFileLine(t *testing.T) {
	cases := []struct {
		loc, file, line string
	}{
		{"src/foo.go:42:5", "src/foo.go", "42"},
		{"src/foo.go:42", "src/foo.go", "42"},
		{"src/foo.go", "src/foo.go", ""},
		{"", "", ""},
	}
	for _, c := range cases {
		f, l := splitFileLine(c.loc)
		assert.Equal(t, c.file, f, "loc=%q", c.loc)
		assert.Equal(t, c.line, l, "loc=%q", c.loc)
	}
}

// TestFormatUptimeMs verifies uptime rendering across thresholds.
func TestFormatUptimeMs(t *testing.T) {
	assert.Equal(t, "-", formatUptimeMs(0))
	assert.Equal(t, "-", formatUptimeMs(-1))
	assert.Equal(t, "30s", formatUptimeMs(30_000))
	assert.Equal(t, "12m", formatUptimeMs(720_000))
	assert.Equal(t, "2h", formatUptimeMs(7_200_000))
}

// TestProxyErrorCountDistribution verifies that proxy-source errors are
// distributed across proxies (best-effort given no per-proxy attribution
// in the unified error stream).
func TestProxyErrorCountDistribution(t *testing.T) {
	procs := []map[string]interface{}{}
	proxies := []map[string]interface{}{
		{"id": "p1", "target_url": "http://a", "listen_addr": ":1"},
		{"id": "p2", "target_url": "http://b", "listen_addr": ":2"},
	}
	errors := []unifiedError{
		{Source: "proxy:http", Severity: "error", Message: "500", Count: 3, LastSeen: time.Now()},
	}
	snap := buildSnapshot(procs, proxies, errors)
	require.Len(t, snap.Proxies, 2)
	total := snap.Proxies[0].ErrorCount + snap.Proxies[1].ErrorCount
	assert.Equal(t, 3, total, "proxy error counts must sum to the total")
}

// findProcess is a test helper.
func findProcess(procs []SnapshotProcess, id string) *SnapshotProcess {
	for i, p := range procs {
		if p.ID == id {
			return &procs[i]
		}
	}
	return nil
}

// BenchmarkBuildSnapshot_5Processes measures the assembly cost for a
// 5-process / 1-proxy / 4-error stack. The acceptance criterion targets
// <200ms wall-clock for the full snapshot tool call. Since IPC dominates,
// the pure assembly should be well under 1ms — anything slower means
// the format/build path itself is the bottleneck and needs review.
func BenchmarkBuildSnapshot_5Processes(b *testing.B) {
	procEntries := make([]map[string]interface{}, 5)
	for i := 0; i < 5; i++ {
		procEntries[i] = map[string]interface{}{
			"id":         "proc" + string(rune('a'+i)),
			"state":      "running",
			"runtime_ms": float64(60000 + i*30000),
			"command":    "go run ./cmd/main.go",
			"urls":       []interface{}{"http://localhost:300" + string(rune('0'+i))},
		}
	}
	proxyEntries := []map[string]interface{}{
		{"id": "dev", "target_url": "http://localhost:3000", "listen_addr": ":10000", "running": true, "total_requests": float64(100)},
	}
	now := time.Now()
	errors := []unifiedError{
		{Source: "process:proca", Severity: "error", Category: "C", Message: "msg1", Location: "f.go:1:1", Count: 1, LastSeen: now},
		{Source: "process:procb", Severity: "error", Category: "C", Message: "msg2", Location: "f.go:2:1", Count: 1, LastSeen: now},
		{Source: "proxy:http", Severity: "warning", Category: "404", Message: "msg3", Count: 1, LastSeen: now},
		{Source: "browser:js", Severity: "error", Category: "TypeError", Message: "msg4", Location: "x.ts:5:10", Count: 1, LastSeen: now},
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = buildSnapshot(procEntries, proxyEntries, errors)
	}
}

// A snapshot assembled while one of its error sources failed is incomplete. The
// raw path returns only the structured Snapshot, so warnings must ride a
// structured field — appending them to the text rendering alone let a raw
// consumer read the gap as "nothing wrong".
func TestSnapshotOutput_WarningsSurfaceInBothModes(t *testing.T) {
	snap := buildSnapshot(nil, nil, nil)
	warnings := []string{"alert store query failed: timeout", "proxy list unavailable"}
	filter := protocol.DirectoryFilter{SessionCode: "abc", Global: true}

	raw := snapshotOutput(&snap, "/proj", filter, warnings, true)
	assert.Equal(t, warnings, raw.Warnings, "raw consumers must see collection failures")
	assert.Empty(t, raw.Output, "raw mode renders no text")
	assert.Equal(t, "/proj", raw.ProjectPath)
	assert.Equal(t, "abc", raw.SessionCode)
	assert.True(t, raw.Global)

	text := snapshotOutput(&snap, "/proj", filter, warnings, false)
	assert.Equal(t, warnings, text.Warnings)
	for _, w := range warnings {
		assert.Contains(t, text.Output, w, "text mode still renders every warning")
	}
}

func TestSnapshotOutput_CleanCollectionHasNoWarnings(t *testing.T) {
	snap := buildSnapshot(nil, nil, nil)
	for _, raw := range []bool{true, false} {
		out := snapshotOutput(&snap, "/proj", protocol.DirectoryFilter{}, nil, raw)
		assert.Empty(t, out.Warnings)
		assert.NotContains(t, out.Output, "⚠")
	}
}
