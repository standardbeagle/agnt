package tools

import (
	"context"

	"fmt"

	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/standardbeagle/agnt/internal/protocol"
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
			action = "list"
		}

		switch action {
		case "list":
			return dt.handleCurrentPageList(input)
		case "get":
			return dt.handleCurrentPageGet(input)
		case "summary":
			return dt.handleCurrentPageSummary(input)
		case "clear":
			return dt.handleCurrentPageClear(input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: list, get, summary, clear", action)), CurrentPageOutput{}, nil
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
