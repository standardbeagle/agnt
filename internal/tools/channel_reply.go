package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/go-sdk/mcp"
)

// ChannelReplyInput is the input for the channel_reply tool.
type ChannelReplyInput struct {
	Content  string `json:"content" jsonschema:"required,Message body to send to the developer (markdown OK)"`
	ProxyID  string `json:"proxy_id,omitempty" jsonschema:"Target a specific proxy; omit to fan out to all active proxies"`
	Severity string `json:"severity,omitempty" jsonschema:"enum=info,enum=warning,enum=error,Toast styling (default: info)"`
	Title    string `json:"title,omitempty" jsonschema:"Toast title"`
}

// ChannelReplyOutput is the output for the channel_reply tool.
type ChannelReplyOutput struct {
	Delivered int    `json:"delivered"`
	Message   string `json:"message"`
}

// RegisterChannelReplyTool registers the channel_reply MCP tool.
// The tool is only registered when channel is enabled and reply-tool is on.
// dt must be non-nil.
func RegisterChannelReplyTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "channel_reply",
		Description: `Send a message to the developer via the browser overlay. Use this to ask questions, confirm intent, report progress, or surface anything that needs visual review.

Examples:
  channel_reply {content: "Build succeeded, opening preview..."}
  channel_reply {content: "Which layout do you prefer?", title: "Choose layout", proxy_id: "dev"}
  channel_reply {content: "Found 3 type errors", severity: "warning"}`,
	}, dt.makeChannelReplyHandler())
}

// makeChannelReplyHandler creates a handler for the channel_reply tool.
func (dt *DaemonTools) makeChannelReplyHandler() func(context.Context, *mcp.CallToolRequest, ChannelReplyInput) (*mcp.CallToolResult, ChannelReplyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ChannelReplyInput) (*mcp.CallToolResult, ChannelReplyOutput, error) {
		if input.Content == "" {
			return errorResult("content is required"), ChannelReplyOutput{}, nil
		}

		severity := input.Severity
		if severity == "" {
			severity = "info"
		}
		if severity != "info" && severity != "warning" && severity != "error" {
			return errorResult(fmt.Sprintf("invalid severity %q: must be info, warning, or error", severity)), ChannelReplyOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ChannelReplyOutput{}, nil
		}

		toastConfig := protocol.ToastConfig{
			Type:    severity,
			Title:   input.Title,
			Message: input.Content,
		}

		// Specific proxy targeting.
		if input.ProxyID != "" {
			_, err := dt.client.ProxyToast(input.ProxyID, toastConfig)
			if err != nil {
				return errorResult(fmt.Sprintf("proxy %q not found or unavailable: %v", input.ProxyID, err)), ChannelReplyOutput{}, nil
			}
			return nil, ChannelReplyOutput{
				Delivered: 1,
				Message:   fmt.Sprintf("Message delivered to proxy %q", input.ProxyID),
			}, nil
		}

		// Fan-out: list proxies for the current session/project directory.
		dirFilter := buildDirFilter(dt)

		listResult, err := dt.client.ProxyList(dirFilter)
		if err != nil {
			return errorResult(fmt.Sprintf("failed to list proxies: %v", err)), ChannelReplyOutput{}, nil
		}

		proxyIDs := extractProxyIDs(listResult)
		if len(proxyIDs) == 0 {
			return errorResult("no active proxies to deliver message to"), ChannelReplyOutput{}, nil
		}

		delivered := 0
		for _, pid := range proxyIDs {
			_, err := dt.client.ProxyToast(pid, toastConfig)
			if err == nil {
				delivered++
			}
		}

		return nil, ChannelReplyOutput{
			Delivered: delivered,
			Message:   fmt.Sprintf("Message delivered to %d/%d proxies", delivered, len(proxyIDs)),
		}, nil
	}
}

// buildDirFilter creates a DirectoryFilter scoped to the current session or project.
func buildDirFilter(dt *DaemonTools) protocol.DirectoryFilter {
	dirFilter := protocol.DirectoryFilter{}
	if sessionCode := dt.SessionCode(); sessionCode != "" {
		dirFilter.SessionCode = sessionCode
	} else if p := getProjectPath(); p != "" {
		dirFilter.Directory = p
	}
	return dirFilter
}

// extractProxyIDs pulls proxy IDs from a ProxyList daemon response.
func extractProxyIDs(result map[string]interface{}) []string {
	proxies, ok := result["proxies"].([]interface{})
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(proxies))
	for _, p := range proxies {
		if pm, ok := p.(map[string]interface{}); ok {
			if id := getString(pm, "id"); id != "" {
				ids = append(ids, id)
			}
		}
	}
	return ids
}
