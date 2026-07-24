package proxy

import (
	"encoding/json"
	"time"
)

type LogEntryType string

const (
	// LogTypeHTTP represents an HTTP request/response.
	LogTypeHTTP LogEntryType = "http"
	// LogTypeError represents a frontend JavaScript error.
	LogTypeError LogEntryType = "error"
	// LogTypePerformance represents frontend performance metrics.
	LogTypePerformance LogEntryType = "performance"
	// LogTypeCustom represents a custom log message from frontend.
	LogTypeCustom LogEntryType = "custom"
	// LogTypeScreenshot represents a screenshot capture.
	LogTypeScreenshot LogEntryType = "screenshot"
	// LogTypeExecution represents JavaScript execution result.
	LogTypeExecution LogEntryType = "execution"
	// LogTypeResponse represents JavaScript execution responses returned to MCP client.
	LogTypeResponse LogEntryType = "response"
	// LogTypeInteraction represents a user interaction event (click, keyboard, etc.).
	LogTypeInteraction LogEntryType = "interaction"
	// LogTypeMutation represents a DOM mutation event.
	LogTypeMutation LogEntryType = "mutation"
	// LogTypePanelMessage represents a message from the floating indicator panel.
	LogTypePanelMessage LogEntryType = "panel_message"
	// LogTypeSketch represents a sketch/wireframe from the sketch mode.
	LogTypeSketch LogEntryType = "sketch"
	// LogTypeScreenshotCapture represents an area capture with a reference ID.
	LogTypeScreenshotCapture LogEntryType = "screenshot_capture"
	// LogTypeElementCapture represents an element capture with a reference ID.
	LogTypeElementCapture LogEntryType = "element_capture"
	// LogTypeSketchCapture represents a sketch capture with a reference ID.
	LogTypeSketchCapture LogEntryType = "sketch_capture"
	// LogTypeDesignState represents the initial state when an element is selected for design iteration.
	LogTypeDesignState LogEntryType = "design_state"
	// LogTypeDesignRequest represents a request for new design alternatives.
	LogTypeDesignRequest LogEntryType = "design_request"
	// LogTypeDesignChat represents a chat message about the selected element.
	LogTypeDesignChat LogEntryType = "design_chat"
	// LogTypeDesignEdit represents a direct-manipulation geometry edit committed
	// on an existing element via the design-mode transform handles. Carries the
	// CSS selector plus the computed before→after delta for the agent to write
	// as a real source diff.
	LogTypeDesignEdit LogEntryType = "design_edit"
	// LogTypeWalkthrough represents a live-demo walkthrough lifecycle event
	// (start, step advance, manual nav, finish, stop) emitted by the chrome-frame
	// walkthrough panel. Lets the agent narrate live and react to where the user
	// is in the demo.
	LogTypeWalkthrough LogEntryType = "walkthrough"
	// LogTypeResponsiveRequest represents a handoff from responsive mode requesting agent fixes for layout shifts at a width.
	LogTypeResponsiveRequest LogEntryType = "responsive_request"
	// LogTypeResponsiveState represents the responsive mode panel state (current width + shift count).
	LogTypeResponsiveState LogEntryType = "responsive_state"
	// LogTypeDiagnostic represents a server-side diagnostic event (proxy errors, connection issues, etc.).
	LogTypeDiagnostic LogEntryType = "diagnostic"
	// LogTypeProcessOutput represents a line of process stdout/stderr output.
	LogTypeProcessOutput LogEntryType = "process"
	// LogTypeHook represents a Claude Code (or other agent) hook event
	// drained from the daemon's hook ring buffer. The payload carries the
	// raw hook JSON plus daemon-side provenance so StreamSink subscribers
	// can filter and display it via `agnt monitor --types hook`.
	LogTypeHook LogEntryType = "hook"
	// LogTypeIncidentDigest carries a periodic incident-inbox digest as a
	// synthetic stream entry. It is the cross-process transport for the
	// unified agent-inbound queue: the daemon broadcasts it over STREAM-EVENTS
	// and consumer processes (agnt mcp, agnt run) render it to the agent. The
	// compact digest text rides in Custom.Message; Custom.Level is the severity.
	LogTypeIncidentDigest LogEntryType = "incident_digest"
)

