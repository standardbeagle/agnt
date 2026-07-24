package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/standardbeagle/agnt/internal/debug"
	"github.com/standardbeagle/agnt/internal/overlay"
)

// formatProxyEventText dispatches a proxy event to the appropriate formatter
// and returns the text to inject into the agent's PTY stdin. Returns empty
// string for unknown event types.
//
// The returned text is deliberately NOT sanitized here. Every injection path —
// this one, the scanner alert batches, and the palette/WebSocket "type" verbs —
// funnels through Overlay.typeText, the single enforced boundary that coerces
// injected content to clean UTF-8 text (see sanitizeInjectedText there). Keeping
// the guard at the write boundary rather than in each formatter means a new
// formatter or injection source cannot forget it.
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
	case "design_edit":
		return formatDesignEditText(event)
	case "browser_error":
		return formatBrowserErrorText(event)
	case "http_error":
		return formatHTTPErrorText(event)
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
		return "**Audit**: (parse error)"
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

		case "error":
			b.WriteString("\nerror: " + att.Summary + "\n")
			if detail, _ := att.RawData["detail"].(string); detail != "" {
				b.WriteString(detail + "\n")
			}

		case "network":
			b.WriteString("\nnetwork: " + att.Summary + "\n")
			if detail, _ := att.RawData["detail"].(string); detail != "" {
				b.WriteString(detail + "\n")
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
		Scheme *designScheme `json:"scheme"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_state: %v", err)
		return ""
	}

	return formatDesignStateMessage(data.Selector, data.Metadata.Tag, data.Metadata.ID,
		data.Metadata.Classes, data.Metadata.Text, event.ProxyID, data.Scheme)
}

// designScheme mirrors proxy.DesignScheme for prompt rendering (snake_case as
// carried over the overlay socket from the marshaled DesignState).
type designScheme struct {
	Palette      []string          `json:"palette"`
	FontFamilies []string          `json:"font_families"`
	FontSizes    []string          `json:"font_sizes"`
	FontWeights  []string          `json:"font_weights"`
	Spacing      []string          `json:"spacing"`
	Radius       []string          `json:"radius"`
	Shadows      []string          `json:"shadows"`
	CSSVars      map[string]string `json:"css_vars"`
}

// formatSchemeBlock renders the extracted design tokens as a compact markdown
// block. Returns "" when the scheme is nil/empty so callers can fall back to the
// legacy prompt.
func formatSchemeBlock(s *designScheme) string {
	if s == nil {
		return ""
	}
	var b strings.Builder
	row := func(label string, vals []string) {
		if len(vals) > 0 {
			b.WriteString(fmt.Sprintf("- %s: %s\n", label, strings.Join(vals, ", ")))
		}
	}
	row("Palette", s.Palette)
	row("Font families", s.FontFamilies)
	row("Font sizes", s.FontSizes)
	row("Font weights", s.FontWeights)
	row("Spacing steps", s.Spacing)
	row("Radius", s.Radius)
	row("Shadows", s.Shadows)
	if len(s.CSSVars) > 0 {
		vars := make([]string, 0, len(s.CSSVars))
		for k, v := range s.CSSVars {
			vars = append(vars, fmt.Sprintf("%s: %s", k, v))
		}
		sort.Strings(vars)
		b.WriteString(fmt.Sprintf("- CSS variables: %s\n", strings.Join(vars, "; ")))
	}
	if b.Len() == 0 {
		return ""
	}
	return "\n**App design scheme (extracted live — keep variations within these tokens):**\n" + b.String()
}

// proxyExec renders the MCP `proxy` exec tool call the agent issues to run code
// in the page. Single source of truth for the exec invocation shape.
func proxyExec(proxyID, code string) string {
	return fmt.Sprintf("proxy {action: \"exec\", id: %q, code: %q}", proxyID, code)
}

// addAltExec renders the exec snippet that pushes one labeled alternative back
// to the browser. Centralizes the addAlternative(html, {label, note}) contract
// so the draft/variation labels stay in lockstep with design.js.
func addAltExec(proxyID, label, note string) string {
	code := fmt.Sprintf("__devtool_design.addAlternative('<html>', {label:'%s', note:'%s'})", label, note)
	return proxyExec(proxyID, code)
}

// designOnSchemeRules is the constraint block every generated variation must
// satisfy. Shared by the initial and continuation prompts so the rules stay in
// one place.
const designOnSchemeRules = `Each variation: follow design.md; note its unique direction in the 'note' field.`

// formatDesignStateMessage formats the design state message for UX design sessions.
func formatDesignStateMessage(selector, tag, id string, classes []string, textContent, proxyID string, scheme *designScheme) string {
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

	text += formatSchemeBlock(scheme)

	text += fmt.Sprintf(`

**Your Mission:** Act as a world-class UX designer. Deliver a fast first look, then polished on-scheme variations.

**Gather context first (cheap, do in parallel):**
1. Screenshot: %s
2. Inspect element: %s
3. Accessibility: %s

**STEP 0 — Ensure DESIGN.md (do this FIRST, then announce it):**
Check the project root for DESIGN.md. If MISSING, create it NOW in the google-labs-code format
(https://github.com/google-labs-code/design.md) — YAML frontmatter design tokens + ordered '##'
sections — derived from the App design scheme above:
- colors      ← palette
- typography  ← font families / sizes / weights
- rounded     ← radius
- spacing     ← spacing steps
- components  ← infer from the selected element + its sibling context
Then post one line so the developer sees it: "Created DESIGN.md — captured the app's design system".
If DESIGN.md ALREADY EXISTS, read it and treat it as the AUTHORITATIVE design system: every variation
must conform to it (it overrides the extracted scheme on any conflict).

**Generation strategy — fast draft first, then concurrent variations:**

STEP 1 — Fast draft (do this immediately, before anything else):
Dispatch ONE low-effort subagent to produce a single quick restyle. It must call addAlternative
with a draft label so the user sees something instantly:
  %s

STEP 2 — On-scheme variations (dispatch CONCURRENTLY in a single message — one subagent each):
Create 3-4 distinct, polished alternatives. Each subagent independently authors ONE variation and
calls addAlternative as soon as it is done (they stream in as they finish):
  %s
Or push the whole set in one call (preferred — avoids racing a recompile):
  proxy {action: "exec", id: %q, code: "__devtool_design.addAlternatives([{html:'<html>', label:'variation', note:'<direction>'}])"}

Previews are NON-INVASIVE: they render in an isolated side-by-side / overlay panel, never written
into the app's live DOM — so produce complete standalone HTML for the element's content.

%s

Run STEP 1 and STEP 2 so the draft lands first and the variations arrive concurrently right after.`,
		proxyExec(proxyID, "__devtool.screenshot('design-context')"),
		proxyExec(proxyID, "__devtool.inspect('"+selector+"')"),
		proxyExec(proxyID, "__devtool.auditAccessibility()"),
		addAltExec(proxyID, "draft", "Fast draft"),
		addAltExec(proxyID, "variation", "<direction>"),
		proxyID,
		designOnSchemeRules)

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
		Scheme         *designScheme `json:"scheme"`
		ScreenshotPath string        `json:"screenshot_path"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_request: %v", err)
		return ""
	}

	text := formatDesignRequestMessage(data.Selector, data.AlternativesCount,
		data.ChatHistory, data.CurrentHTML, event.ProxyID, data.Scheme)
	return text + screenshotBlock(data.ScreenshotPath)
}

