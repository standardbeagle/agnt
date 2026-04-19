package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/standardbeagle/agnt/internal/proxy"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// CurrentPageInput defines input for the currentpage tool.
type CurrentPageInput struct {
	ProxyID   string   `json:"proxy_id" jsonschema:"Proxy ID to query pages from"`
	Action    string   `json:"action,omitempty" jsonschema:"Action: list, get, summary, clear (default: list)"`
	SessionID string   `json:"session_id,omitempty" jsonschema:"Specific session ID (required for get/summary action)"`
	Detail    []string `json:"detail,omitempty" jsonschema:"For summary: sections to include full detail for (interactions, mutations, errors, resources)"`
	Limit     int      `json:"limit,omitempty" jsonschema:"For summary: max items per detailed section (default: 5, max: 100)"`
	Raw       bool     `json:"raw,omitempty" jsonschema:"For get: return full arrays with all details instead of compact format (default: false)"`
}

// CurrentPageOutput defines output for currentpage tool.
type CurrentPageOutput struct {
	// For list
	Sessions []PageSessionOutput `json:"sessions,omitempty"`
	Count    int                 `json:"count"`
	Hint     string              `json:"hint,omitempty"`

	// For get
	Session *PageSessionOutput `json:"session,omitempty"`

	// For summary
	Summary *PageSummaryOutput `json:"summary,omitempty"`

	// For clear
	Success bool   `json:"success,omitempty"`
	Message string `json:"message,omitempty"`
}

// PageSummaryOutput provides a compact summary of a large page without blowing context.
type PageSummaryOutput struct {
	ID           string    `json:"id"`
	URL          string    `json:"url"`
	PageTitle    string    `json:"page_title,omitempty"`
	StartTime    time.Time `json:"start_time"`
	LastActivity time.Time `json:"last_activity"`
	Active       bool      `json:"active"`

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
	ID             string                   `json:"id"`
	URL            string                   `json:"url"`
	PageTitle      string                   `json:"page_title,omitempty"`
	StartTime      time.Time                `json:"start_time"`
	LastActivity   time.Time                `json:"last_activity"`
	Active         bool                     `json:"active"`
	ResourceCount  int                      `json:"resource_count"`
	ErrorCount     int                      `json:"error_count"`
	HasPerformance bool                     `json:"has_performance"`
	LoadTime       int64                    `json:"load_time_ms,omitempty"`
	Resources      []string                 `json:"resources,omitempty"` // URLs of resources
	Errors         []map[string]interface{} `json:"errors,omitempty"`

	// Interaction tracking
	InteractionCount int                      `json:"interaction_count"`
	Interactions     []map[string]interface{} `json:"interactions,omitempty"` // Detailed view only

	// Mutation tracking
	MutationCount int                      `json:"mutation_count"`
	Mutations     []map[string]interface{} `json:"mutations,omitempty"` // Detailed view only
}

// makeCurrentPageHandler creates the handler for the currentpage tool.
func makeCurrentPageHandler(pm *proxy.ProxyManager) func(context.Context, *mcp.CallToolRequest, CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	return func(ctx context.Context, req *mcp.CallToolRequest, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
		if err := validateCurrentPageInput(input); err != nil {
			return errorResult(validationError("currentpage", err)), CurrentPageOutput{}, nil
		}

		if input.ProxyID == "" {
			return errorResult("proxy_id required"), CurrentPageOutput{}, nil
		}

		proxyServer, err := pm.Get(input.ProxyID)
		if err != nil {
			return errorResult(fmt.Sprintf("proxy not found: %s", input.ProxyID)), CurrentPageOutput{}, nil
		}

		action := input.Action
		if action == "" {
			action = "list"
		}

		switch action {
		case "list":
			return handleCurrentPageList(proxyServer)
		case "get":
			return handleCurrentPageGet(proxyServer, input)
		case "clear":
			return handleCurrentPageClear(proxyServer, input)
		default:
			return errorResult(fmt.Sprintf("unknown action %q. Use: list, get, clear", action)), CurrentPageOutput{}, nil
		}
	}
}

