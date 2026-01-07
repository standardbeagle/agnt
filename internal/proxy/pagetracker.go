package proxy

import (
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Limits for interaction and mutation history per session
const (
	MaxInteractionsPerSession = 200
	MaxMutationsPerSession    = 100
)

// appendBounded appends an item to a slice while maintaining a maximum length.
// If the slice is at capacity, it shifts elements left (FIFO) and appends to the end.
// Returns the updated slice.
func appendBounded[T any](slice []T, item T, maxLen int) []T {
	if len(slice) < maxLen {
		return append(slice, item)
	}
	// Shift and append to maintain most recent
	copy(slice, slice[1:])
	slice[maxLen-1] = item
	return slice
}

// PageSession represents a browser tab session and its navigation history.
// All navigations within the same browser tab are grouped together.
type PageSession struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`                       // Current/most recent URL
	BrowserSession string    `json:"browser_session,omitempty"` // Browser tab session ID (from cookie)
	PageTitle      string    `json:"page_title,omitempty"`
	StartTime      time.Time `json:"start_time"`
	LastActivity   time.Time `json:"last_activity"`
	Active         bool      `json:"active"`

	// Navigation history - all document requests in this tab session
	Navigations     []HTTPLogEntry     `json:"navigations,omitempty"`
	DocumentRequest *HTTPLogEntry      `json:"document_request,omitempty"` // Most recent document request (for backwards compat)
	Resources       []HTTPLogEntry     `json:"resources"`
	Errors          []FrontendError    `json:"errors,omitempty"`
	Performance     *PerformanceMetric `json:"performance,omitempty"`

	// User interaction tracking
	Interactions     []InteractionEvent `json:"interactions,omitempty"`
	InteractionCount int                `json:"interaction_count"` // Total count (may exceed slice length)

	// DOM mutation tracking
	Mutations     []MutationEvent `json:"mutations,omitempty"`
	MutationCount int             `json:"mutation_count"` // Total count (may exceed slice length)
}

// PageTracker tracks page sessions and groups requests by page.
type PageTracker struct {
	sessions             sync.Map // map[string]*PageSession (keyed by session ID)
	urlToSession         sync.Map // map[string]string (URL to session ID)
	browserSessionToPage sync.Map // map[string]string (browser session ID to page session ID)
	sessionSeq           atomic.Int64
	maxSessions          int
	sessionTimeout       time.Duration
}

// NewPageTracker creates a new page tracker.
func NewPageTracker(maxSessions int, sessionTimeout time.Duration) *PageTracker {
	if maxSessions <= 0 {
		maxSessions = 100
	}
	if sessionTimeout <= 0 {
		sessionTimeout = 5 * time.Minute
	}

	return &PageTracker{
		maxSessions:    maxSessions,
		sessionTimeout: sessionTimeout,
	}
}

// ResolveSession finds a session by browser session ID with URL fallback.
// Returns empty string if no session found.
func (pt *PageTracker) ResolveSession(browserSessionID, url string) string {
	if sessionID := pt.findSessionByBrowserSession(browserSessionID); sessionID != "" {
		return sessionID
	}
	return pt.findSessionByURL(url)
}

// updateSessionWithBrowserID updates the session's browser mapping if not set,
// updates the last activity timestamp, and stores the session.
func (pt *PageTracker) updateSessionWithBrowserID(sessionID string, session *PageSession, browserSessionID string) {
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}
	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
}

// TrackHTTPRequest processes an HTTP request and associates it with a page session.
func (pt *PageTracker) TrackHTTPRequest(entry HTTPLogEntry) {
	// Extract browser session ID from cookie
	browserSessionID := extractBrowserSessionID(entry.RequestHeaders)

	// Determine if this is a document (HTML) request
	isDocument := isDocumentRequest(entry)

	if isDocument {
		// Create or update page session for this browser tab
		pt.createOrUpdatePageSession(entry, browserSessionID)
	} else {
		// Associate resource with existing page session
		pt.addResourceToSession(entry)
	}
}

// TrackError associates a frontend error with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackError(err FrontendError, browserSessionID string) {
	sessionID := pt.ResolveSession(browserSessionID, err.URL)
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.Errors = append(session.Errors, err)
	pt.updateSessionWithBrowserID(sessionID, session, browserSessionID)
}

// TrackPerformance associates performance metrics with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackPerformance(perf PerformanceMetric, browserSessionID string) {
	sessionID := pt.ResolveSession(browserSessionID, perf.URL)
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.Performance = &perf
	pt.updateSessionWithBrowserID(sessionID, session, browserSessionID)
}

// TrackInteraction associates a user interaction event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackInteraction(interaction InteractionEvent, browserSessionID string) {
	sessionID := pt.ResolveSession(browserSessionID, interaction.URL)
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.InteractionCount++
	session.Interactions = appendBounded(session.Interactions, interaction, MaxInteractionsPerSession)
	pt.updateSessionWithBrowserID(sessionID, session, browserSessionID)
}

// TrackMutation associates a DOM mutation event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackMutation(mutation MutationEvent, browserSessionID string) {
	sessionID := pt.ResolveSession(browserSessionID, mutation.URL)
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.MutationCount++
	session.Mutations = appendBounded(session.Mutations, mutation, MaxMutationsPerSession)
	pt.updateSessionWithBrowserID(sessionID, session, browserSessionID)
}

// GetActiveSessions returns all currently active page sessions.
func (pt *PageTracker) GetActiveSessions() []*PageSession {
	var sessions []*PageSession
	now := time.Now()

	pt.sessions.Range(func(key, value any) bool {
		session := value.(*PageSession)

		// Check if session is still active (within timeout)
		if now.Sub(session.LastActivity) < pt.sessionTimeout {
			session.Active = true
			sessions = append(sessions, session)
		} else {
			session.Active = false
		}

		return true
	})

	return sessions
}

// GetSession returns a specific page session by ID.
func (pt *PageTracker) GetSession(sessionID string) (*PageSession, bool) {
	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return nil, false
	}
	return val.(*PageSession), true
}

// Clear removes all page sessions.
func (pt *PageTracker) Clear() {
	pt.sessions = sync.Map{}
	pt.urlToSession = sync.Map{}
	pt.browserSessionToPage = sync.Map{}
	pt.sessionSeq.Store(0)
}

// createOrUpdatePageSession creates a new page session or updates an existing one for the same browser tab.
func (pt *PageTracker) createOrUpdatePageSession(entry HTTPLogEntry, browserSessionID string) {
	now := time.Now()

	// If we have a browser session ID, try to find existing session for this tab
	if browserSessionID != "" {
		existingSessionID := pt.findSessionByBrowserSession(browserSessionID)
		if existingSessionID != "" {
			val, ok := pt.sessions.Load(existingSessionID)
			if ok {
				session := val.(*PageSession)
				// Update existing session with new navigation
				session.URL = entry.URL
				session.LastActivity = now
				session.DocumentRequest = &entry
				session.Navigations = append(session.Navigations, entry)
				// Clear resources for new page (they belong to old navigation)
				session.Resources = make([]HTTPLogEntry, 0)
				pt.sessions.Store(existingSessionID, session)
				pt.urlToSession.Store(normalizeURL(entry.URL), existingSessionID)
				return
			}
		}
	}

	// Create new session
	sessionID := pt.generateSessionID()
	session := &PageSession{
		ID:              sessionID,
		URL:             entry.URL,
		BrowserSession:  browserSessionID,
		StartTime:       now,
		LastActivity:    now,
		DocumentRequest: &entry,
		Navigations:     []HTTPLogEntry{entry},
		Resources:       make([]HTTPLogEntry, 0),
		Errors:          make([]FrontendError, 0),
		Active:          true,
		Interactions:    make([]InteractionEvent, 0),
		Mutations:       make([]MutationEvent, 0),
	}

	pt.sessions.Store(sessionID, session)
	pt.urlToSession.Store(normalizeURL(entry.URL), sessionID)

	// Register browser session mapping
	if browserSessionID != "" {
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}

	// Cleanup old sessions if we exceed max
	pt.cleanupOldSessions()
}

// addResourceToSession adds a resource request to the most recent matching page session.
func (pt *PageTracker) addResourceToSession(entry HTTPLogEntry) {
	// Find the session by referrer header or most recent session
	sessionID := pt.findSessionForResource(entry)
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.Resources = append(session.Resources, entry)
	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
}

// findSessionForResource finds the appropriate page session for a resource request.
func (pt *PageTracker) findSessionForResource(entry HTTPLogEntry) string {
	// Try to use Referer header to match session
	referer := entry.RequestHeaders["Referer"]
	if referer == "" {
		referer = entry.RequestHeaders["referer"]
	}

	if referer != "" {
		sessionID := pt.findSessionByURL(referer)
		if sessionID != "" {
			return sessionID
		}
	}

	// Fall back to finding most recent active session with same origin
	return pt.findMostRecentSession(entry.URL)
}

// findSessionByBrowserSession finds a session ID by browser session ID.
func (pt *PageTracker) findSessionByBrowserSession(browserSessionID string) string {
	if browserSessionID == "" {
		return ""
	}
	val, ok := pt.browserSessionToPage.Load(browserSessionID)
	if ok {
		return val.(string)
	}
	return ""
}

// findSessionByURL finds a session ID for a given URL.
func (pt *PageTracker) findSessionByURL(urlStr string) string {
	normalized := normalizeURL(urlStr)
	val, ok := pt.urlToSession.Load(normalized)
	if ok {
		return val.(string)
	}
	return ""
}

// findMostRecentSession finds the most recent active session with matching origin.
func (pt *PageTracker) findMostRecentSession(urlStr string) string {
	targetOrigin := getOrigin(urlStr)
	if targetOrigin == "" {
		return ""
	}

	var mostRecent *PageSession
	var mostRecentID string

	pt.sessions.Range(func(key, value any) bool {
		session := value.(*PageSession)
		sessionOrigin := getOrigin(session.URL)

		if sessionOrigin == targetOrigin && session.Active {
			if mostRecent == nil || session.LastActivity.After(mostRecent.LastActivity) {
				mostRecent = session
				mostRecentID = key.(string)
			}
		}
		return true
	})

	return mostRecentID
}

// cleanupOldSessions removes sessions that exceed the max count.
func (pt *PageTracker) cleanupOldSessions() {
	// Count active sessions
	count := 0
	pt.sessions.Range(func(key, value any) bool {
		count++
		return true
	})

	if count <= pt.maxSessions {
		return
	}

	// Find and remove oldest sessions
	type sessionWithTime struct {
		id   string
		time time.Time
	}

	var allSessions []sessionWithTime
	pt.sessions.Range(func(key, value any) bool {
		session := value.(*PageSession)
		allSessions = append(allSessions, sessionWithTime{
			id:   key.(string),
			time: session.StartTime,
		})
		return true
	})

	// Sort by start time and remove oldest
	toRemove := count - pt.maxSessions
	for i := 0; i < toRemove && i < len(allSessions); i++ {
		// Find oldest
		oldest := i
		for j := i + 1; j < len(allSessions); j++ {
			if allSessions[j].time.Before(allSessions[oldest].time) {
				oldest = j
			}
		}

		// Remove oldest session
		pt.sessions.Delete(allSessions[oldest].id)

		// Also clean up URL mapping
		if val, ok := pt.sessions.Load(allSessions[oldest].id); ok {
			session := val.(*PageSession)
			pt.urlToSession.Delete(normalizeURL(session.URL))
		}
	}
}

// generateSessionID generates a unique session ID.
func (pt *PageTracker) generateSessionID() string {
	seq := pt.sessionSeq.Add(1)
	return "page-" + itoa(int(seq))
}

// Helper functions

// isDocumentRequest determines if an HTTP request is for a document (HTML).
func isDocumentRequest(entry HTTPLogEntry) bool {
	contentType := entry.ResponseHeaders["Content-Type"]
	if contentType == "" {
		contentType = entry.ResponseHeaders["content-type"]
	}

	// Explicit HTML response - definitely a document
	if strings.Contains(contentType, "text/html") {
		return true
	}

	// Explicit .html file extension
	if strings.HasSuffix(entry.URL, ".html") {
		return true
	}

	// JSON/API responses are NOT documents
	if strings.Contains(contentType, "application/json") ||
		strings.Contains(contentType, "text/json") {
		return false
	}

	// API paths are NOT documents (common patterns)
	if isAPIPath(entry.URL) {
		return false
	}

	// XHR/Fetch requests are typically NOT documents
	// Check for XMLHttpRequest or fetch indicators in request headers
	xhrHeader := entry.RequestHeaders["X-Requested-With"]
	if xhrHeader == "" {
		xhrHeader = entry.RequestHeaders["x-requested-with"]
	}
	if strings.EqualFold(xhrHeader, "XMLHttpRequest") {
		return false
	}

	// Accept header suggests API call if it prefers JSON
	acceptHeader := entry.RequestHeaders["Accept"]
	if acceptHeader == "" {
		acceptHeader = entry.RequestHeaders["accept"]
	}
	if strings.Contains(acceptHeader, "application/json") &&
		!strings.Contains(acceptHeader, "text/html") {
		return false
	}

	// GET request without resource extension - likely a document navigation
	// but only if none of the above API indicators matched
	return entry.Method == "GET" && !hasResourceExtension(entry.URL)
}

// isAPIPath checks if a URL path looks like an API endpoint.
func isAPIPath(urlStr string) bool {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return false
	}

	path := strings.ToLower(parsed.Path)

	// Common API path patterns
	apiPrefixes := []string{
		"/api/",
		"/v1/",
		"/v2/",
		"/v3/",
		"/graphql",
		"/rest/",
		"/_api/",
		"/ajax/",
	}

	for _, prefix := range apiPrefixes {
		if strings.HasPrefix(path, prefix) || strings.Contains(path, prefix) {
			return true
		}
	}

	return false
}

// hasResourceExtension checks if URL has a resource file extension.
func hasResourceExtension(urlStr string) bool {
	resourceExts := []string{
		".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
		".woff", ".woff2", ".ttf", ".eot", ".json", ".xml", ".txt",
		".webp", ".mp4", ".webm", ".mp3", ".wav",
	}

	urlLower := strings.ToLower(urlStr)
	for _, ext := range resourceExts {
		if strings.Contains(urlLower, ext) {
			return true
		}
	}
	return false
}

// normalizeURL normalizes a URL for comparison.
func normalizeURL(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}

	// Remove fragment
	parsed.Fragment = ""

	// Remove trailing slash for consistency
	path := parsed.Path
	if len(path) > 1 && strings.HasSuffix(path, "/") {
		path = path[:len(path)-1]
	}
	parsed.Path = path

	return parsed.String()
}

// getOrigin extracts the origin (scheme + host) from a URL.
func getOrigin(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// itoa converts an int to string without imports.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	pos := len(buf)
	for i > 0 {
		pos--
		buf[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		buf[pos] = '-'
	}
	return string(buf[pos:])
}

// extractBrowserSessionID extracts the __devtool_sid cookie from request headers.
func extractBrowserSessionID(headers map[string]string) string {
	// Try both capitalized and lowercase header names
	cookieHeader := headers["Cookie"]
	if cookieHeader == "" {
		cookieHeader = headers["cookie"]
	}
	if cookieHeader == "" {
		return ""
	}

	// Parse cookies - format is "name1=value1; name2=value2"
	const cookieName = "__devtool_sid"
	cookies := strings.Split(cookieHeader, ";")
	for _, cookie := range cookies {
		cookie = strings.TrimSpace(cookie)
		if strings.HasPrefix(cookie, cookieName+"=") {
			return strings.TrimPrefix(cookie, cookieName+"=")
		}
	}
	return ""
}
