# get_errors MCP Tool Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Add a unified `get_errors` MCP tool that collects errors from process output (via AlertScanner), browser JS errors, HTTP errors, and proxy diagnostics into a single deduplicated, severity-sorted view.

**Architecture:** Aggregator pattern in the tool handler layer. Add a ring buffer to AlertScanner for retaining recent matches. The `get_errors` handler queries both the AlertScanner ring buffer (via new daemon IPC verb) and the TrafficLogger (via existing `ErrorsOnly` filter), merges results into a unified format with deduplication, noise filtering, and stack trace reduction.

**Tech Stack:** Go 1.24, MCP SDK (`github.com/modelcontextprotocol/go-sdk/mcp`), existing AlertScanner + TrafficLogger infrastructure.

---

### Task 1: Add Ring Buffer to AlertScanner

**Files:**
- Modify: `internal/overlay/alerts.go`
- Test: `internal/overlay/alerts_test.go`

**Step 1: Write the failing test**

Add to `alerts_test.go`:

```go
func TestAlertScanner_RecentMatches(t *testing.T) {
	var delivered []*AlertBatch
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  50 * time.Millisecond,
		DedupeWindow: 50 * time.Millisecond,
		OnAlert: func(b *AlertBatch) {
			delivered = append(delivered, b)
		},
	})
	defer scanner.Stop()

	// Process several lines that match default patterns
	scanner.ProcessLine("panic: runtime error: index out of range", "dev-server")
	scanner.ProcessLine("Error: ENOENT: no such file or directory", "dev-server")

	// RecentMatches should return all matches regardless of dedup/batch state
	since := time.Now().Add(-1 * time.Minute)
	matches := scanner.RecentMatches(since)
	assert.Len(t, matches, 2)
	assert.Equal(t, "dev-server", matches[0].ScriptID)
}

func TestAlertScanner_RecentMatches_SinceFilter(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  time.Hour, // Don't flush
		DedupeWindow: 50 * time.Millisecond,
	})
	defer scanner.Stop()

	scanner.ProcessLine("panic: runtime error: slice bounds out of range", "srv")
	time.Sleep(20 * time.Millisecond)
	cutoff := time.Now()
	time.Sleep(20 * time.Millisecond)
	scanner.ProcessLine("Error: Cannot find module 'express'", "srv")

	all := scanner.RecentMatches(time.Time{})
	assert.Len(t, all, 2)

	recent := scanner.RecentMatches(cutoff)
	assert.Len(t, recent, 1)
	assert.Contains(t, recent[0].Line, "Cannot find module")
}

func TestAlertScanner_RecentMatches_RingBufferOverflow(t *testing.T) {
	scanner := NewAlertScanner(AlertScannerConfig{
		BatchWindow:  time.Hour,
		DedupeWindow: 1 * time.Millisecond,
	})
	defer scanner.Stop()

	// Write more than ring buffer capacity (200)
	time.Sleep(5 * time.Millisecond) // Let dedup window expire
	for i := 0; i < 250; i++ {
		scanner.ProcessLine(fmt.Sprintf("panic: error number %d", i), "srv")
		time.Sleep(1 * time.Millisecond) // Ensure unique dedup windows
	}

	matches := scanner.RecentMatches(time.Time{})
	assert.LessOrEqual(t, len(matches), 200)
	// Should have the most recent entries
	assert.Contains(t, matches[len(matches)-1].Line, "249")
}
```

**Step 2: Run test to verify it fails**

Run: `go test -v -run TestAlertScanner_RecentMatches ./internal/overlay/`
Expected: FAIL — `RecentMatches` method not found.

**Step 3: Implement ring buffer in AlertScanner**

In `alerts.go`, add a fixed-size ring buffer that captures every match (before dedup filtering). The ring buffer is separate from the dedup/batch pipeline so it captures everything.

Add fields to `AlertScanner`:
```go
// Ring buffer for on-demand querying
matchBuf     [200]*AlertMatch  // Fixed ring buffer
matchBufHead int               // Next write position
matchBufLen  int               // Number of entries (max 200)
matchBufMu   sync.RWMutex     // Separate lock from batch pipeline
```

