package tools

import (
	"context"

	"fmt"
	"net"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/go-sdk/mcp"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// proxyAccessURL renders the human-facing "access at" URL for a started proxy.
// listenAddr is a full host:port (net.Listener.Addr().String(), e.g.
// "127.0.0.1:47341"); a tunnel or public URL wins when present. Concatenating
// listenAddr onto a "http://localhost" literal produced the bogus
// "http://localhost127.0.0.1:47341" — the scheme is all the prefix needs.
func proxyAccessURL(listenAddr, bindAddress, publicURL, tunnelURL string) string {
	switch {
	case tunnelURL != "":
		return tunnelURL
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
			return errorResult(validationError("proxy", err)), ProxyOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProxyOutput{}, nil
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
			return errorResult(fmt.Sprintf("unknown action %q. Use: start, stop, restart, status, list, exec, navigate, resize, toast, chaos", input.Action)), ProxyOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleProxyStart(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for start"), ProxyOutput{}, nil
	}
	if input.TargetURL == "" {
		return errorResult("target_url required for start"), ProxyOutput{}, nil
	}

	cwd := getProjectPath()

	port := input.Port
	if port == 0 {
		port = -1
	}

	config := daemon.ProxyStartConfig{
		Path:          cwd,
		BindAddress:   input.BindAddress,
		AllowExternal: input.AllowExternal,
		PublicURL:     input.PublicURL,
		SkipTLSVerify: input.SkipTLSVerify,
	}

	if input.Tunnel != "" {
		config.Tunnel = &protocol.TunnelConfig{
			Provider:  input.Tunnel,
			Command:   input.TunnelCommand,
			Args:      input.TunnelArgs,
			AuthToken: input.TunnelToken,
			Region:    input.TunnelRegion,
		}
	}

	result, err := dt.client.ProxyStartWithConfig(input.ID, input.TargetURL, port, input.MaxLogSize, config)
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}

	listenAddr := getString(result, "listen_addr")
	bindAddress := getString(result, "bind_address")
	publicURL := getString(result, "public_url")
	tunnelURL := getString(result, "tunnel_url")

	accessURL := proxyAccessURL(listenAddr, bindAddress, publicURL, tunnelURL)

	return nil, ProxyOutput{
		ID:          getString(result, "id"),
		TargetURL:   getString(result, "target_url"),
		ListenAddr:  listenAddr,
		BindAddress: bindAddress,
		PublicURL:   publicURL,
		TunnelURL:   tunnelURL,
		Message:     fmt.Sprintf("Proxy started. Access at %s", accessURL),
	}, nil
}

func (dt *DaemonTools) handleProxyStop(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for stop"), ProxyOutput{}, nil
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
		return errorResult("id required for restart"), ProxyOutput{}, nil
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
		return errorResult("id required for status"), ProxyOutput{}, nil
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

	dirFilter := protocol.DirectoryFilter{
		GlobalOverride: input.Global,
	}

	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else {

		projectPath := getProjectPath()
		if projectPath != "" {
			dirFilter.Directory = projectPath
		}
	}

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
		return errorResult("id required for exec"), ProxyOutput{}, nil
	}
	if input.Code == "" {
		return errorResult("code required for exec"), ProxyOutput{}, nil
	}

	// Scan for anti-pattern hints before execution (advisory only, never blocks).
	// Default enabled; set hints: false to opt out.
	var execHints []string
	if input.Hints == nil || *input.Hints {
		execHints = ScanForHints(input.Code)
	}

	result, err := dt.client.ProxyExec(input.ID, input.Code, resolveExecTarget(input.Target, input.FrameID))
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
		return errorResult("id required for navigate"), ProxyOutput{}, nil
	}
	code, err := buildNavigateJS(input.Direction, input.TargetURL)
	if err != nil {
		return errorResult(err.Error()), ProxyOutput{}, nil
	}
	if _, err := dt.client.ProxyExec(input.ID, code, resolveExecTarget(input.Target, "")); err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}
	return nil, ProxyOutput{Success: true, Message: fmt.Sprintf("navigate %s dispatched", input.Direction)}, nil
}

// handleProxyResize resizes the live content frame from the outer chrome shell.
func (dt *DaemonTools) handleProxyResize(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for resize"), ProxyOutput{}, nil
	}
	result, err := dt.client.ProxyExec(input.ID, buildResizeJS(input.Width, input.Height), "@chrome")
	if err != nil {
		return formatDaemonError(err, "proxy"), ProxyOutput{}, nil
	}
	if !getBool(result, "success") {
		return errorResult(fmt.Sprintf("resize failed: %s", getString(result, "error"))), ProxyOutput{}, nil
	}
	return nil, ProxyOutput{Success: true, Message: getString(result, "result")}, nil
}

func (dt *DaemonTools) handleProxyToast(input ProxyInput) (*mcp.CallToolResult, ProxyOutput, error) {
	if input.ID == "" {
		return errorResult("id required for toast"), ProxyOutput{}, nil
	}
	if input.ToastMessage == "" {
		return errorResult("toast_message required for toast"), ProxyOutput{}, nil
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
		return errorResult("id required for chaos"), ProxyOutput{}, nil
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
			return errorResult("chaos_config required for set operation"), ProxyOutput{}, nil
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
			return errorResult("chaos_rule required for add_rule operation"), ProxyOutput{}, nil
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
			return errorResult("chaos_rule_id required for remove_rule operation"), ProxyOutput{}, nil
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
		return errorResult(fmt.Sprintf("unknown chaos operation %q. Use: enable, disable, status, preset, set, add_rule, remove_rule, list_rules, stats, clear", operation)), ProxyOutput{}, nil
	}
}

// makeProxyLogHandler creates a handler for the proxylog tool.
func (dt *DaemonTools) makeProxyLogHandler() func(context.Context, *mcp.CallToolRequest, ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ProxyLogInput) (*mcp.CallToolResult, ProxyLogOutput, error) {
		if err := validateProxyLogInput(input); err != nil {
			return errorResult(validationError("proxylog", err)), ProxyLogOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ProxyLogOutput{}, nil
		}

		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.ProxyID == "" {
			return errorResult("proxy_id required (or `id` alias)"), ProxyLogOutput{}, nil
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
			return errorResult(fmt.Sprintf("unknown action %q. Use: query, summary, clear, stats", action)), ProxyLogOutput{}, nil
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
	}

	entries, totalAvailable, err := dt.client.ProxyLogQueryFull(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	limit := input.Limit
	if limit == 0 {
		limit = 100
	}
	pag := NewPagination(len(entries), int(totalAvailable), limit, input.hasFilters())

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
	}

	result, err := dt.client.ProxyLogQuery(input.ProxyID, filter)
	if err != nil {
		return formatDaemonError(err, "proxylog"), ProxyLogOutput{}, nil
	}

	entries, ok := result["entries"].([]interface{})
	if !ok {
		entries = []interface{}{}
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
