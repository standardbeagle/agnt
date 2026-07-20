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
	FrameID        string    `json:"frame_id,omitempty"`        // Active content-frame execution context
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
//
// Concurrency model: actor. One owner goroutine (run) has exclusive access to
// the state fields below — no mutex, no sync.Map. Public methods send closures
// over the ops channel; track methods are fire-and-forget with backpressure
// (blocking send, lossless), query methods wait for a reply. The single FIFO
// channel gives each caller read-your-writes ordering: a query enqueued after
// a track op observes its effect. Stop terminates the owner goroutine; after
// Stop, track ops are no-ops and queries return zero values.
type PageTracker struct {
	ops  chan func()
	done chan struct{}

	stopped atomic.Bool
	stopWg  sync.WaitGroup

	// Owned exclusively by the run goroutine — never touch from outside ops.
	sessions             map[string]*PageSession
	urlToSession         map[string]string
	browserSessionToPage map[string]string
	frameToSession       map[string]string // content frame id -> session id (always-wrap model)
	sessionSeq           int64
	maxSessions          int
	sessionTimeout       time.Duration
}

// copyPageSession returns a snapshot of s safe to hand out of the actor. The
// five in-place-appended slices are cloned; DocumentRequest/Performance are
// replaced wholesale on update (never mutated in place) so sharing them is safe.
func copyPageSession(s *PageSession) *PageSession {
	cp := *s
	cp.Navigations = append([]HTTPLogEntry(nil), s.Navigations...)
	cp.Resources = append([]HTTPLogEntry(nil), s.Resources...)
	cp.Errors = append([]FrontendError(nil), s.Errors...)
	cp.Interactions = append([]InteractionEvent(nil), s.Interactions...)
	cp.Mutations = append([]MutationEvent(nil), s.Mutations...)
	return &cp
}

// NewPageTracker creates a new page tracker and starts its owner goroutine.
// Call Stop when the tracker is no longer needed.
func NewPageTracker(maxSessions int, sessionTimeout time.Duration) *PageTracker {
	if maxSessions <= 0 {
		maxSessions = 100
	}
	if sessionTimeout <= 0 {
		sessionTimeout = 5 * time.Minute
	}

	pt := &PageTracker{
		ops:                  make(chan func(), 256),
		done:                 make(chan struct{}),
		sessions:             make(map[string]*PageSession),
		urlToSession:         make(map[string]string),
		browserSessionToPage: make(map[string]string),
		frameToSession:       make(map[string]string),
		maxSessions:          maxSessions,
		sessionTimeout:       sessionTimeout,
	}
	pt.stopWg.Add(1)
	go pt.run()
	return pt
}

// run is the owner goroutine — the only code that touches tracker state.
func (pt *PageTracker) run() {
	defer pt.stopWg.Done()
	for {
		select {
		case op := <-pt.ops:
			op()
		case <-pt.done:
			return
		}
	}
}

// Stop terminates the owner goroutine. Idempotent. Pending and subsequent
// track ops are discarded; subsequent queries return zero values.
func (pt *PageTracker) Stop() {
	if pt.stopped.CompareAndSwap(false, true) {
		close(pt.done)
		pt.stopWg.Wait()
	}
}

// send enqueues a state-mutating op. Blocking (lossless backpressure — the
// owner goroutine only does map and slice work, so the queue drains fast).
// After Stop the op is dropped.
func (pt *PageTracker) send(op func()) {
	if pt.stopped.Load() {
		return
	}
	select {
	case pt.ops <- op:
	case <-pt.done:
	}
}

// query runs fn inside the owner goroutine and returns its result. After Stop
// it returns the zero value.
func ptQuery[T any](pt *PageTracker, fn func() T) T {
	var zero T
	if pt.stopped.Load() {
		return zero
	}
	reply := make(chan T, 1)
	select {
	case pt.ops <- func() { reply <- fn() }:
	case <-pt.done:
		return zero
	}
	select {
	case r := <-reply:
		return r
	case <-pt.done:
		return zero
	}
}

