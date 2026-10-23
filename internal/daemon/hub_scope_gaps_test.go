//go:build unix

package daemon

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// PROXY START under an id another project's proxy holds used to answer
// "already running" with that project's listen address and start nothing.
func TestProxyStart_IDHeldByAnotherProjectIsRefused(t *testing.T) {
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	createProxy(t, d, "dev", dirB)

	a := bindConn(t, sockPath, "sess-a", dirA)
	err := a.Request("PROXY", "START", "dev", ephemeralTargetURL(t), "0").OK()
	require.Error(t, err)
	require.Contains(t, err.Error(), "another project")
}

// A bound session may not start a proxy under another project's path.
func TestProxyStart_PathOfAnotherProjectIsRefused(t *testing.T) {
	_, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	a := bindConn(t, sockPath, "sess-a", dirA)
	err := a.Request("PROXY", "START", "dev", ephemeralTargetURL(t), "0").
		WithJSON(map[string]string{"path": dirB}).OK()
	require.Error(t, err)
	require.Contains(t, err.Error(), "another project")
}

// A path-less PROXY START from a bound session belongs to that session's
// project, not to ".".
func TestProxyStart_DefaultsToSessionProject(t *testing.T) {
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))

	a := bindConn(t, sockPath, "sess-a", dirA)
	_, err := a.Request("PROXY", "START", "dev", ephemeralTargetURL(t), "0").JSON()
	require.NoError(t, err)

	p, ok := d.exactProxy("dev")
	require.True(t, ok)
	require.Equal(t, dirA, normalizePath(p.Path))
}

// RUN, like PROC RUN, refuses a path outside the bound session's project.
func TestRun_PathOfAnotherProjectIsRefused(t *testing.T) {
	_, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	a := bindConn(t, sockPath, "sess-a", dirA)
	err := a.Request("RUN").WithJSON(map[string]any{
		"command": "sleep", "args": []string{"30"}, "path": dirB, "mode": "background",
	}).OK()
	require.Error(t, err)
	require.Contains(t, err.Error(), "another project")
}

// A connection bound to a session that has since been retired is confined to
// nothing, not released into an unscoped lookup.
func TestPathVisibleTo_RetiredSessionFailsClosed(t *testing.T) {
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	_ = bindConn(t, sockPath, "sess-a", dirA)

	sess, ok := d.sessionRegistry.Get("sess-a")
	require.True(t, ok)
	d.sessionRegistry.UnregisterExact("sess-a", sess)

	b := NewConn(sockPath)
	defer b.Close()
	// Re-bind then retire out from under the live connection.
	_, err := b.Request("SESSION", "REGISTER", "sess-c").WithJSON(map[string]any{"project_path": dirA, "command": "test"}).JSON()
	require.NoError(t, err)
	sc, ok := d.sessionRegistry.Get("sess-c")
	require.True(t, ok)
	d.sessionRegistry.UnregisterExact("sess-c", sc)

	_, err = b.Request("PROXY", "START", "dev", ephemeralTargetURL(t), "0").JSON()
	require.Error(t, err, "a connection bound to a retired session must not start a proxy")
	require.Contains(t, err.Error(), "another project")
}