// AllLogTypes lists every LogEntryType the traffic logger can emit. It is the
// single source of truth for the proxylog `types` filter: the tool enum and the
// "unknown type" rejection both derive from it, so an unrecognised type filter
// fails loud instead of silently matching nothing.
func AllLogTypes() []LogEntryType {
	return []LogEntryType{
		LogTypeHTTP, LogTypeError, LogTypePerformance, LogTypeCustom,
		LogTypeScreenshot, LogTypeExecution, LogTypeResponse, LogTypeInteraction,
		LogTypeMutation, LogTypePanelMessage, LogTypeSketch, LogTypeScreenshotCapture,
		LogTypeElementCapture, LogTypeSketchCapture, LogTypeDesignState,
		LogTypeDesignRequest, LogTypeDesignChat, LogTypeDesignEdit, LogTypeWalkthrough,
		LogTypeResponsiveRequest, LogTypeResponsiveState, LogTypeDiagnostic,
		LogTypeProcessOutput, LogTypeHook, LogTypeIncidentDigest,
	}
}

// LogTypeNames returns AllLogTypes as strings (for enums and error messages).
func LogTypeNames() []string {
	all := AllLogTypes()
	names := make([]string, len(all))
	for i, t := range all {
		names[i] = string(t)
	}
	return names
}

// IsValidLogType reports whether s names a known LogEntryType.
func IsValidLogType(s string) bool {
	for _, t := range AllLogTypes() {
		if string(t) == s {
			return true
		}
	}
	return false
}

// HTTPLogEntry represents a logged HTTP request/response pair.
type HTTPLogEntry struct {
	ID              string            `json:"id"`
	Timestamp       time.Time         `json:"timestamp"`
	Method          string            `json:"method"`
	URL             string            `json:"url"`
	RequestHeaders  map[string]string `json:"request_headers"`
	RequestBody     string            `json:"request_body,omitempty"`
	StatusCode      int               `json:"status_code"`
	ResponseHeaders map[string]string `json:"response_headers"`
	ResponseBody    string            `json:"response_body,omitempty"`
	Duration        time.Duration     `json:"duration"`
	Error           string            `json:"error,omitempty"`
	// Chaos marks a response the chaos engine synthesised (injected error
	// status / corruption) rather than one the backend actually produced.
	// Synthetic faults stay in the traffic log (proxylog query still surfaces
	// them) but are suppressed from the agent incident inbox so an injected
	// 500 is not mistaken for a real backend error. The swallow detector also
	// keys off this flag.
	Chaos bool `json:"chaos,omitempty"`
	// ChaosSwallowWatch is set when this injected fault should be watched by
	// the swallowed-error heuristic — i.e. chaos injected it AND the proxy's
	// runtime swallow-detect toggle (browser panel) was on. The daemon records
	// it as a pending fault; if no app-side error follows in the window it is
	// raised as a swallowed-error incident.
	ChaosSwallowWatch bool `json:"chaos_swallow_watch,omitempty"`
}

// FrontendError represents a JavaScript error from the frontend.
type FrontendError struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Message   string    `json:"message"`
	Source    string    `json:"source,omitempty"`
	LineNo    int       `json:"lineno,omitempty"`
	ColNo     int       `json:"colno,omitempty"`
	Error     string    `json:"error,omitempty"`
	Stack     string    `json:"stack,omitempty"`
	URL       string    `json:"url"` // Page URL where error occurred
}

// PerformanceMetric represents frontend performance data.
type PerformanceMetric struct {
	ID                   string                 `json:"id"`
	Timestamp            time.Time              `json:"timestamp"`
	URL                  string                 `json:"url"` // Page URL
	NavigationStart      int64                  `json:"navigation_start,omitempty"`
	LoadEventEnd         int64                  `json:"load_event_end,omitempty"`
	DOMContentLoaded     int64                  `json:"dom_content_loaded,omitempty"`
	FirstPaint           int64                  `json:"first_paint,omitempty"`
	FirstContentfulPaint int64                  `json:"first_contentful_paint,omitempty"`
	Resources            []ResourceTiming       `json:"resources,omitempty"`
	Custom               map[string]interface{} `json:"custom,omitempty"`

	// Page context captured alongside the performance sample. PageTitle is the
	// document title (also copied onto the owning PageSession); the dimensions
	// are surfaced by the currentpage summary.
	PageTitle      string `json:"page_title,omitempty"`
	PageWidth      int    `json:"page_width,omitempty"`
	PageHeight     int    `json:"page_height,omitempty"`
	ViewportWidth  int    `json:"viewport_width,omitempty"`
	ViewportHeight int    `json:"viewport_height,omitempty"`
}

