package incident

import (
	"testing"
	"time"
)

func fpOf(src Source, msg string, ctx Context) string {
	return NewIncidentEvent(src, SeverityError, "cat", msg, ctx, nil).Fingerprint
}

// Timestamps a logger prepends must not reach the fingerprint: reISO8601 only
// recognised dashed dates, so Go's stdlib log prefix and bare clock prefixes
// made every occurrence (or every hour) a distinct incident.
func TestCanonicalize_StripsLoggerTimestamps(t *testing.T) {
	t.Parallel()
	pairs := []struct{ name, a, b string }{
		{"go stdlib log", "2024/01/15 10:30:00 dial failed", "2024/01/16 23:59:01 dial failed"},
		{"go stdlib log with shortfile", "2024/01/15 10:30:00 main.go:42: dial failed", "2025/03/02 01:02:03 main.go:42: dial failed"},
		{"go stdlib microseconds", "2024/01/15 10:30:00.123456 dial failed", "2024/01/15 10:30:09.000001 dial failed"},
		{"vite clock", "10:32:15 AM [vite] Internal server error: boom", "11:02:03 PM [vite] Internal server error: boom"},
		{"tsc bracket clock", "[10:32:15] Found 1 error.", "[9:01:59] Found 1 error."},
		{"clock before a source position", "10:32:15 [vite] error src/App.tsx:12:3", "23:00:01 [vite] error src/App.tsx:12:3"},
	}
	for _, tc := range pairs {
		if a, b := Canonicalize(tc.a), Canonicalize(tc.b); a != b {
			t.Errorf("%s: canonical forms differ:\n  %q\n  %q", tc.name, a, b)
		}
	}
	// A source position is not a clock: line:col must survive on a diagnostic.
	if a, b := Canonicalize("src/a.ts:10:20:30 oops"), Canonicalize("src/a.ts:11:21:31 oops"); a == b {
		t.Errorf("source positions were mistaken for a clock and collapsed: %q", a)
	}
}

// Only frame-shaped lines have their positions collapsed. Treating every line
// containing :<digits> as a frame merged distinct endpoints.
func TestCanonicalize_DistinctEndpointsStayDistinct(t *testing.T) {
	t.Parallel()
	distinct := [][2]string{
		{"Error: connect ECONNREFUSED 127.0.0.1:5432", "Error: connect ECONNREFUSED 127.0.0.1:6379"},
		{"listen tcp :3000: bind: address already in use", "listen tcp :8080: bind: address already in use"},
		{"GET http://localhost:3000/api failed", "GET http://localhost:4000/api failed"},
	}
	for _, p := range distinct {
		if Canonicalize(p[0]) == Canonicalize(p[1]) {
			t.Errorf("distinct endpoints collapsed: %q vs %q", p[0], p[1])
		}
	}

	// Real frames below the first app frame still collapse, in every dialect.
	same := [][2]string{
		{"TypeError: x\n    at render (src/App.tsx:12:3)\n    at commit (node_modules/react-dom/index.js:100:9)",
			"TypeError: x\n    at render (src/App.tsx:12:3)\n    at commit (node_modules/react-dom/index.js:245:17)"},
		{"TypeError: x\nrender@http://localhost:3000/src/App.tsx:12:3\ncommit@http://localhost:3000/node_modules/react-dom.js:100:9",
			"TypeError: x\nrender@http://localhost:3000/src/App.tsx:12:3\ncommit@http://localhost:3000/node_modules/react-dom.js:7:1"},
		{"panic: boom\nmain.serve()\n\t/app/main.go:42 +0x1f\nruntime.main()\n\t/usr/go/src/runtime/proc.go:250 +0x20",
			"panic: boom\nmain.serve()\n\t/app/main.go:42 +0x9a\nruntime.main()\n\t/usr/go/src/runtime/proc.go:271 +0x33"},
	}
	for _, p := range same {
		if a, b := Canonicalize(p[0]), Canonicalize(p[1]); a != b {
			t.Errorf("frames below the first app frame should collapse:\n  %q\n  %q", a, b)
		}
	}
}

// Two processes (or proxies) emitting the same line are two failures.
func TestFingerprint_ScopedByProcessAndProxy(t *testing.T) {
	t.Parallel()
	const line = "Error: listen EADDRINUSE"
	api := fpOf(SourceProcessAlert, line, Context{ProcessID: "api"})
	web := fpOf(SourceProcessAlert, line, Context{ProcessID: "web"})
	if api == web {
		t.Fatal("same line from two processes shares a fingerprint")
	}
	if api != fpOf(SourceProcessAlert, line, Context{ProcessID: "api"}) {
		t.Fatal("fingerprint is not stable for the same process")
	}
	if fpOf(SourceProxyDiag, "boom", Context{ProxyID: "dev"}) == fpOf(SourceProxyDiag, "boom", Context{ProxyID: "docs"}) {
		t.Fatal("same diagnostic from two proxies shares a fingerprint")
	}
	if FromProcessExit("api", "crash", 1, "").Fingerprint == FromProcessExit("web", "crash", 1, "").Fingerprint {
		t.Fatal("two processes crashing with the same code share a fingerprint")
	}
}

// With process-scoped fingerprints, retiring one process's errors leaves the
// other's identical line alone, and each stays attributable to its process.
func TestInbox_ClearProcessBefore_SameLineTwoProcesses(t *testing.T) {
	t.Parallel()
	inbox := NewInbox("s")
	for _, proc := range []string{"api", "web"} {
		ev := NewIncidentEvent(SourceBuildFail, SeverityError, "go", "build failed", Context{ProcessID: proc}, nil)
		inbox.Ingest(&InboxEntry{Fingerprint: ev.Fingerprint, FirstSeenAt: ev.ReceivedAt, LastSeenAt: ev.ReceivedAt, Count: 1, Sample: &ev, Severity: ev.Severity})
	}
	if entries, _ := inbox.Query(QueryFilter{}); len(entries) != 2 {
		t.Fatalf("want one entry per process, got %d", len(entries))
	}
	if n := inbox.ClearProcessBefore("api", time.Now().Add(time.Minute)); n != 1 {
		t.Fatalf("clearing api removed %d entries, want 1", n)
	}
	entries, _ := inbox.Query(QueryFilter{})
	if len(entries) != 1 || entries[0].Sample.Ctx.ProcessID != "web" {
		t.Fatalf("web's entry must survive api's clear, got %+v", entries)
	}
}
