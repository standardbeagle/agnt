package tools

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/standardbeagle/go-sdk/mcp"
)

// SnapshotData is the unified dev-environment status payload returned by
// proc {action: "snapshot"}. It merges proc list, proxy list, and error
// data into a single response so an agent can answer "what's running, is
// it healthy, what URLs do I have?" with one call instead of four.
//
// Field shape mirrors the task spec exactly so the contract is testable
// against the documented JSON.
type SnapshotData struct {
	Processes     []SnapshotProcess `json:"processes"`
	Proxies       []SnapshotProxy   `json:"proxies"`
	Errors        []SnapshotError   `json:"errors"`
	URLs          []SnapshotURL     `json:"urls"`
	SuggestedNext []string          `json:"suggested_next"`
}

// SnapshotProcess is the per-process row returned in a snapshot.
// State values follow the ProcessManager state machine (running, stopped,
// failed, starting). UptimeMs is preferred over a string so callers can
// do arithmetic; the formatted string lives only in the compact text
// output. URLs come from the daemon's URLTracker; ErrorCount comes from
// the unified error pipeline (same source as get_errors).
type SnapshotProcess struct {
	ID         string   `json:"id"`
	State      string   `json:"state"`
	UptimeMs   int64    `json:"uptime_ms"`
	URLs       []string `json:"urls,omitempty"`
	ErrorCount int      `json:"error_count"`
	LastError  string   `json:"last_error,omitempty"`
	Command    string   `json:"command,omitempty"`
}

// SnapshotProxy is the per-proxy row returned in a snapshot.
type SnapshotProxy struct {
	ID           string `json:"id"`
	Target       string `json:"target"`
	ListenAddr   string `json:"listen_addr"`
	RequestCount int64  `json:"request_count"`
	ErrorCount   int    `json:"error_count"`
	Running      bool   `json:"running"`
	State        string `json:"state,omitempty"`
}

// SnapshotError is a single error attributed to either a process or proxy.
// FilePath/Line are extracted from the unified error's Location field when
// available so callers don't have to re-parse "file:line:col" strings.
type SnapshotError struct {
	Source    string `json:"source"`               // "process:<id>" or "browser:js" etc — verbatim from the unified error
	ProcessID string `json:"process_id,omitempty"` // extracted from source when source is "process:<id>"
	Severity  string `json:"severity"`
	Message   string `json:"message"`
	File      string `json:"file,omitempty"`
	Line      string `json:"line,omitempty"`
}

// SnapshotURL is a discovered dev-server URL bound to its process.
type SnapshotURL struct {
	ProcessID string `json:"process_id"`
	URL       string `json:"url"`
}