// ResourceTiming represents timing for a single resource.
type ResourceTiming struct {
	Name     string `json:"name"`
	Duration int64  `json:"duration"`
	Size     int64  `json:"size,omitempty"`
}

// CustomLog represents a custom log message from the frontend.
type CustomLog struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Level     string                 `json:"level"` // debug, info, warn, error
	Message   string                 `json:"message"`
	Data      map[string]interface{} `json:"data,omitempty"`
	URL       string                 `json:"url"`
}

// Screenshot represents a captured screenshot.
type Screenshot struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	Name      string    `json:"name"`
	FilePath  string    `json:"file_path"` // Path to saved screenshot file
	URL       string    `json:"url"`       // Page URL where screenshot was taken
	Width     int       `json:"width"`
	Height    int       `json:"height"`
	Format    string    `json:"format"`   // png, jpeg
	Selector  string    `json:"selector"` // CSS selector for element (or "body" for full page)
	Error     string    `json:"error,omitempty"`
}

// ExecutionResult represents the result of executing JavaScript.
type ExecutionResult struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	Code      string                 `json:"code"`   // The code that was executed
	Result    string                 `json:"result"` // String representation of result
	Error     string                 `json:"error,omitempty"`
	Duration  time.Duration          `json:"duration"`
	URL       string                 `json:"url"`
	Data      map[string]interface{} `json:"data,omitempty"`
	FilePath  string                 `json:"file_path,omitempty"` // Path to file if result was too large
}

// ExecutionResponse represents a response sent back to MCP client.
type ExecutionResponse struct {
	ID        string        `json:"id"`
	Timestamp time.Time     `json:"timestamp"`
	ExecID    string        `json:"exec_id"` // Original execution ID
	Success   bool          `json:"success"`
	Result    string        `json:"result,omitempty"`
	Error     string        `json:"error,omitempty"`
	Duration  time.Duration `json:"duration"`
}

// InteractionEvent represents a user interaction (click, keyboard, etc.).
type InteractionEvent struct {
	ID        string                 `json:"id"`
	Timestamp time.Time              `json:"timestamp"`
	EventType string                 `json:"event_type"` // click, dblclick, keydown, input, scroll, focus, blur, submit, contextmenu, mousemove
	Target    InteractionTarget      `json:"target"`
	Position  *InteractionPosition   `json:"position,omitempty"` // For mouse events
	Key       *KeyboardInfo          `json:"key,omitempty"`      // For keyboard events
	Value     string                 `json:"value,omitempty"`    // For input events (sanitized, no passwords)
	URL       string                 `json:"url"`
	Data      map[string]interface{} `json:"data,omitempty"` // Extra data (scroll_position, etc.)
}

// InteractionTarget describes the DOM element that was interacted with.
type InteractionTarget struct {
	Selector   string            `json:"selector"`
	Tag        string            `json:"tag"`
	ID         string            `json:"id,omitempty"`
	Classes    []string          `json:"classes,omitempty"`
	Text       string            `json:"text,omitempty"`       // Truncated innerText
	Attributes map[string]string `json:"attributes,omitempty"` // Relevant attrs only (href, src, type, etc.)
}

// InteractionPosition describes the mouse position during an interaction.
type InteractionPosition struct {
	ClientX int `json:"client_x"`
	ClientY int `json:"client_y"`
	PageX   int `json:"page_x"`
	PageY   int `json:"page_y"`
}