Add `recordMatch(m *AlertMatch)` called from `ProcessLine` after creating the match, before `addMatch` (which does dedup). This writes to the ring buffer under `matchBufMu`.

Add `RecentMatches(since time.Time) []*AlertMatch` that reads the ring buffer and filters by timestamp.

**Step 4: Run tests to verify they pass**

Run: `go test -v -run TestAlertScanner_RecentMatches ./internal/overlay/`
Expected: PASS

**Step 5: Commit**

```
feat(alerts): add ring buffer to AlertScanner for on-demand querying
```

---

### Task 2: Add Daemon IPC for Alert Queries

**Files:**
- Modify: `internal/protocol/commands.go` (add verb/subverb constants)
- Modify: `internal/daemon/hub_handlers.go` (add handler)
- Modify: `internal/daemon/client.go` (add client method)
- Modify: `internal/daemon/resilient.go` (add resilient wrapper)
- Modify: `cmd/agnt/pty_common.go` (expose scanner to daemon)

**Step 1: Write the failing test**

Add to `internal/daemon/hub_integration_test.go`:

```go
func TestHubIntegration_AlertQuery(t *testing.T) {
	// Start daemon, run a process that produces errors,
	// verify ALERTS QUERY returns recent matches
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	d := New(DaemonConfig{SocketPath: sockPath})
	// ... standard daemon setup ...

	// Query alerts - should return empty initially
	result, err := client.AlertQuery(protocol.AlertQueryFilter{})
	require.NoError(t, err)
	assert.NotNil(t, result)
}
```

**Step 2: Run test to verify it fails**

Run: `go test -v -run TestHubIntegration_AlertQuery ./internal/daemon/`
Expected: FAIL — `AlertQuery` method not found.

**Step 3: Implement the IPC plumbing**

In `protocol/commands.go`, add:
```go
const VerbAlerts = "ALERTS"
// SubVerbQuery already exists
```

In `daemon/client.go`, add:
```go
type AlertQueryFilter struct {
	Since     string `json:"since,omitempty"`
	ProcessID string `json:"process_id,omitempty"`
	Limit     int    `json:"limit,omitempty"`
}

func (c *Client) AlertQuery(filter protocol.AlertQueryFilter) (map[string]interface{}, error) {
	return c.conn.Request(protocol.VerbAlerts, protocol.SubVerbQuery).WithJSON(filter).JSON()
}
```

In `daemon/resilient.go`, add the resilient wrapper.

In `daemon/hub_handlers.go`, add the handler that reads from the AlertScanner's ring buffer. The daemon needs access to the AlertScanner — this requires exposing it via the daemon hub or a callback interface.

The cleanest approach: Add an `AlertQuerier` interface to the daemon package:
```go
type AlertQuerier interface {
	RecentMatches(since time.Time) []*AlertMatchInfo
}
```

Where `AlertMatchInfo` is a daemon-package struct (avoiding import of overlay). The `pty_common.go` wiring connects the AlertScanner to the daemon via this interface.

**Step 4: Run test to verify it passes**

Run: `go test -v -run TestHubIntegration_AlertQuery ./internal/daemon/`
Expected: PASS

**Step 5: Commit**

```
feat(daemon): add ALERTS QUERY IPC verb for alert ring buffer queries
```

---

### Task 3: Implement `get_errors` Tool Handler

**Files:**
- Create: `internal/tools/get_errors.go`
- Test: `internal/tools/get_errors_test.go`

**Step 1: Write the failing test**