// screenshotBlock appends an agent-readable pointer to the saved segment PNG.
// PTY stdin can't inline images, so the agent reads the file from disk.
func screenshotBlock(path string) string {
	if path == "" {
		return ""
	}
	return fmt.Sprintf("\n\n**Screenshot of the live segment (read it):** %s\n", path)
}

// formatDesignRequestMessage formats the design request message for continuation.
func formatDesignRequestMessage(selector string, altCount int, chatHistory []struct {
	Message string `json:"message"`
	Role    string `json:"role"`
}, currentHTML, proxyID string, scheme *designScheme) string {
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

	text += formatSchemeBlock(scheme)

	text += fmt.Sprintf(`

**Continue as a world-class UX designer.** Dispatch 2-3 subagents CONCURRENTLY, each authoring ONE
fresh alternative that is distinctly different from the %d existing options and considers the
feedback above. Each calls addAlternative as soon as it finishes:
  %s

%s

**Quick diagnostics if needed:** %s`,
		altCount,
		addAltExec(proxyID, "variation", "<direction>"),
		designOnSchemeRules,
		proxyExec(proxyID, "__devtool.screenshot('design-iteration')"))

	return text
}

// formatDesignChatText formats a design_chat event into text for the AI agent.
func formatDesignChatText(event ProxyEvent) string {
	var data struct {
		Message        string `json:"message"`
		Selector       string `json:"selector"`
		CurrentHTML    string `json:"current_html"`
		OriginalHTML   string `json:"original_html"`
		URL            string `json:"url"`
		ScreenshotPath string `json:"screenshot_path"`
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
%s
**As a premium UX designer, refine the design based on this feedback.**

If you need more context:
- Screenshot current state: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('refinement')"}
- Check DOM mutations: proxylog {proxy_id: "%s", types: ["mutation"], limit: 10}
- View panel messages: proxylog {proxy_id: "%s", types: ["panel_message"]}
- Audit accessibility: proxy {action: "exec", id: "%s", code: "__devtool.auditAccessibility()"}

**Apply the refined design:**
proxy {action: "exec", id: "%s", code: "__devtool_design.addAlternative('<refined HTML>')"}`,
		data.Message, data.Selector, currentHTML, screenshotBlock(data.ScreenshotPath),
		event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID, event.ProxyID)
}

// formatDesignEditText formats a design_edit event (a committed
// direct-manipulation geometry edit) into a code-change request for the AI
// agent. The contract is selector + computed before→after delta; the agent
// owns source-finding and writes the real CSS/JSX diff.
func formatDesignEditText(event ProxyEvent) string {
	var data struct {
		Selector       string            `json:"selector"`
		XPath          string            `json:"xpath"`
		OID            string            `json:"oid"`
		Deltas         map[string]string `json:"deltas"`
		ComputedBefore map[string]string `json:"computed_before"`
		ComputedAfter  map[string]string `json:"computed_after"`
		URL            string            `json:"url"`
		Metadata       struct {
			Tag string `json:"tag"`
			ID  string `json:"id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse design_edit: %v", err)
		return ""
	}

	target := data.Selector
	if target == "" {
		target = data.XPath
	}

	return fmt.Sprintf(`[📐 Design Mode: Geometry Edit Committed]

**Element:** %s (%s)
**OID:** %s

**Requested change (apply to the real source):**
%s

**Before → After (computed):**
- width: %s → %s
- height: %s → %s

Write the corresponding CSS/JSX source change for this element. Use the
selector to locate it; the OID is a fallback locator carried in the page
(data-devtool-oid="%s") when the selector is weak. Do not rely on the live
override stylesheet — it is a non-destructive preview only.

Locate the source if needed:
- Screenshot current state: proxy {action: "exec", id: "%s", code: "__devtool.screenshot('geometry-edit')"}
- Recent edits: proxylog {proxy_id: "%s", types: ["design_edit"], limit: 10}`,
		target, data.Metadata.Tag, valueOr(data.OID, "(none)"),
		formatDeltaLines(data.Deltas),
		valueOr(data.ComputedBefore["width"], "?"), valueOr(data.ComputedAfter["width"], "?"),
		valueOr(data.ComputedBefore["height"], "?"), valueOr(data.ComputedAfter["height"], "?"),
		data.OID, event.ProxyID, event.ProxyID)
}