// KeyboardInfo describes keyboard event details.
type KeyboardInfo struct {
	Key   string `json:"key"`
	Code  string `json:"code"`
	Ctrl  bool   `json:"ctrl,omitempty"`
	Alt   bool   `json:"alt,omitempty"`
	Shift bool   `json:"shift,omitempty"`
	Meta  bool   `json:"meta,omitempty"`
}

// MutationEvent represents a DOM mutation.
type MutationEvent struct {
	ID           string           `json:"id"`
	Timestamp    time.Time        `json:"timestamp"`
	MutationType string           `json:"mutation_type"` // added, removed, attributes, characterData
	Target       MutationTarget   `json:"target"`
	Added        []MutationNode   `json:"added,omitempty"`
	Removed      []MutationNode   `json:"removed,omitempty"`
	Attribute    *AttributeChange `json:"attribute,omitempty"`
	URL          string           `json:"url"`
	// TriggeredBy correlates this mutation to the user interaction that likely
	// caused it (the browser walks recent interaction history within a 500ms
	// window). Nil when no interaction preceded the mutation.
	TriggeredBy *MutationTrigger `json:"triggered_by,omitempty"`
}

// MutationTrigger describes the user interaction the browser correlated to a
// DOM mutation (cause → effect), captured from the interaction history.
type MutationTrigger struct {
	Type      string `json:"type,omitempty"`      // interaction event type (click, keydown, ...)
	Timestamp int64  `json:"timestamp,omitempty"` // interaction time (ms epoch, browser clock)
	Latency   int64  `json:"latency,omitempty"`   // ms between interaction and mutation
	Target    string `json:"target,omitempty"`    // selector of the interaction target
}

// MutationTarget describes the parent element where a mutation occurred.
type MutationTarget struct {
	Selector string `json:"selector"`
	Tag      string `json:"tag"`
	ID       string `json:"id,omitempty"`
}

// MutationNode describes a node that was added or removed.
type MutationNode struct {
	Selector string `json:"selector,omitempty"`
	Tag      string `json:"tag"`
	ID       string `json:"id,omitempty"`
	HTML     string `json:"html,omitempty"` // Truncated outerHTML
}

// AttributeChange describes a changed attribute.
type AttributeChange struct {
	Name     string `json:"name"`
	OldValue string `json:"old_value,omitempty"`
	NewValue string `json:"new_value,omitempty"`
}

// PanelMessage represents a message from the floating indicator panel.
type PanelMessage struct {
	ID                  string            `json:"id"`
	Timestamp           time.Time         `json:"timestamp"`
	Message             string            `json:"message"`
	Attachments         []PanelAttachment `json:"attachments,omitempty"`
	URL                 string            `json:"url"`
	RequestNotification bool              `json:"request_notification,omitempty"`
}

// PanelAttachment represents an attachment to a panel message.
type PanelAttachment struct {
	Type     string                 `json:"type"` // element, screenshot_area
	Selector string                 `json:"selector,omitempty"`
	Tag      string                 `json:"tag,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Classes  []string               `json:"classes,omitempty"`
	Text     string                 `json:"text,omitempty"`
	Summary  string                 `json:"summary,omitempty"`
	FilePath string                 `json:"filePath,omitempty"` // Browser-side file path from capture_ack
	Area     *ScreenshotArea        `json:"area,omitempty"`
	Data     map[string]interface{} `json:"data,omitempty"`
}

// ScreenshotArea represents a selected area for screenshot.
type ScreenshotArea struct {
	X      int    `json:"x"`
	Y      int    `json:"y"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
	Data   string `json:"data,omitempty"` // Base64 image data
}

// SketchEntry represents a sketch/wireframe from sketch mode.
type SketchEntry struct {
	ID           string                 `json:"id"`
	Timestamp    time.Time              `json:"timestamp"`
	URL          string                 `json:"url"`
	Description  string                 `json:"description"`   // User description of the wireframe
	Sketch       map[string]interface{} `json:"sketch"`        // JSON-serialized sketch data
	ImageData    string                 `json:"image_data"`    // Base64 PNG of the sketch
	FilePath     string                 `json:"file_path"`     // Path to saved image file
	ElementCount int                    `json:"element_count"` // Number of elements in sketch
}

