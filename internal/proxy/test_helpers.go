package proxy

import "testing"

// MarkCrashedForTest puts ps in the state runServer leaves it in when Serve
// returns unexpectedly: still registered, no longer marked running. For
// other packages' tests that need a crashed proxy; the *testing.T parameter
// keeps it out of production use.
func MarkCrashedForTest(t *testing.T, ps *ProxyServer) {
	t.Helper()
	ps.running.Store(false)
}
