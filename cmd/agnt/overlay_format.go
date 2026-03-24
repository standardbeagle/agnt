package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// formatProxyEventText dispatches a proxy event to the appropriate formatter
// and returns the text to inject. Returns empty string for unknown event types.
func formatProxyEventText(event ProxyEvent, summarizer *overlay.AuditSummarizer) string {
	switch event.Type {
	case "panel_message":
		return formatPanelMessageText(event, summarizer)
	case "sketch":
		return formatSketchText(event)
	case "design_state":
		return formatDesignStateText(event)
	case "design_request":
		return formatDesignRequestText(event)
	case "design_chat":
		return formatDesignChatText(event)
	default:
		debug.Error("overlay", "unhandled proxy event type: %s (proxy_id=%s)", event.Type, event.ProxyID)
		return ""
	}
}

// formatPanelMessageText formats a panel_message event into text for the AI agent.
func formatPanelMessageText(event ProxyEvent, summarizer *overlay.AuditSummarizer) string {
	var data struct {
		Message     string `json:"message"`
		URL         string `json:"url"`
		Attachments []struct {
			Type     string          `json:"type"`
			ID       string          `json:"id"`
			Selector string          `json:"selector"`
			Tag      string          `json:"tag"`
			Text     string          `json:"text"`
			Summary  string          `json:"summary"`
			Area     *screenshotArea `json:"area"`
			Data     json.RawMessage `json:"data"`
		} `json:"attachments"`
		RequestNotification bool `json:"request_notification"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse panel_message: %v", err)
		return ""
	}

	auditReports, nonAuditAttachments := processAttachments(data.Attachments, data.Message, summarizer)
	return formatPanelMessageBody(event.ProxyID, data.URL, data.Message, auditReports, nonAuditAttachments)
}

// processAttachments separates audit attachments (with LLM summarization) from regular attachments.
func processAttachments(attachments []struct {
	Type     string          `json:"type"`
	ID       string          `json:"id"`
	Selector string          `json:"selector"`
	Tag      string          `json:"tag"`
	Text     string          `json:"text"`
	Summary  string          `json:"summary"`
	Area     *screenshotArea `json:"area"`
	Data     json.RawMessage `json:"data"`
}, userMessage string, summarizer *overlay.AuditSummarizer) ([]string, []attachmentInfo) {
	var auditReports []string
	var nonAuditAttachments []attachmentInfo

	for _, att := range attachments {
		if att.Type == "audit" && len(att.Data) > 0 {
			report := processAuditAttachment(att.Data, userMessage, summarizer)
			auditReports = append(auditReports, report)
		} else {
			info := attachmentInfo{
				Type:     att.Type,
				ID:       att.ID,
				Selector: att.Selector,
				Tag:      att.Tag,
				Text:     att.Text,
				Summary:  att.Summary,
				Area:     att.Area,
			}

			if len(att.Data) > 0 {
				var dataFields map[string]interface{}
				if err := json.Unmarshal(att.Data, &dataFields); err == nil {
					info.RawData = dataFields
					if fp, ok := dataFields["file_path"].(string); ok {
						info.FilePath = fp
					}
					if fn, ok := dataFields["file_name"].(string); ok {
						info.FileName = fn
					}
					if att.Type == "style-edit" {
						extractStyleEditData(&info, dataFields)
					}
				}
			}

			nonAuditAttachments = append(nonAuditAttachments, info)
		}
	}
	return auditReports, nonAuditAttachments
}

// processAuditAttachment processes a single audit attachment and returns a summary.
func processAuditAttachment(data json.RawMessage, userMessage string, summarizer *overlay.AuditSummarizer) string {
	var auditData struct {
		AuditType string          `json:"auditType"`
		Label     string          `json:"label"`
		Summary   string          `json:"summary"`
		Result    json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(data, &auditData); err != nil {
		debug.Warn("overlay", "failed to parse audit data: %v", err)
		return fmt.Sprintf("**Audit**: (parse error)")
	}

	if summarizer != nil && summarizer.IsAvailable() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()

		report, err := summarizer.SummarizeAudit(ctx, overlay.AuditData{
			AuditType: auditData.AuditType,
			Label:     auditData.Label,
			Summary:   auditData.Summary,
			Result:    auditData.Result,
		}, userMessage)

		if err != nil {
			debug.Warn("overlay", "audit summarization failed: %v", err)
			return fmt.Sprintf("**%s**: %s", auditData.Label, auditData.Summary)
		}
		return report
	}

	return fmt.Sprintf("**%s**: %s", auditData.Label, auditData.Summary)
}

// formatPanelMessageBody formats the panel message with structured headings.
// Each section uses a labeled heading (proxy:, page:, screenshot:, element:, etc.)
// so the AI agent can parse values directly into MCP tool parameters.
func formatPanelMessageBody(proxyID, pageURL, message string, auditReports []string, attachments []attachmentInfo) string {
	userMessage := message
	if userMessage == "" && len(auditReports) > 0 {
		userMessage = "Review and fix the issues found in this audit report."
	}

	var b strings.Builder
	b.WriteString("from agnt browser: " + userMessage + "\n")

	if proxyID != "" {
		b.WriteString("proxy: " + proxyID + "\n")
	}
	if pageURL != "" {
		b.WriteString("page: " + pageURL + "\n")
	}

	for _, att := range attachments {
		switch att.Type {
		case "screenshot":
			b.WriteString("\nscreenshot: ")
			if att.Area != nil {
				b.WriteString(fmt.Sprintf("%dx%d at (%d,%d)", att.Area.Width, att.Area.Height, att.Area.X, att.Area.Y))
			} else {
				b.WriteString("full page")
			}
			if att.Summary != "" {
				b.WriteString(" — " + att.Summary)
			}
			b.WriteString("\n")
			if att.FilePath != "" {
				b.WriteString("  file: " + att.FilePath + "\n")
			} else if att.ID != "" {
				b.WriteString("  id: " + att.ID + "\n")
			}

		case "element":
			b.WriteString("\nelement: " + att.Selector + "\n")
			formatElementMeta(&b, att)

		case "sketch":
			b.WriteString("\nsketch: ")
			if att.Summary != "" {
				b.WriteString(att.Summary)
			}
			b.WriteString("\n")
			if att.FilePath != "" {
				b.WriteString("  file: " + att.FilePath + "\n")
			}

		case "style-edit":
			b.WriteString("\nstyle-edit: " + att.Selector + "\n")
			for _, ch := range att.StyleChanges {
				scope := ch.Scope
				if scope == "" {
					scope = "inline"
				}
				b.WriteString(fmt.Sprintf("  %s: %s → %s (%s)\n", ch.Property, ch.Original, ch.Current, scope))
			}
			if att.ReactComponent != "" {
				b.WriteString("  component: " + att.ReactComponent)
				if att.ReactSource != "" {
					b.WriteString(" (" + att.ReactSource + ")")
				}
				b.WriteString("\n")
			}
			if att.ScreenshotBefore != "" {
				b.WriteString("  before: " + att.ScreenshotBefore + "\n")
			}
			if att.ScreenshotAfter != "" {
				b.WriteString("  after: " + att.ScreenshotAfter + "\n")
			}

		default:
			b.WriteString("\n" + att.Type + ": ")
			if att.Selector != "" {
				b.WriteString(att.Selector)
			}
			if att.Text != "" {
				b.WriteString(" — " + truncateText(att.Text, 60))
			}
			b.WriteString("\n")
			if att.FilePath != "" {
				b.WriteString("  file: " + att.FilePath + "\n")
			}
		}
	}

	for _, report := range auditReports {
		b.WriteString("\naudit:\n" + report + "\n")
	}

	if len(auditReports) > 0 {
		b.WriteString("\nall audit data: .agnt/audit/SUMMARY.md\n")
	}

	return b.String()
}

// formatElementMeta writes heuristically selected metadata lines for an element.
// Picks the most actionable fields: framework/component, semantic role, text content,
// ID/test ID, relevant classes, key attributes, computed layout, and event listeners.
func formatElementMeta(b *strings.Builder, att attachmentInfo) {
	d := att.RawData

	// Framework + component (most useful for finding the source code)
	if fw, _ := d["framework"].(string); fw != "" {
		comp, _ := d["component"].(string)
		if comp != "" {
			b.WriteString("  component: " + comp + " (" + fw + ")\n")
		} else {
			b.WriteString("  framework: " + fw + "\n")
		}
	}

	// Tag + semantic role
	line := "  tag: " + att.Tag
	if role, _ := d["role"].(string); role != "" {
		line += "  role: " + role
	}
	b.WriteString(line + "\n")

	// ID and test ID (direct selectors for code search)
	if id, _ := d["id"].(string); id != "" {
		b.WriteString("  id: " + id + "\n")
	}
	if tid, _ := d["data_testid"].(string); tid != "" {
		b.WriteString("  test-id: " + tid + "\n")
	} else if tid, _ = d["data_test_id"].(string); tid != "" {
		b.WriteString("  test-id: " + tid + "\n")
	}

	// Semantic classes (filter out utility/generated classes, keep meaningful ones)
	if classes, ok := d["classes"].([]interface{}); ok && len(classes) > 0 {
		var meaningful []string
		for _, c := range classes {
			cls, _ := c.(string)
			if cls == "" || len(cls) > 30 {
				continue
			}
			meaningful = append(meaningful, cls)
		}
		if len(meaningful) > 0 {
			if len(meaningful) > 5 {
				meaningful = meaningful[:5]
			}
			b.WriteString("  classes: " + strings.Join(meaningful, " ") + "\n")
		}
	}

	// Text content
	if att.Text != "" {
		b.WriteString("  text: " + truncateText(att.Text, 80) + "\n")
	}

	// Key attributes (href, name, type, placeholder, title, alt, aria-label)
	for _, key := range []string{"href", "name", "type", "placeholder", "title", "alt", "aria_label"} {
		if v, _ := d[key].(string); v != "" {
			b.WriteString("  " + strings.ReplaceAll(key, "_", "-") + ": " + truncateText(v, 60) + "\n")
		}
	}

	// Computed layout (only non-default values)
	for _, key := range []string{"display", "position", "overflow"} {
		if v, _ := d[key].(string); v != "" {
			b.WriteString("  " + key + ": " + v + "\n")
		}
	}

	// Event listeners
	if listeners, ok := d["listeners"].([]interface{}); ok && len(listeners) > 0 {
		var names []string
		for _, l := range listeners {
			if s, ok := l.(string); ok {
				names = append(names, s)
			}
		}
		if len(names) > 0 {
			b.WriteString("  listeners: " + strings.Join(names, ", ") + "\n")
		}
	}
}

// formatSketchText formats a sketch event into text for the AI agent.
func formatSketchText(event ProxyEvent) string {
	var data struct {
		FilePath     string `json:"file_path"`
		ElementCount int    `json:"element_count"`
		Description  string `json:"description"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse sketch: %v", err)
		return ""
	}

	if data.Description != "" {
		return data.Description + fmt.Sprintf("\n\n[Sketch: %s with %d elements]", data.FilePath, data.ElementCount)
	}
	return fmt.Sprintf("[Sketch saved: %s with %d elements]", data.FilePath, data.ElementCount)
}

