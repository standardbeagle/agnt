package daemon

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveProjectScope exercises the session-scope chokepoint gate in
// isolation: the pure resolver that every non-debug list/query handler will
// route through (wired in C4/C5). It must resolve a session's project path,
// honor the global bypass, and reject an unresolvable non-global call
// fail-loud (mirroring INCIDENTS QUERY's "no session attached").
func TestResolveProjectScope(t *testing.T) {
	// No t.Parallel(): registers sessions on a shared daemon registry.
	d := NewForTest(t, defaultTestDaemonConfig(t))

	projA := normalizePath(t.TempDir())
	projB := normalizePath(t.TempDir())
	projDefaultGlobal := normalizePath(t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(projDefaultGlobal, ".agnt.kdl"), []byte("scope {\n  default-global true\n}\n"), 0o600))
	projMalformed := normalizePath(t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(projMalformed, ".agnt.kdl"), []byte("scope { invalid"), 0o600))
	require.NoError(t, d.sessionRegistry.Register(&Session{Code: "sess-a", ProjectPath: projA}))
	require.NoError(t, d.sessionRegistry.Register(&Session{Code: "sess-b", ProjectPath: projB}))

	cases := []struct {
		name        string
		filter      protocol.DirectoryFilter
		connSession string
		wantPath    string
		wantGlobal  bool
		wantErr     bool
	}{
		{
			name:       "global bypass returns no filter",
			filter:     protocol.DirectoryFilter{Global: true},
			wantGlobal: true,
		},
		{
			name:        "global wins even with a bound session",
			filter:      protocol.DirectoryFilter{Global: true},
			connSession: "sess-a",
			wantGlobal:  true,
		},
		{
			name:     "explicit session code resolves to its project",
			filter:   protocol.DirectoryFilter{SessionCode: "sess-b"},
			wantPath: projB,
		},
		{
			name:    "explicit unknown session code is rejected",
			filter:  protocol.DirectoryFilter{SessionCode: "ghost"},
			wantErr: true,
		},
		{
			name:     "explicit directory resolves to its normalized path",
			filter:   protocol.DirectoryFilter{Directory: projA},
			wantPath: projA,
		},
		{
			name:       "omitted global uses project default global",
			filter:     protocol.DirectoryFilter{Directory: projDefaultGlobal},
			wantGlobal: true,
		},
		{
			name:     "explicit false overrides project default global",
			filter:   protocol.DirectoryFilter{Directory: projDefaultGlobal, GlobalOverride: protocol.Bool(false)},
			wantPath: projDefaultGlobal,
		},
		{
			name:       "explicit true bypasses malformed project config",
			filter:     protocol.DirectoryFilter{Directory: projMalformed, GlobalOverride: protocol.Bool(true)},
			wantGlobal: true,
		},
		{
			name:     "explicit false bypasses malformed project config",
			filter:   protocol.DirectoryFilter{Directory: projMalformed, GlobalOverride: protocol.Bool(false)},
			wantPath: projMalformed,
		},
		{
			// The parse error is surfaced in the startup log instead
			// (TestResolveProjectScope_MalformedConfigIsVisibleNotFatal).
			name:     "omitted global falls back to project scope on malformed project config",
			filter:   protocol.DirectoryFilter{Directory: projMalformed},
			wantPath: projMalformed,
		},
		{
			name:        "connection's bound session resolves to its project",
			connSession: "sess-a",
			wantPath:    projA,
		},
		{
			name:    "no session, no global, no directory is rejected fail-loud",
			wantErr: true,
		},
		{
			name:        "connection session takes precedence over none but not over explicit",
			filter:      protocol.DirectoryFilter{SessionCode: "sess-b"},
			connSession: "sess-a",
			wantPath:    projB,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, global, err := d.resolveProjectScope(tc.filter, tc.connSession)
			if tc.wantErr {
				require.Error(t, err, "unresolvable non-global scope must fail loud")
				assert.Empty(t, path)
				assert.False(t, global)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantGlobal, global, "global flag mismatch")
			if tc.wantGlobal {
				assert.Empty(t, path, "global scope must carry no project filter")
			} else {
				assert.Equal(t, tc.wantPath, path, "resolved project path mismatch")
			}
		})
	}
}

