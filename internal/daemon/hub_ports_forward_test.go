package daemon

import (
	"testing"

	"github.com/standardbeagle/agnt/internal/protocol"
	"github.com/stretchr/testify/require"
)

func TestForwardMappingSnapshotReplacementRemovesStalePorts(t *testing.T) {
	d := &Daemon{}
	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{{ProxyID: "old", RemotePort: 5173, LocalPort: 5174}})
	require.Contains(t, d.forwardMappingsByRemotePort(), 5173)

	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{{ProxyID: "new", RemotePort: 8080, LocalPort: 8080}})
	got := d.forwardMappingsByRemotePort()
	require.NotContains(t, got, 5173)
	require.Equal(t, "new", got[8080].ProxyID)

	d.forwardMappings.Store("ssh:fixture", []protocol.ForwardMapping{})
	require.Empty(t, d.forwardMappingsByRemotePort())
}
