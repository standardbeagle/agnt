package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// A Register blocked on a full monitor pool must end when its own registration
// is withdrawn; it used to wait on the daemon's context and stay blocked until
// shutdown or until another monitor happened to exit.
func TestAutoRestartRegister_UnregisterReleasesCapacityWait(t *testing.T) {
	r := NewProcessAutoRestarter(nil)
	t.Cleanup(r.cancel)
	require.True(t, r.monitorSem.TryAcquire(maxConcurrentMonitors), "fill the monitor pool")

	done := make(chan struct{})
	go func() {
		defer close(done)
		r.Register("proc", DefaultAutoRestartConfig(), "cmd", nil, nil, nil, "", "")
	}()

	require.Eventually(t, func() bool { return r.IsRegistered("proc") }, 5*time.Second, 5*time.Millisecond,
		"the registration is visible while it waits")
	r.Unregister("proc")

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Register is still blocked on capacity after Unregister")
	}
	require.False(t, r.IsRegistered("proc"))
}
