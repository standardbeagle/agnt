package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// BrowserInput represents input for the browser tool.
type BrowserInput struct {
	Action     string `json:"action" jsonschema:"Action: start, stop, status, list"`
	ID         string `json:"id,omitempty" jsonschema:"Browser ID (optional for start, required for stop/status)"`
	URL        string `json:"url,omitempty" jsonschema:"URL to open (required for start unless proxy_id is provided)"`
	ProxyID    string `json:"proxy_id,omitempty" jsonschema:"Proxy to use - auto-starts proxy if not found and URL is provided"`
	Headless   *bool  `json:"headless,omitempty" jsonschema:"Run in headless mode (default: true)"`
	BinaryPath string `json:"binary_path,omitempty" jsonschema:"Optional path to Chrome binary (auto-detected if empty)"`
	Global     bool   `json:"global,omitempty" jsonschema:"For list: include browsers from all directories (default: false)"`
}

// BrowserOutput represents output from the browser tool.
type BrowserOutput struct {
	ID           string         `json:"id,omitempty"`
	State        string         `json:"state,omitempty"`
	PID          int            `json:"pid,omitempty"`
	URL          string         `json:"url,omitempty"`
	Headless     bool           `json:"headless,omitempty"`
	ProxyStarted bool           `json:"proxy_started,omitempty"` // True if proxy was auto-started
	ProxyURL     string         `json:"proxy_url,omitempty"`
	BinaryPath   string         `json:"binary_path,omitempty"`
	Path         string         `json:"path,omitempty"`
	StartedAt    string         `json:"started_at,omitempty"`
	Error        string         `json:"error,omitempty"`
	Success      bool           `json:"success,omitempty"`
	Message      string         `json:"message,omitempty"`
	Count        int            `json:"count,omitempty"`
	Browsers     []BrowserEntry `json:"browsers,omitempty"`
}

// BrowserEntry represents a browser in a list response.
type BrowserEntry struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	PID      int    `json:"pid,omitempty"`
	URL      string `json:"url,omitempty"`
	Headless bool   `json:"headless"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RegisterBrowserTool registers the browser MCP tool with the server.
func RegisterBrowserTool(server *mcp.Server, dt *DaemonTools) {
	addLenientTool(server, &mcp.Tool{
		Name: "browser",
		Description: `Launch and manage Chrome browser instances for automation.

Actions:
  start: Start a browser instance
  stop: Stop a running browser
  status: Get browser status
  list: List all active browsers

Features:
  - Chrome/Chromium only (auto-detected)
  - Headless mode by default
  - Auto-starts proxy if proxy_id specified but not found

Examples:
  browser {action: "start", url: "http://localhost:3000"}
  browser {action: "start", proxy_id: "dev"}
  browser {action: "start", url: "http://localhost:3000", proxy_id: "dev"}
  browser {action: "start", url: "http://localhost:3000", headless: false}
  browser {action: "status", id: "browser-1234"}
  browser {action: "list"}
  browser {action: "stop", id: "browser-1234"}

Proxy Auto-Start:
  When proxy_id is specified but the proxy doesn't exist, the browser tool
  will auto-start the proxy using the provided URL. The browser then opens
  through the proxy, enabling devtool features (screenshots, DOM inspection, etc.).

Requirements:
  - Chrome or Chromium must be installed
  - Chrome 109+ required for headless mode`,
	}, dt.makeBrowserHandler())
}

// makeBrowserHandler creates a handler for the browser tool.
func (dt *DaemonTools) makeBrowserHandler() func(context.Context, *mcp.CallToolRequest, BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
		emptyOutput := BrowserOutput{Browsers: []BrowserEntry{}}

		if err := validateBrowserInput(input); err != nil {
			return errorResult(validationError("browser", err)), emptyOutput, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		switch input.Action {
		case "start":
			return dt.handleBrowserStart(input)
		case "stop":
			return dt.handleBrowserStop(input)
		case "status":
			return dt.handleBrowserStatus(input)
		case "list":
			return dt.handleBrowserList(input)
		default:
			return errorResult(fmt.Sprintf("unknown action: %s (use: start, stop, status, list)", input.Action)), emptyOutput, nil
		}
	}
}

func (dt *DaemonTools) handleBrowserStart(input BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
	emptyOutput := BrowserOutput{Browsers: []BrowserEntry{}}

	if input.URL == "" && input.ProxyID == "" {
		return errorResult("url or proxy_id required"), emptyOutput, nil
	}

	config := protocol.BrowserStartConfig{
		ID:         input.ID,
		URL:        input.URL,
		ProxyID:    input.ProxyID,
		Headless:   input.Headless,
		BinaryPath: input.BinaryPath,
	}

	result, err := dt.client.BrowserStart(config)
	if err != nil {
		return formatDaemonError(err, "browser start"), emptyOutput, nil
	}

	output := BrowserOutput{
		ID:           getString(result, "id"),
		State:        getString(result, "state"),
		PID:          getInt(result, "pid"),
		Headless:     getBool(result, "headless"),
		ProxyStarted: getBool(result, "proxy_started"),
		ProxyURL:     getString(result, "proxy_url"),
		Browsers:     []BrowserEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleBrowserStop(input BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
	emptyOutput := BrowserOutput{Browsers: []BrowserEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	if err := dt.client.BrowserStop(input.ID); err != nil {
		return formatDaemonError(err, "browser stop"), emptyOutput, nil
	}

	output := BrowserOutput{
		Success:  true,
		ID:       input.ID,
		Message:  "Browser stopped",
		Browsers: []BrowserEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleBrowserStatus(input BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
	emptyOutput := BrowserOutput{Browsers: []BrowserEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	result, err := dt.client.BrowserStatus(input.ID)
	if err != nil {
		return formatDaemonError(err, "browser status"), emptyOutput, nil
	}

	output := BrowserOutput{
		ID:         getString(result, "id"),
		State:      getString(result, "state"),
		PID:        getInt(result, "pid"),
		URL:        getString(result, "url"),
		Headless:   getBool(result, "headless"),
		ProxyURL:   getString(result, "proxy_url"),
		BinaryPath: getString(result, "binary_path"),
		Path:       getString(result, "path"),
		StartedAt:  getString(result, "started_at"),
		Error:      getString(result, "error"),
		Browsers:   []BrowserEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleBrowserList(input BrowserInput) (*mcp.CallToolResult, BrowserOutput, error) {
	dirFilter := protocol.DirectoryFilter{
		Global: input.Global,
	}

	result, err := dt.client.BrowserList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "browser list"), BrowserOutput{Browsers: []BrowserEntry{}}, nil
	}

	count := getInt(result, "count")
	browsersRaw, _ := result["browsers"].([]interface{})

	browsers := make([]BrowserEntry, 0, len(browsersRaw))
	for _, b := range browsersRaw {
		if bm, ok := b.(map[string]interface{}); ok {
			browsers = append(browsers, BrowserEntry{
				ID:       getString(bm, "id"),
				State:    getString(bm, "state"),
				PID:      getInt(bm, "pid"),
				URL:      getString(bm, "url"),
				Headless: getBool(bm, "headless"),
				ProxyURL: getString(bm, "proxy_url"),
				Path:     getString(bm, "path"),
				Error:    getString(bm, "error"),
			})
		}
	}

	output := BrowserOutput{
		Count:    count,
		Browsers: browsers,
	}

	return nil, output, nil
}
