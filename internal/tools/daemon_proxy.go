package tools

import (
	"context"

	"fmt"
	"net"

	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/go-sdk/mcp"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// proxyAccessURL renders the human-facing "access at" URL for a started proxy.
// listenAddr is a full host:port (net.Listener.Addr().String(), e.g.
// "127.0.0.1:47341"); a public URL (set explicitly, or by the tunnel tool via
// SetPublicURL) wins when present. Concatenating listenAddr onto a
// "http://localhost" literal produced the bogus "http://localhost127.0.0.1:47341"
// — the scheme is all the prefix needs.
func proxyAccessURL(listenAddr, bindAddress, publicURL string) string {
	switch {
	case publicURL != "":
		return publicURL
	case bindAddress == "0.0.0.0":
		// Bound on all interfaces: surface only the port and let the operator
		// substitute their reachable IP rather than echoing "0.0.0.0".
		port := listenAddr
		if _, p, err := net.SplitHostPort(listenAddr); err == nil {
			port = p
		}
		return fmt.Sprintf("http://<your-ip>:%s", port)
	default:
		return "http://" + listenAddr
	}
}

// makeProxyHandler creates a handler for the proxy tool.
func (dt *DaemonTools) makeProxyHandler() func(context.Context, *mcp.CallToolRequest, ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
		if err := validateProxyInput(input); err != nil {
			return fail[ProxyOutput](validationError("proxy", err))
		}

		if err := dt.ensureConnected(); err != nil {
			return fail[ProxyOutput](err.Error())
		}

		switch input.Action {
		case "start":
			return dt.handleProxyStart(input)
		case "stop":
			return dt.handleProxyStop(input)
		case "restart":
			return dt.handleProxyRestart(input)
		case "status":
			return dt.handleProxyStatus(input)
		case "list":
			return dt.handleProxyList(input)
		case "exec":
			return dt.handleProxyExec(input)
		case "navigate":
			return dt.handleProxyNavigate(input)
		case "resize":
			return dt.handleProxyResize(input)
		case "toast":
			return dt.handleProxyToast(input)
		case "chaos":
			return dt.handleProxyChaos(input)
		default:
			return fail[ProxyOutput](fmt.Sprintf("unknown action %q. Use: start, stop, restart, status, list, exec, navigate, resize, toast, chaos", input.Action))
		}
	}
}

func (dt *DaemonTools) handleProxyStart(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for start")
	}
	if input.TargetURL == "" {
		return fail[ProxyOutput]("target_url required for start")
	}

	cwd := getProjectPath()

	port := input.Port
	if port == 0 {
		port = -1
	}

	config := daemonclient.ProxyStartConfig{
		Path:          cwd,
		BindAddress:   input.BindAddress,
		AllowExternal: input.AllowExternal,
		PublicURL:     input.PublicURL,
		SkipTLSVerify: input.SkipTLSVerify,
	}

	result, err := dt.client.ProxyStartWithConfig(input.ID, input.TargetURL, port, input.MaxLogSize, config)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	listenAddr := getString(result, "listen_addr")
	bindAddress := getString(result, "bind_address")
	publicURL := getString(result, "public_url")

	accessURL := proxyAccessURL(listenAddr, bindAddress, publicURL)

	return nil, ProxyOutput{
		ID:          getString(result, "id"),
		TargetURL:   getString(result, "target_url"),
		ListenAddr:  listenAddr,
		BindAddress: bindAddress,
		PublicURL:   publicURL,
		Message:     fmt.Sprintf("Proxy started. Access at %s", accessURL),
	}, nil
}

func (dt *DaemonTools) handleProxyStop(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for stop")
	}

	err := dt.client.ProxyStop(input.ID)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	return nil, ProxyOutput{
		Success: true,
		Message: fmt.Sprintf("Proxy %s stopped", input.ID),
	}, nil
}

func (dt *DaemonTools) handleProxyRestart(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for restart")
	}

	result, err := dt.client.ProxyRestart(input.ID)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	return nil, ProxyOutput{
		ID:         getString(result, "id"),
		TargetURL:  getString(result, "target_url"),
		ListenAddr: getString(result, "listen_addr"),
		Success:    getBool(result, "success"),
		Message:    getString(result, "message"),
	}, nil
}

