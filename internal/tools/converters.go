package tools

import (
	"fmt"

	"sort"
	"strings"

	"github.com/standardbeagle/agnt/internal/protocol"

	"time"
)

// parseChaosStats extracts ChaosStatsOutput from a map result.
func parseChaosStats(stats map[string]interface{}) *ChaosStatsOutput {
	output := &ChaosStatsOutput{
		TotalRequests:   getInt64(stats, "total_requests"),
		AffectedCount:   getInt64(stats, "affected_count"),
		LatencyInjected: getInt64(stats, "latency_injected_ms"),
		ErrorsInjected:  getInt64(stats, "errors_injected"),
		DropsInjected:   getInt64(stats, "drops_injected"),
		TruncatedCount:  getInt64(stats, "truncated_count"),
		ReorderedCount:  getInt64(stats, "reordered_count"),
	}
	if ruleStats, ok := stats["rule_stats"].(map[string]interface{}); ok {
		output.RuleStats = make(map[string]int64)
		for k, v := range ruleStats {
			if n, ok := v.(float64); ok {
				output.RuleStats[k] = int64(n)
			}
		}
	}
	return output
}

// parseChaosRules extracts a slice of ChaosRuleOutput from a rules interface.
func parseChaosRules(rules []interface{}) []ChaosRuleOutput {
	var result []ChaosRuleOutput
	for _, r := range rules {
		if rm, ok := r.(map[string]interface{}); ok {
			result = append(result, ChaosRuleOutput{
				ID:           getString(rm, "id"),
				Name:         getString(rm, "name"),
				Type:         getString(rm, "type"),
				Enabled:      getBool(rm, "enabled"),
				URLPattern:   getString(rm, "url_pattern"),
				Probability:  getFloat64(rm, "probability"),
				TimesApplied: getInt64(rm, "times_applied"),
			})
		}
	}
	return result
}

// inputRuleToProtocol converts a ChaosRuleInput to protocol.ChaosRuleConfig.
func inputRuleToProtocol(r ChaosRuleInput) protocol.ChaosRuleConfig {
	return protocol.ChaosRuleConfig{
		ID:                 r.ID,
		Name:               r.Name,
		Type:               r.Type,
		Enabled:            r.Enabled,
		URLPattern:         r.URLPattern,
		Methods:            r.Methods,
		Probability:        r.Probability,
		MinLatencyMs:       r.MinLatencyMs,
		MaxLatencyMs:       r.MaxLatencyMs,
		JitterMs:           r.JitterMs,
		BytesPerMs:         r.BytesPerMs,
		ChunkSize:          r.ChunkSize,
		DropAfterPercent:   r.DropAfterPercent,
		DropAfterBytes:     r.DropAfterBytes,
		ErrorCodes:         r.ErrorCodes,
		ErrorMessage:       r.ErrorMessage,
		TruncatePercent:    r.TruncatePercent,
		ReorderMinRequests: r.ReorderMinRequests,
		ReorderMaxWaitMs:   r.ReorderMaxWaitMs,
		StaleDelayMs:       r.StaleDelayMs,
	}
}