// formatDesignStateText formats a design_state event into text for the AI agent.
func formatDesignStateText(event ProxyEvent) string {
	var data struct {
		Selector     string `json:"selector"`
		XPath        string `json:"xpath"`
		OriginalHTML string `json:"original_html"`
		ContextHTML  string `json:"context_html"`
		URL          string `json:"url"`
		Metadata     struct {
			Tag     string   `json:"tag"`
			ID      string   `json:"id"`
			Classes []string `json:"classes"`
			Text    string   `json:"text"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_state: %v", err)
		return ""
	}

	return formatDesignStateMessage(data.Selector, data.Metadata.Tag, data.Metadata.ID,
		data.Metadata.Classes, data.Metadata.Text, event.ProxyID)
}

// formatDesignStateMessage formats the design state message for UX design sessions.
func formatDesignStateMessage(selector, tag, id string, classes []string, textContent, proxyID string) string {
	text := fmt.Sprintf(`[🎨 Design Mode: Premium UX Design Session]

**Element Selected for Redesign:**
- Selector: %s`, selector)

	if tag != "" {
		text += fmt.Sprintf("\n- Element: <%s>", tag)
		if id != "" {
			text += fmt.Sprintf(` id="%s"`, id)
		}
		if len(classes) > 0 {
			text += fmt.Sprintf(` class="%s"`, strings.Join(classes, " "))
		}
	}
	if textContent != "" {
		textPreview := textContent
		if len(textPreview) > 50 {
			textPreview = textPreview[:50] + "..."
		}
		text += fmt.Sprintf("\n- Content: %q", textPreview)
	}

	text += fmt.Sprintf(`

**Your Mission:** Act as a world-class UX designer creating premium, million-dollar designs.

**Before designing, gather context using these diagnostic tools:**
1. Take a screenshot: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('design-context')"}
2. Check user interactions: proxylog {proxy_id: "%s", types: ["interaction"], limit: 20}
3. Review any errors: proxylog {proxy_id: "%s", types: ["error"]}
4. See page performance: proxylog {proxy_id: "%s", types: ["performance"]}
5. Inspect element details: proxy {action: "exec", id: "%s", code: "__devtool.inspect('%s')"}
6. Check accessibility: proxy {action: "exec", id: "%s", code: "__devtool.auditAccessibility()"}

**Design Requirements:**
Create 3-5 distinct, premium alternatives that:
- Follow modern design principles (visual hierarchy, whitespace, contrast)
- Are accessible (WCAG AA compliant)
- Feel polished and professional
- Each have a unique design direction (e.g., minimal, bold, playful, premium, corporate)

**To add each alternative:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<your complete HTML>')"}

Start by taking a screenshot to understand the visual context, then create your designs.`,
		proxyID, proxyID, proxyID, proxyID, proxyID, selector, proxyID, proxyID)

	return text
}

// formatDesignRequestText formats a design_request event into text for the AI agent.
func formatDesignRequestText(event ProxyEvent) string {
	var data struct {
		Selector          string `json:"selector"`
		XPath             string `json:"xpath"`
		CurrentHTML       string `json:"current_html"`
		OriginalHTML      string `json:"original_html"`
		ContextHTML       string `json:"context_html"`
		AlternativesCount int    `json:"alternatives_count"`
		URL               string `json:"url"`
		Metadata          struct {
			Tag     string   `json:"tag"`
			ID      string   `json:"id"`
			Classes []string `json:"classes"`
			Text    string   `json:"text"`
		} `json:"metadata"`
		ChatHistory []struct {
			Message string `json:"message"`
			Role    string `json:"role"`
		} `json:"chat_history"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_request: %v", err)
		return ""
	}

	return formatDesignRequestMessage(data.Selector, data.AlternativesCount,
		data.ChatHistory, data.CurrentHTML, event.ProxyID)
}

// formatDesignRequestMessage formats the design request message for continuation.
func formatDesignRequestMessage(selector string, altCount int, chatHistory []struct {
	Message string `json:"message"`
	Role    string `json:"role"`
}, currentHTML, proxyID string) string {
	text := fmt.Sprintf(`[🎨 Design Mode: More Premium Alternatives Requested]

**Element:** %s
**Existing alternatives:** %d

`, selector, altCount)

	if len(chatHistory) > 0 {
		text += "**User feedback/requests:**\n"
		for _, msg := range chatHistory {
			if msg.Role == "user" {
				text += fmt.Sprintf("- %s\n", msg.Message)
			}
		}
		text += "\n"
	}

	html := currentHTML
	if len(html) > 500 {
		html = html[:500] + "..."
	}
	text += fmt.Sprintf("**Current design:**\n%s\n", html)

	text += fmt.Sprintf(`
**Continue as a world-class UX designer.** Create 2-3 MORE fresh alternatives that:
- Are distinctly different from the %d existing options
- Push creative boundaries while staying functional
- Consider the user's feedback above (if any)

**Quick diagnostics if needed:**
- Screenshot: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('design-iteration')"}
- Recent clicks: proxylog {proxy_id: "%s", types: ["interaction"], limit: 10}
- Custom logs: proxylog {proxy_id: "%s", types: ["custom"]}

**Add each new alternative:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<your fresh HTML>')"}`,
		altCount, proxyID, proxyID, proxyID, proxyID)

	return text
}

// formatDesignChatText formats a design_chat event into text for the AI agent.
func formatDesignChatText(event ProxyEvent) string {
	var data struct {
		Message      string `json:"message"`
		Selector     string `json:"selector"`
		CurrentHTML  string `json:"current_html"`
		OriginalHTML string `json:"original_html"`
		URL          string `json:"url"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_chat: %v", err)
		return ""
	}

	currentHTML := data.CurrentHTML
	if len(currentHTML) > 400 {
		currentHTML = currentHTML[:400] + "..."
	}

	return fmt.Sprintf(`[🎨 Design Refinement Request]

**User says:** "%s"

**Element:** %s
**Current design:**
%s

**As a premium UX designer, refine the design based on this feedback.**

If you need more context:
- Screenshot current state: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('refinement')"}
- Check DOM mutations: proxylog {proxy_id: "%s", types: ["mutation"], limit: 10}
- View panel messages: proxylog {proxy_id: "%s", types: ["panel_message"]}
- Audit accessibility: proxy {action: "exec", id: "%s", code: "__devtool.auditAccessibility()"}

**Apply the refined design:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<refined HTML>')"}`,
		data.Message, data.Selector, currentHTML,
		event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID)
}