func (dt *DaemonTools) handleProxyStatus(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for status")
	}

	result, err := dt.client.ProxyStatus(input.ID)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	output := ProxyOutput{
		ID:            getString(result, "id"),
		TargetURL:     getString(result, "target_url"),
		ListenAddr:    getString(result, "listen_addr"),
		BindAddress:   getString(result, "bind_address"),
		PublicURL:     getString(result, "public_url"),
		Running:       getBool(result, "running"),
		State:         getString(result, "status"),
		WaitingOn:     getStringSlice(result, "waiting_for"),
		Uptime:        getString(result, "uptime"),
		TotalRequests: getInt64(result, "total_requests"),
	}

	// Fall back to the stats block for log-stats and readiness state
	// in case the server didn't surface them at the top level.
	if stats, ok := result["stats"].(map[string]interface{}); ok {
		if output.State == "" || output.State == "running" {
			if readyForForwarding, ok := stats["ready_for_forwarding"].(bool); ok && !readyForForwarding {
				output.State = "waiting_for_dependencies"
				output.WaitingOn = getStringSlice(stats, "waiting_for")
			}
		}
	}

	if logStats, ok := result["log_stats"].(map[string]interface{}); ok {
		output.LogStats = &LogStatsOutput{
			TotalEntries:     getInt64(logStats, "total_entries"),
			AvailableEntries: getInt64(logStats, "available_entries"),
			MaxSize:          getInt64(logStats, "max_size"),
			Dropped:          getInt64(logStats, "dropped"),
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProxyList(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {

	dirFilter := dt.scopeFilter(input.Global)

	result, err := dt.client.ProxyList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	output := ProxyOutput{
		Count:       getInt(result, "count"),
		ProjectPath: getString(result, "project_path"),
		SessionCode: getString(result, "session_code"),
		Global:      getBool(result, "global"),
	}

	if proxies, ok := result["proxies"].([]interface{}); ok {
		for _, p := range proxies {
			if pm, ok := p.(map[string]interface{}); ok {
				output.Proxies = append(output.Proxies, ProxyEntry{
					ID:            getString(pm, "id"),
					TargetURL:     getString(pm, "target_url"),
					ListenAddr:    getString(pm, "listen_addr"),
					BindAddress:   getString(pm, "bind_address"),
					PublicURL:     getString(pm, "public_url"),
					Path:          getString(pm, "path"),
					Running:       getBool(pm, "running"),
					State:         getString(pm, "status"),
					WaitingOn:     getStringSlice(pm, "waiting_for"),
					Uptime:        getString(pm, "uptime"),
					TotalRequests: getInt64(pm, "total_requests"),
				})
			}
		}
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleProxyExec(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {

	if input.Help {
		return nil, ProxyOutput{
			Success: true,
			Message: GetAPIOverview(),
		}, nil
	}

	if input.Describe != "" {
		doc, found := GetFunctionDescription(input.Describe)
		if !found {

			names := ListFunctionNames()
			return nil, ProxyOutput{
				Success: false,
				Message: fmt.Sprintf("Function %q not found.\n\nAvailable functions:\n%v", input.Describe, names),
			}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: doc,
		}, nil
	}

	// Handle search request - no proxy ID required. Mutually exclusive
	// with code execution.
	if res, out, handled := handleExecSearch(input); handled {
		return res, out, nil
	}

	if input.ID == "" {
		return fail[ProxyOutput]("id required for exec")
	}
	if input.Code == "" {
		return fail[ProxyOutput]("code required for exec")
	}

	// Scan for anti-pattern hints before execution (advisory only, never blocks).
	// Default enabled; set hints: false to opt out.
	var execHints []string
	if input.Hints == nil || *input.Hints {
		execHints = ScanForHints(input.Code)
	}

	execTarget, err := resolveExecTarget(input.Target, input.FrameID)
	if err != nil {
		return fail[ProxyOutput](err.Error())
	}

	result, err := dt.client.ProxyExec(input.ID, input.Code, execTarget)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	success := getBool(result, "success")
	execID := getString(result, "execution_id")

	if !success {
		errorMsg := getString(result, "error")
		return nil, ProxyOutput{
			Success:     false,
			ExecutionID: execID,
			Message:     fmt.Sprintf("JavaScript execution failed: %s", errorMsg),
		}, nil
	}

	resultVal := getString(result, "result")
	duration := getString(result, "duration")
	filePath := getString(result, "file_path")

	if filePath != "" {
		return nil, ProxyOutput{
			Success:     true,
			ExecutionID: execID,
			ExecHints:   execHints,
			Message: fmt.Sprintf(`JavaScript executed successfully.
Result: Large response saved to file
File: %s
Duration: %s

Use the Read tool to view the full result.`, filePath, duration),
		}, nil
	}

	return nil, ProxyOutput{
		Success:     true,
		ExecutionID: execID,
		ExecHints:   execHints,
		Message:     fmt.Sprintf("JavaScript executed successfully.\nResult: %s\nDuration: %s", resultVal, duration),
	}, nil
}

// handleProxyNavigate drives the active content frame (back/forward/reload/goto).
func (dt *DaemonTools) handleProxyNavigate(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for navigate")
	}
	code, err := buildNavigateJS(input.Direction, input.TargetURL)
	if err != nil {
		return fail[ProxyOutput](err.Error())
	}
	execTarget, err := resolveExecTarget(input.Target, "")
	if err != nil {
		return fail[ProxyOutput](err.Error())
	}
	// Navigation drives the page content frame. Navigating the outer chrome
	// shell (the proxy UI runtime) is never the intent and would blow away the
	// shell that hosts the page — fail loud rather than do it.
	if execTarget == "@chrome" {
		return fail[ProxyOutput]("navigate cannot target the outer chrome shell; it drives the page content frame (omit target, or use target:\"inner\")")
	}
	result, err := dt.client.ProxyExec(input.ID, code, execTarget)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}
	// buildNavigateJS returns {navigating:true, from:<href>} synchronously
	// before the deferred navigation runs; surface it instead of discarding it.
	return nil, ProxyOutput{
		Success: true,
		Message: fmt.Sprintf("navigate %s dispatched: %s", input.Direction, getString(result, "result")),
	}, nil
}

// handleProxyResize resizes the live content frame from the outer chrome shell.
func (dt *DaemonTools) handleProxyResize(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for resize")
	}
	result, err := dt.client.ProxyExec(input.ID, buildResizeJS(input.Width, input.Height), "@chrome")
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}
	// Mirror handleProxyExec: a browser-side JS failure is a structured result
	// carrying success=false + the error, not a tool-level IsError. Only a
	// transport error (above) is IsError. This keeps resize and exec consistent
	// so callers branch on the same shape.
	if !getBool(result, "success") {
		return nil, ProxyOutput{
			Success: false,
			Message: fmt.Sprintf("resize failed: %s", getString(result, "error")),
		}, nil
	}
	return nil, ProxyOutput{Success: true, Message: getString(result, "result")}, nil
}

func (dt *DaemonTools) handleProxyToast(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for toast")
	}
	if input.ToastMessage == "" {
		return fail[ProxyOutput]("toast_message required for toast")
	}

	toastConfig := protocol.ToastConfig{
		Type:     input.ToastType,
		Title:    input.ToastTitle,
		Message:  input.ToastMessage,
		Duration: input.ToastDuration,
	}

	if toastConfig.Type == "" {
		toastConfig.Type = "info"
	}

	result, err := dt.client.ProxyToast(input.ID, toastConfig)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	sentCount := getInt(result, "sent_count")

	return nil, ProxyOutput{
		Success: getBool(result, "success"),
		Message: fmt.Sprintf("Toast sent to %d connected client(s)", sentCount),
	}, nil
}

func (dt *DaemonTools) handleProxyChaos(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return fail[ProxyOutput]("id required for chaos")
	}

	operation := input.ChaosOperation
	if operation == "" {
		operation = "status"
	}

	switch operation {
	case "enable":
		result, err := dt.client.ChaosEnable(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos injection enabled",
		}, nil

	case "disable":
		result, err := dt.client.ChaosDisable(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos injection disabled",
		}, nil

	case "status":
		result, err := dt.client.ChaosStatus(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{
			ChaosEnabled: getBool(result, "enabled"),
		}
		if stats, ok := result["stats"].(map[string]interface{}); ok {
			output.ChaosStats = parseChaosStats(stats)
		}
		if rules, ok := result["rules"].([]interface{}); ok {
			output.ChaosRules = parseChaosRules(rules)
		}
		return nil, output, nil

	case "preset":
		if input.ChaosPreset == "" {

			result, err := dt.client.ChaosListPresets()
			if err != nil {
				return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
			}
			if presets, ok := result["presets"].([]interface{}); ok {
				output := ProxyOutput{ChaosPresets: make([]string, 0, len(presets))}
				for _, p := range presets {
					if s, ok := p.(string); ok {
						output.ChaosPresets = append(output.ChaosPresets, s)
					}
				}
				return nil, output, nil
			}
			return nil, ProxyOutput{}, nil
		}

		result, err := dt.client.ChaosPreset(input.ID, input.ChaosPreset)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      fmt.Sprintf("Chaos preset %q applied", input.ChaosPreset),
		}, nil

	case "set":
		if input.ChaosConfig == nil {
			return fail[ProxyOutput]("chaos_config required for set operation")
		}
		config := protocol.ChaosConfigPayload{
			Enabled:     input.ChaosConfig.Enabled,
			GlobalOdds:  input.ChaosConfig.GlobalOdds,
			Seed:        input.ChaosConfig.Seed,
			LoggingMode: input.ChaosConfig.LoggingMode,
		}
		for _, r := range input.ChaosConfig.Rules {
			rule := inputRuleToProtocol(r)
			config.Rules = append(config.Rules, &rule)
		}
		result, err := dt.client.ChaosSet(input.ID, config)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: getBool(result, "enabled"),
			Message:      "Chaos configuration applied",
		}, nil

	case "add_rule":
		if input.ChaosRule == nil {
			return fail[ProxyOutput]("chaos_rule required for add_rule operation")
		}
		rule := inputRuleToProtocol(*input.ChaosRule)
		result, err := dt.client.ChaosAddRule(input.ID, rule)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: fmt.Sprintf("Rule %q added", getString(result, "rule_id")),
		}, nil

	case "remove_rule":
		if input.ChaosRuleID == "" {
			return fail[ProxyOutput]("chaos_rule_id required for remove_rule operation")
		}
		_, err := dt.client.ChaosRemoveRule(input.ID, input.ChaosRuleID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success: true,
			Message: fmt.Sprintf("Rule %q removed", input.ChaosRuleID),
		}, nil

	case "list_rules":
		result, err := dt.client.ChaosListRules(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{}
		if rules, ok := result["rules"].([]interface{}); ok {
			output.ChaosRules = parseChaosRules(rules)
		}
		return nil, output, nil

	case "stats":
		result, err := dt.client.ChaosStats(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		output := ProxyOutput{}
		if stats, ok := result["stats"].(map[string]interface{}); ok {
			output.ChaosStats = parseChaosStats(stats)
		}
		return nil, output, nil

	case "clear":
		_, err := dt.client.ChaosClear(input.ID)
		if err != nil {
			return formatDaemonError(err, "chaos"), ProxyOutput{}, nil
		}
		return nil, ProxyOutput{
			Success:      true,
			ChaosEnabled: false,
			Message:      "Chaos configuration cleared",
		}, nil

	default:
		return fail[ProxyOutput](fmt.Sprintf("unknown chaos operation %q. Use: enable, disable, status, preset, set, add_rule, remove_rule, list_rules, stats, clear", operation))
	}
}

