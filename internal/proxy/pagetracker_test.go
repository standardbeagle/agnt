package proxy

import (
	"testing"
	"time"
)

func TestIsDocumentRequest(t *testing.T) {
	tests := []struct {
		name     string
		entry    HTTPLogEntry
		expected bool
	}{
		{
			name: "HTML content type",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html; charset=utf-8",
				},
			},
			expected: true,
		},
		{
			name: "HTML content type lowercase key",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/",
				ResponseHeaders: map[string]string{
					"content-type": "text/html",
				},
			},
			expected: true,
		},
		{
			name: "URL ending in .html",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/page.html",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/octet-stream",
				},
			},
			expected: true,
		},
		{
			name: "JavaScript file",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/script.js",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/javascript",
				},
			},
			expected: false,
		},
		{
			name: "CSS file",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/style.css",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/css",
				},
			},
			expected: false,
		},
		{
			name: "Image PNG",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/image.png",
				ResponseHeaders: map[string]string{
					"Content-Type": "image/png",
				},
			},
			expected: false,
		},
		{
			name: "JSON API response",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/api/users",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // Fixed: API endpoints should NOT be documents
		},
		{
			name: "Root path with no content type",
			entry: HTTPLogEntry{
				Method:          "GET",
				URL:             "/",
				ResponseHeaders: map[string]string{},
			},
			expected: true,
		},
		{
			name: "POST request to API",
			entry: HTTPLogEntry{
				Method: "POST",
				URL:    "/api/login",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // POST is not a GET, so should be false
		},
		{
			name: "API path /api/meals/favorites",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/api/meals/favorites",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // API path should not be a document
		},
		{
			name: "API path with query params",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/api/meals/recent?limit=10",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // API path should not be a document
		},
		{
			name: "API path /v1/users",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/v1/users",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // Versioned API path
		},
		{
			name: "GraphQL endpoint",
			entry: HTTPLogEntry{
				Method: "POST",
				URL:    "/graphql",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // GraphQL endpoint
		},
		{
			name: "XHR request header",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/data",
				RequestHeaders: map[string]string{
					"X-Requested-With": "XMLHttpRequest",
				},
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // XHR requests are not documents
		},
		{
			name: "Accept header prefers JSON",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/data",
				RequestHeaders: map[string]string{
					"Accept": "application/json",
				},
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // Accept JSON means not a document
		},
		{
			name: "Accept header includes both HTML and JSON",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/page",
				RequestHeaders: map[string]string{
					"Accept": "text/html, application/json",
				},
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html",
				},
			},
			expected: true, // HTML response with HTML in Accept
		},
		{
			name: "text/json content type",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/data",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/json",
				},
			},
			expected: false, // JSON is not a document
		},
		{
			name: "REST API path",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/rest/v2/items",
				ResponseHeaders: map[string]string{
					"Content-Type": "application/json",
				},
			},
			expected: false, // REST API path
		},
		{
			name: "HTML body served for favicon (SPA fallback) is a resource",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/favicon.ico",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html; charset=utf-8",
				},
			},
			expected: false, // index.html fallback for a .ico path is not a navigation
		},
		{
			name: "HTML body served for a .png asset is a resource",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/assets/logo.png?v=2",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html",
				},
			},
			expected: false, // resource-extension path wins over text/html
		},
		{
			name: "HTML body on an extensionless SPA route is a document",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/users/123",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html",
				},
			},
			expected: true, // genuine client-side route navigation
		},
		{
			name: "HTML page with a resource-looking query param is a document",
			entry: HTTPLogEntry{
				Method: "GET",
				URL:    "/dashboard?ref=style.css",
				ResponseHeaders: map[string]string{
					"Content-Type": "text/html",
				},
			},
			expected: true, // path-suffix check ignores the query string
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := isDocumentRequest(tt.entry)
			if result != tt.expected {
				t.Errorf("isDocumentRequest() = %v, want %v", result, tt.expected)
			}
		})
	}
}

func TestPageTracker_TrackHTTPRequest_DocumentCreatesSession(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)

	// Track an HTML document request
	entry := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}

	pt.TrackHTTPRequest(entry)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(sessions))
	}

	if sessions[0].URL != entry.URL {
		t.Errorf("Expected URL %s, got %s", entry.URL, sessions[0].URL)
	}
}

