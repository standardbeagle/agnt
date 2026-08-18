package tools

import (
	"strings"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
)

// TestBuildGetIncidentsFilter_ThreadsSession pins that the tool's `session`
// input reaches the wire filter's SessionCode — the selector the daemon uses to
// pick which inbox to read.
func TestBuildGetIncidentsFilter_ThreadsSession(t *testing.T) {
	t.Parallel()
	f := buildGetIncidentsFilter(GetIncidentsInput{Session: "sess-a"})
	if f.SessionCode != "sess-a" {
		t.Fatalf("session input must reach filter.SessionCode, got %q", f.SessionCode)
	}
}

// TestFormatIncidents_ScopeAmbiguous_RendersCandidates pins that a scope
// disambiguation response renders the sessions to pick from (not "no incidents")
// so the agent can re-call with session:<code> in one more round trip.
func TestFormatIncidents_ScopeAmbiguous_RendersCandidates(t *testing.T) {
	t.Parallel()
	out := formatIncidentsCompact(GetIncidentsOutput{
		ScopeAmbiguous: true,
		ScopeCandidates: []protocol.SessionCandidate{
			{SessionCode: "sess-a", ProjectPath: "/tmp/a", Command: "claude"},
			{SessionCode: "sess-b", ProjectPath: "/tmp/b"},
		},
	})
	if !strings.Contains(out, "sess-a") || !strings.Contains(out, "sess-b") {
		t.Fatalf("must render both candidate session codes, got: %q", out)
	}
	if !strings.Contains(out, "session:") {
		t.Fatalf("must tell the agent how to pick (session:<code>), got: %q", out)
	}
	if strings.Contains(out, "no incidents") {
		t.Fatalf("disambiguation must not read as a clean empty inbox, got: %q", out)
	}
}
