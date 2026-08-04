package daemon

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/chromedp/cdproto/runtime"
	cdp "github.com/chromedp/chromedp"
	"github.com/standardbeagle/agnt/internal/chromedp"
	hubpkg "github.com/standardbeagle/go-cli-server/hub"
	hubproto "github.com/standardbeagle/go-cli-server/protocol"
)

// hubHandleAutomation handles the AUTOMATION command for chromedp sessions.

func (d *Daemon) automationActions() map[string]handlerFn {
	return map[string]handlerFn{
		"START":      d.hubHandleAutomationStart,
		"STOP":       d.hubHandleAutomationStop,
		"STATUS":     noCtx(d.hubHandleAutomationStatus),
		"LIST":       noCtx(d.hubHandleAutomationList),
		"SCREENSHOT": d.hubHandleAutomationScreenshot,
		"NAVIGATE":   d.hubHandleAutomationNavigate,
		"EVALUATE":   d.hubHandleAutomationEvaluate,
	}
}

func (d *Daemon) hubHandleAutomation(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	return newCommandRouter("AUTOMATION").dispatch(ctx, conn, cmd, d.automationActions())
}

// hubHandleAutomationStart handles AUTOMATION START command.

func (d *Daemon) hubHandleAutomationStart(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		ID       string `json:"id"`
		URL      string `json:"url"`
		ProxyID  string `json:"proxy_id"`
		Headless *bool  `json:"headless"`
	}](cmd)

	// Accept ID from args for convenience
	if config.ID == "" && len(cmd.Args) > 0 {
		config.ID = cmd.Args[0]
	}

	// Generate ID if not provided
	if config.ID == "" {
		config.ID = fmt.Sprintf("auto-%d", time.Now().UnixNano()%10000)
	}

	// Get project path from session
	projectPath := d.getSessionProjectPath(conn)

	// Make session ID unique per project
	fullID := config.ID
	if projectPath != "" {
		fullID = makeProcessID(projectPath, config.ID)
	}

	// Determine proxy URL
	var proxyURL string
	if config.ProxyID != "" {
		p, err := getSessionScoped(d, conn, config.ProxyID, d.proxym.GetWithPathFilter)
		if err != nil {
			return conn.WriteErr(hubproto.ErrNotFound, fmt.Sprintf("proxy %q not found: %v", config.ProxyID, err))
		}
		proxyURL = "http://" + p.ListenAddr
	}

	// Set headless mode (default true)
	headless := true
	if config.Headless != nil {
		headless = *config.Headless
	}

	// Create session config
	sessionConfig := chromedp.SessionConfig{
		ID:       fullID,
		URL:      config.URL,
		Headless: headless,
		ProxyURL: proxyURL,
		Path:     projectPath,
	}

	// Start the session
	session, err := d.sessionm.Start(ctx, fullID, sessionConfig)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"id":       fullID,
		"state":    session.State().String(),
		"headless": headless,
	}
	if proxyURL != "" {
		resp["proxy_url"] = proxyURL
	}
	if config.URL != "" {
		resp["url"] = config.URL
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleAutomationStop handles AUTOMATION STOP command.

func (d *Daemon) hubHandleAutomationStop(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "AUTOMATION STOP requires: <id>")
	}

	sessionID := cmd.Args[0]

	// Use session-scoped lookup
	session, err := getSessionScoped(d, conn, sessionID, d.sessionm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	if err := d.sessionm.Stop(ctx, session.ID()); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	return conn.WriteOK("automation session stopped")
}

// hubHandleAutomationStatus handles AUTOMATION STATUS command.

