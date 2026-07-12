//go:build !windows

package main

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/sshclient"
)

// TestReconnectControl_ThreeDropSoakKeepsPushOwnerAlive complements the
// control-protocol soak in internal/sshclient: the real agnt ssh lifecycle
// drives the same durable queue through three Pause/Resume cycles. The
// replacement clients intentionally have no live SSH transport; Resume must
// therefore stay lazy and must not attempt to open SFTP until a push arrives.
func TestReconnectControl_ThreeDropSoakKeepsPushOwnerAlive(t *testing.T) {
	queue := sshclient.NewPushQueue("/remote/project", 4, nil, nil)
	control := &reconnectControl{queue: queue}
	t.Cleanup(control.Stop)

	for drop := 1; drop <= 3; drop++ {
		control.Pause()
		if depth := queue.Depth(); depth != 0 {
			t.Fatalf("drop %d queue depth = %d, want 0", drop, depth)
		}
		// A nil SSH transport would panic if Resume opened SFTP eagerly.
		control.Resume(&sshclient.Client{})
	}
}