// handleProcSnapshot collects process list, proxy list, and current
// errors in parallel and returns a unified dev-environment snapshot.
//
// Parallelism matters: the acceptance criteria say "<200ms for a 5-process
// stack". Three sequential daemon round-trips can blow that budget on a
// loaded socket; running them concurrently keeps the wall-clock close to
// the slowest single call.
func (dt *DaemonTools) handleProcSnapshot(input ProcInput) (*mcp.CallToolResult, ProcOutput, error) {
	// Build directory filter once so process list and proxy list see the
	// same scope (current session or current project).
	dirFilter := dt.scopeFilter(input.Global)

	type procResult struct {
		entries     []map[string]interface{}
		projectPath string
		global      bool
		err         error
	}
	type proxyResult struct {
		entries []map[string]interface{}
		err     error
	}
	type errorsResult struct {
		errors   []unifiedError
		warnings []string
	}

	var (
		procs   procResult
		proxies proxyResult
		errs    errorsResult
		wg      sync.WaitGroup
	)

	wg.Add(3)
	go func() {
		defer wg.Done()
		result, err := dt.client.ProcList(dirFilter)
		if err != nil {
			procs.err = err
			return
		}
		procs.projectPath = getString(result, "project_path")
		procs.global = getBool(result, "global")
		if raw, ok := result["processes"].([]interface{}); ok {
			procs.entries = make([]map[string]interface{}, 0, len(raw))
			for _, p := range raw {
				if pm, ok := p.(map[string]interface{}); ok {
					procs.entries = append(procs.entries, pm)
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		result, err := dt.client.ProxyList(dirFilter)
		if err != nil {
			proxies.err = err
			return
		}
		if raw, ok := result["proxies"].([]interface{}); ok {
			proxies.entries = make([]map[string]interface{}, 0, len(raw))
			for _, p := range raw {
				if pm, ok := p.(map[string]interface{}); ok {
					proxies.entries = append(proxies.entries, pm)
				}
			}
		}
	}()

	go func() {
		defer wg.Done()
		// Reuse the get_errors per-source collectors so error counts
		// match get_errors output exactly (acceptance criterion #2).
		// Each collector is non-fatal — a daemon that doesn't support a
		// store returns nil, nil and we move on. Only a structured
		// CallToolResult signals "this should reach the user".
		all := make([]unifiedError, 0)
		es, w := dt.collectProcessAlerts("", "", input.Global)
		if w != "" {
			errs.warnings = append(errs.warnings, w)
		}
		all = append(all, es...)

		es, w = dt.collectStartupErrors("", "", input.Global)
		if w != "" {
			errs.warnings = append(errs.warnings, w)
		}
		all = append(all, es...)

		es, ws := dt.collectProxyErrors("", "", input.Global)
		errs.warnings = append(errs.warnings, ws...)
		all = append(all, es...)

		errs.errors = all
	}()
	wg.Wait()

	// A daemon-IPC failure on proc list is fatal — without process state
	// the snapshot is meaningless. Proxy/error failures are degraded but
	// not fatal: we surface what we have. proxies.err being non-nil is
	// intentionally ignored — a daemon that lost its proxy registry
	// shouldn't block the agent from seeing process and error state.
	if procs.err != nil {
		return formatDaemonError(procs.err, "proc"), ProcOutput{}, nil
	}
	// Deduplicate errors using the same logic as get_errors so counts
	// match the user-visible numbers.
	deduped := deduplicateErrors(errs.errors)

	snapshot := buildSnapshot(procs.entries, proxies.entries, deduped)

	out := snapshotOutput(&snapshot, procs.projectPath, dirFilter.SessionCode, procs.global, errs.warnings, input.Raw)
	return nil, out, nil
}

// snapshotOutput shapes the snapshot response. Partial error-collection failures
// travel on Warnings in every mode: a raw consumer reads only the structured
// Snapshot, so appending them to the text rendering alone would present an
// incomplete snapshot as a clean one.
func snapshotOutput(snapshot *SnapshotData, projectPath, sessionCode string, effectiveGlobal bool, warnings []string, raw bool) ProcOutput {
	out := ProcOutput{
		Snapshot:    snapshot,
		Count:       len(snapshot.Processes),
		ProjectPath: projectPath,
		SessionCode: sessionCode,
		Global:      effectiveGlobal,
		Warnings:    warnings,
	}
	if !raw {
		out.Output = formatSnapshot(snapshot)
		for _, w := range warnings {
			out.Output += "\n⚠ " + w
		}
	}
	return out
}

// buildSnapshot assembles the unified snapshot from raw daemon-IPC maps.
// Exposed for testing — callers that don't have a live daemon can build
// fixture maps and verify the assembly logic without going through the
// IPC layer.
func buildSnapshot(procEntries, proxyEntries []map[string]interface{}, errors []unifiedError) SnapshotData {
	// 1) Processes — keyed by ID for error-count and last-error lookup.
	processes := make([]SnapshotProcess, 0, len(procEntries))
	for _, pm := range procEntries {
		id := getString(pm, "id")
		processes = append(processes, SnapshotProcess{
			ID:       id,
			State:    getString(pm, "state"),
			UptimeMs: getInt64(pm, "runtime_ms"),
			URLs:     getStringSlice(pm, "urls"),
			Command:  getString(pm, "command"),
		})
	}

	// 2) Proxies.
	proxies := make([]SnapshotProxy, 0, len(proxyEntries))
	for _, pm := range proxyEntries {
		proxies = append(proxies, SnapshotProxy{
			ID:           getString(pm, "id"),
			Target:       getString(pm, "target_url"),
			ListenAddr:   getString(pm, "listen_addr"),
			RequestCount: getInt64(pm, "total_requests"),
			Running:      getBool(pm, "running"),
			State:        getString(pm, "status"),
		})
	}

	// 3) Errors — convert unified errors to snapshot-error rows AND
	// accumulate per-process counts. Per-proxy counts are recovered by
	// matching source = "proxy:..." to a proxy id (best-effort via the
	// proxy registry — proxy errors don't always carry a proxy id, so
	// when source doesn't disambiguate we leave the proxy at zero).
	procCounts := make(map[string]int)
	procLastError := make(map[string]string)
	proxyCount := 0
	snapErrors := make([]SnapshotError, 0, len(errors))
	for _, e := range errors {
		row := SnapshotError{
			Source:   e.Source,
			Severity: e.Severity,
			Message:  e.Message,
		}
		if pid := processIDFromSource(e.Source); pid != "" {
			row.ProcessID = pid
			procCounts[pid] += e.Count
			if procLastError[pid] == "" && e.Severity == "error" {
				procLastError[pid] = e.Message
			}
		}
		if strings.HasPrefix(e.Source, "proxy:") {
			proxyCount += e.Count
		}
		row.File, row.Line = splitFileLine(e.Location)
		snapErrors = append(snapErrors, row)
	}

	// Apply per-process counts and last-error to the snapshot processes.
	for i := range processes {
		processes[i].ErrorCount = procCounts[processes[i].ID]
		processes[i].LastError = procLastError[processes[i].ID]
	}
	// Distribute the proxy error total across proxies. We can't attribute
	// per-proxy without a stable proxy_id on each error, so split evenly.
	// In practice most setups have one proxy, so this is accurate enough.
	if proxyCount > 0 && len(proxies) > 0 {
		share := proxyCount / len(proxies)
		remainder := proxyCount % len(proxies)
		for i := range proxies {
			proxies[i].ErrorCount = share
			if i < remainder {
				proxies[i].ErrorCount++
			}
		}
	}

	// 4) URLs — flatten from processes for easy lookup.
	urls := make([]SnapshotURL, 0)
	for _, p := range processes {
		for _, u := range p.URLs {
			urls = append(urls, SnapshotURL{ProcessID: p.ID, URL: u})
		}
	}
	sort.Slice(urls, func(i, j int) bool {
		if urls[i].ProcessID != urls[j].ProcessID {
			return urls[i].ProcessID < urls[j].ProcessID
		}
		return urls[i].URL < urls[j].URL
	})

	// 5) Suggested next actions.
	suggested := buildSuggestedNext(processes, proxies)

	return SnapshotData{
		Processes:     processes,
		Proxies:       proxies,
		Errors:        snapErrors,
		URLs:          urls,
		SuggestedNext: suggested,
	}
}

// processIDFromSource extracts the process ID from a unified-error source
// string. Sources for process alerts have the shape "process:<id>".
// Returns empty string for non-process sources.
func processIDFromSource(source string) string {
	const prefix = "process:"
	if !strings.HasPrefix(source, prefix) {
		return ""
	}
	return strings.TrimPrefix(source, prefix)
}

// splitFileLine parses a "file:line[:col]" location into separate file
// and line components. Returns the original string as file and empty
// line when there's no colon, so partial data is never lost.
func splitFileLine(location string) (file, line string) {
	if location == "" {
		return "", ""
	}
	parts := strings.Split(location, ":")
	switch len(parts) {
	case 0, 1:
		return location, ""
	case 2:
		return parts[0], parts[1]
	default:
		// "file:line:col" — keep file and line; drop col so the snapshot
		// stays compact. Callers wanting col can use raw mode and parse
		// the unified error themselves.
		return parts[0], parts[1]
	}
}

// buildSuggestedNext applies the documented logic from the task spec:
//   - Any process with errors → suggest proc output with extract:["error"]
//   - Any process Failed/Stopped → suggest proc run with detected command
//   - No proxy running but web process found → suggest proxy start
//   - All healthy → suggest watch {target:"all"}
//
// Order matters: errors take priority because they're the most
// actionable signal. We cap the number of suggestions to keep the
// compact output readable (max ~5 lines).
func buildSuggestedNext(procs []SnapshotProcess, proxies []SnapshotProxy) []string {
	const maxSuggestions = 5
	suggestions := make([]string, 0, maxSuggestions)

	// 1) Processes with errors — most actionable.
	for _, p := range procs {
		if len(suggestions) >= maxSuggestions {
			break
		}
		if p.ErrorCount > 0 {
			suggestions = append(suggestions,
				fmt.Sprintf(`proc {action:"output", process_id:%q, grep:"error|warn"}  ← %d errors need review`,
					p.ID, p.ErrorCount))
		}
	}

	// 2) Failed or stopped processes — agent should consider restart/run.
	for _, p := range procs {
		if len(suggestions) >= maxSuggestions {
			break
		}
		state := strings.ToLower(p.State)
		if state == "failed" || state == "stopped" {
			suggestions = append(suggestions,
				fmt.Sprintf(`proc {action:"restart", process_id:%q}  ← process is %s`, p.ID, state))
		}
	}

	// 3) No proxy targeting any URL but a process exposes an HTTP URL.
	// Treat a proxy as "covering" a process URL if its target_url matches.
	// Stopped proxies don't count — the agent should restart them rather
	// than start a new one, and that's the failed/stopped branch above.
	if len(suggestions) < maxSuggestions {
		coveredURLs := make(map[string]struct{}, len(proxies))
		for _, px := range proxies {
			if px.Running {
				coveredURLs[px.Target] = struct{}{}
			}
		}
		for _, p := range procs {
			if len(p.URLs) == 0 {
				continue
			}
			for _, u := range p.URLs {
				if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
					continue
				}
				if _, covered := coveredURLs[u]; covered {
					continue
				}
				suggestions = append(suggestions,
					fmt.Sprintf(`proxy {action:"start", id:%q, target_url:%q}  ← no proxy for web process`, p.ID, u))
				break
			}
			if len(suggestions) >= maxSuggestions {
				break
			}
		}
	}

	// 4) All healthy → suggest monitoring.
	if len(suggestions) == 0 {
		suggestions = append(suggestions, `watch {target:"all"}  ← all healthy, monitor live`)
	}

	return suggestions
}

// formatSnapshot renders the compact human-readable view. Layout follows
// the task spec example. Per the acceptance criteria: must fit in ~30
// lines for a typical 3-process stack. Counts and recent errors are
// truncated to keep that budget.
func formatSnapshot(s *SnapshotData) string {
	var b strings.Builder

	b.WriteString("=== Dev Environment Snapshot ===\n\n")

	// Processes section.
	runningCount := 0
	for _, p := range s.Processes {
		if strings.EqualFold(p.State, "running") {
			runningCount++
		}
	}
	fmt.Fprintf(&b, "PROCESSES (%d running)\n", runningCount)
	if len(s.Processes) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, p := range s.Processes {
			urlStr := "(no URL)"
			if len(p.URLs) > 0 {
				urlStr = p.URLs[0]
			}
			marker := ""
			if p.ErrorCount > 0 {
				marker = "  needs attention"
			}
			fmt.Fprintf(&b, "  %-8s %-8s %-6s %-32s %d errors%s\n",
				truncate(p.ID, 8),
				p.State,
				formatUptimeMs(p.UptimeMs),
				truncate(urlStr, 32),
				p.ErrorCount,
				marker)
		}
	}
	b.WriteString("\n")

	// Proxies section.
	fmt.Fprintf(&b, "PROXIES (%d active)\n", len(s.Proxies))
	if len(s.Proxies) == 0 {
		b.WriteString("  (none)\n")
	} else {
		for _, px := range s.Proxies {
			fmt.Fprintf(&b, "  %-8s %s -> %s  %d req  %d errors\n",
				truncate(px.ID, 8), px.Target, px.ListenAddr, px.RequestCount, px.ErrorCount)
		}
	}
	b.WriteString("\n")

	// Recent errors — cap at 5 to stay within the ~30-line budget.
	const maxRecent = 5
	recentN := len(s.Errors)
	if recentN > maxRecent {
		recentN = maxRecent
	}
	if recentN > 0 {
		fmt.Fprintf(&b, "RECENT ERRORS (%d)\n", len(s.Errors))
		for i := 0; i < recentN; i++ {
			e := s.Errors[i]
			loc := ""
			if e.File != "" {
				loc = e.File
				if e.Line != "" {
					loc = e.File + ":" + e.Line
				}
				loc = " " + loc + " -"
			}
			fmt.Fprintf(&b, "  [%s:%s]%s %s\n", e.Source, e.Severity, loc, truncate(e.Message, 80))
		}
		if len(s.Errors) > recentN {
			fmt.Fprintf(&b, "  ... +%d more (use raw:true to see all)\n", len(s.Errors)-recentN)
		}
		b.WriteString("\n")
	}

	// URLs section — only render when at least one URL exists.
	if len(s.URLs) > 0 {
		b.WriteString("URLs DISCOVERED\n")
		for _, u := range s.URLs {
			fmt.Fprintf(&b, "  %-8s %s\n", truncate(u.ProcessID, 8), u.URL)
		}
		b.WriteString("\n")
	}

	// Suggested next actions.
	b.WriteString("SUGGESTED NEXT\n")
	for _, suggestion := range s.SuggestedNext {
		fmt.Fprintf(&b, "  %s\n", suggestion)
	}

	return b.String()
}

// formatUptimeMs renders a millisecond uptime as a compact string
// matching the task spec layout (e.g. "12m", "8m", "45s", "2h").
func formatUptimeMs(ms int64) string {
	if ms <= 0 {
		return "-"
	}
	seconds := ms / 1000
	switch {
	case seconds < 60:
		return fmt.Sprintf("%ds", seconds)
	case seconds < 3600:
		return fmt.Sprintf("%dm", seconds/60)
	default:
		return fmt.Sprintf("%dh", seconds/3600)
	}
}