func handleCurrentPageList(proxyServer *proxy.ProxyServer) (*mcp.CallToolResult, CurrentPageOutput, error) {
	// Use lightweight summaries to avoid massive token usage
	summaries := proxyServer.PageTracker().GetActiveSessionSummaries()

	output := make([]PageSessionOutput, len(summaries))
	for i, summary := range summaries {
		output[i] = PageSessionOutput{
			ID:               summary.ID,
			URL:              summary.URL,
			PageTitle:        summary.PageTitle,
			StartTime:        summary.StartTime,
			LastActivity:     summary.LastActivity,
			Active:           summary.Active,
			ResourceCount:    summary.ResourceCount,
			ErrorCount:       summary.ErrorCount,
			HasPerformance:   summary.HasPerformance,
			LoadTime:         summary.LoadTimeMs,
			InteractionCount: summary.InteractionCount,
			MutationCount:    summary.MutationCount,
			// Note: No Resources, Errors, Interactions, or Mutations arrays
			// Use action="get" with specific session_id for full details
		}
	}

	return nil, CurrentPageOutput{
		Sessions: output,
		Count:    len(output),
	}, nil
}

func handleCurrentPageGet(proxyServer *proxy.ProxyServer, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	if input.SessionID == "" {
		return errorResult("session_id required for get action"), CurrentPageOutput{}, nil
	}

	session, ok := proxyServer.PageTracker().GetSession(input.SessionID)
	if !ok {
		return errorResult(fmt.Sprintf("session not found: %s", input.SessionID)), CurrentPageOutput{}, nil
	}

	// Use raw format (full arrays) if requested, otherwise compact
	sessionOutput := convertPageSession(session, input.Raw)

	return nil, CurrentPageOutput{
		Session: &sessionOutput,
	}, nil
}

func handleCurrentPageClear(proxyServer *proxy.ProxyServer, input CurrentPageInput) (*mcp.CallToolResult, CurrentPageOutput, error) {
	proxyServer.PageTracker().Clear()

	return nil, CurrentPageOutput{
		Success: true,
		Message: fmt.Sprintf("Page sessions cleared for proxy %s", input.ProxyID),
	}, nil
}

// convertPageSession converts a PageSession to output format.
func convertPageSession(session *proxy.PageSession, includeDetails bool) PageSessionOutput {
	output := PageSessionOutput{
		ID:               session.ID,
		URL:              session.URL,
		PageTitle:        session.PageTitle,
		StartTime:        session.StartTime,
		LastActivity:     session.LastActivity,
		Active:           session.Active,
		ResourceCount:    len(session.Resources),
		ErrorCount:       len(session.Errors),
		HasPerformance:   session.Performance != nil,
		InteractionCount: session.InteractionCount,
		MutationCount:    session.MutationCount,
	}

	if session.Performance != nil {
		output.LoadTime = session.Performance.LoadEventEnd
	}

	// Include detailed arrays only if requested (to avoid token bloat)
	if includeDetails {
		// Add resource URLs
		output.Resources = make([]string, len(session.Resources))
		for i, res := range session.Resources {
			output.Resources[i] = res.URL
		}

		// Add error details
		output.Errors = make([]map[string]interface{}, len(session.Errors))
		for i, err := range session.Errors {
			output.Errors[i] = map[string]interface{}{
				"message": err.Message,
				"source":  err.Source,
				"lineno":  err.LineNo,
				"colno":   err.ColNo,
				"stack":   err.Stack,
			}
		}

		// Add interaction details
		output.Interactions = make([]map[string]interface{}, len(session.Interactions))
		for i, interaction := range session.Interactions {
			intMap := map[string]interface{}{
				"id":         interaction.ID,
				"event_type": interaction.EventType,
				"timestamp":  interaction.Timestamp,
				"url":        interaction.URL,
			}
			if interaction.Target.Selector != "" {
				intMap["target"] = map[string]interface{}{
					"selector": interaction.Target.Selector,
					"tag":      interaction.Target.Tag,
					"id":       interaction.Target.ID,
					"text":     interaction.Target.Text,
				}
			}
			if interaction.Position != nil {
				intMap["position"] = map[string]interface{}{
					"client_x": interaction.Position.ClientX,
					"client_y": interaction.Position.ClientY,
				}
			}
			output.Interactions[i] = intMap
		}

		// Add mutation details
		output.Mutations = make([]map[string]interface{}, len(session.Mutations))
		for i, mutation := range session.Mutations {
			mutMap := map[string]interface{}{
				"id":            mutation.ID,
				"mutation_type": mutation.MutationType,
				"timestamp":     mutation.Timestamp,
				"url":           mutation.URL,
			}
			if mutation.Target.Selector != "" {
				mutMap["target"] = map[string]interface{}{
					"selector": mutation.Target.Selector,
					"tag":      mutation.Target.Tag,
					"id":       mutation.Target.ID,
				}
			}
			output.Mutations[i] = mutMap
		}
	}

	return output
}
