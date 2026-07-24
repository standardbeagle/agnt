package proxy

import "time"

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
	DiagnosticLevels []string       `json:"diagnostic_levels,omitempty"` // info, warning, error
	ErrorsOnly       bool           `json:"errors_only,omitempty"`       // Filter to errors from all sources
	Frames           []string       `json:"frames,omitempty"`            // Filter to entries from these content frame ids
	// MessagePattern restricts to message-bearing entries (error / custom /
	// diagnostic) whose message contains this substring. Non-message entry
	// types never match when it is set.
	MessagePattern string `json:"message_pattern,omitempty"`
	// MinDurationMs restricts to HTTP entries whose round-trip duration is at
	// least this many milliseconds (surfacing slow requests). Non-HTTP entries
	// never match when it is set (>0).
	MinDurationMs int64 `json:"min_duration_ms,omitempty"`
	// OmitBodies is a serialization hint (not a match criterion): when set, the
	// hub strips HTTP request/response bodies and headers from the result before
	// marshaling. The summary path enables it because it aggregates counts and
	// never reads bodies, so shipping the full 10KB-per-request payloads over IPC
	// only to discard them is pure waste.
	OmitBodies bool `json:"omit_bodies,omitempty"`
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

	// Frame filter — restrict to entries emitted by the named content frames.
	if len(f.Frames) > 0 {
		match := false
		for _, fid := range f.Frames {
			if entry.FrameID == fid {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Time range filter
	timestamp := entry.Timestamp()

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

	// Diagnostic level filter
	if entry.Type == LogTypeDiagnostic && entry.Diagnostic != nil && len(f.DiagnosticLevels) > 0 {
		match := false
		for _, level := range f.DiagnosticLevels {
			if string(entry.Diagnostic.Level) == level {
				match = true
				break
			}
		}
		if !match {
			return false
		}
	}

	// Message-pattern filter — substring match on the message of the
	// message-bearing entry types. When set, entries without such a message
	// (or whose message does not contain the pattern) are excluded.
	if f.MessagePattern != "" {
		msg := entry.Message()
		if msg == "" || !contains(msg, f.MessagePattern) {
			return false
		}
	}

	// Minimum-duration filter — HTTP round-trip at or above the threshold.
	// Only HTTP entries carry a duration, so any non-HTTP entry is excluded.
	if f.MinDurationMs > 0 {
		if entry.Type != LogTypeHTTP || entry.HTTP == nil ||
			entry.HTTP.Duration.Milliseconds() < f.MinDurationMs {
			return false
		}
	}

	// ErrorsOnly filter - matches errors from any source
	if f.ErrorsOnly {
		if !entry.IsError() {
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
