package daemon

import (
	"path/filepath"
	"testing"

	"github.com/standardbeagle/agnt/internal/daemonclient"

	"github.com/stretchr/testify/assert"
)

// The PID tracker records managed processes and their descendants keyed by
// process ID, and process IDs are not globally unique — "client", "server" and
// "dev" recur in every project. Two daemons sharing one tracker file therefore
// share a keyspace, and one daemon's cleanup can kill a live process belonging
// to the other.
func TestPIDTrackerPath_IsPerDaemon(t *testing.T) {
	a := pidTrackerPathFor("/tmp/agnt-a/agnt.sock")
	b := pidTrackerPathFor("/tmp/agnt-b/agnt.sock")

	assert.NotEmpty(t, a)
	assert.NotEqual(t, a, b, "two daemons on different sockets must not share a PID tracker file")
	assert.Equal(t, "/tmp/agnt-a", filepath.Dir(a), "the tracker lives beside its socket")
}

// A daemon on the default socket keeps the historical, AppName-derived path, so
// an upgraded daemon still finds the orphans its predecessor left behind.
func TestPIDTrackerPath_DefaultSocketKeepsLibraryDefault(t *testing.T) {
	assert.Empty(t, pidTrackerPathFor(daemonclient.DefaultSocketPath()),
		"default socket should defer to the library's XDG-derived path")
	assert.Empty(t, pidTrackerPathFor(""), "an unset socket path should defer too")
}

// Two test daemons must not collide: this is what made `sleep 60` processes die
// with "exited with code -1 during startup" when the suite ran under load.
func TestPIDTrackerPath_TestDaemonsGetDistinctFiles(t *testing.T) {
	d1 := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "one.sock")})
	d2 := NewForTest(t, DaemonConfig{SocketPath: filepath.Join(t.TempDir(), "two.sock")})

	p1 := pidTrackerPathFor(d1.config.SocketPath)
	p2 := pidTrackerPathFor(d2.config.SocketPath)

	assert.NotEmpty(t, p1)
	assert.NotEqual(t, p1, p2, "concurrent test daemons shared a PID tracker file")
}