// convertToPageSummary creates a compact summary from page session data.
// detailSet specifies which sections to include full details for (interactions, mutations, errors, resources).
// limit specifies the max items for detailed sections (default 5).
func convertToPageSummary(m map[string]interface{}, detailSet map[string]bool, limit int) PageSummaryOutput {
	summary := PageSummaryOutput{
		ID:               getString(m, "id"),
		URL:              getString(m, "url"),
		PageTitle:        getString(m, "page_title"),
		StartTime:        getTime(m, "start_time"),
		LastActivity:     getTime(m, "last_activity"),
		Active:           getBool(m, "active"),
		ResourceCount:    getInt(m, "resource_count"),
		ErrorCount:       getInt(m, "error_count"),
		LoadTimeMs:       getInt64(m, "load_time_ms"),
		InteractionCount: getInt(m, "interaction_count"),
		MutationCount:    getInt(m, "mutation_count"),
		DetailLimit:      limit,
	}

	// Track which sections have full detail
	var detailSections []string

	if resources, ok := m["resources"].([]interface{}); ok {
		if summary.ResourceCount == 0 {
			summary.ResourceCount = len(resources)
		}
		summary.ResourcesByType = make(map[string]int)
		for _, r := range resources {
			if url, ok := r.(string); ok {
				resType := categorizeResource(url)
				summary.ResourcesByType[resType]++
			}
		}

		if detailSet["resources"] {
			detailSections = append(detailSections, "resources")
			maxResources := limit
			if len(resources) < maxResources {
				maxResources = len(resources)
			}
			for i := 0; i < maxResources; i++ {
				if url, ok := resources[i].(string); ok {
					summary.Resources = append(summary.Resources, url)
				}
			}
		}
	}

	if errors, ok := m["errors"].([]interface{}); ok {
		if summary.ErrorCount == 0 {
			summary.ErrorCount = len(errors)
		}
		summary.ErrorsByType = make(map[string]int)
		errorCounts := make(map[string]*ErrorSummary)

		for _, e := range errors {
			if em, ok := e.(map[string]interface{}); ok {
				msg := getString(em, "message")
				errType := getString(em, "type")
				if errType == "" {
					errType = "Error"
				}
				summary.ErrorsByType[errType]++

				key := msg
				if len(key) > 100 {
					key = key[:100]
				}
				if existing, ok := errorCounts[key]; ok {
					existing.Count++
				} else {
					errorCounts[key] = &ErrorSummary{
						Message: msg,
						Type:    errType,
						Count:   1,
					}
				}
			}
		}

		for _, es := range errorCounts {
			summary.UniqueErrors = append(summary.UniqueErrors, *es)
			if len(summary.UniqueErrors) >= 5 {
				break
			}
		}

		if detailSet["errors"] {
			detailSections = append(detailSections, "errors")
			maxErrors := limit
			if len(errors) < maxErrors {
				maxErrors = len(errors)
			}
			for i := 0; i < maxErrors; i++ {
				if em, ok := errors[i].(map[string]interface{}); ok {
					compactErr := convertToCompactError(em)
					summary.Errors = append(summary.Errors, compactErr)
				}
			}
		}
	}

	if interactions, ok := m["interactions"].([]interface{}); ok {
		summary.InteractionsByType = make(map[string]int)
		for _, i := range interactions {
			if im, ok := i.(map[string]interface{}); ok {
				iType := getString(im, "event_type")
				if iType == "" {
					iType = getString(im, "type")
				}
				if iType == "" {
					iType = "unknown"
				}
				summary.InteractionsByType[iType]++
			}
		}

		if detailSet["interactions"] {
			detailSections = append(detailSections, "interactions")
			maxInteractions := limit
			if len(interactions) < maxInteractions {
				maxInteractions = len(interactions)
			}

			start := len(interactions) - maxInteractions
			if start < 0 {
				start = 0
			}
			for i := start; i < len(interactions); i++ {
				if im, ok := interactions[i].(map[string]interface{}); ok {
					summary.Interactions = append(summary.Interactions, im)
				}
			}
		} else {

			recentLimit := 5
			if limit < recentLimit {
				recentLimit = limit
			}
			start := len(interactions) - recentLimit
			if start < 0 {
				start = 0
			}
			for i := start; i < len(interactions); i++ {
				if im, ok := interactions[i].(map[string]interface{}); ok {
					summary.RecentInteractions = append(summary.RecentInteractions, im)
				}
			}
		}
	}

	if mutations, ok := m["mutations"].([]interface{}); ok {
		summary.MutationsByType = make(map[string]int)
		for _, mut := range mutations {
			if mm, ok := mut.(map[string]interface{}); ok {
				mType := getString(mm, "mutation_type")
				if mType == "" {
					mType = getString(mm, "type")
				}
				if mType == "" {
					mType = "unknown"
				}
				summary.MutationsByType[mType]++
			}
		}

		if detailSet["mutations"] {
			detailSections = append(detailSections, "mutations")
			maxMutations := limit
			if len(mutations) < maxMutations {
				maxMutations = len(mutations)
			}

			start := len(mutations) - maxMutations
			if start < 0 {
				start = 0
			}
			for i := start; i < len(mutations); i++ {
				if mm, ok := mutations[i].(map[string]interface{}); ok {
					summary.Mutations = append(summary.Mutations, mm)
				}
			}
		} else {

			recentLimit := 5
			if limit < recentLimit {
				recentLimit = limit
			}
			start := len(mutations) - recentLimit
			if start < 0 {
				start = 0
			}
			for i := start; i < len(mutations); i++ {
				if mm, ok := mutations[i].(map[string]interface{}); ok {
					summary.RecentMutations = append(summary.RecentMutations, mm)
				}
			}
		}
	}

	if perf, ok := m["performance"].(map[string]interface{}); ok {
		// Read the domain JSON tags emitted by compactPageSession.
		summary.FirstPaintMs = getInt64(perf, "first_paint")
		summary.DOMContentLoaded = getInt64(perf, "dom_content_loaded")
		summary.PageHeight = getInt(perf, "page_height")
		summary.PageWidth = getInt(perf, "page_width")
		summary.ViewportHeight = getInt(perf, "viewport_height")
		summary.ViewportWidth = getInt(perf, "viewport_width")
		if summary.LoadTimeMs == 0 {
			summary.LoadTimeMs = getInt64(perf, "load_event_end")
		}
	}

	if len(detailSections) > 0 {
		summary.DetailSections = detailSections
	}

	return summary
}