// ResolveSession finds a session by browser session ID with URL fallback.
// An optional content-frame id is preferred when present (always-wrap model).
// Returns empty string if no session found.
func (pt *PageTracker) ResolveSession(browserSessionID, url string, frameID ...string) string {
	fid := firstFrameID(frameID)
	return ptQuery(pt, func() string {
		return pt.resolveSession(browserSessionID, url, fid)
	})
}

// resolveSession is the actor-internal form of ResolveSession. Resolution order:
// content frame id (when known) → browser session id → URL.
func (pt *PageTracker) resolveSession(browserSessionID, url string, frameID ...string) string {
	if fid := firstFrameID(frameID); fid != "" {
		if sessionID := pt.frameToSession[fid]; sessionID != "" {
			return sessionID
		}
	}
	if sessionID := pt.findSessionByBrowserSession(browserSessionID); sessionID != "" {
		return sessionID
	}
	return pt.findSessionByURL(url)
}

// updateSessionWithBrowserID updates the session's browser mapping if not set
// and updates the last activity timestamp.
func (pt *PageTracker) updateSessionWithBrowserID(sessionID string, session *PageSession, browserSessionID string) {
	if session.BrowserSession == "" && browserSessionID != "" {
		session.BrowserSession = browserSessionID
		pt.browserSessionToPage[browserSessionID] = sessionID
	}
	session.LastActivity = time.Now()
}

// TrackHTTPRequest processes an HTTP request and associates it with a page session.
func (pt *PageTracker) TrackHTTPRequest(entry HTTPLogEntry) {
	// Extract browser session ID from cookie
	browserSessionID := extractBrowserSessionID(entry.RequestHeaders)

	// Determine if this is a document (HTML) request
	isDocument := isDocumentRequest(entry)

	pt.send(func() {
		if isDocument {
			// Create or update page session for this browser tab
			pt.createOrUpdatePageSession(entry, browserSessionID)
		} else {
			// Associate resource with existing page session
			pt.addResourceToSession(entry)
		}
	})
}

// trackOnSession resolves the session for (browserSessionID, url) inside the
// actor and applies mutate to it. Shared shape of the four Track* methods.
func (pt *PageTracker) trackOnSession(browserSessionID, url string, mutate func(*PageSession), frameID ...string) {
	fid := firstFrameID(frameID)
	pt.send(func() {
		sessionID := pt.resolveSession(browserSessionID, url, fid)
		if sessionID == "" {
			return
		}
		session, ok := pt.sessions[sessionID]
		if !ok {
			return
		}
		// Telemetry is emitted from the inner content frame and is the only
		// reliable signal for client-side SPA navigations (which have no new
		// document request). Promote its marker-free URL to the session so
		// currentpage describes the page the developer is actually viewing.
		if url != "" {
			cleanURL := stripFrameMarker(url)
			if cleanURL != session.URL {
				delete(pt.urlToSession, normalizeURL(session.URL))
				session.URL = cleanURL
				pt.urlToSession[normalizeURL(cleanURL)] = sessionID
			}
		}
		if fid != "" {
			session.FrameID = fid
			pt.frameToSession[fid] = sessionID
		}
		mutate(session)
		pt.updateSessionWithBrowserID(sessionID, session, browserSessionID)
	})
}

// TrackError associates a frontend error with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackError(err FrontendError, browserSessionID string, frameID ...string) {
	pt.trackOnSession(browserSessionID, err.URL, func(session *PageSession) {
		session.Errors = append(session.Errors, err)
	}, frameID...)
}

// TrackPerformance associates performance metrics with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackPerformance(perf PerformanceMetric, browserSessionID string, frameID ...string) {
	pt.trackOnSession(browserSessionID, perf.URL, func(session *PageSession) {
		session.Performance = &perf
		// The browser sends document.title on the performance sample; promote it
		// to the session so list/get/summary all surface the page title.
		if perf.PageTitle != "" {
			session.PageTitle = perf.PageTitle
		}
	}, frameID...)
}

// TrackInteraction associates a user interaction event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackInteraction(interaction InteractionEvent, browserSessionID string, frameID ...string) {
	pt.trackOnSession(browserSessionID, interaction.URL, func(session *PageSession) {
		session.InteractionCount++
		session.Interactions = appendBounded(session.Interactions, interaction, MaxInteractionsPerSession)
	}, frameID...)
}