// ScreenshotCapture represents an area capture from the panel with a reference ID.
type ScreenshotCapture struct {
	ID        string    `json:"id"` // Reference ID for use in messages
	Timestamp time.Time `json:"timestamp"`
	URL       string    `json:"url"`
	Summary   string    `json:"summary"`    // Human-readable summary
	ImageData string    `json:"image_data"` // Base64 PNG data (cleared after save)
	FilePath  string    `json:"file_path"`  // Path to saved image file
	Area      struct {
		X      int `json:"x"`
		Y      int `json:"y"`
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"area"`
}

// ElementCapture represents an element capture from the panel with a reference ID.
type ElementCapture struct {
	ID        string    `json:"id"` // Reference ID for use in messages
	Timestamp time.Time `json:"timestamp"`
	URL       string    `json:"url"`
	Summary   string    `json:"summary"`  // Human-readable summary
	Selector  string    `json:"selector"` // CSS selector
	Tag       string    `json:"tag"`
	ElementID string    `json:"element_id,omitempty"` // DOM element id attribute
	Classes   []string  `json:"classes,omitempty"`
	Text      string    `json:"text,omitempty"` // Truncated text content
	Rect      struct {
		X      float64 `json:"x"`
		Y      float64 `json:"y"`
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"rect,omitempty"`
}

// SketchCapture represents a sketch capture from the panel with a reference ID.
type SketchCapture struct {
	ID           string                 `json:"id"` // Reference ID for use in messages
	Timestamp    time.Time              `json:"timestamp"`
	URL          string                 `json:"url"`
	Summary      string                 `json:"summary"` // Human-readable summary
	ElementCount int                    `json:"element_count"`
	Sketch       map[string]interface{} `json:"sketch,omitempty"` // Sketch data
	ImageData    string                 `json:"image_data,omitempty"`
	FilePath     string                 `json:"file_path,omitempty"`
}

// DesignElementMetadata describes metadata about a selected element for design iteration.
type DesignElementMetadata struct {
	Tag        string            `json:"tag"`
	ID         string            `json:"id,omitempty"`
	Classes    []string          `json:"classes,omitempty"`
	Attributes map[string]string `json:"attributes,omitempty"`
	Text       string            `json:"text,omitempty"` // Truncated text content
	Rect       struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	} `json:"rect,omitempty"`
}

// DesignScheme captures design tokens extracted live from the proxied target
// app so generated alternatives stay on-scheme (matching the app's palette,
// typography, spacing, and declared CSS variables). All fields are best-effort
// and omitted when extraction yields nothing.
type DesignScheme struct {
	Palette      []string          `json:"palette,omitempty"`       // frequency-ranked colors (hex/rgb)
	FontFamilies []string          `json:"font_families,omitempty"` // distinct font-family stacks
	FontSizes    []string          `json:"font_sizes,omitempty"`    // observed font-size ladder
	FontWeights  []string          `json:"font_weights,omitempty"`  // observed font weights
	Spacing      []string          `json:"spacing,omitempty"`       // observed margin/padding/gap steps
	Radius       []string          `json:"radius,omitempty"`        // observed border-radius values
	Shadows      []string          `json:"shadows,omitempty"`       // observed box-shadow samples
	CSSVars      map[string]string `json:"css_vars,omitempty"`      // declared --* custom properties
}

// DesignChatMessage represents a chat message in the design iteration history.
type DesignChatMessage struct {
	Timestamp int64  `json:"timestamp"`
	Message   string `json:"message"`
	Role      string `json:"role"` // user or assistant
}

// DesignState represents the initial state when an element is selected for design iteration.
type DesignState struct {
	ID           string                `json:"id"`
	Timestamp    time.Time             `json:"timestamp"`
	Selector     string                `json:"selector"` // CSS selector
	XPath        string                `json:"xpath"`    // XPath for robustness
	OriginalHTML string                `json:"original_html"`
	ContextHTML  string                `json:"context_html"` // Parent element with siblings for context
	Metadata     DesignElementMetadata `json:"metadata"`
	Scheme       *DesignScheme         `json:"scheme,omitempty"` // live-extracted design tokens of the proxied app
	URL          string                `json:"url"`
}