// categorizeResource determines the type of resource from its URL.
func categorizeResource(url string) string {
	lower := strings.ToLower(url)

	if strings.HasSuffix(lower, ".js") || strings.Contains(lower, ".js?") {
		return "js"
	}
	if strings.HasSuffix(lower, ".css") || strings.Contains(lower, ".css?") {
		return "css"
	}
	if strings.HasSuffix(lower, ".png") || strings.HasSuffix(lower, ".jpg") ||
		strings.HasSuffix(lower, ".jpeg") || strings.HasSuffix(lower, ".gif") ||
		strings.HasSuffix(lower, ".webp") || strings.HasSuffix(lower, ".svg") ||
		strings.HasSuffix(lower, ".ico") {
		return "image"
	}
	if strings.HasSuffix(lower, ".woff") || strings.HasSuffix(lower, ".woff2") ||
		strings.HasSuffix(lower, ".ttf") || strings.HasSuffix(lower, ".eot") {
		return "font"
	}
	if strings.HasSuffix(lower, ".json") || strings.Contains(lower, "/api/") {
		return "api"
	}
	if strings.HasSuffix(lower, ".html") || strings.HasSuffix(lower, "/") {
		return "html"
	}

	return "other"
}

// convertToCompactError converts a raw error map to a CompactError with truncated fields.
// This prevents token overflow when returning error details for pages with many errors.
func convertToCompactError(em map[string]interface{}) CompactError {
	compact := CompactError{
		Message: getString(em, "message"),
		Type:    getString(em, "type"),
		URL:     getString(em, "url"),
	}

	if compact.Type == "" {
		compact.Type = "Error"
	}

	source := getString(em, "source")
	lineno := getInt(em, "lineno")
	colno := getInt(em, "colno")

	if source != "" {

		parts := strings.Split(source, "/")
		filename := parts[len(parts)-1]

		if lineno > 0 {
			if colno > 0 {
				compact.Location = fmt.Sprintf("%s:%d:%d", filename, lineno, colno)
			} else {
				compact.Location = fmt.Sprintf("%s:%d", filename, lineno)
			}
		} else {
			compact.Location = filename
		}
	}

	stack := getString(em, "stack")
	if stack != "" {
		lines := strings.Split(stack, "\n")
		maxLines := 3
		if len(lines) < maxLines {
			maxLines = len(lines)
		}

		var preview []string
		for i := 0; i < maxLines; i++ {
			line := strings.TrimSpace(lines[i])

			if len(line) > 120 {
				line = line[:117] + "..."
			}
			preview = append(preview, line)
		}
		compact.StackPreview = strings.Join(preview, "\n")

		if len(lines) > maxLines {
			compact.StackPreview += fmt.Sprintf("\n... (%d more lines)", len(lines)-maxLines)
		}
	}

	if ts, ok := em["timestamp"].(string); ok {
		compact.Timestamp = ts
	}

	if len(compact.Message) > 500 {
		compact.Message = compact.Message[:497] + "..."
	}

	return compact
}

