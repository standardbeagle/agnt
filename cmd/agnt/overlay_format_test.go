package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// Tests for formatProxyEventText and all sub-formatters.
// These cover the proxy→overlay leg (formatting for PTY injection).

// ── helpers ─────────────────────────────────────────────────────────────────

func makeEvent(eventType, proxyID string, data interface{}) ProxyEvent {
	b, _ := json.Marshal(data)
	return ProxyEvent{
		Type:    eventType,
		ProxyID: proxyID,
		Data:    json.RawMessage(b),
	}
}

func assertContains(t *testing.T, s, substr string) {
	t.Helper()
	if !strings.Contains(s, substr) {
		t.Errorf("expected %q to contain %q", s, substr)
	}
}

func assertNotContains(t *testing.T, s, substr string) {
	t.Helper()
	if strings.Contains(s, substr) {
		t.Errorf("expected %q NOT to contain %q", s, substr)
	}
}

// ── formatProxyEventText dispatcher ─────────────────────────────────────────

func TestFormatProxyEventText_UnknownTypeReturnsEmpty(t *testing.T) {
	event := makeEvent("unknown_type", "proxy-1", map[string]interface{}{})
	result := formatProxyEventText(event, nil)
	if result != "" {
		t.Errorf("expected empty for unknown type, got %q", result)
	}
}

