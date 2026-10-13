package proxy

import (
	"testing"
	"time"
)

func newTestPageTracker(t *testing.T) *PageTracker {
	t.Helper()
	pt := NewPageTracker(50, time.Minute)
	t.Cleanup(pt.Stop)
	return pt
}

func docEntry(url string) HTTPLogEntry {
	return HTTPLogEntry{
		Method:          "GET",
		URL:             url,
		StatusCode:      200,
		ResponseHeaders: map[string]string{"Content-Type": "text/html"},
		Timestamp:       time.Now(),
	}
}

// TestPageTracker_StripsFrameMarker: the shell's top-level request and the
// content frame's marked request for the same page coalesce into ONE session
// whose URL is the clean (marker-free) page URL.
func TestPageTracker_StripsFrameMarker(t *testing.T) {
	pt := newTestPageTracker(t)

	pt.TrackHTTPRequest(docEntry("http://x/dashboard"))                             // shell top-level
	pt.TrackHTTPRequest(docEntry("http://x/dashboard?" + frameMarkerParam + "=f1")) // content frame

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("shell + content document requests must coalesce into 1 session, got %d", len(sessions))
	}
	if got := sessions[0].URL; got != "http://x/dashboard" {
		t.Errorf("session URL must be marker-free, got %q", got)
	}
}

// TestPageTracker_ResolveByFrameID: a content-frame document request registers a
// frame->session mapping; telemetry carrying that frame id resolves to the
// session even with no browser session id and a marker-bearing URL.
func TestPageTracker_ResolveByFrameID(t *testing.T) {
	pt := newTestPageTracker(t)
	pt.TrackHTTPRequest(docEntry("http://x/app?" + frameMarkerParam + "=fXYZ"))

	// Error from the content frame: URL carries the marker, frame id supplied.
	pt.TrackError(
		FrontendError{Message: "boom", URL: "http://x/app?" + frameMarkerParam + "=fXYZ", Timestamp: time.Now()},
		"",     // no browser session id
		"fXYZ", // frame id
	)

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if len(sessions[0].Errors) != 1 {
		t.Errorf("error must attach to the frame's session, got %d errors", len(sessions[0].Errors))
	}
	if sessions[0].URL != "http://x/app" {
		t.Errorf("session URL must be marker-free, got %q", sessions[0].URL)
	}
}

func TestPageTracker_TelemetryPromotesSPANavigationURL(t *testing.T) {
	pt := newTestPageTracker(t)
	pt.TrackHTTPRequest(docEntry("http://x/" + "?" + frameMarkerParam + "=f1"))

	pt.TrackInteraction(InteractionEvent{
		URL:       "http://x/pages/atomic-orbitals?" + frameMarkerParam + "=f1",
		Timestamp: time.Now(),
	}, "", "f1")

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if got := sessions[0].URL; got != "http://x/pages/atomic-orbitals" {
		t.Errorf("SPA telemetry URL was not promoted: got %q", got)
	}
	if got := sessions[0].FrameID; got != "f1" {
		t.Errorf("content execution frame not retained: got %q", got)
	}
}

func TestStripFrameMarker(t *testing.T) {
	cases := map[string]string{
		"http://x/p?" + frameMarkerParam + "=f1":     "http://x/p",
		"http://x/p?a=1&" + frameMarkerParam + "=f1": "http://x/p?a=1",
		"http://x/p?" + frameMarkerParam + "=f1&b=2": "http://x/p?b=2",
		"http://x/p?a=1": "http://x/p?a=1",
		"http://x/p":     "http://x/p",
	}
	for in, want := range cases {
		if got := stripFrameMarker(in); got != want {
			t.Errorf("stripFrameMarker(%q) = %q, want %q", in, got, want)
		}
	}
	if got := frameIDFromURL("http://x/p?" + frameMarkerParam + "=fZ"); got != "fZ" {
		t.Errorf("frameIDFromURL = %q, want fZ", got)
	}
}

// TestPageTracker_TelemetryKeepsOriginFormSessionURL pins the production
// shape the promotion above has to live with: the proxy creates a session from
// the document request, whose URL is origin-form ("/"), and the browser then
// reports telemetry with location.href, which is absolute. Telemetry for the
// SAME page must leave the session URL alone; it used to rewrite "/" to
// "http://host/" on the first event of every page (and re-key urlToSession to
// a key no later document request produces). The real-Chrome proof is
// TestE2E_CurrentPage_RealBrowserHappyPath (chromee2e tier).
func TestPageTracker_TelemetryKeepsOriginFormSessionURL(t *testing.T) {
	pt := newTestPageTracker(t)
	pt.TrackHTTPRequest(docEntry("/?" + frameMarkerParam + "=f1"))

	pt.TrackInteraction(InteractionEvent{
		URL:       "http://127.0.0.1:12345/?" + frameMarkerParam + "=f1",
		Timestamp: time.Now(),
	}, "", "f1")

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if got := sessions[0].URL; got != "/" {
		t.Errorf("same-page telemetry changed the session URL form: got %q, want %q", got, "/")
	}
	if sessions[0].InteractionCount != 1 {
		t.Errorf("interaction not recorded on the session")
	}

	// A later document request for the same page still coalesces into this
	// session: the URL index was not moved to an absolute key.
	pt.TrackHTTPRequest(docEntry("/"))
	if n := len(pt.GetActiveSessions()); n != 1 {
		t.Errorf("document request after telemetry created a duplicate session: %d sessions", n)
	}
}

// TestPageTracker_TelemetryPromotesSPANavigationInOriginForm: a client-side
// navigation is still promoted, in the session's own (origin) form, keeping
// query and hash route and dropping the frame marker.
func TestPageTracker_TelemetryPromotesSPANavigationInOriginForm(t *testing.T) {
	pt := newTestPageTracker(t)
	pt.TrackHTTPRequest(docEntry("/?" + frameMarkerParam + "=f1"))

	pt.TrackInteraction(InteractionEvent{
		URL:       "http://127.0.0.1:12345/pages/orbitals?tab=2&" + frameMarkerParam + "=f1#section",
		Timestamp: time.Now(),
	}, "", "f1")

	sessions := pt.GetActiveSessions()
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if got := sessions[0].URL; got != "/pages/orbitals?tab=2#section" {
		t.Errorf("SPA navigation promoted as %q, want %q", got, "/pages/orbitals?tab=2#section")
	}
}

func TestInSessionURLForm(t *testing.T) {
	for _, tc := range []struct{ session, telemetry, want string }{
		{"/", "http://h:1/", "/"},
		{"/a", "http://h:1/b?x=1", "/b?x=1"},
		{"/a", "https://h/b#frag", "/b#frag"},
		{"/a", "/b", "/b"},                                    // already origin-form
		{"http://x/", "http://x/pages/y", "http://x/pages/y"}, // absolute session keeps absolute
		{"", "http://h/b", "/b"},                              // empty session URL is origin-form
	} {
		if got := inSessionURLForm(tc.session, tc.telemetry); got != tc.want {
			t.Errorf("inSessionURLForm(%q, %q) = %q, want %q", tc.session, tc.telemetry, got, tc.want)
		}
	}
}