// TrackMutation associates a DOM mutation event with a page session.
// browserSessionID is the unique ID from the browser tab's sessionStorage.
func (pt *PageTracker) TrackMutation(mutation MutationEvent, browserSessionID string, frameID ...string) {
	pt.trackOnSession(browserSessionID, mutation.URL, func(session *PageSession) {
		session.MutationCount++
		session.Mutations = appendBounded(session.Mutations, mutation, MaxMutationsPerSession)
	}, frameID...)
}

// activeSessions returns live *PageSession pointers for sessions within the
// timeout, updating their Active flag. Actor-internal; the pointers must not
// leave the owner goroutine — hand out copies.
func (pt *PageTracker) activeSessions() []*PageSession {
	var sessions []*PageSession
	now := time.Now()

	for _, session := range pt.sessions {
		// Check if session is still active (within timeout)
		if now.Sub(session.LastActivity) < pt.sessionTimeout {
			session.Active = true
			sessions = append(sessions, session)
		} else {
			session.Active = false
		}
	}

	return sessions
}

// GetActiveSessions returns snapshot copies of all currently active page
// sessions, safe to read concurrently with tracking.
func (pt *PageTracker) GetActiveSessions() []*PageSession {
	return ptQuery(pt, func() []*PageSession {
		live := pt.activeSessions()
		out := make([]*PageSession, len(live))
		for i, s := range live {
			out[i] = copyPageSession(s)
		}
		return out
	})
}

// PageSessionSummary is a lightweight representation of a page session for list views.
// It omits detailed arrays (interactions, mutations, errors, resources) to reduce token usage.
type PageSessionSummary struct {
	ID             string    `json:"id"`
	URL            string    `json:"url"`
	FrameID        string    `json:"frame_id,omitempty"`
	PageTitle      string    `json:"page_title,omitempty"`
	StartTime      time.Time `json:"start_time"`
	LastActivity   time.Time `json:"last_activity"`
	Active         bool      `json:"active"`
	ResourceCount  int       `json:"resource_count"`
	ErrorCount     int       `json:"error_count"`
	HasPerformance bool      `json:"has_performance"`
	LoadTimeMs     int64     `json:"load_time_ms,omitempty"`
	// Counts only, no detailed arrays
	InteractionCount int `json:"interaction_count"`
	MutationCount    int `json:"mutation_count"`
}

// GetActiveSessionSummaries returns lightweight summaries of active sessions.
// Use this for list views to avoid sending massive arrays of interactions/mutations.
func (pt *PageTracker) GetActiveSessionSummaries() []PageSessionSummary {
	return ptQuery(pt, func() []PageSessionSummary {
		sessions := pt.activeSessions()
		summaries := make([]PageSessionSummary, len(sessions))

		for i, session := range sessions {
			summaries[i] = PageSessionSummary{
				ID:               session.ID,
				URL:              session.URL,
				FrameID:          session.FrameID,
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
				summaries[i].LoadTimeMs = session.Performance.LoadEventEnd
			}
		}

		return summaries
	})
}

// GetSession returns a snapshot of a specific page session by ID.
func (pt *PageTracker) GetSession(sessionID string) (*PageSession, bool) {
	session := ptQuery(pt, func() *PageSession {
		if s, ok := pt.sessions[sessionID]; ok {
			return copyPageSession(s)
		}
		return nil
	})
	return session, session != nil
}

// Clear removes all page sessions.
func (pt *PageTracker) Clear() {
	pt.send(func() {
		pt.sessions = make(map[string]*PageSession)
		pt.urlToSession = make(map[string]string)
		pt.browserSessionToPage = make(map[string]string)
		pt.frameToSession = make(map[string]string)
		pt.sessionSeq = 0
	})
}

