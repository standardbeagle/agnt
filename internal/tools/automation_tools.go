package tools

import (
	"context"
	"fmt"

	"github.com/standardbeagle/agnt/internal/protocol"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// AutomationInput represents input for the automation tool.
type AutomationInput struct {
	Action    string  `json:"action" jsonschema:"Action: start, stop, status, list, screenshot, navigate, evaluate"`
	ID        string  `json:"id,omitempty" jsonschema:"Session ID (optional for start, required for stop/status)"`
	URL       string  `json:"url,omitempty" jsonschema:"URL to open (for start/navigate actions)"`
	ProxyID   string  `json:"proxy_id,omitempty" jsonschema:"Proxy to route through (for start action)"`
	Headless  *bool   `json:"headless,omitempty" jsonschema:"Run in headless mode (default: true)"`
	Global    bool    `json:"global,omitempty" jsonschema:"For list: include sessions from all directories"`
	Type      string  `json:"type,omitempty" jsonschema:"Screenshot type: viewport, fullpage, element, clip"`
	Label     string  `json:"label,omitempty" jsonschema:"Label for screenshot filename"`
	Selector  string  `json:"selector,omitempty" jsonschema:"CSS selector for element screenshot"`
	Viewport  string  `json:"viewport,omitempty" jsonschema:"Viewport preset: desktop, mobile, tablet"`
	X         float64 `json:"x,omitempty" jsonschema:"Clip X coordinate"`
	Y         float64 `json:"y,omitempty" jsonschema:"Clip Y coordinate"`
	Width     float64 `json:"width,omitempty" jsonschema:"Clip width"`
	Height    float64 `json:"height,omitempty" jsonschema:"Clip height"`
	Script    string  `json:"script,omitempty" jsonschema:"JavaScript to evaluate"`
	SessionID string  `json:"session_id,omitempty" jsonschema:"Session ID for screenshot/navigate/evaluate"`
}

// AutomationOutput represents output from the automation tool.
type AutomationOutput struct {
	ID        string            `json:"id,omitempty"`
	State     string            `json:"state,omitempty"`
	URL       string            `json:"url,omitempty"`
	Headless  bool              `json:"headless,omitempty"`
	ProxyURL  string            `json:"proxy_url,omitempty"`
	Path      string            `json:"path,omitempty"`
	StartedAt string            `json:"started_at,omitempty"`
	Error     string            `json:"error,omitempty"`
	Success   bool              `json:"success,omitempty"`
	Message   string            `json:"message,omitempty"`
	Count     int               `json:"count,omitempty"`
	Sessions  []AutomationEntry `json:"sessions,omitempty"`
	// Screenshot fields
	ScreenshotPath string `json:"screenshot_path,omitempty"`
	Filename       string `json:"filename,omitempty"`
	ScreenWidth    int64  `json:"screen_width,omitempty"`
	ScreenHeight   int64  `json:"screen_height,omitempty"`
	ViewportName   string `json:"viewport_name,omitempty"`
	Timestamp      string `json:"timestamp,omitempty"`
	// Evaluate fields
	Result interface{} `json:"result,omitempty"`
}

// AutomationEntry represents an automation session in a list response.
type AutomationEntry struct {
	ID       string `json:"id"`
	State    string `json:"state"`
	URL      string `json:"url,omitempty"`
	Headless bool   `json:"headless"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Path     string `json:"path,omitempty"`
	Error    string `json:"error,omitempty"`
}

// RegisterAutomationTool registers the automation MCP tool with the server.
func RegisterAutomationTool(server *mcp.Server, dt *DaemonTools) {
	mcp.AddTool(server, &mcp.Tool{
		Name: "automation",
		Description: `Control browser automation sessions for programmatic testing and screenshots.

Actions:
  start: Start a chromedp automation session
  stop: Stop a running session
  status: Get session status
  list: List all active sessions
  screenshot: Take a screenshot (viewport, fullpage, element, or clip)
  navigate: Navigate to a URL
  evaluate: Execute JavaScript

Examples:
  automation {action: "start", url: "http://localhost:3000"}
  automation {action: "start", proxy_id: "dev"}
  automation {action: "screenshot", session_id: "auto-1234", type: "viewport"}
  automation {action: "screenshot", session_id: "auto-1234", type: "fullpage", viewport: "mobile"}
  automation {action: "screenshot", session_id: "auto-1234", type: "element", selector: "#main"}
  automation {action: "navigate", session_id: "auto-1234", url: "http://example.com"}
  automation {action: "evaluate", session_id: "auto-1234", script: "document.title"}
  automation {action: "list"}
  automation {action: "stop", id: "auto-1234"}

Screenshot Types:
  viewport: Capture visible viewport only
  fullpage: Capture entire scrollable page
  element: Capture specific element by CSS selector
  clip: Capture specific region (requires x, y, width, height)

Viewport Presets:
  desktop: 1920x1080
  tablet: 768x1024
  mobile: 375x667

Requirements:
  - Chrome or Chromium must be installed
  - Proxy recommended for full devtool features`,
	}, dt.makeAutomationHandler())
}

// makeAutomationHandler creates a handler for the automation tool.
func (dt *DaemonTools) makeAutomationHandler() func(context.Context, *mcp.CallToolRequest, AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
		emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

		if err := validateAutomationInput(input); err != nil {
			return errorResult(validationError("automation", err)), emptyOutput, nil
		}

		if err := dt.ensureConnected(); err != nil {
			return errorResult(err.Error()), emptyOutput, nil
		}

		switch input.Action {
		case "start":
			return dt.handleAutomationStart(input)
		case "stop":
			return dt.handleAutomationStop(input)
		case "status":
			return dt.handleAutomationStatus(input)
		case "list":
			return dt.handleAutomationList(input)
		case "screenshot":
			return dt.handleAutomationScreenshot(input)
		case "navigate":
			return dt.handleAutomationNavigate(input)
		case "evaluate":
			return dt.handleAutomationEvaluate(input)
		default:
			return errorResult(fmt.Sprintf("unknown action: %s (use: start, stop, status, list, screenshot, navigate, evaluate)", input.Action)), emptyOutput, nil
		}
	}
}

func (dt *DaemonTools) handleAutomationStart(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	if input.URL == "" && input.ProxyID == "" {
		return errorResult("url or proxy_id required"), emptyOutput, nil
	}

	config := protocol.AutomationStartConfig{
		ID:       input.ID,
		URL:      input.URL,
		ProxyID:  input.ProxyID,
		Headless: input.Headless,
	}

	result, err := dt.client.AutomationStart(config)
	if err != nil {
		return formatDaemonError(err, "automation start"), emptyOutput, nil
	}

	output := AutomationOutput{
		ID:       getString(result, "id"),
		State:    getString(result, "state"),
		Headless: getBool(result, "headless"),
		ProxyURL: getString(result, "proxy_url"),
		URL:      getString(result, "url"),
		Sessions: []AutomationEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationStop(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	if err := dt.client.AutomationStop(input.ID); err != nil {
		return formatDaemonError(err, "automation stop"), emptyOutput, nil
	}

	output := AutomationOutput{
		Success:  true,
		ID:       input.ID,
		Message:  "Automation session stopped",
		Sessions: []AutomationEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationStatus(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	if input.ID == "" {
		return errorResult("id required"), emptyOutput, nil
	}

	result, err := dt.client.AutomationStatus(input.ID)
	if err != nil {
		return formatDaemonError(err, "automation status"), emptyOutput, nil
	}

	output := AutomationOutput{
		ID:        getString(result, "id"),
		State:     getString(result, "state"),
		Headless:  getBool(result, "headless"),
		URL:       getString(result, "url"),
		ProxyURL:  getString(result, "proxy_url"),
		Path:      getString(result, "path"),
		StartedAt: getString(result, "started_at"),
		Error:     getString(result, "error"),
		Sessions:  []AutomationEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationList(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	dirFilter := protocol.DirectoryFilter{
		Global: input.Global,
	}

	result, err := dt.client.AutomationList(dirFilter)
	if err != nil {
		return formatDaemonError(err, "automation list"), AutomationOutput{Sessions: []AutomationEntry{}}, nil
	}

	sessionsRaw, _ := result["sessions"].([]interface{})

	sessions := make([]AutomationEntry, 0, len(sessionsRaw))
	for _, s := range sessionsRaw {
		if sm, ok := s.(map[string]interface{}); ok {
			sessions = append(sessions, AutomationEntry{
				ID:       getString(sm, "id"),
				State:    getString(sm, "state"),
				URL:      getString(sm, "url"),
				Headless: getBool(sm, "headless"),
				ProxyURL: getString(sm, "proxy_url"),
				Path:     getString(sm, "path"),
				Error:    getString(sm, "error"),
			})
		}
	}

	output := AutomationOutput{
		Count:    len(sessions),
		Sessions: sessions,
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationScreenshot(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	// Session ID can come from either field
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = input.ID
	}
	if sessionID == "" {
		return errorResult("session_id required"), emptyOutput, nil
	}

	// Default to viewport screenshot
	screenshotType := input.Type
	if screenshotType == "" {
		screenshotType = "viewport"
	}

	config := protocol.AutomationScreenshotConfig{
		SessionID: sessionID,
		Type:      screenshotType,
		Label:     input.Label,
		Selector:  input.Selector,
		Viewport:  input.Viewport,
		X:         input.X,
		Y:         input.Y,
		Width:     input.Width,
		Height:    input.Height,
	}

	result, err := dt.client.AutomationScreenshot(config)
	if err != nil {
		return formatDaemonError(err, "automation screenshot"), emptyOutput, nil
	}

	output := AutomationOutput{
		ScreenshotPath: getString(result, "path"),
		Filename:       getString(result, "filename"),
		ScreenWidth:    int64(getInt(result, "width")),
		ScreenHeight:   int64(getInt(result, "height")),
		ViewportName:   getString(result, "viewport"),
		Timestamp:      getString(result, "timestamp"),
		Success:        true,
		Sessions:       []AutomationEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationNavigate(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	// Session ID can come from either field
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = input.ID
	}
	if sessionID == "" {
		return errorResult("session_id required"), emptyOutput, nil
	}

	if input.URL == "" {
		return errorResult("url required"), emptyOutput, nil
	}

	config := protocol.AutomationNavigateConfig{
		SessionID: sessionID,
		URL:       input.URL,
	}

	result, err := dt.client.AutomationNavigate(config)
	if err != nil {
		return formatDaemonError(err, "automation navigate"), emptyOutput, nil
	}

	output := AutomationOutput{
		Success:  getBool(result, "success"),
		URL:      getString(result, "url"),
		Sessions: []AutomationEntry{},
	}

	return nil, output, nil
}

func (dt *DaemonTools) handleAutomationEvaluate(input AutomationInput) (*mcp.CallToolResult, AutomationOutput, error) {
	emptyOutput := AutomationOutput{Sessions: []AutomationEntry{}}

	// Session ID can come from either field
	sessionID := input.SessionID
	if sessionID == "" {
		sessionID = input.ID
	}
	if sessionID == "" {
		return errorResult("session_id required"), emptyOutput, nil
	}

	if input.Script == "" {
		return errorResult("script required"), emptyOutput, nil
	}

	config := protocol.AutomationEvaluateConfig{
		SessionID: sessionID,
		Script:    input.Script,
	}

	result, err := dt.client.AutomationEvaluate(config)
	if err != nil {
		return formatDaemonError(err, "automation evaluate"), emptyOutput, nil
	}

	output := AutomationOutput{
		Success:  getBool(result, "success"),
		Result:   result["result"],
		Sessions: []AutomationEntry{},
	}

	return nil, output, nil
}