// DesignRequest represents a request for new design alternatives.
type DesignRequest struct {
	ID                string                `json:"id"`
	Timestamp         time.Time             `json:"timestamp"`
	Selector          string                `json:"selector"`
	XPath             string                `json:"xpath"`
	CurrentHTML       string                `json:"current_html"`  // Current HTML being displayed
	OriginalHTML      string                `json:"original_html"` // Original HTML before any changes
	ContextHTML       string                `json:"context_html"`  // Parent context
	Metadata          DesignElementMetadata `json:"metadata"`
	AlternativesCount int                   `json:"alternatives_count"` // How many alternatives already exist
	ChatHistory       []DesignChatMessage   `json:"chat_history,omitempty"`
	Scheme            *DesignScheme         `json:"scheme,omitempty"` // live-extracted design tokens of the proxied app
	URL               string                `json:"url"`
	ScreenshotPath    string                `json:"screenshot_path,omitempty"` // saved PNG of the live segment (dataURL stripped after save)
}

// DesignEdit represents a direct-manipulation geometry edit committed on an
// existing element through the design-mode transform handles. The payload
// contract is selector + computed delta (plus OID so the agent can locate an
// element whose selector is weak); the browser never resolves source files.
type DesignEdit struct {
	ID             string                `json:"id"`
	Timestamp      time.Time             `json:"timestamp"`
	Selector       string                `json:"selector"`
	XPath          string                `json:"xpath"`
	OID            string                `json:"oid"`             // data-devtool-oid value
	Deltas         map[string]string     `json:"deltas"`          // property → final value
	ComputedBefore map[string]string     `json:"computed_before"` // measured before the drag
	ComputedAfter  map[string]string     `json:"computed_after"`  // measured after the drag
	Metadata       DesignElementMetadata `json:"metadata"`
	URL            string                `json:"url"`
}

// DesignChat represents a chat message about the selected element.
type DesignChat struct {
	ID             string                `json:"id"`
	Timestamp      time.Time             `json:"timestamp"`
	Message        string                `json:"message"` // User's chat message
	Selector       string                `json:"selector"`
	XPath          string                `json:"xpath"`
	CurrentHTML    string                `json:"current_html"`
	OriginalHTML   string                `json:"original_html"`
	ContextHTML    string                `json:"context_html"`
	Metadata       DesignElementMetadata `json:"metadata"`
	ChatHistory    []DesignChatMessage   `json:"chat_history,omitempty"`
	URL            string                `json:"url"`
	ScreenshotPath string                `json:"screenshot_path,omitempty"` // saved PNG of the live segment
}

// WalkthroughEntry represents a walkthrough (live-demo) lifecycle event emitted
// from the chrome-frame panel. Event is one of: start, step, manual, warning,
// play, pause, finish, stop. Fields are populated as relevant to the event.
type WalkthroughEntry struct {
	ID        string    `json:"id"`
	Timestamp time.Time `json:"timestamp"`
	URL       string    `json:"url"`
	Event     string    `json:"event"`
	ScriptID  string    `json:"script_id,omitempty"`
	Title     string    `json:"title,omitempty"`
	StepIndex int       `json:"step_index"`
	Total     int       `json:"total,omitempty"`
	StepTitle string    `json:"step_title,omitempty"`
	Advance   string    `json:"advance,omitempty"` // auto | click-target | wait
	How       string    `json:"how,omitempty"`     // timer | click | wait | manual | start | goto
	Mode      string    `json:"mode,omitempty"`    // auto | manual
	Message   string    `json:"message,omitempty"`
}

// ResponsiveRequest represents a handoff from responsive mode asking the agent to
// fix the layout shifts detected at a particular viewport width.
type ResponsiveRequest struct {
	ID        string                   `json:"id"`
	Timestamp time.Time                `json:"timestamp"`
	URL       string                   `json:"url"`
	Width     int                      `json:"width"`
	Shifts    []map[string]interface{} `json:"shifts,omitempty"`    // layout-shift findings (id/type/severity/selector/message/width/isNew)
	Selectors []map[string]interface{} `json:"selectors,omitempty"` // flat {id, selector} list for quick targeting
}

