package tools

import (
	"context"

	"fmt"

	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"
)

// makeCurrentPageHandler creates a handler for the currentpage tool.
func (dt *DaemonTools) makeCurrentPageHandler() func(context.Context, *mcp.CallToolRequest, CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
		if err := validateCurrentPageInput(input); err != nil {
			return errorResult(validationError("currentpage", err)), CurrentPageOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), CurrentPageOutput{}, nil
		}

		if input.ProxyID == "" {

			proxies, listErr := dt.client.ProxyList(protocol.DirectoryFilter{Global: true})
			if listErr == nil {
				if proxyList, ok := proxies["proxies"].([]interface{}); ok && len(proxyList) > 0 {
					var ids []string
					for _, p := range proxyList {
						if pm, ok := p.(map[string]interface{}); ok {
							if id, ok := pm["id"].(string); ok {
								ids = append(ids, id)
							}
						}
					}
					return errorResult(fmt.Sprintf("proxy_id required. Running proxies: %s\nExample: currentpage {proxy_id: %q}", strings.Join(ids, ", "), ids[0])), CurrentPageOutput{}, nil
				}
			}
			return errorResult("proxy_id required. No proxies are running. Start one with: proxy {action: \"start\", id: \"dev\", target_url: \"http://localhost:3000\"}"), CurrentPageOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "triage"
		}

		switch action {
		case "triage":
			return dt.handleCurrentPageTriage(input)
		case "list":
			return dt.handleCurrentPageList(input)
		case "get":
			return dt.handleCurrentPageGet(input)
		case "summary":
			return dt.handleCurrentPageSummary(input)
		case "clear":
			return dt.handleCurrentPageClear(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: triage, list, get, summary, clear", action)), CurrentPageOutput{}, nil
		}
	}
}

func (dt *DaemonTools) handleCurrentPageList(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	result, err := dt.client.CurrentPageList(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	output := CurrentPageOutput{
		Count: getInt(result, "count"),
	}

	if sessions, ok := result["sessions"].([]interface{}); ok {
		for _, s := range sessions {
			if sm, ok := s.(map[string]interface{}); ok {
				output.Sessions = append(output.Sessions, convertToPageSessionOutput(sm))
			}
		}
	}

	if output.Count == 0 {

		proxyStatus, statusErr := dt.client.ProxyStatus(input.ProxyID)
		listenAddr := ""
		if statusErr == nil {
			if addr, ok := proxyStatus["listen_addr"].(string); ok {
				listenAddr = addr
			}
		}
		hint := fmt.Sprintf("No page sessions found for proxy %q. ", input.ProxyID)
		if listenAddr != "" {
			hint += fmt.Sprintf("Open http://%s in a browser to start capturing page data. ", normalizeAddr(listenAddr))
		}
		hint += "Page sessions are created when a browser loads an HTML page through the proxy."
		output.Hint = hint
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleCurrentPageGet(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for get"), CurrentPageOutput{}, nil
	}

	result, err := dt.client.CurrentPageGet(input.ProxyID, input.SessionID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	session := convertToPageSessionOutput(result)
	// Honor `raw`: the daemon always returns the full session (arrays and all),
	// but the default compact view exposes only counts to avoid token bloat.
	// Drop the detail arrays unless the caller explicitly asked for them.
	if !input.Raw {
		session.Resources = nil
		session.Errors = nil
		session.Interactions = nil
		session.Mutations = nil
	}
	return nil, CurrentPageOutput{
		Session: &session,
	}, nil
}

func (dt *DaemonTools) handleCurrentPageSummary(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for summary"), CurrentPageOutput{}, nil
	}

	result, err := dt.client.CurrentPageGet(input.ProxyID, input.SessionID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	detailSet := make(map[string]bool)
	for _, d := range input.Detail {
		detailSet[d] = true
	}

	limit := input.Limit
	if limit <= 0 {
		limit = 5
	}
	if limit > 100 {
		limit = 100
	}

	summary := convertToPageSummary(result, detailSet, limit)
	return nil, CurrentPageOutput{
		Summary: &summary,
	}, nil
}

// handleCurrentPageTriage returns the at-a-glance triage view for the page the
// developer currently has open. With no session_id it auto-selects the
// most-recently-active session ("the screen on screen"); deeper detail is one
// hop away via the next_tools it returns.
func (dt *DaemonTools) handleCurrentPageTriage(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	sessionID := input.SessionID
	if sessionID == "" {
		list, err := dt.client.CurrentPageList(input.ProxyID)
		if err != nil {
			return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
		}
		sessionID = pickActiveSessionID(list)
		if sessionID == "" {
			return nil, CurrentPageOutput{
				Hint: fmt.Sprintf("No page sessions for proxy %q yet. Open the app through the proxy in a browser, then re-run triage.", input.ProxyID),
			}, nil
		}
	}

	result, err := dt.client.CurrentPageGet(input.ProxyID, sessionID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	triage := convertToPageTriage(result)
	return nil, CurrentPageOutput{Triage: &triage}, nil
}

// pickActiveSessionID chooses the most-recently-active session from a LIST
// result, preferring active sessions, then latest last_activity.
func pickActiveSessionID(list map[string]interface{}) string {
	sessions, ok := list["sessions"].([]interface{})
	if !ok {
		return ""
	}
	var bestID string
	var bestActive bool
	var bestTime time.Time
	for _, s := range sessions {
		sm, ok := s.(map[string]interface{})
		if !ok {
			continue
		}
		id := getString(sm, "id")
		if id == "" {
			continue
		}
		active := getBool(sm, "active")
		var t time.Time
		if ts := getString(sm, "last_activity"); ts != "" {
			parsed, err := time.Parse(time.RFC3339Nano, ts)
			if err != nil {
				// Best-effort tiebreak: a malformed timestamp leaves t at the
				// zero value (treated as oldest), so this session simply won't
				// be preferred as "newest". Log so a serialization regression
				// upstream is diagnosable.
				debug.Log("currentpage", "best session: unparseable last_activity %q: %v", ts, err)
			} else {
				t = parsed
			}
		}
		// Prefer active over inactive; within the same active-ness, prefer newer.
		better := bestID == "" ||
			(active && !bestActive) ||
			(active == bestActive && t.After(bestTime))
		if better {
			bestID, bestActive, bestTime = id, active, t
		}
	}
	return bestID
}

func (dt *DaemonTools) handleCurrentPageClear(input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	err := dt.client.CurrentPageClear(input.ProxyID)
	if err != nil {
		return formatDaemonError(err, "currentpage"), CurrentPageOutput{}, nil
	}

	return nil, CurrentPageOutput{
		Success: true,
		Message: fmt.Sprintf("Page sessions cleared for proxy %s", input.ProxyID),
	}, nil
}
