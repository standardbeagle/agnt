package proxy

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func logN(tl *TrafficLogger, from, to int, body string) {
	for i := from; i < to; i++ {
		tl.LogHTTP(HTTPLogEntry{ID: fmt.Sprintf("e%d", i), Method: "GET", URL: "/x", ResponseBody: body})
	}
}

func ids(es []LogEntry) []string {
	out := make([]string, len(es))
	for i, e := range es {
		out[i] = e.HTTP.ID
	}
	return out
}

func TestTrafficLogger_TrimKeepsNewestInOrderAfterWrap(t *testing.T) {
	tl := NewTrafficLogger(10)
	logN(tl, 0, 25, strings.Repeat("x", 1000)) // wrapped: holds e15..e24

	dropped, freed := tl.Trim(4)
	if dropped != 6 || freed < 6000 {
		t.Fatalf("Trim(4) dropped=%d freed=%d, want 6 and >=6000", dropped, freed)
	}
	got := ids(tl.Query(LogFilter{}))
	want := []string{"e21", "e22", "e23", "e24"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("after trim got %v, want %v", got, want)
	}

	// The ring keeps working: fill past capacity and confirm order and cap.
	logN(tl, 25, 40, "")
	got = ids(tl.Query(LogFilter{}))
	if len(got) != 10 || got[0] != "e30" || got[9] != "e39" {
		t.Fatalf("after refill got %v", got)
	}
	if s := tl.Stats(); s.Dropped < 6 {
		t.Fatalf("trimmed entries must still count as dropped, stats=%+v", s)
	}
}

func TestTrafficLogger_TrimWithNothingToDropIsFreeWhenCompact(t *testing.T) {
	tl := NewTrafficLogger(1000)
	logN(tl, 0, 3, "")
	if d, f := tl.Trim(1000); d != 0 || f != 0 {
		t.Fatalf("compact ring reclaimed dropped=%d freed=%d", d, f)
	}
	if n := len(tl.Query(LogFilter{})); n != 3 {
		t.Fatalf("entries = %d, want 3", n)
	}
}

func TestPageTracker_ReclaimIdleEvictsOnlyIdleSessions(t *testing.T) {
	pt := NewPageTracker(100, 5*time.Minute)
	defer pt.Stop()

	pt.TrackHTTPRequest(HTTPLogEntry{ID: "a", Method: "GET", URL: "http://x/a", StatusCode: 200, ResponseBody: strings.Repeat("y", 5000), ResponseHeaders: map[string]string{"Content-Type": "text/html"}})
	pt.TrackHTTPRequest(HTTPLogEntry{ID: "b", Method: "GET", URL: "http://x/b", StatusCode: 200, ResponseHeaders: map[string]string{"Content-Type": "text/html"}})
	// Age session a.
	ptQuery(pt, func() int {
		for _, s := range pt.sessions {
			if s.URL == "http://x/a" {
				s.LastActivity = time.Now().Add(-time.Hour)
			}
		}
		return 0
	})

	freed := pt.ReclaimIdle(5 * time.Minute)
	if freed < 5000 {
		t.Fatalf("freed = %d, want the idle session's >=5000 body bytes", freed)
	}
	if n := len(pt.GetActiveSessions()); n != 1 {
		t.Fatalf("active sessions after reclaim = %d, want only the fresh one", n)
	}
	if pt.RetainedBytes() >= freed {
		t.Fatalf("retained %d should be below what was freed %d", pt.RetainedBytes(), freed)
	}
}