```go
func TestGetErrors_FormatUnifiedError(t *testing.T) {
	// Test the formatting function that converts raw error data
	// into the unified compact output format

	tests := []struct {
		name     string
		errors   []unifiedError
		expected string
	}{
		{
			name: "browser js error",
			errors: []unifiedError{{
				Source:    "browser:js",
				Category: "TypeError",
				Message:  "Cannot read properties of undefined (reading 'map')",
				Location: "src/components/UserList.tsx:28:12",
				PageURL:  "http://localhost:3000/users",
				Count:    1,
				LastSeen: time.Now(),
				Severity: "error",
			}},
			expected: `[browser:js] TypeError (1x`,
		},
		{
			name: "http 500",
			errors: []unifiedError{{
				Source:    "proxy:http",
				Category: "500 Internal Server Error",
				Message:  `POST /api/users → "Validation failed: email is required"`,
				Count:    3,
				LastSeen: time.Now(),
				Severity: "error",
			}},
			expected: `[proxy:http] 500 Internal Server Error (3x`,
		},
	}
	// ... assert formatted output contains expected substrings
}
```

**Step 2: Run test to verify it fails**

Run: `go test -v -run TestGetErrors ./internal/tools/`
Expected: FAIL — types not defined.

**Step 3: Implement the handler**

Create `internal/tools/get_errors.go` with:

**Input struct:**
```go
type GetErrorsInput struct {
	ProcessID       string `json:"process_id,omitempty" jsonschema:"Filter to specific process"`
	ProxyID         string `json:"proxy_id,omitempty" jsonschema:"Filter to specific proxy"`
	Since           string `json:"since,omitempty" jsonschema:"Override recency filter (RFC3339 or duration like '5m')"`
	IncludeWarnings bool   `json:"include_warnings,omitempty" jsonschema:"Include warnings (default: true)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"Max errors to return (default: 25)"`
	Raw             bool   `json:"raw,omitempty" jsonschema:"Return full JSON with all fields"`
}
```

**Output struct:**
```go
type GetErrorsOutput struct {
	ErrorCount   int    `json:"error_count"`
	WarningCount int    `json:"warning_count"`
	Summary      string `json:"summary,omitempty"`
}
```

**Internal types:**
```go
type unifiedError struct {
	Source    string    // "process:<id>", "browser:js", "proxy:http", "proxy:transport"
	Category string    // Error type name or HTTP status
	Message  string    // Core error message
	Location string    // file:line:col if available
	PageURL  string    // Browser page URL if applicable
	Count    int       // Dedup count
	LastSeen time.Time // Most recent occurrence
	Severity string    // "error" or "warning"
}
```

**Key logic in the handler:**

1. **Determine time boundary**: If `since` provided, parse it. Otherwise, query process start times and proxy start times, use the most recent restart as the boundary.

2. **Collect proxy errors**: For each active proxy (or filtered proxy), call `ProxyLogQuery` with `ErrorsOnly: true` and the `since` boundary.

3. **Collect process alerts**: Call `AlertQuery` with the `since` boundary.

4. **Normalize to `unifiedError`**: Convert each source into the common struct:
   - `FrontendError` → extract first app frame from stack, message, source:line:col
   - `HTTPLogEntry` with status >= 400 → method + URL + truncated body
   - `HTTPLogEntry` with `.Error` → transport error with target
   - `ProxyDiagnostic` → category + event + message
   - `AlertMatch` → pattern description + matched line + category
   - `CustomLog` with level=error → message

5. **Noise filter**: Skip entries matching noise patterns:
   - HTTP 404 for `.map`, `favicon.ico`, `__webpack_hmr`, `hot-update`
   - HTTP 304, 301, 302

6. **Deduplicate**: Group by `(source, category, message, location)`. Increment count, update `LastSeen`.

7. **Sort**: Errors before warnings. Within each, most recent first.

8. **Format**: Compact text format (default) or raw JSON.

**Stack trace reduction** (helper function):
```go
func extractFirstAppFrame(stack string) string {
	// Split stack into lines
	// Skip lines containing: node_modules/, internal/, runtime/, <anonymous>
	// Return first remaining file:line:col
}
```

**HTTP body truncation** (helper function):
```go
func extractErrorMessage(body string, maxLen int) string {
	// Try JSON: parse and extract "message", "error", or "detail" field
	// Try HTML: strip tags, take first non-empty text
	// Fallback: first maxLen chars
}
```

**Step 4: Run tests to verify they pass**

Run: `go test -v -run TestGetErrors ./internal/tools/`
Expected: PASS

**Step 5: Commit**

```
feat(tools): add get_errors MCP tool for unified error collection
```

---

### Task 4: Register the Tool and Wire It Up

**Files:**
- Modify: `internal/tools/daemon_tools.go` (add `makeGetErrorsHandler`, register in `RegisterDaemonTools`)
- Modify: `cmd/agnt/serve.go` (update tool list in instructions string)

**Step 1: Write the failing test**

Verify the tool is registered by checking server tool list includes `get_errors`.

**Step 2: Implement registration**

In `daemon_tools.go`, inside `RegisterDaemonTools()`, add:
```go
mcp.AddTool(server, &mcp.Tool{
	Name: "get_errors",
	Description: `Get all current errors across processes and proxies.

Collects errors from: process output (compile errors, panics, exceptions),
browser JavaScript errors, HTTP 4xx/5xx responses, and proxy transport errors.

Default behavior:
  - Only shows errors since the last process/proxy restart
  - Deduplicates identical errors (shows count)
  - Reduces stack traces to first application code frame
  - Filters out noise (static asset 404s, redirects)
  - Sorts by severity (errors first) then recency

Examples:
  get_errors {}
  get_errors {proxy_id: "dev"}
  get_errors {process_id: "dev-server", since: "5m"}
  get_errors {include_warnings: false}
  get_errors {raw: true, limit: 50}`,
}, dt.makeGetErrorsHandler())
```

Update the `Instructions` string in `serve.go` to include `get_errors`.

**Step 3: Run full test suite**

Run: `go test ./...`
Expected: PASS

**Step 4: Commit**

```
feat: register get_errors tool in daemon and legacy MCP servers
```

---

### Task 5: Wire AlertScanner to Daemon AlertQuerier

**Files:**
- Modify: `cmd/agnt/pty_common.go` (bridge AlertScanner to daemon)
- Modify: `internal/daemon/hub.go` or `internal/daemon/daemon.go` (accept AlertQuerier)

**Step 1: Write the failing integration test**

Test that when `agnt run` starts a process that produces errors, calling `get_errors` returns those process errors alongside any proxy errors.

**Step 2: Implement the wiring**

In `pty_common.go`, the `setupAlertScanner` function already creates the scanner. Add an adapter that wraps `*overlay.AlertScanner` into the `daemon.AlertQuerier` interface, converting `overlay.AlertMatch` to `daemon.AlertMatchInfo` (to avoid import cycles).

Register this adapter with the daemon hub so the `ALERTS QUERY` handler can read from it.

**Step 3: Run integration test**

Run: `go test -v -run TestGetErrors_Integration ./...`
Expected: PASS

**Step 4: Commit**

```
feat: wire AlertScanner ring buffer to daemon for get_errors queries
```

---

### Task 6: Add Legacy Mode Support

**Files:**
- Create or modify: `internal/tools/get_errors.go` (add `RegisterGetErrorsTool` for legacy mode)
- Modify: `cmd/agnt/serve.go` (register in legacy server)

**Step 1: Implement**

Add `RegisterGetErrorsTool(server *mcp.Server, pm *process.ProcessManager, proxym *proxy.ProxyManager)` that works without the daemon. In legacy mode, process alerts won't be available (no AlertScanner), so the tool only collects proxy errors.

Register in `runLegacyServer()`.

**Step 2: Run tests**

Run: `go test ./...`
Expected: PASS

**Step 3: Commit**

```
feat: add get_errors support for legacy (non-daemon) mode
```

---

### Task 7: End-to-End Verification

**Step 1:** Start agnt with a dev server that produces errors. Verify `get_errors {}` returns process compile errors.

**Step 2:** Start a proxy. Trigger a JS error in the browser. Verify `get_errors {}` returns the browser error with source location.

**Step 3:** Hit a 500 endpoint through the proxy. Verify `get_errors {}` shows the HTTP error with response body excerpt.

**Step 4:** Restart the dev server. Verify old errors are filtered out.

**Step 5:** Trigger the same error 5 times. Verify deduplication shows `(5x, latest Ns ago)`.

**Step 6: Commit**

```
test: verify get_errors end-to-end across all error sources
```