// A broken .agnt.kdl made every scoped verb fail — STARTUP-LOG and `agnt
// monitor` included, the places an agent would look for the cause. The verb
// now resolves to the project (default-global false) and the parse error is a
// startup-log entry that query can see, recorded once per distinct error.
func TestResolveProjectScope_MalformedConfigIsVisibleNotFatal(t *testing.T) {
	d := NewForTest(t, defaultTestDaemonConfig(t))
	proj := normalizePath(t.TempDir())
	cfgPath := filepath.Join(proj, ".agnt.kdl")
	require.NoError(t, os.WriteFile(cfgPath, []byte("scope { invalid"), 0o600))
	require.NoError(t, d.sessionRegistry.Register(&Session{Code: "sess-broken", ProjectPath: proj}))

	configErrors := func() []*StartupLogEntry {
		var out []*StartupLogEntry
		for _, e := range d.startupErrorStore.Query(StartupLogFilter{ProjectPath: proj}) {
			if e.EventType == "config_error" {
				out = append(out, e)
			}
		}
		return out
	}

	for i := 0; i < 3; i++ {
		path, global, err := d.resolveProjectScope(protocol.DirectoryFilter{}, "sess-broken")
		require.NoError(t, err, "a config parse error must not fail the scoped verb")
		assert.False(t, global)
		assert.Equal(t, proj, path)
	}
	entries := configErrors()
	require.Len(t, entries, 1, "the parse error is recorded once, not on every query")
	assert.Equal(t, "warning", entries[0].Level)
	assert.Contains(t, entries[0].Message, "default-global falls back to false")

	// Fixed, then broken again: the new breakage is reported again.
	require.NoError(t, os.WriteFile(cfgPath, []byte("scope {\n  default-global false\n}\n"), 0o600))
	_, _, err := d.resolveProjectScope(protocol.DirectoryFilter{}, "sess-broken")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(cfgPath, []byte("scope { invalid"), 0o600))
	_, _, err = d.resolveProjectScope(protocol.DirectoryFilter{}, "sess-broken")
	require.NoError(t, err)
	assert.Len(t, configErrors(), 2)
}

func TestResolveStreamFilterScope(t *testing.T) {
	d := NewForTest(t, defaultTestDaemonConfig(t))
	project := normalizePath(t.TempDir())
	require.NoError(t, d.sessionRegistry.Register(&Session{Code: "stream-session", ProjectPath: project}))
	defaultGlobalProject := normalizePath(t.TempDir())
	require.NoError(t, os.WriteFile(filepath.Join(defaultGlobalProject, ".agnt.kdl"), []byte("scope {\n  default-global true\n}\n"), 0o600))
	require.NoError(t, d.sessionRegistry.Register(&Session{Code: "default-global-stream", ProjectPath: defaultGlobalProject}))

	sf, err := d.resolveStreamFilter(protocol.StreamEventFilter{}, "stream-session")
	require.NoError(t, err)
	assert.Equal(t, project, sf.projectPath)

	_, err = d.resolveStreamFilter(protocol.StreamEventFilter{}, "")
	assert.ErrorIs(t, err, errNoSessionScope)

	sf, err = d.resolveStreamFilter(protocol.StreamEventFilter{Global: true, ProjectPath: project}, "")
	require.NoError(t, err)
	assert.Empty(t, sf.projectPath)

	sf, err = d.resolveStreamFilter(protocol.StreamEventFilter{}, "default-global-stream")
	require.NoError(t, err)
	assert.Empty(t, sf.projectPath, "omitted global must honor the project's default-global setting")

	sf, err = d.resolveStreamFilter(protocol.StreamEventFilter{GlobalOverride: protocol.Bool(false)}, "default-global-stream")
	require.NoError(t, err)
	assert.Equal(t, defaultGlobalProject, sf.projectPath, "explicit false must override the project's default-global setting")
}