// makeProxyLogHandler creates a handler for the proxylog tool.
func (dt *DaemonTools) makeProxyLogHandler() func(context.Context, *mcp.CallToolRequest, ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
		if err := validateProxyLogInput(input); err != nil {
			return fail[ProxyLogOutput](validationError("proxylog", err))
		}

		if err := dt.ensureConnected(); err != nil {
			return fail[ProxyLogOutput](err.Error())
		}

		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.ProxyID == "" {
			return fail[ProxyLogOutput]("proxy_id required (or `id` alias)")
		}

		action := input.Action
		if action == "" {
			action = "query"
		}

		switch action {
		case "query":
			return dt.handleProxyLogQuery(input)
		case "summary":
			return dt.handleProxyLogSummary(input)
		case "clear":
			return dt.handleProxyLogClear(input)
		case "stats":
			return dt.handleProxyLogStats(input)
		default:
			return fail[ProxyLogOutput](fmt.Sprintf("unknown action %q. Use: query, summary, clear, stats", action))
		}
	}
}

func (dt *DaemonTools) handleProxyLogQuery(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	filter := protocol.LogQueryFilter{
		Types:            input.Types,
		Methods:          input.Methods,
		URLPattern:       input.URLPattern,
		StatusCodes:      input.StatusCodes,
		Since:            input.Since,
		Until:            input.Until,
		Limit:            input.Limit,
		ErrorsOnly:       input.ErrorsOnly,
		DiagnosticLevels: input.DiagnosticLevels,
		InteractionTypes: input.InteractionTypes,
		MutationTypes:    input.MutationTypes,
		Frames:           input.Frames,
		MessagePattern:   input.MessagePattern,
		MinDurationMs:    input.MinDurationMs,
	}

	entries, totalAvailable, dropped, err := dt.client.ProxyLogQueryFull(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	pag := NewPagination(len(entries), int(totalAvailable), limit, input.hasFilters())
	pag.Dropped = int(dropped)

	// Honor `raw`: full JSON dumps when requested, terse one-line compact
	// rendering by default (the token-efficient view). Both formatters are
	// shared with — and identical to — the rendering MCP callers expect.
	if input.Raw {
		return handleProxyLogQueryRaw(entries, &pag)
	}
	return handleProxyLogQueryCompact(entries, &pag)
}

func (dt *DaemonTools) handleProxyLogSummary(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {

	filter := protocol.LogQueryFilter{
		Types:            input.Types,
		Methods:          input.Methods,
		URLPattern:       input.URLPattern,
		StatusCodes:      input.StatusCodes,
		Since:            input.Since,
		Until:            input.Until,
		Limit:            0,
		ErrorsOnly:       input.ErrorsOnly,
		DiagnosticLevels: input.DiagnosticLevels,
		InteractionTypes: input.InteractionTypes,
		MutationTypes:    input.MutationTypes,
		Frames:           input.Frames,
		MessagePattern:   input.MessagePattern,
		MinDurationMs:    input.MinDurationMs,
		// The summary aggregates counts and never reads HTTP bodies/headers, so
		// ask the daemon to drop them before shipping the (Limit:0) full result.
		OmitBodies: true,
	}

	result, err := dt.client.ProxyLogQuery(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	// Distinguish "no logs" (entries present but null/empty) from a malformed
	// daemon response (key absent or wrong type). Silently substituting an empty
	// slice for a decode failure would report a falsely-empty summary and hide a
	// wire/protocol break from the agent.
	raw, present := result["entries"]
	if !present {
		return fail[ProxyLogOutput]("proxylog summary: daemon response missing 'entries' field")
	}
	var entries []interface{}
	if raw != nil {
		arr, ok := raw.([]interface{})
		if !ok {
			return fail[ProxyLogOutput](fmt.Sprintf("proxylog summary: 'entries' is %T, expected an array", raw))
		}
		entries = arr
	}

	detailSet := make(map[string]bool)
	for _, d := range input.Detail {
		detailSet[d] = true
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	summary := buildProxyLogSummary(entries, detailSet, limit)

	return nil, ProxyLogOutput{
		Summary: &summary,
	}, nil
}

func (dt *DaemonTools) handleProxyLogClear(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	err := dt.client.ProxyLogClear(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	return nil, ProxyLogOutput{
		Success: true,
		Message: fmt.Sprintf("Logs cleared for proxy %s", input.ProxyID),
	}, nil
}

func (dt *DaemonTools) handleProxyLogStats(input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	result, err := dt.client.ProxyLogStats(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	return nil, ProxyLogOutput{
		Stats: &LogStatsOutput{
			TotalEntries:     getInt64(result, "total_entries"),
			AvailableEntries: getInt64(result, "available_entries"),
			MaxSize:          getInt64(result, "max_size"),
			Dropped:          getInt64(result, "dropped"),
		},
	}, nil
}
