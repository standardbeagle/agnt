package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newRoutableTestClient starts a daemon on an ephemeral socket and returns a
// connected client.
func newRoutableTestClient(t *testing.T) (*Daemon, *daemonclient.Client) {
	t.Helper()
	sock := shortSockPath(t)
	d := NewForTest(t, DaemonConfig{SocketPath: sock, MaxClients: 4, WriteTimeout: 5 * time.Second})

	c := daemonclient.NewClient(daemonclient.WithSocketPath(sock))
	require.NoError(t, c.Connect())
	t.Cleanup(func() { _ = c.Close() })
	return d, c
}

// TestAutostartReconcile_RoundTrip drives AUTOSTART RECONCILE over the real wire.
// The sub-verb was registered on the router but not on the command, so the
// parser left "RECONCILE" in Args and the daemon answered "unknown action" —
// live `.agnt.kdl` reconcile never ran.
func TestAutostartReconcile_RoundTrip(t *testing.T) {
	_, c := newRoutableTestClient(t)

	project := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(project, ".agnt.kdl"), []byte("scripts {\n}\n"), 0o644))

	res, err := c.AutostartReconcile(project)
	require.NoError(t, err, "AUTOSTART RECONCILE must dispatch to its handler")
	assert.NotNil(t, res)
	// The plan keys prove hubHandleAutostartReconcile ran, not the default branch.
	assert.Contains(t, res, "start_scripts")
	assert.Contains(t, res, "stop_scripts")
	assert.Contains(t, res, "restart_scripts")
}

// TestAutostartReconcile_MissingProjectPathFailsLoud: the handler reads the path
// from Args[0]; with the sub-verb unregistered it used to read "RECONCILE".
func TestAutostartReconcile_MissingProjectPathFailsLoud(t *testing.T) {
	_, c := newRoutableTestClient(t)
	_, err := c.AutostartReconcile("")
	require.Error(t, err, "an empty project path must be rejected, not silently reconciled")
}

// TestOverlayForwarding_RoundTrip drives OVERLAY FORWARDING over the real wire.
// Unregistered, it fell through to OVERLAY's "" default alias (GET), so pausing
// agent-inbound push silently did nothing and returned an overlay endpoint.
func TestOverlayForwarding_RoundTrip(t *testing.T) {
	d, c := newRoutableTestClient(t)

	project := t.TempDir()
	_, err := c.SessionRegister("sess-fwd", "", project, "bash", nil)
	require.NoError(t, err)

	require.NoError(t, c.SetForwarding(true), "OVERLAY FORWARDING must dispatch to its handler")
	assert.True(t, d.IsForwardingPaused("sess-fwd"), "push is actually paused, not just acknowledged")

	require.NoError(t, c.SetForwarding(false))
	assert.False(t, d.IsForwardingPaused("sess-fwd"))
}

// Reaching the real handler means its session gate applies. Before the fix this
// silently returned the overlay endpoint from the "" alias and reported success.
func TestOverlayForwarding_WithoutSessionFailsLoud(t *testing.T) {
	_, c := newRoutableTestClient(t)
	err := c.SetForwarding(true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no session attached")
}
