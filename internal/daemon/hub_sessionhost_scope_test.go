//go:build unix

package daemon

import (
	"encoding/base64"
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

// bindConn opens a connection and binds it to a session of projectPath, the
// way an `agnt run` wrapper does.
func bindConn(t *testing.T, sockPath, code, projectPath string) *Conn {
	t.Helper()
	c := NewConn(sockPath)
	t.Cleanup(func() { _ = c.Close() })
	_, err := c.Request(protocol.VerbSession, protocol.SubVerbRegister, code).WithJSON(protocol.SessionRegisterConfig{
		ProjectPath: projectPath,
		Command:     "test",
	}).JSON()
	require.NoError(t, err)
	return c
}

// A NOTICE that names no project used to match the first session of that name
// in any project and type into its PTY.
func TestSessionHostNotice_RequiresProject(t *testing.T) {
	// No t.Parallel(): spawns a real PTY child.
	d, sockPath := newSessionHostTestDaemon(t)
	c := NewConn(sockPath)
	defer c.Close()

	var created protocol.SessionHostCreateResult
	require.NoError(t, c.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
		Name: "claude", ProjectPath: t.TempDir(), Command: "sh", Args: []string{"-c", "cat"},
	}).JSONInto(&created))
	defer c.Request("SESSION-HOST", "KILL", created.SessionID).OK()

	err := c.Request("SESSION-HOST", "NOTICE").WithJSON(map[string]string{
		"session_name": "claude", "message": "must not reach another project",
	}).OK()
	require.Error(t, err, "a notice with no project_path must be refused")

	s, ok := d.sessionHosts.Get(created.SessionID)
	require.True(t, ok)
	require.Zero(t, s.AttachedCount())
}

// A connection bound to project A must not see, drive or kill a session-host
// session of project B, and must still manage its own.
func TestSessionHost_VerbsConfinedToBoundProject(t *testing.T) {
	// No t.Parallel(): spawns real PTY children.
	d, sockPath := newSessionHostTestDaemon(t)
	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))

	admin := NewConn(sockPath) // unbound: names its target explicitly
	defer admin.Close()
	var inA, inB protocol.SessionHostCreateResult
	for dir, out := range map[string]*protocol.SessionHostCreateResult{dirA: &inA, dirB: &inB} {
		require.NoError(t, admin.Request("SESSION-HOST", "CREATE").WithJSON(protocol.SessionHostCreateConfig{
			ProjectPath: dir, Command: "sh", Args: []string{"-c", "cat"},
		}).JSONInto(out))
	}
	defer admin.Request("SESSION-HOST", "KILL", inA.SessionID).OK()
	defer admin.Request("SESSION-HOST", "KILL", inB.SessionID).OK()

	a := bindConn(t, sockPath, "sess-a", dirA)

	require.Error(t, a.Request("SESSION-HOST", "KILL", inB.SessionID).OK(), "KILL of another project's session")
	require.Error(t, a.Request("SESSION-HOST", "RESIZE", inB.SessionID).WithJSON(protocol.SessionHostResizeConfig{Cols: 100, Rows: 30}).OK(), "RESIZE of another project's session")
	require.Error(t, a.Request("SESSION-HOST", "STDIN", inB.SessionID).WithJSON(map[string]string{
		"attach_id": "x", "data": base64.StdEncoding.EncodeToString([]byte("x")),
	}).OK(), "STDIN to another project's session")
	require.Error(t, a.Request("SESSION-HOST", "DETACH", inB.SessionID).WithJSON(map[string]string{"attach_id": "x"}).OK(), "DETACH on another project's session")
	_, stillThere := d.sessionHosts.Get(inB.SessionID)
	require.True(t, stillThere, "the refused KILL must not have removed project B's session")

	require.NoError(t, a.Request("SESSION-HOST", "RESIZE", inA.SessionID).WithJSON(protocol.SessionHostResizeConfig{Cols: 100, Rows: 30}).OK(), "own session stays manageable")
	require.NoError(t, a.Request("SESSION-HOST", "KILL", inA.SessionID).OK())
}
