package daemon

import (
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
