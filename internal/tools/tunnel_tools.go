package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/standardbeagle/go-sdk/mcp"
)

// TunnelInput represents input for the tunnel tool.
type TunnelInput struct {
	Action     string `json:"action" jsonschema:"Action: start, stop, status, list"`
	ID         string `json:"id,omitempty" jsonschema:"Tunnel ID (required for start/stop/status)"`
	Provider   string `json:"provider,omitempty" jsonschema:"Tunnel provider: 'cloudflare', 'ngrok', or 'tailscale' (required for start)"`
	LocalPort  int    `json:"local_port,omitempty" jsonschema:"Local port to tunnel (required for start)"`
	LocalHost  string `json:"local_host,omitempty" jsonschema:"Local host (default: localhost)"`
	BinaryPath string `json:"binary_path,omitempty" jsonschema:"Optional path to tunnel binary"`
	ProxyID    string `json:"proxy_id,omitempty" jsonschema:"Optional proxy ID to auto-configure with the tunnel's public URL"`
	Global     *bool  `json:"global,omitempty" jsonschema:"For list: override project config; true includes all projects, false forces current project"`
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
	Count     int           `json:"count"`
	Tunnels   []TunnelEntry `json:"tunnels,omitempty"`
}

// TunnelEntry represents a tunnel in a list response.
type TunnelEntry struct {
	ID        string `json:"id,omitempty"`
	Provider  string `json:"provider"`
	State     string `json:"state"`
	PublicURL string `json:"public_url,omitempty"`
	LocalAddr string `json:"local_addr"`
	Path      string `json:"path,omitempty"`
	Error     string `json:"error,omitempty"`
}

// RegisterTunnelTool registers the tunnel MCP tool with the server.
func RegisterTunnelTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "tunnel",
		Description: `Manage tunnel connections for exposing a local port over the network.

Actions:
  start: Start a tunnel for a local port
  stop: Stop a running tunnel
  status: Get tunnel status and URL
  list: List all active tunnels

Providers:
  cloudflare: cloudflared Quick Tunnels — public internet (trycloudflare.com)
  ngrok: ngrok — public internet
  tailscale: 'tailscale serve' — tailnet-private HTTPS at this node's
             MagicDNS name (https://<machine>.<tailnet>.ts.net). Reachable
             only from other devices on your tailnet. One service per node
             at root '/' (multiple tailscale tunnels on the same machine
             will collide).

Examples:
  tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 8080}
  tunnel {action: "start", id: "dev", provider: "tailscale",  local_port: 8080}
  tunnel {action: "start", id: "dev", provider: "cloudflare", local_port: 12345, proxy_id: "dev"}
  tunnel {action: "status", id: "dev"}
  tunnel {action: "list"}
  tunnel {action: "stop", id: "dev"}

The tunnel automatically configures the proxy's public_url when proxy_id is
specified, enabling proper URL rewriting for cross-device testing through
the tunnel.

Requirements:
  - cloudflare: 'cloudflared' binary in PATH
  - ngrok: 'ngrok' binary in PATH
  - tailscale: 'tailscale' binary in PATH, node logged in, HTTPS + MagicDNS
    enabled in the tailnet admin console (serve requires HTTPS certs)`,
	}, dt.makeTunnelHandler())
}

// makeTunnelHandler creates a handler for the tunnel tool.
func (dt *DaemonTools) makeTunnelHandler() func(context.Context, *mcp.CallToolRequest, TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
		emptyOutput := TunnelOutput{Tunnels: []TunnelEntry{}}

		if err := validateTunnelInput(input); err != nil {
			return errorResult(validationError("tunnel", err)), emptyOutput, nil
		}

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
			return dt.handleTunnelList(input)
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
		return errorResult("provider required (cloudflare, ngrok, or tailscale)"), emptyOutput, nil
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

func (dt *DaemonTools) handleTunnelList(input TunnelInput) (*mcp.CallToolResult, TunnelOutput, error) {
	// The MCP daemon connection is not session-bound, so a non-global list
	// must name the project explicitly (SessionCode preferred, Directory
	// fallback) or the session-scope chokepoint rejects it. Mirrors
	// handleProxyList / handleProcList.
	dirFilter := protocol.DirectoryFilter{
		GlobalOverride: input.Global,
	}
	if !globalEnabled(input.Global) {
		if sessionCode := dt.SessionCode(); sessionCode != "" {
			dirFilter.SessionCode = sessionCode
		} else if projectPath := getProjectPath(); projectPath != "" {
			dirFilter.Directory = projectPath
		}
	}

	result, err := dt.client.TunnelList(dirFilter)
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
				Path:      getString(tm, "path"),
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
