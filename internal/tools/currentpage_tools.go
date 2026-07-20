package tools

import "time"

// CurrentPageInput defines input for the currentpage tool.
type CurrentPageInput struct {
	ProxyID   string   `json:"proxy_id,omitempty" jsonschema:"Proxy ID to query pages from (preferred)"`
	ID        string   `json:"id,omitempty" jsonschema:"Alias for proxy_id"`
	Action    string   `json:"action,omitempty" jsonschema:"Action: triage, list, get, summary, clear, layout (default: triage — top signals for the active page; auto-selects the open page when session_id omitted). layout runs a live CSS/layout diagnosis (containing-block traps, ineffective z-index, click interception, clipped content) on the open page"`
	SessionID string   `json:"session_id,omitempty" jsonschema:"Specific session ID (required for get/summary action)"`
	Detail    []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (interactions, mutations, errors, resources)"`
	Limit     int      `json:"limit,omitempty" jsonschema:"For summary: max items per detailed section (default: 5, max: 100)"`
	Raw       bool     `json:"raw,omitempty" jsonschema:"For get: return full arrays with all details instead of compact format (default: false)"`
}

// CurrentPageOutput defines output for currentpage tool.
type CurrentPageOutput struct {
	// For list
	Sessions []PageSessionOutput `json:"sessions,omitempty"`
	// Count is present for list, including an explicit zero, and absent for the
	// other actions in this union response. A zero beside a populated triage was
	// being misread by agents as "no active page".
	Count *int   `json:"count,omitempty"`
	Hint  string `json:"hint,omitempty"`

	// For get
	Session *PageSessionOutput `json:"session,omitempty"`

	// For summary
	Summary *PageSummaryOutput `json:"summary,omitempty"`

	// For triage
	Triage *PageTriageOutput `json:"triage,omitempty"`

	// For layout
	Layout *PageLayoutOutput `json:"layout,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// PageSummaryOutput provides a compact summary of a large page without blowing context.
type PageSummaryOutput struct {
	ID               string    `json:"id"`
	URL              string    `json:"url"`
	FrameID          string    `json:"frame_id,omitempty"`
	ExecutionContext string    `json:"execution_context"`
	PageTitle        string    `json:"page_title,omitempty"`
	StartTime        time.Time `json:"start_time"`
	LastActivity     time.Time `json:"last_activity"`
	Active           bool      `json:"active"`

	// Resource summary
	ResourceCount    int            `json:"resource_count"`
	ResourcesByType  map[string]int `json:"resources_by_type,omitempty"` // e.g., {"js": 5, "css": 3, "img": 10}
	TotalPayloadSize int64          `json:"total_payload_size,omitempty"`
	Resources        []string       `json:"resources,omitempty"` // Full list when detail=["resources"]

	// Error summary
	ErrorCount   int            `json:"error_count"`
	UniqueErrors []ErrorSummary `json:"unique_errors,omitempty"`  // Deduplicated errors with counts
	ErrorsByType map[string]int `json:"errors_by_type,omitempty"` // e.g., {"ReferenceError": 3}
	Errors       []CompactError `json:"errors,omitempty"`         // Compact error list when detail=["errors"]

	// Performance
	LoadTimeMs       int64 `json:"load_time_ms,omitempty"`
	FirstPaintMs     int64 `json:"first_paint_ms,omitempty"`
	DOMContentLoaded int64 `json:"dom_content_loaded_ms,omitempty"`

	// Interaction summary
	InteractionCount   int                      `json:"interaction_count"`
	InteractionsByType map[string]int           `json:"interactions_by_type,omitempty"` // e.g., {"click": 5, "scroll": 10}
	RecentInteractions []map[string]interface{} `json:"recent_interactions,omitempty"`  // Last N (default 5)
	Interactions       []map[string]interface{} `json:"interactions,omitempty"`         // Full list when detail=["interactions"]

	// Mutation summary
	MutationCount   int                      `json:"mutation_count"`
	MutationsByType map[string]int           `json:"mutations_by_type,omitempty"` // e.g., {"added": 10, "modified": 5}
	RecentMutations []map[string]interface{} `json:"recent_mutations,omitempty"`  // Last N (default 5)
	Mutations       []map[string]interface{} `json:"mutations,omitempty"`         // Full list when detail=["mutations"]

	// Page dimensions (if available from client)
	PageHeight     int `json:"page_height,omitempty"`
	PageWidth      int `json:"page_width,omitempty"`
	ViewportHeight int `json:"viewport_height,omitempty"`
	ViewportWidth  int `json:"viewport_width,omitempty"`

	// Detail info
	DetailSections []string `json:"detail_sections,omitempty"` // Which sections have full detail
	DetailLimit    int      `json:"detail_limit,omitempty"`    // Limit applied to detailed sections
}

// PageSessionOutput represents a page session in the output.
type PageSessionOutput struct {
	ID               string                   `json:"id"`
	URL              string                   `json:"url"`
	FrameID          string                   `json:"frame_id,omitempty"`
	ExecutionContext string                   `json:"execution_context"`
	PageTitle        string                   `json:"page_title,omitempty"`
	StartTime        time.Time                `json:"start_time"`
	LastActivity     time.Time                `json:"last_activity"`
	Active           bool                     `json:"active"`
	ResourceCount    int                      `json:"resource_count"`
	ErrorCount       int                      `json:"error_count"`
	HasPerformance   bool                     `json:"has_performance"`
	LoadTime         int64                    `json:"load_time_ms,omitempty"`
	Resources        []string                 `json:"resources,omitempty"` // URLs of resources
	Errors           []map[string]interface{} `json:"errors,omitempty"`

	// Interaction tracking
	InteractionCount int                      `json:"interaction_count"`
	Interactions     []map[string]interface{} `json:"interactions,omitempty"` // Detailed view only

	// Mutation tracking
	MutationCount int                      `json:"mutation_count"`
	Mutations     []map[string]interface{} `json:"mutations,omitempty"` // Detailed view only
}
