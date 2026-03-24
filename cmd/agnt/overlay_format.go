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

// formatPanelMessageBody formats the panel message with audit reports and attachments.
func formatPanelMessageBody(proxyID, pageURL, message string, auditReports []string, attachments []attachmentInfo) string {
	userMessage := message
	if userMessage == "" && len(auditReports) > 0 {
		userMessage = "Review and fix the issues found in this audit report."
	}

	// Source identification — proxy_id and page URL are machine-readable values
	// that can be passed directly to MCP tool parameters like proxylog, get_errors, proxy exec.
	text := "from agnt browser: " + userMessage
	if proxyID != "" {
		text += "\nproxy_id: " + proxyID
	}
	if pageURL != "" {
		text += "\npage: " + pageURL
	}

	if len(auditReports) > 0 {
		text += "\n\n[Audit Report]\n"
		for _, report := range auditReports {
			text += report + "\n"
		}
	}

	if len(attachments) > 0 {
		text += "\n\n[Attachments]\n"
		for i, att := range attachments {
			text += fmt.Sprintf("%d. %s", i+1, att.Type)
			switch att.Type {
			case "screenshot":
				if att.Area != nil {
					text += fmt.Sprintf(" (area: %dx%d at %d,%d)", att.Area.Width, att.Area.Height, att.Area.X, att.Area.Y)
				}
				if att.Summary != "" {
					text += fmt.Sprintf(": %s", att.Summary)
				}
				if att.FilePath != "" {
					text += fmt.Sprintf("\n   → %s", att.FilePath)
				} else {
					text += fmt.Sprintf("\n   → (file path not available - ID: %s)", att.ID)
				}
				text += "\n"
			case "element":
				if att.Selector != "" {
					text += fmt.Sprintf(": %s", att.Selector)
				}
				if att.Tag != "" {
					text += fmt.Sprintf(" (%s)", att.Tag)
				}
				if att.Text != "" {
					text += fmt.Sprintf(" - %q", truncateText(att.Text, 50))
				}
				if att.FilePath != "" {
					text += fmt.Sprintf("\n   → %s", att.FilePath)
				}
				text += "\n"
			case "sketch":
				if att.Summary != "" {
					text += fmt.Sprintf(": %s", att.Summary)
				}
				text += "\n"
				if att.FilePath != "" {
					text += fmt.Sprintf("   → %s\n", att.FilePath)
				}
			case "style-edit":
				if att.Selector != "" {
					text += fmt.Sprintf(" (selector: %s", att.Selector)
					if len(att.StyleChanges) > 0 {
						text += fmt.Sprintf(", %d change", len(att.StyleChanges))
						if len(att.StyleChanges) != 1 {
							text += "s"
						}
					}
					text += ")"
				}
				text += "\n"
				for _, ch := range att.StyleChanges {
					scope := ch.Scope
					if scope == "" {
						scope = "inline"
					}
					text += fmt.Sprintf("   %s: %s → %s (%s)\n", ch.Property, ch.Original, ch.Current, scope)
				}
				if att.ReactComponent != "" {
					react := "   React: " + att.ReactComponent
					if att.ReactSource != "" {
						react += " (" + att.ReactSource + ")"
					}
					text += react + "\n"
				}
				if att.ScreenshotBefore != "" {
					text += fmt.Sprintf("   → before: %s\n", att.ScreenshotBefore)
				}
				if att.ScreenshotAfter != "" {
					text += fmt.Sprintf("   → after: %s\n", att.ScreenshotAfter)
				}
			default:
				if att.Selector != "" {
					text += fmt.Sprintf(": %s", att.Selector)
				}
				if att.Text != "" {
					text += fmt.Sprintf(" (%s)", att.Text)
				}
				if att.FilePath != "" {
					text += fmt.Sprintf("\n   → %s", att.FilePath)
				}
				text += "\n"
			}
		}
	}

	if len(auditReports) > 0 || len(attachments) > 0 {
		text += "\n\n[All Audit Data]\n"
		text += "Summary of all files: .agnt/audit/SUMMARY.md\n"
	}

	return text
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
