package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A connection bound to project A must not read, message, schedule against or
// cancel project B's session or tasks by code/id.
func TestSessionVerbs_BoundConnectionCannotReachOtherProject(t *testing.T) {
	d, c, _ := newBootedDaemonWithClient(t)

	dirA := normalizePath(shortTempDir(t))
	dirB := normalizePath(shortTempDir(t))
	_, err := c.SessionRegister("sess-a", "http://127.0.0.1:19191", dirA, "test", nil)
	require.NoError(t, err)
	activeSession(t, d.sessionRegistry, "sess-b", dirB, "http://127.0.0.1:29292")

	_, err = c.SessionGet("sess-b")
	require.Error(t, err, "SESSION GET across projects must be refused")
	_, err = c.SessionSend("sess-b", "hi")
	require.Error(t, err, "SESSION SEND across projects must be refused")
	_, err = c.SessionSchedule("sess-b", "1h", "hi")
	require.Error(t, err, "SESSION SCHEDULE across projects must be refused")

	task, err := d.scheduler.Schedule("sess-b", time.Hour, "x", dirB)
	require.NoError(t, err)
	require.Error(t, c.SessionCancel(task.ID), "SESSION CANCEL of another project's task must be refused")

	_, err = c.SessionGet("sess-a")
	require.NoError(t, err, "own session stays reachable")
}
