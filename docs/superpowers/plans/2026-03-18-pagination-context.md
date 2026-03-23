# Pagination Context for List Outputs Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix empty `{}` responses from list-returning MCP tools by adding pagination context to proxylog queries and removing `omitempty` from `Count` on all other output structs. Fix daemon unknown-action errors to list valid actions. Fix daemon proxylog key mismatch bug.

**Architecture:** Two-tier approach: (1) Full `Pagination` struct for `ProxyLogOutput` — the only tool with real filtering, large result sets, and where total context matters. (2) Simple `omitempty` removal on `Count` for all other output structs (proc, proxy, currentpage, store, automation, browser, session, tunnel) — these are small unfiltered lists where just showing `count: 0` is sufficient context.

**Tech Stack:** Go, MCP tools (`internal/tools/`), daemon IPC (`internal/daemon/hub_handlers.go`)

---

### Task 1: Fix daemon proxylog query key mismatch bug

The daemon sends `{"logs": entries}` but the client reads `result["entries"]` and `result["count"]` — causing all daemon proxylog queries to return empty. This is the root cause of the original `{}` responses.

**Files:**
- Modify: `internal/daemon/hub_handlers.go:1235-1253`

- [ ] **Step 1: Write a test that demonstrates the bug**

Add to `internal/daemon/hub_integration_test.go`:

```go
func TestHubProxyLogQueryReturnsCorrectKeys(t *testing.T) {
	// Start daemon with proxy that has log entries
	d, conn := setupDaemonWithProxy(t)
	defer d.Shutdown(context.Background())

	// The response should use "entries" and "count" keys, not "logs"
	result, err := conn.Request(protocol.VerbProxyLog, protocol.SubVerbQuery, "dev").JSON()
	require.NoError(t, err)

	// Verify the response uses the correct keys
	_, hasEntries := result["entries"]
	_, hasCount := result["count"]
	_, hasLogs := result["logs"]
	assert.True(t, hasEntries || hasCount, "response must have 'entries' or 'count' key")
	assert.False(t, hasLogs, "response must not use 'logs' key — client expects 'entries'")
}
```

If no existing test helper `setupDaemonWithProxy` exists, adapt from existing integration test patterns in the same file.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/beagle/work/core/agnt && go test -run TestHubProxyLogQueryReturnsCorrectKeys -v ./internal/daemon/`
Expected: FAIL — response has `logs` key, not `entries`

- [ ] **Step 3: Fix the daemon handler**

In `internal/daemon/hub_handlers.go`, change `hubHandleProxyLogQuery` (line 1249-1252) from:

```go
	entries := p.Logger().Query(filter)

	data, _ := json.Marshal(map[string]interface{}{"logs": entries})
	return conn.WriteJSON(data)
```

To:

```go
	entries := p.Logger().Query(filter)
	stats := p.Logger().Stats()

	data, _ := json.Marshal(map[string]interface{}{
		"entries":         entries,
		"count":           len(entries),
		"total_available": stats.AvailableEntries,
	})
	return conn.WriteJSON(data)
```

This fixes the key mismatch AND adds `total_available` for the pagination work in Task 3.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/beagle/work/core/agnt && go test -run TestHubProxyLogQueryReturnsCorrectKeys -v ./internal/daemon/`
Expected: PASS

- [ ] **Step 5: Run full daemon test suite**

Run: `cd /home/beagle/work/core/agnt && go test ./internal/daemon/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/daemon/hub_handlers.go internal/daemon/hub_integration_test.go
git commit -m "fix: daemon proxylog query returns 'logs' key but client expects 'entries'"
```

---

### Task 2: Create shared Pagination struct

**Files:**
- Create: `internal/tools/pagination.go`
- Create: `internal/tools/pagination_test.go`

- [ ] **Step 1: Write the failing test**

```go
// pagination_test.go
package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPaginationAlwaysSerializesZero(t *testing.T) {
	p := Pagination{Count: 0, TotalAvailable: 0, Limit: 100}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"count":0`)
	assert.Contains(t, string(b), `"total_available":0`)
	assert.Contains(t, string(b), `"limit":100`)
}

func TestPaginationFilteredOmittedWhenFalse(t *testing.T) {
	p := Pagination{Count: 5, TotalAvailable: 10, Limit: 100, Filtered: false}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.NotContains(t, string(b), `"filtered"`)
}

func TestPaginationFilteredShownWhenTrue(t *testing.T) {
	p := Pagination{Count: 0, TotalAvailable: 10, Limit: 100, Filtered: true}
	b, err := json.Marshal(p)
	require.NoError(t, err)
	assert.Contains(t, string(b), `"filtered":true`)
}

