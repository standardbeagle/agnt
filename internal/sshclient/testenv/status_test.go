package testenv_test

import (
	"strings"
	"testing"
	"time"

	"github.com/standardbeagle/agnt/internal/sshclient"
	"github.com/standardbeagle/agnt/internal/sshclient/testenv"
	"golang.org/x/crypto/ssh"
)

// TestRemoteStatusSurfaceTracksFixtureConnection drives the status surface
// against a real in-process SSH transport. Synchronization is event-based;
// the timeout is hang protection, not an elapsed-time assertion.
func TestRemoteStatusSurfaceTracksFixtureConnection(t *testing.T) {
	auth, err := testenv.NewAuth("status-user")
	if err != nil {
		t.Fatal(err)
	}
	server, err := testenv.Start(auth)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	client, err := ssh.Dial("tcp", server.Addr(), auth.ClientConfig())
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()

	tracker := sshclient.NewStatusTracker("fixture-session")
	connected := tracker.Snapshot([]sshclient.Mapping{{ProxyID: "fixture-web", RemotePort: 5173, LocalPort: 5174}}, 2)
	view := sshclient.FormatClientStatus(connected)
	for _, want := range []string{"connected", "fixture-session", "fixture-web", "queued pushes: 2"} {
		if !strings.Contains(view, want) {
			t.Fatalf("connected fixture status missing %q:\n%s", want, view)
		}
	}

	waited := make(chan error, 1)
	go func() { waited <- client.Wait() }()
	server.Drop()
	select {
	case <-waited:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture transport did not report its drop")
	}

	tracker.Disconnected()
	tracker.Observe("agnt ssh: reconnecting (attempt 1, retrying in 1s)...")
	reconnecting := tracker.Snapshot(nil, 2)
	if reconnecting.Connection != sshclient.ConnectionReconnecting || reconnecting.ReconnectAttempts != 1 {
		t.Fatalf("reconnecting fixture status = %+v", reconnecting)
	}
}
