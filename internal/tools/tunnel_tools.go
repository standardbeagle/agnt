package tools

import (
	"context"
	"fmt"

	"devtool-mcp/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TunnelInput represents input for the tunnel tool.
type TunnelInput struct {
	Action     string `json:"action" jsonschema:"Action: start, stop, status, list"`
	ID         string `json:"id,omitempty" jsonschema:"Tunnel ID (required for start/stop/status)"`
	Provider   string `json:"provider,omitempty" jsonschema:"Tunnel provider: 'cloudflare' or 'ngrok' (required for start)"`
	LocalPort  int    `json:"local_port,omitempty" jsonschema:"Local port to tunnel (required for start)"`
	LocalHost  string `json:"local_host,omitempty" jsonschema:"Local host (default: localhost)"`
	BinaryPath string `json:"binary_path,omitempty" jsonschema:"Optional path to tunnel binary"`
	ProxyID    string `json:"proxy_id,omitempty" jsonschema:"Optional proxy ID to auto-configure with the tunnel's public URL"`
}

// TunnelOutput represents output from the tunnel tool.
type TunnelOutput struct {
	ID        string        `json:"id,omitempty"`
	Provider  string        `json:"provider,omitempty"`
	State     string        `json:"state,omitempty"`
	PublicURL string        `json:"public_url,omitempty"`
	LocalAddr string        `json:"local_addr,omitempty"`
	Error     string        `json:"error,omitempty"`
	Success   bool          `json:"success,omitempty"`
	Message   string        `json:"message,omitempty"`
	Count     int           `json:"count,omitempty"`
	Tunnels   []TunnelEntry `json:"tunnels,omitempty"`
}

// TunnelEntry represents a tunnel in a list response.
type TunnelEntry struct {
	ID        string `json:"id,omitempty"`
	Provider  string `json:"provider"`
	State     string `json:"state"`
	PublicURL string `json:"public_url,omitempty"`
	LocalAddr string `json:"local_addr"`
	Error     string `json:"error,omitempty"`
}

// RegisterTunnelTool registers the tunnel MCP tool with the server.
func RegisterTunnelTool(server *mcp.Server, dt *DaemonTools) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "tunnel",
		Description: `Manage tunnel connections for exposing local services publicly.

Actions:
  start: Start a tunnel to expose a local port publicly
  stop: Stop a running tunnel
  status: Get tunnel status and public URL
  list: List all active tunnels

Providers:
  cloudflare: Uses cloudflared for Cloudflare Quick Tunnels (trycloudflare.com)
  ngrok: Uses ngrok for tunneling

Examples:
  tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 8080}
  tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}
  tunnel {action: "status", id: "dev"}
  tunnel {action: "list"}
  tunnel {action: "stop", id: "dev"}

The tunnel automatically configures the proxy's public_url when proxy_id is specified,
enabling proper URL rewriting for mobile device testing through the tunnel.

Requirements:
  - cloudflare provider: 'cloudflared' binary must be installed and in PATH
  - ngrok provider: 'ngrok' binary must be installed and in PATH`,
	}, dt.makeTunnelHandler())
}

// makeTunnelHandler creates a handler for the tunnel tool.
func (dt *DaemonTools) makeTunnelHandler() func(context.Context, *mcp.CallToolRequest, TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
		emptyOutput := TunnelOutput{Tunnels: []TunnelEntry{}}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		switch input.Action {
		case "start":
			return dt.handleTunnelStart(input)
		case "stop":
			return dt.handleTunnelStop(input)
		case "status":
			return dt.handleTunnelStatus(input)
		case "list":
			return dt.handleTunnelList()
		default:
			return errorResult(fmt.Sprintf("unknown action: %s (use: start, stop, status, list)", input.Action)), emptyOutput, nil
		}
	}
}

func (dt *DaemonTools) handleTunnelStart(input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	emptyOutput := TunnelOutput{Tunnels: []TunnelEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}
	if input.Provider == "" {
		return errorResult("provider required (cloudflare or ngrok)"), emptyOutput, nil
	}
	if input.LocalPort <= 0 {
		return errorResult("local_port required"), emptyOutput, nil
	}

	config := protocol.TunnelStartConfig{
		ID:         input.ID,
		Provider:   input.Provider,
		LocalPort:  input.LocalPort,
		LocalHost:  input.LocalHost,
		BinaryPath: input.BinaryPath,
		ProxyID:    input.ProxyID,
	}

	result, err := dt.client.TunnelStart(config)
	if err != nil {
		return formatDaemonError(err, "tunnel start"), emptyOutput, nil
	}

	output := TunnelOutput{
		ID:        getString(result, "id"),
		Provider:  getString(result, "provider"),
		State:     getString(result, "state"),
		PublicURL: getString(result, "public_url"),
		LocalAddr: getString(result, "local_addr"),
		Error:     getString(result, "error"),
		Tunnels:   []TunnelEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleTunnelStop(input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	emptyOutput := TunnelOutput{Tunnels: []TunnelEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	if err := dt.client.TunnelStop(input.ID); err != nil {
		return formatDaemonError(err, "tunnel stop"), emptyOutput, nil
	}

	output := TunnelOutput{
		Success: true,
		ID:      input.ID,
		Message: "Tunnel stopped",
		Tunnels: []TunnelEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleTunnelStatus(input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	emptyOutput := TunnelOutput{Tunnels: []TunnelEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	result, err := dt.client.TunnelStatus(input.ID)
	if err != nil {
		return formatDaemonError(err, "tunnel status"), emptyOutput, nil
	}

	output := TunnelOutput{
		ID:        getString(result, "id"),
		Provider:  getString(result, "provider"),
		State:     getString(result, "state"),
		PublicURL: getString(result, "public_url"),
		LocalAddr: getString(result, "local_addr"),
		Error:     getString(result, "error"),
		Tunnels:   []TunnelEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleTunnelList() (*mcp.CallToolResult, TunnelOutput, error) {
	result, err := dt.client.TunnelList()
	if err != nil {
		return formatDaemonError(err, "tunnel list"), TunnelOutput{Tunnels: []TunnelEntry{}}, nil
	}

	count := getInt(result, "count")
	tunnelsRaw, _ := result["tunnels"].([]interface{})

	tunnels := make([]TunnelEntry, 0, len(tunnelsRaw))
	for _, t := range tunnelsRaw {
		if tm, ok := t.(map[string]interface{}); ok {
			tunnels = append(tunnels, TunnelEntry{
				ID:        getString(tm, "id"),
				Provider:  getString(tm, "provider"),
				State:     getString(tm, "state"),
				PublicURL: getString(tm, "public_url"),
				LocalAddr: getString(tm, "local_addr"),
				Error:     getString(tm, "error"),
			})
		}
	}

	output := TunnelOutput{
		Count:   count,
		Tunnels: tunnels,
	}

	return nil, output, nil
}