func TestNewPagination(t *testing.T) {
	p := NewPagination(5, 42, 100, true)
	assert.Equal(t, 5, p.Count)
	assert.Equal(t, 42, p.TotalAvailable)
	assert.Equal(t, 100, p.Limit)
	assert.True(t, p.Filtered)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/beagle/work/core/agnt && go test -run TestPagination -v ./internal/tools/`
Expected: FAIL — `Pagination` type not found

- [ ] **Step 3: Write minimal implementation**

```go
// pagination.go
package tools

// Pagination provides context for list/query results.
// Count, TotalAvailable, and Limit never use omitempty so zero values are visible.
type Pagination struct {
	Count          int  `json:"count"`
	TotalAvailable int  `json:"total_available"`
	Limit          int  `json:"limit"`
	Filtered       bool `json:"filtered,omitempty"`
}

// NewPagination creates a Pagination with all fields set.
func NewPagination(count, totalAvailable, limit int, filtered bool) Pagination {
	return Pagination{
		Count:          count,
		TotalAvailable: totalAvailable,
		Limit:          limit,
		Filtered:       filtered,
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/beagle/work/core/agnt && go test -run TestPagination -v ./internal/tools/`
Expected: PASS

- [ ] **Step 5: Commit**

```bash
git add internal/tools/pagination.go internal/tools/pagination_test.go
git commit -m "feat: add shared Pagination struct for list output context"
```

---

### Task 3: Add Pagination to ProxyLogOutput

ProxyLogOutput is the only output struct getting full pagination because it has real filtering (types, url_pattern, methods, status_codes, since, until, errors_only, diagnostic_levels) and large result sets.

**Files:**
- Modify: `internal/tools/proxy_tools.go` — `ProxyLogOutput` struct, `handleProxyLogQuery`, `handleProxyLogQueryCompact`, `handleProxyLogQueryRaw`
- Modify: `internal/tools/daemon_tools.go` — `handleProxyLogQuery`
- Modify: `internal/tools/pagination_test.go` — add ProxyLogOutput tests

- [ ] **Step 1: Write the failing test**

Add to `internal/tools/pagination_test.go`:

```go
func TestProxyLogOutputZeroCountSerializes(t *testing.T) {
	pag := NewPagination(0, 0, 100, false)
	output := ProxyLogOutput{
		Pagination: &pag,
	}
	b, err := json.Marshal(output)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"count":0`)
	assert.Contains(t, s, `"total_available":0`)
	assert.Contains(t, s, `"limit":100`)
	assert.NotEqual(t, "{}", s, "zero-result output must not be empty JSON")
}

func TestProxyLogOutputFilteredShowsContext(t *testing.T) {
	pag := NewPagination(0, 42, 100, true)
	output := ProxyLogOutput{
		Pagination: &pag,
	}
	b, err := json.Marshal(output)
	require.NoError(t, err)
	s := string(b)
	assert.Contains(t, s, `"count":0`)
	assert.Contains(t, s, `"total_available":42`)
	assert.Contains(t, s, `"filtered":true`)
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/beagle/work/core/agnt && go test -run TestProxyLogOutput -v ./internal/tools/`
Expected: FAIL — `ProxyLogOutput` has no field `Pagination`

- [ ] **Step 3: Modify ProxyLogOutput struct**

In `internal/tools/proxy_tools.go` (line 415-430), replace the `Count` field with `Pagination`:

```go
type ProxyLogOutput struct {
	// For query
	Entries    []LogEntryOutput `json:"entries,omitempty"`
	Pagination *Pagination      `json:"pagination,omitempty"`

	// For summary
	Summary *ProxyLogSummary `json:"summary,omitempty"`

	// For stats
	Stats *LogStatsOutput `json:"stats,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}
```

- [ ] **Step 4: Create helper to detect if ProxyLogInput has active filters**

Add to `internal/tools/proxy_tools.go` near the ProxyLogInput struct:

```go
// hasFilters returns true if any filter parameters are set on the input.
func (input ProxyLogInput) hasFilters() bool {
	return len(input.Types) > 0 || input.URLPattern != "" || len(input.Methods) > 0 ||
		len(input.StatusCodes) > 0 || input.Since != "" || input.Until != "" ||
		input.ErrorsOnly || len(input.DiagnosticLevels) > 0
}
```

- [ ] **Step 5: Update legacy handleProxyLogQuery**

In `internal/tools/proxy_tools.go`, `handleProxyLogQuery` (line 885) already receives `proxyServer` and `input`. Change the function to pass stats info to the formatters.

Change the signature and body of `handleProxyLogQueryCompact` and `handleProxyLogQueryRaw` to accept pagination info:

```go
func handleProxyLogQuery(proxyServer *proxy.ProxyServer, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// ... existing filter building (lines 887-918, unchanged) ...

	// Default limit
	if filter.Limit == 0 {
		filter.Limit = 100
	}

	// Query logs
	entries := proxyServer.Logger().Query(filter)

	// Apply limit
	if filter.Limit > 0 && len(entries) > filter.Limit {
		entries = entries[:filter.Limit]
	}

	// Build pagination context
	stats := proxyServer.Logger().Stats()
	pag := NewPagination(len(entries), int(stats.AvailableEntries), filter.Limit, input.hasFilters())

	if input.Raw {
		return handleProxyLogQueryRaw(entries, &pag)
	}
	return handleProxyLogQueryCompact(entries, &pag)
}
```

Update `handleProxyLogQueryCompact` signature (line ~1121):

```go
func handleProxyLogQueryCompact(entries []proxy.LogEntry, pag *Pagination) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// ... existing formatting code unchanged ...

	return nil, ProxyLogOutput{
		Entries:    output,
		Pagination: pag,
	}, nil
}
```

Update `handleProxyLogQueryRaw` signature similarly (search for `func handleProxyLogQueryRaw`):

```go
func handleProxyLogQueryRaw(entries []proxy.LogEntry, pag *Pagination) (*mcp.CallToolResult, ProxyLogOutput, error) {
	// ... existing formatting code unchanged ...

	return nil, ProxyLogOutput{
		Entries:    output,
		Pagination: pag,
	}, nil
}
```

- [ ] **Step 6: Update daemon handleProxyLogQuery**

In `internal/tools/daemon_tools.go` (line 1431-1474), update to use Pagination:

```go
func (dt *DaemonTools) handleProxyLogQuery(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	filter := protocol.LogQueryFilter{
		Types:       input.Types,
		Methods:     input.Methods,
		URLPattern:  input.URLPattern,
		StatusCodes: input.StatusCodes,
		Since:       input.Since,
		Until:       input.Until,
		Limit:       input.Limit,
	}

	result, err := dt.client.ProxyLogQuery(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	count := getInt(result, "count")
	totalAvailable := getInt(result, "total_available")
	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	pag := NewPagination(count, totalAvailable, limit, input.hasFilters())

	output := ProxyLogOutput{
		Pagination: &pag,
	}

	if entries, ok := result["entries"].([]interface{}); ok {
		for _, e := range entries {
			if em, ok := e.(map[string]interface{}); ok {
				entry := LogEntryOutput{
					Type: getString(em, "type"),
				}
				if data, ok := em["data"].(map[string]interface{}); ok {
					if b, err := json.Marshal(data); err == nil {
						entry.Data = string(b)
					} else {
						entry.Data = "{}"
					}
				}
				if ts, ok := em["timestamp"].(string); ok {
					if t, err := time.Parse(time.RFC3339, ts); err == nil {
						entry.Timestamp = t
					}
				}
				output.Entries = append(output.Entries, entry)
			}
		}
	}

	return nil, output, nil
}
```

- [ ] **Step 7: Fix any compile errors referencing old Count field**

Search for `.Count` references on `ProxyLogOutput` and update. The daemon `handleProxyLogSummary` at line 1477 should not be affected — it uses the `Summary` field, not `Count`.

- [ ] **Step 8: Run tests**

Run: `cd /home/beagle/work/core/agnt && go test -run TestProxyLogOutput -v ./internal/tools/`
Expected: PASS

Run: `cd /home/beagle/work/core/agnt && go test ./internal/tools/... ./internal/daemon/...`
Expected: PASS

- [ ] **Step 9: Commit**

```bash
git add internal/tools/proxy_tools.go internal/tools/daemon_tools.go internal/tools/pagination_test.go
git commit -m "feat: add pagination context to ProxyLogOutput (legacy + daemon)"
```

---

### Task 4: Remove omitempty from Count on all other output structs

These structs have small, unfiltered lists where full pagination is overkill. Just making `Count` always serialize (removing `omitempty`) solves the `{}` problem.

**Files:**
- Modify: `internal/tools/process.go` — `ProcOutput`
- Modify: `internal/tools/proxy_tools.go` — `ProxyOutput`, `CurrentPageOutput`
- Modify: `internal/tools/store_tools.go` — `StoreOutput`
- Modify: `internal/tools/automation_tools.go` — `AutomationOutput`
- Modify: `internal/tools/browser_tools.go` — `BrowserOutput`
- Modify: `internal/tools/session_tools.go` — `SessionOutput`
- Modify: `internal/tools/tunnel_tools.go` — `TunnelOutput`

- [ ] **Step 1: Write test that verifies zero count serializes**

Add to `internal/tools/pagination_test.go`:

```go
func TestOutputStructsSerializeZeroCount(t *testing.T) {
	tests := []struct {
		name   string
		output interface{}
	}{
		{"ProcOutput", ProcOutput{}},
		{"ProxyOutput", ProxyOutput{}},
		{"CurrentPageOutput", CurrentPageOutput{}},
		{"StoreOutput", StoreOutput{}},
		{"AutomationOutput", AutomationOutput{}},
		{"BrowserOutput", BrowserOutput{}},
		{"SessionOutput", SessionOutput{}},
		{"TunnelOutput", TunnelOutput{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := json.Marshal(tt.output)
			require.NoError(t, err)
			s := string(b)
			assert.Contains(t, s, `"count":0`, "count:0 must always appear even when zero")
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd /home/beagle/work/core/agnt && go test -run TestOutputStructsSerializeZeroCount -v ./internal/tools/`
Expected: FAIL — `"count":0` not found in output (omitted by omitempty)

- [ ] **Step 3: Remove omitempty from Count in each struct**

In each file, change:
```go
Count int `json:"count,omitempty"`
```
To:
```go
Count int `json:"count"`
```

Files and approximate line numbers:
- `internal/tools/process.go:90`
- `internal/tools/proxy_tools.go:110` (CurrentPageOutput)
- `internal/tools/proxy_tools.go:327` (ProxyOutput)
- `internal/tools/store_tools.go:28`
- `internal/tools/automation_tools.go:44`
- `internal/tools/browser_tools.go:38`
- `internal/tools/session_tools.go:28`
- `internal/tools/tunnel_tools.go:34`

- [ ] **Step 4: Run test to verify it passes**

Run: `cd /home/beagle/work/core/agnt && go test -run TestOutputStructsSerializeZeroCount -v ./internal/tools/`
Expected: PASS

- [ ] **Step 5: Run full test suite**

Run: `cd /home/beagle/work/core/agnt && go test ./internal/tools/...`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add internal/tools/process.go internal/tools/proxy_tools.go internal/tools/store_tools.go \
  internal/tools/automation_tools.go internal/tools/browser_tools.go internal/tools/session_tools.go \
  internal/tools/tunnel_tools.go internal/tools/pagination_test.go
git commit -m "fix: remove omitempty from Count on list output structs so zero counts serialize"
```

---

### Task 5: Fix daemon unknown-action errors to list valid actions

**Files:**
- Modify: `internal/tools/daemon_tools.go` — 4 error messages

- [ ] **Step 1: Update all daemon unknown-action errors**

Change these 4 lines to include valid actions (verify against each switch statement's cases):

1. `daemon_tools.go:640` (proc) — cases: status, output, stop, restart, list, cleanup_port, autorestart
```go
return errorResult(fmt.Sprintf("unknown action %q. Use: status, output, stop, restart, list, cleanup_port, autorestart", input.Action)), ProcOutput{}, nil
```

2. `daemon_tools.go:862` (proxy) — cases: start, stop, restart, status, list, exec, toast, chaos
```go
return errorResult(fmt.Sprintf("unknown action %q. Use: start, stop, restart, status, list, exec, toast, chaos", input.Action)), ProxyOutput{}, nil
```

3. `daemon_tools.go:1426` (proxylog) — cases: query, summary, clear, stats
```go
return errorResult(fmt.Sprintf("unknown action %q. Use: query, summary, clear, stats", action)), ProxyLogOutput{}, nil
```

4. `daemon_tools.go:1579` (currentpage) — cases: list, get, summary, clear
```go
return errorResult(fmt.Sprintf("unknown action %q. Use: list, get, summary, clear", action)), CurrentPageOutput{}, nil
```

- [ ] **Step 2: Verify valid actions match switch cases**

For each error message, read the switch statement above it and confirm every case is listed. Pay special attention to the proxy handler which has `restart` and `toast` and `chaos` cases.

- [ ] **Step 3: Run tests**

Run: `cd /home/beagle/work/core/agnt && go test ./internal/tools/...`
Expected: PASS

- [ ] **Step 4: Commit**

```bash
git add internal/tools/daemon_tools.go
git commit -m "fix: include valid actions in daemon unknown-action error messages"
```

---

### Task 6: Run full test suite and gofmt

- [ ] **Step 1: Run gofmt on all modified files**

```bash
cd /home/beagle/work/core/agnt && gofmt -w internal/tools/pagination.go internal/tools/pagination_test.go internal/tools/proxy_tools.go internal/tools/daemon_tools.go internal/tools/process.go internal/tools/store_tools.go internal/tools/automation_tools.go internal/tools/browser_tools.go internal/tools/session_tools.go internal/tools/tunnel_tools.go internal/daemon/hub_handlers.go
```

- [ ] **Step 2: Run full test suite**

Run: `cd /home/beagle/work/core/agnt && go test ./...`
Expected: PASS

- [ ] **Step 3: Commit if gofmt made changes**

```bash
git add -u && git diff --cached --quiet || git commit -m "style: gofmt formatting"
```