// ResponsiveState represents the responsive mode panel state at a settled width.
type ResponsiveState struct {
	ID         string    `json:"id"`
	Timestamp  time.Time `json:"timestamp"`
	URL        string    `json:"url"`
	Width      int       `json:"width"`
	ShiftCount int       `json:"shift_count"`
}

// ProcessOutputEvent represents a line of output from a managed process.
type ProcessOutputEvent struct {
	ProcessID string    `json:"process_id"`
	Stream    string    `json:"stream"`    // "stdout" or "stderr"
	Line      string    `json:"line"`      // Output line content (without trailing newline)
	Timestamp time.Time `json:"timestamp"` // When the line was received
}

// HookLogEntry is the proxy-package representation of a daemon HookEvent
// after it has been fanned out through BroadcastLogEntry as a unified
// LogEntry. The proxy package cannot import daemon (the import graph is
// daemon → proxy), so this struct mirrors the wire shape of HookEvent
// without introducing a cycle. Fields are flat by design — StreamSink
// consumers (monitor, get_errors) need direct access to event/payload/
// session_id without unwrapping a nested map.
type HookLogEntry struct {
	Event       string          `json:"event"`
	Payload     json.RawMessage `json:"payload,omitempty"`
	SessionID   string          `json:"session_id,omitempty"`
	ProjectPath string          `json:"project_path,omitempty"`
	Agent       string          `json:"agent,omitempty"`
	ReceivedAt  time.Time       `json:"received_at"`
}

// LogEntry is a union type for all log entry types.
type LogEntry struct {
	Type LogEntryType `json:"type"`
	// FrameID attributes a browser-sourced entry to the content frame that
	// emitted it (the always-wrap model — docs/responsive-canonical-target.md
	// §5.2). Empty for server-sourced entries (HTTP) and for unframed/legacy
	// pages. Carried once on the envelope rather than on each typed payload.
	FrameID           string              `json:"frame_id,omitempty"`
	HTTP              *HTTPLogEntry       `json:"http,omitempty"`
	Error             *FrontendError      `json:"error,omitempty"`
	Performance       *PerformanceMetric  `json:"performance,omitempty"`
	Custom            *CustomLog          `json:"custom,omitempty"`
	Screenshot        *Screenshot         `json:"screenshot,omitempty"`
	Execution         *ExecutionResult    `json:"execution,omitempty"`
	Response          *ExecutionResponse  `json:"response,omitempty"`
	Interaction       *InteractionEvent   `json:"interaction,omitempty"`
	Mutation          *MutationEvent      `json:"mutation,omitempty"`
	PanelMessage      *PanelMessage       `json:"panel_message,omitempty"`
	Sketch            *SketchEntry        `json:"sketch,omitempty"`
	ScreenshotCapture *ScreenshotCapture  `json:"screenshot_capture,omitempty"`
	ElementCapture    *ElementCapture     `json:"element_capture,omitempty"`
	SketchCapture     *SketchCapture      `json:"sketch_capture,omitempty"`
	DesignState       *DesignState        `json:"design_state,omitempty"`
	DesignRequest     *DesignRequest      `json:"design_request,omitempty"`
	DesignChat        *DesignChat         `json:"design_chat,omitempty"`
	DesignEdit        *DesignEdit         `json:"design_edit,omitempty"`
	Walkthrough       *WalkthroughEntry   `json:"walkthrough,omitempty"`
	ResponsiveRequest *ResponsiveRequest  `json:"responsive_request,omitempty"`
	ResponsiveState   *ResponsiveState    `json:"responsive_state,omitempty"`
	Diagnostic        *ProxyDiagnostic    `json:"diagnostic,omitempty"`
	ProcessOutput     *ProcessOutputEvent `json:"process,omitempty"`
	Hook              *HookLogEntry       `json:"hook,omitempty"`
}

