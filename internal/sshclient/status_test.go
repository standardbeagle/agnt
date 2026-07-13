package sshclient

import (
	"strings"
	"testing"
)

func TestStatusTrackerConnectionAttemptsAndSurfaces(t *testing.T) {
	tracker := NewStatusTracker("agent-work")
	tracker.Observe("agnt ssh: reconnecting (attempt 2, retrying in 1s)...")
	status := tracker.Snapshot([]Mapping{{ProxyID: "web", RemotePort: 5173, LocalPort: 5174}}, 3)

	if status.Connection != ConnectionReconnecting || status.ReconnectAttempts != 2 {
		t.Fatalf("connection status = %+v", status)
	}
	if status.SessionName != "agent-work" || status.QueuedPushes != 3 || len(status.Forwards) != 1 {
		t.Fatalf("client surfaces = %+v", status)
	}

	tracker.Observe("agnt ssh: reconnected")
	if got := tracker.Snapshot(nil, 0); got.Connection != ConnectionConnected || got.ReconnectAttempts != 2 {
		t.Fatalf("reconnected status = %+v", got)
	}
}

func TestFormatClientStatusIncludesStableSortedTable(t *testing.T) {
	got := FormatClientStatus(ClientStatus{
		Connection: ConnectionConnected, SessionName: "shell", ReconnectAttempts: 1, QueuedPushes: 2,
		Forwards: []Mapping{
			{ProxyID: "z-api", RemotePort: 8080, LocalPort: 8081},
			{ProxyID: "a-web", RemotePort: 5173, LocalPort: 5173},
		},
	})
	for _, want := range []string{"connected", "session: shell", "reconnect attempts: 1", "queued pushes: 2", "remote :5173", "remote :8080"} {
		if !strings.Contains(got, want) {
			t.Errorf("status missing %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "a-web") > strings.Index(got, "z-api") {
		t.Fatalf("forward table not sorted:\n%s", got)
	}
}

func TestSplashAndTitleExposeRemoteContext(t *testing.T) {
	splash := FormatSplash(Splash{Host: "devbox", LocalVersion: "1.2.3", RemoteVersion: "1.2.4", Project: "/srv/app", Session: "api", DetachHint: "Ctrl-\\ Ctrl-\\"})
	for _, want := range []string{"devbox", "local 1.2.3", "remote 1.2.4", "/srv/app", "session: api", "Ctrl-\\ Ctrl-\\"} {
		if !strings.Contains(splash, want) {
			t.Errorf("splash missing %q:\n%s", want, splash)
		}
	}
	if got := TerminalTitle("devbox", "api"); got != "\x1b]0;agnt ssh devbox · api\x07" {
		t.Fatalf("terminal title = %q", got)
	}
}
