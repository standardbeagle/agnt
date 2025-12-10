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

// PageSession represents a page view and its associated resources.
type PageSession struct {
	ID              string             `json:"id"`
	URL             string             `json:"url"`
	BrowserSession  string             `json:"browser_session,omitempty"` // Browser tab session ID
	PageTitle       string             `json:"page_title,omitempty"`
	StartTime       time.Time          `json:"start_time"`
	LastActivity    time.Time          `json:"last_activity"`
	DocumentRequest *HTTPLogEntry      `json:"document_request,omitempty"`
	Resources       []HTTPLogEntry     `json:"resources"`
	Errors          []FrontendError    `json:"errors,omitempty"`
	Performance     *PerformanceMetric `json:"performance,omitempty"`
	Active          bool               `json:"active"`

	// User interaction tracking
	Interactions     []InteractionEvent `json:"interactions,omitempty"`
	InteractionCount int                `json:"interaction_count"` // Total count (may exceed slice length)

	// DOM mutation tracking
	Mutations     []MutationEvent `json:"mutations,omitempty"`
	MutationCount int             `json:"mutation_count"` // Total count (may exceed slice length)
}

// PageTracker tracks page sessions and groups requests by page.
type PageTracker struct {
	sessions              sync.Map // map[string]*PageSession (keyed by session ID)
	urlToSession          sync.Map // map[string]string (URL to session ID)
	browserSessionToPage  sync.Map // map[string]string (browser session ID to page session ID)
	sessionSeq            atomic.Int64
	maxSessions           int
	sessionTimeout        time.Duration
	mu                    sync.RWMutex
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

// TrackHTTPRequest processes an HTTP request and associates it with a page session.
func (pt *PageTracker) TrackHTTPRequest(entry HTTPLogEntry) {
	// Determine if this is a document (HTML) request
	isDocument := isDocumentRequest(entry)

	if isDocument {
		// Create new page session
		pt.createPageSession(entry)
	} else {
		// Associate resource with existing page session
		pt.addResourceToSession(entry)
	}
}

// TrackError associates a frontend error with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackError(err FrontendError, browserSessionID string) {
	sessionID := pt.findSessionByBrowserSession(browserSessionID)
	if sessionID == "" {
		// Fall back to URL matching if browser session not found
		sessionID = pt.findSessionByURL(err.URL)
	}
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)

	// Update browser session mapping if not set
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}

	session.Errors = append(session.Errors, err)
	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
}

// TrackPerformance associates performance metrics with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackPerformance(perf PerformanceMetric, browserSessionID string) {
	sessionID := pt.findSessionByBrowserSession(browserSessionID)
	if sessionID == "" {
		// Fall back to URL matching if browser session not found
		sessionID = pt.findSessionByURL(perf.URL)
	}
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)

	// Update browser session mapping if not set
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}

	session.Performance = &perf
	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
}

// TrackInteraction associates a user interaction event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackInteraction(interaction InteractionEvent, browserSessionID string) {
	sessionID := pt.findSessionByBrowserSession(browserSessionID)
	if sessionID == "" {
		// Fall back to URL matching if browser session not found
		sessionID = pt.findSessionByURL(interaction.URL)
	}
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.InteractionCount++

	// Update browser session mapping if not set
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}

	// Maintain bounded history using circular buffer behavior
	if len(session.Interactions) < MaxInteractionsPerSession {
		session.Interactions = append(session.Interactions, interaction)
	} else {
		// Shift and append to maintain most recent
		copy(session.Interactions, session.Interactions[1:])
		session.Interactions[MaxInteractionsPerSession-1] = interaction
	}

	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
}

// TrackMutation associates a DOM mutation event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackMutation(mutation MutationEvent, browserSessionID string) {
	sessionID := pt.findSessionByBrowserSession(browserSessionID)
	if sessionID == "" {
		// Fall back to URL matching if browser session not found
		sessionID = pt.findSessionByURL(mutation.URL)
	}
	if sessionID == "" {
		return
	}

	val, ok := pt.sessions.Load(sessionID)
	if !ok {
		return
	}

	session := val.(*PageSession)
	session.MutationCount++

	// Update browser session mapping if not set
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage.Store(browserSessionID, sessionID)
	}

	// Maintain bounded history using circular buffer behavior
	if len(session.Mutations) < MaxMutationsPerSession {
		session.Mutations = append(session.Mutations, mutation)
	} else {
		// Shift and append to maintain most recent
		copy(session.Mutations, session.Mutations[1:])
		session.Mutations[MaxMutationsPerSession-1] = mutation
	}

	session.LastActivity = time.Now()
	pt.sessions.Store(sessionID, session)
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

// createPageSession creates a new page session for a document request.
func (pt *PageTracker) createPageSession(entry HTTPLogEntry) {
	sessionID := pt.generateSessionID()
	now := time.Now()

	session := &PageSession{
		ID:              sessionID,
		URL:             entry.URL,
		StartTime:       now,
		LastActivity:    now,
		DocumentRequest: &entry,
		Resources:       make([]HTTPLogEntry, 0),
		Errors:          make([]FrontendError, 0),
		Active:          true,
		Interactions:    make([]InteractionEvent, 0),
		Mutations:       make([]MutationEvent, 0),
	}

	pt.sessions.Store(sessionID, session)
	pt.urlToSession.Store(normalizeURL(entry.URL), sessionID)

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

	return strings.Contains(contentType, "text/html") ||
		strings.HasSuffix(entry.URL, ".html") ||
		(entry.Method == "GET" && !hasResourceExtension(entry.URL))
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