// buildProxyLogSummary aggregates log entries into a compact summary.
func buildProxyLogSummary(entries []interface{}, detailSet map[string]bool, limit int) ProxyLogSummary {
	summary := ProxyLogSummary{
		TotalEntries:       len(entries),
		EntriesByType:      make(map[string]int),
		ErrorsByType:       make(map[string]int),
		HTTPByStatus:       make(map[string]int),
		HTTPByMethod:       make(map[string]int),
		InteractionsByType: make(map[string]int),
		MutationsByType:    make(map[string]int),
		OtherTypes:         make(map[string]int),
		DetailLimit:        limit,
	}

	var detailSections []string
	var errors []map[string]interface{}
	var httpRequests []map[string]interface{}
	var performance []map[string]interface{}
	var interactions []map[string]interface{}
	var mutations []map[string]interface{}
	var other []map[string]interface{}

	var firstTime, lastTime time.Time
	errorCounts := make(map[string]*ErrorSummary)
	var totalLoadTime int64
	var perfCount int

	for _, e := range entries {
		em, ok := e.(map[string]interface{})
		if !ok {
			continue
		}

		logType := getString(em, "type")
		summary.EntriesByType[logType]++

		data, _ := em[logType].(map[string]interface{})

		if data != nil {
			if ts, ok := data["timestamp"].(string); ok {
				if t, err := time.Parse(time.RFC3339, ts); err == nil {
					if firstTime.IsZero() || t.Before(firstTime) {
						firstTime = t
					}
					if lastTime.IsZero() || t.After(lastTime) {
						lastTime = t
					}
				}
			}
		}

		switch logType {
		case "error":
			summary.ErrorCount++
			if data != nil {
				errors = append(errors, data)

				msg := getString(data, "message")
				// FrontendError carries no dedicated "type" field on the wire,
				// so reading data["type"] always missed and bucketed every error
				// as {"Error": N}. Derive the JS error class from the message
				// prefix (e.g. "TypeError: ...") instead; keep an explicit "type"
				// if a future producer ever sends one.
				errType := getString(data, "type")
				if errType == "" {
					errType = classifyErrorType(msg)
				}
				summary.ErrorsByType[errType]++

				key := msg
				if len(key) > 100 {
					key = key[:100]
				}
				if existing, ok := errorCounts[key]; ok {
					existing.Count++
				} else {
					errorCounts[key] = &ErrorSummary{
						Message: msg,
						Type:    errType,
						Count:   1,
					}
				}
			}

		case "http":
			summary.HTTPCount++
			if data != nil {
				httpRequests = append(httpRequests, data)

				statusCode := getInt(data, "status_code")
				statusGroup := fmt.Sprintf("%dxx", statusCode/100)
				summary.HTTPByStatus[statusGroup]++

				method := getString(data, "method")
				if method != "" {
					summary.HTTPByMethod[method]++
				}
			}

		case "performance":
			summary.PerformanceCount++
			if data != nil {
				performance = append(performance, data)

				loadTime := getInt64(data, "load_event_end")
				if loadTime > 0 {
					totalLoadTime += loadTime
					perfCount++
				}
			}

		case "interaction":
			summary.InteractionCount++
			if data != nil {
				interactions = append(interactions, data)

				iType := getString(data, "event_type")
				if iType == "" {
					iType = getString(data, "type")
				}
				if iType != "" {
					summary.InteractionsByType[iType]++
				}
			}

		case "mutation":
			summary.MutationCount++
			if data != nil {
				mutations = append(mutations, data)

				// MutationEvent's wire field is "mutation_type", not "type";
				// reading "type" left MutationsByType permanently empty. Keep a
				// "type" fallback for any synthetic/legacy producers.
				mType := getString(data, "mutation_type")
				if mType == "" {
					mType = getString(data, "type")
				}
				if mType != "" {
					summary.MutationsByType[mType]++
				}
			}

		default:
			summary.OtherCount++
			summary.OtherTypes[logType]++
			if data != nil {
				other = append(other, data)
			}
		}
	}

	if !firstTime.IsZero() && !lastTime.IsZero() {
		summary.TimeRange = TimeRange{Start: firstTime, End: lastTime}
	}

	if perfCount > 0 {
		summary.AvgLoadTime = totalLoadTime / int64(perfCount)
	}

	// Deterministic "Top 10": rank unique errors by occurrence count desc, then
	// message asc for stable ties. Ranging the map and taking the first 10
	// (previous behavior) returned an arbitrary set in Go's random map order, so
	// a rare error could shadow a frequent one and the same input could yield
	// different "top" errors run to run.
	uniqueErrors := make([]ErrorSummary, 0, len(errorCounts))
	for _, es := range errorCounts {
		uniqueErrors = append(uniqueErrors, *es)
	}
	sort.Slice(uniqueErrors, func(i, j int) bool {
		if uniqueErrors[i].Count != uniqueErrors[j].Count {
			return uniqueErrors[i].Count > uniqueErrors[j].Count
		}
		return uniqueErrors[i].Message < uniqueErrors[j].Message
	})
	if len(uniqueErrors) > 10 {
		uniqueErrors = uniqueErrors[:10]
	}
	summary.UniqueErrors = uniqueErrors

	if detailSet["errors"] {
		detailSections = append(detailSections, "errors")
		maxErrors := limit
		if len(errors) < maxErrors {
			maxErrors = len(errors)
		}

		start := len(errors) - maxErrors
		if start < 0 {
			start = 0
		}
		for i := start; i < len(errors); i++ {
			summary.Errors = append(summary.Errors, convertToCompactError(errors[i]))
		}
	} else if summary.ErrorCount > 0 {

		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(errors) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(errors); i++ {
			summary.RecentErrors = append(summary.RecentErrors, convertToCompactError(errors[i]))
		}
	}

	if detailSet["http"] {
		detailSections = append(detailSections, "http")
		maxHTTP := limit
		if len(httpRequests) < maxHTTP {
			maxHTTP = len(httpRequests)
		}
		start := len(httpRequests) - maxHTTP
		if start < 0 {
			start = 0
		}
		for i := start; i < len(httpRequests); i++ {
			summary.HTTPRequests = append(summary.HTTPRequests, convertToCompactHTTP(httpRequests[i]))
		}
	} else if summary.HTTPCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(httpRequests) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(httpRequests); i++ {
			summary.RecentHTTP = append(summary.RecentHTTP, convertToCompactHTTP(httpRequests[i]))
		}
	}

	if detailSet["performance"] {
		detailSections = append(detailSections, "performance")
		maxPerf := limit
		if len(performance) < maxPerf {
			maxPerf = len(performance)
		}
		start := len(performance) - maxPerf
		if start < 0 {
			start = 0
		}
		for i := start; i < len(performance); i++ {
			summary.Performance = append(summary.Performance, convertToCompactPerformance(performance[i]))
		}
	} else if summary.PerformanceCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(performance) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(performance); i++ {
			summary.RecentPerformance = append(summary.RecentPerformance, convertToCompactPerformance(performance[i]))
		}
	}

	if detailSet["interactions"] {
		detailSections = append(detailSections, "interactions")
		maxInt := limit
		if len(interactions) < maxInt {
			maxInt = len(interactions)
		}
		start := len(interactions) - maxInt
		if start < 0 {
			start = 0
		}
		for i := start; i < len(interactions); i++ {
			summary.Interactions = append(summary.Interactions, convertToCompactInteraction(interactions[i]))
		}
	} else if summary.InteractionCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(interactions) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(interactions); i++ {
			summary.RecentInteractions = append(summary.RecentInteractions, convertToCompactInteraction(interactions[i]))
		}
	}

	if detailSet["mutations"] {
		detailSections = append(detailSections, "mutations")
		maxMut := limit
		if len(mutations) < maxMut {
			maxMut = len(mutations)
		}
		start := len(mutations) - maxMut
		if start < 0 {
			start = 0
		}
		for i := start; i < len(mutations); i++ {
			summary.Mutations = append(summary.Mutations, convertToCompactMutation(mutations[i]))
		}
	} else if summary.MutationCount > 0 {
		recentLimit := 5
		if limit < recentLimit {
			recentLimit = limit
		}
		start := len(mutations) - recentLimit
		if start < 0 {
			start = 0
		}
		for i := start; i < len(mutations); i++ {
			summary.RecentMutations = append(summary.RecentMutations, convertToCompactMutation(mutations[i]))
		}
	}

	if detailSet["other"] && summary.OtherCount > 0 {
		detailSections = append(detailSections, "other")
		maxOther := limit
		if len(other) < maxOther {
			maxOther = len(other)
		}
		start := len(other) - maxOther
		if start < 0 {
			start = 0
		}
		for i := start; i < len(other); i++ {
			summary.Other = append(summary.Other, convertToCompactLogEntry(other[i]))
		}
	}

	if len(detailSections) > 0 {
		summary.DetailSections = detailSections
	}

	return summary
}

