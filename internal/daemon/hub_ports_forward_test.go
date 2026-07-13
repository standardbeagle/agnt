package daemon

import (
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestForwardMappingSnapshotReplacementRemovesStalePorts(t *testing.T) {
	d := &Daemon{sessionRegistry: NewSessionRegistry(time.Minute)}
	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{{ProxyID: "old", RemotePort: 5173, LocalPort: 5174}})
	require.Contains(t, d.forwardMappingsByRemotePort(), 5173)

	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{{ProxyID: "new", RemotePort: 8080, LocalPort: 8080}})
	got := d.forwardMappingsByRemotePort()
	require.NotContains(t, got, 5173)
	require.Equal(t, "new", got[8080].ProxyID)

	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{})
	require.Empty(t, d.forwardMappingsByRemotePort())
}

func TestForwardMappingsCleanedWithOwningSession(t *testing.T) {
	d := &Daemon{sessionRegistry: NewSessionRegistry(time.Minute)}
	d.forwardMappings.Store("ssh-forward-fixture", []protocol.ForwardMapping{{ProxyID: "web", RemotePort: 5173, LocalPort: 5174}})
	d.CleanupSessionResources("ssh-forward-fixture")
	require.Empty(t, d.forwardMappingsByRemotePort())
}