func TestPageTracker_TrackPerformance_PromotesTitleAndDimensions(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)

	// A session is created by the document request; perf samples then mutate it.
	pt.TrackHTTPRequest(HTTPLogEntry{
		ID:              "req-1",
		Timestamp:       time.Now(),
		Method:          "GET",
		URL:             "http://localhost:8080/",
		ResponseHeaders: map[string]string{"Content-Type": "text/html"},
		StatusCode:      200,
	})

	perf := PerformanceMetric{
		ID:             "perf-1",
		URL:            "http://localhost:8080/",
		PageTitle:      "Dashboard",
		PageWidth:      1280,
		PageHeight:     3000,
		ViewportWidth:  1024,
		ViewportHeight: 768,
	}
	pt.TrackPerformance(perf, "")

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session, got %d", len(sessions))
	}
	if sessions[0].PageTitle != "Dashboard" {
		t.Errorf("PageTitle not promoted to session: got %q", sessions[0].PageTitle)
	}
	if sessions[0].Performance == nil {
		t.Fatal("Performance not stored on session")
	}
	if sessions[0].Performance.PageWidth != 1280 || sessions[0].Performance.PageHeight != 3000 {
		t.Errorf("page dimensions wrong: %dx%d", sessions[0].Performance.PageWidth, sessions[0].Performance.PageHeight)
	}
	if sessions[0].Performance.ViewportWidth != 1024 || sessions[0].Performance.ViewportHeight != 768 {
		t.Errorf("viewport dimensions wrong: %dx%d", sessions[0].Performance.ViewportWidth, sessions[0].Performance.ViewportHeight)
	}
}

func TestPageTracker_TrackHTTPRequest_ResourceAddedToSession(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)

	// Track an HTML document request first
	docEntry := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/page",
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(docEntry)

	// Track a resource request with Referer header
	resourceEntry := HTTPLogEntry{
		ID:        "req-2",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/script.js",
		RequestHeaders: map[string]string{
			"Referer": "http://localhost:8080/page",
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "application/javascript",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(resourceEntry)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Errorf("Expected 1 session, got %d", len(sessions))
	}

	if len(sessions[0].Resources) != 1 {
		t.Errorf("Expected 1 resource, got %d", len(sessions[0].Resources))
	}
}

func TestPageTracker_SessionTimeout(t *testing.T) {
	// Create tracker with very short timeout
	pt := NewPageTracker(100, 1*time.Millisecond)

	entry := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry)

	// After the 1ms timeout elapses, the session must no longer count as active:
	// activeSessions() flips Active=false AND drops timed-out sessions from the
	// returned slice, so the post-timeout state is "gone, or present-and-inactive".
	// The flip is recomputed on each GetActiveSessions read against wall-clock now,
	// so a single fixed sleep can race the actor/scheduler under load (this test
	// flaked exactly here — the nominal 5ms margin is not a guarantee on an
	// oversubscribed host). Poll for the invariant with generous headroom instead
	// of asserting on one nominal instant. See
	// .claude/rules/testing-timing-assertion-flakes.md.
	deadline := time.Now().Add(2 * time.Second)
	for {
		sessions := pt.GetActiveSessions()
		if len(sessions) == 0 {
			return // timed-out session dropped from the active set — correct
		}
		if len(sessions) == 1 && !sessions[0].Active {
			return // present but marked inactive — also correct
		}
		if len(sessions) != 1 {
			t.Fatalf("Expected 0 or 1 session, got %d", len(sessions))
		}
		if time.Now().After(deadline) {
			t.Fatalf("session still active %v after a 1ms timeout", 2*time.Second)
		}
		time.Sleep(1 * time.Millisecond)
	}
}

func TestPageTracker_Clear(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)

	entry := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session before clear, got %d", len(sessions))
	}

	pt.Clear()

	sessions = pt.GetActiveSessions()
	if len(sessions) != 0 {
		t.Errorf("Expected 0 sessions after clear, got %d", len(sessions))
	}
}

func TestExtractBrowserSessionID(t *testing.T) {
	tests := []struct {
		name     string
		headers  map[string]string
		expected string
	}{
		{
			name:     "No cookie header",
			headers:  map[string]string{},
			expected: "",
		},
		{
			name: "Cookie header with session ID",
			headers: map[string]string{
				"Cookie": "__devtool_sid=sess-abc123",
			},
			expected: "sess-abc123",
		},
		{
			name: "Cookie header lowercase",
			headers: map[string]string{
				"cookie": "__devtool_sid=sess-xyz789",
			},
			expected: "sess-xyz789",
		},
		{
			name: "Multiple cookies",
			headers: map[string]string{
				"Cookie": "other=value; __devtool_sid=sess-multi; another=thing",
			},
			expected: "sess-multi",
		},
		{
			name: "Session ID at start",
			headers: map[string]string{
				"Cookie": "__devtool_sid=sess-first; other=value",
			},
			expected: "sess-first",
		},
		{
			name: "Session ID at end",
			headers: map[string]string{
				"Cookie": "other=value; __devtool_sid=sess-last",
			},
			expected: "sess-last",
		},
		{
			name: "No session ID cookie",
			headers: map[string]string{
				"Cookie": "other=value; something=else",
			},
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractBrowserSessionID(tt.headers)
			if result != tt.expected {
				t.Errorf("extractBrowserSessionID() = %q, want %q", result, tt.expected)
			}
		})
	}
}