func TestFormatProxyEventText_PanelMessageDispatches(t *testing.T) {
	event := makeEvent("panel_message", "proxy-1", map[string]interface{}{
		"message": "hello",
		"url":     "http://localhost:3000",
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, "from agnt browser: hello")
}

func TestFormatProxyEventText_SketchDispatches(t *testing.T) {
	event := makeEvent("sketch", "proxy-1", map[string]interface{}{
		"description":   "Login wireframe",
		"file_path":     "/tmp/sketch.png",
		"element_count": 4,
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, "Login wireframe")
}

func TestFormatProxyEventText_DesignStateDispatches(t *testing.T) {
	event := makeEvent("design_state", "proxy-1", map[string]interface{}{
		"selector": ".hero-btn",
		"metadata": map[string]interface{}{"tag": "button"},
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, ".hero-btn")
	assertContains(t, result, "Design Mode")
}

func TestFormatProxyEventText_DesignRequestDispatches(t *testing.T) {
	event := makeEvent("design_request", "proxy-1", map[string]interface{}{
		"selector":           ".card",
		"alternatives_count": 2,
		"current_html":       "<div class=\"card\">Content</div>",
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, ".card")
	assertContains(t, result, "More Premium Alternatives")
}

func TestFormatProxyEventText_DesignChatDispatches(t *testing.T) {
	event := makeEvent("design_chat", "proxy-1", map[string]interface{}{
		"message":      "Make it bolder",
		"selector":     ".title",
		"current_html": "<h1 class=\"title\">Title</h1>",
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, "Make it bolder")
	assertContains(t, result, "Refinement")
}

func TestFormatProxyEventText_DesignEditDispatches(t *testing.T) {
	event := makeEvent("design_edit", "proxy-1", map[string]interface{}{
		"selector":        ".hero",
		"xpath":           "//*[@id=\"hero\"]",
		"oid":             "oid-3",
		"deltas":          map[string]string{"width": "320px", "height": "200px"},
		"computed_before": map[string]string{"width": "300px", "height": "180px"},
		"computed_after":  map[string]string{"width": "320px", "height": "200px"},
		"metadata":        map[string]interface{}{"tag": "section", "id": "hero"},
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, ".hero")
	assertContains(t, result, "Geometry Edit")
	assertContains(t, result, "width: 320px")
	assertContains(t, result, "300px → 320px")
	assertContains(t, result, "oid-3")
}

func TestFormatDesignEditText_FallsBackToXPathAndDefaults(t *testing.T) {
	// No selector, no oid, empty deltas → falls back to xpath, "(none)", "(no delta)".
	event := makeEvent("design_edit", "proxy-2", map[string]interface{}{
		"xpath":    "/html/body/div[2]",
		"metadata": map[string]interface{}{"tag": "div"},
	})
	result := formatDesignEditText(event)
	assertContains(t, result, "/html/body/div[2]")
	assertContains(t, result, "(none)")
	assertContains(t, result, "(no delta)")
}

// ── formatPanelMessageBody ───────────────────────────────────────────────────

func TestFormatPanelMessageBody_BasicMessage(t *testing.T) {
	result := formatPanelMessageBody("my-proxy", "http://localhost:3000/", "Fix the nav", nil, nil)

	assertContains(t, result, "from agnt browser: Fix the nav")
	assertContains(t, result, "proxy: my-proxy")
	assertContains(t, result, "page: http://localhost:3000/")
}

func TestFormatPanelMessageBody_EmptyMessageWithAudit(t *testing.T) {
	// When message is empty but audit reports exist, default message is substituted
	reports := []string{"**Accessibility**: 3 issues found"}
	result := formatPanelMessageBody("p1", "http://localhost/", "", reports, nil)

	assertContains(t, result, "Review and fix the issues found")
	assertContains(t, result, "**Accessibility**: 3 issues found")
	assertContains(t, result, "all audit data:")
}

func TestFormatPanelMessageBody_ScreenshotWithArea(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:     "screenshot",
			ID:       "ctx_ss1",
			Summary:  "Header region",
			FilePath: "/tmp/header.png",
			Area:     &screenshotArea{X: 0, Y: 0, Width: 1440, Height: 120},
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Check the header", nil, attachments)

	assertContains(t, result, "screenshot: 1440x120 at (0,0)")
	assertContains(t, result, "— Header region")
	assertContains(t, result, "file: /tmp/header.png")
}

func TestFormatPanelMessageBody_ScreenshotFullPage(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:    "screenshot",
			ID:      "ctx_ss2",
			Summary: "Full page",
			Area:    nil, // no area = full page
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "See page", nil, attachments)

	assertContains(t, result, "screenshot: full page")
	assertNotContains(t, result, "file:")
}

func TestFormatPanelMessageBody_ScreenshotFallsBackToID(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type: "screenshot",
			ID:   "ctx_fallback",
			Area: &screenshotArea{Width: 100, Height: 100},
			// no FilePath
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "msg", nil, attachments)

	assertContains(t, result, "id: ctx_fallback")
	assertNotContains(t, result, "file:")
}

func TestFormatPanelMessageBody_ElementAttachment(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:     "element",
			Selector: ".premium-card",
			Tag:      "div",
			Text:     "Card content",
			RawData: map[string]interface{}{
				"classes":   []interface{}{"premium-card", "active"},
				"framework": "react",
				"component": "PremiumCard",
			},
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Look at element", nil, attachments)

	assertContains(t, result, "element: .premium-card")
	assertContains(t, result, "tag: div")
	assertContains(t, result, "component: PremiumCard (react)")
	assertContains(t, result, "text: Card content")
}

func TestFormatPanelMessageBody_SketchAttachment(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:     "sketch",
			Summary:  "Mobile nav wireframe",
			FilePath: "/tmp/sketch-nav.png",
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Here is sketch", nil, attachments)

	assertContains(t, result, "sketch: Mobile nav wireframe")
	assertContains(t, result, "file: /tmp/sketch-nav.png")
}

func TestFormatPanelMessageBody_StyleEditAttachment(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:     "style-edit",
			Selector: ".button",
			StyleChanges: []styleChange{
				{Property: "color", Original: "#333", Current: "#fff", Scope: "inline"},
				{Property: "background", Original: "#fff", Current: "#007bff", Scope: "inline"},
			},
			ReactComponent: "Button",
			ReactSource:    "src/Button.tsx:42",
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Style changed", nil, attachments)

	assertContains(t, result, "style-edit: .button")
	assertContains(t, result, "color: #333 → #fff (inline)")
	assertContains(t, result, "background: #fff → #007bff (inline)")
	assertContains(t, result, "component: Button (src/Button.tsx:42)")
}

func TestFormatPanelMessageBody_ErrorAttachment(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:    "error",
			Summary: "TypeError: cannot read property",
			RawData: map[string]interface{}{
				"detail": "at src/App.tsx:42",
			},
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Got an error", nil, attachments)

	assertContains(t, result, "error: TypeError: cannot read property")
	assertContains(t, result, "at src/App.tsx:42")
}

func TestFormatPanelMessageBody_NetworkAttachment(t *testing.T) {
	attachments := []attachmentInfo{
		{
			Type:    "network",
			Summary: "POST /api/data → 500",
			RawData: map[string]interface{}{
				"detail": "database connection timeout",
			},
		},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "API failed", nil, attachments)

	assertContains(t, result, "network: POST /api/data → 500")
	assertContains(t, result, "database connection timeout")
}

func TestFormatPanelMessageBody_MultipleAttachments(t *testing.T) {
	attachments := []attachmentInfo{
		{Type: "screenshot", ID: "ctx_ss1", Area: &screenshotArea{Width: 800, Height: 600}},
		{Type: "element", Selector: "h1", Tag: "h1", Text: "Hello"},
	}
	result := formatPanelMessageBody("p1", "http://localhost/", "Multiple", nil, attachments)

	assertContains(t, result, "screenshot:")
	assertContains(t, result, "element: h1")
}

// ── formatElementMeta ────────────────────────────────────────────────────────

func TestFormatElementMeta_AllFields(t *testing.T) {
	var b strings.Builder
	att := attachmentInfo{
		Tag:  "button",
		Text: "Submit",
		RawData: map[string]interface{}{
			"framework":   "react",
			"component":   "SubmitButton",
			"id":          "submit-btn",
			"data_testid": "submit-button",
			"classes":     []interface{}{"btn", "btn-primary", "lg"},
			"type":        "submit",
			"display":     "inline-flex",
			"listeners":   []interface{}{"click", "focus"},
		},
	}
	formatElementMeta(&b, att)
	result := b.String()

	assertContains(t, result, "component: SubmitButton (react)")
	assertContains(t, result, "tag: button")
	assertContains(t, result, "id: submit-btn")
	assertContains(t, result, "test-id: submit-button")
	assertContains(t, result, "classes: btn btn-primary lg")
	assertContains(t, result, "text: Submit")
	assertContains(t, result, "type: submit")
	assertContains(t, result, "display: inline-flex")
	assertContains(t, result, "listeners: click, focus")
}

func TestFormatElementMeta_ClassesTruncatedAt5(t *testing.T) {
	var b strings.Builder
	att := attachmentInfo{
		Tag: "div",
		RawData: map[string]interface{}{
			"classes": []interface{}{"a", "b", "c", "d", "e", "f", "g"},
		},
	}
	formatElementMeta(&b, att)
	result := b.String()

	// Should have at most 5 classes
	classLine := ""
	for _, line := range strings.Split(result, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "classes:") {
			classLine = line
			break
		}
	}
	parts := strings.Fields(strings.TrimPrefix(strings.TrimSpace(classLine), "classes:"))
	if len(parts) > 5 {
		t.Errorf("expected at most 5 classes, got %d: %v", len(parts), parts)
	}
}

// ── formatSketchText ─────────────────────────────────────────────────────────

func TestFormatSketchText_WithDescription(t *testing.T) {
	event := makeEvent("sketch", "p1", map[string]interface{}{
		"description":   "Login flow wireframe",
		"file_path":     "/tmp/sketch.png",
		"element_count": 7,
	})
	result := formatSketchText(event)

	assertContains(t, result, "Login flow wireframe")
	assertContains(t, result, "/tmp/sketch.png")
	assertContains(t, result, "7 elements")
}

func TestFormatSketchText_NoDescription(t *testing.T) {
	event := makeEvent("sketch", "p1", map[string]interface{}{
		"file_path":     "/tmp/sketch-2.png",
		"element_count": 3,
	})
	result := formatSketchText(event)

	assertContains(t, result, "Sketch saved:")
	assertContains(t, result, "/tmp/sketch-2.png")
	assertContains(t, result, "3 elements")
}

func TestFormatSketchText_InvalidJSON(t *testing.T) {
	event := ProxyEvent{Type: "sketch", ProxyID: "p1", Data: json.RawMessage(`{invalid}`)}
	result := formatSketchText(event)
	if result != "" {
		t.Errorf("expected empty for invalid JSON, got %q", result)
	}
}

// ── formatDesignStateMessage ─────────────────────────────────────────────────

func TestFormatDesignStateMessage_Full(t *testing.T) {
	result := formatDesignStateMessage(".hero-btn", "button", "hero", []string{"btn", "primary"}, "Click me", "proxy-abc")

	assertContains(t, result, "Design Mode")
	assertContains(t, result, "Selector: .hero-btn")
	assertContains(t, result, "<button")
	assertContains(t, result, `id="hero"`)
	assertContains(t, result, `class="btn primary"`)
	assertContains(t, result, `"Click me"`)
	// Verify proxy ID is embedded in tool call examples
	assertContains(t, result, `proxy-abc`)
	assertContains(t, result, `__devtool.screenshot`)
	assertContains(t, result, `__devtool_design.addAlternative`)
}

func TestFormatDesignStateMessage_LongTextTruncated(t *testing.T) {
	longText := strings.Repeat("x", 100)
	result := formatDesignStateMessage(".el", "div", "", nil, longText, "p1")

	// Text should be truncated at 50 chars + "..."
	assertContains(t, result, "...")
	if strings.Contains(result, longText) {
		t.Error("long text should be truncated")
	}
}

func TestFormatDesignStateMessage_NoTextContent(t *testing.T) {
	result := formatDesignStateMessage(".icon", "span", "", nil, "", "p1")

	// No "Content:" line when text is empty
	assertNotContains(t, result, "Content:")
}

// ── formatDesignRequestMessage ───────────────────────────────────────────────

func TestFormatDesignRequestMessage_WithHistory(t *testing.T) {
	history := []struct {
		Message string `json:"message"`
		Role    string `json:"role"`
	}{
		{Message: "Make it bigger", Role: "user"},
		{Message: "Done", Role: "assistant"},
		{Message: "Now add shadow", Role: "user"},
	}
	result := formatDesignRequestMessage(".card", 2, history, "<div>old</div>", "proxy-xyz")

	assertContains(t, result, "More Premium Alternatives")
	assertContains(t, result, "**Element:** .card")
	assertContains(t, result, "**Existing alternatives:** 2")
	// Only user messages shown
	assertContains(t, result, "Make it bigger")
	assertContains(t, result, "Now add shadow")
	// Assistant messages not shown
	assertNotContains(t, result, "Done")
	assertContains(t, result, "proxy-xyz")
}

func TestFormatDesignRequestMessage_CurrentHTMLTruncated(t *testing.T) {
	longHTML := "<div>" + strings.Repeat("x", 600) + "</div>"
	result := formatDesignRequestMessage(".el", 0, nil, longHTML, "p1")

	// HTML should be truncated at 500 chars
	assertContains(t, result, "...")
	if strings.Contains(result, longHTML) {
		t.Error("long HTML should be truncated")
	}
}

// ── formatDesignChatText ─────────────────────────────────────────────────────

func TestFormatDesignChatText_Full(t *testing.T) {
	event := makeEvent("design_chat", "proxy-dc", map[string]interface{}{
		"message":      "Make the border radius larger",
		"selector":     ".card",
		"current_html": "<div class=\"card\" style=\"border-radius:4px\">Content</div>",
	})
	result := formatDesignChatText(event)

	assertContains(t, result, "Design Refinement")
	assertContains(t, result, "Make the border radius larger")
	assertContains(t, result, "**Element:** .card")
	assertContains(t, result, "proxy-dc")
	assertContains(t, result, `__devtool_design.addAlternative`)
}

func TestFormatDesignChatText_CurrentHTMLTruncated(t *testing.T) {
	event := makeEvent("design_chat", "p1", map[string]interface{}{
		"message":      "fix it",
		"selector":     ".el",
		"current_html": strings.Repeat("x", 500),
	})
	result := formatDesignChatText(event)

	assertContains(t, result, "...")
}

func TestFormatDesignChatText_InvalidJSON(t *testing.T) {
	event := ProxyEvent{Type: "design_chat", ProxyID: "p1", Data: json.RawMessage(`{bad}`)}
	result := formatDesignChatText(event)
	if result != "" {
		t.Errorf("expected empty for invalid JSON, got %q", result)
	}
}

// ── panel_message end-to-end through formatProxyEventText ───────────────────

func TestFormatProxyEventText_PanelMessageWithAllAttachmentTypes(t *testing.T) {
	event := makeEvent("panel_message", "angel-d057:web:localhost-6112", map[string]interface{}{
		"message": "Review this layout",
		"url":     "http://localhost:30168/",
		"attachments": []interface{}{
			map[string]interface{}{
				"type":    "screenshot",
				"id":      "ctx_ss",
				"summary": "Header",
				"area":    map[string]interface{}{"x": 0, "y": 0, "width": 1440, "height": 80},
				"data":    map[string]interface{}{"file_path": "/tmp/header.png"},
			},
			map[string]interface{}{
				"type":     "element",
				"id":       "ctx_el",
				"selector": ".header",
				"tag":      "header",
				"text":     "Site Header",
				"classes":  []interface{}{"header", "sticky"},
			},
		},
	})
	result := formatProxyEventText(event, nil)

	assertContains(t, result, "from agnt browser: Review this layout")
	assertContains(t, result, "proxy: angel-d057:web:localhost-6112")
	assertContains(t, result, "page: http://localhost:30168/")
	assertContains(t, result, "screenshot: 1440x80 at (0,0)")
	assertContains(t, result, "— Header")
	assertContains(t, result, "element: .header")
	assertContains(t, result, "tag: header")
	assertContains(t, result, "text: Site Header")
}

// ── formatBrowserErrorText ───────────────────────────────────────────────────

func TestFormatBrowserErrorText_Basic(t *testing.T) {
	event := makeEvent("browser_error", "proxy-1", map[string]interface{}{
		"message": "Cannot read property 'map' of undefined",
		"source":  "src/App.tsx",
		"lineno":  42,
		"colno":   15,
		"url":     "http://localhost:3000/dashboard",
	})
	result := formatBrowserErrorText(event)
	assertContains(t, result, "[browser error] Cannot read property 'map' of undefined")
	assertContains(t, result, "source: src/App.tsx:42:15")
	assertContains(t, result, "page: http://localhost:3000/dashboard")
	assertContains(t, result, "proxy: proxy-1")
}

func TestFormatBrowserErrorText_WithStack(t *testing.T) {
	event := makeEvent("browser_error", "p1", map[string]interface{}{
		"message": "TypeError",
		"stack":   "Error\n    at node_modules/react/index.js:1:1\n    at src/App.tsx:42:15",
	})
	result := formatBrowserErrorText(event)
	assertContains(t, result, "[browser error] TypeError")
	assertContains(t, result, "src/App.tsx:42:15")
	assertNotContains(t, result, "node_modules")
}

func TestFormatBrowserErrorText_InvalidJSON(t *testing.T) {
	event := ProxyEvent{Type: "browser_error", ProxyID: "p1", Data: json.RawMessage(`{invalid}`)}
	result := formatBrowserErrorText(event)
	if result != "" {
		t.Errorf("expected empty for invalid JSON, got %q", result)
	}
}

func TestFormatProxyEventText_BrowserErrorDispatches(t *testing.T) {
	event := makeEvent("browser_error", "proxy-1", map[string]interface{}{
		"message": "test error",
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, "[browser error] test error")
}

// ── formatHTTPErrorText ──────────────────────────────────────────────────────

func TestFormatHTTPErrorText_500(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":        "POST",
		"url":           "/api/users",
		"status_code":   500,
		"response_body": `{"error":"database connection timeout"}`,
	})
	result := formatHTTPErrorText(event)
	assertContains(t, result, "[http error] 500 POST /api/users")
	assertContains(t, result, "database connection timeout")
	assertContains(t, result, "proxy: proxy-1")
}

func TestFormatHTTPErrorText_404(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":      "GET",
		"url":         "/api/users",
		"status_code": 404,
	})
	result := formatHTTPErrorText(event)
	assertContains(t, result, "[http warning] 404 GET /api/users")
}

