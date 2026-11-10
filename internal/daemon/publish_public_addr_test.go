package daemon

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// PublicPlaneAddr is the public plane's "is it listening" answer. It kept the
// bound address after the server stopped serving (shutdown, or Serve
// failing), so it reported a listener that no longer existed, and a Serve
// failure reached only the debug log. When Serve returns, the address is
// cleared.
func TestPublicPlaneAddr_ClearedWhenTheServerStops(t *testing.T) {
	d := newBootedDaemonWithConfig(t, DaemonConfig{
		SocketPath:       shortSockPath(t),
		PublicListenAddr: "127.0.0.1:0",
	})
	require.NotEmpty(t, d.PublicPlaneAddr(), "public plane must be listening")
	require.NoError(t, d.publicServer.Close())
	require.Eventually(t, func() bool { return d.PublicPlaneAddr() == "" }, 5*time.Second, 10*time.Millisecond,
		"a stopped public plane must not report an address")
}