func (d *Daemon) hubHandleAutomationStatus(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	if len(cmd.Args) < 1 {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "AUTOMATION STATUS requires: <id>")
	}

	sessionID := cmd.Args[0]

	session, err := getSessionScoped(d, conn, sessionID, d.sessionm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	info := session.Info()
	resp := map[string]interface{}{
		"id":       info.ID,
		"state":    info.State,
		"headless": info.Headless,
		"path":     info.Path,
	}
	if info.URL != "" {
		resp["url"] = info.URL
	}
	if info.ProxyURL != "" {
		resp["proxy_url"] = info.ProxyURL
	}
	if info.StartedAt != "" {
		resp["started_at"] = info.StartedAt
	}
	if info.Error != "" {
		resp["error"] = info.Error
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleAutomationList handles AUTOMATION LIST command.

func (d *Daemon) hubHandleAutomationList(conn *hubpkg.Connection, cmd *hubproto.Command) error {
	dirFilter, _ := unmarshalCommand[hubproto.DirectoryFilter](cmd)

	var infos []chromedp.SessionInfo
	if dirFilter.Global {
		infos = d.sessionm.List()
	} else {
		projectPath := d.getSessionProjectPath(conn)
		if projectPath != "" {
			infos = d.sessionm.ListByPath(projectPath)
		} else {
			infos = d.sessionm.List()
		}
	}

	entries := make([]map[string]interface{}, len(infos))
	for i, info := range infos {
		entry := map[string]interface{}{
			"id":       info.ID,
			"state":    info.State,
			"headless": info.Headless,
			"path":     info.Path,
		}
		if info.URL != "" {
			entry["url"] = info.URL
		}
		if info.ProxyURL != "" {
			entry["proxy_url"] = info.ProxyURL
		}
		if info.Error != "" {
			entry["error"] = info.Error
		}
		entries[i] = entry
	}

	data, _ := json.Marshal(map[string]interface{}{"sessions": entries})
	return conn.WriteJSON(data)
}

// hubHandleAutomationScreenshot handles AUTOMATION SCREENSHOT command.

func (d *Daemon) hubHandleAutomationScreenshot(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		SessionID string `json:"session_id"`
		Type      string `json:"type"`     // viewport, fullpage, element, clip
		Label     string `json:"label"`    // Optional label for filename
		Selector  string `json:"selector"` // CSS selector for element type
		Viewport  string `json:"viewport"` // Viewport preset name
		// Clip bounds
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	}](cmd)

	// Session ID from args takes precedence
	if len(cmd.Args) > 0 {
		config.SessionID = cmd.Args[0]
	}

	if config.SessionID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "session_id required")
	}

	// Default to viewport screenshot
	if config.Type == "" {
		config.Type = "viewport"
	}

	session, err := getSessionScoped(d, conn, config.SessionID, d.sessionm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	// Parse viewport preset if specified
	var viewportPreset *chromedp.ViewportPreset
	if config.Viewport != "" {
		vp := chromedp.GetViewport(config.Viewport)
		if vp == nil {
			return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown viewport: %s", config.Viewport))
		}
		viewportPreset = vp
	}

	opts := chromedp.ScreenshotOptions{
		Label:    config.Label,
		Viewport: viewportPreset,
	}

	var result *chromedp.ScreenshotResult

	switch config.Type {
	case "viewport":
		result, err = chromedp.CaptureViewport(session, opts)
	case "fullpage":
		result, err = chromedp.CaptureFullPage(session, opts)
	case "element":
		if config.Selector == "" {
			return conn.WriteErr(hubproto.ErrInvalidArgs, "selector required for element screenshot")
		}
		result, err = chromedp.CaptureElement(session, config.Selector, opts)
	case "clip":
		if config.Width <= 0 || config.Height <= 0 {
			return conn.WriteErr(hubproto.ErrInvalidArgs, "width and height required for clip screenshot")
		}
		result, err = chromedp.CaptureWithClip(session, config.X, config.Y, config.Width, config.Height, opts)
	default:
		return conn.WriteErr(hubproto.ErrInvalidArgs, fmt.Sprintf("unknown screenshot type: %s", config.Type))
	}

	if err != nil {
		return conn.WriteErr(hubproto.ErrInternal, err.Error())
	}

	resp := map[string]interface{}{
		"path":      result.Path,
		"filename":  result.Filename,
		"timestamp": result.Timestamp,
	}
	if result.Width > 0 {
		resp["width"] = result.Width
	}
	if result.Height > 0 {
		resp["height"] = result.Height
	}
	if result.Viewport != "" {
		resp["viewport"] = result.Viewport
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// hubHandleAutomationNavigate handles AUTOMATION NAVIGATE command.

func (d *Daemon) hubHandleAutomationNavigate(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		SessionID string `json:"session_id"`
		URL       string `json:"url"`
	}](cmd)

	// Accept session_id and URL from args
	if len(cmd.Args) >= 1 {
		config.SessionID = cmd.Args[0]
	}
	if len(cmd.Args) >= 2 {
		config.URL = cmd.Args[1]
	}

	if config.SessionID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "session_id required")
	}
	if config.URL == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "url required")
	}

	session, err := getSessionScoped(d, conn, config.SessionID, d.sessionm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	if err := session.Run(cdp.Navigate(config.URL)); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("navigation failed: %v", err))
	}

	resp := map[string]interface{}{
		"success": true,
		"url":     config.URL,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}