// Timestamp returns the entry's event time regardless of payload type —
// the single place the per-type extraction switch lives, so adding a log
// type means updating one method instead of every consumer's switch.
// Hook entries use ReceivedAt; incident digests ride the Custom payload.
func (e LogEntry) Timestamp() time.Time {
	switch e.Type {
	case LogTypeHTTP:
		if e.HTTP != nil {
			return e.HTTP.Timestamp
		}
	case LogTypeError:
		if e.Error != nil {
			return e.Error.Timestamp
		}
	case LogTypePerformance:
		if e.Performance != nil {
			return e.Performance.Timestamp
		}
	case LogTypeCustom:
		if e.Custom != nil {
			return e.Custom.Timestamp
		}
	case LogTypeScreenshot:
		if e.Screenshot != nil {
			return e.Screenshot.Timestamp
		}
	case LogTypeExecution:
		if e.Execution != nil {
			return e.Execution.Timestamp
		}
	case LogTypeResponse:
		if e.Response != nil {
			return e.Response.Timestamp
		}
	case LogTypeInteraction:
		if e.Interaction != nil {
			return e.Interaction.Timestamp
		}
	case LogTypeMutation:
		if e.Mutation != nil {
			return e.Mutation.Timestamp
		}
	case LogTypePanelMessage:
		if e.PanelMessage != nil {
			return e.PanelMessage.Timestamp
		}
	case LogTypeSketch:
		if e.Sketch != nil {
			return e.Sketch.Timestamp
		}
	case LogTypeScreenshotCapture:
		if e.ScreenshotCapture != nil {
			return e.ScreenshotCapture.Timestamp
		}
	case LogTypeElementCapture:
		if e.ElementCapture != nil {
			return e.ElementCapture.Timestamp
		}
	case LogTypeSketchCapture:
		if e.SketchCapture != nil {
			return e.SketchCapture.Timestamp
		}
	case LogTypeDesignState:
		if e.DesignState != nil {
			return e.DesignState.Timestamp
		}
	case LogTypeDesignRequest:
		if e.DesignRequest != nil {
			return e.DesignRequest.Timestamp
		}
	case LogTypeDesignChat:
		if e.DesignChat != nil {
			return e.DesignChat.Timestamp
		}
	case LogTypeDesignEdit:
		if e.DesignEdit != nil {
			return e.DesignEdit.Timestamp
		}
	case LogTypeWalkthrough:
		if e.Walkthrough != nil {
			return e.Walkthrough.Timestamp
		}
	case LogTypeResponsiveRequest:
		if e.ResponsiveRequest != nil {
			return e.ResponsiveRequest.Timestamp
		}
	case LogTypeResponsiveState:
		if e.ResponsiveState != nil {
			return e.ResponsiveState.Timestamp
		}
	case LogTypeDiagnostic:
		if e.Diagnostic != nil {
			return e.Diagnostic.Timestamp
		}
	case LogTypeProcessOutput:
		if e.ProcessOutput != nil {
			return e.ProcessOutput.Timestamp
		}
	case LogTypeHook:
		if e.Hook != nil {
			return e.Hook.ReceivedAt
		}
	case LogTypeIncidentDigest:
		if e.Custom != nil {
			return e.Custom.Timestamp
		}
	}
	return time.Time{}
}

// Message returns the human-readable message for the message-bearing
// entry types (error, custom, diagnostic); empty for all others.
func (e LogEntry) Message() string {
	switch e.Type {
	case LogTypeError:
		if e.Error != nil {
			return e.Error.Message
		}
	case LogTypeCustom:
		if e.Custom != nil {
			return e.Custom.Message
		}
	case LogTypeDiagnostic:
		if e.Diagnostic != nil {
			return e.Diagnostic.Message
		}
	}
	return ""
}

// IsError reports whether the entry represents an error from any source:
// frontend errors, error-level diagnostics, HTTP 4xx/5xx or transport
// errors, and error-level custom logs.
func (e LogEntry) IsError() bool {
	switch e.Type {
	case LogTypeError:
		return true
	case LogTypeDiagnostic:
		return e.Diagnostic != nil && e.Diagnostic.Level == DiagnosticError
	case LogTypeHTTP:
		return e.HTTP != nil && (e.HTTP.StatusCode >= 400 || e.HTTP.Error != "")
	case LogTypeCustom:
		return e.Custom != nil && e.Custom.Level == "error"
	}
	return false
}
