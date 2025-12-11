package proxy

import (
	"sync"
	"sync/atomic"
	"time"
)

// LogEntryType categorizes different types of log entries.
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
)

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
	ID          string            `json:"id"`
	Timestamp   time.Time         `json:"timestamp"`
	Message     string            `json:"message"`
	Attachments []PanelAttachment `json:"attachments,omitempty"`
	URL         string            `json:"url"`
}

// PanelAttachment represents an attachment to a panel message.
type PanelAttachment struct {
	Type     string                 `json:"type"` // element, screenshot_area
	Selector string                 `json:"selector,omitempty"`
	Tag      string                 `json:"tag,omitempty"`
	ID       string                 `json:"id,omitempty"`
	Classes  []string               `json:"classes,omitempty"`
	Text     string                 `json:"text,omitempty"`
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

// LogEntry is a union type for all log entry types.
type LogEntry struct {
	Type         LogEntryType       `json:"type"`
	HTTP         *HTTPLogEntry      `json:"http,omitempty"`
	Error        *FrontendError     `json:"error,omitempty"`
	Performance  *PerformanceMetric `json:"performance,omitempty"`
	Custom       *CustomLog         `json:"custom,omitempty"`
	Screenshot   *Screenshot        `json:"screenshot,omitempty"`
	Execution    *ExecutionResult   `json:"execution,omitempty"`
	Response     *ExecutionResponse `json:"response,omitempty"`
	Interaction  *InteractionEvent  `json:"interaction,omitempty"`
	Mutation     *MutationEvent     `json:"mutation,omitempty"`
	PanelMessage *PanelMessage      `json:"panel_message,omitempty"`
	Sketch       *SketchEntry       `json:"sketch,omitempty"`
}

// TrafficLogger stores proxy traffic logs with bounded memory.
type TrafficLogger struct {
	entries []LogEntry
	maxSize int
	head    atomic.Int64 // Next write position
	count   atomic.Int64 // Total entries written (for ID generation)
	mu      sync.RWMutex // Protects entries slice
}

// NewTrafficLogger creates a new logger with specified max entries.
func NewTrafficLogger(maxSize int) *TrafficLogger {
	if maxSize <= 0 {
		maxSize = 1000 // Default to 1000 entries
	}
	return &TrafficLogger{
		entries: make([]LogEntry, maxSize),
		maxSize: maxSize,
	}
}

// LogHTTP adds an HTTP request/response log entry.
func (tl *TrafficLogger) LogHTTP(entry HTTPLogEntry) {
	tl.log(LogEntry{
		Type: LogTypeHTTP,
		HTTP: &entry,
	})
}

// LogError adds a frontend error log entry.
func (tl *TrafficLogger) LogError(entry FrontendError) {
	tl.log(LogEntry{
		Type:  LogTypeError,
		Error: &entry,
	})
}

// LogPerformance adds a frontend performance log entry.
func (tl *TrafficLogger) LogPerformance(entry PerformanceMetric) {
	tl.log(LogEntry{
		Type:        LogTypePerformance,
		Performance: &entry,
	})
}

// LogCustom adds a custom log message.
func (tl *TrafficLogger) LogCustom(entry CustomLog) {
	tl.log(LogEntry{
		Type:   LogTypeCustom,
		Custom: &entry,
	})
}

// LogScreenshot adds a screenshot log entry.
func (tl *TrafficLogger) LogScreenshot(entry Screenshot) {
	tl.log(LogEntry{
		Type:       LogTypeScreenshot,
		Screenshot: &entry,
	})
}

// LogExecution adds a JavaScript execution result.
func (tl *TrafficLogger) LogExecution(entry ExecutionResult) {
	tl.log(LogEntry{
		Type:      LogTypeExecution,
		Execution: &entry,
	})
}

// LogResponse adds an execution response sent to MCP client.
func (tl *TrafficLogger) LogResponse(entry ExecutionResponse) {
	tl.log(LogEntry{
		Type:     LogTypeResponse,
		Response: &entry,
	})
}

// LogInteraction adds a user interaction event.
func (tl *TrafficLogger) LogInteraction(entry InteractionEvent) {
	tl.log(LogEntry{
		Type:        LogTypeInteraction,
		Interaction: &entry,
	})
}

// LogMutation adds a DOM mutation event.
func (tl *TrafficLogger) LogMutation(entry MutationEvent) {
	tl.log(LogEntry{
		Type:     LogTypeMutation,
		Mutation: &entry,
	})
}

// LogPanelMessage adds a panel message entry.
func (tl *TrafficLogger) LogPanelMessage(entry PanelMessage) {
	tl.log(LogEntry{
		Type:         LogTypePanelMessage,
		PanelMessage: &entry,
	})
}

// LogSketch adds a sketch entry.
func (tl *TrafficLogger) LogSketch(entry SketchEntry) {
	tl.log(LogEntry{
		Type:   LogTypeSketch,
		Sketch: &entry,
	})
}

// log adds an entry to the circular buffer.
func (tl *TrafficLogger) log(entry LogEntry) {
	pos := tl.head.Add(1) - 1
	idx := int(pos % int64(tl.maxSize))

	tl.mu.Lock()
	tl.entries[idx] = entry
	tl.mu.Unlock()

	tl.count.Add(1)
}

// Query retrieves log entries matching the filter.
func (tl *TrafficLogger) Query(filter LogFilter) []LogEntry {
	tl.mu.RLock()
	defer tl.mu.RUnlock()

	total := tl.count.Load()
	available := int(min(total, int64(tl.maxSize)))

	var results []LogEntry
	for i := 0; i < available; i++ {
		entry := tl.entries[i]
		if filter.Matches(entry) {
			results = append(results, entry)
		}
	}

	return results
}

// Clear removes all log entries.
func (tl *TrafficLogger) Clear() {
	tl.mu.Lock()
	defer tl.mu.Unlock()

	tl.head.Store(0)
	tl.count.Store(0)
	// Zero out entries
	for i := range tl.entries {
		tl.entries[i] = LogEntry{}
	}
}

// Stats returns logger statistics.
func (tl *TrafficLogger) Stats() LoggerStats {
	total := tl.count.Load()
	available := int(min(total, int64(tl.maxSize)))
	return LoggerStats{
		TotalEntries:     total,
		AvailableEntries: int64(available),
		MaxSize:          int64(tl.maxSize),
		Dropped:          max(0, total-int64(tl.maxSize)),
	}
}

// LoggerStats holds logger statistics.
type LoggerStats struct {
	TotalEntries     int64 `json:"total_entries"`
	AvailableEntries int64 `json:"available_entries"`
	MaxSize          int64 `json:"max_size"`
	Dropped          int64 `json:"dropped"`
}

// LogFilter specifies criteria for querying logs.
type LogFilter struct {
	Types            []LogEntryType `json:"types,omitempty"`       // Filter by entry type
	Methods          []string       `json:"methods,omitempty"`     // HTTP methods
	URLPattern       string         `json:"url_pattern,omitempty"` // URL substring match
	StatusCodes      []int          `json:"status_codes,omitempty"`
	Since            *time.Time     `json:"since,omitempty"`
	Until            *time.Time     `json:"until,omitempty"`
	Limit            int            `json:"limit,omitempty"`             // Max results (0 = all)
	InteractionTypes []string       `json:"interaction_types,omitempty"` // click, keydown, scroll, etc.
	MutationTypes    []string       `json:"mutation_types,omitempty"`    // added, removed, attributes
}

// Matches returns true if the entry matches the filter.
func (f LogFilter) Matches(entry LogEntry) bool {
	// Type filter
	if len(f.Types) > 0 {
		match := false
		for _, t := range f.Types {
			if entry.Type == t {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Time range filter
	var timestamp time.Time
	switch entry.Type {
	case LogTypeHTTP:
		if entry.HTTP != nil {
			timestamp = entry.HTTP.Timestamp
		}
	case LogTypeError:
		if entry.Error != nil {
			timestamp = entry.Error.Timestamp
		}
	case LogTypePerformance:
		if entry.Performance != nil {
			timestamp = entry.Performance.Timestamp
		}
	case LogTypeCustom:
		if entry.Custom != nil {
			timestamp = entry.Custom.Timestamp
		}
	case LogTypeScreenshot:
		if entry.Screenshot != nil {
			timestamp = entry.Screenshot.Timestamp
		}
	case LogTypeExecution:
		if entry.Execution != nil {
			timestamp = entry.Execution.Timestamp
		}
	case LogTypeResponse:
		if entry.Response != nil {
			timestamp = entry.Response.Timestamp
		}
	case LogTypeInteraction:
		if entry.Interaction != nil {
			timestamp = entry.Interaction.Timestamp
		}
	case LogTypeMutation:
		if entry.Mutation != nil {
			timestamp = entry.Mutation.Timestamp
		}
	case LogTypePanelMessage:
		if entry.PanelMessage != nil {
			timestamp = entry.PanelMessage.Timestamp
		}
	case LogTypeSketch:
		if entry.Sketch != nil {
			timestamp = entry.Sketch.Timestamp
		}
	}

	if f.Since != nil && timestamp.Before(*f.Since) {
		return false
	}
	if f.Until != nil && timestamp.After(*f.Until) {
		return false
	}

	// Type-specific filters
	if entry.Type == LogTypeHTTP && entry.HTTP != nil {
		// Method filter
		if len(f.Methods) > 0 {
			match := false
			for _, m := range f.Methods {
				if entry.HTTP.Method == m {
					match = true
					break
				}
			}
			if !match {
				return false
			}
		}

		// URL pattern filter
		if f.URLPattern != "" {
			if !contains(entry.HTTP.URL, f.URLPattern) {
				return false
			}
		}

		// Status code filter
		if len(f.StatusCodes) > 0 {
			match := false
			for _, code := range f.StatusCodes {
				if entry.HTTP.StatusCode == code {
					match = true
					break
				}
			}
			if !match {
				return false
			}
		}
	}

	// Interaction type filter
	if entry.Type == LogTypeInteraction && entry.Interaction != nil && len(f.InteractionTypes) > 0 {
		match := false
		for _, t := range f.InteractionTypes {
			if entry.Interaction.EventType == t {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Mutation type filter
	if entry.Type == LogTypeMutation && entry.Mutation != nil && len(f.MutationTypes) > 0 {
		match := false
		for _, t := range f.MutationTypes {
			if entry.Mutation.MutationType == t {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	return true
}

// Helper functions

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && containsSlowPath(s, substr))
}

func containsSlowPath(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