func convertToCompactHTTP(data map[string]interface{}) CompactHTTPRequest {
	compact := CompactHTTPRequest{
		Method:     getString(data, "method"),
		URL:        getString(data, "url"),
		StatusCode: getInt(data, "status_code"),
		Duration:   getInt64(data, "duration") / 1000000,
		Error:      getString(data, "error"),
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	if len(compact.URL) > 100 {
		compact.URL = compact.URL[:97] + "..."
	}

	return compact
}

func convertToCompactPerformance(data map[string]interface{}) CompactPerformance {
	compact := CompactPerformance{
		URL:              getString(data, "url"),
		LoadTimeMs:       getInt64(data, "load_event_end"),
		FirstPaintMs:     getInt64(data, "first_paint"),
		DOMContentLoaded: getInt64(data, "dom_content_loaded"),
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	if len(compact.URL) > 100 {
		compact.URL = compact.URL[:97] + "..."
	}

	return compact
}

func convertToCompactInteraction(data map[string]interface{}) CompactInteraction {
	compact := CompactInteraction{
		Type: getString(data, "event_type"),
	}

	if compact.Type == "" {
		compact.Type = getString(data, "type")
	}

	if target, ok := data["target"].(map[string]interface{}); ok {
		selector := getString(target, "selector")
		if selector != "" {
			compact.Target = selector
		} else {
			tag := getString(target, "tag_name")
			id := getString(target, "id")
			if id != "" {
				compact.Target = fmt.Sprintf("%s#%s", tag, id)
			} else {
				compact.Target = tag
			}
		}
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	if len(compact.Target) > 80 {
		compact.Target = compact.Target[:77] + "..."
	}

	return compact
}

// classifyErrorType derives a JS error class name from a frontend error's
// message. Browser errors conventionally serialize as "TypeError: x is not a
// function"; the token before the first colon is the constructor name.
// FrontendError carries no dedicated type field, so the message prefix is the
// only signal — without this, ErrorsByType degenerates to {"Error": N}.
func classifyErrorType(message string) string {
	msg := strings.TrimSpace(message)
	msg = strings.TrimPrefix(msg, "Uncaught ")
	if idx := strings.Index(msg, ":"); idx > 0 {
		name := strings.TrimSpace(msg[:idx])
		// Accept only a compact single token that looks like a JS error class
		// (ends in "Error"/"Exception"). This rejects URL schemes ("http:") and
		// prose prefixes that merely happen to contain a colon.
		if len(name) <= 40 && !strings.ContainsAny(name, " \t\n/\\") &&
			(strings.HasSuffix(name, "Error") || strings.HasSuffix(name, "Exception")) {
			return name
		}
	}
	return "Error"
}

func convertToCompactMutation(data map[string]interface{}) CompactMutation {
	// Wire field is "mutation_type"; "type" is a legacy/synthetic fallback.
	mType := getString(data, "mutation_type")
	if mType == "" {
		mType = getString(data, "type")
	}
	compact := CompactMutation{
		Type: mType,
	}

	if target, ok := data["target"].(map[string]interface{}); ok {
		selector := getString(target, "selector")
		if selector != "" {
			compact.Target = selector
		} else {
			tag := getString(target, "tag_name")
			id := getString(target, "id")
			if id != "" {
				compact.Target = fmt.Sprintf("%s#%s", tag, id)
			} else {
				compact.Target = tag
			}
		}
	}

	if nodes, ok := data["nodes"].([]interface{}); ok {
		compact.Count = len(nodes)
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	if len(compact.Target) > 80 {
		compact.Target = compact.Target[:77] + "..."
	}

	return compact
}

func convertToCompactLogEntry(data map[string]interface{}) CompactLogEntry {
	compact := CompactLogEntry{
		Type:    getString(data, "type"),
		Message: getString(data, "message"),
	}

	if compact.Message == "" {
		compact.Message = getString(data, "level")
	}

	if ts, ok := data["timestamp"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			compact.Timestamp = t
		}
	}

	if len(compact.Message) > 200 {
		compact.Message = compact.Message[:197] + "..."
	}

	return compact
}

func convertToPageSessionOutput(m map[string]interface{}) PageSessionOutput {
	output := PageSessionOutput{
		ID:               getString(m, "id"),
		URL:              getString(m, "url"),
		PageTitle:        getString(m, "page_title"),
		Active:           getBool(m, "active"),
		ResourceCount:    getInt(m, "resource_count"),
		ErrorCount:       getInt(m, "error_count"),
		HasPerformance:   getBool(m, "has_performance"),
		LoadTime:         getInt64(m, "load_time_ms"),
		InteractionCount: getInt(m, "interaction_count"),
		MutationCount:    getInt(m, "mutation_count"),
	}

	if ts, ok := m["start_time"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			output.StartTime = t
		}
	}
	if ts, ok := m["last_activity"].(string); ok {
		if t, err := time.Parse(time.RFC3339, ts); err == nil {
			output.LastActivity = t
		}
	}

	if resources, ok := m["resources"].([]interface{}); ok {
		for _, r := range resources {
			if rs, ok := r.(string); ok {
				output.Resources = append(output.Resources, rs)
			}
		}
	}

	if errors, ok := m["errors"].([]interface{}); ok {
		for _, e := range errors {
			if em, ok := e.(map[string]interface{}); ok {

				errMap := make(map[string]interface{})
				for k, v := range em {
					errMap[k] = v
				}
				output.Errors = append(output.Errors, errMap)
			}
		}
	}

	if interactions, ok := m["interactions"].([]interface{}); ok {
		for _, i := range interactions {
			if im, ok := i.(map[string]interface{}); ok {
				interactionMap := make(map[string]interface{})
				for k, v := range im {
					interactionMap[k] = v
				}
				output.Interactions = append(output.Interactions, interactionMap)
			}
		}
	}

	if mutations, ok := m["mutations"].([]interface{}); ok {
		for _, m := range mutations {
			if mm, ok := m.(map[string]interface{}); ok {
				mutationMap := make(map[string]interface{})
				for k, v := range mm {
					mutationMap[k] = v
				}
				output.Mutations = append(output.Mutations, mutationMap)
			}
		}
	}

	return output
}
