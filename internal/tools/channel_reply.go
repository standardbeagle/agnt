package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/standardbeagle/agnt/internal/store"
	"github.com/standardbeagle/go-sdk/mcp"
)

// ChannelReplyInput is the input for the channel_reply tool.
type ChannelReplyInput struct {
	Content  string `json:"content" jsonschema:"Message body to send to the developer (markdown OK)"`
	ProxyID  string `json:"proxy_id,omitempty" jsonschema:"Target a specific proxy (preferred); omit to fan out to all active proxies"`
	ID       string `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Severity string `json:"severity,omitempty" jsonschema:"Toast styling: one of info, success, warning, error (default: info)"`
	Title    string `json:"title,omitempty" jsonschema:"Toast title"`
	Input    string `json:"input,omitempty" jsonschema:"Set to 'secret' to render a masked password field on the toast; the submitted value goes straight to the daemon store and NEVER enters the channel/event stream"`
	Name     string `json:"name,omitempty" jsonschema:"Env-var-style secret name (e.g. FIGMA_KEY); required when input is 'secret'. Reference the secret by this name afterwards"`
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
  channel_reply {content: "Found 3 type errors", severity: "warning"}
  channel_reply {content: "Paste your Figma API key", title: "Secret needed", input: "secret", name: "FIGMA_KEY"}

Secret entry (input: "secret", name: ENV_STYLE_NAME): renders a masked
password field. The submitted value goes directly to the daemon store and
never enters the channel event stream — you will receive a panel_message
with the name and last-4 fingerprint only. Reference the secret by NAME;
agnt injects it as an env var into managed processes at spawn.`,
	}, dt.makeChannelReplyHandler())
}

// makeChannelReplyHandler creates a handler for the channel_reply tool.
func (dt *DaemonTools) makeChannelReplyHandler() func(context.Context, *mcp.CallToolRequest, ChannelReplyInput) (*mcp.CallToolResult, ChannelReplyOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input ChannelReplyInput) (*mcp.CallToolResult, ChannelReplyOutput, error) {
		input.ProxyID = pickProxyID(input.ID, input.ProxyID)
		if input.Content == "" {
			return errorResult("content is required"), ChannelReplyOutput{}, nil
		}

		severity := input.Severity
		if severity == "" {
			severity = "info"
		}
		if severity != "info" && severity != "success" && severity != "warning" && severity != "error" {
			return errorResult(fmt.Sprintf("invalid severity %q: must be info, success, warning, or error", severity)), ChannelReplyOutput{}, nil
		}

		// Secret-entry mode: request a masked password field. The submitted
		// value routes browser → proxy → daemon store; it never appears in
		// channel events — the agent only sees {name, fingerprint:last-4}.
		if input.Input != "" && input.Input != "secret" {
			return errorResult(fmt.Sprintf("invalid input mode %q: only 'secret' is supported", input.Input)), ChannelReplyOutput{}, nil
		}
		if input.Input == "secret" && !store.ValidSecretName(input.Name) {
			return errorResult(fmt.Sprintf("input 'secret' requires a valid env-var-style name (got %q): [A-Za-z_][A-Za-z0-9_]*", input.Name)), ChannelReplyOutput{}, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), ChannelReplyOutput{}, nil
		}

		toastConfig := protocol.ToastConfig{
			Type:    severity,
			Title:   input.Title,
			Message: input.Content,
			Input:   input.Input,
			Name:    input.Name,
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
		dirFilter := dt.scopeFilter(nil)

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