func TestFormatHTTPErrorText_Static404Filtered(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":      "GET",
		"url":         "/static/app.js",
		"status_code": 404,
	})
	result := formatHTTPErrorText(event)
	if result != "" {
		t.Errorf("expected empty for static asset 404, got %q", result)
	}
}

func TestFormatHTTPErrorText_HMR404Filtered(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":      "GET",
		"url":         "/@vite/client",
		"status_code": 404,
	})
	result := formatHTTPErrorText(event)
	if result != "" {
		t.Errorf("expected empty for HMR 404, got %q", result)
	}
}

func TestFormatHTTPErrorText_301Filtered(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":      "GET",
		"url":         "/old-path",
		"status_code": 301,
	})
	result := formatHTTPErrorText(event)
	if result != "" {
		t.Errorf("expected empty for redirect, got %q", result)
	}
}

func TestFormatHTTPErrorText_ClientCanceled502Filtered(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":        "GET",
		"url":           "/api/auth/me",
		"status_code":   502,
		"response_body": "Client canceled the request before the response completed.",
	})
	result := formatHTTPErrorText(event)
	if result != "" {
		t.Errorf("expected empty for client-canceled 502, got %q", result)
	}
}

func TestFormatHTTPErrorText_HTMLBody(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":        "GET",
		"url":           "/api/data",
		"status_code":   500,
		"response_body": "<html><head><title>Internal Server Error</title></head><body>Error</body></html>",
	})
	result := formatHTTPErrorText(event)
	assertContains(t, result, "Internal Server Error")
}

