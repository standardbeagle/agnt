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
