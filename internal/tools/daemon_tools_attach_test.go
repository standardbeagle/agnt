package tools

import (
	"os"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemon"
	"github.com/standardbeagle/agnt/internal/daemonclient"
	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// attachTestDaemon starts a daemon with one session registered for the test's
// working directory, and returns DaemonTools pointed at it plus the socket.
func attachTestDaemon(t *testing.T) (*DaemonTools, string) {
	t.Helper()
	// Auto-attach resolves the session from the process cwd; point it at an
	// empty project so no real .agnt.kdl is in play.
	// Best-effort removal rather than t.TempDir(): on Windows the daemon can
	// still hold a handle under the project when cleanup runs, and a failed
	// RemoveAll would fail a test that is not about temp files.
	project, err := os.MkdirTemp("", "agnt-attach")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(project) })
	t.Chdir(project)

	sock := shortDemoSock(t)
	daemon.NewForTest(t, daemon.DaemonConfig{
		SocketPath:   sock,
		MaxClients:   10,
		WriteTimeout: 5 * time.Second,
	})

	owner := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	require.NoError(t, owner.Connect())
	t.Cleanup(func() { _ = owner.Close() })
	_, err = owner.SessionRegister("attach-test", "-", project, "test", nil)
	require.NoError(t, err)

	// version "" skips the client/daemon version check (no auto-upgrade path).
	dt := NewDaemonTools(daemonclient.AutoStartConfig{SocketPath: sock}, "")
	t.Cleanup(func() { _ = dt.Close() })
	return dt, sock
}

// connectionIsBound reports whether the daemon resolves a project for this
// connection with no explicit scope — i.e. whether it carries a session
// binding. This is the lookup publish and PROC RUN rely on.
func connectionIsBound(c *daemonclient.Client) bool {
	_, err := c.ProcList(protocol.DirectoryFilter{})
	return err == nil
}

// After the client is reset the next connection is a new one on the daemon's
// side. The wrapper used to keep attachAttempted=true and its old session
// code, so it believed it was attached while the daemon had no binding.
func TestDaemonTools_ReattachesAfterClientReset(t *testing.T) {
	dt, _ := attachTestDaemon(t)

	require.NoError(t, dt.ensureConnected())
	require.Equal(t, "attach-test", dt.SessionCode())
	require.True(t, connectionIsBound(dt.client.Client()))

	dt.resetClient()
	assert.Empty(t, dt.SessionCode(), "a dropped connection must not leave the wrapper claiming an attachment")

	require.NoError(t, dt.ensureConnected())
	assert.Equal(t, "attach-test", dt.SessionCode())
	assert.True(t, connectionIsBound(dt.client.Client()),
		"the new connection must be bound to the session on the daemon's side")
}

// reattach is the ResilientClient OnReconnect hook: it receives the freshly
// established connection, which starts with no session binding.
func TestDaemonTools_ReattachBindsReconnectedConnection(t *testing.T) {
	dt, sock := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())

	fresh := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	require.NoError(t, fresh.Connect())
	t.Cleanup(func() { _ = fresh.Close() })
	require.False(t, connectionIsBound(fresh), "precondition: a new connection is unbound")

	require.NoError(t, dt.reattach(fresh))
	assert.True(t, connectionIsBound(fresh))
	assert.Equal(t, "attach-test", dt.SessionCode())
}

// With no session left to attach to (daemon restarted, session ended), the
// stale code is dropped and the next tool call gets to try again.
func TestDaemonTools_ReattachDropsStaleSession(t *testing.T) {
	dt, sock := attachTestDaemon(t)
	require.NoError(t, dt.ensureConnected())

	// Move to a directory no session covers: from the wrapper's side this is
	// what a restarted daemon or an ended session looks like, without racing
	// the daemon's asynchronous session retirement.
	elsewhere, err := os.MkdirTemp("", "agnt-attach-none")
	require.NoError(t, err)
	t.Cleanup(func() { _ = os.RemoveAll(elsewhere) })
	t.Chdir(elsewhere)

	other := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	require.NoError(t, other.Connect())
	t.Cleanup(func() { _ = other.Close() })
	require.NoError(t, dt.reattach(other), "a failed re-attach must not fail the reconnect")
	assert.Empty(t, dt.SessionCode())
	dt.sessionMu.Lock()
	attempted := dt.attachAttempted
	dt.sessionMu.Unlock()
	assert.False(t, attempted, "the next tool call must retry the attach")
}
