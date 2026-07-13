package sshclient

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
)

type ConnectionState string

const (
	ConnectionConnected    ConnectionState = "connected"
	ConnectionReconnecting ConnectionState = "reconnecting"
	ConnectionDisconnected ConnectionState = "disconnected"
)

// ClientStatus is the user-facing state behind `agnt ssh --status`.
type ClientStatus struct {
	Connection        ConnectionState
	ReconnectAttempts int
	Forwards          []Mapping
	QueuedPushes      int
	SessionName       string
}

// StatusTracker keeps connection counters independent from transport-owned
// forward and push snapshots, which callers supply when rendering.
type StatusTracker struct {
	mu                sync.RWMutex
	connection        ConnectionState
	reconnectAttempts int
	sessionName       string
}

func NewStatusTracker(sessionName string) *StatusTracker {
	return &StatusTracker{connection: ConnectionConnected, sessionName: sessionName}
}

// Observe consumes the Reconnector's stable status messages.
func (t *StatusTracker) Observe(message string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if strings.Contains(message, "reconnected") {
		t.connection = ConnectionConnected
		return
	}
	const marker = "reconnecting (attempt "
	if i := strings.Index(message, marker); i >= 0 {
		rest := message[i+len(marker):]
		if end := strings.IndexByte(rest, ','); end >= 0 {
			if attempt, err := strconv.Atoi(rest[:end]); err == nil {
				t.reconnectAttempts = attempt
			}
		}
		t.connection = ConnectionReconnecting
	}
}

func (t *StatusTracker) Disconnected() {
	t.mu.Lock()
	t.connection = ConnectionDisconnected
	t.mu.Unlock()
}

func (t *StatusTracker) Snapshot(forwards []Mapping, queuedPushes int) ClientStatus {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return ClientStatus{
		Connection:        t.connection,
		ReconnectAttempts: t.reconnectAttempts,
		Forwards:          append([]Mapping(nil), forwards...),
		QueuedPushes:      queuedPushes,
		SessionName:       t.sessionName,
	}
}

func FormatClientStatus(status ClientStatus) string {
	var b strings.Builder
	fmt.Fprintf(&b, "agnt ssh status: %s\n", status.Connection)
	fmt.Fprintf(&b, "  session: %s\n", status.SessionName)
	fmt.Fprintf(&b, "  reconnect attempts: %d\n", status.ReconnectAttempts)
	fmt.Fprintf(&b, "  queued pushes: %d\n", status.QueuedPushes)
	fmt.Fprintln(&b, "  active forwards:")
	forwards := append([]Mapping(nil), status.Forwards...)
	sort.Slice(forwards, func(i, j int) bool { return forwards[i].ProxyID < forwards[j].ProxyID })
	if len(forwards) == 0 {
		fmt.Fprintln(&b, "    (none)")
	} else {
		for _, mapping := range forwards {
			fmt.Fprintf(&b, "    %-24s remote :%-5d -> http://127.0.0.1:%d\n", mapping.ProxyID, mapping.RemotePort, mapping.LocalPort)
		}
	}
	return b.String()
}

type Splash struct {
	Host          string
	LocalVersion  string
	RemoteVersion string
	Project       string
	Session       string
	DetachHint    string
}

func FormatSplash(s Splash) string {
	return fmt.Sprintf("agnt ssh → %s\n  versions: local %s · remote %s\n  project: %s\n  session: %s\n  detach: %s\n\n",
		s.Host, s.LocalVersion, s.RemoteVersion, s.Project, s.Session, s.DetachHint)
}

func TerminalTitle(host, session string) string {
	return fmt.Sprintf("\x1b]0;agnt ssh %s · %s\x07", host, session)
}