// formatDeltaLines renders a property→value delta map as stably ordered
// "- prop: value" bullet lines.
func formatDeltaLines(deltas map[string]string) string {
	if len(deltas) == 0 {
		return "- (no delta)"
	}
	keys := make([]string, 0, len(deltas))
	for k := range deltas {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	lines := make([]string, len(keys))
	for i, k := range keys {
		lines[i] = fmt.Sprintf("- %s: %s", k, deltas[k])
	}
	return strings.Join(lines, "\n")
}

func valueOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// formatBrowserErrorText formats a browser_error event into compact text for PTY injection.
func formatBrowserErrorText(event ProxyEvent) string {
	var data struct {
		Message string `json:"message"`
		Source  string `json:"source"`
		LineNo  int    `json:"lineno"`
		ColNo   int    `json:"colno"`
		Error   string `json:"error"`
		Stack   string `json:"stack"`
		URL     string `json:"url"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse browser_error: %v", err)
		return ""
	}

	var b strings.Builder
	b.WriteString("[browser error] ")
	b.WriteString(data.Message)

	if data.Source != "" {
		b.WriteString("\n  source: " + data.Source)
		if data.LineNo > 0 {
			b.WriteString(fmt.Sprintf(":%d:%d", data.LineNo, data.ColNo))
		}
	}

	// Extract first application code frame from stack
	if data.Stack != "" {
		frame := firstAppFrame(data.Stack)
		if frame != "" {
			b.WriteString("\n  at " + frame)
		}
	}

	if data.URL != "" {
		b.WriteString("\n  page: " + data.URL)
	}
	b.WriteString("\nproxy: " + event.ProxyID + "\n")

	return b.String()
}

// firstAppFrame extracts the first application code frame from a JS stack trace,
// skipping node_modules, runtime, and webpack internals.
func firstAppFrame(stack string) string {
	lines := strings.Split(stack, "\n")
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		// Skip runtime/node_modules/webpack frames
		if strings.Contains(trimmed, "node_modules") ||
			strings.Contains(trimmed, "webpack-internal") ||
			strings.Contains(trimmed, "chrome-extension") ||
			strings.HasPrefix(trimmed, "Error") {
			continue
		}
		// Return first meaningful frame
		return trimmed
	}
	return ""
}

// formatHTTPErrorText formats an http_error event into compact text for PTY injection.
func formatHTTPErrorText(event ProxyEvent) string {
	var data struct {
		Method       string `json:"method"`
		URL          string `json:"url"`
		StatusCode   int    `json:"status_code"`
		ResponseBody string `json:"response_body"`
	}
	if err := json.Unmarshal(event.Data, &data); err != nil {
		debug.Warn("overlay", "failed to parse http_error: %v", err)
		return ""
	}

	// Skip common noise: static asset 404s, HMR/WebSocket 404s, redirects,
	// and client-initiated cancellations (browser navigated away mid-request).
	if isHTTPNoise(data.StatusCode, data.Method, data.URL, data.ResponseBody) {
		return ""
	}

	var b strings.Builder
	severity := "warning"
	if data.StatusCode >= 500 {
		severity = "error"
	}

	b.WriteString(fmt.Sprintf("[http %s] %d %s %s", severity, data.StatusCode, data.Method, data.URL))

	// Extract error message from response body
	if data.ResponseBody != "" {
		msg := extractHTTPErrorMessage(data.ResponseBody)
		if msg != "" {
			b.WriteString("\n  " + msg)
		}
	}

	b.WriteString("\nproxy: " + event.ProxyID + "\n")

	return b.String()
}

// isHTTPNoise filters out common non-actionable HTTP errors.
func isHTTPNoise(statusCode int, method, url, responseBody string) bool {
	// Redirects are not errors
	if statusCode == 301 || statusCode == 302 || statusCode == 304 {
		return true
	}
	// Client canceled: browser navigated away before response completed.
	// Go's ReverseProxy emits 502 with this message on context.Canceled.
	if statusCode == 502 && strings.Contains(responseBody, "canceled") {
		return true
	}
	// Static asset 404s are noise
	if statusCode == 404 {
		ext := strings.ToLower(url)
		for _, s := range []string{".css", ".js", ".map", ".ico", ".png", ".jpg", ".svg", ".woff", ".ttf"} {
			if strings.HasSuffix(ext, s) {
				return true
			}
		}
		// HMR/WebSocket 404s
		if strings.Contains(url, "/@") || strings.Contains(url, "/__webpack") ||
			strings.Contains(url, "/socket") || strings.Contains(url, "/hot") {
			return true
		}
	}
	return false
}

// extractHTTPErrorMessage tries to extract a meaningful error message from an HTTP response body.
func extractHTTPErrorMessage(body string) string {
	// Try JSON error body
	var jsonErr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
		Detail  string `json:"detail"`
	}
	if json.Unmarshal([]byte(body), &jsonErr) == nil {
		if jsonErr.Message != "" {
			return jsonErr.Message
		}
		if jsonErr.Error != "" {
			return jsonErr.Error
		}
		if jsonErr.Detail != "" {
			return jsonErr.Detail
		}
	}

	// For HTML responses, try to extract <title>
	if strings.Contains(body, "<html") || strings.Contains(body, "<!DOCTYPE") {
		if idx := strings.Index(body, "<title>"); idx != -1 {
			end := strings.Index(body[idx:], "</title>")
			if end != -1 {
				title := body[idx+7 : idx+end]
				if title != "" {
					return title
				}
			}
		}
	}

	// Return first line of body, truncated on a rune boundary — a byte cut
	// can split a multibyte UTF-8 sequence and emit invalid UTF-8 into the
	// agent's PTY.
	lines := strings.Split(strings.TrimSpace(body), "\n")
	if len(lines) > 0 {
		return overlay.TruncateRunes(strings.TrimSpace(lines[0]), 200, "...")
	}

	return ""
}