func TestFormatHTTPErrorText_InvalidJSON(t *testing.T) {
	event := ProxyEvent{Type: "http_error", ProxyID: "p1", Data: json.RawMessage(`{bad}`)}
	result := formatHTTPErrorText(event)
	if result != "" {
		t.Errorf("expected empty for invalid JSON, got %q", result)
	}
}

func TestFormatProxyEventText_HTTPErrorDispatches(t *testing.T) {
	event := makeEvent("http_error", "proxy-1", map[string]interface{}{
		"method":      "GET",
		"url":         "/api/broken",
		"status_code": 500,
	})
	result := formatProxyEventText(event, nil)
	assertContains(t, result, "[http error] 500 GET /api/broken")
}

// ── isHTTPNoise ──────────────────────────────────────────────────────────────

func TestIsHTTPNoise(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		method       string
		url          string
		responseBody string
		expect       bool
	}{
		{"redirect 301", 301, "GET", "/old", "", true},
		{"redirect 302", 302, "GET", "/old", "", true},
		{"not modified 304", 304, "GET", "/page", "", true},
		{"static JS 404", 404, "GET", "/app.js", "", true},
		{"static CSS 404", 404, "GET", "/style.css", "", true},
		{"static map 404", 404, "GET", "/app.js.map", "", true},
		{"HMR at 404", 404, "GET", "/@vite/client", "", true},
		{"webpack 404", 404, "GET", "/__webpack/hmr", "", true},
		{"API 404", 404, "GET", "/api/users", "", false},
		{"500 error", 500, "POST", "/api/data", "", false},
		{"400 error", 400, "POST", "/api/submit", "", false},
		{"client canceled 502", 502, "GET", "/api/auth/me", "Client canceled the request before the response completed.", true},
		{"real 502", 502, "GET", "/api/auth/me", "upstream connect error", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isHTTPNoise(tt.statusCode, tt.method, tt.url, tt.responseBody)
			if result != tt.expect {
				t.Errorf("isHTTPNoise(%d, %s, %s) = %v, want %v", tt.statusCode, tt.method, tt.url, result, tt.expect)
			}
		})
	}
}

// ── firstAppFrame ────────────────────────────────────────────────────────────

func TestFirstAppFrame(t *testing.T) {
	tests := []struct {
		name   string
		stack  string
		expect string
	}{
		{
			name:   "skips node_modules",
			stack:  "Error\n    at node_modules/react/index.js:1:1\n    at src/App.tsx:42:15",
			expect: "at src/App.tsx:42:15",
		},
		{
			name:   "skips Error prefix",
			stack:  "Error: something\n    at real-file.js:10:5",
			expect: "at real-file.js:10:5",
		},
		{
			name:   "skips webpack-internal",
			stack:  "    at webpack-internal:///./src/index.js:10:5\n    at src/component.tsx:3:1",
			expect: "at src/component.tsx:3:1",
		},
		{
			name:   "empty stack",
			stack:  "",
			expect: "",
		},
		{
			name:   "only node_modules",
			stack:  "Error\n    at node_modules/foo/bar.js:1:1",
			expect: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := firstAppFrame(tt.stack)
			if result != tt.expect {
				t.Errorf("firstAppFrame(%q) = %q, want %q", tt.stack, result, tt.expect)
			}
		})
	}
}
