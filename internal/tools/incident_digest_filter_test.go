package tools

import (
	"encoding/json"
	"strings"
	"testing"
)

// The digest sink must never subscribe globally: the daemon stamps each digest
// with its owning project, and a global subscription disables that filter, so
// project A's "you have incidents" ping reached project B's agent.
func TestIncidentDigestFilter_AlwaysScopedNeverGlobal(t *testing.T) {
	cases := []struct {
		name, session, project string
		wantSession, wantDir   string
	}{
		{"attached session wins", "claude-1", "/proj/a", "claude-1", ""},
		{"no session falls back to directory", "", "/proj/a", "", "/proj/a"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := IncidentDigestFilter(tc.session, tc.project)
			if f.Global {
				t.Fatal("digest filter must not set the legacy Global flag")
			}
			if global, explicit := f.GlobalSetting(); global || !explicit {
				t.Fatalf("global must be an explicit false (so scope.default-global cannot widen it); got global=%v explicit=%v", global, explicit)
			}
			if f.SessionCode != tc.wantSession || f.Directory != tc.wantDir {
				t.Fatalf("scope = (session %q, dir %q), want (%q, %q)", f.SessionCode, f.Directory, tc.wantSession, tc.wantDir)
			}
			wire, err := json.Marshal(f)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(wire), `"global":false`) {
				t.Fatalf("wire form must carry global:false, got %s", wire)
			}
		})
	}
}
