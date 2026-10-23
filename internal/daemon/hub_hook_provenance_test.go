//go:build unix

package daemon

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// A hook naming a registered session is filed under that session's project,
// whatever project_path it claims.
func TestHookProvenance_RegisteredSessionProjectWins(t *testing.T) {
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	ca := bindConn(t, sockPath, "sess-a", dirA)
	defer ca.Close()

	ev := HookEvent{SessionID: "sess-a", ProjectPath: dirB}
	d.bindHookProvenance(&ev, "")
	require.Equal(t, normalizePath(dirA), normalizePath(ev.ProjectPath))
}

// A bound connection speaks for its own session, not the one in the tags.
func TestHookProvenance_BoundConnectionOverridesTaggedSession(t *testing.T) {
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	ca := bindConn(t, sockPath, "sess-a", dirA)
	defer ca.Close()
	cb := bindConn(t, sockPath, "sess-b", dirB)
	defer cb.Close()

	ev := HookEvent{SessionID: "sess-b", ProjectPath: dirB}
	d.bindHookProvenance(&ev, "sess-a")
	require.Equal(t, "sess-a", ev.SessionID)
	require.Equal(t, normalizePath(dirA), normalizePath(ev.ProjectPath))
}

// Nothing contradicts a tag that names no registered session, so it stands.
func TestHookProvenance_UnregisteredSessionKeepsTags(t *testing.T) {
	d, _ := newSessionHostTestDaemon(t)
	ev := HookEvent{SessionID: "ghost", ProjectPath: "/tmp/proj"}
	d.bindHookProvenance(&ev, "")
	require.Equal(t, "ghost", ev.SessionID)
	require.Equal(t, "/tmp/proj", ev.ProjectPath)
}