func TestPageTracker_BrowserSessionMerging(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)
	browserSessionID := "sess-test123"

	// First navigation to /
	entry1 := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		RequestHeaders: map[string]string{
			"Cookie": "__devtool_sid=" + browserSessionID,
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry1)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected 1 session after first navigation, got %d", len(sessions))
	}
	firstSessionID := sessions[0].ID

	// Second navigation to /login (same browser tab)
	entry2 := HTTPLogEntry{
		ID:        "req-2",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/login",
		RequestHeaders: map[string]string{
			"Cookie": "__devtool_sid=" + browserSessionID,
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry2)

	sessions = pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected still 1 session after second navigation, got %d", len(sessions))
	}
	if sessions[0].ID != firstSessionID {
		t.Errorf("Session ID changed: was %s, now %s", firstSessionID, sessions[0].ID)
	}
	if sessions[0].URL != "http://localhost:8080/login" {
		t.Errorf("Expected URL to be updated to /login, got %s", sessions[0].URL)
	}

	// Third navigation back to / (same browser tab)
	entry3 := HTTPLogEntry{
		ID:        "req-3",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		RequestHeaders: map[string]string{
			"Cookie": "__devtool_sid=" + browserSessionID,
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry3)

	sessions = pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("Expected still 1 session after third navigation, got %d", len(sessions))
	}
	if sessions[0].ID != firstSessionID {
		t.Errorf("Session ID changed: was %s, now %s", firstSessionID, sessions[0].ID)
	}
	if sessions[0].URL != "http://localhost:8080/" {
		t.Errorf("Expected URL to be updated back to /, got %s", sessions[0].URL)
	}

	// Verify navigation history
	if len(sessions[0].Navigations) != 3 {
		t.Errorf("Expected 3 navigations, got %d", len(sessions[0].Navigations))
	}
}

func TestPageTracker_DifferentBrowserSessions(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)

	// Tab 1 navigates to /
	entry1 := HTTPLogEntry{
		ID:        "req-1",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/",
		RequestHeaders: map[string]string{
			"Cookie": "__devtool_sid=sess-tab1",
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry1)

	// Tab 2 navigates to /login (different browser tab)
	entry2 := HTTPLogEntry{
		ID:        "req-2",
		Timestamp: time.Now(),
		Method:    "GET",
		URL:       "http://localhost:8080/login",
		RequestHeaders: map[string]string{
			"Cookie": "__devtool_sid=sess-tab2",
		},
		ResponseHeaders: map[string]string{
			"Content-Type": "text/html; charset=utf-8",
		},
		StatusCode: 200,
	}
	pt.TrackHTTPRequest(entry2)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 2 {
		t.Fatalf("Expected 2 sessions for 2 different tabs, got %d", len(sessions))
	}

	// Verify each session has correct browser session ID
	sessionsByBrowserID := make(map[string]*PageSession)
	for _, s := range sessions {
		sessionsByBrowserID[s.BrowserSession] = s
	}

	if tab1 := sessionsByBrowserID["sess-tab1"]; tab1 == nil {
		t.Error("Missing session for tab1")
	} else if tab1.URL != "http://localhost:8080/" {
		t.Errorf("Tab1 URL = %s, want /", tab1.URL)
	}

	if tab2 := sessionsByBrowserID["sess-tab2"]; tab2 == nil {
		t.Error("Missing session for tab2")
	} else if tab2.URL != "http://localhost:8080/login" {
		t.Errorf("Tab2 URL = %s, want /login", tab2.URL)
	}
}

func TestIsAPIPath(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"/api/users", true},
		{"/api/meals/favorites", true},
		{"/api/meals/recent?limit=10", true},
		{"/v1/users", true},
		{"/v2/products", true},
		{"/v3/orders/123", true},
		{"/graphql", true},
		{"/graphql/query", true},
		{"/rest/items", true},
		{"/_api/search", true},
		{"/ajax/load", true},
		{"/", false},
		{"/page", false},
		{"/login", false},
		{"/users/profile", false},
		{"/dashboard", false},
		{"/about", false},
		{"http://localhost/api/users", true},
		{"https://example.com/v1/items", true},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			result := isAPIPath(tt.url)
			if result != tt.expected {
				t.Errorf("isAPIPath(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}

func TestHasResourceExtension(t *testing.T) {
	tests := []struct {
		url      string
		expected bool
	}{
		{"/script.js", true},
		{"/style.css", true},
		{"/image.png", true},
		{"/image.jpg", true},
		{"/font.woff2", true},
		{"/data.json", true},
		{"/page.html", false}, // .html is NOT a resource extension
		{"/", false},
		{"/api/users", false},
		{"/page", false},
		{"/api/v1/users.json", true},    // Contains .json
		{"/path/to/resource.jsx", true}, // Contains .js substring
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			result := hasResourceExtension(tt.url)
			if result != tt.expected {
				t.Errorf("hasResourceExtension(%q) = %v, want %v", tt.url, result, tt.expected)
			}
		})
	}
}
