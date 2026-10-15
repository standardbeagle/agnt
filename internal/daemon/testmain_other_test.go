//go:build !unix

package daemon

import (
	"os"
	"testing"
)

// TestMain on non-Unix hosts only adds the fake tunnel provider re-exec; the
// Unix TestMain (daemon_session_containment_test.go) also runs goleak and the
// port-holder helper.
func TestMain(m *testing.M) {
	if mode := os.Getenv(fakeTunnelProviderEnv); mode != "" {
		os.Exit(runFakeTunnelProvider(mode))
	}
	os.Exit(m.Run())
}