// createOrUpdatePageSession creates a new page session or updates an existing one for the same browser tab.
func (pt *PageTracker) createOrUpdatePageSession(entry HTTPLogEntry, browserSessionID string) {
	now := time.Now()

	// Always-wrap model: the shell's top-level request and the content frame's
	// marked request are the same page. Store the marker-stripped URL and key
	// urlToSession on the normalized (marker-free) URL so the two coalesce into
	// one session; record the content frame id when present.
	cleanURL := stripFrameMarker(entry.URL)
	frameID := frameIDFromURL(entry.URL)
	normURL := normalizeURL(entry.URL)

	updateExisting := func(sessionID string, session *PageSession) {
		session.URL = cleanURL
		session.LastActivity = now
		session.DocumentRequest = &entry
		session.Navigations = append(session.Navigations, entry)
		session.Resources = make([]HTTPLogEntry, 0)
		pt.urlToSession[normURL] = sessionID
		if frameID != "" {
			session.FrameID = frameID
			pt.frameToSession[frameID] = sessionID
		}
	}

	// If we have a browser session ID, try to find existing session for this tab
	if browserSessionID != "" {
		existingSessionID := pt.findSessionByBrowserSession(browserSessionID)
		if existingSessionID != "" {
			if session, ok := pt.sessions[existingSessionID]; ok {
				updateExisting(existingSessionID, session)
				return
			}
		}
	}

	// Coalesce shell + content-frame document requests for the same page: if a
	// session already exists for this normalized URL, update it rather than
	// creating a duplicate.
	if existingSessionID := pt.urlToSession[normURL]; existingSessionID != "" {
		if session, ok := pt.sessions[existingSessionID]; ok {
			updateExisting(existingSessionID, session)
			return
		}
	}

	// Create new session
	sessionID := pt.generateSessionID()
	session := &PageSession{
		ID:              sessionID,
		URL:             cleanURL,
		FrameID:         frameID,
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

	pt.sessions[sessionID] = session
	pt.urlToSession[normURL] = sessionID
	if frameID != "" {
		pt.frameToSession[frameID] = sessionID
	}

	// Register browser session mapping
	if browserSessionID != "" {
		pt.browserSessionToPage[browserSessionID] = sessionID
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

	session, ok := pt.sessions[sessionID]
	if !ok {
		return
	}

	session.Resources = append(session.Resources, entry)
	session.LastActivity = time.Now()
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
	return pt.browserSessionToPage[browserSessionID]
}

// findSessionByURL finds a session ID for a given URL.
func (pt *PageTracker) findSessionByURL(urlStr string) string {
	return pt.urlToSession[normalizeURL(urlStr)]
}

// findMostRecentSession finds the most recent active session with matching origin.
func (pt *PageTracker) findMostRecentSession(urlStr string) string {
	targetOrigin := getOrigin(urlStr)
	if targetOrigin == "" {
		return ""
	}

	var mostRecent *PageSession
	var mostRecentID string

	for id, session := range pt.sessions {
		if getOrigin(session.URL) == targetOrigin && session.Active {
			if mostRecent == nil || session.LastActivity.After(mostRecent.LastActivity) {
				mostRecent = session
				mostRecentID = id
			}
		}
	}

	return mostRecentID
}

// cleanupOldSessions removes sessions that exceed the max count.
func (pt *PageTracker) cleanupOldSessions() {
	if len(pt.sessions) <= pt.maxSessions {
		return
	}

	// Find and remove oldest sessions
	type sessionWithTime struct {
		id   string
		time time.Time
	}

	allSessions := make([]sessionWithTime, 0, len(pt.sessions))
	for id, session := range pt.sessions {
		allSessions = append(allSessions, sessionWithTime{id: id, time: session.StartTime})
	}

	// Sort by start time and remove oldest
	toRemove := len(pt.sessions) - pt.maxSessions
	for i := 0; i < toRemove && i < len(allSessions); i++ {
		// Find oldest
		oldest := i
		for j := i + 1; j < len(allSessions); j++ {
			if allSessions[j].time.Before(allSessions[oldest].time) {
				oldest = j
			}
		}
		allSessions[i], allSessions[oldest] = allSessions[oldest], allSessions[i]

		victim := allSessions[i].id
		if session, ok := pt.sessions[victim]; ok {
			delete(pt.urlToSession, normalizeURL(session.URL))
			if session.BrowserSession != "" {
				delete(pt.browserSessionToPage, session.BrowserSession)
			}
		}
		// Drop any frame mappings pointing at the evicted session.
		for fid, sid := range pt.frameToSession {
			if sid == victim {
				delete(pt.frameToSession, fid)
			}
		}
		delete(pt.sessions, victim)
	}
}

// generateSessionID generates a unique session ID.
func (pt *PageTracker) generateSessionID() string {
	pt.sessionSeq++
	return "page-" + itoa(int(pt.sessionSeq))
}

// Helper functions

// isDocumentRequest determines if an HTTP request is for a document (HTML).
func isDocumentRequest(entry HTTPLogEntry) bool {
	contentType := entry.ResponseHeaders["Content-Type"]
	if contentType == "" {
		contentType = entry.ResponseHeaders["content-type"]
	}

	// Explicit HTML response - a document, UNLESS the request path ends in a
	// non-document resource extension. SPA dev servers with an index.html
	// fallback answer unknown paths (favicon.ico, deep-linked assets) with the
	// HTML shell; that mis-served HTML is a resource fetch, not a navigation, so
	// it must not spawn its own page session. A path-suffix check (not the
	// Contains-based hasResourceExtension) avoids query-string false positives.
	if strings.Contains(contentType, "text/html") {
		return !pathHasResourceExtension(entry.URL)
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

// resourceExtensions are file suffixes that mark a request as a static
// resource rather than an HTML document navigation. Note: ".html" is
// deliberately absent — an .html path IS a document.
var resourceExtensions = []string{
	".js", ".css", ".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico",
	".woff", ".woff2", ".ttf", ".eot", ".json", ".xml", ".txt",
	".webp", ".mp4", ".webm", ".mp3", ".wav",
}

// pathHasResourceExtension reports whether the URL's PATH (query/fragment
// ignored) ends in a known non-document resource extension. Unlike
// hasResourceExtension (a substring scan over the whole URL, kept for the
// fallback heuristic and its existing tests), this is an exact path-suffix
// match, so "/page?ref=style.css" is not misread as a resource.
func pathHasResourceExtension(urlStr string) bool {
	parsed, err := url.Parse(urlStr)
	path := urlStr
	if err == nil && parsed.Path != "" {
		path = parsed.Path
	}
	path = strings.ToLower(path)
	for _, ext := range resourceExtensions {
		if strings.HasSuffix(path, ext) {
			return true
		}
	}
	return false
}

// hasResourceExtension checks if a URL contains a resource file extension
// anywhere (substring scan). Kept as the lenient fallback heuristic for the
// GET-without-extension document check; pathHasResourceExtension is the strict
// path-suffix variant used by the text/html gate.
func hasResourceExtension(urlStr string) bool {
	urlLower := strings.ToLower(urlStr)
	for _, ext := range resourceExtensions {
		if strings.Contains(urlLower, ext) {
			return true
		}
	}
	return false
}

// normalizeURL normalizes a URL for comparison.
// stripFrameMarkerQuery removes the content-frame marker (frameMarkerParam,
// defined in injector.go) from a raw query string, preserving order of the rest.
func stripFrameMarkerQuery(rawQuery string) string {
	if rawQuery == "" || !strings.Contains(rawQuery, frameMarkerParam) {
		return rawQuery
	}
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return rawQuery
	}
	if _, ok := values[frameMarkerParam]; !ok {
		return rawQuery
	}
	delete(values, frameMarkerParam)
	return values.Encode()
}

// frameIDFromURL extracts the content-frame id from a URL's marker, or "".
func frameIDFromURL(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return ""
	}
	return parsed.Query().Get(frameMarkerParam)
}

// stripFrameMarker returns urlStr with the content-frame marker removed.
func stripFrameMarker(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}
	parsed.RawQuery = stripFrameMarkerQuery(parsed.RawQuery)
	return parsed.String()
}

func normalizeURL(urlStr string) string {
	parsed, err := url.Parse(urlStr)
	if err != nil {
		return urlStr
	}

	// Drop the internal content-frame marker so the shell's top-level request
	// and the content frame's marked request map to the same page session, and
	// content-frame telemetry resolves to the real page (always-wrap model —
	// docs/responsive-canonical-target.md §6.3).
	parsed.RawQuery = stripFrameMarkerQuery(parsed.RawQuery)

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