// automationEvalScript scopes a caller's script to the requested frame.
//
// The proxy always-wraps a top-level navigation in a chrome shell whose body is
// a single content iframe (#__devtool_content_frame); the app lives in that
// iframe. A bare evaluate therefore lands in the SHELL, where the app's DOM does
// not exist — so callers were reaching through
// document.getElementById('__devtool_content_frame').contentWindow by hand on
// every single call, and a forgotten hop looked like a broken selector (empty
// result) rather than a wrong frame. `proxy exec` has always defaulted to the
// active content frame; this makes automation agree with it.
//
// frame:
//
//	"" / "content" — the app (default). Same-origin, so the shell can hand the
//	                 script to the iframe realm via an indirect eval, which runs
//	                 it in that realm's global scope: `window`/`document` are the
//	                 app's, with no wrapper for the caller to write.
//	"top"          — the shell itself. Needed to inspect proxy chrome (overlay,
//	                 indicator, panels), which is a real debugging surface — not
//	                 a fallback for a missing content frame.
//
// An unwrapped page (no shell, e.g. a direct non-proxied URL) has no content
// iframe; there "content" and "top" are the same document, which is exactly what
// walkthrough.js's own contentWin() concludes for the same case.
//
// The wrapper distinguishes those two cases by the shell's own role marker
// (window.__devtool_role === "chrome", stamped by the injector) instead of by
// whether the iframe lookup happened to hit. That distinction is load-bearing:
// an evaluate racing a navigation sees the shell before the app frame is ready,
// and the old `|| window` fallback silently ran the script in the SHELL — the
// exact wrong-frame failure the content default exists to remove. Now a wrapped
// shell WAITS for the app frame (bounded), and fails loud naming frame:"top" as
// the escape hatch; only a genuinely unwrapped page runs in window directly.
//
// The app frame counts as ready when its realm carries the injected content
// role, or — for non-instrumented documents the proxy did not inject — when it
// has fully loaded off about:blank. Evaluating in the transient about:blank
// realm would be lost the moment the real document lands.
func automationEvalScript(script, frame string) (string, error) {
	switch frame {
	case "", "content":
		lit, err := json.Marshal(script)
		if err != nil {
			return "", fmt.Errorf("invalid script: %w", err)
		}
		return "(async function(){" +
			"function target(){" +
			"var f=document.getElementById('" + contentFrameElementID + "');" +
			"if(!f){return window.__devtool_role==='chrome'?null:window;}" +
			"var w=f.contentWindow;if(!w){return null;}" +
			"try{" +
			"if(w.__devtool_role==='content'){return w;}" +
			"if(w.document&&w.document.readyState==='complete'&&w.location.href!=='about:blank'){return w;}" +
			"}catch(e){return null;}" +
			"return null;}" +
			"var deadline=Date.now()+" + fmt.Sprintf("%d", contentFrameWaitMs) + ";" +
			"for(;;){" +
			"var w=target();" +
			"if(w){return w.eval(" + string(lit) + ");}" +
			"if(Date.now()>deadline){throw new Error(" + contentFrameTimeoutMsg() + ");}" +
			"await new Promise(function(r){setTimeout(r," + fmt.Sprintf("%d", contentFramePollMs) + ");});" +
			"}})()", nil
	case "top":
		return script, nil
	default:
		return "", fmt.Errorf("unknown frame %q: use content (the app, default) or top (the proxy chrome shell)", frame)
	}
}

// contentFrameElementID is the shell's content iframe id — must match
// proxy/injector.go's contentFrameID.
const contentFrameElementID = "__devtool_content_frame"

// contentFrameWaitMs bounds how long a content-frame evaluate waits for the app
// frame to become ready inside a wrapped shell before failing loud; sized to
// cover an evaluate issued immediately after navigate on a slow app load.
// contentFramePollMs is the readiness re-check interval within that window.
const (
	contentFrameWaitMs = 5000
	contentFramePollMs = 50
)

// contentFrameTimeoutMsg is the JS string literal thrown when the app frame
// never becomes ready: the caller asked for the app, so running in the shell
// instead is never an acceptable substitute — the error names the real states
// (still loading vs. shell-only page) and the frame:"top" escape hatch.
func contentFrameTimeoutMsg() string {
	msg := fmt.Sprintf("content frame not ready after %ds: the proxy shell has no "+
		"loaded app frame (app still loading, or the page has no content). Retry "+
		`once the app is up, or pass frame:"top" to inspect the proxy shell itself.`,
		contentFrameWaitMs/1000)
	lit, _ := json.Marshal(msg)
	return string(lit)
}

// hubHandleAutomationEvaluate handles AUTOMATION EVALUATE command.

func (d *Daemon) hubHandleAutomationEvaluate(ctx context.Context, conn *hubpkg.Connection, cmd *hubproto.Command) error {
	config, _ := unmarshalCommand[struct {
		SessionID string `json:"session_id"`
		Script    string `json:"script"`
		Frame     string `json:"frame"`
	}](cmd)

	// Accept session_id from args
	if len(cmd.Args) >= 1 {
		config.SessionID = cmd.Args[0]
	}

	if config.SessionID == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "session_id required")
	}
	if config.Script == "" {
		return conn.WriteErr(hubproto.ErrInvalidArgs, "script required")
	}

	session, err := getSessionScoped(d, conn, config.SessionID, d.sessionm.GetWithPathFilter)
	if err != nil {
		return conn.WriteErr(hubproto.ErrNotFound, err.Error())
	}

	script, err := automationEvalScript(config.Script, config.Frame)
	if err != nil {
		return conn.WriteErr(hubproto.ErrInvalidArgs, err.Error())
	}

	var result interface{}
	// AwaitPromise: an async script resolves to its value instead of handing the
	// caller a pending promise it cannot unwrap. Without it, every caller has to
	// decompose async work into a chain of separate synchronous evaluates.
	if err := session.Run(cdp.Evaluate(script, &result, func(p *runtime.EvaluateParams) *runtime.EvaluateParams {
		return p.WithAwaitPromise(true)
	})); err != nil {
		return conn.WriteErr(hubproto.ErrInternal, fmt.Sprintf("evaluation failed: %v", err))
	}

	resp := map[string]interface{}{
		"success": true,
		"result":  result,
	}

	data, _ := json.Marshal(resp)
	return conn.WriteJSON(data)
}
